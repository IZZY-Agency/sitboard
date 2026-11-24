package shellcmd

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/cpucost"
)

func checkNames(t *testing.T, cases map[string][]string) {
	t.Helper()
	for cmd, want := range cases {
		if got := Parse(cmd).SpawnNames(); !slices.Equal(got, want) {
			t.Errorf("spawnNames(%q) = %q, want %q", cmd, got, want)
		}
	}
}

// A spawn written inside the script of `bash -c`, `sh -c` or `zsh -c` counts
// like a direct one. One level only.
func TestSpawnNamesInsideShellScript(t *testing.T) {
	checkNames(t, map[string][]string{
		`bash -c 'agtermctl session new --name worker-a --no-select'`:                            {"worker-a"},
		`sh -c "agtermctl session new --name \"worker b\""`:                                      {"worker b"},
		`zsh -lc 'cd /x && agtermctl session new --name=worker-c'`:                               {"worker-c"},
		`bash -l -c 'X=1; agtermctl session new --name worker-d; date -u'`:                       {"worker-d"},
		`bash -o pipefail -c 'agtermctl session new --name worker-p'`:                            {"worker-p"},
		`bash --login -c 'agtermctl session new --name worker-l'`:                                {"worker-l"},
		`bash --rcfile rc.sh -c 'agtermctl session new --name worker-rc'`:                        {"worker-rc"},
		`bash -c -- 'agtermctl session new --name worker-dd'`:                                    {"worker-dd"},
		`agtermctl session new --name worker-f; bash -c 'agtermctl session new --name worker-g'`: {"worker-f", "worker-g"},
		`bash -c 'WS=$(agtermctl tree --json | head -1); agtermctl session new --name worker-h'`: {"worker-h"},
		`bash -c 'agtermctl session new --name worker-r' && bash -c 'true'`:                      {"worker-r"},
		`cd /x && /bin/bash -c 'export PATH=/opt/bin:$PATH; ls | tail -2; agtermctl session new --workspace W --name worker-e --command "/bin/zsh run.sh e"; date -u'`: {"worker-e"},
		// Not spawns.
		`echo bash -c 'agtermctl session new --name worker-x'`:      nil, // bash is an argument
		`bash run.sh 'agtermctl session new --name worker-y'`:       nil, // no -c
		`bash -c 'echo agtermctl session new --name worker-z'`:      nil, // agtermctl is an argument
		`bash -c 'bash -c "agtermctl session new --name worker-n"'`: nil, // two levels: not read
		`bash -c`:    nil,
		`bash -c ''`: nil,
		`bash -c 'agtermctl session new --name "$N"'`:                      nil, // a variable is not resolved
		`grep "bash -c 'agtermctl session new --name worker-q'" notes.txt`: nil,
		`bash -o ; 'agtermctl session new --name worker-o'`:                nil, // -o does not take the separator
	})
}

// A command after a shell keyword (if, then, else, elif, while, until, do,
// {, !) is at a command position, so a spawn in a loop or a branch is seen. A
// name built from a variable stays unknown; the printed terminal ids are
// recorded.
func TestSpawnAfterShellKeyword(t *testing.T) {
	checkNames(t, map[string][]string{
		`if true; then agtermctl session new --name worker-a; fi`:                        {"worker-a"},
		`if false; then :; else agtermctl session new --name worker-b; fi`:               {"worker-b"},
		`if false; then :; elif agtermctl session new --name worker-ei; then :; fi`:      {"worker-ei"},
		`if agtermctl session new --name worker-if; then :; fi`:                          {"worker-if"},
		`while agtermctl session new --name worker-wh; do break; done`:                   {"worker-wh"},
		`until agtermctl session new --name worker-un; do :; done`:                       {"worker-un"},
		`time agtermctl session new --name worker-t`:                                     {"worker-t"},
		`time -p agtermctl session new --name worker-tp`:                                 {"worker-tp"},
		`{ agtermctl session new --name worker-c; }`:                                     {"worker-c"},
		`! agtermctl session new --name worker-d`:                                        {"worker-d"},
		`echo do agtermctl session new --name worker-x`:                                  nil, // "do" is an argument here
		`for n in a b agtermctl session new --name worker-in; do :; done`:                nil,
		`for n in a b; do agtermctl session new --name "worker-$n" --no-select; done`:    nil,
		"for n in a b\ndo\n  agtermctl session new --name worker-nl\ndone":               {"worker-nl"},
		`if true; then X=1 agtermctl session new --name worker-as; fi`:                   {"worker-as"},
		`echo if agtermctl session new --name worker-y; echo then agtermctl session new`: nil,
	})

}

// A known wrapper and its options come before the command it runs.
func TestSpawnAfterWrapper(t *testing.T) {
	checkNames(t, map[string][]string{
		`env X=1 agtermctl session new --name worker-env`:                   {"worker-env"},
		`env -i PATH=/x /opt/bin/agtermctl session new --name worker-envi`:  {"worker-envi"},
		`env -u HOME agtermctl session new --name worker-envu`:              {"worker-envu"},
		`sudo -u dev agtermctl session new --name worker-sudo`:              {"worker-sudo"},
		`exec agtermctl session new --name worker-exec`:                     {"worker-exec"},
		`command agtermctl session new --name worker-cmd`:                   {"worker-cmd"},
		`nohup agtermctl session new --name worker-nohup`:                   {"worker-nohup"},
		`timeout 30 agtermctl session new --name worker-to`:                 {"worker-to"},
		`timeout -s KILL 5s agtermctl session new --name worker-tos`:        {"worker-tos"},
		`nohup env X=1 timeout 9 agtermctl session new --name worker-chain`: {"worker-chain"},
		`command -v agtermctl session new --name worker-v`:                  nil, // only looks the program up
		`echo env agtermctl session new --name worker-e`:                    nil,
		`timeout agtermctl session new --name worker-dur`:                   nil, // agtermctl is the duration here
	})
}

// A heredoc body is data, not commands, whether its delimiter is quoted or
// not; a command after the body is read again.
func TestHeredocBodiesAreNotCommands(t *testing.T) {
	notRun := []string{
		"cat > spawn.sh <<'EOF'\nfor n in a b; do agtermctl session new --name worker-a; done\nEOF",
		"cat > run.sh <<'EOF'\nbash -lc 'agtermctl session new --name worker-c'\nEOF",
		"cat > ship.sh <<'EOF'\nif git push; then gh pr create --fill; fi\nEOF",
		"bash -c 'cat <<EOF\nagtermctl session new --name worker-x\nEOF'",
		"cat <<EOF\nagtermctl session new --name worker-y\ngh pr create --fill\nEOF",
		"cat <<\"EOF\"\nagtermctl session new --name worker-q\nEOF",
		"cat <<-EOF\n\tagtermctl session new --name worker-tab\n\tEOF",
		"cat <<EOF\nagtermctl session new --name worker-open", // never closed: the rest is body
	}
	for _, cmd := range notRun {
		c := Parse(cmd)
		if n := c.SpawnNames(); n != nil || len(c.Runs("agtermctl", "session", "new")) > 0 || len(c.Runs("gh", "pr", "create")) > 0 {
			t.Errorf("%q: read a heredoc body as a command (names %q)", cmd, n)
		}
	}
	checkNames(t, map[string][]string{
		"cat > f <<'EOF'\nagtermctl session new --name inside\nEOF\nagtermctl session new --name after":                             {"after"},
		"cat <<-EOF\n\tbody\n\tEOF\nagtermctl session new --name after-tab":                                                         {"after-tab"},
		"cat <<A <<B\nagtermctl session new --name a\nA\nagtermctl session new --name b\nB\nagtermctl session new --name after-two": {"after-two"},
		"cat <<< here; agtermctl session new --name here-string":                                                                    {"here-string"},
		`echo "<<EOF"; agtermctl session new --name quoted`:                                                                         {"quoted"},
		"cat <<EOF; agtermctl session new --name same-line\nbody\nEOF":                                                              {"same-line"},
	})
}

// An unquoted # at the start of a word begins a comment.
func TestCommentsAreSkipped(t *testing.T) {
	if c := Parse(`ls # later; gh pr create`); len(c.Runs("gh", "pr", "create")) > 0 {
		t.Error("a comment read as a command")
	}
	checkNames(t, map[string][]string{
		`# agtermctl session new --name c`:                        nil,
		"# note\nagtermctl session new --name next-line":          {"next-line"},
		`echo a#b; agtermctl session new --name mid-word`:         {"mid-word"},
		`echo '# x'; agtermctl session new --name quoted-hash`:    {"quoted-hash"},
		"agtermctl session new --name before # --name not-a-name": {"before"},
	})
}

// A raw NUL inside quotes is text, never a separator.
func TestNULIsNotASeparator(t *testing.T) {
	checkNames(t, map[string][]string{
		"echo '\x00;' agtermctl session new --name worker-nul":  nil,
		"echo \"\x00;\" agtermctl session new --name worker-n2": nil,
	})
}

// Parsing is linear. Each input's CPU time is taken at size n and 4n (the
// least of five runs, so a loaded machine does not count): a linear parser
// takes about 4 times as long, a quadratic one 16 times, so a limit of 8
// times holds against noise from the race detector and still catches a
// quadratic step. Word-heavy inputs go
// up to about 60,000 words (under the word cap); byte-heavy ones up to about
// 4 MiB, the longest transcript line read.
func TestParsingIsLinear(t *testing.T) {
	const words, bytes = 15000, 1 << 20
	rep := func(unit string, n int) string { return strings.Repeat(unit, max(1, n)) }
	inputs := map[string]func(scale int) string{
		"assignments named like bash": func(k int) string { return rep("X=/bash ", k*words) },
		"assignments named like gh":   func(k int) string { return rep("X=/gh ", k*words) },
		"assignments like agtermctl":  func(k int) string { return rep("X=/agtermctl ", k*words) },
		"shells with -o and ;":        func(k int) string { return rep("-/bash -o ; ", k*words/3) },
		"keywords":                    func(k int) string { return rep("! ", k*words) + "agtermctl session new --name w" },
		"shell options":               func(k int) string { return "bash " + rep("-l ", k*words) },
		"wrapper options":             func(k int) string { return "sudo " + rep("--user x ", k*words/2) },
		"redirections":                func(k int) string { return "echo " + rep("2>&1 ", k*words) },
		"many scripts":                func(k int) string { return rep(`bash -c 'agtermctl session new --name w'; `, k*words/6) },
		"substitutions":               func(k int) string { return rep(`"$(echo "x")" `, k*words/3) },
		"many heredocs":               func(k int) string { return "cat " + rep("<<A ", k*words/2) + "\n" + rep("A\n", k*words/2) },
		"heredoc body": func(k int) string {
			return "cat <<EOF\n" + rep("agtermctl session new --name w \"x\"\n", k*bytes/40) + "EOF"
		},
		"one quoted word": func(k int) string { return `echo "` + rep("a", k*bytes) + `"` },
		"comments":        func(k int) string { return rep("# agtermctl session new\n", k*bytes/24) },
		"ansi-c string":   func(k int) string { return "echo $'" + rep(`\'`, k*bytes/2) + "'" },
		"arithmetic":      func(k int) string { return "echo $((" + rep("(1<<2)+", k*bytes/7) + "1))" },
		"deep nesting":    func(k int) string { return rep(`"$(`, k*bytes/3) },
		"backticks":       func(k int) string { return rep("`echo x` ", k*words/4) },
	}
	timeOf := func(in string) time.Duration {
		return cpucost.Of(5, func() {
			c := Parse(in)
			_ = c.Runs("gh", "pr", "create")
			_ = c.Runs("agtermctl", "session", "new")
			_ = c.SpawnNames()
		})
	}
	for name, gen := range inputs {
		t1, t4 := timeOf(gen(1)), timeOf(gen(4))
		t.Logf("%s: %s at n, %s at 4n", name, t1, t4)
		if t4 > 8*t1+5*time.Millisecond {
			t.Errorf("%s grows faster than linear: %s at n, %s at 4n", name, t1, t4)
		}
		if t4 > 10*time.Second {
			t.Errorf("%s took %s", name, t4)
		}
	}
}

// A script is untrusted text: parsing is bounded, and huge or malformed input
// neither panics nor hangs.
func TestShellScriptParsingIsBounded(t *testing.T) {
	many := strings.Repeat(`bash -c 'agtermctl session new --name w'; `, 20000)
	if got := Parse(many).SpawnNames(); len(got) > maxShellScripts {
		t.Errorf("read %d scripts, cap is %d", len(got), maxShellScripts)
	}
	huge := `bash -c 'agtermctl session new --name big; ` + strings.Repeat("x", maxShellScript) + `'`
	if got := Parse(huge).SpawnNames(); got != nil {
		t.Errorf("a script over the cap was read: %q", got)
	}
	late := strings.Repeat("a ", maxWords) + "; agtermctl session new --name late"
	if got := Parse(late).SpawnNames(); got != nil {
		t.Errorf("words past the cap were read: %q", got)
	}
	for _, bad := range []string{`bash -c '`, `bash -c "`, `bash -c '"'"'`, `sh -c \`, `bash -c "agtermctl session new --name 'w`, "bash -c '\x00;'", "cat <<", "cat <<'", "cat <<-", "<<<", "#", "bash -o", "timeout", "sudo -u", "env -u"} {
		c := Parse(bad)
		_ = c.SpawnNames()
		_ = c.Runs("gh", "pr", "create")
	}
}

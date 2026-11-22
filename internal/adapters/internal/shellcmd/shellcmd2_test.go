package shellcmd

import (
	"runtime"
	"strings"
	"testing"
)

// commitForm is the usual way to commit a multi-line message: a heredoc
// inside a command substitution inside double quotes.
func commitForm(line string) string {
	return "git commit -m \"$(cat <<'EOF'\nFix the thing\n\n" + line + "\nEOF\n)\""
}

func noRuns(t *testing.T, cmd string) {
	t.Helper()
	c := Parse(cmd)
	if n := c.SpawnNames(); n != nil || len(c.Runs("agtermctl", "session", "new")) > 0 || len(c.Runs("gh", "pr", "create")) > 0 {
		t.Errorf("%q: false detection (names %q)", cmd, n)
	}
}

// Finding 1: $(...) and backticks open a nested context, inside double
// quotes too; the commands inside are read, the quotes inside do not end the
// outer string.
func TestCommandSubstitution(t *testing.T) {
	for _, cmd := range []string{
		commitForm(`Document "x; agtermctl session new --name worker-a --no-select" usage`),
		commitForm(`- "(agtermctl session new --name worker-b)" example`),
		commitForm("Mention `agtermctl session new --name worker-bt` here"),
		`echo "$(printf "%s" ";agtermctl session new --name fake")"`,
		`echo "$(echo agtermctl session new --name arg)"`,
		"echo \"`echo \\\"; agtermctl session new --name fake-bt\\\"`\"",
	} {
		noRuns(t, cmd)
	}
	pr := commitForm(`Use "it's" form`) + " && git push && gh pr create --fill"
	if len(Parse(pr).Runs("gh", "pr", "create")) != 1 {
		t.Errorf("missed the pull request after a commit message: %q", pr)
	}
	checkNames(t, map[string][]string{
		`ID="$(agtermctl session new --name w --no-select)"`:               {"w"},
		"ID=`agtermctl session new --name wb --no-select`":                 {"wb"},
		`echo "id: $(agtermctl session new --name wq)"`:                    {"wq"},
		`echo $(agtermctl session new --name wu)`:                          {"wu"},
		`echo "$(echo "$(agtermctl session new --name deep)")"`:            {"deep"},
		`echo "$((1 + 2))"; agtermctl session new --name after-arith`:      {"after-arith"},
		`echo "$(echo ")")"; agtermctl session new --name after-paren`:     {"after-paren"},
		`X=$(agtermctl session new --name inner --cwd "$(pwd)") && echo x`: {"inner"},
		`agtermctl session new --name "$(cat name.txt)"`:                   nil, // a substitution is not a name
	})
}

// Finding 2: inside $'...' a backslash escapes the next character.
func TestANSICQuoting(t *testing.T) {
	noRuns(t, `echo $'it\'s; agtermctl session new --name fake-ansi'`)
	checkNames(t, map[string][]string{
		`echo $'a\\'; agtermctl session new --name after-ansi`: {"after-ansi"},
	})
}

// Finding 3: an empty quoted delimiter still opens a heredoc; its body ends
// at an empty line.
func TestEmptyHeredocDelimiter(t *testing.T) {
	noRuns(t, "cat <<\"\"\nagtermctl session new --name fake-empty")
	noRuns(t, "cat <<''\ngh pr create --fill")
	checkNames(t, map[string][]string{
		"cat <<\"\"\nagtermctl session new --name fake\n\nagtermctl session new --name after-empty": {"after-empty"},
	})
}

// Finding 4: << inside arithmetic is a shift, not a heredoc.
func TestShiftIsNotAHeredoc(t *testing.T) {
	checkNames(t, map[string][]string{
		"echo $((1 << 4))\nagtermctl session new --name after-shift":            {"after-shift"},
		"(( x = 1 << 4 ))\nagtermctl session new --name after-dparen":           {"after-dparen"},
		"if (( 1 << 2 )); then agtermctl session new --name in-if; fi":          {"in-if"},
		"echo \"$(( 2 << 1 ))\"\nagtermctl session new --name after-dq-shift":   {"after-dq-shift"},
		"(( n = $(agtermctl session new --name in-arith | wc -l) ))\necho done": {"in-arith"}, // a substitution inside runs
	})
}

// Finding 5: an option cluster with o or O takes the next word.
func TestClusteredShellOptions(t *testing.T) {
	checkNames(t, map[string][]string{
		`bash -euo pipefail -c 'agtermctl session new --name euo'`: {"euo"},
		`bash -eO extglob -c 'agtermctl session new --name eo'`:    {"eo"},
		`bash -ec 'agtermctl session new --name ec'`:               {"ec"},
	})
}

// Finding 6: wrapper forms, redirections and names next to a redirection.
func TestWrapperAndRedirectionEdges(t *testing.T) {
	checkNames(t, map[string][]string{
		`sudo --user u agtermctl session new --name su`:                     {"su"},
		`sudo -Eu dev agtermctl session new --name cluster`:                 {"cluster"},
		`timeout --signal KILL 5 agtermctl session new --name ts`:           {"ts"},
		`timeout --signal=KILL 5 agtermctl session new --name ts2`:          {"ts2"},
		`env -S 'agtermctl session new --name es'`:                          {"es"},
		`env -S'agtermctl session new --name es2'`:                          {"es2"},
		`env --split-string='agtermctl session new --name es3'`:             {"es3"},
		`time ! agtermctl session new --name tn`:                            {"tn"},
		`/usr/bin/time -o f agtermctl session new --name ut`:                {"ut"},
		`agtermctl session new --name w>/tmp/o`:                             {"w"},
		`agtermctl session new --name w2 2>&1`:                              {"w2"},
		`agtermctl session new --name="w3"</dev/null`:                       {"w3"},
		`>/dev/null agtermctl session new --name redirect-first`:            {"redirect-first"},
		`2>/dev/null X=1 agtermctl session new --name redirect-assign`:      {"redirect-assign"},
		`agtermctl session new --name w4 &>/dev/null; echo ok`:              {"w4"},
		`agtermctl session new --name w5 & agtermctl session new --name w6`: {"w5", "w6"},
		`sudo -l agtermctl session new --name sl`:                           nil,
		`sudo -v`: nil,
	})
	for _, cmd := range []string{`echo done >& gh pr create`, `echo done &> gh pr create`, `cat <& gh pr create`, `echo x >| gh pr create`} {
		noRuns(t, cmd)
	}
}

// Empty substitutions add no words, but each is counted against the word cap,
// so memory stays a small multiple of the input. Nested backticks copy their
// text once per level without doubling growth.
func TestSubstitutionMemoryIsBounded(t *testing.T) {
	const size = 4 << 20
	inputs := map[string]string{
		"empty backticks":          strings.Repeat("``", size/2),
		"empty $()":                strings.Repeat("$()", size/3),
		"empty process subst":      strings.Repeat("<()", size/3),
		"empty in double quotes":   `"` + strings.Repeat("$()``", size/5) + `"`,
		"nested backtick body":     strings.Repeat("`", 1) + strings.Repeat(`\`+"`", 7) + strings.Repeat("x", size) + strings.Repeat(`\`+"`", 7) + "`",
		"deep nested backticks x8": nestedBackticks(8, strings.Repeat("y", size)),
	}
	for name, in := range inputs {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		c := Parse(in)
		runtime.ReadMemStats(&after)
		_ = c.SpawnNames()
		alloc := after.TotalAlloc - before.TotalAlloc
		t.Logf("%s: %d MB allocated for %d MB of input", name, alloc>>20, len(in)>>20)
		if alloc > 64<<20 {
			t.Errorf("%s: %d MB allocated, want at most 64 MB", name, alloc>>20)
		}
	}
}

// nestedBackticks wraps body in depth levels of backticks, escaped the way
// the shell needs at each level.
func nestedBackticks(depth int, body string) string {
	open, close := "", ""
	for d := range depth {
		esc := strings.Repeat(`\`, (1<<d)-1)
		open += esc + "`"
		close = esc + "`" + close
	}
	return "echo " + open + body + close
}

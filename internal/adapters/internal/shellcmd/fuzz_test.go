package shellcmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// The parsers here read untrusted transcript text: a Bash command an agent
// ran and the output it got back. These fuzz targets check what must hold
// for any input, not just that nothing panics: the caps hold, the output is
// bounded by the input, nothing is invented, and quoted text, heredoc bodies
// and comments stay data. `go test` (and so `make check`) runs only the seeds;
// a long run is `go test -run '^$' -fuzz '^FuzzParse$' -fuzztime 10m` in this
// folder.

// testLiterals returns every string literal of this package's tests, and the
// sums of literals ("a" + "b"), each once: the commands and outputs of the
// cases above are the fuzzers' seeds, so a new case seeds them too.
func testLiterals(f *testing.F) []string {
	f.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		f.Fatalf("no test files to read: %v", err)
	}
	fset := token.NewFileSet()
	var out []string
	seen := map[string]bool{}
	var value func(e ast.Expr) (string, bool)
	value = func(e ast.Expr) (string, bool) {
		switch e := e.(type) {
		case *ast.BasicLit:
			if e.Kind == token.STRING {
				s, err := strconv.Unquote(e.Value)
				return s, err == nil
			}
		case *ast.BinaryExpr:
			if e.Op == token.ADD {
				a, ok1 := value(e.X)
				b, ok2 := value(e.Y)
				return a + b, ok1 && ok2
			}
		case *ast.ParenExpr:
			return value(e.X)
		}
		return "", false
	}
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			f.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if e, ok := n.(ast.Expr); ok {
				if s, ok := value(e); ok && !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
			return true
		})
	}
	for _, s := range parseSeeds { // the reader works: it found these
		if !seen[s] {
			f.Fatalf("seed %q was not read from %v", s, files)
		}
	}
	if len(out) < 100 {
		f.Fatalf("only %d seeds read from %v", len(out), files)
	}
	return out
}

// Seeds beside the cases' own, read like them by testLiterals: shapes that
// reach many branches of the lexer at once.
var parseSeeds = []string{
	`bash -c 'agtermctl session new --name a'`, `sh -lc "x; agtermctl session new --name=b"`, `zsh -c`, `bash -o -c`,
	"cat <<'EOF'\nagtermctl session new --name c\nEOF\ngh pr create", `sudo -u x env Y=1 timeout 3 agtermctl session new --name d # e`,
	"git commit -m \"$(cat <<'EOF'\nx \"y\"\nEOF\n)\" && gh pr create", "echo `a $(b \"c\")` $'d\\'e' $((1<<2)) >&2",
	`env -S 'glab mr create --fill' && git -C repo push -o merge_request.create origin HEAD`,
	"bash -c 'gh pr create --fill; gh pr view' && gh pr list <(cat x) 2>&1 | tee log",
}

// checkCommand fails t when c, parsed from s, breaks a rule that holds for
// every input.
func checkCommand(t *testing.T, s string, c Command) {
	t.Helper()
	n := len(c.words)
	if len(c.sep) != n || len(c.at) != n {
		t.Fatalf("slices differ: %d words, %d sep, %d at", n, len(c.sep), len(c.at))
	}
	if n > maxWords {
		t.Fatalf("%d words, cap %d", n, maxWords)
	}
	// Every word, separator and substitution is paid for by the input: at
	// most two entries per byte in the call and two per byte of each script
	// (a script is one of the call's words), plus a separator per script.
	if limit := 4*len(s) + maxShellScripts; n > limit {
		t.Fatalf("%d words from %d bytes, at most %d", n, len(s), limit)
	}
	bytes := 0
	for i, w := range c.words {
		bytes += len(w)
		if c.sep[i] && (w != "" || c.at[i]) {
			t.Fatalf("separator %d holds %q (at %v)", i, w, c.at[i])
		}
		if c.sep[i] && (i == 0 || c.sep[i-1]) {
			t.Fatalf("leading or repeated separator at %d", i)
		}
	}
	// The call's words copy at most its bytes, and a script's words at most
	// the bytes of the word it was.
	if bytes > 2*len(s) {
		t.Fatalf("%d bytes of words from %d bytes of input", bytes, len(s))
	}
	if len(c.scripts) > maxShellScripts {
		t.Fatalf("%d scripts, cap %d", len(c.scripts), maxShellScripts)
	}
	for k, from := range c.scripts {
		if from < 1 || from > n || (k > 0 && from < c.scripts[k-1]) {
			t.Fatalf("script %d starts at %d of %d words: %v", k, from, n, c.scripts)
		}
		end := n
		if k+1 < len(c.scripts) {
			end = c.scripts[k+1]
		}
		size := 0
		for _, w := range c.words[from:end] {
			size += len(w)
		}
		if size > maxShellScript {
			t.Fatalf("script %d has %d bytes of words, cap %d", k, size, maxShellScript)
		}
	}

	runs := func(prog string, args ...string) []int {
		got := c.Runs(prog, args...)
		for k, i := range got {
			if k > 0 && i <= got[k-1] || !c.at[i] || filepath.Base(c.words[i]) != prog {
				t.Fatalf("Runs(%q, %q) = %v: %d is not a run of it", prog, args, got, i)
			}
			for a, arg := range args {
				if c.sep[i+1+a] || c.words[i+1+a] != arg {
					t.Fatalf("Runs(%q, %q) = %v: word %d is %q", prog, args, got, i+1+a, c.words[i+1+a])
				}
			}
		}
		return got
	}
	gh, creates := runs("gh"), runs("gh", "pr", "create")
	for _, i := range creates {
		if !slices.Contains(gh, i) {
			t.Fatalf("gh pr create at %d is not among the gh runs %v", i, gh)
		}
	}
	other, among, first := c.RunsOtherGH(), c.CreatesAmongOtherGH(), c.CreateIsFirstGH()
	if other != (len(creates) > 0 && len(gh) > len(creates)) || among && !other {
		t.Fatalf("other gh %v, among %v with %d gh runs, %d creates", other, among, len(gh), len(creates))
	}
	if first && (len(creates) == 0 || gh[0] != creates[0]) {
		t.Fatalf("create is first, but gh runs %v, creates %v", gh, creates)
	}
	runs("agtermctl", "session", "new")
	glabCreates := runs("glab", "mr", "create")
	if k := c.MRCreation(); k.Glab != (len(glabCreates) > 0) || (k.Mixed || k.First) && !k.Glab || k.Push && len(runs("git")) == 0 {
		t.Fatalf("MRCreation %+v with glab creates %v", k, glabCreates)
	}

	names := c.SpawnNames()
	if len(names) > n {
		t.Fatalf("%d names from %d words", len(names), n)
	}
	for _, v := range names {
		if v == "" || len(v) > 256 || strings.ContainsAny(v, "$`") ||
			!slices.Contains(c.words, v) && !slices.Contains(c.words, "--name="+v) {
			t.Fatalf("name %q is not a plain --name value of the command", v)
		}
	}
}

// FuzzParse: any command line parses within the caps, the same way twice,
// with every query consistent with the words; and text in single quotes, in a
// heredoc body or in a comment never becomes a command.
func FuzzParse(f *testing.F) {
	for _, s := range testLiterals(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c := Parse(s)
		checkCommand(t, s, c)
		if !reflect.DeepEqual(c, Parse(s)) {
			t.Fatal("not deterministic")
		}

		// Single quotes keep everything literal: one word, an argument.
		q := strings.ReplaceAll(s, "'", "")
		quoted := Parse("echo '" + q + "'")
		if !slices.Equal(quoted.words, []string{"echo", q}) || !slices.Equal(quoted.at, []bool{true, false}) || quoted.scripts != nil {
			t.Fatalf("a quoted %q was read as %q (at %v, scripts %v)", q, quoted.words, quoted.at, quoted.scripts)
		}

		// A quoted heredoc's body is data, whatever it holds, up to its
		// delimiter line.
		lines := slices.DeleteFunc(strings.Split(s, "\n"), func(l string) bool { return l == "EOF" })
		doc := Parse("cat <<'EOF'\n" + strings.Join(lines, "\n") + "\nEOF\n")
		if !slices.Equal(doc.words, []string{"cat", ""}) || !slices.Equal(doc.sep, []bool{false, true}) || doc.scripts != nil {
			t.Fatalf("a heredoc body %q was read as %q", lines, doc.words)
		}

		// A comment runs to the end of its line.
		line := strings.ReplaceAll(s, "\n", "")
		if got := Parse("true # " + line).words; !slices.Equal(got, []string{"true"}) {
			t.Fatalf("a comment %q was read as %q", line, got)
		}
	})
}

// prFrom says whether ref names a pull request address written in out: the
// parsers never invent one.
func prFrom(out string, ref model.PRRef) bool {
	return strings.Contains(out, "https://github.com/"+ref.Repo.Path+"/pull/")
}

func checkPRs(t *testing.T, what, out string, refs []model.PRRef) {
	t.Helper()
	if len(refs) > maxIDsCall {
		t.Fatalf("%s: %d pull requests, cap %d", what, len(refs), maxIDsCall)
	}
	for i, r := range refs {
		if !r.Repo.Valid() || r.Repo.Forge != model.ForgeGitHub || r.Number < 1 {
			t.Fatalf("%s: %+v is no GitHub pull request", what, r)
		}
		if !prFrom(out, r) {
			t.Fatalf("%s: %+v is not in the output", what, r)
		}
		if slices.Contains(refs[:i], r) {
			t.Fatalf("%s: %+v twice", what, r)
		}
	}
}

const (
	refusalLines = "a pull request for branch \"feature\" into branch \"main\" already exists:\nhttps://github.com/example-org/refused/pull/9\n"
	createdLines = "Creating pull request for feature into main in example-org/announced\n\nhttps://github.com/example-org/announced/pull/7\n"
)

// FuzzPullRequests: what gh printed yields at most maxIDsCall valid pull
// requests, each once and each written in it. The three readings agree with
// each other; a refused create's address is never counted, and an announced
// create's always is.
func FuzzPullRequests(f *testing.F) {
	for _, s := range testLiterals(f) {
		f.Add(s, false)
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, out string, createFirst bool) {
		all := PullRequests(out)
		checkPRs(t, "PullRequests", out, all)
		own, ownFirst := OwnPullRequests(out, false), OwnPullRequests(out, createFirst)
		checkPRs(t, "OwnPullRequests", out, own)
		checkPRs(t, "OwnPullRequests first", out, ownFirst)
		switch {
		case len(own) > 0 && !slices.Equal(own, ownFirst):
			t.Fatalf("announced %v, but %v when the create is first", own, ownFirst)
		case len(own) == 0 && (len(ownFirst) > 1 || len(ownFirst) == 1 && !slices.Contains(all, ownFirst[0])):
			t.Fatalf("nothing announced, but %v when the create is first (all %v)", ownFirst, all)
		}
		unordered := UnorderedPullRequests(out)
		if !slices.Equal(unordered, all) && !slices.Equal(unordered, own) {
			t.Fatalf("unordered %v is neither all %v nor the announced %v", unordered, all, own)
		}
		if !strings.Contains(out, "already exists:") && !slices.Equal(unordered, all) {
			t.Fatalf("no refusal, but unordered %v differs from all %v", unordered, all)
		}

		refused := model.PRRef{Repo: model.GitHubRepo("example-org/refused"), Number: 9}
		if !prFrom(out, refused) {
			after := PullRequests(refusalLines + out)
			if !slices.Equal(after, all) {
				t.Fatalf("after a refusal %v, want %v", after, all)
			}
			if slices.Contains(OwnPullRequests(refusalLines+out, createFirst), refused) || slices.Contains(UnorderedPullRequests(refusalLines+out), refused) {
				t.Fatal("a refused create's address was counted")
			}
		}
		announced := model.PRRef{Repo: model.GitHubRepo("example-org/announced"), Number: 7}
		if got := OwnPullRequests(createdLines+out, createFirst); len(got) == 0 || got[0] != announced {
			t.Fatalf("an announced create was not credited first: %v", got)
		}
	})
}

// FuzzTerminalIDs: what `agtermctl session new` printed yields at most
// maxIDsCall upper-case session ids, each once and each written in it; past
// maxJSONResult bytes, only from short lines.
func FuzzTerminalIDs(f *testing.F) {
	for _, s := range testLiterals(f) {
		f.Add(s)
	}
	f.Add(`{"id":"0f8e1c2a-3b4d-4e5f-8a9b-0c1d2e3f4a5b","name":"w"}`)
	f.Add("session worker-a 0F8E1C2A-3B4D-4E5F-8A9B-0C1D2E3F4A5B\n")
	f.Fuzz(func(t *testing.T, out string) {
		ids := TerminalIDs(out)
		if len(ids) > maxIDsCall {
			t.Fatalf("%d ids, cap %d", len(ids), maxIDsCall)
		}
		upper := strings.ToUpper(out)
		for i, id := range ids {
			if !uuidRe.MatchString(id) || id != strings.ToUpper(id) || !strings.Contains(upper, id) || slices.Contains(ids[:i], id) {
				t.Fatalf("id %q of %q", id, ids)
			}
			if len(out) <= maxJSONResult {
				continue
			}
			short := false
			for line := range strings.SplitSeq(upper, "\n") {
				w := strings.Fields(line)
				short = short || len(w) > 0 && len(w) <= maxIDLineWords && w[len(w)-1] == id
			}
			if !short {
				t.Fatalf("id %q read from a long output not on a short line", id)
			}
		}
	})
}

// mrKind reads the four flags of an MRCreation from the low bits of b.
func mrKind(b uint8) MRCreation {
	return MRCreation{Glab: b&1 != 0, Push: b&2 != 0, Mixed: b&4 != 0, First: b&8 != 0}
}

func checkMRs(t *testing.T, text string, k MRCreation, mrs []MergeRequest) {
	t.Helper()
	if !k.Any() && mrs != nil {
		t.Fatalf("%+v creates nothing, but read %v", k, mrs)
	}
	if len(mrs) > maxIDsCall {
		t.Fatalf("%d merge requests, cap %d", len(mrs), maxIDsCall)
	}
	for i, m := range mrs {
		r := m.Ref
		if r.Repo.Forge != model.ForgeGitLab || !r.Repo.Valid() || r.Number < 1 || r.Number > math.MaxInt32 {
			t.Fatalf("%+v is no GitLab merge request", r)
		}
		if m.Pushed && !k.Push || !m.Pushed && !k.Glab {
			t.Fatalf("%+v from %+v", m, k)
		}
		// https's own port, written out in the output, is no port in the ref.
		if !strings.Contains(text, "https://"+r.Repo.Host+"/"+r.Repo.Path+"/-/merge_requests/") &&
			!strings.Contains(text, "https://"+r.Repo.Host+":443/"+r.Repo.Path+"/-/merge_requests/") {
			t.Fatalf("%+v is not in the output", r)
		}
		if slices.ContainsFunc(mrs[:i], func(o MergeRequest) bool { return o.Ref == r }) {
			t.Fatalf("%+v twice", r)
		}
	}
}

const (
	mrCreatedLines = "Creating merge request for feature into main in group/announced\n\nhttps://gitlab.example.com/group/announced/-/merge_requests/3\n"
	mrPushedLines  = "remote:\nremote: View merge request for feature:\nremote:   https://gitlab.example.com/group/pushed/-/merge_requests/4\nremote:\n"
)

// FuzzMergeRequests: what a creating command printed yields at most
// maxIDsCall valid GitLab merge requests, each once, each written in it and
// of a kind the command creates. A mixed call only ever leaves addresses out,
// and an announced create (glab's line, or GitLab's on a push) always counts.
func FuzzMergeRequests(f *testing.F) {
	for _, s := range testLiterals(f) {
		for _, b := range []uint8{1, 2, 3, 5, 13} {
			f.Add(s, b)
		}
	}
	f.Fuzz(func(t *testing.T, text string, b uint8) {
		k := mrKind(b)
		mrs := MergeRequests(text, k)
		checkMRs(t, text, k, mrs)
		if k.Mixed {
			plain := MergeRequests(text, MRCreation{Glab: k.Glab, Push: k.Push})
			for _, m := range mrs {
				if len(plain) < maxIDsCall && !slices.Contains(plain, m) {
					t.Fatalf("a mixed call read %+v, which a plain one does not: %v", m, plain)
				}
			}
		}
		if k.Glab {
			want := MergeRequest{Ref: model.PRRef{Repo: model.GitLabRepo("gitlab.example.com", "group/announced"), Number: 3}}
			if got := MergeRequests(mrCreatedLines+text, k); len(got) == 0 || got[0] != want {
				t.Fatalf("an announced create was not credited first: %v", got)
			}
		}
		if k.Push {
			want := MergeRequest{Ref: model.PRRef{Repo: model.GitLabRepo("gitlab.example.com", "group/pushed"), Number: 4}, Pushed: true}
			if got := MergeRequests(mrPushedLines+text, k); len(got) == 0 || got[0] != want {
				t.Fatalf("a push's announced merge request was not credited first: %v", got)
			}
		}
	})
}

// FuzzAddMergeRequests: two results, in either order, added to a session's
// list. The list only grows, never past the budget of each forge, holds each
// ref once, and only what an output named; a pushed ref is in it, with a
// usable window (the earliest), and never one a bare line credited.
func FuzzAddMergeRequests(f *testing.F) {
	lits := testLiterals(f)
	for i, s := range lits {
		f.Add(s, lits[(i+1)%len(lits)], uint8(i%16), int64(0), int64(60), int64(-30), int64(90), uint8(i%40))
	}
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	at := func(v int64) time.Time { // 0 is the zero time; the rest seconds from t0
		if v == 0 {
			return time.Time{}
		}
		return t0.Add(time.Duration(v%1_000_000) * time.Second)
	}
	f.Fuzz(func(t *testing.T, text1, text2 string, b uint8, from1, to1, from2, to2 int64, lim uint8) {
		limit := int(lim % 40)
		gh := model.PRRef{Repo: model.GitHubRepo("example-org/example-repo"), Number: 1}
		start := []model.PRRef{gh}
		calls := []struct {
			mrs      []MergeRequest
			from, to time.Time
		}{
			{MergeRequests(text1, mrKind(b)), at(from1), at(to1)},
			{MergeRequests(text2, mrKind(b>>4)), at(from2), at(to2)},
		}
		created, pushed := slices.Clone(start), []model.PushedRef(nil)
		for n, c := range calls {
			before := slices.Clone(created)
			created, pushed = AddMergeRequests(created, pushed, c.mrs, c.from, c.to, limit)
			if !slices.Equal(created[:len(before)], before) {
				t.Fatalf("call %d changed the list: %v, was %v", n, created, before)
			}
			gitlab := 0
			for i, r := range created {
				if slices.Contains(created[:i], r) {
					t.Fatalf("%+v twice in %v", r, created)
				}
				if r.Repo.Forge == model.ForgeGitLab {
					gitlab++
				}
			}
			if gitlab > limit || created[0] != gh {
				t.Fatalf("%d merge requests, budget %d: %v", gitlab, limit, created)
			}
			for _, r := range created[1:] {
				named := false
				for _, c := range calls[:n+1] {
					named = named || slices.ContainsFunc(c.mrs, func(m MergeRequest) bool { return m.Ref == r })
				}
				if !named {
					t.Fatalf("%+v was named by no output", r)
				}
			}
			for i, p := range pushed {
				if !slices.Contains(created, p.Ref) || slices.ContainsFunc(pushed[:i], func(o model.PushedRef) bool { return o.Ref == p.Ref }) {
					t.Fatalf("pushed %+v: not in %v, or twice", p, created)
				}
				if p.From.IsZero() || p.To.IsZero() || p.To.Before(p.From) {
					t.Fatalf("pushed %+v has an unusable window", p)
				}
				earliest := time.Time{}
				for _, c := range calls[:n+1] {
					for _, m := range c.mrs {
						if m.Ref != p.Ref {
							continue
						}
						if !m.Pushed {
							t.Fatalf("%+v is still pushed after a bare line credited it", p)
						}
						usable := !c.from.IsZero() && !c.to.IsZero() && !c.to.Before(c.from)
						if usable && (earliest.IsZero() || c.from.Before(earliest)) {
							earliest = c.from
						}
					}
				}
				if !p.From.Equal(earliest) {
					t.Fatalf("pushed %+v keeps a window from %s, the earliest is %s", p, p.From, earliest)
				}
			}
		}
	})
}

// FuzzIssueCommands: any command line yields at most maxIDsCall issues, each
// once, of the tool that named it, with a number in range, a repository that
// is valid and written in one word of the command (as the lexer reads it),
// and an address only in a shape the page's patterns accept. A call that
// changes folder first gives only the issues that name their repository; one
// that runs env, sudo or the like, or assigns a GH_/GLAB_/GITLAB_ variable,
// first gives none.
func FuzzIssueCommands(f *testing.F) {
	for _, s := range testLiterals(f) {
		f.Add(s)
	}
	f.Add("echo create " + strings.Repeat("[", 1<<20)) // one word of brackets
	f.Add("gh issue view 7 || gh issue view 8; gh repo set-default x/y; gh issue view 9")
	f.Add("git diff --quiet && gh issue view 7; (true && { gh issue view 8; })")
	f.Add("gh issue view 7 |\\\n| gh issue view 8")
	f.Add(strings.Repeat("glab mr create;", 13000))
	f.Add(strings.Repeat("gh issue view 7;", 13000))                                                  // the longest line of a transcript, one command over and over
	f.Add("true " + strings.Repeat("x ", 65530) + "; gh issue view 7 -R github.com/example-org/tool") // a lexer cap behind the invocation
	for _, in := range []string{`gh issue view $'\067'`, `QUERY='.number -R github.com/example-org/other'; gh issue view -q $QUERY 7 --json number`, `bash -n -c 'gh issue view 7'`, `printf -v GH_REPO %s github.com/example-org/other; export GH_REPO; gh issue view 7`, `GIT_DIR=/repo-b/.git gh issue view 7`, `glab issue view 7 -R group/project`} {
		f.Add(in)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c := Parse(s)
		got := c.IssueCommands()
		if len(got) > maxIDsCall {
			t.Fatalf("%d issues, cap %d", len(got), maxIDsCall)
		}
		for i, ic := range got {
			if ic.Number < 1 || ic.Number > math.MaxInt32 {
				t.Fatalf("%+v: number out of range", ic)
			}
			if !ic.Repo.IsZero() {
				if !ic.Repo.Valid() || ic.Repo.Forge != ic.Forge || !slices.ContainsFunc(c.words, func(w string) bool { return strings.Contains(w, ic.Repo.Path) }) {
					t.Fatalf("%+v: not a valid repository written in the command", ic)
				}
				if ic.Repo.Forge == model.ForgeGitHub && ic.Repo.Host != model.GitHubHost {
					t.Fatalf("%+v: a GitHub repository on another host", ic)
				}
				if ic.Repo.Forge == model.ForgeGitLab && model.GitLabHostname(ic.Repo.Host) == model.GitHubHost {
					t.Fatalf("%+v: a GitLab repository on github.com", ic)
				}
			}
			if ic.Forge != model.ForgeGitHub && ic.Forge != model.ForgeGitLab {
				t.Fatalf("%+v: no tool", ic)
			}
			if slices.ContainsFunc(got[:i], func(o IssueCommand) bool {
				return sameIssue(o.Forge, o.Repo, o.Number, ic.Forge, ic.Repo, ic.Number)
			}) {
				t.Fatalf("%+v twice", ic)
			}
		}
		if !reflect.DeepEqual(got, Parse(s).IssueCommands()) {
			t.Fatal("not deterministic")
		}
		for _, ic := range Parse("cd /x; " + s).IssueCommands() {
			if ic.Repo.IsZero() {
				t.Fatalf("a cd in front still gave an issue left to the folder: %+v", ic)
			}
		}
		// Whatever the line, these in front of it make the whole line unread.
		for _, pre := range []string{"GH_REPO=example-org/other ", "GIT_DIR=/x/.git ", "FOO=1 ", "export GITLAB_HOST=gitlab.example.com\n", "env -C /x ", "sudo -D /x ",
			"printf -v GH_REPO %s x; ", "true || ", "gh repo set-default x/y; ", "eval x; ", "read GH_REPO; ", "set -n; ", "bash -n -c true; ", "bash -c ' ' $* ; echo $HOME; "} {
			if got := Parse(pre + s).IssueCommands(); len(got) != 0 {
				t.Fatalf("%q in front still gave %+v", pre, got)
			}
		}
		// What is read leaves no assignment, no cut and no expansion before
		// the last invocation behind: the arguments of what it gave are known.
		if len(got) > 0 {
			if c.inexact {
				t.Fatalf("a line a cap cut still gave %+v", got)
			}
			for k, w := range c.words {
				if !c.sep[k] && assignRe.MatchString(w) {
					t.Fatalf("an assignment word %q, and still %+v", w, got)
				}
			}
		}
	})
}

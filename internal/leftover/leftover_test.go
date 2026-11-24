package leftover

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/gitsrc"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// lab is the scratch setup: a bare origin, the
// owner's clone with one worktree per case, and a second clone standing in
// for GitHub, which merges.
type lab struct {
	t      *testing.T
	dir    string
	repo   string // the owner's clone (main checkout)
	server string // the second clone that merges
	prs    []model.PullRequest
	n      int
	// incomplete: heads whose pull requests GitHub may not all have given
	incomplete map[string]bool
	// live: folders of the open sessions and terminals the board knows
	live []string
	// bases: heads that are the base of an open pull request
	bases map[string]bool
	// checkouts: other repos' checkouts the board knows
	checkouts []string
	// checkoutsUnknown: some repo's checkouts could not all be listed
	checkoutsUnknown bool
}

// labEnv is the environment of every process the lab starts: the caller's,
// without any GIT_ variable (GIT_DIR, GIT_INDEX_FILE, GIT_CONFIG_PARAMETERS
// and the like would send git somewhere else when the tests run from a git
// hook), and without the user's git config.
func labEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			env = append(env, kv)
		}
	}
	return append(append(env,
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"), extra...)
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = labEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newLab(t *testing.T) *lab {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := &lab{t: t, dir: dir, repo: filepath.Join(dir, "repo"), server: filepath.Join(dir, "server")}
	run(t, dir, "init", "-q", "--bare", "-b", "main", "origin.git")
	run(t, dir, "clone", "-q", "origin.git", "repo")
	run(t, l.repo, "commit", "-q", "--allow-empty", "-m", "init")
	run(t, l.repo, "push", "-q", "origin", "main")
	run(t, dir, "clone", "-q", "origin.git", "server")
	return l
}

func (l *lab) wt(name string) string { return filepath.Join(l.dir, "wt-"+name) }

func (l *lab) write(dir, file, text string) {
	l.t.Helper()
	p := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		l.t.Fatal(err)
	}
}

// branch makes a worktree on a new branch with two pushed commits.
func (l *lab) branch(name string) {
	l.t.Helper()
	run(l.t, l.repo, "fetch", "-q", "origin")
	run(l.t, l.repo, "worktree", "add", "-q", "-b", name, l.wt(name), "origin/main")
	l.write(l.wt(name), name+".txt", "one\n")
	run(l.t, l.wt(name), "add", ".")
	run(l.t, l.wt(name), "commit", "-q", "-m", name+" one")
	l.write(l.wt(name), name+".txt", "one\ntwo\n")
	run(l.t, l.wt(name), "commit", "-q", "-am", name+" two")
	run(l.t, l.wt(name), "push", "-q", "origin", name)
}

// pr records a pull request for branch on the "GitHub" side and merges it
// the given way: "merge", "squash", "rebase"; "open" and "closed" leave it
// unmerged. The branch stays on origin, as without auto-delete.
func (l *lab) pr(branch, how string) model.PullRequest {
	l.t.Helper()
	s := l.server
	run(l.t, s, "fetch", "-q", "origin")
	run(l.t, s, "checkout", "-q", "main")
	run(l.t, s, "reset", "-q", "--hard", "origin/main")
	head := run(l.t, s, "rev-parse", "origin/"+branch)
	l.n++
	pr := model.PullRequest{Repo: model.GitHubRepo("example-org/example-repo"), Number: l.n, HeadBranch: branch, HeadOID: head,
		HeadRefOID: head, BaseBranch: "main", BaseIsDefault: true, OpenedAt: time.Unix(int64(l.n), 0), State: model.PRStateMerged}
	switch how {
	case "merge":
		run(l.t, s, "merge", "-q", "--no-ff", "-m", "merge "+branch, "origin/"+branch)
	case "squash":
		run(l.t, s, "merge", "-q", "--squash", "origin/"+branch)
		run(l.t, s, "commit", "-q", "-m", "squash "+branch)
	case "rebase":
		run(l.t, s, "checkout", "-q", "-B", "tmp", "origin/"+branch)
		run(l.t, s, "rebase", "-q", "main")
		run(l.t, s, "checkout", "-q", "main")
		run(l.t, s, "merge", "-q", "--ff-only", "tmp")
	case "open":
		pr.State = model.PRStateOpen
	case "closed":
		pr.State = model.PRStateClosed
	}
	run(l.t, s, "push", "-q", "origin", "main")
	run(l.t, l.repo, "fetch", "-q", "origin")
	l.prs = append(l.prs, pr)
	return pr
}

// scan looks at the lab with processes working in cwds and leftover
// sessions in sessions; each session owns the processes working in its own
// folder.
func (l *lab) scan(cwds, sessions []string) (map[string]Item, map[string]Held) {
	l.t.Helper()
	var procs []model.Proc
	for i, d := range cwds {
		procs = append(procs, model.Proc{PID: 1000 + i, PPID: 1, Cwd: d})
	}
	var sitters []Sitter
	for _, d := range sessions {
		own := map[int]bool{}
		for _, p := range procs {
			if inside([]string{p.Cwd}, d) {
				own[p.PID] = true
			}
		}
		sitters = append(sitters, Sitter{Dir: d, Own: own})
	}
	return l.scanProcs(procs, sitters)
}

func (l *lab) scanProcs(procs []model.Proc, sitters []Sitter) (map[string]Item, map[string]Held) {
	l.t.Helper()
	git := gitsrc.NewInventory(gitsrc.Options{})
	inv, err := git.Inventory(context.Background(), l.repo)
	if err != nil {
		l.t.Fatal(err)
	}
	items, held := Scan(context.Background(), git, Repo{Root: l.repo, PRs: l.prs, Incomplete: l.incomplete, Bases: l.bases,
		Procs: procs, ProcsKnown: true, Sessions: sitters, LiveDirs: l.live, Checkouts: l.checkouts, CheckoutsUnknown: l.checkoutsUnknown}, inv)
	im, hm := map[string]Item{}, map[string]Held{}
	for _, it := range items {
		key := it.Branch
		if key == "" {
			key = "remote:" + it.PR.HeadBranch
		}
		if _, dup := im[key]; dup {
			l.t.Fatalf("two items for %s", key)
		}
		im[key] = it
	}
	for _, h := range held {
		hm[h.Branch] = h
	}
	return im, hm
}

// Every case of the lab, in one repository, as the scan sees them together.
func TestLabCases(t *testing.T) {
	l := newLab(t)
	for _, b := range []string{"mergec", "squash", "rebase", "behind", "unpushed", "dirty", "untracked",
		"stashed", "live", "waiting", "ignored", "locked", "openpr", "closedpr", "otherbase", "reused", "stalebase", "behindmerge"} {
		l.branch(b)
	}
	l.pr("mergec", "merge")
	l.pr("squash", "squash")
	l.pr("rebase", "rebase")
	// behind: one more commit pushed to the pull request from elsewhere; the
	// local branch never got it.
	run(t, l.server, "fetch", "-q", "origin")
	run(t, l.server, "checkout", "-q", "-B", "elsewhere", "origin/behind")
	l.write(l.server, "behind.txt", "one\ntwo\nthree\n")
	run(t, l.server, "commit", "-q", "-am", "from elsewhere")
	run(t, l.server, "push", "-q", "origin", "elsewhere:behind")
	l.pr("behind", "squash")
	// behindmerge: the same, merged with a merge commit, which puts the
	// branch's own commits on main: nothing ties the tip to the pull request.
	run(t, l.server, "fetch", "-q", "origin")
	run(t, l.server, "checkout", "-q", "-B", "elsewhere", "origin/behindmerge")
	l.write(l.server, "behindmerge.txt", "one\ntwo\nthree\n")
	run(t, l.server, "commit", "-q", "-am", "from elsewhere")
	run(t, l.server, "push", "-q", "origin", "elsewhere:behindmerge")
	l.pr("behindmerge", "merge")
	l.pr("stalebase", "squash")
	for _, b := range []string{"unpushed", "dirty", "untracked", "stashed", "live", "waiting", "ignored", "locked", "reused"} {
		l.pr(b, "squash")
	}
	l.pr("openpr", "open")
	l.pr("closedpr", "closed")
	other := l.pr("otherbase", "squash")
	l.prs[len(l.prs)-1].BaseIsDefault = false
	_ = other

	// What happened after the merges, in the owner's clone.
	l.write(l.wt("unpushed"), "unpushed.txt", "more\n")
	run(t, l.wt("unpushed"), "commit", "-q", "-am", "after the merge")
	l.write(l.wt("dirty"), "dirty.txt", "edited\n")
	l.write(l.wt("untracked"), "notes.md", "new\n")
	l.write(l.wt("stashed"), "stashed.txt", "wip\n")
	run(t, l.wt("stashed"), "stash", "push", "-q", "-m", "wip")
	l.write(l.repo, ".git/info/exclude", ".env\nnode_modules/\n")
	l.write(l.wt("ignored"), ".env", "KEY=1\n")
	l.write(l.wt("ignored"), "node_modules/x/y.js", "x\n")
	run(t, l.repo, "worktree", "lock", l.wt("locked"))
	// reused: the branch name was deleted after its merge and made again for
	// new work, from today's main, with no commit yet.
	run(t, l.repo, "worktree", "remove", "--force", l.wt("reused"))
	run(t, l.repo, "branch", "-D", "reused")
	run(t, l.repo, "worktree", "add", "-q", "-b", "reused", l.wt("reused"), "origin/main")
	// stalebase: the same, made again from the local main, which never got
	// the merges: its tip is the commit the pull request started from.
	run(t, l.repo, "worktree", "remove", "--force", l.wt("stalebase"))
	run(t, l.repo, "branch", "-D", "stalebase")
	run(t, l.repo, "worktree", "add", "-q", "-b", "stalebase", l.wt("stalebase"), "main")
	// fresh: a brand-new branch from today's main, no commit, no pull request.
	run(t, l.repo, "worktree", "add", "-q", "-b", "fresh", l.wt("fresh"), "origin/main")
	// a branch left only on GitHub: pushed, merged, the local one deleted.
	l.branch("ghonly")
	l.pr("ghonly", "squash")
	run(t, l.repo, "worktree", "remove", l.wt("ghonly"))
	run(t, l.repo, "branch", "-D", "ghonly")

	cwds := []string{l.wt("live") + "/sub", l.wt("waiting"), l.repo}
	items, held := l.scan(cwds, []string{l.wt("waiting")})

	for _, b := range []string{"mergec", "squash", "rebase", "behind", "ignored"} {
		it, ok := items[b]
		if !ok {
			t.Errorf("%s: not listed (held: %+v)", b, held[b])
			continue
		}
		if it.Worktree != l.wt(b) || it.Waiting || !it.Remote {
			t.Errorf("%s: %+v", b, it)
		}
	}
	if items["ignored"].Ignored != 2 || items["squash"].Ignored != 0 {
		t.Errorf("ignored counts: %d and %d", items["ignored"].Ignored, items["squash"].Ignored)
	}
	if it := items["waiting"]; it.Worktree != l.wt("waiting") || !it.Waiting {
		t.Errorf("waiting: %+v", it)
	}
	if it, ok := items["remote:ghonly"]; !ok || !it.Remote || it.Branch != "" || it.Worktree != "" {
		t.Errorf("ghonly: %+v (ok %v)", it, ok)
	}
	wantHeld := map[string]Reason{
		"unpushed": ReasonCommits, "dirty": ReasonChanges, "untracked": ReasonChanges,
		"stashed": ReasonStash, "live": ReasonInUse, "locked": ReasonLocked,
	}
	for b, r := range wantHeld {
		if h, ok := held[b]; !ok || h.Reason != r {
			t.Errorf("%s: held = %+v, want %s", b, h, r)
		}
		if _, ok := items[b]; ok {
			t.Errorf("%s: listed, must not be", b)
		}
	}
	for _, b := range []string{"fresh", "reused", "stalebase", "behindmerge", "openpr", "closedpr", "otherbase", "main"} {
		if _, ok := items[b]; ok {
			t.Errorf("%s: listed, must not be", b)
		}
		if _, ok := held[b]; ok {
			t.Errorf("%s: counted as held back, but it holds no merged work", b)
		}
	}
	if len(items) != 7 || len(held) != len(wantHeld) {
		t.Errorf("items %d, held %d", len(items), len(held))
	}
}

// The sessions and terminals the board knows count by their own folders,
// whatever folder their processes report: an open one holds the worktree, a
// leftover session makes it wait until that session is closed.
func TestBoardKnownSessionsHoldOrWait(t *testing.T) {
	l := newLab(t)
	for _, b := range []string{"open", "terminal", "leftover", "both", "free"} {
		l.branch(b)
		l.pr(b, "squash")
	}
	l.live = []string{l.wt("open") + "/sub", l.wt("terminal"), l.wt("both")}
	items, held := l.scan([]string{l.repo}, []string{l.wt("leftover") + "/deep/er", l.wt("both")})
	for _, b := range []string{"open", "terminal", "both"} {
		if h, ok := held[b]; !ok || h.Reason != ReasonInUse {
			t.Errorf("%s: held = %+v, want inUse", b, h)
		}
	}
	if it := items["leftover"]; it.Worktree != l.wt("leftover") || !it.Waiting {
		t.Errorf("leftover: %+v", it)
	}
	if it, ok := items["free"]; !ok || it.Waiting || it.Worktree != l.wt("free") {
		t.Errorf("free: %+v", it)
	}
	if len(items) != 2 || len(held) != 3 {
		t.Errorf("items %d, held %d", len(items), len(held))
	}
}

// The lab and the scan stay in the lab when the tests run under git (a hook sets GIT_DIR, GIT_INDEX_FILE and the like): another
// repository is neither read nor changed.
func TestLabIgnoresInheritedGitVariables(t *testing.T) {
	decoyDir := t.TempDir()
	run(t, decoyDir, "init", "-q", "-b", "main")
	run(t, decoyDir, "commit", "-q", "--allow-empty", "-m", "decoy")
	refs := run(t, decoyDir, "for-each-ref")
	t.Setenv("GIT_DIR", filepath.Join(decoyDir, ".git"))
	t.Setenv("GIT_WORK_TREE", decoyDir)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoyDir, "other-index"))
	l := newLab(t)
	l.branch("x")
	l.pr("x", "squash")
	items, _ := l.scan(nil, nil)
	it, ok := items["x"]
	if !ok || it.Worktree != l.wt("x") {
		t.Fatalf("x: %+v", items)
	}
	if got := run(t, decoyDir, "for-each-ref"); got != refs {
		t.Fatalf("the other repository changed:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(decoyDir, "other-index")); err == nil {
		t.Fatal("the other index was written")
	}
}

// Without the process list no worktree is listed; a branch without one still is.
func TestUnknownProcessesHoldWorktrees(t *testing.T) {
	l := newLab(t)
	l.branch("a")
	l.pr("a", "merge")
	run(t, l.repo, "branch", "b", "origin/a")
	l.prs = append(l.prs, model.PullRequest{Number: 99, HeadBranch: "b", HeadOID: l.prs[0].HeadOID, BaseIsDefault: true,
		BaseBranch: "main", State: model.PRStateMerged})
	git := gitsrc.NewInventory(gitsrc.Options{})
	inv, err := git.Inventory(context.Background(), l.repo)
	if err != nil {
		t.Fatal(err)
	}
	items, held := Scan(context.Background(), git, Repo{Root: l.repo, PRs: l.prs}, inv)
	if len(items) != 1 || items[0].Branch != "b" || items[0].Worktree != "" || len(held) != 1 || held[0].Reason != ReasonUnknown {
		t.Fatalf("items %+v held %+v", items, held)
	}
}

// One look asks git each ancestry question once, however many branches and
// stashes repeat it.
func TestAncestryAskedOncePerLook(t *testing.T) {
	l := newLab(t)
	l.write(l.repo, "base.txt", "x\n")
	run(t, l.repo, "add", "base.txt")
	run(t, l.repo, "commit", "-q", "-m", "base")
	run(t, l.repo, "push", "-q", "origin", "main")
	for _, m := range []string{"one", "two", "three"} {
		l.write(l.repo, "base.txt", m+"\n")
		run(t, l.repo, "stash", "push", "-q", "-m", m)
	}
	for _, b := range []string{"a", "b", "c"} {
		l.branch(b)
		l.pr(b, "squash")
	}
	git := gitsrc.NewInventory(gitsrc.Options{})
	inv, err := git.Inventory(context.Background(), l.repo)
	if err != nil {
		t.Fatal(err)
	}
	c := &countAncestry{RepoInventory: git, asked: map[[2]string]int{}}
	items, _ := Scan(context.Background(), c, Repo{Root: l.repo, PRs: l.prs, ProcsKnown: true}, inv)
	if len(items) != 3 {
		t.Fatalf("items %+v", items)
	}
	for pair, n := range c.asked {
		if n > 1 {
			t.Errorf("%v asked %d times", pair, n)
		}
	}
}

type countAncestry struct {
	source.RepoInventory
	asked map[[2]string]int
}

func (c *countAncestry) IsAncestor(ctx context.Context, root, a, b string) (bool, error) {
	c.asked[[2]string{a, b}]++
	return c.RepoInventory.IsAncestor(ctx, root, a, b)
}

// A stash made on main before the branch was cut is not the branch's.
func TestOldStashOnMainDoesNotHold(t *testing.T) {
	l := newLab(t)
	l.write(l.repo, "base.txt", "x\n")
	run(t, l.repo, "add", "base.txt")
	run(t, l.repo, "commit", "-q", "-m", "base")
	run(t, l.repo, "push", "-q", "origin", "main")
	l.write(l.repo, "base.txt", "wip\n")
	run(t, l.repo, "stash", "push", "-q", "-m", "old")
	l.branch("feature")
	l.pr("feature", "squash")
	// A stash made on the branch's first commit from a detached head has no
	// branch name in its subject, but its commit is the branch's.
	items, held := l.scan(nil, nil)
	if _, ok := items["feature"]; !ok {
		t.Fatalf("held: %+v", held)
	}
	first := run(t, l.repo, "rev-parse", "feature~1")
	run(t, l.repo, "checkout", "-q", "--detach", first)
	l.write(l.repo, "feature.txt", "wip\n")
	run(t, l.repo, "stash", "push", "-q", "-m", "detached")
	run(t, l.repo, "checkout", "-q", "main")
	items, held = l.scan(nil, nil)
	if _, ok := items["feature"]; ok || held["feature"].Reason != ReasonStash {
		t.Fatalf("items %+v held %+v", items, held)
	}
}

func TestNames(t *testing.T) {
	inv := model.RepoInventory{
		Worktrees: []model.Worktree{{Path: "/r", Main: true, Branch: model.Of("main")}},
		Branches:  []model.Branch{{Name: "main"}, {Name: "a"}},
		Remote:    []model.Branch{{Name: "a"}, {Name: "b"}, {Name: "main"}},
	}
	names, partial := Names(inv)
	if strings.Join(names, ",") != "a,b" || partial {
		t.Fatalf("names %v partial %v", names, partial)
	}
	for i := range MaxNames + 1 {
		inv.Branches = append(inv.Branches, model.Branch{Name: "x" + strings.Repeat("y", i)})
	}
	if names, partial = Names(inv); len(names) != MaxNames || !partial {
		t.Fatalf("%d names, partial %v", len(names), partial)
	}
}

// A branch on GitHub that moved after the merge is not listed.
func TestRemoteOnlyAtTheMergedHead(t *testing.T) {
	pr := model.PullRequest{HeadBranch: "x", HeadOID: strings.Repeat("a", 40), HeadRefOID: strings.Repeat("b", 40)}
	it := Item{PR: pr}
	remote(&it, nil)
	if it.Remote {
		t.Fatalf("moved branch listed: %+v", it)
	}
	it.PR.HeadRefOID = pr.HeadOID
	remote(&it, nil)
	if !it.Remote {
		t.Fatalf("branch at the head not listed: %+v", it)
	}
	// GitHub did not say where the branch points: not listed.
	it = Item{PR: pr}
	it.PR.HeadRefOID, it.PR.HeadOID = "", ""
	remote(&it, nil)
	if it.Remote {
		t.Fatalf("unknown head listed: %+v", it)
	}
}

// A branch that is a symbolic ref, or that one points at, is held back:
// deleting it would delete or break another ref.
func TestSymbolicBranchesAreHeld(t *testing.T) {
	l := newLab(t)
	l.branch("target")
	l.branch("link")
	l.pr("target", "squash")
	l.pr("link", "squash")
	run(t, l.repo, "worktree", "remove", l.wt("target"))
	run(t, l.repo, "worktree", "remove", l.wt("link"))
	run(t, l.repo, "symbolic-ref", "refs/heads/alias", "refs/heads/target")
	run(t, l.repo, "branch", "keep", "link")
	run(t, l.repo, "update-ref", "-d", "refs/heads/link")
	run(t, l.repo, "symbolic-ref", "refs/heads/link", "refs/heads/keep") // at link's own head
	items, held := l.scan(nil, nil)
	for _, b := range []string{"target", "link"} {
		if _, ok := items[b]; ok {
			t.Errorf("%s: listed", b)
		}
		if held[b].Reason != ReasonUnknown {
			t.Errorf("%s: held %+v, want unknown", b, held[b])
		}
	}
}

// Any open pull request from the branch name keeps it off the list, even an
// older one beside a merged one.
func TestOpenPullRequestKeepsBranchOff(t *testing.T) {
	head := strings.Repeat("a", 40)
	merged := model.PullRequest{Number: 2, State: model.PRStateMerged, BaseIsDefault: true, HeadOID: head, OpenedAt: time.Unix(2, 0)}
	if _, ok := mergedPR([]model.PullRequest{merged}); !ok {
		t.Fatal("a merged pull request alone is merged work")
	}
	open := model.PullRequest{Number: 1, State: model.PRStateOpen, OpenedAt: time.Unix(1, 0)}
	if _, ok := mergedPR([]model.PullRequest{open, merged}); ok {
		t.Fatal("listed while a pull request from the branch is open")
	}
	closed := model.PullRequest{Number: 3, State: model.PRStateClosed, OpenedAt: time.Unix(3, 0)}
	if _, ok := mergedPR([]model.PullRequest{merged, closed}); ok {
		t.Fatal("the newest pull request was closed unmerged: not merged work")
	}
	noHead := merged
	noHead.HeadOID = ""
	if _, ok := mergedPR([]model.PullRequest{noHead}); ok {
		t.Fatal("no head commit, no evidence")
	}
}

// A rebase or bisect under way detaches the worktree's head and goes back to
// its branch at the end: that branch is in use, not left over, even though
// the worktree no longer names it.
func TestRebaseOrBisectUnderWayHoldsTheBranch(t *testing.T) {
	l := newLab(t)
	l.branch("rb")
	l.pr("rb", "merge")
	l.branch("bi")
	l.pr("bi", "squash")
	// A rebase that stops on a failing step, as one stops on a conflict.
	cmd := exec.Command("git", "rebase", "--exec", "false", "HEAD~1")
	cmd.Dir = l.wt("rb")
	cmd.Env = labEnv()
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("the rebase was meant to stop:\n%s", out)
	}
	run(t, l.wt("bi"), "bisect", "start", "HEAD", "HEAD~2")
	items, held := l.scan(nil, nil)
	for _, b := range []string{"rb", "bi"} {
		if _, ok := items[b]; ok {
			t.Errorf("%s is listed while a rebase or bisect uses it", b)
		}
		if h, ok := held[b]; !ok || h.Reason != ReasonInUse {
			t.Errorf("%s: held %+v, want in use", b, h)
		}
	}
}

// A worktree is listed only when git, asked from its folder, sees all of it
// and finds that very worktree: an edit git was told to overlook, a folder
// replaced by another checkout, or a .git file sent to another worktree's
// metadata holds it back as unknown. A merge or cherry-pick under way on its
// branch holds it as in use, though its files match the commit.
func TestWorktreeSeenWholeOrHeld(t *testing.T) {
	l := newLab(t)
	cases := []string{"hidden", "replaced", "redirected", "merging", "picking", "fine"}
	for _, b := range cases {
		l.branch(b)
	}
	for _, b := range cases {
		l.pr(b, "squash")
	}
	l.write(l.wt("hidden"), "hidden.txt", "edited\n")
	run(t, l.wt("hidden"), "update-index", "--assume-unchanged", "hidden.txt")
	tip := run(t, l.wt("replaced"), "rev-parse", "HEAD")
	if err := os.Rename(l.wt("replaced"), l.wt("replaced")+"-old"); err != nil {
		t.Fatal(err)
	}
	run(t, l.dir, "clone", "-q", "origin.git", l.wt("replaced"))
	run(t, l.wt("replaced"), "checkout", "-q", "-b", "replaced", tip)
	gitFile := filepath.Join(l.wt("redirected"), ".git")
	other, err := os.ReadFile(filepath.Join(l.wt("fine"), ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitFile, other, 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, l.wt("merging"), "merge", "-q", "-s", "ours", "--no-commit", "--no-ff", "origin/main")
	run(t, l.wt("picking"), "update-ref", "CHERRY_PICK_HEAD", "HEAD")
	for _, b := range []string{"merging", "picking"} {
		if out := run(t, l.wt(b), "status", "--porcelain"); out != "" {
			t.Fatalf("%s: files changed: %s", b, out)
		}
	}
	items, held := l.scan(nil, nil)
	if _, ok := items["fine"]; !ok {
		t.Fatalf("fine: not listed (held %+v)", held["fine"])
	}
	want := map[string]Reason{"hidden": ReasonUnknown, "replaced": ReasonUnknown, "redirected": ReasonUnknown,
		"merging": ReasonInUse, "picking": ReasonInUse}
	for b, r := range want {
		if _, ok := items[b]; ok {
			t.Errorf("%s: listed", b)
		}
		if held[b].Reason != r {
			t.Errorf("%s: held %+v, want %s", b, held[b], r)
		}
	}
}

// A head whose pull requests GitHub may not all have given (more than one
// page, a part of the answer missing) lists nothing: an open one may be
// among the missing. Merged work on it is held back as unknown.
func TestIncompleteHeadIsHeld(t *testing.T) {
	l := newLab(t)
	l.branch("local")
	l.branch("ghonly")
	l.pr("local", "squash")
	l.pr("ghonly", "squash")
	run(t, l.repo, "worktree", "remove", l.wt("ghonly"))
	run(t, l.repo, "branch", "-D", "ghonly")
	if items, _ := l.scan(nil, nil); len(items) != 2 {
		t.Fatalf("complete answers: %+v", items)
	}
	l.incomplete = map[string]bool{"local": true, "ghonly": true}
	items, held := l.scan(nil, nil)
	if len(items) != 0 {
		t.Fatalf("listed: %+v", items)
	}
	if h := held["local"]; h.Reason != ReasonUnknown || len(h.Worktrees) != 1 || len(held) != 2 {
		t.Fatalf("held %+v", held)
	}
	// Left only on GitHub, its merged work is held back the same way.
	if h := held["ghonly"]; h.Reason != ReasonUnknown || len(h.Worktrees) != 0 || h.PR.HeadBranch != "ghonly" {
		t.Fatalf("held %+v", held)
	}
}

// A branch behind the merged head whose tip cannot be told apart from the
// default branch's history is held back as unknown: with no origin/main to
// compare, or when git cannot answer.
func TestBehindTipWithoutTheDefaultBranchIsUnknown(t *testing.T) {
	l := newLab(t)
	l.branch("behind")
	run(t, l.server, "fetch", "-q", "origin")
	run(t, l.server, "checkout", "-q", "-B", "elsewhere", "origin/behind")
	l.write(l.server, "behind.txt", "one\ntwo\nthree\n")
	run(t, l.server, "commit", "-q", "-am", "from elsewhere")
	run(t, l.server, "push", "-q", "origin", "elsewhere:behind")
	pr := l.pr("behind", "squash")
	if items, _ := l.scan(nil, nil); items["behind"].Branch != "behind" {
		t.Fatal("behind: not listed with origin/main known")
	}
	git := gitsrc.NewInventory(gitsrc.Options{})
	inv, err := git.Inventory(context.Background(), l.repo)
	if err != nil {
		t.Fatal(err)
	}
	tip := run(t, l.repo, "rev-parse", "behind")
	base := run(t, l.repo, "rev-parse", "origin/main")
	failing := failAncestry{git, tip, base}
	_, held := Scan(context.Background(), failing, Repo{Root: l.repo, PRs: l.prs, ProcsKnown: true}, inv)
	if len(held) != 1 || held[0].Reason != ReasonUnknown {
		t.Fatalf("git failing: held %+v", held)
	}
	run(t, l.repo, "update-ref", "-d", "refs/remotes/origin/main")
	if inv, err = git.Inventory(context.Background(), l.repo); err != nil {
		t.Fatal(err)
	}
	// A git that answers "no" for a commit it was not given, rather than failing.
	items, held := Scan(context.Background(), lenientAncestry{git}, Repo{Root: l.repo, PRs: l.prs, ProcsKnown: true}, inv)
	if len(items) != 0 || len(held) != 1 || held[0].Reason != ReasonUnknown || held[0].PR.Number != pr.Number {
		t.Fatalf("no origin/main: items %+v held %+v", items, held)
	}
}

// Merged work whose ancestry git cannot tell is counted as held back
// (unknown), never left out quietly: a pull request head that is not here, a
// failing probe on either side. A stash whose base cannot be read is
// unknown too, whatever its subject says.
func TestUnreadableEvidenceIsCountedUnknown(t *testing.T) {
	l := newLab(t)
	l.branch("gone")
	l.pr("gone", "squash")
	l.branch("fail")
	fail := l.pr("fail", "squash")
	l.write(l.wt("fail"), "more.txt", "after\n")
	run(t, l.wt("fail"), "add", "more.txt")
	run(t, l.wt("fail"), "commit", "-q", "-m", "after the merge, so the tip is not the head")
	l.branch("reused")
	reused := l.pr("reused", "squash")
	run(t, l.repo, "worktree", "remove", "--force", l.wt("reused"))
	run(t, l.repo, "branch", "-D", "reused")
	run(t, l.repo, "worktree", "add", "-q", "-b", "reused", l.wt("reused"), "origin/main")
	l.branch("half")
	half := l.pr("half", "squash")
	run(t, l.repo, "worktree", "remove", "--force", l.wt("half"))
	run(t, l.repo, "branch", "-D", "half")
	run(t, l.repo, "worktree", "add", "-q", "-b", "half", l.wt("half"), "origin/main")
	l.prs[0].HeadOID = strings.Repeat("1", 40) // gone: its head commit is not in this clone
	git := gitsrc.NewInventory(gitsrc.Options{})
	inv, err := git.Inventory(context.Background(), l.repo)
	if err != nil {
		t.Fatal(err)
	}
	tipOf := func(b string) string { return run(t, l.repo, "rev-parse", b) }
	failing := failTwo{git, map[[2]string]bool{
		{tipOf("fail"), fail.HeadOID}: true, {fail.HeadOID, tipOf("fail")}: true, // fail: both probes
		{reused.HeadOID, tipOf("reused")}: true, // reused: the second probe
		{tipOf("half"), half.HeadOID}:     true, // half: the first probe only
	}}
	items, held := Scan(context.Background(), failing, Repo{Root: l.repo, PRs: l.prs, ProcsKnown: true}, inv)
	got := map[string]Reason{}
	for _, h := range held {
		got[h.Branch] = h.Reason
	}
	if len(items) != 0 || len(got) != 4 || got["gone"] != ReasonUnknown || got["fail"] != ReasonUnknown || got["reused"] != ReasonUnknown ||
		got["half"] != ReasonUnknown {
		t.Fatalf("items %+v held %+v", items, held)
	}

	br := model.Branch{Name: "feat", Tip: tipOf("main")}
	for _, st := range [][]model.Stash{
		{{Base: "", Subject: "On other: wip"}},
		{{Base: "", Subject: "a message of its own"}},
	} {
		if got := stashed(func(string, string) (bool, bool) { return false, true }, st, br, tipOf("origin/main")); got != unknown {
			t.Errorf("stash %+v: %v", st, got)
		}
	}
}

// failTwo is git, except that it cannot answer for the given pairs.
type failTwo struct {
	source.RepoInventory
	pairs map[[2]string]bool
}

func (f failTwo) IsAncestor(ctx context.Context, root, a, b string) (bool, error) {
	if f.pairs[[2]string{a, b}] {
		return false, errors.New("git failed")
	}
	return f.RepoInventory.IsAncestor(ctx, root, a, b)
}

// lenientAncestry answers false, not an error, for an empty commit id.
type lenientAncestry struct{ source.RepoInventory }

func (l lenientAncestry) IsAncestor(ctx context.Context, root, a, b string) (bool, error) {
	if a == "" || b == "" {
		return false, nil
	}
	return l.RepoInventory.IsAncestor(ctx, root, a, b)
}

// failAncestry is git, except that it cannot answer whether a is in b.
type failAncestry struct {
	source.RepoInventory
	a, b string
}

func (f failAncestry) IsAncestor(ctx context.Context, root, a, b string) (bool, error) {
	if a == f.a && b == f.b {
		return false, errors.New("git failed")
	}
	return f.RepoInventory.IsAncestor(ctx, root, a, b)
}

// A branch checked out in two worktrees (worktree add --force) is held back,
// though each is clean: each checkout uses the branch the other would leave.
func TestBranchInTwoWorktreesIsHeld(t *testing.T) {
	l := newLab(t)
	l.branch("twice")
	l.pr("twice", "squash")
	second := l.wt("twice") + "-2"
	run(t, l.repo, "worktree", "add", "-q", "--force", second, "twice")
	items, held := l.scan(nil, nil)
	if _, ok := items["twice"]; ok {
		t.Fatalf("listed: %+v", items["twice"])
	}
	h := held["twice"]
	if h.Reason != ReasonInUse || len(h.Worktrees) != 2 {
		t.Fatalf("held %+v", h)
	}
}

// Each thing git reports from the folder must match on its own.
func TestSameWorktreeChecksEveryField(t *testing.T) {
	tip := strings.Repeat("a", 40)
	w := model.Worktree{Path: "/home/dev/wt", Branch: model.Of("feat")}
	br := model.Branch{Name: "feat", Tip: tip}
	good := model.TreeStatus{Top: "/home/dev/wt", GitDir: "/home/dev/r/.git/worktrees/wt", CommonDir: "/home/dev/r/.git",
		Head: "refs/heads/feat", Tip: tip}
	if !sameWorktree(good, w, "/home/dev/r/.git", br) {
		t.Fatal("the same worktree was not recognised")
	}
	if sameWorktree(good, w, "", br) {
		t.Error("no common folder in the inventory")
	}
	for name, change := range map[string]func(*model.TreeStatus){
		"top":        func(s *model.TreeStatus) { s.Top = "/home/dev/r" },
		"git folder": func(s *model.TreeStatus) { s.GitDir = "/home/dev/r/.git" },
		"common":     func(s *model.TreeStatus) { s.CommonDir = "/home/dev/other/.git" },
		"head":       func(s *model.TreeStatus) { s.Head = "refs/heads/other" },
		"detached":   func(s *model.TreeStatus) { s.Head = "" },
		"tip":        func(s *model.TreeStatus) { s.Tip = strings.Repeat("b", 40) },
	} {
		st := good
		change(&st)
		if sameWorktree(st, w, "/home/dev/r/.git", br) {
			t.Errorf("%s: another worktree taken for this one", name)
		}
	}
}

// A fork's pull request carries no head commit, so a branch of the same name
// here is never matched to it.
func TestForkPullRequestListsNothing(t *testing.T) {
	l := newLab(t)
	l.branch("fx")
	l.pr("fx", "merge")
	l.prs[0].HeadOID = "" // what the GitHub adapter gives a fork's pull request
	items, held := l.scan(nil, nil)
	if len(items) != 0 || len(held) != 0 {
		t.Fatalf("items %+v held %+v", items, held)
	}
}

// A worktree with another checkout beneath it, of this repo or another, is
// held back: that checkout's files are inside it, whatever they hold.
func TestWorktreeHoldingAnotherCheckoutIsHeld(t *testing.T) {
	l := newLab(t)
	for _, b := range []string{"parent", "host", "plain"} {
		l.branch(b)
		l.pr(b, "squash")
	}
	l.write(l.repo, ".git/info/exclude", ".claude/\nvendor/\n")
	// A worktree of this repo, on a branch of its own, with edits.
	child := filepath.Join(l.wt("parent"), ".claude", "worktrees", "child")
	run(t, l.repo, "worktree", "add", "-q", "-b", "child", child, "origin/main")
	l.write(child, "notes.md", "work in progress\n")
	// Another repo's checkout the board knows.
	other := filepath.Join(l.wt("host"), "vendor", "lib")
	l.checkouts = []string{filepath.Join(l.dir, "elsewhere"), other}
	items, held := l.scan(nil, nil)
	for _, b := range []string{"parent", "host"} {
		if _, ok := items[b]; ok {
			t.Errorf("%s: listed: %+v", b, items[b])
		}
		if h := held[b]; h.Reason != ReasonNested {
			t.Errorf("%s: held %+v, want nested", b, h)
		}
	}
	if it := items["plain"]; it.Worktree != l.wt("plain") {
		t.Errorf("plain: %+v", it)
	}
}

// While some repo's checkouts could not all be listed, a worktree with
// ignored files may hide one in them: it is held back. One without ignored
// files would show such a checkout as untracked: it is listed.
func TestUnlistedCheckoutsHoldWorktreesWithIgnoredFiles(t *testing.T) {
	l := newLab(t)
	for _, b := range []string{"ignored", "plain"} {
		l.branch(b)
		l.pr(b, "squash")
	}
	l.write(l.repo, ".git/info/exclude", "vendor/\n")
	l.write(l.wt("ignored"), "vendor/lib.txt", "x\n")
	l.checkoutsUnknown = true
	items, held := l.scan(nil, nil)
	if _, ok := items["ignored"]; ok || held["ignored"].Reason != ReasonUnknown {
		t.Errorf("ignored: listed %+v, held %+v", items["ignored"], held["ignored"])
	}
	if it := items["plain"]; it.Worktree != l.wt("plain") {
		t.Errorf("plain: %+v", it)
	}
	l.checkoutsUnknown = false
	if items, _ := l.scan(nil, nil); items["ignored"].Worktree != l.wt("ignored") {
		t.Errorf("all listed: ignored %+v", items["ignored"])
	}
}

// Waiting exempts only the leftover session's own processes: any other
// process in its worktree holds the worktree as in use.
func TestWaitingExemptsOnlyTheSessionsProcesses(t *testing.T) {
	l := newLab(t)
	for _, b := range []string{"shared", "own"} {
		l.branch(b)
		l.pr(b, "squash")
	}
	procs := []model.Proc{
		{PID: 10, PPID: 9, Cwd: l.wt("shared")},                       // the session
		{PID: 20, PPID: 1, Cwd: filepath.Join(l.wt("shared"), "web")}, // a build of someone else's
		{PID: 30, PPID: 29, Cwd: l.wt("own")},                         // the other session
		{PID: 31, PPID: 30, Cwd: filepath.Join(l.wt("own"), "x")},     // one it started
	}
	items, held := l.scanProcs(procs, []Sitter{
		{Dir: l.wt("shared"), Own: map[int]bool{10: true}},
		{Dir: l.wt("own"), Own: map[int]bool{30: true, 31: true}},
	})
	if h, ok := held["shared"]; !ok || h.Reason != ReasonInUse {
		t.Errorf("shared: held %+v, items %+v", h, items["shared"])
	}
	if it := items["own"]; !it.Waiting || it.Worktree != l.wt("own") {
		t.Errorf("own: %+v (held %+v)", it, held["own"])
	}
}

// A branch on GitHub that an open pull request builds on (its base) is in
// use: it is not listed, and alone it is held back.
func TestBaseOfAnOpenPullRequestIsNotListed(t *testing.T) {
	l := newLab(t)
	for _, b := range []string{"stackbase", "ghbase", "plain"} {
		l.branch(b)
		l.pr(b, "squash")
	}
	run(t, l.repo, "worktree", "remove", l.wt("ghbase"))
	run(t, l.repo, "branch", "-D", "ghbase")
	l.bases = map[string]bool{"stackbase": true, "ghbase": true}
	items, held := l.scan(nil, nil)
	if it := items["stackbase"]; it.Remote || it.Worktree != l.wt("stackbase") {
		t.Errorf("stackbase: %+v", it)
	}
	if it, ok := items["remote:ghbase"]; ok {
		t.Errorf("ghbase listed: %+v", it)
	}
	if h := held["ghbase"]; h.Reason != ReasonInUse {
		t.Errorf("ghbase: held %+v", h)
	}
	if it := items["plain"]; !it.Remote {
		t.Errorf("plain: %+v", it)
	}
}

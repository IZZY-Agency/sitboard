package gitsrc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// git runs git in dir with a fixed test identity passed through the
// environment, so no git setting is read or written.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testEnv(
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// testEnv is the environment of the git the tests start themselves: the
// caller's without any GIT_ variable (GIT_DIR, GIT_INDEX_FILE and the like
// would send it to another repository when the tests run from a git hook),
// without the user's git config, plus extra.
func testEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"), extra...)
}

// newRepo makes a repo with one commit on branch main.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func TestResolveRepoRoot(t *testing.T) {
	repo := newRepo(t)
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := New(Options{}).Resolve(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if r.Root != repo || r.MainRoot != repo {
		t.Fatalf("Root=%q MainRoot=%q, want %q", r.Root, r.MainRoot, repo)
	}
	if b, ok := r.Branch.Get(); !ok || b != "main" {
		t.Fatalf("Branch = %v, want main", r.Branch)
	}
}

func TestWorktreeBelongsToMainRepo(t *testing.T) {
	repo := newRepo(t)
	wt := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-wt")
	git(t, repo, "worktree", "add", "-q", "-b", "feature/x", wt)
	t.Cleanup(func() { _ = os.RemoveAll(wt) })

	r, err := New(Options{}).Resolve(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	if r.Root != wt {
		t.Errorf("Root = %q, want %q", r.Root, wt)
	}
	if r.MainRoot != repo {
		t.Errorf("MainRoot = %q, want %q", r.MainRoot, repo)
	}
	if b, _ := r.Branch.Get(); b != "feature/x" {
		t.Errorf("Branch = %v", r.Branch)
	}
}

func TestDetachedHeadBranchUnknown(t *testing.T) {
	repo := newRepo(t)
	git(t, repo, "checkout", "-q", "--detach")
	r, err := New(Options{}).Resolve(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if r.Branch.OK {
		t.Fatalf("Branch = %v, want unknown", r.Branch)
	}
}

func TestSlugFromHTTPSAndSSHRemotes(t *testing.T) {
	cases := map[string]string{
		"https://github.com/example-org/example-repo.git":   "example-org/example-repo",
		"https://github.com/example-org/example-repo":       "example-org/example-repo",
		"git@github.com:example-org/example-repo.git":       "example-org/example-repo",
		"ssh://git@github.com/example-org/example-repo.git": "example-org/example-repo",
		"https://gitlab.com/example-org/example-repo.git":   "",
		"https://github.com/example-org":                    "",
		"https://github.com/../example-repo":                "",
		"":                                                  "",
	}
	for url, want := range cases {
		if got := slugFromURL(url); got != want {
			t.Errorf("slugFromURL(%q) = %q, want %q", url, got, want)
		}
	}

	repo := newRepo(t)
	git(t, repo, "remote", "add", "origin", "git@github.com:example-org/example-repo.git")
	r, err := New(Options{}).Resolve(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if r.Remote.Key() != "example-org/example-repo" {
		t.Fatalf("Slug = %q", r.Remote.Key())
	}
}

func TestFolderOutsideRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	r, err := New(Options{}).Resolve(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Root != dir || r.MainRoot != dir || r.Remote.Key() != "" || r.Branch.OK {
		t.Fatalf("got %+v", r)
	}
}

func TestMissingFolderIsAnError(t *testing.T) {
	_, err := New(Options{}).Resolve(context.Background(), filepath.Join(t.TempDir(), "gone"))
	if err == nil {
		t.Fatal("want an error for a folder that does not exist")
	}
}

func TestInventoryListsWorktreesBranchesAndStashes(t *testing.T) {
	repo := newRepo(t)
	git(t, repo, "branch", "spare")
	wt := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-wt2")
	git(t, repo, "worktree", "add", "-q", "-b", "worker-a", wt)
	t.Cleanup(func() { _ = os.RemoveAll(wt) })
	git(t, repo, "worktree", "lock", wt)
	det := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-det")
	git(t, repo, "worktree", "add", "-q", "--detach", det)
	t.Cleanup(func() { _ = os.RemoveAll(det) })
	git(t, repo, "update-ref", "refs/remotes/origin/gone-upstream", "HEAD")
	git(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/gone-upstream")
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", "a.txt")
	git(t, wt, "stash", "push", "-q", "-m", "keep me")

	inv, err := NewInventory(Options{}).Inventory(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]model.Worktree{}
	for _, w := range inv.Worktrees {
		byPath[w.Path] = w
	}
	if len(inv.Worktrees) != 3 || !inv.Worktrees[0].Main || inv.Worktrees[0].Path != repo || byPath[wt].Main {
		t.Fatalf("worktrees = %+v", inv.Worktrees)
	}
	if b, _ := byPath[wt].Branch.Get(); b != "worker-a" || !byPath[wt].Locked {
		t.Fatalf("worktree = %+v", byPath[wt])
	}
	if d, ok := byPath[det]; !ok || d.Branch.OK || d.Locked {
		t.Fatalf("detached worktree = %+v", d)
	}
	var names []string
	for _, b := range inv.Branches {
		if len(b.Tip) != 40 {
			t.Fatalf("tip of %s = %q", b.Name, b.Tip)
		}
		names = append(names, b.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"main", "spare", "worker-a"}) {
		t.Fatalf("branches = %v", names)
	}
	if len(inv.Remote) != 1 || inv.Remote[0].Name != "gone-upstream" || len(inv.Remote[0].Tip) != 40 {
		t.Fatalf("remote = %v", inv.Remote)
	}
	tip := strings.TrimSpace(gitOut(t, repo, "rev-parse", "worker-a"))
	if len(inv.Stashes) != 1 || inv.Stashes[0].Base != tip || inv.Stashes[0].Subject != "On worker-a: keep me" {
		t.Fatalf("stashes = %+v", inv.Stashes)
	}
}

// Inventory names origin's repository on GitHub when it has one fetch
// address there.
func TestInventorySlug(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	cases := []struct {
		name, slug string
		config     [][]string
	}{
		{"none", "", nil},
		{"scp", "example-org/app", [][]string{{"remote.origin.url", "git@github.com:example-org/app"}}},
		{"https", "example-org/app", [][]string{{"remote.origin.url", "https://github.com/example-org/app.git"}}},
		{"ssh", "example-org/app", [][]string{{"remote.origin.url", "ssh://git@github.com/example-org/app"}}},
		{"elsewhere", "", [][]string{{"remote.origin.url", "git@example.com:example-org/app.git"}}},
		{"gitlab.com", "", [][]string{{"remote.origin.url", "https://gitlab.com/example-group/sub/app.git"}}},
		{"gitlab ssh port", "", [][]string{{"remote.origin.url", "ssh://git@example.com:2222/example-group/app.git"}}},
		{"web port kept", "", [][]string{{"remote.origin.url", "https://gitlab.example.com:8443/example-group/app.git"}}},
		{"web port 443 written out", "", [][]string{{"remote.origin.url", "https://gitlab.example.com:443/example-group/app.git"}}},
		{"subfolder in the path", "", [][]string{{"remote.origin.url", "https://example.com/gitlab/example-group/app.git"}}},
		{"github three parts", "", [][]string{{"remote.origin.url", "https://github.com/example-org/app/extra.git"}}},
		{"local path", "", [][]string{{"remote.origin.url", "/srv/git/app.git"}}},
		{"two addresses", "", [][]string{{"remote.origin.url", "git@github.com:example-org/app.git"},
			{"--add", "remote.origin.url", "git@github.com:example-org/fork.git"}}},
		{"two addresses elsewhere", "", [][]string{{"remote.origin.url", "git@example.com:example-org/app.git"},
			{"--add", "remote.origin.url", "git@example.com:example-org/fork.git"}}},
		{"pushurl ignored", "example-org/app", [][]string{
			{"remote.origin.url", "git@github.com:example-org/app.git"}, {"remote.origin.pushurl", "git@github.com:example-org/fork.git"}}},
		// The effective fetch address: insteadOf rewrites are followed both ways.
		{"rewritten onto GitHub", "example-org/app", [][]string{
			{"remote.origin.url", "mirror:app"}, {"url.https://github.com/example-org/app.git.insteadOf", "mirror:app"}}},
		{"rewritten away from GitHub", "", [][]string{
			{"remote.origin.url", "https://github.com/example-org/app.git"}, {"url.https://example.com/.insteadOf", "https://github.com/"}}},
		{"pushInsteadOf ignored", "example-org/app", [][]string{
			{"remote.origin.url", "https://github.com/example-org/app.git"}, {"url.https://example.com/.pushInsteadOf", "https://github.com/"}}},
	}
	// Resolve's Remote where it is not the slug: a remote the core may find
	// on GitLab (it asks glab about the host); the inventory stays GitHub only.
	remotes := map[string]string{
		"elsewhere":                  "gitlab:example.com/example-org/app",
		"gitlab.com":                 "gitlab:gitlab.com/example-group/sub/app",
		"gitlab ssh port":            "gitlab:example.com/example-group/app",
		"web port kept":              "gitlab:gitlab.example.com:8443/example-group/app",
		"web port 443 written out":   "gitlab:gitlab.example.com/example-group/app", // https's own port is no port
		"subfolder in the path":      "gitlab:example.com/gitlab/example-group/app",
		"rewritten away from GitHub": "gitlab:example.com/example-org/app",
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := newRepo(t)
			for _, kv := range c.config {
				git(t, repo, append([]string{"config"}, kv...)...)
			}
			inv, err := NewInventory(Options{}).Inventory(context.Background(), repo)
			if err != nil {
				t.Fatal(err)
			}
			if inv.Origin.Key() != c.slug {
				t.Errorf("inventory slug %q, want %q", inv.Origin.Key(), c.slug)
			}
			// Resolve reads the same address the same way.
			got, err := New(Options{}).Resolve(context.Background(), repo)
			if err != nil {
				t.Fatal(err)
			}
			want := c.slug
			if r, ok := remotes[c.name]; ok {
				want = r
			}
			if got.Remote.Key() != want {
				t.Errorf("resolve remote %q, want %q", got.Remote.Key(), want)
			}
			// Only an ssh, git or scp-like GitLab address is bare.
			wantBare := c.name == "elsewhere" || c.name == "gitlab ssh port"
			if got.RemoteBare != wantBare {
				t.Errorf("bare %v, want %v", got.RemoteBare, wantBare)
			}
			if inv.Origin.Forge != model.ForgeGitHub && !inv.Origin.IsZero() {
				t.Errorf("inventory origin %+v: GitHub only", inv.Origin)
			}
			// More than one address, one on GitHub: a GitHub repo whose
			// name is unknown.
			if want := c.name == "two addresses"; got.OriginUnclear != want {
				t.Errorf("resolve origin unclear %v, want %v", got.OriginUnclear, want)
			}
		})
	}
}

// Symbolic refs under refs/heads, and the branches they point at, are marked.
func TestInventoryMarksSymbolicBranches(t *testing.T) {
	repo := newRepo(t)
	git(t, repo, "branch", "target")
	git(t, repo, "branch", "plain")
	git(t, repo, "symbolic-ref", "refs/heads/alias", "refs/heads/target")
	inv, err := NewInventory(Options{}).Inventory(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, b := range inv.Branches {
		got[b.Name] = b.Symbolic
	}
	want := map[string]bool{"main": false, "plain": false, "target": true, "alias": true}
	if len(got) != len(want) {
		t.Fatalf("branches %v", got)
	}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("%s: symbolic %v, want %v", n, got[n], w)
		}
	}
}

func TestIsAncestor(t *testing.T) {
	repo := newRepo(t)
	first := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "two")
	second := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	inv := NewInventory(Options{})
	ctx := context.Background()
	if ok, err := inv.IsAncestor(ctx, repo, first, second); err != nil || !ok {
		t.Fatalf("first in second: %v %v", ok, err)
	}
	if ok, err := inv.IsAncestor(ctx, repo, second, second); err != nil || !ok {
		t.Fatalf("a commit is its own ancestor: %v %v", ok, err)
	}
	if ok, err := inv.IsAncestor(ctx, repo, second, first); err != nil || ok {
		t.Fatalf("second in first: %v %v", ok, err)
	}
	missing := strings.Repeat("1", 40)
	if _, err := inv.IsAncestor(ctx, repo, missing, second); err == nil {
		t.Fatal("a missing commit must be an error, not false")
	}
	if _, err := inv.IsAncestor(ctx, repo, "HEAD", second); err == nil {
		t.Fatal("only full commit ids are passed to git")
	}
}

func TestTreeStatus(t *testing.T) {
	repo := newRepo(t)
	inv := NewInventory(Options{})
	ctx := context.Background()
	write := func(name string) {
		t.Helper()
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if st, err := inv.TreeStatus(ctx, repo); err != nil || !st.Clean || st.Ignored != 0 {
		t.Fatalf("fresh repo: %+v %v", st, err)
	}
	write(".gitignore")
	git(t, repo, "add", ".gitignore")
	git(t, repo, "commit", "-q", "-m", "ignore")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".env\nnode_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "commit", "-q", "-am", "ignore")
	write(".env")
	write("node_modules/a/b.js")
	write("node_modules/c.js")
	if st, err := inv.TreeStatus(ctx, repo); err != nil || !st.Clean || st.Ignored != 2 {
		t.Fatalf("ignored only: %+v %v", st, err)
	}
	write("notes.md")
	if st, _ := inv.TreeStatus(ctx, repo); st.Clean {
		t.Fatal("an untracked file is not clean")
	}
	if err := os.Remove(filepath.Join(repo, "notes.md")); err != nil {
		t.Fatal(err)
	}
	write(".gitignore")
	if st, _ := inv.TreeStatus(ctx, repo); st.Clean {
		t.Fatal("a modified file is not clean")
	}
}

// TreeStatus says where git found the worktree and what is checked out.
func TestTreeStatusIdentity(t *testing.T) {
	repo := newRepo(t)
	wt := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-id")
	git(t, repo, "worktree", "add", "-q", "-b", "feat", wt)
	t.Cleanup(func() { _ = os.RemoveAll(wt) })
	inv := NewInventory(Options{})
	st, err := inv.TreeStatus(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	tip := strings.TrimSpace(gitOut(t, wt, "rev-parse", "HEAD"))
	common := filepath.Join(repo, ".git")
	if st.Top != wt || st.CommonDir != common || st.GitDir != filepath.Join(common, "worktrees", filepath.Base(wt)) ||
		st.Head != "refs/heads/feat" || st.Tip != tip {
		t.Fatalf("status = %+v", st)
	}
	full, err := inv.Inventory(context.Background(), repo)
	if err != nil || full.CommonDir != common {
		t.Fatalf("inventory common dir %q, %v", full.CommonDir, err)
	}
	if _, err := inv.Inventory(context.Background(), wt); err == nil {
		t.Fatal("a linked worktree was read as a main checkout")
	}
	git(t, wt, "checkout", "-q", "--detach")
	if st, err := inv.TreeStatus(context.Background(), wt); err != nil || st.Head != "" {
		t.Fatalf("detached: %+v %v", st, err)
	}
}

// What git status does not show makes TreeStatus an error, not clean: files
// git was told to overlook, submodules, a folder git cannot read.
func TestTreeStatusCannotTell(t *testing.T) {
	ctx := context.Background()
	inv := NewInventory(Options{})
	setup := func(t *testing.T) string {
		repo := newRepo(t)
		for _, f := range []string{"a", "b"} {
			if err := os.WriteFile(filepath.Join(repo, f), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git(t, repo, "add", "a", "b")
		git(t, repo, "commit", "-q", "-m", "files")
		if st, err := inv.TreeStatus(ctx, repo); err != nil || !st.Clean {
			t.Fatalf("clean repo: %+v %v", st, err)
		}
		return repo
	}
	edit := func(t *testing.T, repo string) {
		if err := os.WriteFile(filepath.Join(repo, "a"), []byte("edited"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]func(t *testing.T, repo string){
		"assume-unchanged": func(t *testing.T, repo string) {
			git(t, repo, "update-index", "--assume-unchanged", "a")
			edit(t, repo)
		},
		"skip-worktree": func(t *testing.T, repo string) {
			git(t, repo, "update-index", "--skip-worktree", "a")
			edit(t, repo)
		},
		"submodule": func(t *testing.T, repo string) {
			head := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
			git(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+head+",sub")
			git(t, repo, "commit", "-q", "-m", "sub")
		},
		"filter attribute": func(t *testing.T, repo string) {
			// status would run the clean filter on a file whose stat
			// changed: a program the repo's config names.
			marker := filepath.Join(t.TempDir(), "ran")
			git(t, repo, "config", "filter.x.clean", "touch '"+marker+"'; cat")
			if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("a filter=x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git(t, repo, "add", ".gitattributes")
			git(t, repo, "commit", "-q", "-m", "attrs")
			if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			later := time.Unix(2_000_000_000, 0)
			if err := os.Chtimes(filepath.Join(repo, "a"), later, later); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := os.Stat(marker); err == nil {
					t.Error("the clean filter ran")
				}
			})
		},
		"unreadable folder": func(t *testing.T, repo string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads every folder")
			}
			dir := filepath.Join(repo, "secret")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			repo := setup(t)
			change(t, repo)
			if st, err := inv.TreeStatus(ctx, repo); err == nil {
				t.Fatalf("read as %+v", st)
			}
		})
	}
}

// A partial clone may fetch a missing object while it is read: its
// inventory is an error, so nothing of it is listed.
func TestInventoryOfAPartialCloneIsAnError(t *testing.T) {
	for name, cfg := range map[string][2]string{
		"promisor remote": {"remote.origin.promisor", "true"},
		"partial clone":   {"extensions.partialClone", "origin"},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newRepo(t)
			if inv, err := NewInventory(Options{}).Inventory(context.Background(), repo); err != nil || !inv.WorktreesListed {
				t.Fatalf("plain repo: %v, listed %v", err, inv.WorktreesListed)
			}
			git(t, repo, "config", cfg[0], cfg[1])
			inv, err := NewInventory(Options{}).Inventory(context.Background(), repo)
			if err == nil {
				t.Fatalf("read as %+v", inv)
			}
			// Its checkouts are still given, all of them: one may sit inside
			// another.
			if len(inv.Worktrees) != 1 || inv.Worktrees[0].Path != repo || !inv.WorktreesListed {
				t.Fatalf("worktrees %+v, listed %v", inv.Worktrees, inv.WorktreesListed)
			}
		})
	}
}

// Variables a parent git sets (git runs hooks and aliases with them) never
// send sitboard's reads to another repository, index or config.
func TestInheritedGitVariablesAreDropped(t *testing.T) {
	repo := newRepo(t)
	git(t, repo, "branch", "only-here")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "f")
	git(t, repo, "commit", "-q", "-m", "a tracked file: read with another index, it is gone")
	decoy := newRepo(t)
	git(t, decoy, "branch", "only-there")
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, "other-index"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(decoy, ".git", "objects"))
	t.Setenv("GIT_CONFIG_PARAMETERS", "'remote.origin.promisor'='true'")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "extensions.partialClone")
	t.Setenv("GIT_CONFIG_VALUE_0", "origin")
	inv, err := NewInventory(Options{}).Inventory(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, b := range inv.Branches {
		names = append(names, b.Name)
	}
	if inv.CommonDir != filepath.Join(repo, ".git") || !slices.Contains(names, "only-here") || slices.Contains(names, "only-there") {
		t.Fatalf("read %s: %v", inv.CommonDir, names)
	}
	if st, err := NewInventory(Options{}).TreeStatus(context.Background(), repo); err != nil || st.Top != repo || !st.Clean {
		t.Fatalf("status %+v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(decoy, "other-index")); err == nil {
		t.Fatal("the other index was written")
	}
}

// GIT_CONFIG sends `git config` (only it) to another file, GIT_CONFIG_GLOBAL
// and GIT_CONFIG_SYSTEM replace the user's and the system's: the probes that
// read the origin URL and the promisor settings still see the repository's
// own config and the user's.
func TestInheritedGitConfigFileIsDropped(t *testing.T) {
	repo := newRepo(t)
	git(t, repo, "config", "remote.origin.url", "https://github.com/example-org/app.git")
	decoy := filepath.Join(t.TempDir(), "decoy-config")
	if err := os.WriteFile(decoy, []byte("[remote \"origin\"]\n\turl = https://github.com/example-org/decoy.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG", decoy)
	t.Setenv("GIT_CONFIG_GLOBAL", decoy)
	t.Setenv("GIT_CONFIG_SYSTEM", decoy)
	got, err := New(Options{}).Resolve(context.Background(), repo)
	if err != nil || got.Remote.Key() != "example-org/app" {
		t.Fatalf("resolved %+v %v", got, err)
	}
	// The inventory reads the same one address: a second one from the decoy
	// would leave it no slug.
	if inv, err := NewInventory(Options{}).Inventory(context.Background(), repo); err != nil || inv.Origin.Key() != "example-org/app" {
		t.Fatalf("inventory %+v %v", inv, err)
	}
	git(t, repo, "config", "remote.origin.promisor", "true")
	if inv, err := NewInventory(Options{}).Inventory(context.Background(), repo); err == nil {
		t.Fatalf("a promisor remote hidden by GIT_CONFIG was read as %+v", inv)
	}
}

// Every git call runs with lazy fetching off.
func TestGitRunsWithoutLazyFetch(t *testing.T) {
	repo := newRepo(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	envFile := filepath.Join(t.TempDir(), "env")
	bin := writeScript(t, "#!/bin/sh\nenv > '"+envFile+"'\nexec '"+real+"' \"$@\"\n")
	if _, err := New(Options{Bin: bin}).Resolve(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(strings.Split(string(env), "\n"), "GIT_NO_LAZY_FETCH=1") {
		t.Fatal("GIT_NO_LAZY_FETCH=1 not set")
	}
}

// The worktree listing is read in NUL-ended records: a path may hold a
// newline, and a listing cut short, or one that does not start a record
// with its path, is an error, never a shorter list.
func TestParseWorktrees(t *testing.T) {
	ok := "worktree /r\x00HEAD " + strings.Repeat("a", 40) + "\x00branch refs/heads/main\x00\x00" +
		"worktree /r/w\nx\x00HEAD " + strings.Repeat("b", 40) + "\x00branch refs/heads/feat\x00locked a\nb\x00\x00" +
		"worktree /r/d\x00HEAD " + strings.Repeat("c", 40) + "\x00detached\x00prunable gone\x00\x00"
	wts, err := parseWorktrees([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 3 || wts[0].Path != "/r" || !wts[0].Main || wts[0].Branch.V != "main" ||
		wts[1].Path != "/r/w\nx" || wts[1].Main || wts[1].Branch.V != "feat" || !wts[1].Locked ||
		wts[2].Path != "/r/d" || wts[2].Branch.OK || !wts[2].Prunable {
		t.Fatalf("wts = %+v", wts)
	}
	for name, out := range map[string]string{
		"empty":                "",
		"cut in a record":      ok[:len(ok)-1],
		"cut in a field":       ok[:len(ok)-5],
		"no record terminator": "worktree /r\x00HEAD " + strings.Repeat("a", 40) + "\x00",
		"field before a path":  "HEAD " + strings.Repeat("a", 40) + "\x00worktree /r\x00\x00",
		"relative path":        "worktree r\x00\x00",
		"newline-ended lines":  "worktree /r\nHEAD " + strings.Repeat("a", 40) + "\nbranch refs/heads/main\n\n",
	} {
		if wts, err := parseWorktrees([]byte(out)); err == nil {
			t.Errorf("%s: read as %+v", name, wts)
		}
	}
}

// An origin with several addresses, one on GitHub, is unclear in the
// inventory too.
func TestInventoryOfAnUnclearOrigin(t *testing.T) {
	repo := newRepo(t)
	git(t, repo, "remote", "add", "origin", "https://gitlab.example.com/example-group/beta.git")
	git(t, repo, "remote", "set-url", "--add", "origin", "https://github.com/example-org/beta.git")
	inv, err := NewInventory(Options{}).Inventory(context.Background(), repo)
	if err != nil || !inv.OriginUnclear || !inv.Origin.IsZero() {
		t.Fatalf("%v: origin %+v, unclear %v", err, inv.Origin, inv.OriginUnclear)
	}
}

// A GitHub origin read before a later read fails is still given, and every
// git call runs with signatures not shown (no verifier is run).
func TestInventoryKeepsTheOriginWhenTheStashesFail(t *testing.T) {
	var calls [][]string
	r := &resolver{bin: "git", timeout: callTimeout, run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		switch {
		case slices.Contains(args, "rev-parse"):
			return []byte("/r\n/r/.git\n/r/.git\n"), nil
		case slices.Contains(args, "config"):
			return nil, &runner.ExitError{Code: 1}
		case slices.Contains(args, "worktree"):
			return []byte("worktree /r\x00HEAD " + strings.Repeat("a", 40) + "\x00branch refs/heads/main\x00\x00"), nil
		case slices.Contains(args, "for-each-ref"):
			return nil, nil
		case slices.Contains(args, "remote"):
			return []byte("https://github.com/example-org/beta.git\n"), nil
		case slices.Contains(args, "stash"):
			return nil, &runner.ExitError{Code: 128}
		}
		t.Fatalf("unexpected git %v", args)
		return nil, nil
	}}
	inv, err := r.Inventory(context.Background(), "/r")
	if err == nil || inv.Origin != model.GitHubRepo("example-org/beta") {
		t.Fatalf("%v: origin %+v", err, inv.Origin)
	}
	for _, a := range calls {
		if i := slices.Index(a, "log.showSignature=false"); i < 1 || a[i-1] != "-c" {
			t.Errorf("git %v shows signatures", a)
		}
	}
}

// A worktree listing cut short makes the inventory an error.
func TestInventoryWithACutWorktreeListIsAnError(t *testing.T) {
	r := &resolver{bin: "git", timeout: callTimeout, run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case slices.Contains(args, "rev-parse"):
			return []byte("/r\n/r/.git\n/r/.git\n"), nil
		case slices.Contains(args, "config"):
			return nil, &runner.ExitError{Code: 1}
		case slices.Contains(args, "worktree"):
			return []byte("worktree /r\x00HEAD " + strings.Repeat("a", 40) + "\x00branch refs/heads/main\x00\x00worktree /r/w\x00HEAD"), nil
		}
		t.Fatalf("unexpected git %v", args)
		return nil, nil
	}}
	if inv, err := r.Inventory(context.Background(), "/r"); err == nil || inv.WorktreesListed {
		t.Fatalf("read as %+v", inv)
	}
}

// A worktree whose path holds a newline is never read as another path: it is
// listed exactly, on its own branch, the listing is whole, and git is never
// run in it.
func TestInventoryWithANewlineInAWorktreePath(t *testing.T) {
	repo := newRepo(t)
	base := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-w")
	git(t, repo, "worktree", "add", "-q", "-b", "other", base)
	git(t, repo, "worktree", "add", "-q", "-b", "feat", base+"\nx") // read by lines: base, on feat
	g := NewInventory(Options{})
	inv, err := g.Inventory(context.Background(), repo)
	if err != nil || !inv.WorktreesListed {
		t.Fatalf("inventory: %v, listed %v", err, inv.WorktreesListed)
	}
	on := map[string]string{}
	for _, w := range inv.Worktrees {
		on[w.Path] = w.Branch.V
	}
	if len(on) != 3 || on[base] != "other" || on[base+"\nx"] != "feat" {
		t.Fatalf("worktrees %q", on)
	}
	if _, err := g.TreeStatus(context.Background(), base+"\nx"); err == nil {
		t.Fatal("status read in a path with a newline")
	}
}

// Only git's own answer that a folder is in no repository makes it one
// outside: any other failure, or an answer that is not the one asked for,
// is an error (the folder may be in any repo).
func TestResolveTellsNoRepoFromNoAnswer(t *testing.T) {
	dir := t.TempDir()
	plain := &runner.ExitError{Code: 128, Stderr: []byte("fatal: not a git repository (or any of the parent directories): .git\n")}
	for name, c := range map[string]struct {
		revParse  []byte
		revErr    error
		headErr   error
		originErr error
		outside   bool
	}{
		"head unreadable": {revParse: []byte(dir + "\n" + dir + "/.git\n"), headErr: &runner.ExitError{Code: 128}},
		"no repo":         {revErr: plain, outside: true},
		"broken pointer":  {revErr: &runner.ExitError{Code: 128, Stderr: []byte("fatal: gitfile does not point to a valid repository: " + dir + "/.git\n")}},
		"not owned":       {revErr: &runner.ExitError{Code: 128, Stderr: []byte("fatal: detected dubious ownership in repository at '" + dir + "'\n")}},
		"other exit":      {revErr: &runner.ExitError{Code: 1, Stderr: plain.Stderr}},
		"one line":        {revParse: []byte(dir + "\n")},
		"relative":        {revParse: []byte(dir + "\n.git\n")},
		"origin unread":   {revParse: []byte(dir + "\n" + dir + "/.git\n"), originErr: &runner.ExitError{Code: 128, Stderr: []byte("fatal: bad config line 1\n")}},
		"origin missing":  {revParse: []byte(dir + "\n" + dir + "/.git\n"), originErr: &runner.ExitError{Code: 2}, outside: false},
	} {
		t.Run(name, func(t *testing.T) {
			r := &resolver{bin: "git", timeout: callTimeout, run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				switch {
				case slices.Contains(args, "rev-parse"):
					return c.revParse, c.revErr
				case slices.Contains(args, "symbolic-ref"):
					if c.headErr != nil {
						return nil, c.headErr
					}
					return []byte("refs/heads/main\n"), nil
				case slices.Contains(args, "remote"):
					return nil, c.originErr
				}
				return nil, &runner.ExitError{Code: 1}
			}}
			repo, err := r.Resolve(context.Background(), dir)
			switch {
			case name == "origin missing":
				if err != nil || repo.Root != dir || !repo.Remote.IsZero() || !repo.Branch.OK {
					t.Fatalf("got %+v, %v", repo, err)
				}
			case c.outside:
				if err != nil || repo.Root != dir || repo.MainRoot != dir || repo.Branch.OK {
					t.Fatalf("got %+v, %v", repo, err)
				}
			case err == nil:
				t.Fatalf("read as %+v", repo)
			}
		})
	}
}

// A real folder whose .git points nowhere is no answer, not a folder outside
// a repo.
func TestResolveOfABrokenGitPointerIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+filepath.Join(dir, "gone")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if repo, err := New(Options{}).Resolve(context.Background(), dir); err == nil {
		t.Fatalf("read as %+v", repo)
	}
}

// Every worktree's operation under way is read, on a branch or detached.
func TestInventoryReadsOperationsOnEveryWorktree(t *testing.T) {
	repo := newRepo(t)
	wt := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-op")
	git(t, repo, "worktree", "add", "-q", "-b", "feat", wt)
	t.Cleanup(func() { _ = os.RemoveAll(wt) })
	inv := NewInventory(Options{})
	read := func() model.Worktree {
		t.Helper()
		full, err := inv.Inventory(context.Background(), repo)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range full.Worktrees {
			if w.Path == wt {
				return w
			}
		}
		t.Fatal("worktree not listed")
		return model.Worktree{}
	}
	if w := read(); w.Operation || len(w.Busy) > 0 {
		t.Fatalf("nothing under way: %+v", w)
	}
	git(t, wt, "update-ref", "CHERRY_PICK_HEAD", "HEAD")
	if w := read(); !w.Operation || !w.Branch.OK {
		t.Fatalf("cherry-pick on a branch: %+v", w)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func TestGitCallTimesOutAt5s(t *testing.T) {
	r := &resolver{bin: "git", timeout: callTimeout}
	if callTimeout != 5*time.Second {
		t.Fatalf("callTimeout = %v, want 5s", callTimeout)
	}
	// A git that never answers: the call must end at the timeout, not hang.
	r.timeout = 50 * time.Millisecond
	r.run = nil
	slow := writeScript(t, "#!/bin/sh\nsleep 10\n")
	r.bin = slow
	start := time.Now()
	_, err := r.Resolve(context.Background(), t.TempDir())
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestHostileFoldersNeverReachGit(t *testing.T) {
	calls := 0
	r := &resolver{bin: "git", timeout: callTimeout, run: func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, nil
	}}
	ctx := context.Background()
	for _, bad := range []string{"", "-c", "--upload-pack=touch x", "-C/tmp", "relative/dir", "/tmp/a\x00b", "/tmp/a\nb"} {
		if _, err := r.Resolve(ctx, bad); err == nil {
			t.Errorf("Resolve(%q) accepted", bad)
		}
		if _, err := r.Inventory(ctx, bad); err == nil {
			t.Errorf("Inventory(%q) accepted", bad)
		}
		if _, err := r.TreeStatus(ctx, bad); err == nil {
			t.Errorf("TreeStatus(%q) accepted", bad)
		}
		if _, err := r.IsAncestor(ctx, bad, strings.Repeat("a", 40), strings.Repeat("b", 40)); err == nil {
			t.Errorf("IsAncestor(%q) accepted", bad)
		}
	}
	if calls != 0 {
		t.Fatalf("git ran %d times for hostile folders", calls)
	}
}

// A folder that looks like an option still travels as the value of -C.
func TestFolderIsPassedAsTheValueOfC(t *testing.T) {
	var got []string
	r := &resolver{bin: "git", timeout: callTimeout, run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		got = args
		return nil, nil
	}}
	_, _ = r.Inventory(context.Background(), "/tmp/--upload-pack=x")
	i := slices.Index(got, "-C")
	if i < 0 || got[i+1] != "/tmp/--upload-pack=x" {
		t.Fatalf("args = %v", got)
	}
}

func TestProbe(t *testing.T) {
	st := New(Options{Bin: filepath.Join(t.TempDir(), "nope")}).Probe(context.Background())
	if st.State != source.StateMissing {
		t.Fatalf("missing git: %+v", st)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	st = New(Options{}).Probe(context.Background())
	if st.State != source.StateOK || st.Version == "" || st.Kind != source.KindGit {
		t.Fatalf("Probe = %+v", st)
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-git")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

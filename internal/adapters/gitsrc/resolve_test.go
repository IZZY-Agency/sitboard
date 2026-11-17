package gitsrc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/source"
)

// git runs git in dir with a fixed test identity passed through the
// environment, so no git setting is read or written.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
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
	if r.Slug != "example-org/example-repo" {
		t.Fatalf("Slug = %q", r.Slug)
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
	if r.Root != dir || r.MainRoot != dir || r.Slug != "" || r.Branch.OK {
		t.Fatalf("got %+v", r)
	}
}

func TestMissingFolderIsAnError(t *testing.T) {
	_, err := New(Options{}).Resolve(context.Background(), filepath.Join(t.TempDir(), "gone"))
	if err == nil {
		t.Fatal("want an error for a folder that does not exist")
	}
}

func TestListsWorktreesAndBranches(t *testing.T) {
	repo := newRepo(t)
	git(t, repo, "branch", "spare")
	wt := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-wt2")
	git(t, repo, "worktree", "add", "-q", "-b", "worker-a", wt)
	t.Cleanup(func() { _ = os.RemoveAll(wt) })

	ctx := context.Background()
	res := New(Options{})
	wts, err := res.Worktrees(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 2 || wts[0].Path != repo || wts[1].Path != wt {
		t.Fatalf("worktrees = %+v", wts)
	}
	if b, _ := wts[1].Branch.Get(); b != "worker-a" {
		t.Fatalf("worktree branch = %v", wts[1].Branch)
	}
	branches, err := res.LocalBranches(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(branches)
	if !slices.Equal(branches, []string{"main", "spare", "worker-a"}) {
		t.Fatalf("branches = %v", branches)
	}
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
		if _, err := r.Worktrees(ctx, bad); err == nil {
			t.Errorf("Worktrees(%q) accepted", bad)
		}
		if _, err := r.LocalBranches(ctx, bad); err == nil {
			t.Errorf("LocalBranches(%q) accepted", bad)
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
	_, _ = r.Worktrees(context.Background(), "/tmp/--upload-pack=x")
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

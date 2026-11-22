package tmux

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// shortDir is a private folder under /tmp, short enough for a socket path
// (about 100 bytes at most), resolved, removed at the end.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sbsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// listen makes a real unix socket at path, closed at the end.
func listen(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
}

func TestSafeSocketAcceptsTheUsersOwnSocket(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "s")
	listen(t, sock)
	got, err := safeSocket(sock, os.Getuid())
	if err != nil || got != sock {
		t.Fatalf("safeSocket = %q, %v", got, err)
	}
	// Reached through a symlinked folder: tmux gets the resolved path.
	if err := os.Symlink(dir, filepath.Join(dir, "via")); err != nil {
		t.Fatal(err)
	}
	if got, err := safeSocket(filepath.Join(dir, "via", "s"), os.Getuid()); err != nil || got != sock {
		t.Fatalf("through a symlinked folder: safeSocket = %q, %v, want %q", got, err, sock)
	}
	// A private folder in a sticky folder others can write to, as tmux's
	// default /tmp/tmux-<uid>/ in /tmp.
	sticky := filepath.Join(dir, "sticky")
	if err := os.Mkdir(sticky, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sticky, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(sticky, "tmux-1")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	listen(t, filepath.Join(private, "default"))
	if _, err := safeSocket(filepath.Join(private, "default"), os.Getuid()); err != nil {
		t.Fatalf("socket in a private folder in a sticky one: %v", err)
	}
}

func TestSafeSocketRefusesWhatIsNotTheUsersSocket(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "s")
	listen(t, sock)

	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(sock, link); err != nil {
		t.Fatal(err)
	}
	// A folder anyone can write to and that is not sticky: another user
	// could put their own socket in place of the owner's.
	open := filepath.Join(dir, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	listen(t, filepath.Join(open, "s"))
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	// The same for a folder further up the path.
	deep := filepath.Join(open, "deep")
	if err := os.Mkdir(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	listen(t, filepath.Join(deep, "s"))
	// Directly in a sticky folder others can write to (tmux -S /tmp/x.sock):
	// another user can take the name once the owner's server removed it.
	sticky := filepath.Join(dir, "sticky")
	if err := os.Mkdir(sticky, 0o700); err != nil {
		t.Fatal(err)
	}
	listen(t, filepath.Join(sticky, "s"))
	if err := os.Chmod(sticky, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	// In a sticky folder the group can write to.
	group := filepath.Join(dir, "group")
	if err := os.Mkdir(group, 0o700); err != nil {
		t.Fatal(err)
	}
	listen(t, filepath.Join(group, "s"))
	if err := os.Chmod(group, 0o770|os.ModeSticky); err != nil {
		t.Fatal(err)
	}

	for name, p := range map[string]string{
		"a regular file":              file,
		"a symlink to a socket":       link,
		"a missing socket":            filepath.Join(dir, "none"),
		"a folder as the socket":      dir + "/",
		"a socket in an open folder":  filepath.Join(open, "s"),
		"a socket below an open one":  filepath.Join(deep, "s"),
		"a socket in a missing place": filepath.Join(dir, "nowhere", "s"),
		"a socket in a sticky folder": filepath.Join(sticky, "s"),
		"a socket in a group folder":  filepath.Join(group, "s"),
	} {
		if got, err := safeSocket(p, os.Getuid()); !errors.Is(err, errUnsafeSocket) {
			t.Errorf("%s: safeSocket = %q, %v", name, got, err)
		}
	}
	// Owned by someone else: the socket, and a folder on the way.
	other := os.Getuid() + 1
	fi, err := os.Lstat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if !socketOK(fi, os.Getuid()) || socketOK(fi, other) {
		t.Errorf("socketOK ignores the owner")
	}
	di, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !dirOK(di, os.Getuid()) || dirOK(di, other) {
		t.Errorf("dirOK ignores the owner")
	}
	root, err := os.Lstat("/")
	if err != nil {
		t.Fatal(err)
	}
	if !dirOK(root, other) {
		t.Errorf("dirOK refuses a folder owned by root")
	}
	// The socket's own folder must be the user's: not root's, not another's.
	if !ownDirOK(di, os.Getuid()) || ownDirOK(di, other) || ownDirOK(root, os.Getuid()) {
		t.Errorf("ownDirOK ignores the owner")
	}
}

// The real adapter refuses an unsafe socket before it runs tmux at all: a
// tmux binary that is not there would otherwise fail differently.
func TestJumpChecksTheSocketBeforeAnyTmuxCall(t *testing.T) {
	dir := shortDir(t)
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a := newAdapter(t, Options{}, nil)
	a.bin = "/usr/bin/tmux"
	ran := false
	a.run = func(context.Context, string, ...string) ([]byte, error) {
		ran = true
		return nil, nil
	}
	err := a.Jump(context.Background(), model.FocusTarget{Kind: model.FocusTmux, ID: "%1", Socket: file})
	if !errors.Is(err, source.ErrUnknownTerminal) || ran {
		t.Fatalf("Jump = %v, tmux ran: %v", err, ran)
	}

	f := &fakeTmux{panes: panes}
	b := newAdapter(t, Options{}, f)
	b.socket = func(string) (string, error) { return "", errUnsafeSocket }
	if err := b.Jump(context.Background(), target("%1")); !errors.Is(err, source.ErrUnknownTerminal) || len(f.calls) != 0 {
		t.Fatalf("Jump = %v, calls %q", err, f.calls)
	}
}

func TestJumpTalksToTheCheckedPath(t *testing.T) {
	f := &fakeTmux{panes: panes, clients: "100 12 /dev/pts/4 $1\n"}
	a := newAdapter(t, Options{}, f)
	a.socket = func(string) (string, error) { return "/private/tmp/tmux-501/default", nil }
	if err := a.Jump(context.Background(), target("%1")); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if c[3] != "/private/tmp/tmux-501/default" {
			t.Fatalf("call %q not on the checked path", c)
		}
	}
}

// tmux runs with PATH as its only environment: it sends its environment to
// the server, and sitboard's own TMUX and TMUX_PANE must not reach it.
func TestTmuxGetsOnlyPATH(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,1,0")
	t.Setenv("TMUX_PANE", "%1")
	t.Setenv("SECRET_TOKEN", "x")
	out, err := newAdapter(t, Options{}, nil).run(context.Background(), "env")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(out)), "PATH="+os.Getenv("PATH"); got != want {
		t.Fatalf("environment %q, want only %q", got, want)
	}
}

// tmux is found once, as an absolute path with symlinks resolved, never in a
// PATH folder that others can write to or that another account owns (they
// could put their own "tmux" there), nor relative, and only a file this
// process may run.
func TestLookBin(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	me, other := os.Getuid(), os.Getuid()+1
	mk := func(name string, perm, mode os.FileMode) string {
		t.Helper()
		d := filepath.Join(root, name)
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "tmux"), []byte("#!/bin/sh\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(d, "tmux"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, perm); err != nil {
			t.Fatal(err)
		}
		return d
	}
	link := func(name, to string) string {
		t.Helper()
		d := filepath.Join(root, name)
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(to, "tmux"), filepath.Join(d, "tmux")); err != nil {
			t.Fatal(err)
		}
		return d
	}
	open := mk("open", 0o777, 0o755)
	group := mk("group", 0o775, 0o755)
	plain := mk("plain", 0o755, 0o644)     // not executable
	othersX := mk("othersx", 0o755, 0o601) // executable by others only, not by this process
	good := mk("good", 0o755, 0o755)
	toOpen := link("toopen", open) // a trusted folder, but the file is in an open one
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, good) // the right folder, named relatively
	if err != nil {
		t.Fatal(err)
	}
	list := func(dirs ...string) string { return strings.Join(dirs, string(os.PathListSeparator)) }
	path := list("", relative, open, group, filepath.Join(root, "none"), plain, othersX, toOpen, good)
	if got, want := lookBin("tmux", path, me), filepath.Join(good, "tmux"); got != want {
		t.Fatalf("lookBin = %q, want %q", got, want)
	}
	if got := lookBin("tmux", list(open, group, plain, othersX, toOpen), me); got != "" {
		t.Fatalf("lookBin = %q, want none", got)
	}
	// A folder owned by another account (not root) is not trusted.
	if got := lookBin("tmux", good, other); got != "" {
		t.Fatalf("a folder of another account: %q", got)
	}
	// Through a symlink in a trusted folder to a trusted one: the resolved
	// path is kept and run.
	if got, want := lookBin("tmux", link("togood", good), me), filepath.Join(good, "tmux"); got != want {
		t.Fatalf("through a symlink: %q, want %q", got, want)
	}
	// A symlink in a folder others can write to is not followed, even to a
	// trusted file: that folder is not trusted at all.
	openLink := link("openlink", good)
	if err := os.Chmod(openLink, 0o777); err != nil {
		t.Fatal(err)
	}
	if got := lookBin("tmux", openLink, me); got != "" {
		t.Fatalf("a symlink in an open folder: %q", got)
	}
	if got, want := lookBin(filepath.Join(good, "tmux"), "", me), filepath.Join(good, "tmux"); got != want {
		t.Fatalf("an absolute Bin: %q", got)
	}
	if got := lookBin(filepath.Join(plain, "tmux"), "", me); got != "" {
		t.Fatalf("a Bin that cannot run: %q", got)
	}
	if root, err := os.Stat("/"); err != nil || !binDirOK(root, other) {
		t.Fatalf("a folder of root's not trusted: %v", err)
	}
	// The adapter runs the path it found, every time.
	t.Setenv("PATH", path)
	f := &fakeTmux{panes: panes, clients: "100 12 /dev/pts/4 $1\n"}
	a := newAdapter(t, Options{}, nil)
	a.run, a.socket = f.run, func(p string) (string, error) { return p, nil }
	if a.bin != filepath.Join(good, "tmux") {
		t.Fatalf("adapter bin %q", a.bin)
	}
	if err := a.Jump(context.Background(), target("%1")); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if c[0] != filepath.Join(good, "tmux") {
			t.Fatalf("ran %q", c[0])
		}
	}
}

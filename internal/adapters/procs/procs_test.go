package procs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

func TestLsofOutputGivesProcs(t *testing.T) {
	var got []string
	l := &lister{goos: "darwin", uid: 501, run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return []byte("p12\nR1\nfcwd\nn/home/dev/code/alpha\np13\nR12\nfcwd\nn/home/dev/code/beta/../alpha\n"), nil
	}}
	procs, err := l.Procs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []model.Proc{{PID: 12, PPID: 1, Cwd: "/home/dev/code/alpha"}, {PID: 13, PPID: 12, Cwd: "/home/dev/code/alpha"}}
	if !slices.Equal(procs, want) {
		t.Fatalf("procs = %v", procs)
	}
	if !slices.Equal(got, []string{"lsof", "-a", "-d", "cwd", "-u", "501", "-F", "pRn", "-w", "-n", "-P"}) {
		t.Fatalf("args = %v", got)
	}
}

// A failing lsof, even one that printed something, means "cannot tell".
func TestLsofFailureIsAnError(t *testing.T) {
	l := &lister{goos: "darwin", run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte("p1\nfcwd\nn/x\n")}
	}}
	if procs, err := l.Procs(context.Background()); err == nil || procs != nil {
		t.Fatalf("procs=%v err=%v", procs, err)
	}
}

// Every process lsof names must come with one absolute working folder: a
// process without one, a folder that is no path, or a line lsof should not
// print is a folder sitboard could not read, so the answer is unknown.
func TestLsofProcessWithoutFolderIsAnError(t *testing.T) {
	for name, out := range map[string]string{
		"no folder":       "p12\nfcwd\nn/home/dev/alpha\np13\nfcwd\np14\nfcwd\nn/home/dev/beta\n",
		"last no folder":  "p12\nfcwd\nn/home/dev/alpha\np13\nfcwd\n",
		"relative folder": "p12\nfcwd\nn/home/dev/alpha\np14\nfcwd\nnrelative\n",
		"error text":      "p12\nfcwd\nn (readlink: Permission denied)\n",
		"folder no pid":   "fcwd\nn/home/dev/alpha\n",
		"two folders":     "p12\nfcwd\nn/home/dev/alpha\nn/home/dev/beta\n",
		"unknown field":   "p12\nfcwd\nn/home/dev/alpha\nxsomething\n",
		"bad pid":         "pxx\nfcwd\nn/home/dev/alpha\n",
		"bad parent":      "p12\nRxx\nfcwd\nn/home/dev/alpha\n",
		"parent no pid":   "R1\nfcwd\nn/home/dev/alpha\n",
	} {
		l := &lister{goos: "darwin", run: func(context.Context, string, ...string) ([]byte, error) { return []byte(out), nil }}
		if procs, err := l.Procs(context.Background()); err == nil || procs != nil {
			t.Errorf("%s: procs=%v err=%v", name, procs, err)
		}
	}
}

// lsof, in the C locale, prints the bytes of a non-ASCII letter as \xHH: a
// process in a folder with such a letter works in that folder.
func TestLsofNonASCIIFolder(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "équipe", "wt")
	// Two letters, six escaped bytes: 64 readings, one of them on disk.
	cjk := filepath.Join(root, "日本")
	for _, d := range []string{dir, cjk, filepath.Join(root, "日x")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for printed, want := range map[string]string{
		root + `/\xc3\xa9quipe/wt`:         dir,
		root + `/\xe6\x97\xa5\xe6\x9c\xac`: cjk,
	} {
		l := &lister{goos: "darwin", run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("p12\nR1\nfcwd\nn" + printed + "\n"), nil
		}}
		procs, err := l.Procs(context.Background())
		if err != nil || len(procs) != 1 || procs[0].Cwd != want {
			t.Errorf("%s: procs=%v err=%v", printed, procs, err)
		}
	}
}

// lsof prints a backslash or a caret in a name as it is, so a printed folder
// can name more than one real one: when more than one of them exists, or
// none, the answer is unknown.
func TestLsofAmbiguousFolderIsUnknown(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dirs := []string{`aA`, `a\x41`, `b^A`}
	// Folders for every reading of five escapes: 32 readings stay alive.
	many := []string{""}
	for range 5 {
		var next []string
		for _, m := range many {
			next = append(next, m+"A", m+`\x41`)
		}
		many = next
	}
	for _, m := range many {
		dirs = append(dirs, "m"+m)
	}
	for _, d := range dirs {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, c := range map[string]struct {
		printed string
		want    string // "" for unknown
	}{
		"both exist":            {`/a\x41`, ""},
		"none exists":           {`/c\x41`, ""},
		"literal caret":         {`/b^A`, root + "/b^A"},
		"too many escapes":      {"/m" + strings.Repeat(`\x41`, 5), ""},
		"escapes, none on disk": {"/n" + strings.Repeat(`\x41`, 8), ""},
	} {
		l := &lister{goos: "darwin", run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("p12\nfcwd\nn" + root + c.printed + "\n"), nil
		}}
		procs, err := l.Procs(context.Background())
		switch {
		case c.want == "" && (err == nil || procs != nil):
			t.Errorf("%s: procs=%v err=%v, want unknown", name, procs, err)
		case c.want != "" && (err != nil || len(procs) != 1 || procs[0].Cwd != c.want):
			t.Errorf("%s: procs=%v err=%v, want %s", name, procs, err, c.want)
		}
	}
}

func TestLinuxReadsProcCwd(t *testing.T) {
	l := &lister{goos: "linux",
		procs: func(context.Context) ([]string, error) { return []string{"1", "2", "3"}, nil },
		cwdOf: func(pid string) (string, error) {
			switch pid {
			case "1":
				return "/home/dev/alpha", nil
			case "2":
				return "", os.ErrNotExist // ended meanwhile
			default:
				return "/home/dev/beta (deleted)", nil
			}
		},
		ppidOf: func(pid string) (int, error) { return 100 + len(pid), nil }}
	procs, err := l.Procs(context.Background())
	// A folder named "... (deleted)" counts as itself and as the folder it
	// may have been before it was removed.
	want := []model.Proc{{PID: 1, PPID: 101, Cwd: "/home/dev/alpha"}, {PID: 3, PPID: 101, Cwd: "/home/dev/beta (deleted)"},
		{PID: 3, PPID: 101, Cwd: "/home/dev/beta"}}
	if err != nil || !slices.Equal(procs, want) {
		t.Fatalf("procs=%v err=%v", procs, err)
	}
	l.cwdOf = func(string) (string, error) { return "", os.ErrPermission }
	if _, err := l.Procs(context.Background()); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("a process it cannot read must make the answer unknown, got %v", err)
	}
	l.cwdOf = func(string) (string, error) { return "/home/dev/alpha", nil }
	l.ppidOf = func(string) (int, error) { return 0, os.ErrPermission }
	if _, err := l.Procs(context.Background()); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("a parent it cannot read must make the answer unknown, got %v", err)
	}
}

func TestParseStatPPID(t *testing.T) {
	if p, err := parseStatPPID("123 (a (b) c) S 45 123 123 0 -1\n"); err != nil || p != 45 {
		t.Fatalf("ppid %d, %v", p, err)
	}
	for _, bad := range []string{"", "123 (x", "123 (x) S", "123 (x) S y"} {
		if _, err := parseStatPPID(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// A cancelled look stops the walk with an error instead of a shorter list.
func TestLinuxWalkHonoursTheDeadline(t *testing.T) {
	l := &lister{goos: "linux",
		procs:  func(context.Context) ([]string, error) { return []string{"1", "2"}, nil },
		cwdOf:  func(string) (string, error) { return "/home/dev/alpha", nil },
		ppidOf: func(string) (int, error) { return 1, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if procs, err := l.Procs(ctx); err == nil || procs != nil {
		t.Fatalf("procs=%v err=%v", procs, err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if pids, err := pidsOwnedBy(ctx, root, 7, func(string) (int, error) { return 7, nil }); err == nil || pids != nil {
		t.Fatalf("pids=%v err=%v", pids, err)
	}
}

// Under /proc, a process whose owner cannot be read makes the list unknown;
// one that ended meanwhile is skipped; other users' processes are not this
// user's to list.
func TestLinuxPidsOwnedBy(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"10", "11", "12", "13", "self", "net"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	owners := map[string]int{"10": 7, "11": 8, "12": 7}
	ownerOf := func(path string) (int, error) {
		if u, ok := owners[filepath.Base(path)]; ok {
			return u, nil
		}
		if filepath.Base(path) == "13" {
			return -1, os.ErrNotExist // ended meanwhile
		}
		t.Fatalf("asked for %s", path)
		return -1, nil
	}
	pids, err := pidsOwnedBy(context.Background(), root, 7, ownerOf)
	if err != nil || !slices.Equal(pids, []string{"10", "12"}) {
		t.Fatalf("pids=%v err=%v", pids, err)
	}
	owners["11"] = -1
	ownerOf2 := func(path string) (int, error) {
		if filepath.Base(path) == "11" {
			return -1, os.ErrPermission
		}
		return ownerOf(path)
	}
	if pids, err := pidsOwnedBy(context.Background(), root, 7, ownerOf2); !errors.Is(err, os.ErrPermission) || pids != nil {
		t.Fatalf("an owner it cannot read must make the list unknown: pids=%v err=%v", pids, err)
	}
}

// The real call on this machine answers and includes this test's own
// process, with its parent and folder.
func TestRealProcsIncludeThisOne(t *testing.T) {
	procs, err := New().Procs(context.Background())
	if err != nil {
		t.Skipf("cannot list here: %v", err)
	}
	wd, _ := os.Getwd()
	if !slices.Contains(procs, model.Proc{PID: os.Getpid(), PPID: os.Getppid(), Cwd: wd}) {
		t.Fatalf("this process (%d in %s) not among %d", os.Getpid(), wd, len(procs))
	}
}

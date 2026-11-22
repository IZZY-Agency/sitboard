package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
)

func TestParseLsof(t *testing.T) {
	a, b, c := "/x/thread-writer-locks/a.lock", "/x/thread-writer-locks/b.lock", "/x/thread-writer-locks/c.lock"
	out := "p4242\nfcwd\nn/x\nf42\nn" + a + "\np99\nf7\nn" + b + "\nn/elsewhere\npjunk\nn" + c + "\n"
	got := parseLsof([]byte(out), []string{a, b, c})
	if len(got) != 2 || got[a] != 4242 || got[b] != 99 {
		t.Errorf("parseLsof = %v", got)
	}
}

// lsof exits 1 when some files are not open and still prints the others.
func TestLsofExitOneStillCounts(t *testing.T) {
	a := "/x/thread-writer-locks/a.lock"
	var gotArgs []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return nil, &runner.ExitError{Code: 1, Stdout: []byte("p7\nn" + a + "\n")}
	}
	h, ok := lsofHolders(run)(context.Background(), []string{a})
	if !ok || h[a] != 7 {
		t.Errorf("holders = %v %v", h, ok)
	}
	if strings.Join(gotArgs, " ") != "lsof -nP -w -Fpn -- "+a {
		t.Errorf("args = %v", gotArgs)
	}
	// Exit 1 with an error message is a failure, not "no one holds the lock".
	broken := func(context.Context, string, ...string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stderr: []byte("lsof: illegal option")}
	}
	if _, ok := lsofHolders(broken)(context.Background(), []string{a}); ok {
		t.Error("a failing lsof told liveness")
	}
	// "status error on <an asked path>" only: that file vanished between the
	// listing and lsof (Codex removes a lock when a thread ends). A normal
	// answer. One for a path that was not asked about is a real failure.
	vanished := func(context.Context, string, ...string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte("p7\nn" + a + "\n"), Stderr: []byte("lsof: status error on /x/thread-writer-locks/b.lock: No such file or directory\n")}
	}
	b := "/x/thread-writer-locks/b.lock"
	if h, ok := lsofHolders(vanished)(context.Background(), []string{a, b}); !ok || h[a] != 7 || len(h) != 1 {
		t.Errorf("vanished lock: %v %v", h, ok)
	}
	if _, ok := lsofHolders(vanished)(context.Background(), []string{a}); ok {
		t.Error("a status error on a path that was not asked about is a failure")
	}
	fail := func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not found") }
	if _, ok := lsofHolders(fail)(context.Background(), []string{a}); ok {
		t.Error("a missing lsof told liveness")
	}
	if h, ok := lsofHolders(fail)(context.Background(), nil); !ok || len(h) != 0 {
		t.Error("no lock files needs no lsof")
	}
}

func TestNamesIndex(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "session_index.jsonl")
	keep := func(id string) bool { return id != idDesktop }
	write := func(s string) {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(s)
		_ = f.Close()
	}
	write(`{"id":"` + idWorker + `","thread_name":"Placeholder one","updated_at":"x"}` + "\n" +
		`{"id":"` + idDesktop + `","thread_name":"Placeholder desktop","updated_at":"x"}` + "\n" +
		`{"id":"not-an-id","thread_name":"Placeholder bad","updated_at":"x"}` + "\n" +
		`{"id":"` + idExec + `","thread_name":"` + strings.Repeat("y", maxIndexLine) + `"}` + "\n" +
		`{"id":"` + idWorker + `","thread_name":"Placeholder two`) // incomplete last line
	n := newNames(p)
	if !n.update(keep) || n.byID[idWorker] != "Placeholder one" || len(n.byID) != 1 {
		t.Fatalf("names = %v", n.byID)
	}
	write(`","updated_at":"x"}` + "\n")
	if !n.update(keep) || n.byID[idWorker] != "Placeholder two" {
		t.Fatalf("names = %v", n.byID)
	}
	if n.update(keep) {
		t.Error("an unchanged index reported a change")
	}
	write(`{"id":"` + idExec + `","thread_name":"` + strings.Repeat("z", maxName+10) + `"}` + "\n")
	n.update(keep)
	if len(n.byID[idExec]) != maxName {
		t.Errorf("name not capped: %d", len(n.byID[idExec]))
	}
}

// A line for a thread that was not wanted yet (its rollout not recognised)
// must not be lost for good: when the thread becomes wanted, the index is
// read again.
func TestNamesIndexLateWantedThread(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session_index.jsonl")
	line := `{"id":"` + idWorker + `","thread_name":"Placeholder late","updated_at":"x"}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	wanted := false
	keep := func(string) bool { return wanted }
	n := newNames(p)
	if n.update(keep) || len(n.byID) != 0 {
		t.Fatalf("unwanted thread named: %v", n.byID)
	}
	wanted = true
	if !n.update(keep) || n.byID[idWorker] != "Placeholder late" {
		t.Errorf("names = %v", n.byID)
	}
}

// A line longer than the buffer that is still being written must not be cut
// in two: the part that arrives later is not a line of its own, even when it
// looks like one and the cut fell exactly at the end of a buffer.
func TestNamesIndexPartialLongLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session_index.jsonl")
	prefix := `{"id":"` + idExec + `","thread_name":"`
	head := prefix + strings.Repeat("a", 2*maxIndexLine-len(prefix)) // two whole buffers, no newline yet
	tail := `{"id":"` + idWorker + `","thread_name":"Placeholder tail"}` + "\n"
	if err := os.WriteFile(p, []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
	keep := func(string) bool { return true }
	n := newNames(p)
	n.update(keep)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(tail) // the long line ends here
	_, _ = f.WriteString(`{"id":"` + idDesktop + `","thread_name":"Placeholder after"}` + "\n")
	_ = f.Close()
	n.update(keep)
	if _, ok := n.byID[idWorker]; ok {
		t.Errorf("the end of a long line was read as a line: %v", n.byID)
	}
	if n.byID[idDesktop] != "Placeholder after" {
		t.Errorf("the line after it was not read: %v", n.byID)
	}
	if fi, _ := os.Stat(p); n.off != fi.Size() {
		t.Errorf("offset %d, want the end of the file %d", n.off, fi.Size())
	}
}

// A cut at the cap never splits a character.
func TestNamesIndexCapKeepsCharacters(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session_index.jsonl")
	name := strings.Repeat("a", maxName-1) + "é" // the 2-byte é straddles the cap
	if err := os.WriteFile(p, []byte(`{"id":"`+idWorker+`","thread_name":"`+name+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := newNames(p)
	n.update(func(string) bool { return true })
	if got := n.byID[idWorker]; !utf8.ValidString(got) || len(got) != maxName-1 {
		t.Errorf("name = %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}

func TestEffortWord(t *testing.T) {
	for in, want := range map[string]string{
		"ultra": "ultra", "high": "high", "minimal": "minimal", "": "",
		"Ultra": "", "x-high": "", "ultra" + " ": "", "abcdefghijklmnopq": "", "abcdefghijklmnop": "abcdefghijklmnop",
	} {
		if got := effortWord(in); got != want {
			t.Errorf("effortWord(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPickAgtermID(t *testing.T) {
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	env := [][]byte{[]byte("PATH=/bin"), []byte("AGTERM_SESSION_ID=" + id), []byte("SECRET=x")}
	if got := pickAgtermID(env); got != strings.ToUpper(id) {
		t.Errorf("pick = %q", got)
	}
	if got := pickAgtermID([][]byte{[]byte("AGTERM_SESSION_ID=nope")}); got != "" {
		t.Errorf("pick bad = %q", got)
	}
	if got := pickAgtermID([][]byte{[]byte("X_AGTERM_SESSION_ID=" + id)}); got != "" {
		t.Errorf("pick other var = %q", got)
	}
}

// Only the first session_meta is the thread's own: a fork repeats its
// parent's further down, and readMeta reads the first line only.
func TestMetaIsTheFirstLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.jsonl")
	w := fixture(t, "worker.jsonl")
	c := fixture(t, "child.jsonl")
	if err := os.WriteFile(p, append(append([]byte{}, c...), w...), 0o644); err != nil {
		t.Fatal(err)
	}
	m, ok := readMeta(p, fixedNow)
	if !ok || m.ID != idChild || m.Source != "subagent" || m.Parent != idWorker {
		t.Errorf("meta = %+v %v", m, ok)
	}
	if err := os.WriteFile(p, w, 0o644); err != nil {
		t.Fatal(err)
	}
	if m, ok := readMeta(p, fixedNow); !ok || m.Source != "vscode" || m.Parent != "" || m.Originator != "codex-tui" {
		t.Errorf("meta = %+v %v", m, ok)
	}
	if err := os.Symlink(p, filepath.Join(dir, "link.jsonl")); err == nil {
		if _, ok := readMeta(filepath.Join(dir, "link.jsonl"), fixedNow); ok {
			t.Error("a link was followed")
		}
	}
}

package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// TestRealTmux drives a real tmux server of its own, on a private socket in a
// temporary folder, with clients attached through script(1). It runs only on
// Linux with tmux and script installed: `make test-tmux-linux` runs it in a
// container. It never touches another tmux server, and works from inside a
// tmux pane too: TMUX and TMUX_PANE are taken out of its tmux commands'
// environment, or tmux would refuse to attach a nested client.
func TestRealTmux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux script(1)")
	}
	for _, bin := range []string{"tmux", "script"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	dir, err := os.MkdirTemp("/tmp", "sbtmux") // short: a socket path is limited to about 100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	tm := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("tmux", append([]string{"-f", "/dev/null", "-S", sock}, args...)...)
		cmd.Env = outsideTmux()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %q: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	tm("new-session", "-d", "-s", "one", "-x", "80", "-y", "24")
	t.Cleanup(func() {
		kill := exec.Command("tmux", "-S", sock, "kill-server")
		kill.Env = outsideTmux()
		_ = kill.Run()
	})
	tm("new-session", "-d", "-s", "two", "-x", "80", "-y", "24")
	tm("new-window", "-t", "two")
	ttys := map[string]string{"%999": "/dev/pts/999"} // pane id → its terminal
	paneOf := func(where string) string {
		t.Helper()
		id := tm("display", "-p", "-t", where, "#{pane_id}")
		ttys[id] = tm("display", "-p", "-t", where, "#{pane_tty}")
		return id
	}
	paneOne := paneOf("one:0")
	paneTwo1 := paneOf("two:1")

	attach := func(session string) {
		t.Helper()
		before := len(clients(tm))
		cmd := exec.Command("script", "-qfc", "tmux -S "+sock+" attach -t "+session, "/dev/null")
		cmd.Env = append(outsideTmux(), "TERM=xterm-256color")
		in, _ := cmd.StdinPipe() // kept open: script ends at end of input
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
		waitFor(t, func() bool { return len(clients(tm)) > before })
		time.Sleep(1100 * time.Millisecond) // client_activity has a one-second resolution
	}

	a := newAdapter(t, Options{}, nil) // the real tmux and socket check, as outside tests
	jump := func(pane string) error {
		return a.Jump(context.Background(), model.FocusTarget{Kind: model.FocusTmux, ID: pane, Socket: sock, TTY: ttys[pane], PID: os.Getpid()})
	}

	// The pane shows its own terminal, and no other.
	if !a.Owns(context.Background(), model.TmuxPane{Socket: sock, Pane: paneOne, TTY: ttys[paneOne]}) {
		t.Fatal("the pane showing its terminal not owned")
	}
	if a.Owns(context.Background(), model.TmuxPane{Socket: sock, Pane: paneOne, TTY: ttys[paneTwo1]}) {
		t.Fatal("a pane owned with another pane's terminal")
	}

	if err := jump(paneTwo1); !errors.Is(err, source.ErrNoClient) {
		t.Fatalf("no client attached: %v", err)
	}

	attach("one") // A
	attach("two") // B, the most recently active
	tm("select-window", "-t", "two:0")

	// B already shows session two: only its view changes, A stays on one.
	if err := jump(paneTwo1); err != nil {
		t.Fatal(err)
	}
	if got := clients(tm); !hasView(got, "one", paneOne) || !hasView(got, "two", paneTwo1) {
		t.Fatalf("after the jump to %s: %q", paneTwo1, got)
	}
	// A shows session one: jumping there does not pull B away from two.
	if err := jump(paneOne); err != nil {
		t.Fatal(err)
	}
	if got := clients(tm); !hasView(got, "one", paneOne) || !hasView(got, "two", paneTwo1) {
		t.Fatalf("after the jump to %s: %q", paneOne, got)
	}

	// A session no client shows: the most recently active client moves.
	tm("new-session", "-d", "-s", "three")
	paneThree := paneOf("three:0")
	if err := jump(paneThree); err != nil {
		t.Fatal(err)
	}
	if got := clients(tm); !hasView(got, "three", paneThree) {
		t.Fatalf("after the jump to %s: %q", paneThree, got)
	}

	// Grouped sessions: twin shares two's windows, and its client C is the
	// only client on either. Session two is made the more recently active
	// (B passes through it), so a bare pane target would change two's view,
	// which nobody sees. The pane is listed under both; C must show it,
	// inside twin, and nobody else moves.
	tm("new-session", "-d", "-t", "two", "-s", "twin")
	attach("twin") // C
	tm("select-window", "-t", "twin:0")
	tm("select-window", "-t", "two:0")
	var bTTY string
	for _, l := range strings.Split(tm("list-clients", "-F", "#{client_tty} #{session_name}"), "\n") {
		if tty, ok := strings.CutSuffix(l, " three"); ok {
			bTTY = tty
		}
	}
	tm("switch-client", "-c", bTTY, "-t", "two")
	tm("switch-client", "-c", bTTY, "-t", "three")
	if err := jump(paneTwo1); err != nil {
		t.Fatal(err)
	}
	if got := clients(tm); !hasView(got, "twin", paneTwo1) || !hasView(got, "one", paneOne) || !hasView(got, "three", paneThree) {
		t.Fatalf("grouped: after the jump to %s: %q", paneTwo1, got)
	}

	if err := jump("%999"); !errors.Is(err, source.ErrUnknownTerminal) {
		t.Fatalf("unknown pane: %v", err)
	}
	tm("kill-server")
	if err := jump(paneOne); !errors.Is(err, source.ErrUnknownTerminal) {
		t.Fatalf("no server: %v", err)
	}
	// That -N is passed is checked by the fake runner of the unit tests: none
	// of these commands would start a server without it either.
}

// outsideTmux is this process's environment without TMUX and TMUX_PANE.
func outsideTmux() []string {
	return slices.DeleteFunc(os.Environ(), func(e string) bool {
		return strings.HasPrefix(e, "TMUX=") || strings.HasPrefix(e, "TMUX_PANE=")
	})
}

// clients lists "session pane" for each attached client.
func clients(tm func(...string) string) []string {
	out := tm("list-clients", "-F", "#{session_name} #{pane_id}")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func hasView(views []string, session, pane string) bool {
	return slices.Contains(views, session+" "+pane)
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for range 100 {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out")
}

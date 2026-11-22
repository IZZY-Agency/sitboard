package claude

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

func TestReadsTheTmuxPaneAndItsSocket(t *testing.T) {
	env := []string{"TMUX=/tmp/tmux-501/default,4242,0", "SECRET_TOKEN=do-not-read", "TMUX_PANE=%12"}
	want := model.TmuxPane{Socket: "/tmp/tmux-501/default", Pane: "%12"}
	for _, got := range []procFacts{procFactsFromProcArgs2(procArgs2([]string{"claude"}, env)), procFactsFromLinux(nul([]string{"claude"}), nul(env))} {
		if got.Tmux != want {
			t.Fatalf("tmux = %+v, want %+v", got.Tmux, want)
		}
	}
}

// Both variables are needed, each of its shape; anything else keeps no pane.
func TestRefusesATmuxPaneOfTheWrongShape(t *testing.T) {
	for _, env := range [][]string{
		{"TMUX_PANE=%1"},                                     // no socket
		{"TMUX=/tmp/tmux-501/default,1,0"},                   // no pane
		{"TMUX=/tmp/tmux-501/default,1,0", "TMUX_PANE=1"},    // pane without %
		{"TMUX=/tmp/tmux-501/default,1,0", "TMUX_PANE=%1 x"}, // trailing material
		{"TMUX=/tmp/tmux-501/default,1,0", "TMUX_PANE=%"},
		{"TMUX=/tmp/tmux-501/default,1,0", "TMUX_PANE=%1234567890"}, // too long
		{"TMUX=relative/default,1,0", "TMUX_PANE=%1"},               // not absolute
		{"TMUX=/tmp/../etc/default,1,0", "TMUX_PANE=%1"},            // not clean
		{"TMUX=/tmp/tmux-501/default", "TMUX_PANE=%1"},              // no pid and index
		{"TMUX=/tmp/tmux-501/default,x,0", "TMUX_PANE=%1"},          // pid not a number
		{"TMUX=/tmp/a,b/default,1,0", "TMUX_PANE=%1"},               // comma in the path
		{"TMUX=/tmp/tmux 501/default,1,0", "TMUX_PANE=%1"},          // space in the path
		{"TMUX=-S/x,1,0", "TMUX_PANE=%1"},                           // a flag
		{"TMUX=,1,0", "TMUX_PANE=%1"},
	} {
		if got := procFactsFromLinux(nil, nul(env)); got.Tmux != (model.TmuxPane{}) {
			t.Errorf("%q kept %+v", env, got.Tmux)
		}
	}
}

func TestSessionFactCarriesTheTmuxPaneWhileLive(t *testing.T) {
	e := newEnv(t)
	e.tmux = map[int]model.TmuxPane{4242: {Socket: "/tmp/tmux-501/default", Pane: "%7"}}
	e.ttys = map[int]string{4242: "/dev/ttys004"}
	e.owns = func(model.TmuxPane) bool { return true }
	pane := model.TmuxPane{Socket: "/tmp/tmux-501/default", Pane: "%7", TTY: "/dev/ttys004", PID: 4242, Start: procStart}
	e.setProc(4242, procStart)
	e.record(4242, nil)
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "tmux pane", session(coordID, func(f model.SessionFact) bool { return f.Tmux == pane && f.TerminalID == termCoord }))
	waitScans(t, e, 3) // more scans of the same process
	e.mu.Lock()
	delete(e.procs, 4242) // the process ends
	e.mu.Unlock()
	c.wait(t, 2*time.Second, "pane dropped", session(coordID, func(f model.SessionFact) bool { return f.Ended && f.Tmux == (model.TmuxPane{}) }))
	e.mu.Lock()
	defer e.mu.Unlock()
	if !slices.Equal(e.asked, []model.TmuxPane{{Socket: pane.Socket, Pane: pane.Pane, TTY: pane.TTY}}) {
		t.Fatalf("tmux asked about %+v, want once about %+v", e.asked, pane)
	}
}

// TMUX and TMUX_PANE are inherited: an editor started from a tmux pane gives
// them to the claude in its own terminal. tmux says the pane does not show
// that terminal, so the session is not in tmux, and its agterm id stays.
func TestTmuxPaneOfAnotherTerminalIsDropped(t *testing.T) {
	e := newEnv(t)
	e.tmux = map[int]model.TmuxPane{4242: {Socket: "/tmp/tmux-501/default", Pane: "%3"}}
	e.ttys = map[int]string{4242: "/dev/ttys009"}
	e.owns = func(p model.TmuxPane) bool { return p.TTY == "/dev/ttys004" } // %3 shows ttys004
	e.setProc(4242, procStart)
	e.record(4242, nil)
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "agterm only", session(coordID, func(f model.SessionFact) bool { return f.TerminalID == termCoord && f.Live.OK }))
	waitAsked(t, e, 1)
	for _, f := range c.all() {
		if s, ok := f.(model.SessionFact); ok && s.Tmux != (model.TmuxPane{}) {
			t.Fatalf("pane kept: %+v", s.Tmux)
		}
	}
}

// No terminal (ps prints "??"), no tmux to ask, or a pane of the wrong
// shape: no pane, and tmux is not asked.
func TestTmuxPaneNeedsATerminalAndTmux(t *testing.T) {
	for name, set := range map[string]func(e *env){
		"no terminal": func(e *env) { e.owns = func(model.TmuxPane) bool { return true } },
		"no tmux":     func(e *env) { e.ttys = map[int]string{4242: "/dev/ttys004"} },
	} {
		e := newEnv(t)
		e.tmux = map[int]model.TmuxPane{4242: {Socket: "/tmp/tmux-501/default", Pane: "%3"}}
		set(e)
		e.setProc(4242, procStart)
		e.record(4242, nil)
		c := run(t, e.adapter())
		c.wait(t, 2*time.Second, name, session(coordID, func(f model.SessionFact) bool { return f.TerminalID == termCoord && f.Live.OK }))
		for _, f := range c.all() {
			if s, ok := f.(model.SessionFact); ok && s.Tmux != (model.TmuxPane{}) {
				t.Fatalf("%s: pane kept: %+v", name, s.Tmux)
			}
		}
		e.mu.Lock()
		if len(e.asked) != 0 {
			t.Errorf("%s: tmux asked %+v", name, e.asked)
		}
		e.mu.Unlock()
	}
}

// waitScans waits until n more scans have run.
func waitScans(t *testing.T, e *env, n int) {
	t.Helper()
	e.mu.Lock()
	want := e.scans + n
	e.mu.Unlock()
	for range 200 {
		e.mu.Lock()
		got := e.scans
		e.mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d more scans did not run", n)
}

func waitAsked(t *testing.T, e *env, n int) {
	t.Helper()
	for range 200 {
		e.mu.Lock()
		got := len(e.asked)
		e.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tmux not asked %d times", n)
}

func TestPsTTY(t *testing.T) {
	for out, want := range map[string]string{
		"ttys004\n":    "/dev/ttys004",
		"pts/3\n":      "/dev/pts/3",
		"??      \n":   "",
		"?\n":          "",
		"\n":           "",
		"../etc/x\n":   "",
		"pts/3 -t x\n": "",
	} {
		var args []string
		got := psTTY(func(_ context.Context, name string, a ...string) ([]byte, error) {
			args = append([]string{name}, a...)
			return []byte(out), nil
		})(context.Background(), 4242)
		if got != want {
			t.Errorf("ps printed %q: tty %q, want %q", out, got, want)
		}
		if !slices.Equal(args, []string{"ps", "-o", "tty=", "-p", "4242"}) {
			t.Fatalf("ran %q", args)
		}
	}
	failed := psTTY(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("ttys004\n"), errors.New("exit 1")
	})
	if got := failed(context.Background(), 4242); got != "" {
		t.Fatalf("failed ps: tty %q", got)
	}
}

// AgtermIDOf reads a real process of the user's: a child started with the
// variables set, killed at the end.
func TestAgtermIDOf(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process environments are read on macOS and Linux only")
	}
	if got := AgtermIDOf(0); got != "" {
		t.Errorf("pid 0: %q", got)
	}
	if got := AgtermIDOf(-1); got != "" {
		t.Errorf("pid -1: %q", got)
	}
	const id = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	child := func(env ...string) int {
		t.Helper()
		// The test binary itself, waiting: macOS hides the environment of its
		// own system binaries (such as /bin/sleep), even from their user.
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperWaits$")
		cmd.Env = append(slices.DeleteFunc(os.Environ(), func(e string) bool {
			return strings.HasPrefix(e, "AGTERM_SESSION_ID=") || strings.HasPrefix(e, "TMUX=") || strings.HasPrefix(e, "TMUX_PANE=")
		}), append(env, helperWaits+"=1")...)
		if err := cmd.Start(); err != nil { // returns once it runs, with its environment
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd.Process.Pid
	}
	if got := AgtermIDOf(child("AGTERM_SESSION_ID=" + id)); got != strings.ToUpper(id) {
		t.Errorf("a client in agterm: %q, want %q", got, strings.ToUpper(id))
	}
	if got := AgtermIDOf(child()); got != "" {
		t.Errorf("a client outside agterm: %q", got)
	}
	// Nested tmux: the client runs in a pane of an outer tmux, so its agterm
	// id is the outer server's, inherited, and not trusted.
	if got := AgtermIDOf(child("AGTERM_SESSION_ID="+id, "TMUX=/tmp/tmux-501/default,1,0", "TMUX_PANE=%3")); got != "" {
		t.Errorf("a client inside tmux: %q, want none", got)
	}
}

const helperWaits = "SITBOARD_TEST_HELPER_WAITS"

// TestHelperWaits is the child process of TestAgtermIDOf: with the variable
// set it waits to be killed; otherwise it does nothing.
func TestHelperWaits(t *testing.T) {
	if os.Getenv(helperWaits) == "" {
		return
	}
	time.Sleep(30 * time.Second)
}

func TestProcessAlive(t *testing.T) {
	const start = "Tue Nov 17 08:00:00 2026"
	starts := func(running map[int]string, ok bool) startsFunc {
		return func(context.Context, []int) (map[int]string, bool) { return running, ok }
	}
	for name, c := range map[string]struct {
		s     startsFunc
		pid   int
		start string
		want  bool
	}{
		"same process":         {starts(map[int]string{4242: "Tue Nov 17  08:00:00 2026"}, true), 4242, start, true},
		"start unknown":        {starts(map[int]string{4242: start}, true), 4242, "", false},
		"start blank":          {starts(map[int]string{4242: start}, true), 4242, "  ", false},
		"both blank":           {starts(map[int]string{4242: " "}, true), 4242, " ", false},
		"pid taken by another": {starts(map[int]string{4242: "Wed Nov 18 09:00:00 2026"}, true), 4242, start, false},
		"not running":          {starts(map[int]string{}, true), 4242, start, false},
		"ps cannot tell":       {starts(nil, false), 4242, start, false},
		"no pid":               {starts(map[int]string{0: start}, true), 0, start, false},
	} {
		if got := processAlive(context.Background(), c.s, c.pid, c.start); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	// The real one, on this test's own process.
	own, ok := defaultStarts()(context.Background(), []int{os.Getpid()})
	if !ok || !ProcessAlive(context.Background(), os.Getpid(), own[os.Getpid()]) {
		t.Fatalf("this process not alive: %v %v", own, ok)
	}
	if ProcessAlive(context.Background(), os.Getpid(), "Mon Jan  1 00:00:00 2001") {
		t.Fatal("this process alive with another start time")
	}
}

// The pane keeps the start time ps printed for the process, which the click
// then requires, not the one in the session record (which may be missing);
// with no start time from ps there is no pane.
func TestTmuxPaneKeepsTheStartTimePsPrinted(t *testing.T) {
	for name, c := range map[string]struct {
		ps   string
		want string // the pane's Start; "" means no pane
	}{
		"ps printed it":   {procStart, procStart},
		"ps printed none": {" ", ""},
	} {
		e := newEnv(t)
		e.tmux = map[int]model.TmuxPane{4242: {Socket: "/tmp/tmux-501/default", Pane: "%7"}}
		e.ttys = map[int]string{4242: "/dev/ttys004"}
		e.owns = func(model.TmuxPane) bool { return true }
		e.setProc(4242, c.ps)
		e.record(4242, func(m map[string]any) { m["procStart"] = "" }) // an older record without it
		col := run(t, e.adapter())
		col.wait(t, 2*time.Second, name, session(coordID, func(f model.SessionFact) bool { return f.Live.OK && f.Live.V && f.TerminalID == termCoord }))
		waitScans(t, e, 2)
		var got model.TmuxPane
		for _, f := range col.all() {
			if s, ok := f.(model.SessionFact); ok {
				got = s.Tmux
			}
		}
		if got.Start != c.want || (c.want == "") != (got == model.TmuxPane{}) {
			t.Errorf("%s: pane %+v, want Start %q", name, got, c.want)
		}
	}
}

// A live session carries its process; one that ended, none.
func TestSessionFactCarriesItsProcessWhileLive(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "live with its process", session(coordID, func(f model.SessionFact) bool { return f.Live.V && f.PID == 4242 }))
	e.mu.Lock()
	delete(e.procs, 4242) // the process ends
	e.mu.Unlock()
	c.wait(t, 2*time.Second, "ended without one", session(coordID, func(f model.SessionFact) bool { return f.Ended && f.PID == 0 }))
}

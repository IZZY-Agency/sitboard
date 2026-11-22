package tmux

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

const (
	sock    = "/tmp/tmux-501/default"
	agterm1 = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
)

// fakeTmux answers tmux calls from canned output and records them.
type fakeTmux struct {
	mu      sync.Mutex
	calls   [][]string
	panes   string // list-panes output
	clients string // list-clients output
	fail    map[string]error
}

func (f *fakeTmux) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{name}, args...))
	if len(args) < 4 || args[0] != "-N" || args[1] != "-S" {
		return nil, errors.New("called without -N -S <socket>")
	}
	cmd := args[3]
	if err := f.fail[cmd]; err != nil {
		return nil, err
	}
	switch cmd {
	case "list-panes":
		return []byte(f.panes), nil
	case "list-clients":
		return []byte(f.clients), nil
	case "select-window", "select-pane", "switch-client":
		return nil, nil
	}
	return nil, errors.New("unexpected call " + cmd)
}

// commands are the tmux commands run, without the binary and -N -S <socket>.
func (f *fakeTmux) commands() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		out = append(out, c[4:])
	}
	return out
}

type fakeRaise struct{ got []model.FocusTarget }

func (r *fakeRaise) Jump(_ context.Context, t model.FocusTarget) error {
	r.got = append(r.got, t)
	return nil
}

// newAdapter builds the adapter on the fake tmux f, with tmux installed and
// every socket safe. A nil f keeps everything real, as outside tests: only
// TestRealTmux and the tests of the real wiring pass nil.
func newAdapter(t *testing.T, opts Options, f *fakeTmux) *Adapter {
	t.Helper()
	a := New(opts)
	if f == nil {
		return a
	}
	a.run = f.run
	a.bin = "/usr/bin/tmux"
	a.socket = func(p string) (string, error) { return p, nil }
	return a
}

// target is pane on sock, showing the terminal /dev/ttys10N for pane %N,
// for the session process 4242.
func target(pane string) model.FocusTarget {
	return model.FocusTarget{Kind: model.FocusTmux, ID: pane, Socket: sock, TTY: "/dev/ttys10" + pane[1:], PID: 4242, Start: start4242}
}

const start4242 = "Tue Nov 17 08:00:00 2026"

// Two sessions: $0 with pane %0 in window @0, $1 with panes %1 and %2 in
// windows @1 and @2.
const panes = "%0 $0 @0 /dev/ttys100\n%1 $1 @1 /dev/ttys101\n%2 $1 @2 /dev/ttys102\n"

func TestJumpSelectsThePaneWhenAClientShowsItsSession(t *testing.T) {
	f := &fakeTmux{panes: panes, clients: "200 11 /dev/pts/3 $0\n100 12 /dev/pts/4 $1\n"}
	if err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%2")); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"list-panes", "-a", "-F", "#{pane_id} #{session_id} #{window_id} #{pane_tty}"},
		{"list-clients", "-F", "#{client_activity} #{client_pid} #{client_tty} #{session_id}"},
		{"select-window", "-t", "$1:@2"},
		{"select-pane", "-t", "%2"},
	}
	if got := f.commands(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("commands\n got %q\nwant %q", got, want)
	}
	for _, c := range f.calls {
		if c[0] != "/usr/bin/tmux" || c[3] != sock {
			t.Fatalf("call %q not on the pane's socket", c)
		}
	}
}

// The trap from the spike: a bare switch-client moves the most recently
// active client even when another already shows the session. The jump must
// leave both alone and only change the view of the one showing it.
func TestJumpNeverMovesAClientAwayWhenAnotherShowsTheSession(t *testing.T) {
	f := &fakeTmux{panes: panes, clients: "900 11 /dev/pts/3 $0\n100 12 /dev/pts/4 $1\n"}
	if err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%1")); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.commands() {
		if c[0] == "switch-client" {
			t.Fatalf("switched a client: %q", c)
		}
	}
}

func TestJumpSwitchesTheMostRecentClientWhenNoneShowsTheSession(t *testing.T) {
	f := &fakeTmux{panes: panes, clients: "100 11 /dev/pts/3 $0\n300 12 /dev/pts/4 $0\n200 13 /dev/pts/5 $0\n"}
	if err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%1")); err != nil {
		t.Fatal(err)
	}
	cmds := f.commands()
	if last := cmds[len(cmds)-1]; !slices.Equal(last, []string{"switch-client", "-c", "/dev/pts/4", "-t", "%1"}) {
		t.Fatalf("last command %q, want a switch of /dev/pts/4", last)
	}
}

func TestJumpPicksTheMostRecentOfTheClientsShowingTheSession(t *testing.T) {
	f := &fakeTmux{panes: panes, clients: "100 11 /dev/pts/3 $1\n900 12 /dev/pts/4 $0\n300 13 /dev/pts/5 $1\n"}
	raise := &fakeRaise{}
	of := func(pid int) string {
		if pid == 13 {
			return agterm1
		}
		return ""
	}
	if err := newAdapter(t, Options{AgtermOf: of, Raise: raise}, f).Jump(context.Background(), target("%1")); err != nil {
		t.Fatal(err)
	}
	if want := []model.FocusTarget{{Kind: model.FocusAgterm, ID: agterm1}}; !slices.Equal(raise.got, want) {
		t.Fatalf("raised %+v, want the agterm session of client pid 13", raise.got)
	}
}

func TestJumpRaisesTheAgtermSessionOfTheClientFirst(t *testing.T) {
	f := &fakeTmux{panes: panes, clients: "100 12 /dev/pts/4 $0\n"}
	var order []string
	raise := jumperFunc(func(t model.FocusTarget) error {
		order = append(order, "raise "+t.ID)
		return nil
	})
	of := func(pid int) string {
		if pid != 12 {
			return ""
		}
		return agterm1
	}
	a := newAdapter(t, Options{AgtermOf: of, Raise: raise}, f)
	run := a.run
	a.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		order = append(order, args[3])
		return run(ctx, name, args...)
	}
	if err := a.Jump(context.Background(), target("%1")); err != nil {
		t.Fatal(err)
	}
	want := []string{"list-panes", "list-clients", "raise " + agterm1, "switch-client"}
	if !slices.Equal(order, want) {
		t.Fatalf("order %q, want %q", order, want)
	}
}

func TestJumpGoesOnWhenTheClientIsNotInAgtermOrRaiseFails(t *testing.T) {
	f := &fakeTmux{panes: panes, clients: "100 12 /dev/pts/4 $1\n"}
	none := &fakeRaise{}
	if err := newAdapter(t, Options{AgtermOf: func(int) string { return "" }, Raise: none}, f).Jump(context.Background(), target("%1")); err != nil {
		t.Fatal(err)
	}
	if len(none.got) != 0 {
		t.Fatalf("raised %+v for a client outside agterm", none.got)
	}
	failing := jumperFunc(func(model.FocusTarget) error { return errors.New("agterm down") })
	f2 := &fakeTmux{panes: panes, clients: "100 12 /dev/pts/4 $1\n"}
	if err := newAdapter(t, Options{AgtermOf: func(int) string { return agterm1 }, Raise: failing}, f2).Jump(context.Background(), target("%1")); err != nil {
		t.Fatalf("a failed raise stopped the jump: %v", err)
	}
	if cmds := f2.commands(); cmds[len(cmds)-1][0] != "select-pane" {
		t.Fatalf("pane not selected after a failed raise: %q", cmds)
	}
}

func TestJumpRefusesMalformedTargetsWithoutCallingTmux(t *testing.T) {
	const tty1 = "/dev/ttys101"
	for _, tg := range []model.FocusTarget{
		{Kind: model.FocusAgterm, ID: "%1", Socket: sock, TTY: tty1},
		{Kind: model.FocusTmux, ID: "1", Socket: sock, TTY: tty1},
		{Kind: model.FocusTmux, ID: "%1 -t x", Socket: sock, TTY: tty1},
		{Kind: model.FocusTmux, ID: "-%1", Socket: sock, TTY: tty1},
		{Kind: model.FocusTmux, ID: "%1", Socket: "", TTY: tty1},
		{Kind: model.FocusTmux, ID: "%1", Socket: "-L x", TTY: tty1},
		{Kind: model.FocusTmux, ID: "%1", Socket: "tmp/sock", TTY: tty1},
		{Kind: model.FocusTmux, ID: "%1", Socket: "/tmp/../sock", TTY: tty1},
		{Kind: model.FocusTmux, ID: "%1", Socket: "/tmp/a b", TTY: tty1},
		{Kind: model.FocusTmux, ID: "%1", Socket: sock},
		{Kind: model.FocusTmux, ID: "%1", Socket: sock, TTY: "ttys101"},
		{Kind: model.FocusTmux, ID: "%1", Socket: sock, TTY: "/dev/../etc/x"},
		{Kind: model.FocusTmux, ID: "%1", Socket: sock, TTY: "/dev/a b"},
	} {
		f := &fakeTmux{panes: panes}
		if err := newAdapter(t, Options{}, f).Jump(context.Background(), tg); !errors.Is(err, source.ErrUnknownTerminal) {
			t.Errorf("Jump(%+v) = %v", tg, err)
		}
		if len(f.calls) != 0 {
			t.Errorf("tmux called for %+v: %q", tg, f.calls)
		}
	}
}

func TestJumpToAPaneThatIsGone(t *testing.T) {
	f := &fakeTmux{panes: panes, clients: "100 12 /dev/pts/4 $1\n"}
	if err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%9")); !errors.Is(err, source.ErrUnknownTerminal) {
		t.Fatalf("Jump = %v, want ErrUnknownTerminal", err)
	}
	if n := len(f.calls); n != 1 {
		t.Fatalf("%d calls after the pane was not found, want only list-panes", n)
	}
	// No server on the socket any more.
	f = &fakeTmux{fail: map[string]error{"list-panes": &runner.ExitError{Code: 1, Stderr: []byte("no server running on /tmp/tmux-501/default\n")}}}
	if err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%1")); !errors.Is(err, source.ErrUnknownTerminal) {
		t.Fatalf("no server: Jump = %v", err)
	}
	// The pane went away between the reads.
	f = &fakeTmux{panes: panes, clients: "100 12 /dev/pts/4 $1\n", fail: map[string]error{"select-window": &runner.ExitError{Code: 1, Stderr: []byte("can't find pane: %1\n")}}}
	if err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%1")); !errors.Is(err, source.ErrUnknownTerminal) {
		t.Fatalf("pane gone: Jump = %v", err)
	}
}

// Grouped sessions ($1 and $2 share window @1, and %1 in it): list-panes -a
// lists the pane under both. A client on either already shows it, so the
// most recent of those changes its view inside its own session, and the
// newest client, elsewhere, is never moved.
func TestJumpWithGroupedSessions(t *testing.T) {
	grouped := "%0 $0 @0 /dev/ttys100\n%1 $1 @1 /dev/ttys101\n%1 $2 @1 /dev/ttys101\n"
	for name, c := range map[string]struct{ clients, window string }{
		"clients on both":             {"100 11 /dev/pts/3 $1\n900 12 /dev/pts/4 $0\n300 13 /dev/pts/5 $2\n", "$2:@1"},
		"a client on the second only": {"900 12 /dev/pts/4 $0\n300 13 /dev/pts/5 $2\n", "$2:@1"},
		"a client on the first only":  {"900 12 /dev/pts/4 $0\n300 13 /dev/pts/5 $1\n", "$1:@1"},
	} {
		f := &fakeTmux{panes: grouped, clients: c.clients}
		if err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%1")); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := [][]string{{"select-window", "-t", c.window}, {"select-pane", "-t", "%1"}}
		if got := f.commands()[2:]; !slices.EqualFunc(got, want, slices.Equal) {
			t.Errorf("%s: commands %q, want %q", name, got, want)
		}
	}
}

// Lines of list-panes of the wrong shape do not place the pane: its window
// and session go into a tmux target.
func TestJumpIgnoresPaneLinesOfTheWrongShape(t *testing.T) {
	for _, line := range []string{
		"%1 $1 1 /dev/ttys101", "%1 $1 @1:x /dev/ttys101", "%1 $1 -t /dev/ttys101",
		"%1 1 @1 /dev/ttys101", "%1 $1 @1", "%1 $1 @1 /dev/ttys101 x",
	} {
		f := &fakeTmux{panes: line + "\n", clients: "100 12 /dev/pts/4 $1\n"}
		if err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%1")); !errors.Is(err, source.ErrUnknownTerminal) || len(f.calls) != 1 {
			t.Errorf("%q: Jump = %v, calls %q", line, err, f.calls)
		}
	}
}

// The server was restarted and %1 now names a pane on another terminal than
// the session's: that is not the session's pane, and nothing moves.
func TestJumpToAReusedPaneID(t *testing.T) {
	f := &fakeTmux{panes: "%1 $0 @1 /dev/ttys200\n", clients: "100 12 /dev/pts/4 $0\n"}
	if err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%1")); !errors.Is(err, source.ErrUnknownTerminal) {
		t.Fatalf("Jump = %v, want ErrUnknownTerminal", err)
	}
	if n := len(f.calls); n != 1 {
		t.Fatalf("%d calls, want only list-panes", n)
	}
}

// The session's process ended and a new pane took both its pane id and its
// terminal before the board noticed: tmux would confirm the pane, so the
// click asks whether the process still runs first, and focuses nothing.
func TestJumpWhenTheSessionProcessEnded(t *testing.T) {
	type call struct {
		pid   int
		start string
	}
	var asked []call
	alive := false
	opts := Options{Alive: func(_ context.Context, pid int, start string) bool {
		asked = append(asked, call{pid, start})
		return alive
	}}
	f := &fakeTmux{panes: panes, clients: "100 12 /dev/pts/4 $1\n"} // %1 on /dev/ttys101, as the target says
	if err := newAdapter(t, opts, f).Jump(context.Background(), target("%1")); !errors.Is(err, source.ErrUnknownTerminal) || len(f.calls) != 0 {
		t.Fatalf("Jump = %v, calls %q; want ErrUnknownTerminal and no tmux call", err, f.calls)
	}
	if want := []call{{4242, start4242}}; !slices.Equal(asked, want) {
		t.Fatalf("asked %v, want %v", asked, want)
	}
	alive = true
	if err := newAdapter(t, opts, f).Jump(context.Background(), target("%1")); err != nil {
		t.Fatalf("process still runs: %v", err)
	}
	noPID := target("%1")
	noPID.PID = 0
	f.calls = nil
	if err := newAdapter(t, opts, f).Jump(context.Background(), noPID); !errors.Is(err, source.ErrUnknownTerminal) || len(f.calls) != 0 {
		t.Fatalf("no pid: Jump = %v, calls %q", err, f.calls)
	}
}

func TestOwns(t *testing.T) {
	pane := model.TmuxPane{Socket: sock, Pane: "%1", TTY: "/dev/ttys101"}
	f := &fakeTmux{panes: panes}
	a := newAdapter(t, Options{}, f)
	if !a.Owns(context.Background(), pane) {
		t.Fatal("the pane showing the tty not owned")
	}
	if c := f.commands(); len(c) != 1 || c[0][0] != "list-panes" {
		t.Fatalf("commands %q, want one list-panes", c)
	}
	other := pane
	other.TTY = "/dev/ttys999" // an editor's terminal that inherited TMUX_PANE=%1
	if a.Owns(context.Background(), other) {
		t.Fatal("a pane showing another terminal owned")
	}
	for name, b := range map[string]*Adapter{
		"unsafe socket": func() *Adapter {
			b := newAdapter(t, Options{}, f)
			b.socket = func(string) (string, error) { return "", errUnsafeSocket }
			return b
		}(),
		"no tmux": func() *Adapter {
			b := newAdapter(t, Options{}, f)
			b.bin = ""
			return b
		}(),
	} {
		f.calls = nil
		if b.Owns(context.Background(), pane) || len(f.calls) != 0 {
			t.Errorf("%s: owned, or tmux ran: %q", name, f.calls)
		}
	}
	if a.Owns(context.Background(), model.TmuxPane{Socket: sock, Pane: "%1"}) {
		t.Fatal("a pane without a tty owned")
	}
	f = &fakeTmux{fail: map[string]error{"list-panes": &runner.ExitError{Code: 1, Stderr: []byte("no server running\n")}}}
	if newAdapter(t, Options{}, f).Owns(context.Background(), pane) {
		t.Fatal("owned with no server")
	}
}

// The chosen client detaches between list-clients and switch-client: the
// pane is still there, so this is "no client" (the page offers Retry), not
// "the session is gone".
func TestJumpWhenTheClientDetachesMidway(t *testing.T) {
	f := &fakeTmux{panes: panes, clients: "100 12 /dev/pts/4 $0\n", fail: map[string]error{
		"switch-client": &runner.ExitError{Code: 1, Stderr: []byte("can't find client: /dev/pts/4\n")},
	}}
	if err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%1")); !errors.Is(err, source.ErrNoClient) || errors.Is(err, source.ErrUnknownTerminal) {
		t.Fatalf("Jump = %v, want ErrNoClient", err)
	}
	// The pane going away at that moment is still "gone".
	f = &fakeTmux{panes: panes, clients: "100 12 /dev/pts/4 $0\n", fail: map[string]error{
		"switch-client": &runner.ExitError{Code: 1, Stderr: []byte("can't find pane: %1\n")},
	}}
	if err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%1")); !errors.Is(err, source.ErrUnknownTerminal) {
		t.Fatalf("pane gone at the switch: Jump = %v, want ErrUnknownTerminal", err)
	}
}

func TestJumpWithNoClientAttached(t *testing.T) {
	f := &fakeTmux{panes: panes, clients: ""}
	if err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%1")); !errors.Is(err, source.ErrNoClient) {
		t.Fatalf("Jump = %v, want ErrNoClient", err)
	}
	// Lines of the wrong shape are not clients.
	f = &fakeTmux{panes: panes, clients: "x 12 /dev/pts/4 $1\n100 0 /dev/pts/4 $1\n100 12 -t $1\n100 12 /dev/pts/4 1\n100 12 /dev/pts/4\n"}
	if err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%1")); !errors.Is(err, source.ErrNoClient) {
		t.Fatalf("malformed clients: Jump = %v, want ErrNoClient", err)
	}
}

func TestJumpOtherFailures(t *testing.T) {
	f := &fakeTmux{panes: panes}
	a := newAdapter(t, Options{}, f)
	a.bin = ""
	if err := a.Jump(context.Background(), target("%1")); !errors.Is(err, source.ErrNotInstalled) {
		t.Fatalf("no tmux: Jump = %v", err)
	}
	f = &fakeTmux{fail: map[string]error{"list-panes": &runner.ExitError{Code: 1, Stderr: []byte("protocol version mismatch\n")}}}
	err := newAdapter(t, Options{}, f).Jump(context.Background(), target("%1"))
	if err == nil || errors.Is(err, source.ErrUnknownTerminal) || errors.Is(err, source.ErrNoClient) {
		t.Fatalf("other failure: Jump = %v, want a plain error", err)
	}
	if strings.Contains(err.Error(), "protocol") {
		t.Fatalf("tmux's own text leaked into the error: %v", err)
	}
}

type jumperFunc func(model.FocusTarget) error

func (j jumperFunc) Jump(_ context.Context, t model.FocusTarget) error { return j(t) }

// Package tmux is the tmux jump target: it focuses a tmux pane from outside.
//
// It talks to the tmux server on the pane's own socket (tested with tmux
// 3.5a), never starting one (-N):
//
//	tmux -N -S <socket> list-panes -a -F ...      the pane exists and shows the
//	                                              session's terminal; its window
//	                                              and the sessions holding it
//	tmux -N -S <socket> list-clients -F ...       the clients, to pick one
//	tmux -N -S <socket> select-window -t <session>:<window>   a client already
//	tmux -N -S <socket> select-pane -t <pane>     shows one of those sessions:
//	                                              change its view
//	tmux -N -S <socket> switch-client -c <tty> -t <pane>   otherwise: move the
//	                                              most recently active client
//
// A bare switch-client moves the most recently active client even when
// another one already shows the pane's session, so the client is always
// chosen here and named with -c. tmux cannot raise the terminal window that
// shows the client; when that client runs in agterm, the agterm session is
// focused first.
//
// Fields in tmux's answers are split on spaces: tmux 3.5a prints a tab in a
// format as "_", and no field asked for (ids, numbers, a tty) holds a space.
//
// Every call takes an argument list, never a shell string; the pane and the
// socket are checked against their shapes (model.TmuxPane.Valid) before they
// are passed on, and the client's tty is taken only from tmux's own answer.
// The socket comes from another process's environment, so before the first
// call it must also be a socket this user owns, in folders no other user can
// change (safeSocket), and tmux runs with PATH as its only environment.
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Options configure the adapter.
type Options struct {
	Bin string // tmux binary; "" means look "tmux" up on PATH (lookBin)
	// AgtermOf returns the agterm session id in the environment of the
	// user's process pid (a tmux client), "" when none. Nil: never looked up.
	AgtermOf func(pid int) string
	// Raise focuses an agterm session. Nil: the window showing the tmux
	// client is not raised.
	Raise source.Jumper
	// Alive says whether the session's process pid still runs with the start
	// time start (an empty one matches nothing); a jump focuses nothing when it does not,
	// as its pane id and terminal may now be a new pane's. Nil: not asked.
	Alive func(ctx context.Context, pid int, start string) bool
}

const callTimeout = 5 * time.Second

// Adapter focuses tmux panes. It implements source.Jumper.
type Adapter struct {
	opts   Options
	bin    string // tmux's absolute path, found once; "" when not found
	run    runner.Func
	socket func(string) (string, error) // safeSocket for this user; a seam for tests
}

// New returns the tmux adapter.
func New(opts Options) *Adapter {
	name := opts.Bin
	if name == "" {
		name = "tmux"
	}
	uid := os.Getuid()
	return &Adapter{
		opts: opts,
		bin:  lookBin(name, os.Getenv("PATH"), uid),
		// Only PATH: tmux sends its whole environment to the server, and
		// sitboard's own TMUX and TMUX_PANE (when it runs in tmux) must not
		// reach the child.
		run:    runner.ExecEnv(callTimeout, []string{"PATH=" + os.Getenv("PATH")}),
		socket: func(p string) (string, error) { return safeSocket(p, uid) },
	}
}

var (
	sessionIDRe = regexp.MustCompile(`^\$[0-9]{1,9}$`)
	windowIDRe  = regexp.MustCompile(`^@[0-9]{1,9}$`)
)

// client is one tmux client as listed.
type client struct {
	activity int64
	pid      int
	tty      string
	session  string // the id of the session it shows
}

// Options returns the options the adapter was built with, so the program's
// wiring can be checked.
func (a *Adapter) Options() Options { return a.opts }

// Jump focuses the tmux pane target.ID on the server at target.Socket, when
// that pane still shows target.TTY, the session's own terminal.
func (a *Adapter) Jump(ctx context.Context, target model.FocusTarget) error {
	pane := model.TmuxPane{Socket: target.Socket, Pane: target.ID, TTY: target.TTY}
	if target.Kind != model.FocusTmux || target.PID <= 0 {
		return source.ErrUnknownTerminal
	}
	pane, err := a.check(pane)
	if err != nil {
		return err
	}
	if a.opts.Alive != nil && !a.opts.Alive(ctx, target.PID, target.Start) {
		return fmt.Errorf("%w: the session's process ended", source.ErrUnknownTerminal)
	}
	where, err := a.paneWhere(ctx, pane)
	if err != nil {
		return err
	}
	c, err := a.pickClient(ctx, pane.Socket, where.sessions)
	if err != nil {
		return err
	}
	a.raise(ctx, c.pid)
	if where.sessions[c.session] {
		// A client already shows a session holding the pane: change only
		// its view, inside its own session (with grouped sessions, a bare
		// pane target could change another session's).
		if _, err := a.tmux(ctx, pane.Socket, "select-window", "-t", c.session+":"+where.window); err != nil {
			return fmt.Errorf("tmux: select window: %w", gone(err))
		}
		if _, err := a.tmux(ctx, pane.Socket, "select-pane", "-t", pane.Pane); err != nil {
			return fmt.Errorf("tmux: select pane: %w", gone(err))
		}
		return nil
	}
	if _, err := a.tmux(ctx, pane.Socket, "switch-client", "-c", c.tty, "-t", pane.Pane); err != nil {
		if says(err, "can't find client") {
			// The client detached since it was listed; the pane is still
			// there, so another try can work.
			return fmt.Errorf("tmux: switch client: %w", source.ErrNoClient)
		}
		return fmt.Errorf("tmux: switch client: %w", gone(err))
	}
	return nil
}

// Owns says whether pane is a pane of the server at pane.Socket that shows
// pane.TTY. The claude watcher asks it once per process, so a pane read from
// inherited, stale TMUX and TMUX_PANE variables is never shown as one.
func (a *Adapter) Owns(ctx context.Context, pane model.TmuxPane) bool {
	pane, err := a.check(pane)
	if err != nil {
		return false
	}
	_, err = a.paneWhere(ctx, pane)
	return err == nil
}

// check refuses a pane of the wrong shape, a missing tmux and an unsafe
// socket, before any tmux call; it returns the pane with the checked socket
// path.
func (a *Adapter) check(pane model.TmuxPane) (model.TmuxPane, error) {
	if !pane.Valid() {
		return pane, source.ErrUnknownTerminal
	}
	if a.bin == "" {
		return pane, source.ErrNotInstalled
	}
	sock, err := a.socket(pane.Socket)
	if err != nil {
		return pane, fmt.Errorf("%w: %w", source.ErrUnknownTerminal, err)
	}
	pane.Socket = sock
	return pane, nil
}

// raise focuses the agterm session the tmux client runs in, when it runs in
// one. A failure is logged and does not stop the jump: the pane still moves.
func (a *Adapter) raise(ctx context.Context, pid int) {
	if a.opts.AgtermOf == nil || a.opts.Raise == nil {
		return
	}
	id := a.opts.AgtermOf(pid)
	if id == "" {
		return
	}
	if err := a.opts.Raise.Jump(ctx, model.FocusTarget{Kind: model.FocusAgterm, ID: id}); err != nil {
		log.Printf("tmux: jump: could not raise the agterm session showing the client: %v", err)
	}
}

// place is where a pane is: its window, and every session holding that
// window (more than one with grouped sessions or a linked window: list-panes
// -a lists the pane once for each).
type place struct {
	window   string
	sessions map[string]bool
}

// paneWhere returns where the pane is, read fresh. A pane that shows another
// terminal than pane.TTY is not the session's: the server was restarted and
// the id now names another pane.
func (a *Adapter) paneWhere(ctx context.Context, pane model.TmuxPane) (place, error) {
	out, err := a.tmux(ctx, pane.Socket, "list-panes", "-a", "-F", "#{pane_id} #{session_id} #{window_id} #{pane_tty}")
	if err != nil {
		return place{}, fmt.Errorf("tmux: list panes: %w", gone(err))
	}
	p := place{sessions: map[string]bool{}}
	for line := range bytes.Lines(out) {
		f := bytes.Split(bytes.TrimRight(line, "\n"), []byte(" "))
		if len(f) != 4 || string(f[0]) != pane.Pane || string(f[3]) != pane.TTY ||
			!sessionIDRe.Match(f[1]) || !windowIDRe.Match(f[2]) {
			continue
		}
		if p.window != "" && p.window != string(f[2]) {
			return place{}, source.ErrUnknownTerminal // a pane is in one window
		}
		p.window = string(f[2])
		p.sessions[string(f[1])] = true
	}
	if p.window == "" {
		return place{}, source.ErrUnknownTerminal
	}
	return p, nil
}

// pickClient chooses the client to show the pane: the most recently active
// one already showing one of its sessions, else the most recently active of
// all.
func (a *Adapter) pickClient(ctx context.Context, socket string, sessions map[string]bool) (client, error) {
	out, err := a.tmux(ctx, socket, "list-clients", "-F", "#{client_activity} #{client_pid} #{client_tty} #{session_id}")
	if err != nil {
		return client{}, fmt.Errorf("tmux: list clients: %w", gone(err))
	}
	var best, bestHere client
	var some, here bool
	for line := range bytes.Lines(out) {
		c, ok := parseClient(bytes.TrimRight(line, "\n"))
		if !ok {
			continue
		}
		if !some || c.activity > best.activity {
			best, some = c, true
		}
		if sessions[c.session] && (!here || c.activity > bestHere.activity) {
			bestHere, here = c, true
		}
	}
	switch {
	case here:
		return bestHere, nil
	case some:
		return best, nil
	}
	return client{}, source.ErrNoClient
}

func parseClient(line []byte) (client, bool) {
	f := bytes.Split(line, []byte(" "))
	if len(f) != 4 {
		return client{}, false
	}
	act, err1 := strconv.ParseInt(string(f[0]), 10, 64)
	pid, err2 := strconv.Atoi(string(f[1]))
	if err1 != nil || err2 != nil || pid <= 0 || !model.ValidTTY(string(f[2])) || !sessionIDRe.Match(f[3]) {
		return client{}, false
	}
	return client{activity: act, pid: pid, tty: string(f[2]), session: string(f[3])}, true
}

// tmux runs one tmux command on the server at socket, never starting one.
func (a *Adapter) tmux(ctx context.Context, socket string, args ...string) ([]byte, error) {
	return a.run(ctx, a.bin, append([]string{"-N", "-S", socket}, args...)...)
}

// gone turns tmux's "this does not exist" answers into ErrUnknownTerminal:
// no server on the socket (tmux 3.4 says "server exited unexpectedly" when
// the server dies as the client connects), or a pane, window or client that went away.
func gone(err error) error {
	for _, s := range []string{"no server running", "error connecting", "server exited unexpectedly", "can't find", "no such file"} {
		if says(err, s) {
			return fmt.Errorf("%w: %s", source.ErrUnknownTerminal, s)
		}
	}
	return err
}

// says reports whether err is tmux exiting with s (lower case) in its
// standard error.
func says(err error, s string) bool {
	ee, ok := errors.AsType[*runner.ExitError](err)
	return ok && bytes.Contains(bytes.ToLower(ee.Stderr), []byte(s))
}

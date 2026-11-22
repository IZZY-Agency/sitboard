package claude

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/proc"
	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// Liveness is shared with the other agent adapters (internal/adapters/internal/proc).
type startsFunc = proc.StartsFunc

var (
	psStarts      = proc.PSStarts
	defaultStarts = proc.DefaultStarts
	parsePS       = proc.ParsePS
	sameStart     = proc.SameStart
)

// ttyFunc returns the terminal of process pid as a path ("/dev/ttys004",
// "/dev/pts/3"), "" when it has none or ps cannot tell.
type ttyFunc func(ctx context.Context, pid int) string

// psTTY asks ps for the terminal of one process. ps prints its name below
// /dev ("ttys004" on macOS, "pts/3" on Linux), and "??" or "?" for none.
func psTTY(run runner.Func) ttyFunc {
	return func(ctx context.Context, pid int) string {
		out, err := run(ctx, "ps", "-o", "tty=", "-p", strconv.Itoa(pid))
		if err != nil {
			return ""
		}
		tty := "/dev/" + strings.TrimSpace(string(out))
		if !model.ValidTTY(tty) {
			return ""
		}
		return tty
	}
}

func defaultTTY() ttyFunc {
	return psTTY(runner.Exec(2*time.Second, "LC_ALL=C"))
}

// ownPane keeps the tmux pane read from process pid's environment only when
// tmux confirms that the pane shows the process's own terminal. TMUX and
// TMUX_PANE are inherited: a program started from a tmux pane (an editor
// whose terminal then starts claude) carries them too, and after a tmux
// server restart the same pane id can name another pane. A process holds its
// terminal open, so no other pane can show that terminal while it runs.
//
// The process and the start time ps printed for it (start) go with the
// pane: a click confirms that process still runs before focusing
// (ProcessAlive), since a new pane can take both the pane id and the terminal
// once it ended. Without a start time there is no pane.
func (a *adapter) ownPane(ctx context.Context, pid int, start string, pane model.TmuxPane) model.TmuxPane {
	if !pane.ValidPlace() || a.opts.TmuxOwns == nil || strings.TrimSpace(start) == "" {
		return model.TmuxPane{}
	}
	pane.TTY = a.tty(ctx, pid)
	if !pane.Valid() || !a.opts.TmuxOwns(ctx, pane) {
		return model.TmuxPane{}
	}
	pane.PID, pane.Start = pid, start
	return pane
}

// ProcessAlive says whether process pid runs and started at start (ps
// lstart): the same liveness check the watcher makes. False when ps cannot
// tell, and for an empty start, which matches nothing. The tmux jump asks it
// at click time.
func ProcessAlive(ctx context.Context, pid int, start string) bool {
	return processAlive(ctx, defaultStarts(), pid, start)
}

func processAlive(ctx context.Context, starts startsFunc, pid int, start string) bool {
	if pid <= 0 || strings.TrimSpace(start) == "" {
		return false
	}
	got, ok := starts(ctx, []int{pid})
	if !ok {
		return false
	}
	s, running := got[pid]
	return running && sameStart(s, start)
}

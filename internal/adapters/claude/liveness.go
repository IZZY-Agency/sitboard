package claude

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
)

// startsFunc returns the start time, as `ps -o lstart=` prints it, of each pid
// in pids that is running. ok is false when it could not tell at all.
type startsFunc func(ctx context.Context, pids []int) (starts map[int]string, ok bool)

// psStarts asks ps once for all pids. Claude Code records the same value for
// its own process in procStart (with LC_ALL=C and TZ=UTC), so comparing the
// two tells a live session from a reused pid: a pid that a new process took
// over after the session ended has a different start time.
func psStarts(run runner.Func) startsFunc {
	return func(ctx context.Context, pids []int) (map[int]string, bool) {
		if len(pids) == 0 {
			return map[int]string{}, true
		}
		list := make([]string, len(pids))
		for i, p := range pids {
			list[i] = strconv.Itoa(p)
		}
		out, err := run(ctx, "ps", "-o", "pid=,lstart=", "-p", strings.Join(list, ","))
		if err != nil {
			// ps exits 1, silently, when some pid is not running; what it
			// printed still counts. Any other failure, or an exit 1 with an
			// error message, says nothing about the sessions: unknown.
			var ee *runner.ExitError
			if !errors.As(err, &ee) || ee.Code != 1 || len(bytes.TrimSpace(ee.Stderr)) > 0 {
				return nil, false
			}
			out = ee.Stdout
		}
		return parsePS(out), true
	}
}

func defaultStarts() startsFunc {
	return psStarts(runner.Exec(2*time.Second, "LC_ALL=C", "TZ=UTC"))
}

func parsePS(out []byte) map[int]string {
	starts := map[int]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil || pid <= 0 {
			continue
		}
		starts[pid] = strings.Join(f[1:], " ")
	}
	return starts
}

// sameStart compares two lstart strings, ignoring how spaces pad them.
func sameStart(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

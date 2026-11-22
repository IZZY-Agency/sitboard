package codex

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
)

// defaultHolders reads /proc/<pid>/fd of the user's own processes: each fd is
// a link to the file it has open. Without /proc, lsof.
func defaultHolders() holdersFunc {
	lsof := lsofHolders(runner.Exec(2 * time.Second))
	return func(ctx context.Context, paths []string) (map[string]int, bool) {
		if len(paths) == 0 {
			return map[string]int{}, true
		}
		procs, err := os.ReadDir("/proc")
		if err != nil {
			return lsof(ctx, paths)
		}
		want := map[string]bool{}
		for _, p := range paths {
			want[p] = true
		}
		holders := map[string]int{}
		for _, e := range procs {
			if ctx.Err() != nil {
				return nil, false
			}
			pid, err := strconv.Atoi(e.Name())
			if err != nil || pid <= 0 {
				continue
			}
			dir := filepath.Join("/proc", e.Name(), "fd")
			fds, err := os.ReadDir(dir) // another user's process: not readable, skipped
			if err != nil {
				continue
			}
			for _, fd := range fds {
				target, err := os.Readlink(filepath.Join(dir, fd.Name()))
				if err == nil && want[target] {
					if _, seen := holders[target]; !seen {
						holders[target] = pid
					}
				}
			}
		}
		return holders, true
	}
}

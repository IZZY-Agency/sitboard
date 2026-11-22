package codex

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
)

// Built from the documented shape, NOT checked on a Linux machine (#111): that
// `ps -x -ww -o pid=,uid=,lstart=,command=` lists the terminal client's
// program as `codex` (an npm install may run it as `node .../codex.js` with a
// native `codex` child), that /proc/<pid>/cwd links to the working folder,
// and that the daemon's locks behave as on macOS. Arguments come from ps,
// split at blanks; /proc/<pid>/cmdline would be exact. To verify: start
// `codex` in a folder, then run `ps -x -ww -o pid=,uid=,lstart=,command= |
// grep codex` and `readlink /proc/<pid>/cwd`.
func platformCwds() cwdsFunc {
	lsof := lsofCwds(runner.Exec(2 * time.Second))
	return func(ctx context.Context, pids []int) (map[int]string, bool) {
		if _, err := os.Stat("/proc/self"); err != nil {
			return lsof(ctx, pids)
		}
		res := map[int]string{}
		for _, pid := range pids {
			if p, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd"); err == nil {
				res[pid] = p
			}
		}
		return res, true
	}
}

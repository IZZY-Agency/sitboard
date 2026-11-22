package proc

import (
	"os"
	"strconv"
)

func readArgsEnv(pid int, pick Pick) bool {
	dir := "/proc/" + strconv.Itoa(pid) + "/"
	environ, err := os.ReadFile(dir + "environ")
	if err != nil {
		return false
	}
	cmdline, _ := os.ReadFile(dir + "cmdline")        // without it, the environment alone
	defer func() { clear(environ); clear(cmdline) }() // nothing else is kept
	pick(SplitNul(cmdline), SplitNul(environ))
	return true
}

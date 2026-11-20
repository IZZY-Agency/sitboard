package claude

import (
	"os"
	"strconv"
)

// readProcFacts reads a live process's arguments and environment once and
// keeps only procFacts. Only works for the user's own processes.
func readProcFacts(pid int) procFacts {
	dir := "/proc/" + strconv.Itoa(pid) + "/"
	environ, err := os.ReadFile(dir + "environ")
	if err != nil {
		return procFacts{}
	}
	cmdline, _ := os.ReadFile(dir + "cmdline") // without it, no launch level
	f := procFactsFromLinux(cmdline, environ)
	clear(environ) // the rest of the arguments and environment is not kept
	clear(cmdline)
	return f
}

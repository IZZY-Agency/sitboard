package claude

import "golang.org/x/sys/unix"

// readProcFacts reads a live process's arguments and environment once and
// keeps only procFacts. Only works for the user's own processes.
func readProcFacts(pid int) procFacts {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return procFacts{}
	}
	f := procFactsFromProcArgs2(buf)
	clear(buf) // the rest of the arguments and environment is not kept
	return f
}

package claude

import "golang.org/x/sys/unix"

// readAgtermID returns the agterm session id from a live process's
// environment. Only works for the user's own processes.
func readAgtermID(pid int) string {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return ""
	}
	id := agtermIDFromProcArgs2(buf)
	clear(buf) // the rest of the environment is not kept
	return id
}

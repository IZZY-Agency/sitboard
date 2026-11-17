package claude

import (
	"os"
	"strconv"
)

// readAgtermID returns the agterm session id from a live process's
// environment. Only works for the user's own processes.
func readAgtermID(pid int) string {
	buf, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return ""
	}
	id := agtermIDFromEnviron(buf)
	clear(buf) // the rest of the environment is not kept
	return id
}

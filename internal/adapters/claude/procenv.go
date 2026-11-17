package claude

import (
	"bytes"
	"encoding/binary"
	"regexp"
	"strings"
)

// agtermVar is the one environment variable read from a live session's
// process. Nothing else from the environment is kept, logged or returned: it
// can hold secrets.
const agtermVar = "AGTERM_SESSION_ID"

var terminalIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// pickAgtermID returns the agterm session id from a list of NUL-separated
// KEY=VALUE entries, or "" when it is absent or not an id.
func pickAgtermID(env [][]byte) string {
	prefix := []byte(agtermVar + "=")
	for _, e := range env {
		if v, ok := bytes.CutPrefix(e, prefix); ok {
			if s := string(v); terminalIDRe.MatchString(s) {
				return strings.ToUpper(s)
			}
			return ""
		}
	}
	return ""
}

// agtermIDFromEnviron reads Linux's /proc/<pid>/environ format.
func agtermIDFromEnviron(buf []byte) string {
	return pickAgtermID(bytes.Split(buf, []byte{0}))
}

// agtermIDFromProcArgs2 reads macOS's KERN_PROCARGS2 format: argc (int32),
// the executable path, NUL padding, argc arguments, then the environment,
// each NUL-terminated. Arguments are skipped without being kept.
func agtermIDFromProcArgs2(buf []byte) string {
	if len(buf) < 4 {
		return ""
	}
	argc := int(binary.LittleEndian.Uint32(buf[:4]))
	rest := buf[4:]
	i := bytes.IndexByte(rest, 0) // end of the executable path
	if i < 0 {
		return ""
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	for ; argc > 0; argc-- {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return ""
		}
		rest = rest[i+1:]
	}
	var env [][]byte
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i <= 0 { // an empty string ends the environment
			break
		}
		env = append(env, rest[:i])
		rest = rest[i+1:]
	}
	return pickAgtermID(env)
}

package proc

import (
	"bytes"
	"encoding/binary"
)

// Pick receives a process's arguments (the first is the program) and its
// KEY=VALUE environment entries. The slices point into a buffer that is
// cleared when Pick returns: Pick copies only what it keeps.
type Pick func(args, env [][]byte)

// ReadArgsEnv reads a live process's arguments and environment once and hands
// them to pick. It reports false when they could not be read (another user's
// process, a process gone, an unsupported platform). Arguments and
// environment can hold prompts and secrets: nothing of them is kept here.
func ReadArgsEnv(pid int, pick Pick) bool { return readArgsEnv(pid, pick) }

// SplitNul splits NUL-terminated strings; a trailing NUL ends the last one.
func SplitNul(buf []byte) [][]byte {
	buf = bytes.TrimSuffix(buf, []byte{0})
	if len(buf) == 0 {
		return nil
	}
	return bytes.Split(buf, []byte{0})
}

// ParseProcArgs2 reads macOS's KERN_PROCARGS2 format: argc (int32), the
// executable path, NUL padding, argc arguments, then the environment, each
// NUL-terminated. ok is false when the buffer is cut short.
func ParseProcArgs2(buf []byte) (args, env [][]byte, ok bool) {
	if len(buf) < 4 {
		return nil, nil, false
	}
	argc := int(binary.LittleEndian.Uint32(buf[:4]))
	rest := buf[4:]
	i := bytes.IndexByte(rest, 0) // end of the executable path
	if i < 0 {
		return nil, nil, false
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	for ; argc > 0; argc-- {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return nil, nil, false
		}
		args = append(args, rest[:i])
		rest = rest[i+1:]
	}
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i <= 0 { // an empty string ends the environment
			break
		}
		env = append(env, rest[:i])
		rest = rest[i+1:]
	}
	return args, env, true
}

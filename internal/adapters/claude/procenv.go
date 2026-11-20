package claude

import (
	"bytes"
	"encoding/binary"
	"regexp"
	"strings"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// procFacts is everything kept from a live session's process: its agterm
// session id and the effort level it was launched with. Nothing else from the
// arguments or the environment is kept, logged or returned: they can hold
// prompts and secrets.
type procFacts struct {
	TermID       string // agterm session id, "" when none
	LaunchEffort string // one of model.EffortLevels, "" when none was given
}

// The environment variables read from a live session's process.
const (
	agtermVar = "AGTERM_SESSION_ID"
	// effortVar overrides the --effort flag and every later pick in the
	// session (Claude Code's documented order).
	effortVar = "CLAUDE_CODE_EFFORT_LEVEL"
)

var terminalIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// pickProcFacts reads the facts from a process's arguments (the first is the
// program) and its KEY=VALUE environment entries.
func pickProcFacts(args, env [][]byte) procFacts {
	return procFacts{TermID: pickAgtermID(env), LaunchEffort: launchEffort(args, env)}
}

// pickAgtermID returns the agterm session id from a list of KEY=VALUE
// entries, or "" when it is absent or not an id.
func pickAgtermID(env [][]byte) string {
	if v, ok := envValue(env, agtermVar); ok && terminalIDRe.Match(v) {
		return strings.ToUpper(string(v)) // only a matching id is copied
	}
	return ""
}

func envValue(env [][]byte, key string) ([]byte, bool) {
	prefix := []byte(key + "=")
	for _, e := range env {
		if v, ok := bytes.CutPrefix(e, prefix); ok {
			return v, true
		}
	}
	return nil, false
}

// launchEffort is the effort level a claude process was started with, as
// Claude Code 2.1.328 resolves it before any pick made in the session (tried):
//   - CLAUDE_CODE_EFFORT_LEVEL in the process's environment, when Claude Code
//     accepts its value, wins over the flag: a level (any case), "med" for
//     medium; "auto", "unset", or a value that starts like a number (an
//     optional sign, then a digit: "-1", "+5", "12000abc", any length) mean
//     no level is sent, so none is known. Any other value (empty, "bogus",
//     "x7") is ignored by Claude Code, and so here.
//   - Otherwise the last --effort flag (also --effort=level): a level, "med",
//     or "ultracode", which runs at xhigh. Arguments after "--" are not flags.
//
// The settings default is never read. Not seen from here, so the level can be
// wrong: CLAUDE_CODE_EFFORT_LEVEL set in a settings file's "env" block (it
// overrides the flag), and a model that cannot run the level.
//
// Arguments are compared as bytes and never converted: a prompt passed on the
// command line is not copied. Only a constant from model.EffortLevels is
// returned.
func launchEffort(args, env [][]byte) string {
	if v, ok := envValue(env, effortVar); ok {
		if level, accepted := envEffort(v); accepted {
			return level
		}
	}
	level := ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		if bytes.Equal(a, []byte("--")) {
			break
		}
		var v []byte
		switch {
		case bytes.Equal(a, []byte("--effort")) && i+1 < len(args):
			i++
			v = args[i]
		case bytes.HasPrefix(a, []byte("--effort=")):
			v = a[len("--effort="):]
		default:
			continue
		}
		level = flagEffort(v)
	}
	return level
}

// maxEffortWord is longer than any word Claude Code accepts for a level.
const maxEffortWord = 16

// effortWord lower-cases and trims v into buf; ok is false when it is too
// long to be a level word.
func effortWord(buf *[maxEffortWord]byte, v []byte) (w []byte, ok bool) {
	v = bytes.TrimSpace(v)
	if len(v) > maxEffortWord {
		return nil, false
	}
	n := copy(buf[:], v)
	for i := range n {
		if c := buf[i]; 'A' <= c && c <= 'Z' {
			buf[i] = c + ('a' - 'A')
		}
	}
	return buf[:n], true
}

// level returns the constant from model.EffortLevels that w names ("med" is
// medium), or "".
func level(w []byte) string {
	if string(w) == "med" {
		return "medium"
	}
	for _, l := range model.EffortLevels {
		if string(w) == l {
			return l
		}
	}
	return ""
}

// flagEffort is the level a --effort value runs at, or "".
func flagEffort(v []byte) string {
	var buf [maxEffortWord]byte
	w, ok := effortWord(&buf, v)
	if !ok {
		return ""
	}
	if string(w) == "ultracode" {
		return "xhigh"
	}
	return level(w)
}

// envEffort reads a CLAUDE_CODE_EFFORT_LEVEL value. accepted says whether
// Claude Code takes it (then it wins over the flag); level is "" for a value
// that sends no level.
func envEffort(v []byte) (lvl string, accepted bool) {
	if numeric(bytes.TrimSpace(v)) {
		return "", true // read as a number, before any length limit
	}
	var buf [maxEffortWord]byte
	w, ok := effortWord(&buf, v)
	if !ok {
		return "", false
	}
	if string(w) == "auto" || string(w) == "unset" {
		return "", true
	}
	if l := level(w); l != "" {
		return l, true
	}
	return "", false
}

// numeric says whether v starts the way a number does for Claude Code
// (JavaScript's parseInt): an optional sign, then a digit.
func numeric(v []byte) bool {
	if len(v) > 0 && (v[0] == '+' || v[0] == '-') {
		v = v[1:]
	}
	return len(v) > 0 && '0' <= v[0] && v[0] <= '9'
}

// splitNul splits NUL-terminated strings; a trailing NUL ends the last one.
func splitNul(buf []byte) [][]byte {
	buf = bytes.TrimSuffix(buf, []byte{0})
	if len(buf) == 0 {
		return nil
	}
	return bytes.Split(buf, []byte{0})
}

// procFactsFromLinux reads Linux's /proc/<pid>/cmdline and /proc/<pid>/environ.
func procFactsFromLinux(cmdline, environ []byte) procFacts {
	return pickProcFacts(splitNul(cmdline), splitNul(environ))
}

// procFactsFromProcArgs2 reads macOS's KERN_PROCARGS2 format: argc (int32),
// the executable path, NUL padding, argc arguments, then the environment,
// each NUL-terminated.
func procFactsFromProcArgs2(buf []byte) procFacts {
	if len(buf) < 4 {
		return procFacts{}
	}
	argc := int(binary.LittleEndian.Uint32(buf[:4]))
	rest := buf[4:]
	i := bytes.IndexByte(rest, 0) // end of the executable path
	if i < 0 {
		return procFacts{}
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	var args [][]byte
	for ; argc > 0; argc-- {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return procFacts{}
		}
		args = append(args, rest[:i])
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
	return pickProcFacts(args, env)
}

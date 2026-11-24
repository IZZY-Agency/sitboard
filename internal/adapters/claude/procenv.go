package claude

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/proc"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// procFacts is everything kept from a live session's process: its agterm
// session id, its tmux pane and the effort level it was launched with.
// Nothing else from the arguments or the environment is kept, logged or
// returned: they can hold prompts and secrets.
type procFacts struct {
	TermID       string         // agterm session id, "" when none
	Tmux         model.TmuxPane // zero when none
	LaunchEffort string         // one of model.EffortLevels, "" when none was given
	Headless     bool           // started with -p / --print: a headless run
}

// The environment variables read from a live session's process.
const (
	agtermVar = "AGTERM_SESSION_ID"
	// tmuxVar is "<socket>,<server pid>,<session index>"; tmuxPaneVar the
	// pane id, "%N" (tmux(1), "COMMANDS").
	tmuxVar     = "TMUX"
	tmuxPaneVar = "TMUX_PANE"
	// effortVar overrides the --effort flag and every later pick in the
	// session (Claude Code's documented order).
	effortVar = "CLAUDE_CODE_EFFORT_LEVEL"
)

var terminalIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// pickProcFacts reads the facts from a process's arguments (the first is the
// program) and its KEY=VALUE environment entries.
func pickProcFacts(args, env [][]byte) procFacts {
	return procFacts{TermID: pickAgtermID(env), Tmux: pickTmux(env), LaunchEffort: launchEffort(args, env), Headless: printFlag(args)}
}

// pickTmux returns the tmux pane from a list of KEY=VALUE entries, or zero
// when either variable is absent or not of the expected shape. Only the
// socket path is taken from TMUX; the server pid and session index are not
// kept.
func pickTmux(env [][]byte) model.TmuxPane {
	pane, ok := envValue(env, tmuxPaneVar)
	if !ok {
		return model.TmuxPane{}
	}
	v, ok := envValue(env, tmuxVar)
	if !ok {
		return model.TmuxPane{}
	}
	parts := bytes.Split(v, []byte(","))
	if len(parts) != 3 || !digits(parts[1]) || !digits(parts[2]) {
		return model.TmuxPane{}
	}
	p := model.TmuxPane{Socket: string(parts[0]), Pane: string(pane)}
	if !p.ValidPlace() {
		return model.TmuxPane{}
	}
	return p
}

func digits(b []byte) bool {
	if len(b) == 0 || len(b) > 10 {
		return false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
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

// printFlag: the arguments (the first is the program) hold -p or --print
// before any "--": claude -p, a run with no person at it. Only this yes or
// no is kept, never an argument (a prompt can be one).
func printFlag(args [][]byte) bool {
	for i := 1; i < len(args); i++ {
		a := args[i]
		if bytes.Equal(a, []byte("--")) {
			return false
		}
		if bytes.Equal(a, []byte("-p")) || bytes.Equal(a, []byte("--print")) {
			return true
		}
	}
	return false
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

// readProcFacts reads a live process's arguments and environment once and
// keeps only procFacts. Only works for the user's own processes.
func readProcFacts(pid int) procFacts {
	var f procFacts
	proc.ReadArgsEnv(pid, func(args, env [][]byte) { f = pickProcFacts(args, env) })
	return f
}

// procFactsFromLinux reads Linux's /proc/<pid>/cmdline and /proc/<pid>/environ.
func procFactsFromLinux(cmdline, environ []byte) procFacts {
	return pickProcFacts(proc.SplitNul(cmdline), proc.SplitNul(environ))
}

// procFactsFromProcArgs2 reads macOS's KERN_PROCARGS2 format.
func procFactsFromProcArgs2(buf []byte) procFacts {
	args, env, ok := proc.ParseProcArgs2(buf)
	if !ok {
		return procFacts{}
	}
	return pickProcFacts(args, env)
}

// AgtermIDOf returns the agterm session id in the environment of the user's
// own process pid, or "" when it has none or cannot be read. The tmux jump
// asks it for the tmux client it is about to move, to raise that client's
// agterm session. Like for a session's process, only the id is kept.
//
// A client that itself runs in tmux (nested tmux) has an inherited id: it
// comes from the outer tmux server's environment, captured where that server
// was started, not from the agterm session showing it now. Then it is "".
func AgtermIDOf(pid int) string {
	if pid <= 0 {
		return ""
	}
	f := readProcFacts(pid)
	if f.Tmux.ValidPlace() {
		return ""
	}
	return f.TermID
}

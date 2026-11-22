package codex

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/lsofname"
	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/proc"
	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
)

// A terminal Codex is only a client. Since 0.171 it starts a managed
// `codex app-server` daemon and that daemon, not the terminal process, holds
// the thread's writer lock and rollout, and goes on holding them after the
// terminal has quit (checked live). So the
// lock says "this thread is loaded", never "a terminal has it open". Liveness
// and the agterm terminal of a terminal thread come from the terminal
// processes themselves: the user's `codex` processes that are neither the
// daemon nor `exec` nor another subcommand.

// tuiProc is one running terminal client of the watched Codex.
type tuiProc struct {
	pid    int
	start  string // as ps prints it
	folder string // its working folder (a --cd option wins)
	term   string // its agterm session id, "" when it has none
}

// tuisFunc lists the user's terminal clients of the watched Codex. ok is
// false when the list cannot be trusted as complete: ps failed, or a client
// could not be inspected (it is running, but its folder or environment could
// not be read). The caller then treats liveness as unknown and links nothing.
type tuisFunc func(ctx context.Context) (procs []tuiProc, ok bool)

// cwdsFunc returns the working folder of each of the pids it can read.
type cwdsFunc func(ctx context.Context, pids []int) (map[int]string, bool)

// psRow is one process of the user as ps lists it: pid, owner, start time and
// the arguments split at blanks.
type psRow struct {
	pid   int
	uid   int
	start string
	args  [][]byte
}

// envInfo is what is kept of a terminal client's environment: its agterm
// session id, or that it belongs to another Codex home. Nothing else of it is
// kept.
type envInfo struct {
	term     string
	excluded bool // another Codex home: nothing else is kept
}

// nonTUI lists the subcommands of `codex --help` (0.171) that are not the
// terminal client.
var nonTUI = map[string]bool{
	"agents": true, "exec": true, "e": true, "review": true, "login": true, "logout": true,
	"mcp": true, "plugin": true, "app-server": true, "remote-control": true, "app": true,
	"completion": true, "update": true, "doctor": true, "sandbox": true, "debug": true,
	"apply": true, "a": true, "queue": true, "archive": true, "delete": true,
	"migrate-rollouts": true, "migrate-nfs-db": true, "unarchive": true, "cloud": true,
	"exec-server": true, "features": true, "help": true,
}

// valueFlags are the options that take a value in a separate argument.
var valueFlags = map[string]bool{
	"-a": true, "--ask-for-approval": true, "-s": true, "--sandbox": true, "-m": true, "--model": true,
	"-c": true, "--config": true, "-C": true, "--cd": true, "-p": true, "--profile": true,
	"-i": true, "--image": true, "--enable": true, "--disable": true, "--add-dir": true,
	"--local-provider": true, "--remote": true, "--remote-auth-token-env": true,
}

const maxFolder = 4096

// classify reads a `codex` process's arguments (the program first). It tells
// whether it is the terminal client (no subcommand, or `resume` or `fork`; a
// prompt is a positional argument too), whether it talks to a remote app
// server (--remote: not this machine's Codex), and its --cd folder, wherever
// the option stands up to `--` (0.171 accepts it after resume and fork too).
// An empty list is nothing. Only those facts are kept; the rest, which may
// hold a prompt, is read past.
func classify(args [][]byte) (tui, remote bool, cd string) {
	if len(args) == 0 {
		return false, false, ""
	}
	sub := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		if bytes.Equal(a, []byte("--")) {
			break
		}
		if len(a) > 0 && a[0] == '-' {
			name, val, hasVal := strings.Cut(string(a), "=")
			if name == "--remote" {
				remote = true
			}
			if name == "--cd" || name == "-C" {
				if !hasVal && i+1 < len(args) {
					i++
					val = string(args[i])
				}
				if len(val) <= maxFolder {
					cd = val
				}
			} else if valueFlags[name] && !hasVal {
				i++
			}
			continue
		}
		if !sub {
			sub = true
			if nonTUI[string(a)] {
				return false, false, ""
			}
		}
	}
	return true, remote, cd
}

// classifyFlat is classify for the arguments as ps prints them: joined with
// blanks, so the boundaries of an argument are lost (a folder with a space
// becomes two words, a prompt that contains "--cd /other" looks like an
// option). It must fail towards "no link", never towards a wrong folder: when
// the line cannot be split with certainty the client is unsure, and the caller
// reads nothing of it and links nothing in that scan. Unsure are: a --cd whose
// value is followed by anything but the end or an option; text after the
// options (a prompt, or what an option value with blanks spills) followed by
// an option or by a word that names a subcommand such as exec.
func classifyFlat(args [][]byte) (tui, remote bool, cd string, unsure bool) {
	if len(args) == 0 {
		return false, false, "", false
	}
	pos, allowed := 0, 0
	for i := 1; i < len(args); i++ {
		a := args[i]
		if bytes.Equal(a, []byte("--")) {
			break
		}
		if len(a) > 0 && a[0] == '-' {
			if pos > allowed {
				unsure = true // an option after a word that may be (the start of) a prompt
			}
			name, val, hasVal := strings.Cut(string(a), "=")
			if name == "--remote" {
				remote = true
			}
			if name == "--cd" || name == "-C" {
				if !hasVal && i+1 < len(args) {
					i++
					val = string(args[i])
				}
				if i+1 < len(args) && !bytes.HasPrefix(args[i+1], []byte("-")) {
					unsure = true // the value may go on: a folder with a blank
				}
				if len(val) <= maxFolder {
					cd = val
				}
			} else if valueFlags[name] && !hasVal {
				i++
			}
			continue
		}
		pos++
		switch {
		case pos == 1 && nonTUI[string(a)]:
			return false, false, "", false
		case pos == 1 && (string(a) == "resume" || string(a) == "fork"):
			allowed = 2 // an id may follow
		case pos > 1 && nonTUI[string(a)]:
			unsure = true // a subcommand hidden behind what an option value spilled
		}
	}
	if unsure {
		return false, false, "", true
	}
	return true, remote, cd, false
}

// tuiScanner finds the terminal clients. One ps listing per poll gives every
// process of the user with its owner, start time and arguments; a process is
// classified from those arguments alone, and its environment is read only
// once it is a confirmed terminal client, once per identity (pid, owner and
// start time, so a reused pid is a new one). The daemon, its updater and
// `codex exec` never have their environment read, and neither has a client
// whose command line cannot be split with certainty. Working folders are asked
// (one lsof for all new clients) only for clients whose --cd does not give
// one. What was read about new clients is kept only if a second listing shows
// the same identities.
type tuiScanner struct {
	list  func(ctx context.Context) ([]psRow, bool)
	env   func(pid int) (envInfo, bool)
	cwds  cwdsFunc
	uid   int
	stat  func(path string) bool // is it a folder
	cache map[string]*tuiEntry
}

type tuiEntry struct {
	tui, remote, unsure bool
	cd                  string
	info                envInfo
	envOK               bool
	cwd                 string
	cwdOK               bool
	fresh               bool // read in this scan, not yet confirmed by a second listing
}

func identity(r psRow) string {
	return strconv.Itoa(r.pid) + "|" + strconv.Itoa(r.uid) + "|" + r.start
}

func (s *tuiScanner) scan(ctx context.Context) ([]tuiProc, bool) {
	rows, ok := s.list(ctx)
	if !ok {
		return nil, false
	}
	if s.cache == nil {
		s.cache = map[string]*tuiEntry{}
	}
	type seen struct {
		row psRow
		e   *tuiEntry
	}
	var found []seen
	keep := map[string]bool{}
	complete := true
	for _, r := range rows {
		if r.uid != s.uid || len(r.args) == 0 || filepath.Base(string(r.args[0])) != "codex" {
			continue
		}
		key := identity(r)
		keep[key] = true
		e := s.cache[key]
		if e == nil {
			e = &tuiEntry{}
			e.tui, e.remote, e.cd, e.unsure = classifyFlat(r.args)
			s.cache[key] = e
		}
		if e.unsure {
			complete = false // maybe a client: nothing is read of it
		} else if e.tui && !e.remote {
			found = append(found, seen{r, e})
		}
	}
	for k := range s.cache {
		if !keep[k] {
			delete(s.cache, k)
		}
	}
	// The environment, once per confirmed client. A client of another Codex
	// home keeps only that fact.
	read := false
	for _, f := range found {
		if !f.e.envOK {
			if filepath.IsAbs(f.e.cd) && !s.stat(f.e.cd) {
				complete = false // not a folder: the line was split wrongly; nothing is read
				continue
			}
			info, ok := s.env(f.row.pid)
			read = true
			if !ok {
				complete = false
				continue
			}
			f.e.info, f.e.envOK, f.e.fresh = info, true, true
		}
	}
	// Working folders, for the clients that have none yet and need one.
	var need []int
	for _, f := range found {
		if f.e.envOK && !f.e.info.excluded && !f.e.cwdOK && (f.e.cd == "" || !filepath.IsAbs(f.e.cd)) {
			need = append(need, f.row.pid)
		}
	}
	if len(need) > 0 {
		read = true
		got, ok := s.cwds(ctx, need)
		if !ok {
			complete = false
		}
		for _, f := range found {
			if c, ok := got[f.row.pid]; ok && c != "" && !f.e.cwdOK {
				f.e.cwd, f.e.cwdOK, f.e.fresh = c, true, true
			}
		}
	}
	// What was read goes by pid: keep it only if the same processes are still
	// there. (A pid taken over within the same second of the start time is not
	// told apart: an accepted limit.)
	if read {
		again, ok := s.list(ctx)
		same := map[string]bool{}
		for _, r := range again {
			same[identity(r)] = true
		}
		for _, f := range found {
			if f.e.fresh && (!ok || !same[identity(f.row)]) {
				f.e.envOK, f.e.cwdOK, f.e.info, f.e.cwd = false, false, envInfo{}, ""
				complete = false
			}
		}
	}
	for _, f := range found {
		f.e.fresh = false
	}
	var procs []tuiProc
	for _, f := range found {
		e := f.e
		if !e.envOK || e.info.excluded {
			continue
		}
		folder := e.cd
		if folder == "" || !filepath.IsAbs(folder) {
			if !e.cwdOK {
				complete = false // running, folder unknown
				continue
			}
			folder = filepath.Join(e.cwd, folder) // an empty cd gives the folder itself
		}
		if e.cd != "" && !s.stat(folder) {
			complete = false // not a folder: the line was split wrongly
			continue
		}
		procs = append(procs, tuiProc{pid: f.row.pid, start: f.row.start, folder: folder, term: e.info.term})
	}
	return procs, complete
}

// readEnvInfo reads a confirmed terminal client's environment once and keeps
// the agterm id and whether its CODEX_HOME is the watched one: for a client of
// another home nothing else is kept. It checks with the exact arguments that
// the process still is a terminal client (a pid ps listed can be gone and
// reused).
func readEnvInfo(instance func(home string, set bool) bool) func(pid int) (envInfo, bool) {
	return readEnvWith(proc.ReadArgsEnv, instance)
}

func readEnvWith(read func(pid int, pick proc.Pick) bool, instance func(home string, set bool) bool) func(pid int) (envInfo, bool) {
	return func(pid int) (envInfo, bool) {
		var info envInfo
		tui := false
		ok := read(pid, func(args, env [][]byte) {
			var remote bool
			if tui, remote, _ = classify(args); !tui || remote {
				tui = false
				return
			}
			home, set := pickVar(env, "CODEX_HOME")
			if !instance(home, set) {
				info.excluded = true
				return
			}
			info.term = pickAgtermID(env)
		})
		return info, ok && tui
	}
}

func pickVar(env [][]byte, name string) (string, bool) {
	prefix := []byte(name + "=")
	for _, e := range env {
		if v, ok := bytes.CutPrefix(e, prefix); ok {
			if len(v) > maxFolder {
				return "", true
			}
			return string(v), true
		}
	}
	return "", false
}

// psList reads the user's processes with their arguments: one ps call.
func psList(run runner.Func) func(ctx context.Context) ([]psRow, bool) {
	return func(ctx context.Context) ([]psRow, bool) {
		out, err := run(ctx, "ps", "-x", "-ww", "-o", "pid=,uid=,lstart=,command=")
		if err != nil {
			return nil, false
		}
		var rows []psRow
		sc := bufio.NewScanner(bytes.NewReader(out))
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			// Fields keeps slices into the scanner's buffer, which the next Scan
			// reuses: copy the line first.
			f := bytes.Fields(append([]byte(nil), sc.Bytes()...))
			if len(f) < 8 {
				continue
			}
			pid, err1 := strconv.Atoi(string(f[0]))
			uid, err2 := strconv.Atoi(string(f[1]))
			if err1 != nil || err2 != nil || pid <= 0 {
				continue
			}
			rows = append(rows, psRow{pid: pid, uid: uid, start: string(bytes.Join(f[2:7], []byte(" "))), args: f[7:]})
		}
		if sc.Err() != nil {
			return nil, false
		}
		return rows, true
	}
}

// newTUIs is the production terminal client finder. instance says whether a
// client's CODEX_HOME (set or not) is the watched Codex home.
func newTUIs(instance func(home string, set bool) bool) tuisFunc {
	s := &tuiScanner{
		list: psList(runner.Exec(2*time.Second, "LC_ALL=C", "TZ=UTC")),
		env:  readEnvInfo(instance), cwds: platformCwds(), uid: os.Getuid(),
		stat: func(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() },
	}
	return s.scan
}

// lsofCwds asks lsof for the working folder of the pids (macOS).
func lsofCwds(run runner.Func) cwdsFunc {
	return func(ctx context.Context, pids []int) (map[int]string, bool) {
		list := make([]string, len(pids))
		for i, p := range pids {
			list[i] = strconv.Itoa(p)
		}
		out, err := run(ctx, "lsof", "-nP", "-w", "-a", "-d", "cwd", "-Fpn", "-p", strings.Join(list, ","))
		if err != nil {
			var ee *runner.ExitError
			if !errors.As(err, &ee) || ee.Code != 1 || len(bytes.TrimSpace(ee.Stderr)) > 0 {
				return nil, false
			}
			out = ee.Stdout
		}
		return parseCwds(out), true
	}
}

func parseCwds(out []byte) map[int]string {
	res := map[int]string{}
	pid := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		l := sc.Text()
		switch {
		case len(l) > 1 && l[0] == 'p':
			pid, _ = strconv.Atoi(l[1:])
		case len(l) > 1 && l[0] == 'n' && pid > 0 && len(l) <= 4*maxFolder:
			res[pid] = lsofname.Unescape(l[1:])
		}
	}
	return res
}

// terminalLive decides which held terminal threads are live and which
// terminal each one is linked to. A held lock proves nothing about a terminal
// thread (the daemon keeps it), so a thread is live only when a terminal
// client is open in its folder: with k clients in a folder, the k held threads
// there that changed most recently are live. That is a best guess when more
// threads are held than clients (a known limit: the client that quit may have
// owned the newer one). A terminal is linked only when nothing is ambiguous:
// no more held threads than clients in the folder, and either one client or
// all the clients in the same agterm terminal. The result maps each live
// thread to its terminal id, "" for no link.
func terminalLive(threads []heldThread, procs []tuiProc) map[string]string {
	byFolder := map[string][]tuiProc{}
	for _, p := range procs {
		k := folderKey(p.folder)
		byFolder[k] = append(byFolder[k], p)
	}
	tf := map[string][]heldThread{}
	for _, t := range threads {
		k := folderKey(t.folder)
		tf[k] = append(tf[k], t)
	}
	out := map[string]string{}
	for folder, ps := range byFolder {
		ts := tf[folder]
		slices.SortFunc(ts, func(a, b heldThread) int {
			if c := b.mod.Compare(a.mod); c != 0 {
				return c
			}
			return strings.Compare(a.id, b.id)
		})
		unambiguous := len(ts) <= len(ps)
		if len(ts) > len(ps) {
			ts = ts[:len(ps)]
		}
		link := ""
		if unambiguous {
			link = ps[0].term
			for _, p := range ps {
				if p.term != link {
					link = ""
				}
			}
		}
		for _, t := range ts {
			out[t.id] = link
		}
	}
	return out
}

// heldThread is a terminal thread whose lock is held.
type heldThread struct {
	id     string
	folder string
	mod    time.Time
}

// folderKey makes two spellings of one folder equal.
func folderKey(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

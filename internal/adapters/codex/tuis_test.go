package codex

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/proc"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

func argv(a ...string) [][]byte {
	out := make([][]byte, len(a))
	for i, x := range a {
		out[i] = []byte(x)
	}
	return out
}

// The argument lists are the ones seen on Codex 0.171.0 (sample prompt text).
func TestClassifyTerminalClients(t *testing.T) {
	for _, c := range []struct {
		name   string
		args   [][]byte
		tui    bool
		remote bool
		cd     string
	}{
		{"plain", argv("codex"), true, false, ""},
		{"empty", nil, false, false, ""},
		{"flags with values", argv("codex", "-a", "on-request", "-s", "read-only"), true, false, ""},
		{"resume", argv("codex", "resume", "01a202ef-d34b-7403-8d7a-d1f5802c6e72"), true, false, ""},
		{"prompt", argv("codex", "-m", "sample", "say", "hi"), true, false, ""},
		{"cd", argv("codex", "--cd", "/home/dev/projects/alpha"), true, false, "/home/dev/projects/alpha"},
		{"cd equals", argv("codex", "-C=/home/dev/projects/beta", "resume"), true, false, "/home/dev/projects/beta"},
		{"cd after resume", argv("codex", "resume", "--last", "--cd", "alpha"), true, false, "alpha"},
		{"cd after fork", argv("codex", "fork", "-C", "alpha"), true, false, "alpha"},
		{"cd after the prompt separator is a prompt", argv("codex", "--", "--cd", "alpha"), true, false, ""},
		{"remote", argv("codex", "--remote", "ws://127.0.0.1:1"), true, true, ""},
		{"daemon", argv("codex", "app-server", "--listen", "unix://", "--analytics-default-enabled", "--managed-daemon"), false, false, ""},
		{"daemon updater", argv("codex", "app-server", "daemon", "pid-update-loop"), false, false, ""},
		{"exec", argv("codex", "exec", "-s", "read-only", "say", "hi"), false, false, ""},
		{"exec after flags", argv("codex", "-a", "never", "exec", "x"), false, false, ""},
		{"a value that is a subcommand name", argv("codex", "-m", "exec"), true, false, ""},
		{"prompt after --", argv("codex", "--", "exec"), true, false, ""},
	} {
		tui, remote, cd := classify(c.args)
		if tui != c.tui || remote != c.remote || cd != c.cd {
			t.Errorf("%s: classify = %v %v %q, want %v %v %q", c.name, tui, remote, cd, c.tui, c.remote, c.cd)
		}
	}
}

func TestParseCwds(t *testing.T) {
	got := parseCwds([]byte("p100\nn/home/dev/projects/alpha\np200\nn/home/dev/projects/beta\n"))
	if len(got) != 2 || got[100] != "/home/dev/projects/alpha" || got[200] != "/home/dev/projects/beta" {
		t.Errorf("parseCwds = %v", got)
	}
	// lsof prints the bytes of a non-ASCII letter as \xHH in the C locale.
	got = parseCwds([]byte(`p1` + "\n" + `n/home/dev/caf\xc3\xa9` + "\n" + `p2` + "\n" + `n/home/dev/\xd0\xbf\xd1\x80\xd0\xb8\xd0\xb2\xd0\xb5\xd1\x82 dir\\x` + "\n"))
	if got[1] != "/home/dev/café" || got[2] != `/home/dev/привет dir\x` {
		t.Errorf("escaped names: %q %q", got[1], got[2])
	}
}

func th(id, folder string, ago time.Duration) heldThread {
	return heldThread{id: id, folder: folder, mod: fixedNow.Add(-ago)}
}

// exact: the live threads and their links, nothing else.
func exact(t *testing.T, name string, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: live = %v, want %v", name, got, want)
		return
	}
	for id, link := range want {
		if l, ok := got[id]; !ok || l != link {
			t.Errorf("%s: live = %v, want %v", name, got, want)
			return
		}
	}
}

func TestTerminalLive(t *testing.T) {
	const f1, f2 = "/home/dev/projects/alpha", "/home/dev/projects/beta"
	one := []tuiProc{{pid: 1, start: "s1", folder: f1, term: agtermID}}
	// The daemon holds the locks of a thread after /new and after /quit: three
	// loaded threads, one client. The most recently changed is live (a best
	// guess), and no terminal is linked: more threads than clients.
	exact(t, "one client, three threads", terminalLive([]heldThread{th("old", f1, 3*time.Hour), th("new", f1, time.Minute), th("mid", f1, time.Hour)}, one),
		map[string]string{"new": ""})
	// One client, one thread: certain.
	exact(t, "one and one", terminalLive([]heldThread{th("a", f1, time.Minute)}, one), map[string]string{"a": agtermID})
	// No client in the folder: nothing is live.
	exact(t, "other folder", terminalLive([]heldThread{th("a", f2, time.Minute)}, one), map[string]string{})
	exact(t, "no client", terminalLive([]heldThread{th("a", f1, time.Minute)}, nil), map[string]string{})
	// Two clients in two terminals, two threads: live, no link.
	two := []tuiProc{{pid: 1, start: "s1", folder: f1, term: agtermID}, {pid: 2, start: "s2", folder: f1, term: termID}}
	exact(t, "two terminals", terminalLive([]heldThread{th("a", f1, time.Minute), th("b", f1, 2*time.Minute), th("c", f1, time.Hour)}, two),
		map[string]string{"a": "", "b": ""})
	// Two clients in two terminals, two threads: live, and no link (which
	// terminal holds which thread is not known).
	exact(t, "two terminals, two threads", terminalLive([]heldThread{th("a", f1, time.Minute), th("b", f1, 2*time.Minute)}, two),
		map[string]string{"a": "", "b": ""})
	// Two clients in the same terminal, two threads: the terminal is certain.
	same := []tuiProc{{pid: 1, start: "s1", folder: f1, term: agtermID}, {pid: 2, start: "s2", folder: f1, term: agtermID}}
	exact(t, "one terminal", terminalLive([]heldThread{th("a", f1, time.Minute), th("b", f1, 2*time.Minute)}, same),
		map[string]string{"a": agtermID, "b": agtermID})
	// ... but with a third held thread (a client quit) it is a guess again.
	exact(t, "one terminal, a thread too many", terminalLive([]heldThread{th("a", f1, time.Minute), th("b", f1, 2*time.Minute), th("c", f1, 3*time.Minute)}, same),
		map[string]string{"a": "", "b": ""})
	// A client outside agterm and one inside: not certain.
	mixed := []tuiProc{{pid: 1, start: "s1", folder: f1, term: agtermID}, {pid: 2, start: "s2", folder: f1}}
	exact(t, "mixed", terminalLive([]heldThread{th("a", f1, time.Minute), th("b", f1, 2*time.Minute)}, mixed),
		map[string]string{"a": "", "b": ""})
	// Folders are matched through links (a temporary folder on macOS).
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	exact(t, "linked folder", terminalLive([]heldThread{th("a", dir, time.Minute)}, []tuiProc{{pid: 1, start: "s", folder: link, term: agtermID}}),
		map[string]string{"a": agtermID})
}

func yes(string) bool { return true }

const t0 = "Sun Nov 22 12:00:00 2026"

// scanner builds a tuiScanner over fixed rows; lists counts the ps listings.
func scanner(rows *[]psRow, lists *int) *tuiScanner {
	return &tuiScanner{
		uid:  501,
		stat: yes,
		list: func(context.Context) ([]psRow, bool) { *lists++; return *rows, true },
		env:  func(int) (envInfo, bool) { return envInfo{term: agtermID}, true },
		cwds: func(_ context.Context, pids []int) (map[int]string, bool) {
			m := map[int]string{}
			for _, p := range pids {
				m[p] = "/home/dev/alpha"
			}
			return m, true
		},
	}
}

// The production discovery, through injected readers: what is read, and what
// never is.
func TestScannerReadsOnlyConfirmedClients(t *testing.T) {
	const self = 501
	rows := []psRow{
		{pid: 10, uid: self, start: t0, args: argv("/opt/codex/bin/codex", "-a", "on-request")},                          // terminal client
		{pid: 11, uid: self, start: t0, args: argv("/x/codex", "app-server", "--listen", "unix://", "--managed-daemon")}, // daemon
		{pid: 12, uid: self, start: t0, args: argv("/x/codex", "app-server", "daemon", "pid-update-loop")},               // updater
		{pid: 13, uid: self, start: t0, args: argv("/x/codex", "exec", "say", "hi")},                                     // exec
		{pid: 14, uid: self + 1, start: t0, args: argv("/x/codex")},                                                      // another user's
		{pid: 15, uid: self, start: t0, args: argv("/x/codex", "--remote", "ws://127.0.0.1:1")},                          // remote
		{pid: 16, uid: self, start: t0, args: argv("/x/other")},                                                          // not codex
		{pid: 17, uid: self, start: t0, args: argv("/x/codex", "-C", "sub")},                                             // relative --cd, other home
		{pid: 18, uid: self, start: t0, args: argv("/x/codex", "--cd", "/home/dev/abs")},                                 // absolute --cd
	}
	var envRead, cwdAsked []int
	lists := 0
	s := scanner(&rows, &lists)
	s.env = func(pid int) (envInfo, bool) {
		envRead = append(envRead, pid)
		if pid == 17 {
			return envInfo{excluded: true}, true
		}
		return envInfo{term: agtermID}, true
	}
	s.cwds = func(_ context.Context, pids []int) (map[int]string, bool) {
		cwdAsked = append(cwdAsked, pids...)
		return map[int]string{10: "/home/dev/alpha", 17: "/home/dev/rel"}, true
	}
	procs, ok := s.scan(context.Background())
	if !ok || len(procs) != 2 {
		t.Fatalf("scan = %v %v", procs, ok)
	}
	if procs[0].pid != 10 || procs[0].folder != "/home/dev/alpha" || procs[0].term != agtermID {
		t.Errorf("client 10 = %+v", procs[0])
	}
	if procs[1].pid != 18 || procs[1].folder != "/home/dev/abs" {
		t.Errorf("client 18 = %+v", procs[1])
	}
	// The environment of the daemon, the updater, exec, another user's process
	// and a remote client is never read; a client of another home and a client
	// with an absolute --cd are not asked for a working folder.
	if want := []int{10, 17, 18}; !slices.Equal(envRead, want) {
		t.Errorf("environments read: %v, want %v", envRead, want)
	}
	if want := []int{10}; !slices.Equal(cwdAsked, want) {
		t.Errorf("working folders asked: %v, want %v", cwdAsked, want)
	}
	// Reading made a second listing (the identity check), nothing more.
	if lists != 2 {
		t.Errorf("listings in a scan that read: %d, want 2", lists)
	}
	// The next poll reads nothing more: one ps listing, the rest from the
	// identity cache.
	envRead, cwdAsked, lists = nil, nil, 0
	if procs, ok := s.scan(context.Background()); !ok || len(procs) != 2 || len(envRead) != 0 || len(cwdAsked) != 0 || lists != 1 {
		t.Errorf("second scan: %v %v, environments %v, folders %v, listings %d", procs, ok, envRead, cwdAsked, lists)
	}
	// A pid that another process took over (a new start time) is a new
	// identity: read again.
	rows[0].start = "Sun Nov 22 13:00:00 2026"
	s.scan(context.Background())
	if !slices.Equal(envRead, []int{10}) {
		t.Errorf("reused pid: environments read %v, want [10]", envRead)
	}
}

// A client that cannot be inspected makes the scan incomplete; links and
// liveness are not guessed from the rest. A relative --cd is joined to the
// client's working folder.
func TestScannerIncompleteAndRelativeCd(t *testing.T) {
	rows := []psRow{{pid: 17, uid: 501, start: t0, args: argv("/x/codex", "resume", "-C", "sub")}}
	lists := 0
	s := scanner(&rows, &lists)
	cwd := map[int]string{}
	envOK := true
	s.env = func(int) (envInfo, bool) { return envInfo{term: agtermID}, envOK }
	s.cwds = func(context.Context, []int) (map[int]string, bool) { return cwd, true }
	if procs, ok := s.scan(context.Background()); ok || len(procs) != 0 {
		t.Errorf("folder unreadable: %v %v, want incomplete", procs, ok)
	}
	cwd[17] = "/home/dev/rel"
	procs, ok := s.scan(context.Background())
	if !ok || len(procs) != 1 || procs[0].folder != "/home/dev/rel/sub" {
		t.Errorf("relative --cd: %v %v", procs, ok)
	}
	rows = append(rows, psRow{pid: 18, uid: 501, start: t0, args: argv("/x/codex")})
	envOK = false
	if _, ok := s.scan(context.Background()); ok {
		t.Error("an unreadable environment of a client was taken as complete")
	}
	rows = []psRow{{pid: 19, uid: 501, start: t0, args: nil}}
	if procs, ok := s.scan(context.Background()); !ok || len(procs) != 0 {
		t.Errorf("empty argv: %v %v, want nothing", procs, ok)
	}
	// A --cd that is no folder (the line was split wrongly): incomplete.
	rows = []psRow{{pid: 20, uid: 501, start: t0, args: argv("/x/codex", "--cd", "/home/dev/my")}}
	envOK = true
	s.stat = func(string) bool { return false }
	if procs, ok := s.scan(context.Background()); ok || len(procs) != 0 {
		t.Errorf("--cd that is no folder: %v %v", procs, ok)
	}
}

// ps joins the arguments with blanks: what cannot be split with certainty is
// unsure, and an unsure client is never read.
func TestClassifyFlat(t *testing.T) {
	for _, c := range []struct {
		name   string
		args   [][]byte
		tui    bool
		cd     string
		unsure bool
	}{
		{"plain", argv("codex"), true, "", false},
		{"options", argv("codex", "-a", "on-request", "-s", "read-only"), true, "", false},
		{"cd at the end", argv("codex", "--cd", "/home/dev/alpha"), true, "/home/dev/alpha", false},
		{"cd then an option", argv("codex", "-C", "/home/dev/alpha", "-a", "never"), true, "/home/dev/alpha", false},
		{"cd with a blank", argv("codex", "--cd", "/home/dev/my", "project"), false, "", true},
		{"cd equals with a blank", argv("codex", "--cd=/home/dev/my", "project", "-a", "never"), false, "", true},
		{"prompt that looks like an option", argv("codex", "fix", "the", "--cd", "/other"), false, "", true},
		{"prompt alone", argv("codex", "say", "hi"), true, "", false},
		{"value with a blank before exec", argv("codex", "-c", "k=a", "b", "exec", "x"), false, "", true},
		{"exec", argv("codex", "exec", "say", "hi"), false, "", false},
		{"daemon", argv("codex", "app-server", "--managed-daemon"), false, "", false},
		{"resume with an id and an option", argv("codex", "resume", "01a2", "--cd", "alpha"), true, "alpha", false},
		{"resume, a prompt, an option", argv("codex", "resume", "01a2", "fix", "--cd", "/other"), false, "", true},
		{"one-word prompt prefix", argv("codex", "fix", "--cd", "/other"), false, "", true},
		{"folder with a blank then a dash (the folder check rejects it)", argv("codex", "--cd", "/home/dev/my", "-x"), true, "/home/dev/my", false},
		{"resume", argv("codex", "resume"), true, "", false},
		{"cd simple", argv("codex", "--cd", "/simple/path"), true, "/simple/path", false},
		{"empty", nil, false, "", false},
	} {
		tui, _, cd, unsure := classifyFlat(c.args)
		if tui != c.tui || cd != c.cd || unsure != c.unsure {
			t.Errorf("%s: classifyFlat = %v %q %v, want %v %q %v", c.name, tui, cd, unsure, c.tui, c.cd, c.unsure)
		}
	}
	// An unsure client's environment is never read, and the scan is incomplete.
	rows := []psRow{
		{pid: 30, uid: 501, start: t0, args: argv("/x/codex", "-c", "k=a", "b", "exec", "x")},
		{pid: 31, uid: 501, start: t0, args: argv("/x/codex")},
	}
	lists := 0
	s := scanner(&rows, &lists)
	var envRead []int
	s.env = func(pid int) (envInfo, bool) { envRead = append(envRead, pid); return envInfo{term: agtermID}, true }
	procs, ok := s.scan(context.Background())
	if ok || len(procs) != 1 || !slices.Equal(envRead, []int{31}) {
		t.Errorf("unsure client: %v %v, environments %v", procs, ok, envRead)
	}
}

// Between the listing and the reading a process can be replaced (its pid
// reused): what was read is dropped, the scan is incomplete, and the next
// poll reads again.
func TestScannerProcessReplacedWhileReading(t *testing.T) {
	rows := []psRow{{pid: 40, uid: 501, start: t0, args: argv("/x/codex")}}
	lists := 0
	s := scanner(&rows, &lists)
	reads := 0
	s.env = func(int) (envInfo, bool) {
		reads++
		if reads == 1 { // replaced after the first listing
			rows = []psRow{{pid: 40, uid: 501, start: "Sun Nov 22 12:00:09 2026", args: argv("/x/codex")}}
		}
		return envInfo{term: agtermID}, true
	}
	if procs, ok := s.scan(context.Background()); ok || len(procs) != 0 {
		t.Errorf("replaced: %v %v, want nothing and incomplete", procs, ok)
	}
	procs, ok := s.scan(context.Background())
	if !ok || len(procs) != 1 || reads != 2 {
		t.Errorf("next poll: %v %v, reads %d", procs, ok, reads)
	}
	// A second listing that fails drops what was read too.
	rows = []psRow{{pid: 41, uid: 501, start: t0, args: argv("/x/codex")}}
	calls := 0
	s.list = func(context.Context) ([]psRow, bool) { calls++; return rows, calls != 2 }
	s.env = func(int) (envInfo, bool) { return envInfo{term: agtermID}, true }
	if procs, ok := s.scan(context.Background()); ok || len(procs) != 0 {
		t.Errorf("second listing failed: %v %v", procs, ok)
	}
}

// The production parser, through a fake command runner: one ps call per poll,
// with the arguments asked for, padded start times and commands with blanks,
// and output big enough that the scanner reuses its buffer.
func TestPSListThroughRunner(t *testing.T) {
	var calls [][]string
	var out bytes.Buffer
	out.WriteString("  101   501 Sun Nov 22 09:05:01 2026 /opt/codex/bin/codex --cd /home/dev/alpha\n")
	out.WriteString("  102   501 Sun Nov 22 09:05:02 2026 /opt/codex/bin/codex app-server --managed-daemon\n")
	for i := 0; i < 3000; i++ { // enough rows to refill the scanner buffer many times
		out.WriteString("  900   502 Sun Nov 22 09:05:03 2026 /usr/bin/some-long-running-tool --with --many --arguments --to --make --the --line --long\n")
	}
	out.WriteString("  103   501 Sun Nov 22 09:05:04 2026 /opt/codex/bin/codex -a never\n")
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return out.Bytes(), nil
	}
	rows, ok := psList(run)(context.Background())
	if !ok || len(calls) != 1 || calls[0][0] != "ps" || !slices.Contains(calls[0], "pid=,uid=,lstart=,command=") {
		t.Fatalf("ps calls: %v ok %v", calls, ok)
	}
	byPid := map[int]psRow{}
	for _, r := range rows {
		if r.pid != 900 {
			byPid[r.pid] = r
		}
	}
	if len(rows) != 3003 || len(byPid) != 3 {
		t.Fatalf("rows %d, codex rows %d", len(rows), len(byPid))
	}
	r := byPid[101]
	if r.uid != 501 || r.start != "Sun Nov 22 09:05:01 2026" || len(r.args) != 3 || string(r.args[2]) != "/home/dev/alpha" {
		t.Errorf("row 101 = %+v", r)
	}
	if r := byPid[103]; len(r.args) != 3 || string(r.args[0]) != "/opt/codex/bin/codex" || string(r.args[2]) != "never" {
		t.Errorf("row 103 = %+v (a later row overwrote it?)", r)
	}
	if r := byPid[102]; len(r.args) != 3 || string(r.args[1]) != "app-server" {
		t.Errorf("row 102 = %+v", r)
	}
}

func TestSameCodexHome(t *testing.T) {
	same := sameCodexHome("/home/dev/.codex", "/home/dev/.codex")
	if !same("", false) || !same("/home/dev/.codex", true) || same("/other", true) {
		t.Error("default watched home")
	}
	custom := sameCodexHome("/srv/codex", "/home/dev/.codex")
	if custom("", false) || !custom("/srv/codex", true) {
		t.Error("custom watched home")
	}
}

const idSecond = "99999999-9999-4999-8999-999999999999"

// addTerminal adds a second terminal thread: the worker fixture under another id.
func (e *env) addTerminal(id string, mod time.Time) {
	e.t.Helper()
	e.addBytes(id, bytes.ReplaceAll(fixture(e.t, "worker.jsonl"), []byte(idWorker), []byte(id)), mod)
}

// What the daemon does after /quit: the lock stays held (by the daemon, a
// process that is not a terminal), but no terminal client is left. The
// session ends. Shapes checked live on Codex 0.171.0 (#111).
func TestLockHeldByDaemonAfterTerminalQuit(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.lock(idWorker, 37030) // the daemon's pid, not the client's
	e.rescan()
	if f, ok := e.last(idWorker); !ok || f.Live != model.Of(true) || f.TerminalID != agtermID {
		t.Fatalf("running terminal: %v %+v", ok, f)
	}
	e.tuis = nil // /quit: the client is gone, the daemon keeps the lock
	e.rescan()
	if f, ok := e.last(idWorker); !ok || f.Live != model.Of(false) || !f.Ended || f.TerminalID != "" {
		t.Errorf("after quit: %v live=%v ended=%v terminal=%q", ok, f.Live, f.Ended, f.TerminalID)
	}
}

// /new in a running terminal: both threads stay locked by the daemon; only
// the one that changed last is live.
func TestNewThreadEndsTheOldOne(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.lock(idWorker, 37030)
	e.rescan()
	e.addTerminal(idSecond, fixedNow.Add(-time.Minute))
	e.lock(idSecond, 37030)
	e.rescan()
	// Two threads held, one client: the newer is live, but which terminal it is
	// cannot be told, so no link.
	if f, _ := e.last(idSecond); f.Live != model.Of(true) || f.TerminalID != "" {
		t.Errorf("new thread: live=%v terminal=%q", f.Live, f.TerminalID)
	}
	if f, _ := e.last(idWorker); f.Live != model.Of(false) || !f.Ended {
		t.Errorf("old thread: live=%v ended=%v", f.Live, f.Ended)
	}
}

// A held lock with no terminal client in the thread's folder is not a live
// session; one never seen live is not shown at all.
func TestHeldLockWithoutClientInFolderIsNotLive(t *testing.T) {
	e := newEnv(t)
	e.tuis = []tuiProc{{pid: 4242, start: "s", folder: "/home/dev/projects/other", term: agtermID}}
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.lock(idWorker, 37030)
	e.rescan()
	if _, ok := e.last(idWorker); ok {
		t.Error("a thread whose folder has no terminal client was shown")
	}
}

// If the terminal clients cannot be listed, liveness of a terminal thread is
// unknown (shown as recently active), and a session already seen live keeps
// what it was.
func TestTerminalLookupFailure(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-5*time.Minute))
	e.lock(idWorker, 37030)
	e.tuiFail = true
	e.rescan()
	if f, ok := e.last(idWorker); !ok || f.Live.OK {
		t.Errorf("lookup failed: %v live=%v, want shown with liveness unknown", ok, f.Live)
	}
	e.tuiFail = false
	e.rescan()
	if f, _ := e.last(idWorker); f.Live != model.Of(true) {
		t.Errorf("lookup works: live=%v", f.Live)
	}
	e.tuiFail = true
	e.rescan()
	if f, _ := e.last(idWorker); f.Live != model.Of(true) {
		t.Errorf("a failed lookup changed a live session: live=%v", f.Live)
	}
}

// /resume of an older thread appends to its original rollout, which lies in
// the folder of its first day (checked live: no new file is written). It is
// found through its held lock and shown, though its folder is outside the
// scanned days.
func TestResumedThreadInOldDayFolder(t *testing.T) {
	e := newEnv(t)
	e.addAt(20, "worker.jsonl", idWorker, fixedNow.Add(-time.Minute)) // appended a minute ago
	e.lock(idWorker, 37030)
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || f.Live != model.Of(true) || f.TerminalID != agtermID {
		t.Fatalf("resumed old thread: %v %+v", ok, f)
	}
}

// Which threads are terminal sessions: the originator the terminal client
// writes now, and source "cli" for older versions. The desktop app and Claude
// Code's plugin (shapes from this machine's rollouts) are not.
func TestWhichThreadsAreTerminalSessions(t *testing.T) {
	line := func(orig, src string) []byte {
		return []byte(`{"type":"session_meta","payload":{"id":"` + idSecond + `","timestamp":"2026-11-20T11:00:00.000Z","cwd":"/home/dev/projects/alpha","originator":"` + orig + `","cli_version":"0.171.0","source":` + src + `}}` + "\n")
	}
	for _, c := range []struct {
		orig, src string
		terminal  bool
	}{
		{"codex-tui", `"vscode"`, true},
		{"codex_cli_rs", `"cli"`, true},
		{"Claude Code", `"vscode"`, false},
		{"Codex Desktop", `"vscode"`, false},
		{"codex_work_desktop", `"vscode"`, false},
		{"codex_exec", `"exec"`, false},
		{"codex-tui", `{"subagent":{"thread_spawn":{"depth":1}}}`, false},
	} {
		m, ok := parseMeta(line(c.orig, c.src), fixedNow)
		if !ok || m.terminal() != c.terminal {
			t.Errorf("%s/%s: terminal=%v, want %v", c.orig, c.src, m.terminal(), c.terminal)
		}
	}
	m, _ := parseMeta(line("codex_exec", `"exec"`), fixedNow)
	if !m.showable() {
		t.Error("codex exec is not shown")
	}
	m, _ = parseMeta(line("Claude Code", `"vscode"`), fixedNow)
	if m.showable() {
		t.Error("a plugin thread is shown")
	}
}

// Two terminals in one folder, both threads locked by the daemon; the newer
// one's terminal quits. The survivor's thread looks ended (a known limit) but
// no thread gets a Jump link: the closed one must not point at the survivor.
func TestNewerClientQuitsWhileBothLocksStay(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.addTerminal(idSecond, fixedNow.Add(-5*time.Minute))
	e.lock(idWorker, 37030)
	e.lock(idSecond, 37030)
	e.tuis = []tuiProc{
		{pid: 4242, start: "s1", folder: "/home/dev/projects/alpha", term: agtermID},
		{pid: 4343, start: "s2", folder: "/home/dev/projects/alpha", term: termID},
	}
	e.rescan()
	e.tuis = e.tuis[:1] // the newer thread's terminal quits
	e.rescan()
	for _, id := range []string{idWorker, idSecond} {
		if f, ok := e.last(id); ok && f.TerminalID != "" {
			t.Errorf("thread %s linked to terminal %q with two threads held and one client", id[:2], f.TerminalID)
		}
	}
}

// If the clients cannot be listed completely, a link that was shown is
// cleared and liveness is left as it was.
func TestIncompleteClientScanClearsLinks(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-5*time.Minute))
	e.lock(idWorker, 37030)
	e.rescan()
	if f, _ := e.last(idWorker); f.TerminalID != agtermID {
		t.Fatalf("link before: %q", f.TerminalID)
	}
	e.tuiFail = true
	e.rescan()
	if f, _ := e.last(idWorker); f.TerminalID != "" || f.Live != model.Of(true) {
		t.Errorf("incomplete scan: terminal %q live %v", f.TerminalID, f.Live)
	}
}

// A linked card whose lock lookup then fails keeps its liveness but loses the
// link.
func TestLockLookupFailureClearsLinks(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-5*time.Minute))
	e.lock(idWorker, 37030)
	e.rescan()
	if f, _ := e.last(idWorker); f.TerminalID != agtermID {
		t.Fatalf("link before: %q", f.TerminalID)
	}
	e.touch(filepath.Join(e.root, "thread-writer-locks", idWorker+".lock"), fixedNow.Add(time.Hour))
	e.fail = true
	e.rescan()
	if f, _ := e.last(idWorker); f.TerminalID != "" || f.Live != model.Of(true) {
		t.Errorf("lock lookup failed: terminal %q live %v", f.TerminalID, f.Live)
	}
}

var _ = context.Background

// An absolute --cd that is no folder is rejected before the environment is
// read; an existing simple path is read.
func TestAbsoluteCdCheckedBeforeEnvRead(t *testing.T) {
	rows := []psRow{
		{pid: 50, uid: 501, start: t0, args: argv("/x/codex", "--cd", "/home/dev/my")},
		{pid: 51, uid: 501, start: t0, args: argv("/x/codex", "--cd", "/home/dev/alpha")},
		{pid: 52, uid: 501, start: t0, args: argv("/x/codex", "--cd", "/home/dev/my", "-x")}, // "/home/dev/my -x"
	}
	lists := 0
	s := scanner(&rows, &lists)
	s.stat = func(p string) bool { return p == "/home/dev/alpha" }
	var envRead []int
	s.env = func(pid int) (envInfo, bool) { envRead = append(envRead, pid); return envInfo{term: agtermID}, true }
	procs, ok := s.scan(context.Background())
	if ok || len(procs) != 1 || procs[0].pid != 51 || !slices.Equal(envRead, []int{51}) {
		t.Errorf("scan = %v %v, environments read %v, want [51] only", procs, ok, envRead)
	}
}

// The production environment filter: a client of another CODEX_HOME gives
// back only "excluded", though its environment holds a valid terminal id.
func TestEnvFilterKeepsOnlyExcluded(t *testing.T) {
	read := func(env ...string) func(int, proc.Pick) bool {
		return func(_ int, pick proc.Pick) bool {
			var e [][]byte
			for _, v := range env {
				e = append(e, []byte(v))
			}
			pick(argv("codex"), e)
			return true
		}
	}
	other := readEnvWith(read("CODEX_HOME=/elsewhere", "AGTERM_SESSION_ID="+agtermID), sameCodexHome("/home/dev/.codex", "/home/dev/.codex"))
	if info, ok := other(1); !ok || info != (envInfo{excluded: true}) {
		t.Errorf("other home: %+v %v", info, ok)
	}
	mine := readEnvWith(read("AGTERM_SESSION_ID="+agtermID), sameCodexHome("/home/dev/.codex", "/home/dev/.codex"))
	if info, ok := mine(1); !ok || info.excluded || info.term != agtermID {
		t.Errorf("watched home: %+v %v", info, ok)
	}
}

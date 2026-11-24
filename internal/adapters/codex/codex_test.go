package codex

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/testguard"
)

// The ids of the fixtures in testdata/fixtures/codex/0.171.0.
const (
	idWorker    = "11111111-1111-4111-8111-111111111111" // terminal session
	idChild     = "22222222-2222-4222-8222-222222222222" // spawned by idWorker
	idExec      = "33333333-3333-4333-8333-333333333333" // codex exec
	idDesktop   = "44444444-4444-4444-8444-444444444444" // desktop app thread
	idGrand     = "88888888-8888-4888-8888-888888888888" // spawned by idChild
	idNoRollout = "66666666-6666-4666-8666-666666666666" // a lock with no rollout
	termID      = "55555555-5555-4555-8555-555555555555" // printed by a spawn
	agtermID    = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA" // in the worker's environment
)

var fixedNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func TestTestsUseNewAdapter(t *testing.T) {
	testguard.OnlyIn(t, []string{"New"}, []string{"newAdapter", "TestNewUsesCodexHome"})
}

// newAdapter is the only way tests make an adapter: no real lsof, ps or
// process environment is ever reached.
func newAdapter(t *testing.T, opts Options) *adapter {
	t.Helper()
	a := New(opts).(*adapter)
	a.holders = func(context.Context, []string) (map[string]int, bool) { return map[string]int{}, true }
	a.starts = func(_ context.Context, pids []int) (map[int]string, bool) {
		m := map[int]string{}
		for _, p := range pids {
			m[p] = "Fri Nov 20 10:00:00 2026"
		}
		return m, true
	}
	a.tuis = func(context.Context) ([]tuiProc, bool) { return nil, true }
	return a
}

// env is a CODEX_HOME in a temporary folder.
type env struct {
	t       *testing.T
	root    string
	a       *adapter
	r       *registry
	facts   []model.Fact
	held    map[string]int // thread id → pid holding its lock
	fail    bool           // the holder lookup fails
	tuis    []tuiProc      // the terminal clients running
	tuiFail bool           // the terminal client lookup fails
	paths   map[string]string
}

func newEnv(t *testing.T) *env {
	e := &env{t: t, root: t.TempDir(), held: map[string]int{}, paths: map[string]string{}}
	e.a = newAdapter(t, Options{CodexHome: e.root, Now: func() time.Time { return fixedNow }})
	e.a.holders = func(_ context.Context, paths []string) (map[string]int, bool) {
		if e.fail {
			return nil, false
		}
		out := map[string]int{}
		for _, p := range paths {
			if pid, ok := e.held[strings.TrimSuffix(filepath.Base(p), ".lock")]; ok {
				out[p] = pid
			}
		}
		return out, true
	}
	// By default one terminal client runs, in the worker fixture's folder, in
	// an agterm terminal.
	e.tuis = []tuiProc{{pid: 4242, start: "Fri Nov 20 10:00:00 2026", folder: "/home/dev/projects/alpha", term: agtermID}}
	e.a.tuis = func(context.Context) ([]tuiProc, bool) { return e.tuis, !e.tuiFail }
	e.r = &registry{a: e.a, emit: func(f model.Fact) { e.facts = append(e.facts, f) }, sessions: map[string]*sess{}, children: map[string]int{}, sent: map[string]model.SessionFact{}, located: map[string]string{}, notFound: map[string]time.Time{}, names: newNames(filepath.Join(e.root, "session_index.jsonl"))}
	return e
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fixtures", "codex", "0.171.0", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// add puts a fixture in today's day folder as thread id's rollout, changed at mod.
func (e *env) add(name, id string, mod time.Time) {
	e.t.Helper()
	d := fixedNow.Local()
	dir := filepath.Join(e.root, "sessions", d.Format("2006"), d.Format("01"), d.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-2026-11-20T11-00-00-"+id+".jsonl")
	if err := os.WriteFile(p, fixture(e.t, name), 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.touch(p, mod)
	e.paths[id] = p
}

// addBytes is add with the rollout's text given.
func (e *env) addBytes(id string, content []byte, mod time.Time) {
	e.t.Helper()
	d := fixedNow.Local()
	dir := filepath.Join(e.root, "sessions", d.Format("2006"), d.Format("01"), d.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-2026-11-20T11-00-00-"+id+".jsonl")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.touch(p, mod)
	e.paths[id] = p
}

func (e *env) touch(p string, mod time.Time) {
	e.t.Helper()
	if err := os.Chtimes(p, mod, mod); err != nil {
		e.t.Fatal(err)
	}
}

// lock marks a thread live: its lock file exists and pid holds it.
func (e *env) lock(id string, pid int) {
	e.t.Helper()
	dir := filepath.Join(e.root, "thread-writer-locks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".lock"), nil, 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.held[id] = pid
}

// unlock: the process stopped writing the thread and Codex removed the file.
func (e *env) unlock(id string) {
	e.t.Helper()
	_ = os.Remove(filepath.Join(e.root, "thread-writer-locks", id+".lock"))
	delete(e.held, id)
}

func (e *env) appendLines(id string, lines ...string) {
	e.t.Helper()
	f, err := os.OpenFile(e.paths[id], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			e.t.Fatal(err)
		}
	}
	_ = f.Close()
	e.touch(e.paths[id], fixedNow.Add(-time.Minute))
}

func (e *env) rescan() { e.r.rescan(context.Background()) }

// flatten unfolds the batches among facts, in order.
func flatten(facts []model.Fact) []model.Fact {
	var out []model.Fact
	for _, f := range facts {
		if b, ok := f.(model.Batch); ok {
			out = append(out, flatten(b.Facts)...)
		} else {
			out = append(out, f)
		}
	}
	return out
}

// last returns the last fact sent for id, and whether it was withdrawn after.
func (e *env) last(id string) (f model.SessionFact, sent bool) {
	for _, x := range flatten(e.facts) {
		switch x := x.(type) {
		case model.SessionFact:
			if x.Key.ID == id {
				f, sent = x, true
			}
		case model.SessionGone:
			if x.Key.ID == id {
				f, sent = model.SessionFact{}, false
			}
		}
	}
	return f, sent
}

func TestTerminalSessionFromRollout(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.add("child.jsonl", idChild, fixedNow.Add(-56*time.Minute))
	if err := os.WriteFile(filepath.Join(e.root, "session_index.jsonl"), fixture(t, "session_index.jsonl"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.lock(idWorker, 4242)
	e.lock(idChild, 4242)
	e.rescan()

	f, ok := e.last(idWorker)
	if !ok {
		t.Fatal("no fact for the terminal session")
	}
	if f.Key.Tool != model.ToolCodex || f.ToolVersion != "0.171.0" || f.Name != "Placeholder title" {
		t.Errorf("key/version/name = %v %q %q", f.Key, f.ToolVersion, f.Name)
	}
	if f.Model != model.Of("gpt-6.2-sol") || f.Effort != model.Of("ultra") {
		t.Errorf("model/effort = %v %v", f.Model, f.Effort)
	}
	if f.Status != model.Of(model.StatusFinished) || len(f.TurnEnds) != 1 || !f.TurnEnds[0].Equal(time.Date(2026, 11, 20, 11, 5, 0, 0, time.UTC)) {
		t.Errorf("status/turns = %v %v", f.Status, f.TurnEnds)
	}
	if f.Live != model.Of(true) || f.Ended || f.TerminalID != agtermID {
		t.Errorf("live/ended/terminal = %v %v %q", f.Live, f.Ended, f.TerminalID)
	}
	if f.Folder != "/home/dev/projects/alpha" || !f.StartedAt.Equal(time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("folder/started = %q %v", f.Folder, f.StartedAt)
	}
	// gh pr create's printed pull request counts; an echoed "gh pr create" does not.
	if !slices.Equal(f.CreatedPRs, []model.PRRef{{Repo: model.GitHubRepo("example-org/example-repo"), Number: 301}}) {
		t.Errorf("CreatedPRs = %v", f.CreatedPRs)
	}
	if !slices.Equal(f.SpawnedNames, []string{"worker-b"}) || !slices.Equal(f.SpawnedTerminalIDs, []string{termID}) {
		t.Errorf("spawned = %v %v", f.SpawnedNames, f.SpawnedTerminalIDs)
	}
	// The spawned thread is an in-process subagent: a count, never a card,
	// never a parent link.
	if f.Subagents != 1 || f.Parent.OK {
		t.Errorf("subagents/parent = %d %v", f.Subagents, f.Parent)
	}
	if _, ok := e.last(idChild); ok {
		t.Error("the spawned thread became a session")
	}
	if f.Headless {
		t.Error("a terminal session is headless")
	}
}

// Only the terminal client and codex exec are shown; the desktop app's
// threads are not, even while live.
func TestOnlyTerminalAndExecAreShown(t *testing.T) {
	e := newEnv(t)
	e.add("exec.jsonl", idExec, fixedNow.Add(-50*time.Minute))
	e.add("desktop.jsonl", idDesktop, fixedNow.Add(-40*time.Minute))
	e.lock(idExec, 5000)
	e.lock(idDesktop, 5001)
	e.rescan()
	if f, ok := e.last(idExec); !ok || !f.Headless || f.Effort != model.Of("high") {
		t.Errorf("exec = %v %+v", ok, f)
	}
	if _, ok := e.last(idDesktop); ok {
		t.Error("a desktop app thread was shown")
	}
}

// A codex exec started in another agent's terminal inherits its agterm
// session id; it gets no terminal. The desktop app's thread is not shown.
func TestOnlyTerminalSessionsGetATerminal(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.add("exec.jsonl", idExec, fixedNow.Add(-50*time.Minute))
	e.add("desktop.jsonl", idDesktop, fixedNow.Add(-40*time.Minute))
	e.lock(idWorker, 4242)
	e.lock(idExec, 4242)
	e.lock(idDesktop, 4242)
	e.rescan()
	if f, _ := e.last(idWorker); f.TerminalID != agtermID {
		t.Errorf("terminal session: terminal %q", f.TerminalID)
	}
	if f, ok := e.last(idExec); !ok || f.TerminalID != "" || !f.Headless {
		t.Errorf("exec: %v %+v", ok, f)
	}
}

// addAt is add in the day folder of days days ago.
func (e *env) addAt(days int, name, id string, mod time.Time) {
	e.t.Helper()
	d := fixedNow.Local().AddDate(0, 0, -days)
	dir := filepath.Join(e.root, "sessions", d.Format("2006"), d.Format("01"), d.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-2026-11-10T11-00-00-"+id+".jsonl")
	if err := os.WriteFile(p, fixture(e.t, name), 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.touch(p, mod)
	e.paths[id] = p
}

// A terminal session kept open past the scanned window (its rollout is in the
// folder of the day it started and has not changed for days) is found from
// its lock, shown while the lock is held and kept; when the lock goes it ends.
func TestHeldLockFindsARolloutOutsideTheWindow(t *testing.T) {
	e := newEnv(t)
	e.addAt(10, "worker.jsonl", idWorker, fixedNow.Add(-9*24*time.Hour))
	e.lock(idWorker, 4242)
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || f.Live != model.Of(true) || f.TerminalID != agtermID {
		t.Fatalf("held old session: %v %+v", ok, f)
	}
	e.rescan() // found once, stays
	if f, ok := e.last(idWorker); !ok || f.Live != model.Of(true) {
		t.Errorf("second scan: %v %+v", ok, f)
	}
	// A lock whose rollout is nowhere does no harm.
	e.lock(idExec, 5000)
	e.rescan()
	e.unlock(idWorker)
	e.rescan()
	if _, ok := e.last(idWorker); ok {
		t.Error("an old session whose lock went stayed shown (out of the window)")
	}
}

// Only locks of threads that may become cards are asked about.
func TestOnlyCandidateLocksAreAskedAbout(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.add("child.jsonl", idChild, fixedNow.Add(-56*time.Minute))
	e.add("desktop.jsonl", idDesktop, fixedNow.Add(-40*time.Minute))
	e.lock(idWorker, 4242)
	e.lock(idChild, 4242)
	e.lock(idDesktop, 4242)
	e.add("exec.jsonl", idExec, fixedNow.Add(-50*time.Minute))
	if err := os.Truncate(e.paths[idExec], 0); err != nil { // first line not written yet
		t.Fatal(err)
	}
	e.lock(idExec, 5000)
	e.lock(idNoRollout, 5001) // no rollout listed at all
	var asked []string
	inner := e.a.holders
	e.a.holders = func(ctx context.Context, paths []string) (map[string]int, bool) {
		for _, p := range paths {
			asked = append(asked, strings.TrimSuffix(filepath.Base(p), ".lock"))
		}
		return inner(ctx, paths)
	}
	e.rescan()
	slices.Sort(asked)
	if want := []string{idWorker, idExec, idNoRollout}; !slices.Equal(asked, want) {
		t.Errorf("asked about %v, want %v", asked, want)
	}
}

// lsof walks every process's files, so it runs only when its answer can have
// changed: while the same locks are all held by the same processes it is not
// asked again.
func TestLockHoldersAreNotAskedAgainWhileNothingChanged(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.add("exec.jsonl", idExec, fixedNow.Add(-50*time.Minute))
	e.lock(idWorker, 4242)
	e.lock(idExec, 5000)
	calls := 0
	inner := e.a.holders
	e.a.holders = func(ctx context.Context, paths []string) (map[string]int, bool) {
		calls++
		return inner(ctx, paths)
	}
	starts := map[int]string{4242: "a", 5000: "b"}
	e.a.starts = func(_ context.Context, pids []int) (map[int]string, bool) {
		m := map[int]string{}
		for _, p := range pids {
			m[p] = starts[p]
		}
		return m, true
	}
	e.rescan()
	e.rescan()
	e.rescan()
	if calls != 1 {
		t.Fatalf("holders asked %d times for 3 unchanged scans, want 1", calls)
	}
	if f, ok := e.last(idWorker); !ok || f.Live != model.Of(true) || f.TerminalID != agtermID {
		t.Fatalf("worker after cached scans: %v %+v", ok, f)
	}
	// A holder that is a new process under the same pid: ask again.
	starts[5000] = "c"
	e.rescan()
	if calls != 2 {
		t.Errorf("a holder that restarted: %d calls, want 2", calls)
	}
	// The exec ends: its lock file goes.
	e.unlock(idExec)
	e.rescan()
	if f, _ := e.last(idExec); calls != 3 || !f.Ended {
		t.Errorf("a lock went: %d calls, ended %v", calls, f.Ended)
	}
	// A new lock file: ask again. A lock file nobody holds: ask every time.
	e.rescan()
	if calls != 3 {
		t.Errorf("unchanged again: %d calls, want 3", calls)
	}
	e.lock(idDesktop, 0)
	delete(e.held, idDesktop) // a leftover lock file nobody holds
	e.add("desktop.jsonl", idDesktop, fixedNow.Add(-40*time.Minute))
	e.rescan()
	e.rescan()
	if calls != 3 {
		t.Errorf("a desktop thread's lock is not asked about: %d calls", calls)
	}
	e.lock(idChild, 0)
	delete(e.held, idChild)
	e.rescan() // no rollout listed: a candidate, held by nobody
	e.rescan()
	if calls != 5 {
		t.Errorf("an unheld candidate lock: %d calls, want 5", calls)
	}
}

// The cached answer is dropped when it stops holding: a lock file made again
// (a new file, so a new time) and an answer that said nobody holds it.
func TestLockHolderCacheDoesNotGoStale(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.lock(idWorker, 4242)
	starts := map[int]string{4242: "a", 6000: "a"}
	e.a.starts = func(_ context.Context, pids []int) (map[int]string, bool) {
		m := map[int]string{}
		for _, p := range pids {
			m[p] = starts[p]
		}
		return m, true
	}
	e.rescan()
	e.rescan()
	// The thread was resumed in another process: the lock file is a new one.
	lockPath := filepath.Join(e.root, "thread-writer-locks", idWorker+".lock")
	e.held[idWorker] = 6000
	e.touch(lockPath, fixedNow.Add(time.Hour))
	e.rescan()
	// The holder is another process now; the terminal does not depend on it.
	if f, _ := e.last(idWorker); f.TerminalID != agtermID || f.Live != model.Of(true) {
		t.Errorf("after the lock was taken again by pid 6000: %+v", f)
	}
	// Nobody holds it any more but the file stays (a crash); the holder's
	// start time changes back to what it was when the answer was cached.
	starts[6000] = "b"
	delete(e.held, idWorker)
	e.rescan()
	starts[6000] = "a"
	e.rescan()
	if f, _ := e.last(idWorker); !f.Ended || f.Live != model.Of(false) {
		t.Errorf("a stale answer kept the session live: %+v", f)
	}
}

// A rollout whose first line is not yet complete is read again whenever the
// file changes, however many scans it took; only an unchanged file stops
// being read.
func TestMetaIsReadAgainWhenTheFileChanges(t *testing.T) {
	full := fixture(t, "worker.jsonl")
	line := full[:bytes.IndexByte(full, '\n')]
	cases := []struct {
		name  string
		steps [][]byte // each written in turn, at a time of its own, except where noted
		same  bool     // all written with the time of the empty file: only the size changes
	}{
		{"grows within the same second", [][]byte{line[:len(line)/2], full}, true},
		{"same size, a later time", [][]byte{bytes.Repeat([]byte("x"), len(full)), full}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.add("worker.jsonl", idWorker, fixedNow.Add(-5*time.Minute))
			if err := os.WriteFile(e.paths[idWorker], nil, 0o644); err != nil { // created, nothing written yet
				t.Fatal(err)
			}
			e.touch(e.paths[idWorker], fixedNow.Add(-5*time.Minute))
			e.lock(idWorker, 4242)
			for range 2 * maxMetaTries {
				e.rescan()
			}
			if _, ok := e.last(idWorker); ok {
				t.Fatal("a session without a first line was shown")
			}
			for i, part := range c.steps {
				if err := os.WriteFile(e.paths[idWorker], part, 0o644); err != nil {
					t.Fatal(err)
				}
				mod := fixedNow.Add(-5 * time.Minute)
				if !c.same {
					mod = fixedNow.Add(-time.Duration(4-i) * time.Minute)
				}
				e.touch(e.paths[idWorker], mod)
				for range 2 * maxMetaTries {
					e.rescan()
				}
			}
			if f, ok := e.last(idWorker); !ok || f.ToolVersion != "0.171.0" {
				t.Errorf("after the file was written: %v %+v", ok, f)
			}
		})
	}
}

// A spawned thread can spawn too: the count lands on the terminal session at
// the top of the chain, whose children are never cards.
func TestNestedSpawnsAreCountedOnTheTopThread(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.add("child.jsonl", idChild, fixedNow.Add(-56*time.Minute))
	grand := strings.ReplaceAll(string(fixture(t, "child.jsonl")), idChild, idGrand)
	grand = strings.ReplaceAll(grand, idWorker, idChild)
	e.addBytes(idGrand, []byte(grand), fixedNow.Add(-57*time.Minute))
	e.lock(idWorker, 4242)
	e.rescan()
	if f, ok := e.last(idWorker); !ok || f.Subagents != 2 {
		t.Errorf("worker: %v subagents %d, want 2 (child and grandchild)", ok, f.Subagents)
	}
	for _, id := range []string{idChild, idGrand} {
		if _, ok := e.last(id); ok {
			t.Errorf("spawned thread %s became a session", id)
		}
	}
}

// A chain of parents that loops is cut, not followed for ever.
func TestSpawnChainThatLoopsEnds(t *testing.T) {
	r := &registry{sessions: map[string]*sess{
		idChild: {metaOK: true, meta: meta{Parent: idGrand}},
		idGrand: {metaOK: true, meta: meta{Parent: idChild}},
	}}
	if got := r.topOf(idChild); got != idChild && got != idGrand {
		t.Errorf("topOf = %q", got)
	}
}

func TestStatusFollowsTheRollout(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.lock(idWorker, 4242)
	e.rescan()
	steps := []struct {
		line string
		want model.Status
	}{
		{`{"timestamp":"2026-11-20T11:50:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}`, model.StatusBusy},
		{`{"timestamp":"2026-11-20T11:51:00.000Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call_9","arguments":"{}"}}`, model.StatusBlocked},
		{`{"timestamp":"2026-11-20T11:52:00.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_9","output":"Placeholder answer."}}`, model.StatusBusy},
		{`{"timestamp":"2026-11-20T11:52:30.000Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input_async","call_id":"call_10","arguments":"{}"}}`, model.StatusBusy},
		{`{"timestamp":"2026-11-20T11:53:00.000Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t2","reason":"interrupted"}}`, model.StatusFinished},
	}
	for _, s := range steps {
		e.appendLines(idWorker, s.line)
		e.rescan()
		f, _ := e.last(idWorker)
		if f.Status != model.Of(s.want) {
			t.Fatalf("after %s: status %v, want %v", s.line, f.Status, s.want)
		}
	}
	f, _ := e.last(idWorker)
	if len(f.TurnEnds) != 2 || !f.StatusSince.Equal(time.Date(2026, 11, 20, 11, 53, 0, 0, time.UTC)) {
		t.Errorf("turn ends / since = %v %v", f.TurnEnds, f.StatusSince)
	}
}

func TestEndsWhenTheLockIsReleased(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.add("exec.jsonl", idExec, fixedNow.Add(-50*time.Minute)) // ended before sitboard started
	e.lock(idWorker, 4242)
	e.rescan()
	e.unlock(idWorker)
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || !f.Ended || f.Live != model.Of(false) || f.Status.OK || f.TerminalID != "" {
		t.Fatalf("after unlock: %v %+v", ok, f)
	}
	if !f.EndedAt.Equal(fixedNow.Add(-55 * time.Minute)) {
		t.Errorf("EndedAt = %v, want the rollout's last change", f.EndedAt)
	}
	if _, ok := e.last(idExec); ok {
		t.Error("a session never seen live was shown")
	}
	// A lock file left behind by a crash, held by nobody, is not liveness.
	e.lock(idExec, 0)
	delete(e.held, idExec)
	e.rescan()
	if _, ok := e.last(idExec); ok {
		t.Error("an unheld lock file made a session live")
	}
}

// Without a way to see lock holders, a recently changed rollout is shown as
// recently active with liveness unknown; an older one is not.
func TestUnknownLivenessShowsRecentRollouts(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-5*time.Minute))
	e.add("exec.jsonl", idExec, fixedNow.Add(-2*time.Hour))
	e.lock(idWorker, 4242)
	e.fail = true
	e.rescan()
	if f, ok := e.last(idWorker); !ok || f.Live.OK || f.Ended {
		t.Errorf("recent = %v %+v", ok, f)
	}
	if _, ok := e.last(idExec); ok {
		t.Error("an old rollout was shown with unknown liveness")
	}
}

// A failed holder lookup says nothing about sessions already resolved: an
// ended session stays ended (its rollout stays on disk, unlike Claude's
// session file), a live one keeps its terminal.
func TestFailedLookupKeepsResolvedSessions(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.add("exec.jsonl", idExec, fixedNow.Add(-50*time.Minute))
	e.lock(idWorker, 4242)
	e.lock(idExec, 5000)
	e.rescan()
	e.unlock(idExec)
	e.rescan()
	if f, _ := e.last(idExec); !f.Ended {
		t.Fatal("the exec did not end")
	}
	n := len(e.facts)
	e.fail = true
	e.rescan()
	if f, ok := e.last(idExec); !ok || !f.Ended || f.Live != model.Of(false) {
		t.Errorf("ended session after a failed lookup: %v %+v", ok, f)
	}
	if f, ok := e.last(idWorker); !ok || f.Live != model.Of(true) || f.TerminalID != agtermID {
		t.Errorf("live session after a failed lookup: %v %+v", ok, f)
	}
	if len(e.facts) != n {
		t.Errorf("a failed lookup sent %d new facts: %v", len(e.facts)-n, e.facts[n:])
	}
	e.fail = false
	e.rescan()
	if len(e.facts) != n {
		t.Errorf("the next good lookup sent %d new facts", len(e.facts)-n)
	}
}

// A Codex without the writer-lock folder cannot tell liveness: a recently
// changed rollout is shown as recently active; once the folder exists, locks
// decide.
func TestNoLocksFolderFallsBackToRecentRollouts(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-5*time.Minute))
	e.add("exec.jsonl", idExec, fixedNow.Add(-2*time.Hour))
	e.rescan()
	if f, ok := e.last(idWorker); !ok || f.Live.OK || f.Ended {
		t.Errorf("recent rollout without a locks folder: %v %+v", ok, f)
	}
	if _, ok := e.last(idExec); ok {
		t.Error("an old rollout was shown without a locks folder")
	}
	// The folder appears; nobody holds the worker's lock.
	if err := os.MkdirAll(filepath.Join(e.root, "thread-writer-locks"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.rescan()
	if _, ok := e.last(idWorker); ok {
		t.Error("an unheld session stayed shown once locks decide")
	}
}

func TestGoneWhenTheRolloutGoes(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.lock(idWorker, 4242)
	e.rescan()
	if err := os.Remove(e.paths[idWorker]); err != nil {
		t.Fatal(err)
	}
	e.rescan()
	if _, ok := e.last(idWorker); ok {
		t.Error("still shown after its rollout went")
	}
}

func TestProbe(t *testing.T) {
	e := newEnv(t)
	if st := e.a.Probe(context.Background()); st.State != source.StateOK || st.Version != "" {
		t.Errorf("empty = %+v", st)
	}
	e.add("worker.jsonl", idWorker, fixedNow.Add(-time.Hour))
	if st := e.a.Probe(context.Background()); st.Version != "0.171.0" || !st.Tested {
		t.Errorf("probe = %+v", st)
	}
	missing := newAdapter(t, Options{Home: t.TempDir()})
	if st := missing.Probe(context.Background()); st.State != source.StateMissing {
		t.Errorf("missing = %+v", st)
	}
}

func TestNewUsesCodexHome(t *testing.T) {
	if a := New(Options{Home: "/home/dev"}).(*adapter); a.root != "/home/dev/.codex" {
		t.Errorf("root = %q", a.root)
	}
	if a := New(Options{Home: "/home/dev", CodexHome: "/srv/codex"}).(*adapter); a.root != "/srv/codex" {
		t.Errorf("root = %q", a.root)
	}
}

// holderStarts makes the fake ps answer from a map; a pid not in it is not
// running.
func (e *env) holderStarts(m map[int]string) {
	e.a.starts = func(_ context.Context, pids []int) (map[int]string, bool) {
		out := map[int]string{}
		for _, p := range pids {
			if s, ok := m[p]; ok {
				out[p] = s
			}
		}
		return out, true
	}
}

// lsof named a holder that was already gone when ps ran: it is not a holder,
// and the answer is not kept (it would be repeated every scan for ever, ps
// saying nothing about a pid that is not there).
func TestHolderThatPSDoesNotKnowIsNotHolding(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.lock(idWorker, 4242)
	e.holderStarts(map[int]string{}) // the process died after lsof saw it
	calls := 0
	inner := e.a.holders
	e.a.holders = func(ctx context.Context, paths []string) (map[string]int, bool) {
		calls++
		return inner(ctx, paths)
	}
	e.rescan()
	e.rescan()
	e.rescan()
	if f, ok := e.last(idWorker); ok {
		t.Errorf("a holder that is not running kept a session live: %+v", f)
	}
	if calls != 3 {
		t.Errorf("holders asked %d times, want every scan (3): a dead holder must not be cached", calls)
	}
}

// A pid that a new process took over between lsof and ps (it started after the
// lookup began) is not the process lsof saw.
func TestHolderStartedAfterTheLookupIsNotHolding(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.lock(idWorker, 4242)
	e.holderStarts(map[int]string{4242: "Fri Nov 20 12:00:30 2026"}) // 30 s after fixedNow (UTC)
	e.rescan()
	if f, ok := e.last(idWorker); ok {
		t.Errorf("a process that started after the lookup was taken for the holder: %+v", f)
	}
	e.holderStarts(map[int]string{4242: "Fri Nov 20 11:59:59 2026"})
	e.rescan()
	if f, ok := e.last(idWorker); !ok || f.Live != model.Of(true) {
		t.Errorf("a process that started before the lookup: %v %+v", ok, f)
	}
}

// Even a holder that is still there and unchanged is asked about again now
// and then: the cache is a saving, not a belief.
func TestLockHoldersAreAskedAgainNowAndThen(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.lock(idWorker, 4242)
	now := fixedNow
	e.a.now = func() time.Time { return now }
	calls := 0
	inner := e.a.holders
	e.a.holders = func(ctx context.Context, paths []string) (map[string]int, bool) {
		calls++
		return inner(ctx, paths)
	}
	e.rescan()
	now = now.Add(holderRevalidate / 2)
	e.rescan()
	if calls != 1 {
		t.Fatalf("asked %d times within the revalidation time, want 1", calls)
	}
	now = now.Add(holderRevalidate)
	e.rescan()
	if calls != 2 {
		t.Errorf("asked %d times after the revalidation time, want 2", calls)
	}
}

// A cached holder that ps stops listing (it died, the lock file stays) is
// asked about again, not believed.
func TestCachedHolderThatPSStopsListingIsAskedAgain(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.lock(idWorker, 4242)
	e.holderStarts(map[int]string{4242: "Fri Nov 20 10:00:00 2026"})
	calls := 0
	inner := e.a.holders
	e.a.holders = func(ctx context.Context, paths []string) (map[string]int, bool) {
		calls++
		return inner(ctx, paths)
	}
	e.rescan()
	e.rescan()
	if calls != 1 {
		t.Fatalf("asked %d times, want 1", calls)
	}
	e.holderStarts(map[int]string{}) // ps no longer lists 4242
	delete(e.held, idWorker)         // and lsof sees nobody
	e.rescan()
	if f, _ := e.last(idWorker); calls != 2 || !f.Ended {
		t.Errorf("calls %d, ended %v: a holder ps does not list was believed", calls, f.Ended)
	}
}

// linkedTree makes a real rollout for thread id outside the sessions folder,
// in the date folders of days days ago, and a sessions folder in which the
// folder of the given level ("year", "month" or "day") is a link to it.
func (e *env) linkedTree(level string, days int, id string) {
	e.t.Helper()
	d := fixedNow.Local().AddDate(0, 0, -days)
	ext := filepath.Join(e.t.TempDir(), "elsewhere")
	parts := []string{d.Format("2006"), d.Format("01"), d.Format("02")}
	real := filepath.Join(append([]string{ext}, parts...)...)
	if err := os.MkdirAll(real, 0o755); err != nil {
		e.t.Fatal(err)
	}
	p := filepath.Join(real, "rollout-2026-11-10T11-00-00-"+id+".jsonl")
	if err := os.WriteFile(p, fixture(e.t, "worker.jsonl"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.touch(p, fixedNow.Add(-time.Minute))
	n := map[string]int{"year": 1, "month": 2, "day": 3}[level]
	inside := filepath.Join(append([]string{e.root, "sessions"}, parts[:n-1]...)...)
	if err := os.MkdirAll(inside, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(append([]string{ext}, parts[:n]...)...), filepath.Join(inside, parts[n-1])); err != nil {
		e.t.Fatal(err)
	}
	e.paths[id] = filepath.Join(append([]string{e.root, "sessions"}, append(parts, filepath.Base(p))...)...)
}

// A date folder that is a link (a year, a month or a day) is never followed:
// the rollouts behind it are not listed, and a held lock does not find them.
func TestLinkedDateFoldersAreNotFollowed(t *testing.T) {
	for _, level := range []string{"year", "month", "day"} {
		for _, days := range []int{0, 10} { // in the scanned days, and outside them
			t.Run(level+"/"+strconv.Itoa(days), func(t *testing.T) {
				e := newEnv(t)
				e.linkedTree(level, days, idWorker)
				if _, err := os.Stat(e.paths[idWorker]); err != nil {
					t.Fatalf("the test's link does not lead to the rollout: %v", err)
				}
				if got := e.a.rollouts(); len(got) != 0 {
					t.Errorf("rollouts() followed a linked %s folder: %v", level, got)
				}
				e.lock(idWorker, 4242)
				e.rescan()
				if f, ok := e.last(idWorker); ok {
					t.Errorf("a rollout behind a linked %s folder became a card: %+v", level, f)
				}
			})
		}
	}
}

// holdersCalls counts the holder lookups that name files.
func (e *env) holdersCalls() *int {
	n := 0
	inner := e.a.holders
	e.a.holders = func(ctx context.Context, paths []string) (map[string]int, bool) {
		if len(paths) > 0 {
			n++
		}
		return inner(ctx, paths)
	}
	return &n
}

// A held desktop-app thread whose rollout lies outside the scanned days is
// found once and remembered with what it is (not a card): it is not asked
// about again and the date folders are not walked again, as long as its lock
// is there.
func TestHeldRolloutOutsideTheWindowIsRemembered(t *testing.T) {
	e := newEnv(t)
	e.addAt(10, "desktop.jsonl", idDesktop, fixedNow.Add(-9*24*time.Hour))
	e.lock(idDesktop, 4242)
	asked := e.holdersCalls()
	for range 4 {
		e.rescan()
	}
	if *asked != 1 {
		t.Errorf("holders asked %d times in 4 scans, want 1 (the thread's source is known)", *asked)
	}
	if e.r.walks != 1 {
		t.Errorf("date folders walked %d times, want 1", e.r.walks)
	}
	if _, ok := e.last(idDesktop); ok {
		t.Error("a desktop thread became a card")
	}
	// Its lock goes: it is forgotten.
	e.unlock(idDesktop)
	e.rescan()
	if e.r.sessions[idDesktop] != nil || e.r.located[idDesktop] != "" {
		t.Error("a thread whose lock went is still remembered")
	}
}

// Every lost rollout is looked for in one walk; a miss is not looked for
// again for a while, and only so many misses are kept.
func TestLostRolloutsAreSearchedInOneWalk(t *testing.T) {
	e := newEnv(t)
	e.addAt(10, "worker.jsonl", idWorker, fixedNow.Add(-9*24*time.Hour))
	e.addAt(12, "exec.jsonl", idExec, fixedNow.Add(-9*24*time.Hour))
	e.lock(idWorker, 4242)
	e.lock(idExec, 5000)
	e.lock(idNoRollout, 5001)
	now := fixedNow
	e.a.now = func() time.Time { return now }
	e.rescan()
	if e.r.walks != 1 {
		t.Fatalf("3 lost ids made %d walks, want 1", e.r.walks)
	}
	for _, id := range []string{idWorker, idExec} {
		if _, ok := e.last(id); !ok {
			t.Errorf("%s was not found", id)
		}
	}
	e.rescan()
	e.rescan()
	if e.r.walks != 1 {
		t.Errorf("a miss was searched for again at once: %d walks", e.r.walks)
	}
	now = now.Add(locateRetry + time.Second)
	e.rescan()
	if e.r.walks != 2 {
		t.Errorf("after the retry time: %d walks, want 2", e.r.walks)
	}
}

func TestMissesRememberedAreBounded(t *testing.T) {
	e := newEnv(t)
	for i := range 3 * maxNotFound {
		e.r.miss(strconv.Itoa(i), fixedNow)
	}
	if n := len(e.r.notFound); n > maxNotFound || n == 0 {
		t.Errorf("%d misses kept, want at most %d", n, maxNotFound)
	}
}

// One search reads only so many directory entries.
func TestSearchOfTheDateFoldersIsBounded(t *testing.T) {
	e := newEnv(t)
	for d := 11; d <= 15; d++ {
		e.addAt(d, "child.jsonl", "00000000-0000-4000-8000-0000000000"+strconv.Itoa(d), fixedNow.Add(-9*24*time.Hour))
	}
	e.addAt(10, "worker.jsonl", idWorker, fixedNow.Add(-9*24*time.Hour))
	e.lock(idWorker, 4242)
	e.r.walkLimit = 12 // the folders of 5 older days use it up before the 10th day
	e.rescan()
	if _, ok := e.last(idWorker); ok {
		t.Error("a search found a rollout past its limit")
	}
	e.r.walkLimit = 0
	e.r.notFound = map[string]time.Time{}
	e.rescan()
	if _, ok := e.last(idWorker); !ok {
		t.Error("the unbounded search did not find the rollout")
	}
}

// A session resumed today into a rollout under an old date: the lock goes and
// the card stays, as ended, with its recent turns; a lookup that fails does
// not remove it either.
func TestResumedSessionInAnOldFolderEndsAndStays(t *testing.T) {
	e := newEnv(t)
	e.addAt(10, "worker.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.lock(idWorker, 4242)
	e.rescan()
	if f, ok := e.last(idWorker); !ok || f.Live != model.Of(true) {
		t.Fatalf("held: %v %+v", ok, f)
	}
	e.touch(filepath.Join(e.root, "thread-writer-locks", idWorker+".lock"), fixedNow.Add(time.Hour)) // a lookup is made again
	e.fail = true
	e.rescan()
	if f, ok := e.last(idWorker); !ok || f.Live != model.Of(true) || f.TerminalID != "" {
		// Live as it was; the link is not kept (nobody can verify it now).
		t.Fatalf("after a failed lookup: %v %+v", ok, f)
	}
	e.fail = false
	e.unlock(idWorker)
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || !f.Ended || len(f.TurnEnds) == 0 {
		t.Fatalf("after the lock went: %v ended=%v turns=%v", ok, f.Ended, f.TurnEnds)
	}
	if !f.EndedAt.Equal(fixedNow.Add(-30 * time.Minute)) {
		t.Errorf("EndedAt = %v", f.EndedAt)
	}
}

// No text of the conversation reaches the facts: a prompt, a reply, the
// model's reasoning, a tool's output and any other field of the session
// index are read past; the only free text is the title Codex recorded.
func TestNoRolloutTextInEmittedFacts(t *testing.T) {
	const secret = "SECRET-CONVERSATION-TEXT"
	e := newEnv(t)
	rollout := strings.ReplaceAll(string(fixture(t, "worker.jsonl")), "Placeholder prompt.", secret+" prompt")
	rollout = strings.ReplaceAll(rollout, "Placeholder output.", secret+" output")
	rollout += `{"timestamp":"2026-11-20T11:05:30.000Z","ordinal":11,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + secret + ` reply"}]}}
{"timestamp":"2026-11-20T11:05:31.000Z","ordinal":12,"type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"` + secret + ` thought"}]}}
{"timestamp":"2026-11-20T11:05:32.000Z","ordinal":13,"type":"event_msg","payload":{"type":"agent_message","message":"` + secret + ` message"}}
{"timestamp":"2026-11-20T11:05:33.000Z","ordinal":14,"type":"event_msg","payload":{"type":"user_message","message":"` + secret + ` user"}}
`
	e.addBytes(idWorker, []byte(rollout), fixedNow.Add(-55*time.Minute))
	index := `{"id": "` + idWorker + `", "thread_name": "Placeholder title", "first_user_message": "` + secret + ` index", "updated_at": "2026-11-20T11:06:00.000000Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(e.root, "session_index.jsonl"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	e.lock(idWorker, 4242)
	e.rescan()

	f, ok := e.last(idWorker)
	if !ok || f.Name != "Placeholder title" {
		t.Fatalf("no fact, or a name other than the recorded title: %v %q", ok, f.Name)
	}
	for _, x := range flatten(e.facts) {
		if s := fmt.Sprintf("%+v", x); strings.Contains(s, secret) {
			t.Errorf("conversation text reached a fact: %s", s)
		}
	}
}

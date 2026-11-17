package claude

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

const (
	coordID   = "11111111-1111-4111-8111-111111111111"
	workerID  = "22222222-2222-4222-8222-222222222222"
	termCoord = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
	procStart = "Tue Nov 17 08:00:00 2026"
)

// env is a temp home with Claude Code's folders and a fake process table.
type env struct {
	t     *testing.T
	home  string
	mu    sync.Mutex
	procs map[int]string // pid → lstart; nil map with psFail means ps failed
	psBad bool
	reads []int // pids whose environment was read
}

func newEnv(t *testing.T) *env {
	e := &env{t: t, home: t.TempDir(), procs: map[int]string{}}
	if err := os.MkdirAll(filepath.Join(e.home, ".claude", "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) setProc(pid int, start string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if start == "" {
		delete(e.procs, pid)
	} else {
		e.procs[pid] = start
	}
}

func (e *env) setPSBad(bad bool) {
	e.mu.Lock()
	e.psBad = bad
	e.mu.Unlock()
}

func (e *env) starts(_ context.Context, pids []int) (map[int]string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.psBad {
		return nil, false
	}
	out := map[int]string{}
	for _, p := range pids {
		if s, ok := e.procs[p]; ok {
			out[p] = s
		}
	}
	return out, true
}

func (e *env) termID(pid int) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reads = append(e.reads, pid)
	if pid == 4242 {
		return termCoord
	}
	return ""
}

// record writes a session file from the fixture with the given changes.
func (e *env) record(pid int, change func(m map[string]any)) {
	e.t.Helper()
	var m map[string]any
	if err := json.Unmarshal(fixture(e.t, "session-busy.json"), &m); err != nil {
		e.t.Fatal(err)
	}
	m["pid"] = pid
	if change != nil {
		change(m)
	}
	b, _ := json.Marshal(m)
	writeFile(e.t, e.recordPath(pid), b)
}

func (e *env) recordPath(pid int) string {
	return filepath.Join(e.home, ".claude", "sessions", itoa(pid)+".json")
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func (e *env) transcript(folder, id, fixtureName string) string {
	p := filepath.Join(e.home, ".claude", "projects", projectDirName(folder), id+".jsonl")
	writeFile(e.t, p, fixture(e.t, fixtureName))
	return p
}

func (e *env) adapter() *adapter {
	a := New(Options{Home: e.home, Now: func() time.Time { return fixedNow }}).(*adapter)
	a.starts = e.starts
	a.termID = e.termID
	a.tick = 50 * time.Millisecond
	a.liveTick = 50 * time.Millisecond
	a.debounce = 5 * time.Millisecond
	return a
}

type collector struct {
	mu    sync.Mutex
	facts []model.Fact
}

func (c *collector) emit(f model.Fact) {
	c.mu.Lock()
	c.facts = append(c.facts, f)
	c.mu.Unlock()
}

func (c *collector) all() []model.Fact {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.facts)
}

func (c *collector) wait(t *testing.T, within time.Duration, what string, ok func(model.Fact) bool) model.Fact {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, f := range c.all() {
			if ok(f) {
				return f
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; facts: %+v", what, c.all())
	return nil
}

func session(id string, pred func(model.SessionFact) bool) func(model.Fact) bool {
	return func(f model.Fact) bool {
		s, ok := f.(model.SessionFact)
		return ok && s.Key.ID == id && pred(s)
	}
}

func run(t *testing.T, a *adapter) *collector {
	t.Helper()
	c := &collector{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, c.emit) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run = %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return c
}

func TestParsesSessionFile(t *testing.T) {
	staticFolders(t)
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	c := run(t, e.adapter())
	f := c.wait(t, 2*time.Second, "coordinator", session(coordID, func(model.SessionFact) bool { return true })).(model.SessionFact)
	if f.Key.Tool != model.ToolClaude || f.Name != "coordinator-a" || f.ToolVersion != "2.1.326" ||
		f.Folder != "/home/dev/projects/alpha" || f.Status != model.Of(model.StatusBusy) ||
		f.Live != model.Of(true) || f.Ended || f.TerminalID != termCoord ||
		!f.StartedAt.Equal(time.UnixMilli(1794900000000)) || !f.StatusSince.Equal(time.UnixMilli(1794900600000)) {
		t.Fatalf("fact = %+v", f)
	}
	if f.Parent.OK {
		t.Fatal("Claude Code records no parent: Parent must stay unknown")
	}
}

func TestStatusMapping(t *testing.T) {
	cases := map[string]model.Known[model.Status]{
		"busy": model.Of(model.StatusBusy), "shell": model.Of(model.StatusBusy),
		"waiting": model.Of(model.StatusBlocked), "idle": model.Of(model.StatusFinished),
	}
	for in, want := range cases {
		if got := mapStatus(in); got != want {
			t.Errorf("mapStatus(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestUnknownStatusValueIsUnknown(t *testing.T) {
	for _, s := range []string{"", "thinking", "BUSY"} {
		if mapStatus(s).OK {
			t.Errorf("mapStatus(%q) is known", s)
		}
	}
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, func(m map[string]any) { m["status"] = "something-new" })
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "unknown status", session(coordID, func(f model.SessionFact) bool { return !f.Status.OK }))
}

func TestBadRecordsAreSkipped(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.home, ".claude", "sessions")
	writeFile(t, filepath.Join(dir, "1.json"), []byte("{not json"))
	writeFile(t, filepath.Join(dir, "2.json"), []byte(`[1,2,3]`))
	writeFile(t, filepath.Join(dir, "3.json"), bytes.Repeat([]byte(" "), 70<<10))
	e.record(5, func(m map[string]any) { m["sessionId"] = "../../escape" })
	e.record(6, func(m map[string]any) { m["sessionId"] = ".." })
	e.record(7, func(m map[string]any) { m["sessionId"] = workerID; m["pid"] = 8 }) // name and pid disagree
	e.record(4242, nil)
	e.setProc(4242, procStart)
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "the good one", session(coordID, func(model.SessionFact) bool { return true }))
	time.Sleep(100 * time.Millisecond)
	for _, f := range c.all() {
		if s, ok := f.(model.SessionFact); ok && s.Key.ID != coordID {
			t.Fatalf("bad record emitted: %+v", s.Key)
		}
	}
}

func TestDeadPidEmitsEnded(t *testing.T) {
	e := newEnv(t)
	e.record(4242, nil) // no such process
	c := run(t, e.adapter())
	f := c.wait(t, 2*time.Second, "ended", session(coordID, func(f model.SessionFact) bool { return f.Ended })).(model.SessionFact)
	if f.Live != model.Of(false) || f.EndedAt.IsZero() || f.TerminalID != "" {
		t.Fatalf("fact = %+v", f)
	}
}

func TestReusedPidIsNotLive(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, "Wed Nov 18 10:00:00 2026") // same pid, another process
	e.record(4242, nil)
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "ended", session(coordID, func(f model.SessionFact) bool { return f.Ended && f.Live == model.Of(false) }))
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.reads) != 0 {
		t.Fatal("environment of a reused pid was read")
	}
}

func TestStartTimeComparisonIgnoresPadding(t *testing.T) {
	if !sameStart("Sat Nov  7 08:00:00 2026", "Sat Nov 7 08:00:00 2026") {
		t.Fatal("padding mismatch")
	}
	got := parsePS([]byte("  4242 Tue Nov 17 08:00:00 2026\n 99 Sat Nov  7 01:02:03 2026\njunk\n"))
	if got[4242] != procStart || got[99] != "Sat Nov 7 01:02:03 2026" || len(got) != 2 {
		t.Fatalf("parsePS = %v", got)
	}
}

func TestLivenessUnknownWhenPsFails(t *testing.T) {
	e := newEnv(t)
	e.setPSBad(true)
	e.record(4242, nil)
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "unknown liveness", session(coordID, func(f model.SessionFact) bool { return !f.Live.OK && !f.Ended }))
}

func TestFileRemovedEndsThenTranscriptRemovedIsGone(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	tr := e.transcript("/home/dev/projects/alpha", coordID, "coordinator.jsonl")
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "live", session(coordID, func(f model.SessionFact) bool { return f.Live == model.Of(true) }))
	if err := os.Remove(e.recordPath(4242)); err != nil {
		t.Fatal(err)
	}
	f := c.wait(t, 2*time.Second, "ended", session(coordID, func(f model.SessionFact) bool { return f.Ended })).(model.SessionFact)
	if !f.EndedAt.Equal(fixedNow) || f.Live != model.Of(false) {
		t.Fatalf("ended fact = %+v", f)
	}
	for _, x := range c.all() {
		if _, gone := x.(model.SessionGone); gone {
			t.Fatal("SessionGone sent while the transcript still exists")
		}
	}
	if err := os.Remove(tr); err != nil {
		t.Fatal(err)
	}
	c.wait(t, 2*time.Second, "gone", func(f model.Fact) bool {
		g, ok := f.(model.SessionGone)
		return ok && g.Key == model.SessionKey{Tool: model.ToolClaude, ID: coordID}
	})
}

func TestStatusChangeEmitsWithinOneSecond(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	a := e.adapter()
	a.tick, a.liveTick = time.Hour, time.Hour // only the file watch may deliver the change
	c := run(t, a)
	c.wait(t, 2*time.Second, "busy", session(coordID, func(f model.SessionFact) bool { return f.Status == model.Of(model.StatusBusy) }))
	time.Sleep(50 * time.Millisecond)
	e.record(4242, func(m map[string]any) { m["status"] = "idle" })
	c.wait(t, time.Second, "finished", session(coordID, func(f model.SessionFact) bool { return f.Status == model.Of(model.StatusFinished) }))
}

func TestLiveSessionsEmittedBeforeIndexingFinishes(t *testing.T) {
	staticFolders(t)
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	e.transcript("/home/dev/projects/alpha", coordID, "coordinator.jsonl")
	a := e.adapter()
	release := make(chan struct{})
	var once sync.Once
	a.beforeIndex = func() { once.Do(func() { <-release }) }
	c := run(t, a)
	first := c.wait(t, 2*time.Second, "registry fact", session(coordID, func(model.SessionFact) bool { return true })).(model.SessionFact)
	if first.Model.OK || len(first.CreatedPRs) != 0 {
		t.Fatalf("first fact already has derived fields: %+v", first)
	}
	close(release)
	f := c.wait(t, 2*time.Second, "derived", session(coordID, func(f model.SessionFact) bool { return f.Model.OK })).(model.SessionFact)
	if f.Model.V != "claude-opus-5-5" || f.Folder != "/home/dev/projects/alpha" || len(f.TurnEnds) != 1 ||
		!slices.Equal(f.CreatedPRs, []model.PRRef{{Repo: "example-org/example-repo", Number: 101}}) {
		t.Fatalf("derived fact = %+v", f)
	}
}

func TestCountsSubagentsNotAsSessions(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	tr := e.transcript("/home/dev/projects/alpha", coordID, "coordinator.jsonl")
	writeFile(t, filepath.Join(filepath.Dir(tr), coordID, "subagents", "agent-a1.jsonl"), fixture(t, "subagent.jsonl"))
	writeFile(t, filepath.Join(filepath.Dir(tr), coordID, "subagents", "agent-a1.meta.json"), []byte(`{}`))
	c := run(t, e.adapter())
	f := c.wait(t, 2*time.Second, "subagent", session(coordID, func(f model.SessionFact) bool { return f.Subagents == 1 })).(model.SessionFact)
	want := []model.PRRef{{Repo: "example-org/example-repo", Number: 101}, {Repo: "example-org/example-repo", Number: 104}}
	if !slices.Equal(f.CreatedPRs, want) {
		t.Fatalf("CreatedPRs = %+v", f.CreatedPRs)
	}
	if !f.LastActivity.Equal(ts("2026-11-17T08:41:05Z")) {
		t.Fatalf("LastActivity = %v (subagent work counts as activity)", f.LastActivity)
	}
	for _, x := range c.all() {
		if s, ok := x.(model.SessionFact); ok && s.Key.ID != coordID {
			t.Fatalf("subagent emitted as a session: %+v", s.Key)
		}
	}
}

func TestWorkerHintFromReceivedMessages(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.setProc(4243, procStart)
	e.record(4242, nil)
	e.record(4243, func(m map[string]any) { m["sessionId"] = workerID; m["name"] = "worker-a" })
	e.transcript("/home/dev/projects/alpha", coordID, "coordinator.jsonl")
	e.transcript("/home/dev/projects/alpha", workerID, "worker.jsonl")
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "worker hint", session(workerID, func(f model.SessionFact) bool { return f.Model.OK && f.WorkerHint }))
	f := c.wait(t, 2*time.Second, "coordinator", session(coordID, func(f model.SessionFact) bool { return f.Model.OK })).(model.SessionFact)
	if f.WorkerHint {
		t.Fatal("a coordinator hearing from the worker it spawned got WorkerHint")
	}
	if !slices.Equal(f.PeerSenders, []string{"worker-a"}) {
		t.Fatalf("PeerSenders = %v", f.PeerSenders)
	}
}

func TestTranscriptFoundWhenFolderMoved(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, func(m map[string]any) { m["cwd"] = "/home/dev/projects/gamma" })
	e.transcript("/home/dev/projects/alpha", coordID, "coordinator.jsonl")
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "derived", session(coordID, func(f model.SessionFact) bool { return f.Model.OK }))
}

func TestNoTranscriptTextInEmittedFacts(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	tr := e.transcript("/home/dev/projects/alpha", coordID, "coordinator.jsonl")
	writeFile(t, filepath.Join(filepath.Dir(tr), coordID, "subagents", "agent-a1.jsonl"), fixture(t, "subagent.jsonl"))
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "derived", session(coordID, func(f model.SessionFact) bool { return f.Subagents == 1 && f.Model.OK }))
	b, _ := json.Marshal(c.all())
	if bytes.Contains(b, []byte("CANARY")) {
		t.Fatalf("transcript text reached a fact: %s", b)
	}
}

func TestTerminalIDReadOncePerProcess(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "fact", session(coordID, func(f model.SessionFact) bool { return f.TerminalID == termCoord }))
	time.Sleep(200 * time.Millisecond) // several rescans
	e.mu.Lock()
	n := len(e.reads)
	e.mu.Unlock()
	if n != 1 {
		t.Fatalf("environment read %d times, want 1", n)
	}
}

func TestReadsOnlyTheAgtermVariable(t *testing.T) {
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	env := []string{"SECRET_TOKEN=do-not-read", "AGTERM_SESSION_ID=" + id, "HOME=/home/dev"}

	var pa bytes.Buffer
	_ = binary.Write(&pa, binary.LittleEndian, int32(2))
	pa.WriteString("/home/dev/.local/bin/claude\x00\x00\x00\x00")
	pa.WriteString("claude\x00--flag AGTERM_SESSION_ID=bad\x00")
	for _, v := range env {
		pa.WriteString(v + "\x00")
	}
	pa.WriteString("\x00")
	if got := agtermIDFromProcArgs2(pa.Bytes()); got != strings.ToUpper(id) {
		t.Fatalf("procargs2 = %q", got)
	}
	if got := agtermIDFromEnviron([]byte(strings.Join(env, "\x00") + "\x00")); got != strings.ToUpper(id) {
		t.Fatalf("environ = %q", got)
	}
	// Anything that is not an id is refused, and nothing else is returned.
	for _, bad := range []string{"AGTERM_SESSION_ID=--window x", "AGTERM_SESSION_ID=", "OTHER=" + id} {
		if got := agtermIDFromEnviron([]byte(bad + "\x00")); got != "" {
			t.Fatalf("environ(%q) = %q", bad, got)
		}
	}
	for _, short := range [][]byte{nil, {1}, {1, 0, 0, 0}, {5, 0, 0, 0, 'x', 0, 'a', 0}} {
		_ = agtermIDFromProcArgs2(short) // must not panic
	}
}

func TestProbe(t *testing.T) {
	st := New(Options{Home: t.TempDir()}).Probe(context.Background())
	if st.State != source.StateMissing || st.Kind != source.KindClaude {
		t.Fatalf("missing: %+v", st)
	}
	e := newEnv(t)
	e.record(4242, nil)
	st = e.adapter().Probe(context.Background())
	if st.State != source.StateOK || st.Version != "2.1.326" || !st.Tested {
		t.Fatalf("Probe = %+v", st)
	}
	e.record(4242, func(m map[string]any) { m["version"] = "9.9.9" })
	if st := e.adapter().Probe(context.Background()); st.Tested || st.Version != "9.9.9" {
		t.Fatalf("untested version: %+v", st)
	}
}

func TestRunWithoutClaudeReturnsAtOnce(t *testing.T) {
	done := make(chan error, 1)
	go func() { done <- New(Options{Home: t.TempDir()}).Run(context.Background(), func(model.Fact) {}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return for a missing source")
	}
}

// SourceReady comes once, after every live session's first fact and before
// any transcript-derived fact.
func TestSourceReadyAfterFirstPass(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.setProc(4243, procStart)
	e.record(4242, nil)
	e.record(4243, func(m map[string]any) { m["sessionId"] = workerID; m["name"] = "worker-a" })
	e.transcript("/home/dev/projects/alpha", coordID, "coordinator.jsonl")
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "derived", session(coordID, func(f model.SessionFact) bool { return f.Model.OK }))
	facts := c.all()
	readyAt, seen := -1, map[string]bool{}
	for i, f := range facts {
		switch x := f.(type) {
		case model.SourceReady:
			if readyAt >= 0 {
				t.Fatal("SourceReady sent twice")
			}
			if x.Kind != string(source.KindClaude) {
				t.Fatalf("kind = %q", x.Kind)
			}
			readyAt = i
		case model.SessionFact:
			if readyAt < 0 {
				seen[x.Key.ID] = true
				if x.Model.OK {
					t.Fatal("a derived fact came before SourceReady")
				}
			}
		}
	}
	if readyAt < 0 || !seen[coordID] || !seen[workerID] {
		t.Fatalf("ready at %d, sessions before it %v", readyAt, seen)
	}
}

func TestSpawnFieldsReachTheFact(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	e.transcript("/home/dev/projects/alpha", coordID, "coordinator.jsonl")
	c := run(t, e.adapter())
	f := c.wait(t, 2*time.Second, "derived", session(coordID, func(f model.SessionFact) bool { return f.Model.OK })).(model.SessionFact)
	if !slices.Equal(f.SpawnedNames, []string{"worker-a"}) ||
		!slices.Equal(f.SpawnedTerminalIDs, []string{"33333333-3333-4333-8333-333333333333"}) {
		t.Fatalf("spawned = %v %v", f.SpawnedNames, f.SpawnedTerminalIDs)
	}
}

func TestEndedImpliesLiveFalse(t *testing.T) {
	e := newEnv(t)
	e.record(4242, nil)
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "ended", session(coordID, func(f model.SessionFact) bool { return f.Ended }))
	for _, x := range c.all() {
		if s, ok := x.(model.SessionFact); ok && s.Ended && s.Live != model.Of(false) {
			t.Fatalf("ended with Live = %v", s.Live)
		}
	}
}

func TestPsFailureIsUnknownNotEnded(t *testing.T) {
	cases := map[string]error{
		"exit 1 with a message": &runner.ExitError{Code: 1, Stderr: []byte("ps: illegal option")},
		"exit 2":                &runner.ExitError{Code: 2},
		"not runnable":          errors.New("exec: not found"),
	}
	for name, err := range cases {
		starts := psStarts(func(context.Context, string, ...string) ([]byte, error) { return nil, err })
		if _, ok := starts(context.Background(), []int{4242}); ok {
			t.Errorf("%s: ps failure read as an answer", name)
		}
	}
	// A silent exit 1 is ps saying some pids are not running.
	starts := psStarts(func(context.Context, string, ...string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte("4242 " + procStart + "\n")}
	})
	got, ok := starts(context.Background(), []int{4242, 4243})
	if !ok || got[4242] != procStart || len(got) != 1 {
		t.Fatalf("silent exit 1 = %v, %v", got, ok)
	}
}

func TestWatchComesBackAfterFolderIsRecreated(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	a := e.adapter()
	a.liveTick = time.Hour // only the watch (re-added on the fast tick) can deliver changes
	c := run(t, a)
	c.wait(t, 2*time.Second, "first", session(coordID, func(model.SessionFact) bool { return true }))
	dir := filepath.Join(e.home, ".claude", "sessions")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	// No transcript either: the records are gone.
	c.wait(t, 2*time.Second, "gone", func(f model.Fact) bool { _, ok := f.(model.SessionGone); return ok })
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // a few fast ticks: the watch is back
	e.record(4242, func(m map[string]any) { m["status"] = "idle" })
	c.wait(t, 2*time.Second, "live again", session(coordID, func(f model.SessionFact) bool {
		return !f.Ended && f.Status == model.Of(model.StatusFinished)
	}))
}

func TestGlobSearchIsThrottled(t *testing.T) {
	e := newEnv(t)
	a := e.adapter()
	if globEvery < 10*time.Second {
		t.Fatalf("globEvery = %v", globEvery)
	}
	// Placed under another folder's name: found only by the search.
	writeFile(t, filepath.Join(e.home, ".claude", "projects", "-elsewhere", coordID+".jsonl"), []byte("{}\n"))
	if p := a.findTranscript(coordID, "/home/dev/projects/alpha", false); p != "" {
		t.Fatalf("searched without being asked: %q", p)
	}
	if p := a.findTranscript(coordID, "/home/dev/projects/alpha", true); p == "" {
		t.Fatal("search did not find it")
	}
}

package claude

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

const (
	endedID = "33333333-3333-4333-8333-333333333333"
	alphaF  = "/home/dev/projects/alpha"
)

// endedTranscript writes a transcript for a session that left no record: the
// worker fixture under id, with extra lines, last changed at mtime.
func (e *env) endedTranscript(id, extra string, mtime time.Time) string {
	e.t.Helper()
	b := strings.ReplaceAll(string(fixture(e.t, "worker.jsonl")), workerID, id) + extra
	p := filepath.Join(e.home, ".claude", "projects", projectDirName(alphaF), id+".jsonl")
	writeFile(e.t, p, []byte(b))
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func line(format string, args ...any) string { return fmt.Sprintf(format, args...) + "\n" }

// agentName is the line Claude Code writes for a session's name.
func agentName(id, name string) string {
	return line(`{"type":"agent-name","agentName":%q,"sessionId":%q}`, name, id)
}

func turnAt(at string) string {
	return line(`{"isSidechain":false,"type":"system","subtype":"turn_duration","durationMs":1000,"timestamp":%q,"cwd":%q,"entrypoint":"cli"}`, at, alphaF)
}

func ended(id string) func(model.Fact) bool {
	return session(id, func(f model.SessionFact) bool { return f.Ended })
}

// A session that ended before sitboard started and left no record file is
// found by its transcript and indexed once: its turns, model, effort, folder
// and name, as an ended session.
func TestEndedSessionFoundByItsTranscript(t *testing.T) {
	e := newEnv(t)
	e.endedTranscript(endedID, agentName(endedID, "worker-b"), fixedNow.Add(-time.Hour))
	c := run(t, e.adapter())
	f := c.wait(t, 2*time.Second, "the ended session", ended(endedID)).(model.SessionFact)
	if f.Live != model.Of(false) || f.Status.OK || !f.EndedAt.Equal(fixedNow.Add(-time.Hour)) {
		t.Fatalf("not an ended session: %+v", f)
	}
	if f.Name != "worker-b" || f.Folder != alphaF || f.Model != model.Of("claude-sonnet-5-5") || f.Effort != model.Of("medium") ||
		!slices.Equal(f.TurnEnds, []time.Time{ts("2026-11-17T08:06:01Z")}) {
		t.Fatalf("fact %+v", f)
	}
	for _, x := range c.all() {
		if s, ok := x.(model.SessionFact); ok && s.Key.ID == endedID && !s.Model.OK {
			t.Fatalf("sent before it was indexed: %+v", s)
		}
	}
}

// The record's name wins over the transcript's: it is the newer one.
func TestRecordNameWinsOverTheTranscripts(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, func(m map[string]any) { m["sessionId"] = endedID })
	e.endedTranscript(endedID, agentName(endedID, "older-name"), fixedNow)
	c := run(t, e.adapter())
	f := c.wait(t, 2*time.Second, "indexed", session(endedID, func(f model.SessionFact) bool { return f.Model.OK })).(model.SessionFact)
	if f.Name != "coordinator-a" {
		t.Fatalf("name %q", f.Name)
	}
}

// Only transcripts changed within the 7 days count; subagent transcripts and
// headless runs (claude -p, the SDKs) never become sessions.
func TestEndedSessionsLeftOut(t *testing.T) {
	e := newEnv(t)
	const oldID, headless, cli = "44444444-4444-4444-8444-444444444444", "55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666"
	e.endedTranscript(oldID, "", fixedNow.Add(-sessionKeep-time.Minute))
	e.endedTranscript(headless, strings.ReplaceAll(turnAt("2026-11-17T08:30:00Z"), `"cli"`, `"sdk-cli"`)+line(`{"type":"user","entrypoint":"sdk-cli","cwd":%q,"timestamp":"2026-11-17T08:31:00Z"}`, alphaF), fixedNow)
	sub := e.endedTranscript(cli, turnAt("2026-11-17T08:30:00Z"), fixedNow)
	writeFile(t, filepath.Join(filepath.Dir(sub), cli, "subagents", "agent-a1.jsonl"), fixture(t, "subagent.jsonl"))
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "the interactive one", ended(cli))
	time.Sleep(200 * time.Millisecond) // several passes
	for _, x := range c.all() {
		if s, ok := x.(model.SessionFact); ok && s.Key.ID != cli {
			t.Fatalf("%s sent: %+v", s.Key.ID, s)
		}
	}
}

// The board's first paint does not wait: live sessions and SourceReady come
// before any session found by its transcript.
func TestLiveSessionsComeBeforeEndedOnes(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	e.transcript(alphaF, coordID, "coordinator.jsonl")
	e.endedTranscript(endedID, "", fixedNow)
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "the ended one", ended(endedID))
	var order []string
	for _, x := range c.all() {
		switch f := x.(type) {
		case model.SourceReady:
			order = append(order, "ready")
		case model.SessionFact:
			if len(order) == 0 || order[len(order)-1] != f.Key.ID {
				order = append(order, f.Key.ID)
			}
		}
	}
	if i, j := slices.Index(order, "ready"), slices.Index(order, endedID); order[0] != coordID || i < 0 || j < i {
		t.Fatalf("order %v", order)
	}
}

// A session whose record is still there but whose process is gone (it
// crashed) gets its last pass too.
func TestDeadSessionWithARecordIsIndexed(t *testing.T) {
	e := newEnv(t)
	e.record(4242, nil) // no such process
	e.transcript(alphaF, coordID, "coordinator.jsonl")
	c := run(t, e.adapter())
	f := c.wait(t, 2*time.Second, "indexed", session(coordID, func(f model.SessionFact) bool { return f.Ended && f.Model.OK })).(model.SessionFact)
	if len(f.CreatedPRs) == 0 || len(f.TurnEnds) != 1 {
		t.Fatalf("fact %+v", f)
	}
}

// A session that ends while sitboard runs gets one last pass: a turn written
// just before it ended is not lost.
func TestSessionEndingGetsALastPass(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	tr := e.transcript(alphaF, coordID, "coordinator.jsonl")
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "indexed", session(coordID, func(f model.SessionFact) bool { return f.Model.OK }))
	e.setProc(4242, "")
	appendFile(t, tr, []byte(turnAt("2026-11-17T08:50:00Z")))
	if err := os.Remove(e.recordPath(4242)); err != nil {
		t.Fatal(err)
	}
	c.wait(t, 2*time.Second, "the last turn", session(coordID, func(f model.SessionFact) bool {
		return f.Ended && slices.Contains(f.TurnEnds, ts("2026-11-17T08:50:00Z"))
	}))
}

// An ended session leaves memory once it ended more than 7 days ago.
func TestEndedSessionGoesAfterTheWindow(t *testing.T) {
	e := newEnv(t)
	e.endedTranscript(endedID, "", fixedNow.Add(-time.Hour))
	a := e.adapter()
	var mu sync.Mutex
	now := fixedNow
	a.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	c := run(t, a)
	c.wait(t, 2*time.Second, "ended", ended(endedID))
	mu.Lock()
	now = fixedNow.Add(sessionKeep)
	mu.Unlock()
	c.wait(t, 2*time.Second, "gone", func(f model.Fact) bool {
		g, ok := f.(model.SessionGone)
		return ok && g.Key.ID == endedID
	})
}

// After an ended session's last pass only its derived facts are kept: its
// tailers (line state, folder votes) are dropped, and it is not read again.
func TestLastPassDropsTheTailers(t *testing.T) {
	e := newEnv(t)
	p := e.endedTranscript(endedID, "", fixedNow)
	sub := filepath.Join(filepath.Dir(p), endedID, "subagents", "agent-a1.jsonl")
	writeFile(t, sub, fixture(t, "subagent.jsonl"))
	ix := newIndexer(e.adapter())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ix.run(ctx)
	j := job{id: endedID, main: p, subDirs: []string{filepath.Dir(sub)}, ended: true}
	ix.want(map[string]job{endedID: j})
	res := next(t, ix)
	if !res.final || res.subagents != 1 || len(res.d.TurnEnds) != 1 {
		t.Fatalf("result %+v", res)
	}
	appendFile(t, p, []byte(turnAt("2026-11-17T08:55:00Z"))) // read again, it would differ
	ix.want(map[string]job{endedID: j})                      // the registry has not taken it in yet
	select {
	case r := <-ix.results:
		t.Fatalf("indexed twice: %+v", r)
	case <-time.After(150 * time.Millisecond):
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if len(ix.tailers) != 0 || len(ix.subs) != 0 {
		t.Fatalf("kept %d tailers, %d subagent lists", len(ix.tailers), len(ix.subs))
	}
}

// Live sessions are indexed before ended ones in one pass, whatever their size.
func TestLiveIndexedBeforeEnded(t *testing.T) {
	e := newEnv(t)
	small := e.endedTranscript(endedID, "", fixedNow)
	big := e.transcript(alphaF, coordID, "coordinator.jsonl")
	appendFile(t, big, []byte(strings.Repeat(turnAt("2026-11-17T08:40:00Z"), 200)))
	ix := newIndexer(e.adapter())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ix.run(ctx)
	ix.want(map[string]job{
		endedID: {id: endedID, main: small, subDirs: []string{"none"}, ended: true},
		coordID: {id: coordID, main: big, subDirs: []string{"none"}},
	})
	if r := next(t, ix); r.id != coordID {
		t.Fatalf("first result %s, want the live session", r.id)
	}
	if r := next(t, ix); r.id != endedID {
		t.Fatalf("second result %s", r.id)
	}
}

// An ended session's last pass waits for its folder lookups (a few passes at
// most), so it is not sent without the folder a live session would get.
func TestLastPassWaitsForTheFolder(t *testing.T) {
	e := newEnv(t)
	p := e.endedTranscript(endedID, "", fixedNow)
	a := e.adapter()
	release := make(chan struct{})
	a.prober = testProber(t, func(jobs []probeJob, _ string) []probeResult {
		<-release
		out := make([]probeResult, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, probeResult{probeJob: j, group: j.folder})
		}
		return out
	})
	a.prober.expiry = func(time.Time) <-chan time.Time { return expiredNow }
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	t.Cleanup(free) // before the prober's drain, even after a failure
	ix := newIndexer(a)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ix.run(ctx)
	j := map[string]job{endedID: {id: endedID, main: p, subDirs: []string{"none"}, ended: true}}
	ix.want(j)
	select {
	case r := <-ix.results:
		t.Fatalf("sent while the lookup ran: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	free()
	drain(a.prober) // the lookup has answered
	ix.want(j)
	if r := next(t, ix); r.d.Folder != alphaF {
		t.Fatalf("folder %q", r.d.Folder)
	}
}

// ...and not forever: after finalTries passes it is sent with what is known.
func TestLastPassGivesUpOnTheFolder(t *testing.T) {
	e := newEnv(t)
	p := e.endedTranscript(endedID, "", fixedNow)
	a := e.adapter()
	block := make(chan struct{})
	a.prober = testProber(t, func([]probeJob, string) []probeResult { <-block; return nil })
	t.Cleanup(func() { close(block) }) // runs before the prober's drain
	a.prober.expiry = func(time.Time) <-chan time.Time { return expiredNow }
	ix := newIndexer(a)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ix.run(ctx)
	j := map[string]job{endedID: {id: endedID, main: p, subDirs: []string{"none"}, ended: true}}
	got := make(chan result, 1)
	go func() { got <- <-ix.results }()
	for i := range finalTries {
		select {
		case r := <-got:
			if i < finalTries-1 {
				t.Fatalf("sent after %d passes", i)
			}
			if !r.final {
				t.Fatalf("%+v", r)
			}
			return
		case <-time.After(30 * time.Millisecond):
		}
		ix.want(j)
	}
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("never sent")
	}
}

// A transcript first opened for a last pass is read from its last
// endedReadCap bytes, from the first whole line: older lines are left out,
// nothing is read wrong.
func TestReadCapReadsTheTail(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	var b strings.Builder
	b.WriteString(turnAt("2026-11-17T07:00:00Z"))
	b.WriteString(line(`{"type":"user","cwd":%q,"pad":%q}`, alphaF, strings.Repeat("x", 300)))
	b.WriteString(turnAt("2026-11-17T08:00:00Z"))
	writeFile(t, p, []byte(b.String()))
	tl := newTailer(p, staticProber(t))
	tl.readCap = int64(len(turnAt("2026-11-17T08:00:00Z")) + 100) // cuts the long line
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tl.d.TurnEnds, []time.Time{ts("2026-11-17T08:00:00Z")}) || tl.read > tl.readCap {
		t.Fatalf("turns %v, read %d", tl.d.TurnEnds, tl.read)
	}
	// A cut that lands on a line start loses that one line only.
	tl = newTailer(p, staticProber(t))
	tl.readCap = int64(len(turnAt("2026-11-17T08:00:00Z")))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if len(tl.d.TurnEnds) != 0 {
		t.Fatalf("turns %v", tl.d.TurnEnds)
	}
}

// Name and entrypoint come from main-chain lines only, within their caps; a
// name line of another session is not this one's.
func TestNameAndEntrypoint(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, endedID+".jsonl")
	writeFile(t, p, []byte(
		agentName(endedID, "first")+
			agentName(endedID, "second")+
			agentName("99999999-9999-4999-8999-999999999999", "someone-else")+
			line(`{"type":"agent-name","agentName":"sub","isSidechain":true}`)+
			line(`{"type":"agent-name","agentName":%q}`, strings.Repeat("n", 1025))+
			line(`{"type":"user","entrypoint":"cli"}`)+
			line(`{"type":"user","entrypoint":"sdk-cli","isSidechain":true}`)+
			line(`{"type":"user","entrypoint":%q}`, strings.Repeat("e", 33))))
	tl := newTailer(p, staticProber(t))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if tl.d.Name != "second" || tl.d.Entrypoint != "cli" {
		t.Fatalf("name %q, entrypoint %q", tl.d.Name, tl.d.Entrypoint)
	}
	for e, want := range map[string]bool{"": true, "cli": true, "claude-vscode": true, "claude-desktop": true, "remote_mobile": true,
		"sdk-cli": false, "sdk-ts": false, "sdk-py": false, "mcp": false} {
		if interactive(e) != want {
			t.Errorf("interactive(%q) = %v", e, !want)
		}
	}
}

// Start-up cost on synthetic transcripts of realistic sizes: SB_BENCH=1 go
// test -run TestIndexCost -v.
func TestIndexCost(t *testing.T) {
	if os.Getenv("SB_BENCH") == "" {
		t.Skip("SB_BENCH=1 to measure")
	}
	for _, prCalls := range []int{1, 12} { // percent of lines that are `gh pr create` calls
		measureIndexCost(t, prCalls)
	}
}

func measureIndexCost(t *testing.T, prCalls int) {
	dir := t.TempDir()
	r := rand.New(rand.NewPCG(1, 2))
	sizes := []int{}
	for range 40 {
		sizes = append(sizes, 300_000+r.IntN(4_000_000))
	}
	sizes = append(sizes, 13_000_000, 13_000_000, 25_000_000, 60_000_000)
	total := 0
	var paths []string
	for k, s := range sizes {
		p := filepath.Join(dir, fmt.Sprintf("s%03d.jsonl", k))
		synthTranscript(t, p, s, r, fixedNow.Add(-5*24*time.Hour), prCalls)
		paths = append(paths, p)
		total += s
	}
	pr := staticProber(t)
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	t0 := time.Now()
	var keep []derived
	for _, p := range paths {
		tl := newTailer(p, pr)
		tl.readCap = endedReadCap
		if _, err := tl.update(context.Background(), fixedNow); err != nil {
			t.Fatal(err)
		}
		keep = append(keep, tl.d)
	}
	el := time.Since(t0)
	runtime.GC()
	runtime.ReadMemStats(&m1)
	t.Logf("%d%% pr calls: %d files, %.0f MB in %v: %.0f MB/s; heap kept %+.2f MB for %d sessions",
		prCalls, len(paths), float64(total)/1e6, el, float64(total)/1e6/el.Seconds(), float64(int64(m1.HeapAlloc)-int64(m0.HeapAlloc))/1e6, len(keep))
}

// synthTranscript writes a transcript with a working session's mix of lines:
// replies with and without tool calls, tool results mostly small and some
// very large, turn ends, progress lines.
func synthTranscript(t *testing.T, path string, size int, r *rand.Rand, start time.Time, prCalls int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	pad := func(k int) string { return strings.Repeat("lorem ipsum dolor ", k/18+1)[:k] }
	at := start
	for n, i := 0, 0; n < size; i++ {
		at = at.Add(time.Duration(r.IntN(20)) * time.Second)
		stamp := at.UTC().Format(time.RFC3339Nano)
		var l string
		switch p := r.IntN(100); {
		case p < prCalls:
			l = fmt.Sprintf(`{"type":"assistant","timestamp":%q,"cwd":%q,"entrypoint":"cli","effort":"high","message":{"model":"claude-opus-5-5","content":[{"type":"tool_use","id":"t%d","name":"Bash","input":{"command":"gh pr create --fill %s"}}]}}`, stamp, alphaF, i, pad(200))
		case p < 35 && p >= prCalls:
			l = fmt.Sprintf(`{"type":"assistant","timestamp":%q,"cwd":%q,"entrypoint":"cli","effort":"high","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":%q}]}}`, stamp, alphaF, pad(500+r.IntN(3000)))
		case p < 75:
			k := 1000 + r.IntN(6000)
			if q := r.IntN(100); q < 8 {
				k = 30_000 + r.IntN(100_000)
			} else if q < 9 {
				k = 300_000 + r.IntN(700_000)
			}
			l = fmt.Sprintf(`{"type":"user","timestamp":%q,"cwd":%q,"entrypoint":"cli","message":{"content":[{"type":"tool_result","tool_use_id":"t%d","content":%q}]}}`, stamp, alphaF, i-1, pad(k))
		case p < 78:
			l = fmt.Sprintf(`{"type":"system","subtype":"turn_duration","timestamp":%q,"cwd":%q,"durationMs":12000}`, stamp, alphaF)
		default:
			l = fmt.Sprintf(`{"type":"progress","timestamp":%q,"cwd":%q,"data":%q}`, stamp, alphaF, pad(200+r.IntN(4000)))
		}
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
		n += len(l) + 1
	}
}

// The indexer reads an ended session's transcript from its last
// endedReadCap bytes.
func TestLastPassUsesTheReadCap(t *testing.T) {
	e := newEnv(t)
	p := e.endedTranscript(endedID, turnAt("2026-11-17T08:50:00Z"), fixedNow)
	old := endedReadCap
	endedReadCap = int64(len(turnAt("2026-11-17T08:50:00Z")) + 10)
	t.Cleanup(func() { endedReadCap = old })
	ix := newIndexer(e.adapter())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ix.run(ctx)
	ix.want(map[string]job{endedID: {id: endedID, main: p, subDirs: []string{"none"}, ended: true}})
	if r := next(t, ix); !slices.Equal(r.d.TurnEnds, []time.Time{ts("2026-11-17T08:50:00Z")}) {
		t.Fatalf("turns %v: read more than the cap", r.d.TurnEnds)
	}
}

// New work stops a pass over ended sessions: the live session that arrived
// is indexed before the ended ones left.
func TestNewWorkComesBeforeTheEndedLeft(t *testing.T) {
	e := newEnv(t)
	const second = "77777777-7777-4777-8777-777777777777"
	a := e.endedTranscript(endedID, "", fixedNow)
	b := e.endedTranscript(second, strings.Repeat(turnAt("2026-11-17T08:40:00Z"), 50), fixedNow)
	live := e.transcript(alphaF, coordID, "coordinator.jsonl")
	ix := newIndexer(e.adapter())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ix.run(ctx)
	ja := job{id: endedID, main: a, subDirs: []string{"none"}, ended: true}
	jb := job{id: second, main: b, subDirs: []string{"none"}, ended: true}
	ix.want(map[string]job{endedID: ja, second: jb})
	time.Sleep(100 * time.Millisecond) // the indexer waits to hand over the first
	ix.want(map[string]job{endedID: ja, second: jb, coordID: {id: coordID, main: live, subDirs: []string{"none"}}})
	var got []string
	for range 3 {
		got = append(got, (next(t, ix)).id)
	}
	if !slices.Equal(got, []string{endedID, coordID, second}) {
		t.Fatalf("order %v", got)
	}
}

// Each ended session's folder lookup gets its own wait, not what is left of
// the pass's: a pass of several ended sessions sends them all.
func TestEachLastPassWaitsOnItsOwn(t *testing.T) {
	e := newEnv(t)
	const second = "77777777-7777-4777-8777-777777777777"
	a := e.endedTranscript(endedID, "", fixedNow)
	b := e.endedTranscript(second, "", fixedNow)
	ad := e.adapter()
	ad.prober = testProber(t, func(jobs []probeJob, _ string) []probeResult {
		time.Sleep(60 * time.Millisecond)
		out := make([]probeResult, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, probeResult{probeJob: j, group: j.folder})
		}
		return out
	})
	ad.prober.budget, ad.prober.expiry = 100*time.Millisecond, at
	ix := newIndexer(ad)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ix.run(ctx)
	ix.want(map[string]job{
		endedID: {id: endedID, main: a, subDirs: []string{"none"}, ended: true},
		second:  {id: second, main: b, subDirs: []string{"none"}, ended: true},
	})
	for range 2 {
		select {
		case r := <-ix.results:
			if r.d.Folder != alphaF {
				t.Fatalf("%+v", r)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("an ended session waited for a later pass")
		}
	}
}

// An answer that is already in is taken even when the deadline has passed.
func TestProbeTakesAnAnswerAlreadyIn(t *testing.T) {
	for range 50 {
		pr := staticProber(t)
		pr.expiry = func(time.Time) <-chan time.Time { return expiredNow }
		tl := newTailer("x", pr)
		tl.vote(alphaF)
		ch := make(chan []probeResult, 1)
		ch <- []probeResult{{probeJob: probeJob{folder: alphaF, look: true, check: true}, group: alphaF}}
		tl.probing = ch
		tl.probe(fixedNow)
		if tl.probing != nil || !tl.settled() {
			t.Fatal("the answer in the channel was not taken")
		}
	}
}

// next is the indexer's next result, or a failure after two seconds.
func next(t *testing.T, ix *indexer) result {
	t.Helper()
	select {
	case r := <-ix.results:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("no result")
		return result{}
	}
}

// A result from a pass made while the session still ran is not taken once
// it has ended: its last pass follows.
func TestStaleLiveResultIsNotTaken(t *testing.T) {
	r := &registry{a: newEnv(t).adapter(), emit: func(model.Fact) {}, sessions: map[string]*sess{}, sent: map[string]model.SessionFact{}}
	s := &sess{rec: record{SessionID: endedID}, ended: true, live: model.Of(false)}
	r.sessions[endedID] = s
	r.applyIndex(result{id: endedID, d: derived{Model: "m"}})
	if s.d.Model != "" || s.final {
		t.Fatalf("taken: %+v", s)
	}
	r.applyIndex(result{id: endedID, d: derived{Model: "m"}, final: true, readable: true})
	if s.d.Model != "m" || !s.final {
		t.Fatalf("last pass not taken: %+v", s)
	}
}

// A session found by its transcript is not sent before its last pass is in,
// however many rescans come first.
func TestFoundSessionNotSentEmpty(t *testing.T) {
	e := newEnv(t)
	e.endedTranscript(endedID, "", fixedNow)
	a := e.adapter()
	var passes int
	var mu sync.Mutex
	a.beforeIndex = func() {
		mu.Lock()
		passes++
		n := passes
		mu.Unlock()
		if n == 2 { // the pass after discover: hold it over several rescans
			time.Sleep(300 * time.Millisecond)
		}
	}
	c := run(t, a)
	c.wait(t, 3*time.Second, "indexed", ended(endedID))
	for _, x := range c.all() {
		if s, ok := x.(model.SessionFact); ok && s.Key.ID == endedID && !s.Model.OK {
			t.Fatalf("sent before it was indexed: %+v", s)
		}
	}
}

// An ended session that comes back (claude --resume) is indexed again, as a
// live one.
func TestEndedSessionComesBack(t *testing.T) {
	e := newEnv(t)
	p := e.endedTranscript(endedID, "", fixedNow)
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "ended", ended(endedID))
	appendFile(t, p, []byte(turnAt("2026-11-17T08:55:00Z")))
	e.setProc(4242, procStart)
	e.record(4242, func(m map[string]any) { m["sessionId"] = endedID; m["cwd"] = alphaF })
	c.wait(t, 3*time.Second, "live with the new turn", session(endedID, func(f model.SessionFact) bool {
		return !f.Ended && slices.Contains(f.TurnEnds, ts("2026-11-17T08:55:00Z"))
	}))
}

// Transcripts older than the window are not even taken in.
func TestDiscoverSkipsOldTranscripts(t *testing.T) {
	e := newEnv(t)
	const oldID = "44444444-4444-4444-8444-444444444444"
	e.endedTranscript(oldID, "", fixedNow.Add(-sessionKeep-time.Minute))
	e.endedTranscript(endedID, "", fixedNow)
	r := &registry{a: e.adapter(), emit: func(model.Fact) {}, sessions: map[string]*sess{}, sent: map[string]model.SessionFact{}}
	r.discover()
	if _, ok := r.sessions[oldID]; ok || r.sessions[endedID] == nil {
		t.Fatalf("sessions %v", slices.Collect(maps.Keys(r.sessions)))
	}
}

// A crashed session whose record and transcript last changed more than 7
// days ago is not indexed.
func TestOldDeadRecordIsNotIndexed(t *testing.T) {
	e := newEnv(t)
	e.record(4242, nil) // no such process
	tr := e.transcript(alphaF, coordID, "coordinator.jsonl")
	old := fixedNow.Add(-sessionKeep - time.Hour)
	for _, p := range []string{tr, e.recordPath(4242)} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "ended", ended(coordID))
	time.Sleep(200 * time.Millisecond) // several passes
	for _, x := range c.all() {
		if s, ok := x.(model.SessionFact); ok && s.Model.OK {
			t.Fatalf("indexed: %+v", s)
		}
	}
}

func backfillDone(f model.Fact) bool { _, ok := f.(model.BackfillDone); return ok }

// Review of #82, A: BackfillDone comes once the sessions that had ended
// before the start are read, never before; found sessions carry Backfill.
func TestBackfillDoneAfterTheEndedSessions(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	e.transcript(alphaF, coordID, "coordinator.jsonl")
	e.endedTranscript(endedID, "", fixedNow)
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "done", backfillDone)
	var seenEnded bool
	for _, x := range c.all() {
		if f, ok := x.(model.SessionFact); ok {
			if f.Backfill != (f.Key.ID == endedID) {
				t.Fatalf("%s Backfill %v", f.Key.ID, f.Backfill)
			}
			seenEnded = seenEnded || f.Key.ID == endedID
		}
		if backfillDone(x) {
			if !seenEnded {
				t.Fatal("BackfillDone before the ended session")
			}
			return
		}
	}
}

func TestBackfillDoneWithNothingToRead(t *testing.T) {
	e := newEnv(t)
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "done", backfillDone)
}

// Review of #82, C: interactive sessions not started from a terminal count;
// the MCP server does not; and a claude -p run seen while it ran is
// withdrawn at its end, so no -p run is in catch-up.
func TestHeadlessIsOneRule(t *testing.T) {
	e := newEnv(t)
	const ide, mcp = "88888888-8888-4888-8888-888888888888", "99999999-9999-4999-8999-999999999999"
	entry := func(id, ep string) string {
		return line(`{"type":"user","entrypoint":%q,"sessionId":%q,"cwd":%q}`, ep, id, alphaF)
	}
	e.endedTranscript(ide, entry(ide, "claude-vscode"), fixedNow)
	e.endedTranscript(mcp, entry(mcp, "mcp"), fixedNow)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	tr := e.transcript(alphaF, coordID, "coordinator.jsonl")
	appendFile(t, tr, []byte(entry(coordID, "sdk-cli")))
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "the IDE session", ended(ide))
	c.wait(t, 2*time.Second, "the -p run while it runs, marked headless", session(coordID, func(f model.SessionFact) bool { return f.Model.OK && f.Headless }))
	e.setProc(4242, "")
	if err := os.Remove(e.recordPath(4242)); err != nil {
		t.Fatal(err)
	}
	c.wait(t, 2*time.Second, "the -p run withdrawn", func(f model.Fact) bool {
		g, ok := f.(model.SessionGone)
		return ok && g.Key.ID == coordID
	})
	time.Sleep(150 * time.Millisecond)
	for _, x := range c.all() {
		if f, ok := x.(model.SessionFact); ok && (f.Key.ID == mcp || (f.Key.ID == coordID && f.Model.OK && !f.Headless) || (f.Key.ID == ide && f.Headless)) {
			t.Fatalf("headless session sent: %+v", f)
		}
	}
}

// A claude -p run started in another session's terminal inherits its
// AGTERM_SESSION_ID (the test process 4242 has one). The arguments say -p, so
// no fact of the run, the first included, has a terminal.
func TestPrintFlagRunHasNoTerminalFromTheFirstFact(t *testing.T) {
	e := newEnv(t)
	e.printFlag = true
	e.setProc(4242, procStart)
	e.record(4242, nil)
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "the run", session(coordID, func(f model.SessionFact) bool { return f.Live.V }))
	n := 0
	for _, x := range c.all() {
		if f, ok := x.(model.SessionFact); ok && f.Key.ID == coordID {
			n++
			if f.TerminalID != "" || !f.Headless {
				t.Fatalf("fact %d: terminal %q, headless %v", n, f.TerminalID, f.Headless)
			}
		}
	}
}

// An SDK run has no -p; its transcript's entrypoint says it is headless, and
// from then on its facts have no terminal.
func TestSDKRunHasNoTerminalOnceItsTranscriptIsRead(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	tr := e.transcript(alphaF, coordID, "coordinator.jsonl")
	appendFile(t, tr, []byte(line(`{"type":"user","entrypoint":"sdk-ts","sessionId":%q,"cwd":%q}`, coordID, alphaF)))
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "the run without a terminal", session(coordID, func(f model.SessionFact) bool {
		return f.Model.OK && f.Headless && f.Live.V && f.TerminalID == ""
	}))
}

// Review of #82, 4: a tail with no whole line (one line longer than the
// cap) says nothing about how the session was started: it is not counted.
func TestTailWithNothingReadableIsNotCounted(t *testing.T) {
	old := endedReadCap
	endedReadCap = 1 << 20
	t.Cleanup(func() { endedReadCap = old })
	e := newEnv(t)
	big := line(`{"type":"user","entrypoint":"sdk-cli","message":{"content":%q}}`, strings.Repeat("x", 3<<20))
	e.endedTranscript(endedID, line(`{"type":"user","entrypoint":"sdk-cli","cwd":%q}`, alphaF)+big, fixedNow)
	c := run(t, e.adapter())
	c.wait(t, 3*time.Second, "done", backfillDone)
	for _, x := range c.all() {
		if f, ok := x.(model.SessionFact); ok && f.Key.ID == endedID {
			t.Fatalf("sent: %+v", f)
		}
	}
}

// Review of #82, 5: nothing read from a headless session is kept.
func TestHeadlessKeepsNothing(t *testing.T) {
	r := &registry{a: newEnv(t).adapter(), emit: func(model.Fact) {}, sessions: map[string]*sess{}, sent: map[string]model.SessionFact{}}
	s := &sess{rec: record{SessionID: endedID}, ended: true, live: model.Of(false), found: true}
	r.sessions[endedID] = s
	r.applyIndex(result{id: endedID, final: true, readable: true, subagents: 2,
		d: derived{Entrypoint: "sdk-cli", Model: "m", TurnEnds: []time.Time{fixedNow}, CreatedPRs: []model.PRRef{{Repo: model.GitHubRepo("example-org/x"), Number: 1}}}})
	if !s.headless || !reflect.DeepEqual(s.d, derived{}) || s.subagents != 0 {
		t.Fatalf("kept %+v", s)
	}
}

// Review of #82, 6: a session that ends, comes back and ends again before
// the indexer ran a pass gets a last pass for its second end too.
func TestEachEndGetsItsLastPass(t *testing.T) {
	e := newEnv(t)
	p := e.endedTranscript(endedID, "", fixedNow)
	ix := newIndexer(e.adapter())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ix.run(ctx)
	ix.want(map[string]job{endedID: {id: endedID, main: p, subDirs: []string{"none"}, ended: true, gen: 1}})
	next(t, ix)
	appendFile(t, p, []byte(turnAt("2026-11-17T08:55:00Z")))
	ix.want(map[string]job{endedID: {id: endedID, main: p, subDirs: []string{"none"}, ended: true, gen: 2}})
	if r := next(t, ix); !slices.Contains(r.d.TurnEnds, ts("2026-11-17T08:55:00Z")) {
		t.Fatalf("second last pass %+v", r)
	}
}

// Review of #82, 7: links are not followed, to a folder or to a file, and
// a transcript swapped for a link after it was listed is not read.
func TestLinksAreNotFollowed(t *testing.T) {
	e := newEnv(t)
	real := e.endedTranscript(endedID, agentName(endedID, "real"), fixedNow)
	projects := filepath.Join(e.home, ".claude", "projects")
	const viaFile, viaDir = "44444444-4444-4444-8444-444444444444", "55555555-5555-4555-8555-555555555555"
	if err := os.Symlink(real, filepath.Join(filepath.Dir(real), viaFile+".jsonl")); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "elsewhere")
	writeFile(t, filepath.Join(other, viaDir+".jsonl"), []byte(turnAt("2026-11-17T08:00:00Z")))
	if err := os.Symlink(other, filepath.Join(projects, "-linked")); err != nil {
		t.Fatal(err)
	}
	r := &registry{a: e.adapter(), emit: func(model.Fact) {}, sessions: map[string]*sess{}, sent: map[string]model.SessionFact{}}
	r.discover()
	if len(r.sessions) != 1 || r.sessions[endedID] == nil {
		t.Fatalf("found %v", slices.Collect(maps.Keys(r.sessions)))
	}
	// Swapped after the listing: the tailer refuses it.
	if err := os.Remove(real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, viaDir+".jsonl"), real); err != nil {
		t.Fatal(err)
	}
	tl := newTailer(real, staticProber(t))
	if _, err := tl.update(context.Background(), fixedNow); err == nil || len(tl.d.TurnEnds) != 0 {
		t.Fatalf("read through a link: err %v, turns %v", err, tl.d.TurnEnds)
	}
	if f, err := openNoFollow(real); err == nil {
		_ = f.Close()
		t.Fatal("openNoFollow followed a link")
	}
}

// Review of #82, 2: a transcript in two project folders is read from the
// one changed last.
func TestDuplicateTranscriptTheNewerWins(t *testing.T) {
	e := newEnv(t)
	newer := e.endedTranscript(endedID, agentName(endedID, "newer"), fixedNow)
	older := filepath.Join(e.home, ".claude", "projects", "-a-first-folder", endedID+".jsonl") // sorts first
	writeFile(t, older, []byte(agentName(endedID, "older")))
	if err := os.Chtimes(older, fixedNow.Add(-time.Hour), fixedNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	r := &registry{a: e.adapter(), emit: func(model.Fact) {}, sessions: map[string]*sess{}, sent: map[string]model.SessionFact{}}
	r.discover()
	if s := r.sessions[endedID]; s == nil || s.transcript != newer {
		t.Fatalf("read %+v", s)
	}
}

// D and 8: a turn line of another session (as a fork might copy) is not
// this session's turn.
func TestForeignTurnLinesDoNotCount(t *testing.T) {
	p := filepath.Join(t.TempDir(), endedID+".jsonl")
	writeFile(t, p, []byte(
		line(`{"type":"system","subtype":"turn_duration","timestamp":"2026-11-17T08:00:00Z","sessionId":"99999999-9999-4999-8999-999999999999"}`)+
			line(`{"type":"system","subtype":"turn_duration","timestamp":"2026-11-17T08:30:00Z","sessionId":%q}`, endedID)+
			turnAt("2026-11-17T08:40:00Z"))) // no id: an older Claude Code
	tl := newTailer(p, staticProber(t))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tl.d.TurnEnds, []time.Time{ts("2026-11-17T08:30:00Z"), ts("2026-11-17T08:40:00Z")}) {
		t.Fatalf("turns %v", tl.d.TurnEnds)
	}
}

// A session that ends, comes back and ends again gets a last pass for each
// end: a turn written before the second end is not lost.
func TestSecondEndGetsItsLastPass(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	tr := e.transcript(alphaF, coordID, "coordinator.jsonl")
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "indexed", session(coordID, func(f model.SessionFact) bool { return f.Model.OK }))
	end := func() {
		e.setProc(4242, "")
		if err := os.Remove(e.recordPath(4242)); err != nil {
			t.Fatal(err)
		}
	}
	end()
	c.wait(t, 2*time.Second, "first end", ended(coordID))
	time.Sleep(150 * time.Millisecond) // its last pass is in
	e.setProc(4242, procStart)
	e.record(4242, nil)
	n := len(c.all())
	deadline := time.Now().Add(2 * time.Second)
	for back := false; !back; {
		for _, x := range c.all()[n:] {
			if f, ok := x.(model.SessionFact); ok && f.Key.ID == coordID && f.Live == model.Of(true) {
				back = true
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("never came back")
		}
		time.Sleep(2 * time.Millisecond)
	}
	appendFile(t, tr, []byte(turnAt("2026-11-17T08:58:00Z")))
	end()
	c.wait(t, 2*time.Second, "the second last pass", session(coordID, func(f model.SessionFact) bool {
		return f.Ended && slices.Contains(f.TurnEnds, ts("2026-11-17T08:58:00Z"))
	}))
}

// Each end of a session is a new generation of its last pass, whether its
// process went (the record stays) or its record went.
func TestEachEndIsANewGeneration(t *testing.T) {
	e := newEnv(t)
	e.transcript(alphaF, coordID, "coordinator.jsonl")
	r := &registry{a: e.adapter(), emit: func(model.Fact) {}, sessions: map[string]*sess{}, sent: map[string]model.SessionFact{}, firsts: map[string]firstSession{}}
	ctx := context.Background()
	gen := func() int {
		t.Helper()
		j, ok := r.jobs()[coordID]
		if !ok || !j.ended {
			t.Fatalf("no last pass: %+v", j)
		}
		return j.gen
	}
	live := func() {
		e.setProc(4242, procStart)
		e.record(4242, nil)
		r.rescan(ctx)
	}
	live()
	e.setProc(4242, "") // the process goes, the record stays
	r.rescan(ctx)
	first := gen()
	live()
	if err := os.Remove(e.recordPath(4242)); err != nil { // the record goes
		t.Fatal(err)
	}
	e.setProc(4242, "")
	r.rescan(ctx)
	second := gen()
	if second == first {
		t.Fatalf("both ends are generation %d", first)
	}
	live()
	e.setProc(4242, "") // the process goes again, the record stays
	r.rescan(ctx)
	if third := gen(); third == second {
		t.Fatalf("the second and third ends are generation %d", second)
	}
}

// Review of #82 (second round), 4: one interactive line makes the session
// interactive, in either order: an interactive session resumed with
// claude -p keeps its turns, and so does a -p run a person resumed.
func TestInteractiveLineWins(t *testing.T) {
	asPrint := func(l string) string { return strings.ReplaceAll(l, `"cli"`, `"sdk-cli"`) }
	for name, extra := range map[string]string{
		"then -p":  turnAt("2026-11-17T08:30:00Z") + asPrint(turnAt("2026-11-17T08:50:00Z")),
		"-p first": asPrint(turnAt("2026-11-17T08:30:00Z")) + turnAt("2026-11-17T08:50:00Z"),
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.endedTranscript(endedID, extra, fixedNow)
			c := run(t, e.adapter())
			f := c.wait(t, 2*time.Second, "sent", ended(endedID)).(model.SessionFact)
			if f.Headless || len(f.TurnEnds) == 0 {
				t.Fatalf("headless %v, turns %v", f.Headless, f.TurnEnds)
			}
		})
	}
}

// Review of #82 (second round), 2c: a session that ends, comes back and ends
// again with its transcript unchanged still gets its second last pass.
func TestSecondLastPassWithAnUnchangedTranscript(t *testing.T) {
	e := newEnv(t)
	p := e.endedTranscript(endedID, "", fixedNow)
	ix := newIndexer(e.adapter())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ix.run(ctx)
	ix.want(map[string]job{endedID: {id: endedID, main: p, subDirs: []string{"none"}, ended: true, gen: 1}})
	if r := next(t, ix); !r.final || r.gen != 1 {
		t.Fatalf("first %+v", r)
	}
	ix.want(map[string]job{endedID: {id: endedID, main: p, subDirs: []string{"none"}, ended: true, gen: 2}})
	if r := next(t, ix); !r.final || r.gen != 2 {
		t.Fatalf("second %+v", r)
	}
}

// A last pass for an earlier end is not taken once the session ended again.
func TestLastPassOfAnEarlierEndIsNotTaken(t *testing.T) {
	r := &registry{a: newEnv(t).adapter(), emit: func(model.Fact) {}, sessions: map[string]*sess{}, sent: map[string]model.SessionFact{}}
	s := &sess{rec: record{SessionID: endedID}, ended: true, live: model.Of(false), ends: 2}
	r.sessions[endedID] = s
	r.applyIndex(result{id: endedID, final: true, readable: true, gen: 1, d: derived{Model: "m"}})
	if s.final {
		t.Fatal("took the last pass of the first end")
	}
	r.applyIndex(result{id: endedID, final: true, readable: true, gen: 2, d: derived{Model: "m"}})
	if !s.final {
		t.Fatal("did not take its own last pass")
	}
}

// BackfillDone comes from the last pass itself: no scan follows it.
func TestBackfillDoneFromTheLastPass(t *testing.T) {
	var got []model.Fact
	r := &registry{a: newEnv(t).adapter(), emit: func(f model.Fact) { got = append(got, f) }, sessions: map[string]*sess{}, sent: map[string]model.SessionFact{}, backfilling: true}
	r.sessions[endedID] = &sess{rec: record{SessionID: endedID}, transcript: "x", ended: true, endedAt: fixedNow, live: model.Of(false), found: true, ends: 1}
	r.applyIndex(result{id: endedID, final: true, readable: true, gen: 1, d: derived{Model: "m"}})
	if len(got) != 2 || !backfillDone(got[1]) {
		t.Fatalf("emitted %v", got)
	}
}

// Every pass runs at least one ended job, however often new work wakes it.
func TestEveryPassRunsAnEndedJob(t *testing.T) {
	e := newEnv(t)
	live := e.transcript(alphaF, coordID, "coordinator.jsonl")
	p := e.endedTranscript(endedID, "", fixedNow)
	jobs := map[string]job{
		coordID: {id: coordID, main: live, subDirs: []string{"none"}},
		endedID: {id: endedID, main: p, subDirs: []string{"none"}, ended: true, gen: 1},
	}
	a := e.adapter()
	ix := newIndexer(a)
	a.beforeIndex = func() { ix.want(jobs) } // new work at the start of every pass
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ix.run(ctx)
	ix.want(jobs)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case r := <-ix.results:
			if r.final {
				return
			}
		case <-deadline:
			t.Fatal("the ended job never ran")
		}
	}
}

// The entrypoint of a line of another session is not this one's.
func TestForeignEntrypointDoesNotCount(t *testing.T) {
	p := filepath.Join(t.TempDir(), endedID+".jsonl")
	writeFile(t, p, []byte(
		line(`{"type":"user","entrypoint":"sdk-cli","sessionId":%q}`, endedID)+
			line(`{"type":"user","entrypoint":"cli","sessionId":"99999999-9999-4999-8999-999999999999"}`)))
	tl := newTailer(p, staticProber(t))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if tl.d.Entrypoint != "sdk-cli" {
		t.Fatalf("entrypoint %q", tl.d.Entrypoint)
	}
}

// Only a session found by its transcript is a backfill one: a session that
// ends while sitboard runs is not, so it stays a spawn record on the board.
func TestEndedInThisRunIsNotBackfill(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	e.transcript(alphaF, coordID, "coordinator.jsonl")
	c := run(t, e.adapter())
	c.wait(t, 2*time.Second, "indexed", session(coordID, func(f model.SessionFact) bool { return f.Model.OK }))
	e.setProc(4242, "")
	if err := os.Remove(e.recordPath(4242)); err != nil {
		t.Fatal(err)
	}
	f := c.wait(t, 2*time.Second, "ended", ended(coordID)).(model.SessionFact)
	if f.Backfill {
		t.Fatal("a session that ended during the run is marked as backfill")
	}
}

// Without a Claude Code folder there is nothing to read: BackfillDone at once.
func TestBackfillDoneWithoutClaudeCode(t *testing.T) {
	a := newAdapter(t, Options{Home: t.TempDir()})
	run(t, a).wait(t, 2*time.Second, "done without Claude Code", backfillDone)
}

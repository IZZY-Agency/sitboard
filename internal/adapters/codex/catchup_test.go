package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Catch-up of the Codex sessions that had ended before sitboard started
// (#114): their token records are read once, bounded, and their days go into
// the Codex totals as Backfill facts, which are never cards.

// The fork fixture's thread, and its tokens: the fork's own two responses.
const idFork = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

var forkTokens = model.Tokens{Input: 10000 - 4000, CacheRead: 4000, Output: 300}

// catchup runs one catch-up the way Run does, in the test's own goroutine.
func (e *env) catchup() catchupResult {
	e.t.Helper()
	e.rescan()
	ctx := context.Background()
	older, cut := e.a.olderRollouts(ctx, firstDisplayedMidnight(fixedNow))
	res := readCatchup(ctx, e.r.planCatchup(older), fixedNow)
	res.cut = res.cut || cut
	e.r.applyCatchup(res)
	for e.r.publishCatchup() {
	}
	return res
}

// withCatchupLimits sets the bounds for one test.
func withCatchupLimits(t *testing.T, files int, fileBytes int64) {
	t.Helper()
	of, ob := catchupMaxFiles, catchupFileCap
	catchupMaxFiles, catchupFileCap = files, fileBytes
	t.Cleanup(func() { catchupMaxFiles, catchupFileCap = of, ob })
}

// recLine is a token_usage_record line of thread id with one response: its
// usage is also the thread's running total.
func recLine(id, ts string, in, cached, out int) string {
	u := fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":0,"output_tokens":%d,"reasoning_output_tokens":0,"total_tokens":%d}`, in, cached, out, in+out)
	return fmt.Sprintf(`{"timestamp":%q,"type":"token_usage_record","payload":{"thread_id":%q,"usage":%s,"thread_token_usage":%s}}`, ts, id, u, u)
}

// metaLine is the first line of a terminal thread's rollout.
func threadMeta(id, originator, src string) string {
	return `{"type":"session_meta","payload":{"id":"` + id + `","timestamp":"2026-11-20T11:00:00.000Z","cwd":"/home/dev/projects/alpha","originator":"` + originator + `","cli_version":"0.171.0","source":` + src + `}}`
}

func (e *env) addLines(id string, mod time.Time, lines ...string) {
	e.t.Helper()
	e.addBytes(id, []byte(strings.Join(lines, "\n")+"\n"), mod)
}

// The ended terminal session of the 0.171 fixture is counted from its
// records: the same total and the same quarter hours a live read of the same
// rollout gives, the thread named in the other thread's record left out.
func TestCatchupCountsAnEndedTerminalSession(t *testing.T) {
	live := newEnv(t)
	live.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	live.lock(idWorker, 4242)
	live.rescan()
	want, _ := live.last(idWorker)

	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	res := e.catchup()
	if res.cut {
		t.Error("cut without a bound being reached")
	}
	f, ok := e.last(idWorker)
	if !ok {
		t.Fatal("no fact for the ended session")
	}
	if !f.Backfill || !f.Ended || f.Live != model.Of(false) {
		t.Errorf("backfill %v ended %v live %+v", f.Backfill, f.Ended, f.Live)
	}
	if f.Spend.Tokens != workerTokens || f.Spend.Tokens != want.Spend.Tokens {
		t.Errorf("total %+v, want %+v (live: %+v)", f.Spend.Tokens, workerTokens, want.Spend.Tokens)
	}
	got, wantSpans := spanTokens(f.Spend), spanTokens(want.Spend)
	if len(got) != 2 || got[at(11, 0)].Output != 300 {
		t.Errorf("spans %+v", got)
	}
	for k, v := range wantSpans {
		if got[k] != v {
			t.Errorf("span %v: %+v, live read %+v", k, got[k], v)
		}
	}
	if f.Folder != "/home/dev/projects/alpha" || f.Key.Tool != model.ToolCodex || f.Key.ID != idWorker {
		t.Errorf("folder %q key %+v", f.Folder, f.Key)
	}
	if len(f.TurnEnds) != 0 || len(f.CreatedPRs) != 0 {
		t.Errorf("a catch-up fact carries turns or pull requests: %+v %+v", f.TurnEnds, f.CreatedPRs)
	}
}

// A `codex exec` thread counts too, from an older Codex (0.166); it is not a
// card and keeps its headless mark.
func TestCatchupCountsAnEndedExecThread(t *testing.T) {
	e := newEnv(t)
	e.addBytes(idOldExec, fixtureAt(t, "0.166.0", "exec-spend.jsonl"), fixedNow.Add(-2*time.Hour))
	e.catchup()
	f, ok := e.last(idOldExec)
	if !ok || !f.Backfill || !f.Headless || f.Spend.Tokens != oldExecSpend {
		t.Fatalf("exec thread: %v %+v", ok, f)
	}
}

// Only the threads the board shows as sessions count: the desktop app's and
// the plugin's threads do not, even with records.
func TestCatchupLeavesOutDesktopAndPluginThreads(t *testing.T) {
	e := newEnv(t)
	base := string(fixtureAt(t, "0.171.0", "spend.jsonl"))
	desk := strings.ReplaceAll(strings.ReplaceAll(base, idWorker, idDesktop), `"originator":"codex-tui"`, `"originator":"Codex Desktop"`)
	plug := strings.ReplaceAll(strings.ReplaceAll(base, idWorker, idExec), `"originator":"codex-tui"`, `"originator":"Claude Code"`)
	e.addBytes(idDesktop, []byte(desk), fixedNow.Add(-time.Hour))
	e.addBytes(idExec, []byte(plug), fixedNow.Add(-time.Hour))
	res := e.catchup()
	if len(res.reads) != 0 {
		t.Errorf("read %d threads that are not sessions", len(res.reads))
	}
	for _, id := range []string{idDesktop, idExec} {
		if _, ok := e.last(id); ok {
			t.Errorf("%s: a fact for a thread that is not a session", id)
		}
	}
}

// Spawned threads count under their root when the root counts, a grandchild
// too, and are not facts of their own.
func TestCatchupCountsSpawnedThreadsUnderTheirRoot(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.addBytes(idChild, fixtureAt(t, "0.171.0", "spend-child.jsonl"), fixedNow.Add(-40*time.Minute))
	grand := strings.ReplaceAll(string(fixtureAt(t, "0.171.0", "spend-child.jsonl")), idChild, idGrand)
	grand = strings.ReplaceAll(grand, idWorker, idChild)
	e.addBytes(idGrand, []byte(grand), fixedNow.Add(-41*time.Minute))
	e.catchup()
	f, ok := e.last(idWorker)
	want := workerTokens.Add(childTokens).Add(childTokens)
	if !ok || f.Spend.Tokens != want {
		t.Fatalf("root %+v, want %+v", f.Spend.Tokens, want)
	}
	if got := spanTokens(f.Spend)[at(11, 0)]; got.Output != 300+100+100 {
		t.Errorf("11:00 span %+v: the children's use is not in the days", got)
	}
	for _, id := range []string{idChild, idGrand} {
		if _, ok := e.last(id); ok {
			t.Errorf("%s: a spawned thread has a fact of its own", id)
		}
	}
}

// A thread spawned under a root that does not count (the desktop app's) does
// not count either.
func TestCatchupLeavesOutThreadsSpawnedUnderAnUncountedRoot(t *testing.T) {
	e := newEnv(t)
	e.add("desktop.jsonl", idDesktop, fixedNow.Add(-time.Hour))
	child := strings.ReplaceAll(string(fixtureAt(t, "0.171.0", "spend-child.jsonl")), idWorker, idDesktop)
	e.addBytes(idChild, []byte(child), fixedNow.Add(-40*time.Minute))
	res := e.catchup()
	if len(res.reads) != 0 {
		t.Errorf("read %d threads", len(res.reads))
	}
	if _, ok := e.last(idChild); ok {
		t.Error("a child of the desktop app's thread has a fact")
	}
	if _, ok := e.last(idDesktop); ok {
		t.Error("the desktop app's thread has a fact")
	}
}

// A fork's rollout repeats its parent's session_meta and history. On this
// machine's 74 forks (0.166 to 0.171) that copy holds no token record, and
// the fork's first total equals its own first response, so nothing of the
// parent's use is counted twice (#114).
func TestCatchupOfAForkCountsOnlyItsOwnResponses(t *testing.T) {
	e := newEnv(t)
	e.addBytes(idFork, fixtureAt(t, "0.171.0", "fork-spend.jsonl"), fixedNow.Add(-time.Hour))
	e.catchup()
	f, ok := e.last(idFork)
	if !ok || f.Spend.Tokens != forkTokens {
		t.Fatalf("fork %v %+v, want %+v", ok, f.Spend.Tokens, forkTokens)
	}
	got := spanTokens(f.Spend)
	if len(got) != 2 || got[at(11, 0)] != (model.Tokens{Input: 3000, CacheRead: 1000, Output: 200}) || got[at(11, 30)] != (model.Tokens{Input: 3000, CacheRead: 3000, Output: 100}) {
		t.Errorf("spans %+v", got)
	}
}

// Should a later Codex copy the parent's records into the fork, they name
// the parent's thread and are left out.
func TestCatchupOfAForkLeavesOutACopiedRecordOfTheParent(t *testing.T) {
	parent := strings.Split(string(fixtureAt(t, "0.171.0", "spend.jsonl")), "\n")[3] // the parent's first record
	if !strings.Contains(parent, `"token_usage_record"`) || !strings.Contains(parent, idWorker) {
		t.Fatal("fixture changed")
	}
	lines := strings.Split(strings.TrimSpace(string(fixtureAt(t, "0.171.0", "fork-spend.jsonl"))), "\n")
	copied := append(append(append([]string{}, lines[:3]...), parent), lines[3:]...)
	e := newEnv(t)
	e.addLines(idFork, fixedNow.Add(-time.Hour), copied...)
	e.catchup()
	if f, ok := e.last(idFork); !ok || f.Spend.Tokens != forkTokens {
		t.Fatalf("fork %v %+v, want %+v", ok, f.Spend.Tokens, forkTokens)
	}
}

// A session seen live is never read by the catch-up: it counts through its
// own tailer.
func TestCatchupSkipsASessionSeenLive(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.lock(idWorker, 4242)
	e.rescan()
	res := e.catchup()
	if len(res.reads) != 0 {
		t.Errorf("read %d threads of a live session", len(res.reads))
	}
	if f, _ := e.last(idWorker); f.Backfill || !f.Live.OK || !f.Live.V {
		t.Errorf("live session: %+v", f)
	}
}

// A caught-up session later seen live is replaced by its live fact, whose
// spend is the whole rollout once, not added to the catch-up's: the same
// identity, and the kept fact is let go.
func TestCatchupSessionLaterSeenLiveCountsOnce(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.catchup()
	if f, _ := e.last(idWorker); !f.Backfill {
		t.Fatal("not caught up")
	}
	if o := e.r.obs[idWorker]; o == nil || o.fact == nil || !o.fact.Backfill {
		t.Fatal("the catch-up fact is not kept in the store")
	}
	if _, card := e.r.sent[idWorker]; card {
		t.Fatal("a caught-up session is a card")
	}
	e.lock(idWorker, 4242)
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || f.Backfill || !f.Live.V || f.Spend.Tokens != workerTokens {
		t.Fatalf("live: %v backfill %v spend %+v", ok, f.Backfill, f.Spend.Tokens)
	}
	if got := spanTokens(f.Spend); len(got) != 2 || got[at(11, 0)].Output != 300 {
		t.Errorf("spans %+v", got)
	}
	if o := e.r.obs[idWorker]; o != nil {
		t.Error("the catch-up entry stays after the session is live")
	}
	// It ends and its rollout is gone: the kept fact is the live one, and the
	// catch-up does not come back.
	e.unlock(idWorker)
	e.rescan()
	if err := os.Remove(e.paths[idWorker]); err != nil {
		t.Fatal(err)
	}
	e.rescan()
	f, ok = e.last(idWorker)
	if !ok || f.Backfill || !f.Ended || f.Spend.Tokens != workerTokens {
		t.Errorf("kept: %v %+v", ok, f)
	}
}

// Resuming a caught-up thread never lowers the totals (#114): the live read
// of a rollout that is shorter than the catch-up's (past the tailer's first
// read, say) carries the caught-up spend, and a caught-up child whose rollout
// is not in the scan any more keeps its use.
func TestCatchupResumeCarriesCaughtSpendAndChildren(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.addBytes(idChild, fixtureAt(t, "0.171.0", "spend-child.jsonl"), fixedNow.Add(-40*time.Minute))
	e.catchup()
	caught, _ := e.last(idWorker)
	if want := workerTokens.Add(childTokens); caught.Spend.Tokens != want {
		t.Fatalf("caught %+v, want %+v", caught.Spend.Tokens, want)
	}
	// The child's rollout leaves the scan; the root's now reads shorter than
	// before; the thread is live again.
	if err := os.Remove(e.paths[idChild]); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(fixtureAt(t, "0.171.0", "spend.jsonl"))), "\n")
	var short []string
	for _, l := range lines {
		if strings.Contains(l, `"token_usage_record"`) && strings.Contains(l, "T11:05") {
			continue // the first quarter hour's response is not read by the live tailer
		}
		short = append(short, l)
	}
	e.addLines(idWorker, fixedNow.Add(-time.Minute), short...)
	e.lock(idWorker, 4242)
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || f.Backfill || !f.Live.V {
		t.Fatalf("not live: %v %+v", ok, f)
	}
	if f.Spend.Tokens != caught.Spend.Tokens {
		t.Errorf("total %+v, caught up %+v", f.Spend.Tokens, caught.Spend.Tokens)
	}
	got, want := spanTokens(f.Spend), spanTokens(caught.Spend)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("span %v: %+v, caught up %+v", k, got[k], v)
		}
	}
}

// A session that turned live between the plan and the end of the read is not
// overwritten by the late catch-up result.
func TestCatchupResultForASessionThatTurnedLiveIsDropped(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.rescan()
	plan := e.r.planCatchup(nil)
	if len(plan.jobs) != 1 {
		t.Fatalf("plan %+v", plan)
	}
	e.lock(idWorker, 4242)
	e.rescan()
	e.r.applyCatchup(readCatchup(context.Background(), plan, fixedNow))
	if f, ok := e.last(idWorker); !ok || f.Backfill || !f.Live.V {
		t.Errorf("late catch-up overwrote the live fact: %v %+v", ok, f)
	}
}

// A caught-up session whose rollout leaves the scan (its date folder aged
// out, or it went) is not withdrawn: its use stays in the days until its own
// quarter hours age out (#114).
func TestCatchupFactStaysWhenTheRolloutGoesAndAgesOut(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.catchup()
	e.rescan() // the next scans keep it
	e.rescan()
	if f, ok := e.last(idWorker); !ok || !f.Backfill {
		t.Fatalf("lost between scans: %v %+v", ok, f)
	}
	if err := os.Remove(e.paths[idWorker]); err != nil {
		t.Fatal(err)
	}
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || !f.Backfill || f.Spend.Tokens != workerTokens || len(f.Spend.Spans) != 2 {
		t.Fatalf("after its rollout went: %v %+v", ok, f)
	}
	// Later, the first quarter hour is older than spendKeep: it goes, the second
	// stays, then the thread is withdrawn with the last.
	e.a.now = func() time.Time { return at(11, 0).Add(spendKeep + model.SpendSpanLength) }
	e.rescan()
	f, ok = e.last(idWorker)
	if !ok || len(f.Spend.Spans) != 1 || !f.Spend.Spans[0].At.Equal(at(11, 15)) {
		t.Fatalf("aged: %v %+v", ok, f.Spend.Spans)
	}
	e.a.now = func() time.Time { return at(11, 15).Add(spendKeep + model.SpendSpanLength) }
	e.rescan()
	if _, ok := e.last(idWorker); ok {
		t.Error("still there with every quarter hour aged out")
	}
	if e.r.obs[idWorker] != nil {
		t.Error("the entry stays")
	}
}

// A recent rollout of a thread known to have ended is caught up; one failed
// lookup of who holds the locks makes it look "recently active", but it is
// no card then: its spend stays and nothing is withdrawn. It is replaced only
// when a scan finds the thread live (#114).
func TestCatchupOfARecentEndedThreadSurvivesAFailedLookup(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-5*time.Minute))
	if err := os.MkdirAll(filepath.Join(e.root, "thread-writer-locks"), 0o755); err != nil { // the lookup works
		t.Fatal(err)
	}
	e.catchup()
	if f, ok := e.last(idWorker); !ok || !f.Backfill {
		t.Fatalf("not caught up: %v %+v", ok, f)
	}
	e.fail = true
	e.rescan()
	e.r.tailAll(context.Background())
	e.rescan()
	for _, x := range flatten(e.facts) {
		if g, ok := x.(model.SessionGone); ok && g.Key.ID == idWorker {
			t.Fatal("withdrawn by a failed lookup")
		}
	}
	if f, ok := e.last(idWorker); !ok || !f.Backfill || f.Spend.Tokens != workerTokens || len(f.Spend.Spans) != 2 {
		t.Fatalf("after the failed lookup: %v %+v", ok, f)
	}
	if _, card := e.r.sent[idWorker]; card {
		t.Fatal("a card for a thread known to have ended")
	}
	e.fail = false
	e.rescan()
	if f, ok := e.last(idWorker); !ok || !f.Backfill {
		t.Fatalf("after recovery: %v %+v", ok, f)
	}
	// Seen live by a scan: now the card, carrying what was caught up.
	e.lock(idWorker, 4242)
	e.rescan()
	if f, ok := e.last(idWorker); !ok || f.Backfill || !f.Live.V || f.Spend.Tokens != workerTokens {
		t.Fatalf("live: %v %+v", ok, f)
	}
}

// An ended thread whose rollout is in a day folder older than the scanned
// ones, resumed since the first displayed midnight, is caught up (Codex
// appends to the thread's first rollout). One last changed before that is not.
func TestCatchupFindsAResumedRolloutInAnOlderFolder(t *testing.T) {
	e := newEnv(t)
	e.addAt(14, "spend.jsonl", idWorker, fixedNow.Add(-26*time.Hour))
	e.addAt(15, "spend.jsonl", idFork, fixedNow.Add(-9*24*time.Hour))
	e.catchup()
	f, ok := e.last(idWorker)
	if !ok || !f.Backfill || f.Spend.Tokens != workerTokens {
		t.Fatalf("resumed thread of an old folder: %v %+v", ok, f)
	}
	if _, ok := e.last(idFork); ok {
		t.Error("a rollout last changed before the first displayed midnight was caught up")
	}
	// The scan that follows does not take it away (it is not in the scan).
	e.rescan()
	if f, ok := e.last(idWorker); !ok || !f.Backfill {
		t.Errorf("after a scan: %v %+v", ok, f)
	}
}

// The discovery of older rollouts is bounded: by the entries it lists, and it
// reports the cut.
func TestCatchupOlderDiscoveryBound(t *testing.T) {
	e := newEnv(t)
	e.addAt(14, "spend.jsonl", idWorker, fixedNow.Add(-26*time.Hour))
	cutoff := firstDisplayedMidnight(fixedNow)
	if files, cut := e.a.olderRollouts(context.Background(), cutoff); len(files) != 1 || cut {
		t.Fatalf("files %d cut %v", len(files), cut)
	}
	old := catchupWalkEntries
	catchupWalkEntries = 1
	t.Cleanup(func() { catchupWalkEntries = old })
	if files, cut := e.a.olderRollouts(context.Background(), cutoff); len(files) != 0 || !cut {
		t.Errorf("files %d cut %v, want none and a cut", len(files), cut)
	}
}

// A week that holds a clock change is longer than turnWindow (a 25-hour
// day): the first hour of the oldest displayed day is still scanned, and an
// older folder's rollout changed in it is found.
func TestCatchupWeekWithAClockChange(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no time zone data")
	}
	old := time.Local
	time.Local = berlin
	t.Cleanup(func() { time.Local = old })
	// Sat 31 Oct 2026, 23:30 Berlin (CET); the clocks went back on Sun 25 Oct,
	// so the week shown, from Sun 25 Oct 00:00 (CEST), is 7 days and 30 minutes.
	now := time.Date(2026, 10, 31, 22, 30, 0, 0, time.UTC)
	first := time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC)
	if got := firstDisplayedMidnight(now); !got.Equal(first) {
		t.Fatalf("first displayed midnight %v, want %v", got, first)
	}
	e := newEnv(t)
	e.a.now = func() time.Time { return now }
	put := func(day string, id string, mod time.Time) {
		dir := filepath.Join(e.root, "sessions", "2026", "10", day)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "rollout-2026-10-"+day+"T01-00-00-"+id+".jsonl")
		if err := os.WriteFile(p, fixture(t, "spend.jsonl"), 0o644); err != nil {
			t.Fatal(err)
		}
		e.touch(p, mod)
	}
	// In the oldest displayed day, changed 15 minutes after its midnight: older
	// than 168 hours, still in the week.
	put("25", idWorker, first.Add(15*time.Minute))
	// A rollout of an older folder, changed in that same first hour.
	put("20", idFork, first.Add(15*time.Minute))
	// And one changed before the first displayed midnight.
	put("20", idOldExec, first.Add(-15*time.Minute))
	var scanned []string
	for _, f := range e.a.rollouts() {
		scanned = append(scanned, f.id)
	}
	if !slices.Contains(scanned, idWorker) {
		t.Errorf("the first hour of the oldest displayed day is not scanned: %v", scanned)
	}
	files, _ := e.a.olderRollouts(context.Background(), firstDisplayedMidnight(now))
	var older []string
	for _, f := range files {
		older = append(older, f.id)
	}
	slices.Sort(older)
	if !slices.Equal(older, []string{idFork}) {
		t.Errorf("older rollouts %v, want just the one changed since the first midnight", older)
	}
}

// The catch-up facts go out in chunks, each one batch, and the last is
// followed by BackfillDone.
func TestCatchupPublishesInChunks(t *testing.T) {
	old := catchupChunk
	catchupChunk = 2
	t.Cleanup(func() { catchupChunk = old })
	e := newEnv(t)
	ids := []string{idWorker, idFork, idOldExec, idExec, idDesktop}
	for i, id := range ids {
		e.addBytes(id, []byte(strings.ReplaceAll(string(fixtureAt(t, "0.171.0", "spend.jsonl")), idWorker, id)), fixedNow.Add(-time.Duration(i+1)*time.Hour))
	}
	e.rescan()
	e.facts = nil
	e.r.applyCatchup(readCatchup(context.Background(), e.r.planCatchup(nil), fixedNow))
	var sizes []int
	for {
		more := e.r.publishCatchup()
		b, ok := e.facts[len(e.facts)-1].(model.Batch)
		if !ok {
			t.Fatalf("published %T, want a batch", e.facts[len(e.facts)-1])
		}
		sizes = append(sizes, len(b.Facts))
		if !more {
			break
		}
	}
	if !slices.Equal(sizes, []int{2, 2, 1}) {
		t.Errorf("batch sizes %v, want 2 2 1", sizes)
	}
	// Run publishes them all and still says done.
	facts := runUntilBackfillDone(t, e.a)
	n := 0
	for _, f := range flatten(facts) {
		if sf, ok := f.(model.SessionFact); ok && sf.Backfill {
			n++
		}
	}
	if n < 1 {
		t.Error("Run published no catch-up fact")
	}
}

// Rollouts untouched for more than the window are not read; use older than
// spendKeep inside a recent one is dropped, the thread's total is not.
func TestCatchupWindow(t *testing.T) {
	e := newEnv(t)
	e.addLines(idWorker, fixedNow.Add(-turnWindow-time.Hour), threadMeta(idWorker, "codex-tui", `"vscode"`),
		recLine(idWorker, "2026-11-12T10:00:00.000Z", 1000, 0, 10))
	old := "bbbbbbbb-1111-4111-8111-bbbbbbbbbbbb"
	e.addLines(old, fixedNow.Add(-time.Hour), threadMeta(old, "codex-tui", `"vscode"`),
		recLine(old, "2026-11-10T10:00:00.000Z", 1000, 0, 10),
		recLine(old, "2026-11-19T10:00:00.000Z", 2000, 0, 20))
	e.catchup()
	if _, ok := e.last(idWorker); ok {
		t.Error("a rollout older than the window was read")
	}
	f, ok := e.last(old)
	if !ok || len(f.Spend.Spans) != 1 || f.Spend.Spans[0].Tokens.Output != 20 {
		t.Fatalf("spans: %v %+v", ok, f.Spend.Spans)
	}
}

// Bound: the number of files. The newest are read; the cut is reported.
func TestCatchupFileBound(t *testing.T) {
	withCatchupLimits(t, 1, 1<<20)
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.addBytes(idOldExec, fixtureAt(t, "0.166.0", "exec-spend.jsonl"), fixedNow.Add(-5*time.Hour))
	res := e.catchup()
	if !res.cut {
		t.Error("the bound cut nothing")
	}
	if _, ok := e.last(idWorker); !ok {
		t.Error("the newest rollout was not read")
	}
	if _, ok := e.last(idOldExec); ok {
		t.Error("the oldest rollout was read past the bound")
	}
}

// Bound: bytes per file. A rollout larger than the cap is left out whole, not
// half read (a total from a head-less file would be wrong); the others count.
func TestCatchupFileSizeBound(t *testing.T) {
	e := newEnv(t)
	e.addBytes(idOldExec, fixtureAt(t, "0.166.0", "exec-spend.jsonl"), fixedNow.Add(-5*time.Hour))
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	small := int64(len(fixtureAt(t, "0.166.0", "exec-spend.jsonl")) + 1)
	withCatchupLimits(t, 100, small)
	res := e.catchup()
	if !res.cut {
		t.Error("the bound cut nothing")
	}
	if _, ok := e.last(idWorker); ok {
		t.Error("a file over the bound was read")
	}
	if f, ok := e.last(idOldExec); !ok || f.Spend.Tokens != oldExecSpend {
		t.Errorf("the file within the bound: %v %+v", ok, f.Spend.Tokens)
	}
}

// Bound: time. A read whose time is up reads nothing more and reports the cut;
// what it read before stays.
func TestCatchupTimeBound(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.rescan()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := readCatchup(ctx, e.r.planCatchup(nil), fixedNow)
	if !res.cut || len(res.reads) != 0 {
		t.Errorf("cut %v reads %d", res.cut, len(res.reads))
	}
}

// Run: the catch-up comes after the first scan and ready, as Backfill facts,
// then BackfillDone, once.
func TestRunCatchesUpAndSaysDone(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	facts := runUntilBackfillDone(t, e.a)
	var order []string
	for _, f := range flatten(facts) {
		switch f := f.(type) {
		case model.SourceReady:
			order = append(order, "ready")
		case model.SessionFact:
			if f.Backfill && f.Key.ID == idWorker && f.Spend.Tokens == workerTokens {
				order = append(order, "caught")
			}
		case model.BackfillDone:
			if f.Kind != string(source.KindCodex) {
				t.Errorf("kind %q", f.Kind)
			}
			order = append(order, "done")
		}
	}
	if strings.Join(order, " ") != "ready caught done" {
		t.Errorf("order %v", order)
	}
}

// With no Codex folder there is nothing to read: ready and done at once.
func TestRunWithoutCodexSaysDone(t *testing.T) {
	a := newAdapter(t, Options{CodexHome: t.TempDir() + "/none", Now: func() time.Time { return fixedNow }})
	facts := runUntilBackfillDone(t, a)
	if len(facts) != 2 {
		t.Fatalf("facts %v", facts)
	}
	if _, ok := facts[0].(model.SourceReady); !ok {
		t.Errorf("first %T", facts[0])
	}
}

// A catch-up whose time was up before it began reads nothing and still says
// done, so the page's pending mark ends.
func TestRunCatchupOutOfTimeStillSaysDone(t *testing.T) {
	old := catchupBudget
	catchupBudget = -time.Second
	t.Cleanup(func() { catchupBudget = old })
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	for _, f := range flatten(runUntilBackfillDone(t, e.a)) {
		if sf, ok := f.(model.SessionFact); ok && sf.Backfill {
			t.Errorf("a fact from a catch-up out of time: %+v", sf)
		}
	}
}

// runUntilBackfillDone runs the adapter and collects what it emits up to and
// including BackfillDone, then stops it.
func runUntilBackfillDone(t *testing.T, a *adapter) []model.Fact {
	t.Helper()
	a.tick, a.liveTick = time.Millisecond, time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan model.Fact, 1024)
	errc := make(chan error, 1)
	go func() { errc <- a.Run(ctx, func(f model.Fact) { ch <- f }) }()
	var out []model.Fact
	for {
		select {
		case f := <-ch:
			out = append(out, f)
			if _, ok := f.(model.BackfillDone); ok {
				cancel()
				if err := <-errc; err != nil {
					t.Errorf("Run = %v", err)
				}
				return out
			}
		case <-time.After(30 * time.Second): // only a failure ends up here
			cancel()
			t.Fatalf("no BackfillDone; got %d facts", len(out))
		}
	}
}

// With no way to see lock holders, an old rollout is still "shown" between
// two scans (liveness unknown); the tail pass must not replace its catch-up
// fact with a card's, nor may the next scan withdraw it.
func TestCatchupOfUnknownLivenessSurvivesTailAndScan(t *testing.T) {
	e := newEnv(t)
	e.fail = true
	e.add("spend.jsonl", idWorker, fixedNow.Add(-2*time.Hour))
	e.catchup()
	if f, ok := e.last(idWorker); !ok || !f.Backfill {
		t.Fatalf("not caught up: %v %+v", ok, f)
	}
	e.r.tailAll(context.Background())
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || !f.Backfill || !f.Ended || f.Spend.Tokens != workerTokens {
		t.Errorf("after a tail pass and a scan: %v %+v", ok, f)
	}
	if _, card := e.r.sent[idWorker]; card {
		t.Error("a card replaced the catch-up fact")
	}
	if o := e.r.obs[idWorker]; o == nil || o.fact == nil {
		t.Error("the catch-up fact left the store")
	}
}

// With liveness unknown, a rollout changed in the last few minutes may be a
// running session (it is shown as a card): it is not caught up.
func TestCatchupLeavesARecentRolloutOfUnknownLiveness(t *testing.T) {
	e := newEnv(t)
	e.fail = true
	e.add("spend.jsonl", idWorker, fixedNow.Add(-5*time.Minute))
	res := e.catchup()
	if len(res.reads) != 0 {
		t.Errorf("read %d threads of a possibly running session", len(res.reads))
	}
	if f, ok := e.last(idWorker); !ok || f.Backfill || f.Live.OK {
		t.Errorf("fact %v %+v; want the card with liveness unknown", ok, f)
	}
}

// An older rollout changed a few minutes ago is an ended session when the
// last lookup of the lock holders worked (nobody holds it), and may be one in
// use when it did not.
func TestCatchupOlderRolloutRecentOnlyCountsWithAWorkingLookup(t *testing.T) {
	for _, works := range []bool{true, false} {
		e := newEnv(t)
		if err := os.MkdirAll(filepath.Join(e.root, "thread-writer-locks"), 0o755); err != nil {
			t.Fatal(err)
		}
		e.fail = !works
		e.addAt(14, "spend.jsonl", idWorker, fixedNow.Add(-5*time.Minute))
		e.catchup()
		if _, ok := e.last(idWorker); ok != works {
			t.Errorf("lookup works %v: caught up %v", works, ok)
		}
	}
}

// A caught-up thread with no quarter hour left in the days is not published:
// it would count nothing and only occupy the store.
func TestCatchupLeavesOutAThreadWithNoUseInTheDays(t *testing.T) {
	e := newEnv(t)
	e.addLines(idWorker, fixedNow.Add(-time.Hour), threadMeta(idWorker, "codex-tui", `"vscode"`),
		recLine(idWorker, "2026-11-01T10:00:00.000Z", 1000, 0, 10))
	e.catchup()
	if _, ok := e.last(idWorker); ok {
		t.Error("a fact for a thread with no use in the days")
	}
}

// The store holds as many caught-up threads as the catch-up may bring: none
// of them is let go by the bound.
func TestCatchupFitsTheStore(t *testing.T) {
	e := newEnv(t)
	e.r.obs = map[string]*observed{}
	n := catchupMaxFiles + 100
	for i := range n {
		id := fmt.Sprintf("%08x-0000-4000-8000-000000000000", i)
		e.r.queue = append(e.r.queue, caughtItem{id: id, caughtRoot: caughtRoot{meta: meta{ID: id, Cwd: "/x"}, mod: fixedNow}, parts: map[string]*derived{id: {Spans: map[time.Time]model.Tokens{at(11, 0): {Output: 1}}}}})
	}
	for e.r.publishCatchup() {
	}
	if len(e.r.obs) != n {
		t.Errorf("%d of %d kept", len(e.r.obs), n)
	}
	for _, f := range e.facts {
		if _, ok := f.(model.SessionGone); ok {
			t.Fatal("a caught-up thread was let go")
		}
	}
}

// A root that turned live after it was queued, between two chunks, is not
// published as caught up: the card stands.
func TestCatchupQueuedRootThatTurnedLiveIsNotPublished(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.rescan()
	e.r.applyCatchup(readCatchup(context.Background(), e.r.planCatchup(nil), fixedNow))
	e.lock(idWorker, 4242)
	e.rescan()
	e.r.publishCatchup()
	if f, ok := e.last(idWorker); !ok || f.Backfill || !f.Live.V {
		t.Errorf("the card was overwritten: %v %+v", ok, f)
	}
	if e.r.obs[idWorker] != nil {
		t.Error("a caught-up entry for a live session")
	}
}

// An older rollout of a thread the scan already knows (found through its held
// lock) is not planned: the scan's view of it stands.
func TestCatchupPlanLeavesAnOlderRolloutTheScanKnows(t *testing.T) {
	e := newEnv(t)
	e.addAt(14, "spend.jsonl", idWorker, fixedNow.Add(-26*time.Hour))
	e.lock(idWorker, 4242)
	e.rescan()
	older, _ := e.a.olderRollouts(context.Background(), firstDisplayedMidnight(fixedNow))
	if len(older) != 1 {
		t.Fatalf("older %d", len(older))
	}
	if plan := e.r.planCatchup(older); len(plan.jobs) != 0 {
		t.Errorf("planned %d jobs for a live session", len(plan.jobs))
	}
}

// A thread spawned under a caught-up root is not followed by the live path
// while the root is not a card, even with liveness unknown.
func TestCatchupSpawnedThreadOfACaughtRootIsNotFollowed(t *testing.T) {
	e := newEnv(t)
	e.fail = true
	e.add("spend.jsonl", idWorker, fixedNow.Add(-2*time.Hour))
	e.addBytes(idChild, fixtureAt(t, "0.171.0", "spend-child.jsonl"), fixedNow.Add(-2*time.Hour))
	e.catchup()
	delete(e.r.obs, idChild) // what the scan before the catch-up saw of it
	e.r.tailAll(context.Background())
	e.rescan()
	if o := e.r.obs[idChild]; o != nil {
		t.Error("the spawned thread of a caught-up root is read live")
	}
}

// Between two scans the tail pass follows only what the last scan made a card:
// an old rollout of unknown liveness is "shown" by that alone, and must not
// become a card before the catch-up has published it (nor flicker as one).
func TestTailPassLeavesAnOldRolloutOfUnknownLivenessAlone(t *testing.T) {
	e := newEnv(t)
	e.fail = true
	e.add("spend.jsonl", idWorker, fixedNow.Add(-2*time.Hour))
	e.rescan()
	e.r.tailAll(context.Background())
	if f, ok := e.last(idWorker); ok {
		t.Fatalf("a card from the tail pass: %+v", f)
	}
}

// The carry is per thread: a caught-up child whose rollout is not in the scan
// any more adds to the root's own use in the same quarter hour. Merging the
// root and the child before taking the maximum would show 400 where the root
// used 305 and the child 100.
func TestCatchupResumeCarriesPerThreadInTheSameQuarterHour(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.addBytes(idChild, fixtureAt(t, "0.171.0", "spend-child.jsonl"), fixedNow.Add(-40*time.Minute))
	e.catchup()
	if err := os.Remove(e.paths[idChild]); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(fixtureAt(t, "0.171.0", "spend.jsonl"))), "\n")
	// The root adds 5 output tokens in the quarter hour the child used 100 in.
	lines = append(lines, recLine(idWorker, "2026-11-20T11:07:00.000Z", 0, 0, 5))
	e.addLines(idWorker, fixedNow.Add(-time.Minute), lines...)
	e.lock(idWorker, 4242)
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || f.Backfill || !f.Live.V {
		t.Fatalf("not live: %v %+v", ok, f)
	}
	if got := spanTokens(f.Spend)[at(11, 0)].Output; got != 300+5+childTokens.Output {
		t.Errorf("output in the quarter hour %d, want %d", got, 300+5+childTokens.Output)
	}
}

// A root queued for publishing that resumes, accrues spend and leaves the scan
// between two chunks keeps the live fact the scan kept: the stale history is
// not published over it.
func TestCatchupQueuedRootThatResumedAndLeftTheScanKeepsItsLiveFact(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.rescan()
	e.r.applyCatchup(readCatchup(context.Background(), e.r.planCatchup(nil), fixedNow))
	lines := strings.Split(strings.TrimSpace(string(fixtureAt(t, "0.171.0", "spend.jsonl"))), "\n")
	lines = append(lines, recLine(idWorker, "2026-11-20T11:07:00.000Z", 0, 0, 5))
	e.addLines(idWorker, fixedNow.Add(-time.Minute), lines...)
	e.lock(idWorker, 4242)
	e.rescan()
	if err := os.Remove(e.paths[idWorker]); err != nil {
		t.Fatal(err)
	}
	delete(e.held, idWorker)
	e.rescan()
	o := e.r.obs[idWorker]
	if o == nil || o.fact == nil || o.fact.Backfill {
		t.Fatalf("the scan kept no live fact: %+v", o)
	}
	e.r.publishCatchup()
	o = e.r.obs[idWorker]
	if o == nil || o.fact == nil || o.fact.Backfill {
		t.Fatalf("the stale history replaced the live fact: %+v", o)
	}
	if got := spanTokens(o.fact.Spend)[at(11, 0)].Output; got != 305 {
		t.Errorf("the resumed spend is %d, want 305", got)
	}
}

// addAtLines is a rollout of thread id with the lines given, in the day folder
// of days days ago, changed at mod.
func (e *env) addAtLines(days int, id string, mod time.Time, lines ...string) {
	e.t.Helper()
	d := fixedNow.Local().AddDate(0, 0, -days)
	dir := filepath.Join(e.root, "sessions", d.Format("2006"), d.Format("01"), d.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-2026-11-06T11-00-00-"+id+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.touch(p, mod)
	e.paths[id] = p
}

func (e *env) addOld(days int, id string, mod time.Time) {
	e.t.Helper()
	e.addAtLines(days, id, mod, threadMeta(id, "codex-tui", `"cli"`), recLine(id, "2026-11-20T11:05:00.000Z", 1000, 0, 10))
}

// A limit reached inside the last day folder keeps what was found before it,
// says it cut, and the catch-up reads what was found: the directory is read in
// chunks, the budget checked before each.
func TestCatchupOlderDiscoveryLimitInsideTheLastFolderKeepsWhatWasFound(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{idWorker, idFork, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"} {
		e.addOld(14, id, fixedNow.Add(-26*time.Hour))
	}
	oc, ow := catchupDirChunk, catchupWalkEntries
	t.Cleanup(func() { catchupDirChunk, catchupWalkEntries = oc, ow })
	// the year, the month and the day, then two of the three files
	catchupDirChunk, catchupWalkEntries = 1, 5
	files, cut := e.a.olderRollouts(context.Background(), firstDisplayedMidnight(fixedNow))
	if len(files) != 2 || !cut {
		t.Fatalf("files %d cut %v, want 2 and a cut", len(files), cut)
	}
	e.rescan()
	if plan := e.r.planCatchup(files); len(plan.jobs) != 2 {
		t.Errorf("planned %d jobs, want the 2 found", len(plan.jobs))
	}
}

// A walk that used up its own time leaves the read its own: the rollouts of
// the scanned days are caught up all the same.
func TestRunReadsAfterTheDiscoveryRanOutOfTime(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	old := catchupDiscoveryBudget
	catchupDiscoveryBudget = time.Nanosecond
	t.Cleanup(func() { catchupDiscoveryBudget = old })
	var caught bool
	for _, f := range flatten(runUntilBackfillDone(t, e.a)) {
		if f, ok := f.(model.SessionFact); ok && f.Backfill && f.Key.ID == idWorker && f.Spend.Tokens == workerTokens {
			caught = true
		}
	}
	if !caught {
		t.Error("the read of the scanned days was cut with the discovery's time")
	}
}

// The older rollouts are chosen by modification time, newest first, not in
// the order of their folders and names: with room for one, the more recently
// changed one is read, whichever way the names run.
func TestCatchupOlderRolloutsAreChosenNewestFirst(t *testing.T) {
	const lo, hi = "00000000-0000-4000-8000-000000000000", "ffffffff-ffff-4fff-8fff-ffffffffffff"
	for _, newer := range []string{lo, hi} {
		older := lo
		if newer == lo {
			older = hi
		}
		e := newEnv(t)
		e.addOld(14, older, fixedNow.Add(-50*time.Hour))
		e.addOld(15, newer, fixedNow.Add(-26*time.Hour))
		withCatchupLimits(t, 1, catchupFileCap)
		e.catchup()
		if _, ok := e.last(newer); !ok {
			t.Errorf("the newer rollout (%s...) was left out", newer[:2])
		}
		if _, ok := e.last(older); ok {
			t.Errorf("the older rollout (%s...) took the room", older[:2])
		}
	}
}

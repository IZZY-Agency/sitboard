package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// idOldExec is the `codex exec` thread of the 0.166.0 fixture.
const idOldExec = "99999999-9999-4999-8999-999999999999"

// The token figures of the fixtures (invented): the worker's three responses
// (the third one the compaction call that token_count never counts), the
// spawned child's one, and the 0.166 exec thread's two.
var (
	workerTokens = model.Tokens{Input: 60000 - 27000, CacheRead: 27000, Output: 2800}
	childTokens  = model.Tokens{Input: 1000, CacheRead: 3000, Output: 100}
	oldExecSpend = model.Tokens{Input: 15000 - 13500, CacheRead: 13500, Output: 450}
)

func fixtureAt(t *testing.T, version, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fixtures", "codex", version, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func at(hh, mm int) time.Time { return time.Date(2026, 11, 20, hh, mm, 0, 0, time.UTC) }

func spanTokens(s model.Spend) map[time.Time]model.Tokens {
	out := map[time.Time]model.Tokens{}
	for _, sp := range s.Spans {
		if sp.USD != 0 || sp.Priced {
			panic("a Codex span has a price")
		}
		out[sp.At] = sp.Tokens
	}
	return out
}

// A terminal thread's spend is its last token_usage_record's thread total,
// which counts the compaction call that token_count leaves out; each
// record's own usage lands in its quarter hour. Codex keeps no price, so the
// figure is tokens only.
func TestCodexSpendIsTheRecordsThreadTotal(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.lock(idWorker, 4242)
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok {
		t.Fatal("no session")
	}
	if f.Spend.Tokens != workerTokens {
		t.Errorf("tokens %+v, want %+v", f.Spend.Tokens, workerTokens)
	}
	if f.Spend.USD != 0 || f.Spend.Priced || f.Spend.Recorded {
		t.Errorf("a Codex figure has a price: %+v", f.Spend)
	}
	spans := spanTokens(f.Spend)
	want := map[time.Time]model.Tokens{
		at(11, 0):  {Input: 2000, CacheRead: 8000, Output: 300},
		at(11, 15): {Input: 1000 + 30000, CacheRead: 19000, Output: 2500},
	}
	if len(spans) != len(want) {
		t.Fatalf("spans %+v, want %+v", spans, want)
	}
	for k, v := range want {
		if spans[k] != v {
			t.Errorf("span %v: %+v, want %+v", k, spans[k], v)
		}
	}
}

// The 0.166 shape is the same: a `codex exec` thread from that version counts too.
func TestCodexSpendFrom0166Exec(t *testing.T) {
	e := newEnv(t)
	e.addBytes(idOldExec, fixtureAt(t, "0.166.0", "exec-spend.jsonl"), fixedNow.Add(-2*time.Hour))
	e.lock(idOldExec, 5151)
	e.rescan()
	f, ok := e.last(idOldExec)
	if !ok || f.Spend.Tokens != oldExecSpend {
		t.Fatalf("exec: %v %+v, want %+v", ok, f.Spend.Tokens, oldExecSpend)
	}
	if len(f.Spend.Spans) != 2 {
		t.Errorf("spans %+v, want two quarter hours", f.Spend.Spans)
	}
}

// A record that carries another thread's id (a fork's copy of its parent's
// history) is never counted for this thread.
func TestCodexSpendSkipsAnotherThreadsRecord(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.lock(idWorker, 4242)
	e.rescan()
	f, _ := e.last(idWorker)
	for _, sp := range f.Spend.Spans {
		if sp.At.Equal(at(11, 15)) && sp.Tokens.Output > 2500 {
			t.Errorf("the other thread's record landed in a span: %+v", sp)
		}
	}
	if f.Spend.Tokens.Output != workerTokens.Output {
		t.Errorf("output %d, want %d", f.Spend.Tokens.Output, workerTokens.Output)
	}
}

// A thread Codex spawned counts on the card at the top of its chain, a
// grandchild too, and its later use reaches that card; it never becomes a
// session of its own.
func TestCodexSpendIncludesSpawnedThreads(t *testing.T) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.addBytes(idChild, fixtureAt(t, "0.171.0", "spend-child.jsonl"), fixedNow.Add(-40*time.Minute))
	grand := strings.ReplaceAll(string(fixtureAt(t, "0.171.0", "spend-child.jsonl")), idChild, idGrand)
	grand = strings.ReplaceAll(grand, idWorker, idChild)
	e.addBytes(idGrand, []byte(grand), fixedNow.Add(-41*time.Minute))
	e.lock(idWorker, 4242)
	e.rescan()
	want := workerTokens.Add(childTokens).Add(childTokens)
	f, _ := e.last(idWorker)
	if f.Spend.Tokens != want {
		t.Fatalf("worker tokens %+v, want %+v (own, child, grandchild)", f.Spend.Tokens, want)
	}
	if got := spanTokens(f.Spend)[at(11, 0)]; got.Output != 300+100+100 {
		t.Errorf("11:00 span %+v: the spawned threads' use is not in it", got)
	}
	for _, id := range []string{idChild, idGrand} {
		if _, ok := e.last(id); ok {
			t.Errorf("spawned thread %s became a session", id)
		}
	}
	// The child goes on working: the next read of the rollouts carries it up.
	more := model.Tokens{Input: 500, CacheRead: 0, Output: 50}
	e.appendLines(idChild, fmt.Sprintf(`{"timestamp":"2026-11-20T11:50:00.000Z","type":"token_usage_record","payload":{"thread_id":%q,"session_id":%q,"usage":{"input_tokens":500,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":0,"total_tokens":550},"thread_token_usage":{"input_tokens":4500,"cached_input_tokens":3000,"cache_write_input_tokens":0,"output_tokens":150,"reasoning_output_tokens":40,"total_tokens":4650}}}`, idChild, idWorker))
	e.r.tailAll(context.Background())
	f, _ = e.last(idWorker)
	if f.Spend.Tokens != want.Add(more) {
		t.Errorf("after the child grew: %+v, want %+v", f.Spend.Tokens, want.Add(more))
	}
}

// Spans older than 8 days are dropped; the thread's total stays whole.
func TestCodexSpendSpansKeep8Days(t *testing.T) {
	tl := newTailer(tailerFile(t, fixtureAt(t, "0.171.0", "spend.jsonl")))
	tl.id = idWorker
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if len(tl.d.Spans) != 2 {
		t.Fatalf("spans %v", tl.d.Spans)
	}
	later := at(11, 15).Add(spendKeep)
	if changed := tl.prune(later); !changed {
		t.Error("prune reported no change")
	}
	if len(tl.d.Spans) != 1 || tl.d.Spans[at(11, 15)] == (model.Tokens{}) {
		t.Errorf("after 8 days: %v, want only the 11:15 span", tl.d.Spans)
	}
	if tl.d.Total != workerTokens {
		t.Errorf("total %+v, want %+v", tl.d.Total, workerTokens)
	}
}

// A rollout read from the cap before its end still gets the whole thread
// total, from its last record; only the spans of what was read are there.
func TestCodexSpendTotalSurvivesTheReadCap(t *testing.T) {
	content := fixtureAt(t, "0.171.0", "spend.jsonl")
	p := tailerFile(t, content)
	tl := newTailer(p)
	tl.id = idWorker
	// Start the read after the first record: only the later ones are seen.
	cut := strings.Index(string(content), `"compacted"`)
	tl.cap = int64(len(content) - cut + 40)
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if tl.d.Total != workerTokens {
		t.Errorf("total %+v, want %+v", tl.d.Total, workerTokens)
	}
	if _, ok := tl.d.Spans[at(11, 0)]; ok {
		t.Errorf("a span from before the cap: %v", tl.d.Spans)
	}
}

// A record with a missing, negative or impossible count is dropped, never
// guessed; one without a usable time counts in the total, in no span.
func TestCodexSpendDropsBadRecords(t *testing.T) {
	good := `{"timestamp":"2026-11-20T11:05:00.000Z","type":"token_usage_record","payload":{"thread_id":"` + idWorker + `","usage":{"input_tokens":100,"cached_input_tokens":40,"cache_write_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0,"total_tokens":110},"thread_token_usage":{"input_tokens":100,"cached_input_tokens":40,"cache_write_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0,"total_tokens":110}}}`
	bad := []string{
		strings.ReplaceAll(good, `"output_tokens":10,`, ``),                                   // a count missing
		strings.ReplaceAll(good, `"output_tokens":10,`, `"output_tokens":-10,`),               // negative
		strings.ReplaceAll(good, `"cached_input_tokens":40,`, `"cached_input_tokens":4000,`),  // more cached than input
		strings.ReplaceAll(good, `"output_tokens":10,`, `"output_tokens":"10",`),              // not a number
		strings.ReplaceAll(good, `"thread_token_usage":{`, `"thread_token_usage":null,"x":{`), // no thread total
		strings.ReplaceAll(good, `"thread_id":"`+idWorker+`"`, `"thread_id":"`+idChild+`"`),   // another thread
	}
	for i, b := range bad {
		tl := newTailer(tailerFile(t, []byte(good+"\n"+strings.ReplaceAll(b, "100", "900")+"\n")))
		tl.id = idWorker
		if _, err := tl.update(context.Background(), fixedNow); err != nil {
			t.Fatal(err)
		}
		if tl.d.Total != (model.Tokens{Input: 60, CacheRead: 40, Output: 10}) {
			t.Errorf("bad record %d counted: total %+v", i, tl.d.Total)
		}
		if got := tl.d.Spans[at(11, 0)]; got != (model.Tokens{Input: 60, CacheRead: 40, Output: 10}) {
			t.Errorf("bad record %d counted in the span: %+v", i, got)
		}
	}
	noTime := strings.Replace(strings.ReplaceAll(good, "100", "300"), `"timestamp":"2026-11-20T11:05:00.000Z"`, `"timestamp":"soon"`, 1)
	tl := newTailer(tailerFile(t, []byte(good+"\n"+noTime+"\n")))
	tl.id = idWorker
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if tl.d.Total.Input != 260 || len(tl.d.Spans) != 1 || tl.d.Spans[at(11, 0)].Input != 60 {
		t.Errorf("record without a time: total %+v spans %v", tl.d.Total, tl.d.Spans)
	}
}

// A thread with no record has no spend at all: zero, not "zero tokens".
func TestCodexSpendIsZeroWithoutRecords(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.lock(idWorker, 4242)
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok {
		t.Fatal("no session: a thread without a record must still be one")
	}
	if !f.Spend.Tokens.IsZero() || len(f.Spend.Spans) != 0 {
		t.Errorf("spend %+v", f.Spend)
	}
}

// spawnedEnv is a worker with a child and a grandchild, all with records.
func spawnedEnv(t *testing.T) *env {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.addBytes(idChild, fixtureAt(t, "0.171.0", "spend-child.jsonl"), fixedNow.Add(-40*time.Minute))
	grand := strings.ReplaceAll(string(fixtureAt(t, "0.171.0", "spend-child.jsonl")), idChild, idGrand)
	grand = strings.ReplaceAll(grand, idWorker, idChild)
	e.addBytes(idGrand, []byte(grand), fixedNow.Add(-41*time.Minute))
	e.lock(idWorker, 4242)
	e.rescan()
	return e
}

// A parent's count never goes down: a spawned thread whose rollout left the
// scan keeps its last total and its spans on the card at the top of its
// chain, and a grandchild still counts there after its own parent's rollout
// went.
func TestCodexSpendOfASpawnedThreadStaysAfterItsRolloutGoes(t *testing.T) {
	e := spawnedEnv(t)
	want := workerTokens.Add(childTokens).Add(childTokens)
	if err := os.Remove(e.paths[idChild]); err != nil {
		t.Fatal(err)
	}
	e.rescan()
	f, _ := e.last(idWorker)
	if f.Spend.Tokens != want {
		t.Fatalf("after the child's rollout went: %+v, want %+v", f.Spend.Tokens, want)
	}
	if got := spanTokens(f.Spend)[at(11, 0)]; got.Output != 500 {
		t.Errorf("11:00 span %+v: the child's use left the days", got)
	}
	more := model.Tokens{Input: 500, Output: 50}
	e.appendLines(idGrand, fmt.Sprintf(`{"timestamp":"2026-11-20T11:50:00.000Z","type":"token_usage_record","payload":{"thread_id":%q,"usage":{"input_tokens":500,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":0,"total_tokens":550},"thread_token_usage":{"input_tokens":4500,"cached_input_tokens":3000,"cache_write_input_tokens":0,"output_tokens":150,"reasoning_output_tokens":40,"total_tokens":4650}}}`, idGrand))
	e.rescan()
	f, _ = e.last(idWorker)
	if f.Spend.Tokens != want.Add(more) {
		t.Errorf("the grandchild's new use: %+v, want %+v on the worker", f.Spend.Tokens, want.Add(more))
	}
}

// A session seen live keeps its spans in the days after its rollout left the
// scan (its date folder aged out, or it was archived): its last fact stays,
// ended, until its newest quarter hour is older than spendKeep.
func TestCodexSpendStaysInTheDaysAfterTheRolloutLeavesTheScan(t *testing.T) {
	e := newEnv(t)
	now := fixedNow
	e.a.now = func() time.Time { return now }
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.lock(idWorker, 4242)
	e.rescan()
	e.unlock(idWorker)
	if err := os.Remove(e.paths[idWorker]); err != nil {
		t.Fatal(err)
	}
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || !f.Ended || f.Spend.Tokens != workerTokens || len(f.Spend.Spans) != 2 {
		t.Fatalf("after the rollout left: sent %v ended %v spend %+v", ok, f.Ended, f.Spend)
	}
	now = at(11, 15).Add(spendKeep)
	e.rescan()
	if f, ok = e.last(idWorker); !ok || len(f.Spend.Spans) != 1 || f.Spend.Spans[0].At != at(11, 15) {
		t.Fatalf("a day later: sent %v spans %+v, want the 11:15 span alone", ok, f.Spend.Spans)
	}
	now = at(11, 30).Add(spendKeep)
	e.rescan()
	if _, ok = e.last(idWorker); ok {
		t.Error("still sent with no span left")
	}
}

// A card shown while liveness cannot be told (it may have ended before
// sitboard started) has its tokens but adds nothing to the days until it is
// seen live.
func TestCodexFallbackCardHasNoSpansUntilSeenLive(t *testing.T) {
	e := newEnv(t)
	e.fail = true
	e.add("spend.jsonl", idWorker, fixedNow.Add(-5*time.Minute))
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || f.Spend.Tokens != workerTokens || len(f.Spend.Spans) != 0 {
		t.Fatalf("fallback card: sent %v spend %+v, want tokens and no spans", ok, f.Spend)
	}
	e.fail = false
	e.lock(idWorker, 4242)
	e.rescan()
	if f, _ = e.last(idWorker); len(f.Spend.Spans) != 2 {
		t.Errorf("seen live: spans %+v", f.Spend.Spans)
	}
}

// A spawned thread is read for its token records only: nothing else of its
// rollout is parsed or kept.
func TestCodexSpawnedThreadIsReadForSpendOnly(t *testing.T) {
	e := spawnedEnv(t)
	c := e.r.sessions[idChild]
	if c == nil || c.t == nil {
		t.Fatal("the child is not followed")
	}
	if c.t.d.Total != childTokens {
		t.Errorf("child total %+v", c.t.d.Total)
	}
	if c.t.d.Status != (model.Known[model.Status]{}) || !c.t.d.LastActivity.IsZero() || c.t.d.Model != "" {
		t.Errorf("the child's other lines were read: %+v", c.t.d)
	}
}

// The store of what was observed: a spawned thread's entry stays while the
// card it counts on is known (its lifetime total) and goes once that card is
// gone and its spans aged out; past maxObserved, in the tail path too, the
// least recently used entries go first.
func TestObservedStoreIsBounded(t *testing.T) {
	e := newEnv(t)
	r := e.r
	r.obs = map[string]*observed{}
	r.sessions[idWorker] = &sess{metaOK: true}
	r.obs["kept"] = &observed{root: idWorker, total: childTokens}
	r.obs["orphan"] = &observed{root: "gone", total: childTokens}
	r.tendObserved(fixedNow)
	if r.obs["kept"] == nil || r.obs["orphan"] != nil {
		t.Fatalf("kept %v orphan %v", r.obs["kept"], r.obs["orphan"])
	}
	r.sessions[idChild] = &sess{metaOK: true}
	for i := range maxObserved + 1 {
		o := &observed{root: idChild}
		r.touch(o)
		r.obs[fmt.Sprintf("t%05d", i)] = o
	}
	// The oldest entry is read (the worker's card sums it): it is used again.
	r.obs["t00000"].root = idWorker
	r.spawnedSpend(idWorker)
	r.tailAll(context.Background())
	if len(r.obs) != maxObserved {
		t.Fatalf("%d entries after the tail path, want %d", len(r.obs), maxObserved)
	}
	// Read with it: "kept" (the worker's too). Least recently used: t00001, t00002.
	if r.obs["kept"] == nil || r.obs["t00000"] == nil || r.obs["t00001"] != nil || r.obs["t00002"] != nil || r.obs["t00003"] == nil {
		t.Error("not the least recently used let go")
	}
}

// retainedEnv: a worker seen live, then ended with its rollout gone from the
// scan, kept in the store. The rollout's bytes are returned for putting back.
func retainedEnv(t *testing.T) (*env, []byte) {
	e := newEnv(t)
	e.add("spend.jsonl", idWorker, fixedNow.Add(-30*time.Minute))
	e.lock(idWorker, 4242)
	e.rescan()
	e.unlock(idWorker)
	b, err := os.ReadFile(e.paths[idWorker])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.paths[idWorker]); err != nil {
		t.Fatal(err)
	}
	e.rescan()
	if f, ok := e.last(idWorker); !ok || !f.Ended || len(f.Spend.Spans) != 2 {
		t.Fatalf("not kept: %v %+v", ok, f)
	}
	return e, b
}

// A kept session whose rollout comes back (a resume) without a held lock is
// followed again as the ended session it was: it stays seen live, its spend
// stays in the days, and it still ages out.
func TestCodexKeptSessionReturnsUnlocked(t *testing.T) {
	e, b := retainedEnv(t)
	now := fixedNow
	e.a.now = func() time.Time { return now }
	e.addBytes(idWorker, b, fixedNow.Add(-20*time.Minute))
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || !f.Ended || f.Spend.Tokens != workerTokens || len(f.Spend.Spans) != 2 {
		t.Fatalf("returned unlocked: sent %v ended %v spend %+v", ok, f.Ended, f.Spend)
	}
	// Its rollout leaves again and the days age out: it is withdrawn.
	if err := os.Remove(e.paths[idWorker]); err != nil {
		t.Fatal(err)
	}
	now = at(11, 30).Add(spendKeep)
	e.rescan()
	if _, ok := e.last(idWorker); ok {
		t.Error("never withdrawn")
	}
}

// The same with liveness unknown: the fallback must not replace the kept
// spend with a figure that has no spans.
func TestCodexKeptSessionReturnsWithLivenessUnknown(t *testing.T) {
	e, b := retainedEnv(t)
	e.fail = true
	e.addBytes(idWorker, b, fixedNow.Add(-5*time.Minute))
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || f.Spend.Tokens != workerTokens || len(f.Spend.Spans) != 2 {
		t.Fatalf("returned, liveness unknown: sent %v spend %+v", ok, f.Spend)
	}
}

// A child whose rollout comes back keeps its saved contribution until its new
// read has parsed a valid token record: a failed or empty first read never
// makes the parent's count dip.
func TestCodexReturningChildNeverDipsTheParent(t *testing.T) {
	e := spawnedEnv(t)
	want := workerTokens.Add(childTokens).Add(childTokens)
	full, err := os.ReadFile(e.paths[idChild])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.paths[idChild]); err != nil {
		t.Fatal(err)
	}
	e.rescan()
	check := func(step string) {
		t.Helper()
		if f, _ := e.last(idWorker); f.Spend.Tokens != want {
			t.Fatalf("%s: worker %+v, want %+v", step, f.Spend.Tokens, want)
		}
	}
	check("child gone")
	// It comes back with its first line only (being written again) ...
	meta := full[:strings.IndexByte(string(full), '\n')+1]
	e.addBytes(idChild, meta, fixedNow.Add(-2*time.Minute))
	e.rescan()
	check("back, no record yet")
	// ... then grows while it cannot be read ...
	e.appendLines(idChild, strings.Split(strings.TrimSpace(string(full[len(meta):])), "\n")...)
	if err := os.Chmod(e.paths[idChild], 0); err != nil {
		t.Fatal(err)
	}
	e.r.tailAll(context.Background())
	check("first read failed")
	// ... and the retry reads its record.
	if err := os.Chmod(e.paths[idChild], 0o644); err != nil {
		t.Fatal(err)
	}
	e.r.tailAll(context.Background())
	check("retry")
}

// A kept session whose rollout comes back shorter than it was (being
// written again) keeps its kept spend; one that comes back unreadable is not
// followed, and its kept fact still ages out.
func TestCodexKeptSessionReturnsShortOrUnreadable(t *testing.T) {
	e, b := retainedEnv(t)
	meta := b[:strings.IndexByte(string(b), '\n')+1]
	e.addBytes(idWorker, meta, fixedNow.Add(-20*time.Minute))
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || f.Spend.Tokens != workerTokens || len(f.Spend.Spans) != 2 {
		t.Fatalf("returned short: sent %v spend %+v", ok, f.Spend)
	}

	e, _ = retainedEnv(t)
	now := fixedNow
	e.a.now = func() time.Time { return now }
	e.addBytes(idWorker, nil, fixedNow.Add(-20*time.Minute)) // no first line yet
	e.rescan()
	now = at(11, 30).Add(spendKeep)
	e.rescan()
	if _, ok := e.last(idWorker); ok {
		t.Error("an unreadable return took the kept fact's expiry")
	}
}

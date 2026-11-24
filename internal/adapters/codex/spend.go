package codex

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// Spend. Codex writes a token_usage_record
// line after every model response: the response's usage and the thread's
// running total. That total also counts the compaction call, which the
// token_count events leave out, so token_count is not read. Codex keeps no
// price anywhere on the machine: a Codex figure is tokens only, never money.

// spendKeep is how long a quarter hour of use is kept: the 7 local days the
// summary shows, plus a day so that a 25-hour day keeps its first hour.
const spendKeep = 8 * 24 * time.Hour

// tokenUsage is one of Codex's usage objects. Pointers tell a count that is
// missing from one that is 0.
type tokenUsage struct {
	Input      *int64 `json:"input_tokens"`
	Cached     *int64 `json:"cached_input_tokens"`
	CacheWrite *int64 `json:"cache_write_input_tokens"`
	Output     *int64 `json:"output_tokens"`
}

// tokens turns a usage object into sitboard's counts: Codex's input includes
// its cached input, sitboard's Input does not. ok is false for a count that is
// missing or negative, or more cached input than input.
func (u *tokenUsage) tokens() (model.Tokens, bool) {
	if u == nil || u.Input == nil || u.Cached == nil || u.Output == nil {
		return model.Tokens{}, false
	}
	cw := int64(0)
	if u.CacheWrite != nil {
		cw = *u.CacheWrite
	}
	in, cached, out := *u.Input, *u.Cached, *u.Output
	if in < 0 || cached < 0 || out < 0 || cw < 0 || cached > in {
		return model.Tokens{}, false
	}
	return model.Tokens{Input: in - cached, CacheRead: cached, CacheWrite: cw, Output: out}, true
}

type usageRecord struct {
	ThreadID string      `json:"thread_id"`
	Usage    *tokenUsage `json:"usage"`
	Thread   *tokenUsage `json:"thread_token_usage"`
}

// record counts one token_usage_record. Only the thread's own records count:
// a record that names another thread (a copy of a parent's history) never
// does. A record with a count that cannot be read is dropped whole.
func (t *tailer) record(raw json.RawMessage, at time.Time) {
	var r usageRecord
	if t.id == "" || json.Unmarshal(raw, &r) != nil || r.ThreadID != t.id {
		return
	}
	one, ok1 := r.Usage.tokens()
	total, ok2 := r.Thread.tokens()
	if !ok1 || !ok2 {
		return
	}
	t.d.Total = total
	t.sawRecord = true
	// A span older than spendKeep (a line without a usable time, which is
	// zero, too) is dropped by the prune that ends every read. So spans run
	// from spendKeep back to a day ahead (parseTS drops a later time): at most
	// 864 quarter hours, and no cap is needed.
	sp := at.UTC().Truncate(model.SpendSpanLength)
	if t.d.Spans == nil {
		t.d.Spans = map[time.Time]model.Tokens{}
	}
	t.d.Spans[sp] = t.d.Spans[sp].Add(one)
}

// spendCut is the start of the oldest quarter hour kept, as of now.
func spendCut(now time.Time) time.Time {
	return now.Add(-spendKeep).Truncate(model.SpendSpanLength)
}

// pruneSpans drops the quarter hours older than spendKeep.
func (t *tailer) pruneSpans(now time.Time) bool {
	cut := spendCut(now)
	n := len(t.d.Spans)
	maps.DeleteFunc(t.d.Spans, func(at time.Time, _ model.Tokens) bool { return at.Before(cut) })
	return len(t.d.Spans) != n
}

// spendOf sums the threads' spend: a card's own and that of the threads
// Codex spawned under it. Zero when none has a record.
func spendOf(ds ...*derived) model.Spend {
	var s model.Spend
	spans := map[time.Time]model.Tokens{}
	for _, d := range ds {
		if d == nil {
			continue
		}
		s.Tokens = s.Tokens.Add(d.Total)
		for at, tk := range d.Spans {
			spans[at] = spans[at].Add(tk)
		}
	}
	for _, at := range slices.SortedFunc(maps.Keys(spans), func(a, b time.Time) int { return a.Compare(b) }) {
		s.Spans = append(s.Spans, model.SpendSpan{At: at, Tokens: spans[at]})
	}
	return s
}

// What was observed: the spend sitboard saw, kept after its rollout leaves
// the scan, so that a card's count never goes down and the days keep a
// session's use for as long as they show it.
//
//   - A thread Codex spawned: its last total and spans, and the card it
//     counts on (root), which a grandchild resolves through once the
//     thread's own rollout has gone.
//   - A session seen live whose rollout left the scan (its date folder aged
//     out, or it was archived): its last fact, ended, sent on until its
//     newest quarter hour is older than spendKeep.
//
// At most maxObserved entries, in the scan and the tail path alike; past
// that, the ones least recently used are let go first. With more threads than
// that in one run, a departed child's tokens can drop from its parent's count
// (an accepted limit).
type observed struct {
	root  string // the card it counts on; for a kept fact, the session itself
	total model.Tokens
	spans map[time.Time]model.Tokens
	fact  *model.SessionFact  // a kept session's last fact
	parts map[string]*derived // a caught-up fact's spend by thread id: the root and its spawned threads (catchup.go)
	used  uint64              // when it was last written or read (registry.useTick)
}

// The catch-up (catchup.go) can hold up to catchupMaxFiles of them, so the
// bound leaves room for those and for the live threads.
const maxObserved = 8192

// touch marks an entry used now.
func (r *registry) touch(o *observed) {
	r.useTick++
	o.used = r.useTick
}

// observeSpawned records a spawned thread's spend under the card it counts on.
func (r *registry) observeSpawned(id, root string, d *derived) {
	o := r.obs[id]
	if o == nil {
		o = &observed{}
		r.obs[id] = o
	}
	o.root = root
	o.total = d.Total
	o.spans = maps.Clone(d.Spans)
	r.touch(o)
}

// keep holds on to a session seen live whose rollout left the scan: its last
// fact goes on, ended, while its spans are in the days. ok is false when it
// has none: nothing to keep.
func (r *registry) keep(id string, f model.SessionFact) bool {
	if len(f.Spend.Spans) == 0 {
		return false
	}
	if !f.Ended {
		f.Ended, f.EndedAt = true, f.LastActivity
	}
	f.Live, f.Status, f.TerminalID = model.Of(false), model.Unknown[model.Status](), ""
	o := &observed{root: id, fact: &f}
	r.touch(o)
	r.obs[id] = o
	return true
}

// tendObserved ages the store: a kept fact loses the spans older than
// spendKeep, and is withdrawn with none left and no turn end within
// turnWindow (a caught-up thread can have turns and no token record); a
// spawned thread's entry goes once its spans have aged out and the card it
// counts on is gone. Then the store is held to maxObserved.
func (r *registry) tendObserved(now time.Time) {
	cut, turnCut := spendCut(now), now.Add(-turnWindow)
	for _, id := range slices.Sorted(maps.Keys(r.obs)) {
		o := r.obs[id]
		if f := o.fact; f != nil {
			if _, back := r.sent[id]; back {
				delete(r.obs, id) // followed again, and its replacement is published (handOff)
				continue
			}
			kept := slices.DeleteFunc(slices.Clone(f.Spend.Spans), func(sp model.SpendSpan) bool { return sp.At.Before(cut) })
			turns := slices.ContainsFunc(f.TurnEnds, func(at time.Time) bool { return !at.Before(turnCut) })
			// A pull request lives as long as the forge lookup of an ended session does.
			prs := len(f.CreatedPRs)+len(f.PushedPRs) > 0 && !f.EndedAt.Before(turnCut)
			if len(kept) == 0 && !turns && !prs {
				delete(r.obs, id)
				r.emit(model.SessionGone{Key: f.Key})
				continue
			}
			if len(kept) == len(f.Spend.Spans) {
				continue
			}
			nf := *f
			nf.Spend.Spans = kept
			o.fact = &nf
			r.emit(nf)
			continue
		}
		maps.DeleteFunc(o.spans, func(at time.Time, _ model.Tokens) bool { return at.Before(cut) })
		_, rootKnown := r.sessions[o.root]
		if ro := r.obs[o.root]; ro != nil && ro.fact != nil {
			rootKnown = true
		}
		if len(o.spans) == 0 && !rootKnown {
			if _, self := r.sessions[id]; !self {
				delete(r.obs, id)
			}
		}
	}
	r.boundObserved()
}

// boundObserved holds the store to maxObserved, letting the least recently
// used entries go.
func (r *registry) boundObserved() {
	over := len(r.obs) - maxObserved
	if over <= 0 {
		return
	}
	ids := slices.SortedFunc(maps.Keys(r.obs), func(a, b string) int { return cmp.Compare(r.obs[a].used, r.obs[b].used) })
	for _, id := range ids[:over] {
		if f := r.obs[id].fact; f != nil {
			if _, back := r.sent[id]; !back {
				r.emit(model.SessionGone{Key: f.Key})
			}
		}
		delete(r.obs, id)
	}
}

// handOff: a kept session whose rollout is in the scan again (a resume) is
// followed as what it was, seen live and ended, carrying its kept spend, so
// that neither the fallback nor a shorter read can take its use from the days.
// The kept fact ages on until the replacement is published.
func (r *registry) handOff() {
	for _, id := range slices.Sorted(maps.Keys(r.obs)) {
		o := r.obs[id]
		s := r.sessions[id]
		// A caught-up thread (Backfill) is no card: it was never followed, and
		// is handed over only when a scan finds it live (rescan).
		if o.fact == nil || o.fact.Backfill || s == nil || s.seenLive {
			continue
		}
		s.seenLive = true
		if !s.ended {
			s.ended, s.endedAt = true, o.fact.EndedAt
		}
		sp := o.fact.Spend
		s.carry = &sp
	}
}

// withCarry is a session's spend with what it carried from the store: per
// count and per quarter hour the larger, the carried spans older than
// spendKeep left out.
func withCarry(s model.Spend, c *model.Spend, now time.Time) model.Spend {
	if c == nil {
		return s
	}
	mx := func(a, b model.Tokens) model.Tokens {
		return model.Tokens{Input: max(a.Input, b.Input), Output: max(a.Output, b.Output), CacheRead: max(a.CacheRead, b.CacheRead), CacheWrite: max(a.CacheWrite, b.CacheWrite)}
	}
	s.Tokens = mx(s.Tokens, c.Tokens)
	spans := map[time.Time]model.Tokens{}
	for _, sp := range s.Spans {
		spans[sp.At] = sp.Tokens
	}
	cut := spendCut(now)
	for _, sp := range c.Spans {
		if !sp.At.Before(cut) {
			spans[sp.At] = mx(spans[sp.At], sp.Tokens)
		}
	}
	s.Spans = nil
	for _, at := range slices.SortedFunc(maps.Keys(spans), func(a, b time.Time) int { return a.Compare(b) }) {
		s.Spans = append(s.Spans, model.SpendSpan{At: at, Tokens: spans[at]})
	}
	return s
}

// spawnedSpend is what the threads spawned under card id have used.
func (r *registry) spawnedSpend(id string) []*derived {
	var out []*derived
	for _, k := range slices.Sorted(maps.Keys(r.obs)) {
		if o := r.obs[k]; o.fact == nil && o.root == id && k != id {
			r.touch(o)
			out = append(out, &derived{Total: o.total, Spans: o.spans})
		}
	}
	return out
}

// threadSpend is the spend of card id: its own thread's (d) and the threads
// spawned under it, each merged with what the catch-up read of the same
// thread (carried: by thread id; per count and per quarter hour the larger,
// within one thread), then summed. A caught-up thread the live scan no longer
// has (a child whose rollout left it) keeps its caught-up spend.
func (r *registry) threadSpend(id string, d *derived, carried map[string]*derived) model.Spend {
	threads := map[string]*derived{id: d}
	for _, k := range slices.Sorted(maps.Keys(r.obs)) {
		if o := r.obs[k]; o.fact == nil && o.root == id && k != id {
			r.touch(o)
			threads[k] = &derived{Total: o.total, Spans: o.spans}
		}
	}
	cut := spendCut(r.a.now())
	for k, c := range carried {
		threads[k] = maxDerived(threads[k], c, cut)
	}
	ds := make([]*derived, 0, len(threads))
	for _, k := range slices.Sorted(maps.Keys(threads)) {
		ds = append(ds, threads[k])
	}
	return spendOf(ds...)
}

// maxDerived is the spend of one thread read two ways: per count and per
// quarter hour the larger, the quarter hours before cut left out of the
// carried one. live may be nil.
func maxDerived(live, carried *derived, cut time.Time) *derived {
	mx := func(a, b model.Tokens) model.Tokens {
		return model.Tokens{Input: max(a.Input, b.Input), Output: max(a.Output, b.Output), CacheRead: max(a.CacheRead, b.CacheRead), CacheWrite: max(a.CacheWrite, b.CacheWrite)}
	}
	out := &derived{Spans: map[time.Time]model.Tokens{}}
	if live != nil {
		out.Total = live.Total
		maps.Copy(out.Spans, live.Spans)
	}
	out.Total = mx(out.Total, carried.Total)
	for at, tk := range carried.Spans {
		if !at.Before(cut) {
			out.Spans[at] = mx(out.Spans[at], tk)
		}
	}
	return out
}

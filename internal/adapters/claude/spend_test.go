package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// reply writes one assistant line with token counts.
func reply(b *bytes.Buffer, session, id, mdl, at string, in, out, read, w5, w1 int64, extra map[string]any) {
	u := map[string]any{
		"input_tokens": in, "output_tokens": out, "cache_read_input_tokens": read,
		"cache_creation_input_tokens": w5 + w1,
		"cache_creation":              map[string]any{"ephemeral_5m_input_tokens": w5, "ephemeral_1h_input_tokens": w1},
		"service_tier":                "standard", "speed": "standard",
	}
	for k, v := range extra {
		u[k] = v
	}
	line, _ := json.Marshal(map[string]any{
		"type": "assistant", "sessionId": session, "timestamp": at,
		"message": map[string]any{"id": id, "model": mdl, "role": "assistant", "usage": u,
			"content": []any{map[string]any{"type": "text", "text": "Placeholder"}}},
	})
	b.Write(line)
	b.WriteByte('\n')
}

func costState(b *bytes.Buffer, session string, usd float64, unknown bool) {
	line, _ := json.Marshal(map[string]any{
		"type": "cost-state", "sessionId": session, "totalCostUSD": usd, "hasUnknownModelCost": unknown,
		"modelUsage": map[string]any{"claude-opus-5-5": map[string]any{
			"inputTokens": 900, "outputTokens": 9000, "cacheReadInputTokens": 90000, "cacheCreationInputTokens": 900, "costUSD": usd,
		}},
	})
	b.Write(line)
	b.WriteByte('\n')
}

// spendOf builds and prices a session's spend, as the adapter does.
func spendOf(main *spendLog, subs []*spendLog, tab priceTable, cut time.Time) model.Spend {
	return priceSpend(buildSpend(main, subs, cut), tab)
}

func f64(v float64) *float64 { return &v }

// testPrices are sample prices, US dollars per million tokens.
var testPrices = priceTable{
	"claude-opus-5-5": {In: f64(4), Out: f64(20), Write5m: f64(5), Write1h: f64(8), Read: f64(0.2), Web: f64(0.01),
		Fast: &price{In: f64(8), Out: f64(40), Write5m: f64(10), Write1h: f64(16), Read: f64(0.4)}},
	"claude-haiku-5-5": {In: f64(1), Out: f64(5), Write5m: f64(1.25), Write1h: f64(2), Read: f64(0.1)},
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

const spendID = "s"

func TestUsageCountsEachReplyOnce(t *testing.T) {
	var b bytes.Buffer
	// One reply written as two lines (two content blocks) with the same counts.
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 1000, 50, 0, nil)
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:01Z", 10, 100, 1000, 50, 0, nil)
	reply(&b, spendID, "msg_2", "claude-opus-5-5", "2026-11-17T08:10:00Z", 1, 20, 300, 0, 40, nil)
	reply(&b, spendID, "msg_3", "<synthetic>", "2026-11-17T08:11:00Z", 0, 99, 0, 0, 0, nil)
	reply(&b, "another-session", "msg_4", "claude-opus-5-5", "2026-11-17T08:12:00Z", 0, 99, 0, 0, 0, nil)
	reply(&b, spendID, "msg_5", "claude-opus-5-5", "2026-11-17T08:13:00Z", -5, 7, 0, 0, 0, nil)
	tl := tailOf(t, b.Bytes())
	got := spendOf(&tl.spend, nil, testPrices, fixedNow.Add(-turnWindow))
	want := model.Tokens{Input: 11, Output: 127, CacheRead: 1300, CacheWrite: 90}
	if got.Tokens != want {
		t.Fatalf("Tokens = %+v; want %+v", got.Tokens, want)
	}
	usd := (11*4 + 127*20 + 1300*0.2 + 50*5 + 40*8) / 1e6
	if !got.Priced || got.Recorded || !near(got.USD, usd) {
		t.Fatalf("USD = %v priced %v recorded %v; want %v computed", got.USD, got.Priced, got.Recorded, usd)
	}
	if len(got.Spans) != 1 || !got.Spans[0].At.Equal(ts("2026-11-17T08:00:00Z")) || got.Spans[0].Tokens != want || !near(got.Spans[0].USD, usd) {
		t.Fatalf("Spans = %+v", got.Spans)
	}
}

func TestPriceParts(t *testing.T) {
	u := usage{in: 1e6, out: 1e6, read: 1e6, write5m: 1e6, write1h: 1e6, web: 3}
	if got, ok := testPrices.cost(priceKey{model: "claude-opus-5-5"}, u); !ok || !near(got, 4+20+0.2+5+8+0.03) {
		t.Fatalf("standard = %v %v", got, ok)
	}
	// Fast mode has its own prices; web search keeps the model's.
	if got, ok := testPrices.cost(priceKey{model: "claude-opus-5-5", fast: true}, u); !ok || !near(got, 8+40+0.4+10+16+0.03) {
		t.Fatalf("fast = %v %v", got, ok)
	}
	if _, ok := testPrices.cost(priceKey{model: "claude-haiku-5-5", fast: true}, u); ok {
		t.Fatal("fast without a fast price must have no price")
	}
	// A web search with no price for it makes the reply unpriced; none used, it is priced.
	if _, ok := testPrices.cost(priceKey{model: "claude-haiku-5-5"}, u); ok {
		t.Fatal("web searches without a price must have no price")
	}
	u.web = 0
	if _, ok := testPrices.cost(priceKey{model: "claude-haiku-5-5"}, u); !ok {
		t.Fatal("haiku without web searches must be priced")
	}
	if _, ok := testPrices.cost(priceKey{model: "claude-unknown"}, u); ok {
		t.Fatal("a model missing from the table must have no price")
	}
	bad := priceTable{"m": {In: f64(-1), Out: f64(1), Write5m: f64(1), Write1h: f64(1), Read: f64(1)}}
	if _, ok := bad.cost(priceKey{model: "m"}, usage{in: 1}); ok {
		t.Fatal("a negative price must not count")
	}
}

func TestUnpricedModelGivesTokensOnly(t *testing.T) {
	var b bytes.Buffer
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 0, 0, 0, nil)
	reply(&b, spendID, "msg_2", "claude-custom", "2026-11-17T09:00:00Z", 10, 100, 0, 0, 0, nil)
	got := spendOf(&tailOf(t, b.Bytes()).spend, nil, testPrices, fixedNow.Add(-turnWindow))
	// The part with a price is kept, and the whole says it is not complete.
	if got.Priced || !near(got.USD, (10*4+100*20)/1e6) || got.Tokens.Output != 200 {
		t.Fatalf("got %+v; want the priced part, marked not priced", got)
	}
	// The span with only a priced model is priced.
	if len(got.Spans) != 2 || !got.Spans[0].Priced || got.Spans[0].USD == 0 || got.Spans[1].Priced || got.Spans[1].USD != 0 {
		t.Fatalf("Spans = %+v", got.Spans)
	}
	if got := spendOf(&tailOf(t, b.Bytes()).spend, nil, nil, fixedNow.Add(-turnWindow)); got.Priced || got.USD != 0 || got.Tokens.Output != 200 {
		t.Fatalf("no price table: %+v", got)
	}
}

func TestCostStateUntilTheNextReply(t *testing.T) {
	var b bytes.Buffer
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 0, 0, 0, nil)
	costState(&b, spendID, 12.5, false)
	p := filepath.Join(t.TempDir(), spendID+".jsonl")
	writeFile(t, p, b.Bytes())
	tl := newTailer(p, staticProber(t))
	if _, err := tl.update(t.Context(), fixedNow); err != nil {
		t.Fatal(err)
	}
	got := spendOf(&tl.spend, nil, testPrices, fixedNow.Add(-turnWindow))
	if !got.Recorded || !got.Priced || got.USD != 12.5 || got.Tokens != (model.Tokens{Input: 900, Output: 9000, CacheRead: 90000, CacheWrite: 900}) {
		t.Fatalf("ended: %+v; want Claude Code's recorded total", got)
	}
	// Hours stay the counted ones.
	if len(got.Spans) != 1 || got.Spans[0].Tokens.Output != 100 {
		t.Fatalf("Spans = %+v", got.Spans)
	}
	// Resumed: Claude Code restores the recorded total, and what came after it
	// is priced and added.
	b.Reset()
	reply(&b, spendID, "msg_2", "claude-opus-5-5", "2026-11-17T08:30:00Z", 0, 50, 0, 0, 0, nil)
	appendFile(t, p, b.Bytes())
	if _, err := tl.update(t.Context(), fixedNow); err != nil {
		t.Fatal(err)
	}
	got = spendOf(&tl.spend, nil, testPrices, fixedNow.Add(-turnWindow))
	if got.Recorded || !got.Priced || got.Tokens.Output != 9050 || !near(got.USD, 12.5+(50*20)/1e6) {
		t.Fatalf("resumed: %+v; want the recorded total plus the new reply", got)
	}
	// A cost-state with a model Claude Code could not price gives no price.
	b.Reset()
	costState(&b, spendID, 3, true)
	appendFile(t, p, b.Bytes())
	if _, err := tl.update(t.Context(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if got = spendOf(&tl.spend, nil, testPrices, fixedNow.Add(-turnWindow)); !got.Recorded || got.Priced || got.USD != 3 {
		t.Fatalf("unknown model cost: %+v", got)
	}
}

// A time zone's midnight can fall at :30 or :45 UTC, so what was used before
// and after it must not share a span. (+5:45 and +12:45 are the ones a span
// longer than a quarter of an hour would straddle.)
func TestSpendSpansNeverStraddleAZoneMidnight(t *testing.T) {
	for _, z := range []struct {
		name   string
		offset time.Duration
	}{
		{"+5:30", 5*time.Hour + 30*time.Minute},
		{"+5:45", 5*time.Hour + 45*time.Minute},
		{"+12:45", 12*time.Hour + 45*time.Minute},
		{"-3:30", -(3*time.Hour + 30*time.Minute)},
	} {
		t.Run(z.name, func(t *testing.T) {
			zone := time.FixedZone(z.name, int(z.offset/time.Second))
			midnight := time.Date(2026, 11, 17, 0, 0, 0, 0, zone)
			stamp := func(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05Z") }
			var b bytes.Buffer
			reply(&b, spendID, "msg_1", "claude-opus-5-5", stamp(midnight.Add(-10*time.Minute)), 0, 10, 0, 0, 0, nil)
			reply(&b, spendID, "msg_2", "claude-opus-5-5", stamp(midnight.Add(10*time.Minute)), 0, 20, 0, 0, 0, nil)
			tl := tailOf(t, b.Bytes())
			got := spendOf(&tl.spend, nil, testPrices, fixedNow.Add(-turnWindow))
			if len(got.Spans) != 2 || got.Spans[0].Tokens.Output != 10 || got.Spans[1].Tokens.Output != 20 {
				t.Fatalf("Spans = %+v; want the two replies apart", got.Spans)
			}
			if !got.Spans[0].At.Before(midnight) || got.Spans[1].At.Before(midnight) {
				t.Fatalf("Spans = %+v; want one before and one from the local midnight %v", got.Spans, midnight)
			}
		})
	}
}

func TestSpendSpansKeepOnly7Days(t *testing.T) {
	var b bytes.Buffer
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-09T08:00:00Z", 0, 100, 0, 0, 0, nil)
	reply(&b, spendID, "msg_2", "claude-opus-5-5", "2026-11-16T08:00:00Z", 0, 10, 0, 0, 0, nil)
	tl := tailOf(t, b.Bytes())
	got := spendOf(&tl.spend, nil, testPrices, fixedNow.Add(-turnWindow).Truncate(model.SpendSpanLength))
	if got.Tokens.Output != 110 {
		t.Fatalf("total = %+v; the whole session counts", got.Tokens)
	}
	if len(got.Spans) != 1 || got.Spans[0].Tokens.Output != 10 {
		t.Fatalf("Spans = %+v; want only the last 7 days", got.Spans)
	}
	if len(tl.spend.spans) != 1 {
		t.Fatalf("the tailer keeps %d spans; want old ones pruned", len(tl.spend.spans))
	}
}

func writeConfig(t *testing.T, home string, v any) string {
	t.Helper()
	b, _ := json.Marshal(v)
	p := filepath.Join(home, ".claude.json")
	writeFile(t, p, b)
	return p
}

var opusPrice = map[string]any{"inputTokens": 4, "outputTokens": 20, "promptCacheWriteTokens": 5, "promptCacheWrite1hTokens": 8, "promptCacheReadTokens": 0.2}

func TestPriceTableIsTheOnlyKeyKept(t *testing.T) {
	home := t.TempDir()
	p := writeConfig(t, home, map[string]any{
		"oauthAccount":              map[string]any{"emailAddress": "sample-account-value"},
		"projects":                  map[string]any{"/home/dev/alpha": map[string]any{"lastCost": 1}},
		"additionalModelCostsCache": map[string]any{"claude-opus-5-5": opusPrice},
	})
	pr := newPrices(p)
	tab := pr.table(fixedNow)
	if len(tab) != 1 || tab["claude-opus-5-5"].Out == nil || *tab["claude-opus-5-5"].Out != 20 {
		t.Fatalf("table = %+v", tab)
	}
	// A half-written file keeps the last table.
	writeFile(t, p, []byte(`{"additionalModelCostsCache": {`))
	if tab := pr.table(fixedNow.Add(2 * priceCheck)); len(tab) != 1 {
		t.Fatalf("after a broken write: %+v; want the last table", tab)
	}
	// Within priceCheck the file is not looked at again.
	writeConfig(t, home, map[string]any{"additionalModelCostsCache": map[string]any{}})
	if tab := pr.table(fixedNow.Add(2*priceCheck + time.Second)); len(tab) != 1 {
		t.Fatalf("looked again too soon: %+v", tab)
	}
	if tab := pr.table(fixedNow.Add(4 * priceCheck)); len(tab) != 0 {
		t.Fatalf("after the change: %+v; want the new, empty table", tab)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if tab := pr.table(fixedNow.Add(6 * priceCheck)); tab != nil {
		t.Fatalf("no file: %+v", tab)
	}
}

func TestSessionSpendCountsSubagents(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	writeConfig(t, e.home, map[string]any{"additionalModelCostsCache": map[string]any{"claude-opus-5-5": opusPrice}})
	var main, sub bytes.Buffer
	main.Write(fixture(t, "coordinator.jsonl"))
	reply(&main, coordID, "msg_m1", "claude-opus-5-5", "2026-11-17T08:20:00Z", 10, 1000, 0, 0, 0, nil)
	// A subagent's lines carry the session's id, not the subagent's.
	reply(&sub, coordID, "msg_s1", "claude-opus-5-5", "2026-11-17T08:21:00Z", 5, 500, 0, 0, 0, nil)
	tr := e.transcript("/home/dev/projects/alpha", coordID, "coordinator.jsonl")
	writeFile(t, tr, main.Bytes())
	writeFile(t, filepath.Join(filepath.Dir(tr), coordID, "subagents", "agent-a1.jsonl"), sub.Bytes())
	c := run(t, e.adapter())
	f := c.wait(t, 2*time.Second, "spend", session(coordID, func(f model.SessionFact) bool {
		return f.Subagents == 1 && f.Spend.Tokens.Output >= 1500
	})).(model.SessionFact)
	// The fixture's own reply counts 1 input and 1 output token.
	if f.Spend.Tokens.Input != 16 || f.Spend.Tokens.Output != 1501 {
		t.Fatalf("Spend = %+v; want the session's and its subagent's replies", f.Spend)
	}
	if !f.Spend.Priced || !near(f.Spend.USD, (16*4+1501*20)/1e6) {
		t.Fatalf("Spend = %+v; want priced from .claude.json", f.Spend)
	}
}

// forkOf copies every line of a parent's transcript the way a fork's
// transcript holds them: the same lines, the fork's session id, and a
// forkedFrom marker. This shape is read from Claude Code's code, not from a
// real fork: it needs confirming against a real fork.
func forkOf(t *testing.T, parent []byte, fork, parentID string) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, l := range bytes.Split(bytes.TrimSpace(parent), []byte("\n")) {
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatal(err)
		}
		m["sessionId"], m["isSidechain"] = fork, false
		m["forkedFrom"] = map[string]any{"sessionId": parentID, "messageUuid": "00000000-0000-4000-8000-000000000001"}
		b, _ := json.Marshal(m)
		out.Write(b)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func TestForkedCopiesAreNotCounted(t *testing.T) {
	var parent bytes.Buffer
	reply(&parent, "parent-id", "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 1000, 0, 0, nil)
	costState(&parent, "parent-id", 7, false)
	// The fork's transcript: the parent's lines copied, then its own reply.
	b := forkOf(t, parent.Bytes(), spendID, "parent-id")
	var own bytes.Buffer
	reply(&own, spendID, "msg_2", "claude-opus-5-5", "2026-11-17T09:00:00Z", 1, 20, 0, 0, 0, nil)
	tl := tailOf(t, append(b, own.Bytes()...))
	got := spendOf(&tl.spend, nil, testPrices, fixedNow.Add(-turnWindow))
	if got.Recorded || got.Tokens != (model.Tokens{Input: 1, Output: 20}) || len(got.Spans) != 1 {
		t.Fatalf("fork: %+v; want only its own reply, no copied cost-state", got)
	}
	// Nothing but copies: no tokens, and no recorded cost either.
	if got := spendOf(&tailOf(t, forkOf(t, parent.Bytes(), spendID, "parent-id")).spend, nil, testPrices, fixedNow.Add(-turnWindow)); got.Recorded || !got.Tokens.IsZero() {
		t.Fatalf("copies only: %+v; want nothing", got)
	}
	// A null marker is not a marker.
	var c bytes.Buffer
	reply(&c, spendID, "msg_3", "claude-opus-5-5", "2026-11-17T09:00:00Z", 1, 20, 0, 0, 0, nil)
	line := bytes.Replace(c.Bytes(), []byte(`{"message"`), []byte(`{"forkedFrom":null,"message"`), 1)
	if got := spendOf(&tailOf(t, line).spend, nil, testPrices, fixedNow.Add(-turnWindow)); got.Tokens.Output != 20 {
		t.Fatalf("null forkedFrom: %+v; want counted", got)
	}
}

func TestReplyCountedOnceAcrossTranscripts(t *testing.T) {
	cl := newReplyClaims()
	var b bytes.Buffer
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 0, 0, 0, nil)
	reply(&b, spendID, "msg_2", "claude-opus-5-5", "2026-11-17T08:05:00Z", 10, 50, 0, 0, 0, nil)
	tails := func(name string, cl *replyClaims, owner uint32, b []byte) *tailer {
		p := filepath.Join(t.TempDir(), name+".jsonl")
		writeFile(t, p, b)
		tl := newTailer(p, staticProber(t))
		tl.usageOwner, tl.claims, tl.owner = spendID, cl, owner
		if _, err := tl.update(t.Context(), fixedNow); err != nil {
			t.Fatal(err)
		}
		return tl
	}
	first, second := tails("a", cl, 1, b.Bytes()), tails("b", cl, 2, b.Bytes())
	out := func(tl *tailer) (n int64) {
		for _, u := range tl.spend.total {
			n += u.out
		}
		return n
	}
	if out(first) != 150 {
		t.Fatalf("first: %+v", first.spend.total)
	}
	if len(second.spend.total) != 0 || len(second.spend.spans) != 0 {
		t.Fatalf("second holds copies of counted replies: %+v", second.spend)
	}
	// The same transcript read again (after a reset) counts again.
	first.reset()
	first.info = nil // as a tailer made again for the same file
	if _, err := first.update(t.Context(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if out(first) != 150 {
		t.Fatalf("after a reset: %+v", first.spend.total)
	}
	// Another session's transcripts have claims of their own: what this
	// session counted is no concern of theirs.
	if other := tails("c", newReplyClaims(), 3, b.Bytes()); out(other) != 150 {
		t.Fatalf("another session: %+v", other.spend.total)
	}
	// The same reply id further down the same transcript is the same reply.
	var rep bytes.Buffer
	reply(&rep, spendID, "msg_7", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 0, 0, 0, nil)
	reply(&rep, spendID, "msg_8", "claude-opus-5-5", "2026-11-17T08:05:00Z", 10, 50, 0, 0, 0, nil)
	reply(&rep, spendID, "msg_7", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 0, 0, 0, nil)
	if got := tails("d", newReplyClaims(), 4, rep.Bytes()); out(got) != 150 {
		t.Fatalf("a repeated id: %+v; want it once", got.spend.total)
	}
}

// Which session counts a reply is a property of the reply (the session id it
// carries), never of the order the sessions are read in: two sessions whose
// transcripts each hold the same reply id under their own session id each
// count it, whichever is read first, live or ended, small or large.
func TestSpendDoesNotDependOnTheOrderSessionsAreRead(t *testing.T) {
	for _, order := range [][]string{{"aaaa", "bbbb"}, {"bbbb", "aaaa"}} {
		ix := &indexer{a: &adapter{prices: newPrices(filepath.Join(t.TempDir(), "none.json")), prober: staticProber(t)},
			tailers: map[string]*tailer{}, claims: map[string]*replyClaims{}, subs: map[string][]string{}, subMod: map[string]string{}}
		dir := t.TempDir()
		out := map[string]int64{}
		for _, id := range order {
			var b bytes.Buffer
			reply(&b, id, "msg_shared", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 0, 0, 0, nil)
			p := filepath.Join(dir, id+".jsonl")
			writeFile(t, p, b.Bytes())
			tl := ix.tail(t.Context(), p, id, fixedNow, false, nil)
			out[id] = sumUsage(tl.spend.total).out
		}
		if out["aaaa"] != 100 || out["bbbb"] != 100 {
			t.Fatalf("order %v: %v; want each session's own reply counted", order, out)
		}
	}
}

func TestSessionSpendCountsASubagentsCopyOnce(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	writeConfig(t, e.home, map[string]any{"additionalModelCostsCache": map[string]any{"claude-opus-5-5": opusPrice}})
	var main, sub bytes.Buffer
	main.Write(fixture(t, "coordinator.jsonl"))
	reply(&main, coordID, "msg_m1", "claude-opus-5-5", "2026-11-17T08:20:00Z", 10, 1000, 0, 0, 0, nil)
	// The subagent's transcript holds a copy of that reply, and one of its own.
	reply(&sub, coordID, "msg_m1", "claude-opus-5-5", "2026-11-17T08:20:00Z", 10, 1000, 0, 0, 0, nil)
	reply(&sub, coordID, "msg_s1", "claude-opus-5-5", "2026-11-17T08:21:00Z", 5, 500, 0, 0, 0, nil)
	tr := e.transcript("/home/dev/projects/alpha", coordID, "coordinator.jsonl")
	writeFile(t, tr, main.Bytes())
	writeFile(t, filepath.Join(filepath.Dir(tr), coordID, "subagents", "agent-a1.jsonl"), sub.Bytes())
	c := run(t, e.adapter())
	f := c.wait(t, 2*time.Second, "spend", session(coordID, func(f model.SessionFact) bool {
		return f.Subagents == 1 && f.Spend.Tokens.Output >= 1500
	})).(model.SessionFact)
	if f.Spend.Tokens.Input != 16 || f.Spend.Tokens.Output != 1501 {
		t.Fatalf("Spend = %+v; want the copied reply once", f.Spend)
	}
}

// longTable has a model with a long-prompt rate card, as Claude Code's price
// table can: a reply whose prompt passes 200000 tokens is priced whole at it.
func longTable() priceTable {
	above := int64(200000)
	return priceTable{
		"claude-opus-5-5": {In: f64(4), Out: f64(20), Write5m: f64(5), Write1h: f64(8), Read: f64(0.2), Web: f64(0.01),
			Long: &price{In: f64(8), Out: f64(30), Write5m: f64(10), Write1h: f64(16), Read: f64(0.4), Above: &above}},
		"claude-haiku-5-5": {In: f64(1), Out: f64(5), Write5m: f64(1.25), Write1h: f64(2), Read: f64(0.1)},
	}
}

// tailWithPrices reads a transcript with the price table of that moment, as
// the indexer does.
func tailWithPrices(t *testing.T, tab priceTable, b []byte) *tailer {
	t.Helper()
	p := filepath.Join(t.TempDir(), spendID+".jsonl")
	writeFile(t, p, b)
	tl := newTailer(p, staticProber(t))
	tl.tab = tab
	if _, err := tl.update(t.Context(), fixedNow); err != nil {
		t.Fatal(err)
	}
	return tl
}

func TestLongPromptRepliesAreAtTheLongRates(t *testing.T) {
	tab := longTable()
	var b bytes.Buffer
	// Prompt = input + cache read + cache write: exactly at the threshold is
	// still the base rates, one token more is the long ones, for the whole reply.
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 1000, 100, 150000, 49000, 0, nil) // 200000
	reply(&b, spendID, "msg_2", "claude-opus-5-5", "2026-11-17T08:20:00Z", 1001, 100, 150000, 49000, 0, nil) // 200001
	reply(&b, spendID, "msg_3", "claude-opus-5-5", "2026-11-17T08:40:00Z", 10, 100, 100000, 0, 100000, nil)  // 1h writes count too
	tl := tailWithPrices(t, tab, b.Bytes())
	got := spendOf(&tl.spend, nil, tab, fixedNow.Add(-turnWindow))
	base := (1000*4 + 100*20 + 150000*0.2 + 49000*5) / 1e6
	long := (1001*8 + 100*30 + 150000*0.4 + 49000*10) / 1e6
	long3 := (10*8 + 100*30 + 100000*0.4 + 100000*16) / 1e6
	if !got.Priced || !near(got.USD, base+long+long3) {
		t.Fatalf("USD = %v priced %v; want %v (base %v, long %v, long %v)", got.USD, got.Priced, base+long+long3, base, long, long3)
	}
	if len(got.Spans) != 3 || !near(got.Spans[0].USD, base) || !near(got.Spans[1].USD, long) || !near(got.Spans[2].USD, long3) {
		t.Fatalf("Spans = %+v", got.Spans)
	}
	// A model with no long-prompt card is priced at its one card, whatever the size.
	var h bytes.Buffer
	reply(&h, spendID, "msg_1", "claude-haiku-5-5", "2026-11-17T08:00:00Z", 900000, 0, 0, 0, 0, nil)
	if got := spendOf(&tailWithPrices(t, tab, h.Bytes()).spend, nil, tab, fixedNow.Add(-turnWindow)); !got.Priced || !near(got.USD, 0.9) {
		t.Fatalf("no long card: %+v", got)
	}
	// An entry whose long card has no threshold (or none that makes sense) has no tier.
	for _, bad := range []*int64{nil, new(int64)} {
		e := tab["claude-opus-5-5"]
		l := *e.Long
		l.Above = bad
		e.Long = &l
		odd := priceTable{"claude-opus-5-5": e}
		if got := spendOf(&tailWithPrices(t, odd, b.Bytes()).spend, nil, odd, fixedNow.Add(-turnWindow)); !got.Priced || !near(got.USD, 3*0+(1000*4+100*20+150000*0.2+49000*5+1001*4+100*20+150000*0.2+49000*5+10*4+100*20+100000*0.2+100000*8)/1e6) {
			t.Fatalf("threshold %v: %+v", bad, got)
		}
	}
}

func TestLongPromptTierNeedsTheTableAtCountTime(t *testing.T) {
	tab := longTable()
	var b bytes.Buffer
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 1000, 100, 0, 0, 0, nil)
	reply(&b, spendID, "msg_2", "claude-haiku-5-5", "2026-11-17T08:00:00Z", 1000, 100, 0, 0, 0, nil)
	// Counted while the table could not be read: the haiku-like model with no
	// long card is priced later, the one with a long card is not (its tier is
	// not known), and the figure says so.
	tl := tailWithPrices(t, nil, b.Bytes())
	got := spendOf(&tl.spend, nil, tab, fixedNow.Add(-turnWindow))
	if got.Priced || !near(got.USD, (1000*1+100*5)/1e6) {
		t.Fatalf("no table at count time: %+v", got)
	}
	if len(got.Spans) != 1 || got.Spans[0].Priced {
		t.Fatalf("Spans = %+v", got.Spans)
	}
	// Fast mode on a long prompt has its card only inside the long one.
	f := price{In: f64(1), Out: f64(1), Write5m: f64(1), Read: f64(1)}
	tab["claude-opus-5-5"] = func() price { e := tab["claude-opus-5-5"]; e.Fast = &f; return e }()
	if _, ok := tab.cost(priceKey{model: "claude-opus-5-5", fast: true, tier: tierLong}, usage{in: 1}); ok {
		t.Fatal("fast and long with no fast long card must have no price")
	}
	if _, ok := tab.cost(priceKey{model: "claude-opus-5-5", fast: true, tier: tierBase}, usage{in: 1}); !ok {
		t.Fatal("fast and base must be priced")
	}
	// The long card's web search rate is the entry's when it has none.
	if got, ok := tab.cost(priceKey{model: "claude-opus-5-5", tier: tierLong}, usage{web: 100}); !ok || !near(got, 1) {
		t.Fatalf("long web = %v %v", got, ok)
	}
}

func TestModelIDsAreMatchedToThePriceTable(t *testing.T) {
	tab := priceTable{
		"claude-opus-5-5":           {In: f64(4), Out: f64(20), Write5m: f64(5), Read: f64(0.2)},
		"claude-haiku-4-5-20251001": {In: f64(1), Out: f64(5), Write5m: f64(1), Read: f64(0.1)},
	}
	for _, m := range []string{
		"claude-opus-5-5", "claude-opus-5-5[1m]", "claude-opus-5-5-20260101", "anthropic.claude-opus-5-5", "us.anthropic.claude-opus-5-5-20260101-v1:0",
		"claude-opus-5-5@20260101", "publishers/anthropic/models/claude-opus-5-5",
	} {
		if got, ok := tab.cost(priceKey{model: m}, usage{out: 1e6}); !ok || got != 20 {
			t.Errorf("%q: %v %v; want priced like claude-opus-5-5", m, got, ok)
		}
	}
	// The table's own key can be the dated one.
	for _, m := range []string{"claude-haiku-4-5-20251001", "claude-haiku-4-5"} {
		if got, ok := tab.cost(priceKey{model: m}, usage{out: 1e6}); !ok || got != 5 {
			t.Errorf("%q: %v %v; want priced like the haiku entry", m, got, ok)
		}
	}
	for _, m := range []string{"claude-opus-5", "claude-opus-5-55", "gpt-5", ""} {
		if _, ok := tab.cost(priceKey{model: m}, usage{out: 1}); ok {
			t.Errorf("%q must have no price", m)
		}
	}
}

func TestATableFarFromTheRecordedTotalPricesNoSpans(t *testing.T) {
	var b bytes.Buffer
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 900, 9000, 90000, 900, 0, nil)
	costState(&b, spendID, 0.2, false) // the counted use is $0.2 at testPrices
	tl := tailOf(t, b.Bytes())
	near1 := spendOf(&tl.spend, nil, testPrices, fixedNow.Add(-turnWindow))
	if !near1.Recorded || len(near1.Spans) != 1 || !near1.Spans[0].Priced {
		t.Fatalf("agreeing table: %+v", near1)
	}
	// The same table in other units (per token, not per million) is no price.
	tiny := priceTable{"claude-opus-5-5": {In: f64(4e-6), Out: f64(20e-6), Write5m: f64(5e-6), Write1h: f64(8e-6), Read: f64(0.2e-6)}}
	got := spendOf(&tl.spend, nil, tiny, fixedNow.Add(-turnWindow))
	if !got.Recorded || got.USD != 0.2 || !got.Priced || len(got.Spans) != 1 || got.Spans[0].Priced || got.Spans[0].USD != 0 {
		t.Fatalf("far table: %+v; want the recorded total, spans unpriced", got)
	}
}

func TestRecordedTotalStaysWhenSubagentsAreMerged(t *testing.T) {
	var m, a bytes.Buffer
	reply(&m, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 0, 0, 0, nil)
	costState(&m, spendID, 12.5, false)
	// A subagent's own use, before the exit. Claude Code's total already has it.
	reply(&a, spendID, "msg_s1", "claude-opus-5-5", "2026-11-17T07:50:00Z", 0, 400, 0, 0, 0, nil)
	main, sub := tailOf(t, m.Bytes()), tailOf(t, a.Bytes())
	got := spendOf(&main.spend, []*spendLog{&sub.spend}, testPrices, fixedNow.Add(-turnWindow))
	if !got.Recorded || got.USD != 12.5 || !got.Priced {
		t.Fatalf("ended: %+v; want the recorded total unchanged by the subagent", got)
	}
	if len(got.Spans) != 2 { // but the hours have the subagent's use
		t.Fatalf("Spans = %+v", got.Spans)
	}
}

// A resumed session adds to Claude Code's restored total what came after it,
// told per reply: the main transcript's replies below its last cost-state
// line, and a subagent's replies from the moment the session was resumed (its
// first reply below the line: nothing of a session runs between its exit and
// then). A subagent's reply before the exit is in the recorded total already.
func TestResumedSessionAddsEachTranscriptsUseAfterTheExit(t *testing.T) {
	dir := t.TempDir()
	mainPath, subPath := filepath.Join(dir, spendID+".jsonl"), filepath.Join(dir, spendID, "subagents", "agent-a1.jsonl")
	var m, a bytes.Buffer
	reply(&m, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 0, 0, 0, nil)
	// 08:20, in the span after msg_1's: before the exit, so in the total.
	reply(&a, spendID, "msg_s1", "claude-opus-5-5", "2026-11-17T08:20:00Z", 0, 400, 0, 0, 0, nil)
	costState(&m, spendID, 12.5, false)
	writeFile(t, mainPath, m.Bytes())
	writeFile(t, subPath, a.Bytes())
	ix := newIndexer(newAdapter(t, Options{Home: t.TempDir(), Now: func() time.Time { return fixedNow }}))
	pass := func() model.Spend {
		main := ix.tail(t.Context(), mainPath, spendID, fixedNow, false, nil)
		sub := ix.tail(t.Context(), subPath, spendID, fixedNow, false, main)
		return spendOf(&main.spend, []*spendLog{&sub.spend}, testPrices, fixedNow.Add(-spendKeep))
	}
	if got := pass(); !got.Recorded || got.USD != 12.5 {
		t.Fatalf("ended: %+v; want the recorded total", got)
	}
	// Resumed at 09:00; the subagent works on alone at 09:10.
	m.Reset()
	reply(&m, spendID, "msg_2", "claude-opus-5-5", "2026-11-17T09:00:00Z", 0, 50, 0, 0, 0, nil)
	appendFile(t, mainPath, m.Bytes())
	a.Reset()
	reply(&a, spendID, "msg_s2", "claude-opus-5-5", "2026-11-17T09:10:00Z", 0, 200, 0, 0, 0, nil)
	appendFile(t, subPath, a.Bytes())
	if got := pass(); got.Recorded || !got.Priced || !near(got.USD, 12.5+(50*20+200*20)/1e6) {
		t.Fatalf("resumed: %+v; want the recorded total, the main reply and the subagent's after the resume, the 08:20 one not again", got)
	}
	// Read from cold (a restart): the same.
	ix = newIndexer(newAdapter(t, Options{Home: t.TempDir(), Now: func() time.Time { return fixedNow }}))
	if got := pass(); !near(got.USD, 12.5+(50*20+200*20)/1e6) {
		t.Fatalf("cold: %+v", got)
	}
	// It exits again: the new line covers everything.
	m.Reset()
	costState(&m, spendID, 20, false)
	appendFile(t, mainPath, m.Bytes())
	if got := pass(); !got.Recorded || got.USD != 20 {
		t.Fatalf("ended again: %+v; want the new recorded total alone", got)
	}
}

// An ended session is priced again whenever the price table is, so a table
// that was missing when it was read, or that changed, reaches it later.
func TestEndedSessionIsPricedWithTheTableOfNow(t *testing.T) {
	var b bytes.Buffer
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 0, 0, 0, nil)
	tl := tailOf(t, b.Bytes()) // read with no table
	s := &sess{d: derived{spend: buildSpend(&tl.spend, nil, fixedNow.Add(-turnWindow))}}
	if got := s.pricedSpend(nil); got.Priced || got.USD != 0 || got.Tokens.Output != 100 {
		t.Fatalf("no table: %+v", got)
	}
	if got := s.pricedSpend(testPrices); !got.Priced || !near(got.USD, (10*4+100*20)/1e6) || !got.Spans[0].Priced {
		t.Fatalf("table appeared: %+v", got)
	}
	higher := priceTable{"claude-opus-5-5": {In: f64(8), Out: f64(40), Write5m: f64(5), Read: f64(0.2)}}
	if got := s.pricedSpend(higher); !near(got.USD, (10*8+100*40)/1e6) {
		t.Fatalf("table changed: %+v", got)
	}
	if got := s.pricedSpend(higher); !near(got.USD, (10*8+100*40)/1e6) {
		t.Fatalf("same table again: %+v", got)
	}
}

func TestATableSomewhatOffTheRecordedTotalStillPrices(t *testing.T) {
	// A session resumed from another transcript has more recorded than counted.
	var b bytes.Buffer
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 900, 9000, 90000, 900, 0, nil) // $0.2061
	costState(&b, spendID, 1.2, false)
	if got := spendOf(&tailOf(t, b.Bytes()).spend, nil, testPrices, fixedNow.Add(-turnWindow)); !got.Spans[0].Priced {
		t.Fatalf("a ratio of 0.17 is not a bad table: %+v", got)
	}
	// And one that counts far more than Claude Code recorded is not trusted.
	b.Reset()
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 900, 9000, 90000, 900, 0, nil)
	costState(&b, spendID, 0.05, false)
	if got := spendOf(&tailOf(t, b.Bytes()).spend, nil, testPrices, fixedNow.Add(-turnWindow)); got.Spans[0].Priced || got.USD != 0.05 {
		t.Fatalf("a ratio of 4: %+v", got)
	}
}

// A transcript first opened for an ended session's last pass is read from its
// last endedReadCap bytes; what is before them still counts for spend.
func TestReadCapStillCountsTheHeadForSpend(t *testing.T) {
	var b bytes.Buffer
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-16T08:00:00Z", 10, 100, 0, 0, 0, nil)
	b.WriteString(`{"type":"user","pad":"` + strings.Repeat("x", 5000) + `"}` + "\n")
	reply(&b, spendID, "msg_2", "claude-opus-5-5", "2026-11-16T08:30:00Z", 5, 40, 0, 0, 0, nil)
	headLen := b.Len()
	b.WriteString(`{"type":"user","pad":"` + strings.Repeat("y", 3000) + `"}` + "\n")
	reply(&b, spendID, "msg_3", "claude-opus-5-5", "2026-11-17T08:00:00Z", 1, 7, 0, 0, 0, nil)
	costState(&b, spendID, 1.5, false)
	p := filepath.Join(t.TempDir(), spendID+".jsonl")
	writeFile(t, p, b.Bytes())
	read := func(ctx context.Context, tl *tailer) error {
		_, err := tl.update(ctx, fixedNow)
		return err
	}
	tl := newTailer(p, staticProber(t))
	tl.tab = nil
	tl.readCap = int64(b.Len() - headLen - 100) // the tail starts inside the second user line
	if err := read(t.Context(), tl); err != nil {
		t.Fatal(err)
	}
	if tl.headRead == 0 || tl.read > tl.readCap {
		t.Fatalf("head read %d, tail read %d (cap %d)", tl.headRead, tl.read, tl.readCap)
	}
	got := spendOf(&tl.spend, nil, testPrices, fixedNow.Add(-turnWindow))
	want := usage{in: 16, out: 147}
	if sum := sumUsage(tl.spend.total); sum != want {
		t.Fatalf("total = %+v; want %+v", sum, want)
	}
	if !got.Recorded || got.USD != 1.5 || len(got.Spans) != 3 {
		t.Fatalf("got %+v; want the recorded total and three spans", got)
	}
	// The head scan stops with the context and goes on where it stopped.
	var c bytes.Buffer
	reply(&c, spendID, "msg_1", "claude-opus-5-5", "2026-11-16T08:00:00Z", 10, 100, 0, 0, 0, nil)
	for range 3 * ctxCheckLines {
		c.WriteString(`{"type":"user"}` + "\n")
	}
	reply(&c, spendID, "msg_2", "claude-opus-5-5", "2026-11-16T08:30:00Z", 5, 40, 0, 0, 0, nil)
	headLen = c.Len()
	c.WriteString(`{"type":"user","pad":"` + strings.Repeat("y", 3000) + `"}` + "\n")
	p = filepath.Join(t.TempDir(), spendID+".jsonl")
	writeFile(t, p, c.Bytes())
	tl = newTailer(p, staticProber(t))
	tl.readCap = int64(c.Len() - headLen - 100)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := read(ctx, tl); err == nil || tl.headPos >= tl.headEnd || tl.headPos == 0 {
		t.Fatalf("a stopped scan must report it and keep its place: %v, at %d of %d", err, tl.headPos, tl.headEnd)
	}
	if err := read(t.Context(), tl); err != nil {
		t.Fatal(err)
	}
	if sum := sumUsage(tl.spend.total); sum != (usage{in: 15, out: 140}) {
		t.Fatalf("after a resume: total = %+v", sum)
	}
}

func sumUsage(m map[priceKey]usage) (s usage) {
	for _, u := range m {
		s = s.add(u)
	}
	return s
}

// A cut that lands exactly on a line start: the read of the rest drops that
// line, so the head's read must have it.
func TestReadCapCutOnALineStartLosesNoReply(t *testing.T) {
	var a, b bytes.Buffer
	reply(&a, spendID, "msg_1", "claude-opus-5-5", "2026-11-16T08:00:00Z", 10, 100, 0, 0, 0, nil)
	reply(&b, spendID, "msg_2", "claude-opus-5-5", "2026-11-16T08:30:00Z", 5, 40, 0, 0, 0, nil)
	p := filepath.Join(t.TempDir(), spendID+".jsonl")
	writeFile(t, p, append(a.Bytes(), b.Bytes()...))
	tl := newTailer(p, staticProber(t))
	tl.readCap = int64(b.Len())
	if _, err := tl.update(t.Context(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if sum := sumUsage(tl.spend.total); sum != (usage{in: 15, out: 140}) {
		t.Fatalf("total = %+v", sum)
	}
}

// An ended session's tailers go after its last pass; if it comes back (it was
// resumed) its transcripts are read again from the start, and its replies
// count again, with nothing left of the claims of the first reading.
func TestASessionReadAgainCountsItsRepliesAgain(t *testing.T) {
	ix := newIndexer(newAdapter(t, Options{Home: t.TempDir(), Now: func() time.Time { return fixedNow }}))
	var b bytes.Buffer
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 0, 0, 0, nil)
	p := filepath.Join(t.TempDir(), spendID+".jsonl")
	writeFile(t, p, b.Bytes())
	j := job{id: spendID, main: p, subDirs: []string{filepath.Join(filepath.Dir(p), spendID, "subagents")}, ended: true, gen: 1}
	if got := sumUsage(ix.tail(t.Context(), p, spendID, fixedNow, true, nil).spend.total).out; got != 100 {
		t.Fatalf("first reading: %d", got)
	}
	ix.finish(j)
	if got := sumUsage(ix.tail(t.Context(), p, spendID, fixedNow, false, nil).spend.total).out; got != 100 {
		t.Fatalf("read again: %d; want the reply counted again", got)
	}
	// A transcript no longer read gives up its claims too.
	ix.forget(map[string]job{spendID: {id: spendID, main: filepath.Join(filepath.Dir(p), "other.jsonl")}})
	if got := sumUsage(ix.tail(t.Context(), p, spendID, fixedNow, false, nil).spend.total).out; got != 100 {
		t.Fatalf("after forget: %d; want the reply counted again", got)
	}
}

// A subagent's reply with no time, in a resumed session: whether it came
// before the exit is not known, so it is counted after it but not priced.
func TestASubagentsReplyWithNoTimeIsNotPricedAfterAResume(t *testing.T) {
	l := spendLog{sub: true, costOn: true, resumeAt: fixedNow.Add(-time.Hour)}
	l.reply("claude-opus-5-5", &usageJSON{Output: 10}, time.Time{}, testPrices)
	if !l.after || sumUsage(l.since).out != 10 || sumUsage(l.since).unpriced != 1 {
		t.Fatalf("since = %+v; want counted and marked unpriced", l.since)
	}
	for k, u := range l.since {
		if c, ok := testPrices.cost(k, u); ok || c != 0 {
			t.Fatalf("since %+v cost %v %v; want no dollars, partly priced", k, c, ok)
		}
	}
	// In the figure: the recorded total stays as it was, and says it is not whole.
	main := &spendLog{recorded: &recordedCost{usd: 1}}
	got := spendOf(main, []*spendLog{&l}, testPrices, fixedNow.Add(-spendKeep))
	if !near(got.USD, 1) || got.Priced {
		t.Fatalf("figure = %+v; want the recorded $1 and not priced", got)
	}
}

// A session resumed in another folder goes on in a transcript of the same
// name in that folder's project directory. Both are the session's: the card
// follows the newer one, and the use of both counts, live and after a
// restart. The older one's cost-state line is not the session's last.
func TestASessionResumedInAnotherFolderCountsBothTranscripts(t *testing.T) {
	const betaF = "/home/dev/projects/beta"
	setupIn := func(e *env, oldF, curF string) {
		writeConfig(t, e.home, map[string]any{"additionalModelCostsCache": map[string]any{"claude-opus-5-5": opusPrice}})
		var old, cur bytes.Buffer
		reply(&old, coordID, "msg_old", "claude-opus-5-5", "2026-11-17T07:00:00Z", 0, 3000, 0, 0, 0, nil)
		costState(&old, coordID, 99, false)
		p := filepath.Join(e.home, ".claude", "projects", projectDirName(oldF), coordID+".jsonl")
		writeFile(t, p, old.Bytes())
		at := fixedNow.Add(-2 * time.Hour)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		cur.Write(fixture(t, "coordinator.jsonl"))
		reply(&cur, coordID, "msg_new", "claude-opus-5-5", "2026-11-17T08:20:00Z", 0, 1000, 0, 0, 0, nil)
		tr := e.transcript(curF, coordID, "coordinator.jsonl")
		writeFile(t, tr, cur.Bytes())
	}
	setup := func(e *env) { setupIn(e, betaF, alphaF) }
	check := func(t *testing.T, f model.SessionFact) {
		t.Helper()
		// The fixture's own reply counts 1 output token.
		if f.Spend.Recorded || f.Spend.Tokens.Output != 4001 || !near(f.Spend.USD, 4001*20/1e6+1/1e6*4) {
			t.Fatalf("Spend = %+v; want both transcripts' use, priced", f.Spend)
		}
		var days int64
		for _, s := range f.Spend.Spans {
			days += s.Tokens.Output
		}
		if days != 4001 {
			t.Fatalf("Spans = %+v; want both transcripts' use", f.Spend.Spans)
		}
	}
	t.Run("live", func(t *testing.T) {
		e := newEnv(t)
		e.setProc(4242, procStart)
		e.record(4242, nil)
		setup(e)
		c := run(t, e.adapter())
		check(t, c.wait(t, 2*time.Second, "spend", session(coordID, func(f model.SessionFact) bool {
			return f.Spend.Tokens.Output >= 4001
		})).(model.SessionFact))
	})
	t.Run("resumed while watched", func(t *testing.T) {
		// It runs in beta first; then it is resumed in alpha.
		e := newEnv(t)
		e.setProc(4242, procStart)
		e.record(4242, func(m map[string]any) { m["cwd"] = betaF })
		writeConfig(t, e.home, map[string]any{"additionalModelCostsCache": map[string]any{"claude-opus-5-5": opusPrice}})
		var old bytes.Buffer
		reply(&old, coordID, "msg_old", "claude-opus-5-5", "2026-11-17T07:00:00Z", 0, 3000, 0, 0, 0, nil)
		writeFile(t, filepath.Join(e.home, ".claude", "projects", projectDirName(betaF), coordID+".jsonl"), old.Bytes())
		c := run(t, e.adapter())
		c.wait(t, 2*time.Second, "beta", session(coordID, func(f model.SessionFact) bool { return f.Spend.Tokens.Output == 3000 }))
		var cur bytes.Buffer
		cur.Write(fixture(t, "coordinator.jsonl"))
		reply(&cur, coordID, "msg_new", "claude-opus-5-5", "2026-11-17T08:20:00Z", 0, 1000, 0, 0, 0, nil)
		writeFile(t, filepath.Join(e.home, ".claude", "projects", projectDirName(alphaF), coordID+".jsonl"), cur.Bytes())
		e.record(4242, func(m map[string]any) { m["cwd"] = alphaF })
		f := c.wait(t, 2*time.Second, "alpha", session(coordID, func(f model.SessionFact) bool {
			return f.Spend.Tokens.Output >= 4001
		})).(model.SessionFact)
		check(t, f)
		if f.Folder != alphaF {
			t.Fatalf("Folder = %q; want the folder it was resumed in", f.Folder)
		}
	})
	t.Run("ended with a cost line in each", func(t *testing.T) {
		// The older transcript's own cost-state line, and a reply below
		// it, do not make its use count as after the session's last line.
		e := newEnv(t)
		var old, cur bytes.Buffer
		reply(&old, coordID, "msg_old", "claude-opus-5-5", "2026-11-17T07:00:00Z", 0, 3000, 0, 0, 0, nil)
		costState(&old, coordID, 99, false)
		reply(&old, coordID, "msg_old2", "claude-opus-5-5", "2026-11-17T07:30:00Z", 0, 10, 0, 0, 0, nil)
		p := filepath.Join(e.home, ".claude", "projects", projectDirName(betaF), coordID+".jsonl")
		writeFile(t, p, old.Bytes())
		at := fixedNow.Add(-2 * time.Hour)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		cur.Write(fixture(t, "coordinator.jsonl"))
		reply(&cur, coordID, "msg_new", "claude-opus-5-5", "2026-11-17T08:20:00Z", 0, 1000, 0, 0, 0, nil)
		costState(&cur, coordID, 150, false)
		writeFile(t, e.transcript(alphaF, coordID, "coordinator.jsonl"), cur.Bytes())
		c := run(t, e.adapter())
		f := c.wait(t, 2*time.Second, "spend", session(coordID, func(f model.SessionFact) bool {
			return f.Spend.Recorded || f.Spend.USD > 100
		})).(model.SessionFact)
		if !f.Spend.Recorded || f.Spend.USD != 150 {
			t.Fatalf("Spend = %+v; want the main transcript's recorded total alone", f.Spend)
		}
	})
	t.Run("restart, the newer transcript listed last", func(t *testing.T) {
		e := newEnv(t)
		setupIn(e, alphaF, betaF)
		c := run(t, e.adapter())
		check(t, c.wait(t, 2*time.Second, "spend", session(coordID, func(f model.SessionFact) bool {
			return f.Spend.Tokens.Output >= 4001
		})).(model.SessionFact))
	})
	t.Run("restart", func(t *testing.T) {
		e := newEnv(t)
		setup(e)
		c := run(t, e.adapter())
		check(t, c.wait(t, 2*time.Second, "spend", session(coordID, func(f model.SessionFact) bool {
			return f.Spend.Tokens.Output >= 4001
		})).(model.SessionFact))
	})
}

// A part with no rate leaves the rest priced; a part not used needs none.
func TestAPartWithNoRateLeavesTheRestPriced(t *testing.T) {
	noWeb := priceTable{"claude-opus-5-5": {In: f64(4), Out: f64(20), Write5m: f64(5), Read: f64(0.2)}}
	k := priceKey{model: "claude-opus-5-5"}
	usd, all := noWeb.cost(k, usage{in: 1000, out: 1000, web: 2})
	if all || !near(usd, (1000*4+1000*20)/1e6) {
		t.Fatalf("cost = %v, %v; want the tokens priced and the searches not", usd, all)
	}
	noRead := priceTable{"claude-opus-5-5": {In: f64(4), Out: f64(20), Write5m: f64(5)}}
	if usd, all := noRead.cost(k, usage{in: 1000, out: 1000}); !all || !near(usd, (1000*4+1000*20)/1e6) {
		t.Fatalf("cost = %v, %v; want priced whole: no cache read was used", usd, all)
	}
	// Through priceSpend: the card and the day keep the priced part.
	var b bytes.Buffer
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 1000, 1000, 0, 0, 0,
		map[string]any{"server_tool_use": map[string]any{"web_search_requests": 2}})
	got := spendOf(&tailOf(t, b.Bytes()).spend, nil, noWeb, fixedNow.Add(-spendKeep))
	if got.Priced || !near(got.USD, 0.024) || len(got.Spans) != 1 || got.Spans[0].Priced || !near(got.Spans[0].USD, 0.024) {
		t.Fatalf("Spend = %+v; want $0.024 and partly priced", got)
	}
}

// usage.iterations as seen (one "message" entry, the top-level counts) is
// priced from the top level; any other shape, or a speed whose rates are not
// known, is counted and marked partly priced.
func TestReplyShapesThatAreNotPricedWhole(t *testing.T) {
	one := []any{map[string]any{"type": "message", "input_tokens": 10, "output_tokens": 100}}
	for _, c := range []struct {
		name  string
		extra map[string]any
		whole bool
		usd   bool // the top-level counts are charged (an unknown speed has no rates: nothing is)
	}{
		{"one iteration", map[string]any{"iterations": one}, true, true},
		{"no iterations", map[string]any{"iterations": []any{}}, true, true},
		{"two iterations", map[string]any{"iterations": append(one, one[0])}, false, true},
		{"another kind", map[string]any{"iterations": []any{map[string]any{"type": "tool"}}}, false, true},
		{"another model", map[string]any{"iterations": []any{map[string]any{"type": "message", "model": "claude-haiku-5-5"}}}, false, true},
		{"fast", map[string]any{"speed": "fast"}, true, true},
		{"unknown speed", map[string]any{"speed": "turbo"}, false, false},
	} {
		var b bytes.Buffer
		reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 0, 0, 0, c.extra)
		got := spendOf(&tailOf(t, b.Bytes()).spend, nil, testPrices, fixedNow.Add(-spendKeep))
		if got.Priced != c.whole || got.Tokens.Output != 100 || (got.USD > 0) != c.usd {
			t.Errorf("%s: %+v; want priced whole %v, tokens counted, dollars %v", c.name, got, c.whole, c.usd)
		}
	}
}

// The copies of one reply can grow (it was still streaming): the last, the
// largest, is what counts, in the total, the day and the use after a resume.
func TestAReplysLaterCopyCountsWhatItGrewBy(t *testing.T) {
	var b bytes.Buffer
	reply(&b, spendID, "msg_0", "claude-opus-5-5", "2026-11-17T07:00:00Z", 1, 1, 0, 0, 0, nil)
	costState(&b, spendID, 1, false)
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 5, 0, 0, 0, nil)
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 100, 0, 0, 0,
		map[string]any{"server_tool_use": map[string]any{"web_search_requests": 1}})
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 10, 40, 0, 0, 0, nil) // never less
	tl := tailOf(t, b.Bytes())
	if u := sumUsage(tl.spend.total); u.in != 11 || u.out != 101 || u.web != 1 {
		t.Fatalf("total = %+v; want msg_1 once, at its largest", u)
	}
	if u := sumUsage(tl.spend.since); u.out != 100 || u.web != 1 {
		t.Fatalf("since = %+v", u)
	}
	var day int64
	for k, u := range tl.spend.spans {
		if k.at.Hour() == 8 {
			day += u.out
		}
	}
	if day != 100 {
		t.Fatalf("spans = %+v", tl.spend.spans)
	}
}

// Copies of a reply another transcript of the session counted are not
// counted, growing or not; a reply of this one's own grows by its copies.
func TestCopiesOfAClaimedReplyAreNotCounted(t *testing.T) {
	cl := newReplyClaims()
	tails := func(name string, owner uint32, b []byte) *tailer {
		p := filepath.Join(t.TempDir(), name+".jsonl")
		writeFile(t, p, b)
		tl := newTailer(p, staticProber(t))
		tl.usageOwner, tl.claims, tl.owner = spendID, cl, owner
		if _, err := tl.update(t.Context(), fixedNow); err != nil {
			t.Fatal(err)
		}
		return tl
	}
	var a, b bytes.Buffer
	reply(&a, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 5, 0, 0, 0, nil)
	reply(&a, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 100, 0, 0, 0, nil)
	if got := sumUsage(tails("a", 1, a.Bytes()).spend.total).out; got != 100 {
		t.Fatalf("first: %d; want its reply at its largest", got)
	}
	reply(&b, spendID, "msg_2", "claude-opus-5-5", "2026-11-17T08:10:00Z", 0, 7, 0, 0, 0, nil)
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 5, 0, 0, 0, nil)
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 100, 0, 0, 0, nil)
	reply(&b, spendID, "msg_2", "claude-opus-5-5", "2026-11-17T08:10:00Z", 0, 70, 0, 0, 0, nil) // not next to its first
	if got := sumUsage(tails("b", 2, b.Bytes()).spend.total).out; got != 70 {
		t.Fatalf("second: %d; want only its own reply, once, at its largest (another reply came between its copies)", got)
	}
}

// A reply that is not the last one read still grows by a later copy: another
// reply came in between. The two replies are in different spans, and each
// span has its own reply's figures.
func TestALaterCopyAfterAnotherReplyRaisesTheFigure(t *testing.T) {
	var b bytes.Buffer
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 5, 0, 0, 0, nil)
	reply(&b, spendID, "msg_2", "claude-opus-5-5", "2026-11-17T08:30:00Z", 0, 7, 0, 0, 0, nil)
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 100, 0, 0, 0, nil)
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 30, 0, 0, 0, nil) // never less
	got := spendOf(&tailOf(t, b.Bytes()).spend, nil, testPrices, fixedNow.Add(-spendKeep))
	if got.Tokens.Output != 107 || !near(got.USD, 107*20/1e6) {
		t.Fatalf("total = %+v; want 100 + 7", got)
	}
	want := []struct {
		at  string
		out int64
	}{{"2026-11-17T08:00:00Z", 100}, {"2026-11-17T08:30:00Z", 7}}
	if len(got.Spans) != len(want) {
		t.Fatalf("spans = %+v; want two", got.Spans)
	}
	for i, w := range want {
		sp := got.Spans[i]
		if !sp.At.Equal(parseTS(w.at, fixedNow)) || sp.Tokens.Output != w.out || !near(sp.USD, float64(w.out)*20/1e6) || !sp.Priced {
			t.Errorf("span %d = %+v; want %s, %d output tokens and their dollars", i, sp, w.at, w.out)
		}
	}
}

// The larger copy is in another transcript of the session (a subagent's): it
// raises what the transcript that counted the reply holds, and the other one
// adds nothing of it.
func TestALargerCopyInAnotherTranscriptRaisesTheFigure(t *testing.T) {
	cl := newReplyClaims()
	tails := func(name string, owner uint32, b []byte) *tailer {
		p := filepath.Join(t.TempDir(), name+".jsonl")
		writeFile(t, p, b)
		tl := newTailer(p, staticProber(t))
		tl.usageOwner, tl.claims, tl.owner = spendID, cl, owner
		if _, err := tl.update(t.Context(), fixedNow); err != nil {
			t.Fatal(err)
		}
		return tl
	}
	var a, b bytes.Buffer
	reply(&a, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 5, 0, 0, 0, nil)
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 100, 0, 0, 0, nil)
	main, sub := tails("a", 1, a.Bytes()), tails("b", 2, b.Bytes())
	if m, s := sumUsage(main.spend.total).out, sumUsage(sub.spend.total).out; m != 100 || s != 0 {
		t.Fatalf("main %d, sub %d; want the reply once, at its largest, in the transcript that counted it", m, s)
	}
}

// What a transcript counted is given up when it is read again from the start,
// and counted afresh: the reply is not taken for one another transcript counted,
// and an old claim does not reach into the new log.
func TestAReadAgainCountsItsRepliesAfresh(t *testing.T) {
	cl := newReplyClaims()
	var a bytes.Buffer
	reply(&a, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 5, 0, 0, 0, nil)
	reply(&a, spendID, "msg_2", "claude-opus-5-5", "2026-11-17T08:30:00Z", 0, 7, 0, 0, 0, nil)
	reply(&a, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 100, 0, 0, 0, nil)
	p := filepath.Join(t.TempDir(), "a.jsonl")
	writeFile(t, p, a.Bytes())
	tl := newTailer(p, staticProber(t))
	tl.usageOwner, tl.claims, tl.owner = spendID, cl, 1
	if _, err := tl.update(t.Context(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if got := sumUsage(tl.spend.total).out; got != 107 {
		t.Fatalf("first read: %d; want 107", got)
	}
	// The file is replaced by a shorter one: the tailer reads it from the start.
	var c bytes.Buffer
	reply(&c, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 5, 0, 0, 0, nil)
	reply(&c, spendID, "msg_2", "claude-opus-5-5", "2026-11-17T08:30:00Z", 0, 7, 0, 0, 0, nil)
	writeFile(t, p, c.Bytes())
	if _, err := tl.update(t.Context(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if got := sumUsage(tl.spend.total).out; got != 12 {
		t.Fatalf("read again: %d; want 12, not the replies lost to the claims of the old log", got)
	}
}

// An unknown speed has no rates: its tokens are counted and no dollars are
// added, not even at the standard rates.
func TestAnUnknownSpeedReplyAddsNoDollars(t *testing.T) {
	turbo := map[string]any{"speed": "turbo"}
	var b bytes.Buffer
	reply(&b, spendID, "msg_1", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 1_000_000, 0, 0, 0, turbo)
	got := spendOf(&tailOf(t, b.Bytes()).spend, nil, testPrices, fixedNow.Add(-spendKeep))
	if got.USD != 0 || got.Priced || got.Tokens.Output != 1_000_000 {
		t.Fatalf("all unknown: %+v; want tokens only, no dollars, not priced", got)
	}
	for _, sp := range got.Spans {
		if sp.USD != 0 || sp.Priced {
			t.Fatalf("day %+v; want no dollars", sp)
		}
	}
	reply(&b, spendID, "msg_2", "claude-opus-5-5", "2026-11-17T08:10:00Z", 0, 1_000_000, 0, 0, 0, nil)
	got = spendOf(&tailOf(t, b.Bytes()).spend, nil, testPrices, fixedNow.Add(-spendKeep))
	if !near(got.USD, 20) || got.Priced || got.Tokens.Output != 2_000_000 {
		t.Fatalf("mixed: %+v; want only the priced reply's $20, not priced, both in tokens", got)
	}
	// After a resume: the recorded total plus the priced use only.
	var r bytes.Buffer
	costState(&r, spendID, 1, false)
	reply(&r, spendID, "msg_3", "claude-opus-5-5", "2026-11-17T09:00:00Z", 0, 1_000_000, 0, 0, 0, turbo)
	got = spendOf(&tailOf(t, r.Bytes()).spend, nil, testPrices, fixedNow.Add(-spendKeep))
	if !near(got.USD, 1) || got.Priced {
		t.Fatalf("resumed: %+v; want the recorded $1 and not priced", got)
	}
}

// Two dated entries for one model: the same prices give a price; different
// ones give none, whatever order the table's map gives them in.
func TestConflictingDatedEntriesGiveNoPrice(t *testing.T) {
	a := price{In: f64(4), Out: f64(20)}
	b := price{In: f64(8), Out: f64(40)}
	for i := 0; i < 20; i++ {
		same := priceTable{"claude-opus-5-5-20261001": a, "claude-opus-5-5-20261101": {In: f64(4), Out: f64(20)}}
		if p, ok := same.lookup("claude-opus-5-5"); !ok || *p.In != 4 {
			t.Fatalf("same prices: %+v, %v", p, ok)
		}
		apart := priceTable{"claude-opus-5-5-20261001": a, "claude-opus-5-5-20261101": b}
		if _, ok := apart.lookup("claude-opus-5-5"); ok {
			t.Fatal("different prices: priced; want no price")
		}
	}
}

// The first day the page shows starts at most 7 days and an hour (a
// fall-back night) before now: a session that ended 7 days and 12 hours ago
// can hold use of that day, so it is still found and read.
func TestASessionEndedMoreThanSevenDaysAgoStillGivesItsDays(t *testing.T) {
	e := newEnv(t)
	writeConfig(t, e.home, map[string]any{"additionalModelCostsCache": map[string]any{"claude-opus-5-5": opusPrice}})
	at := fixedNow.Add(-7*24*time.Hour - 12*time.Hour)
	var b bytes.Buffer
	reply(&b, endedID, "msg_1", "claude-opus-5-5", at.UTC().Format(time.RFC3339), 0, 100, 0, 0, 0, nil)
	e.endedTranscript(endedID, b.String(), at)
	c := run(t, e.adapter())
	f := c.wait(t, 2*time.Second, "spend", session(endedID, func(f model.SessionFact) bool { return len(f.Spend.Spans) > 0 })).(model.SessionFact)
	if f.Spend.Spans[0].Tokens.Output < 100 {
		t.Fatalf("Spans = %+v", f.Spend.Spans)
	}
}

// A long transcript read from cold: spans older than the window are not kept
// while reading, so they cannot fill the cap before this week's use is read.
func TestOldSpansDoNotCrowdOutThisWeek(t *testing.T) {
	var b bytes.Buffer
	old := fixedNow.Add(-spendKeep - 24*time.Hour)
	for i := 0; i <= maxSpendSpans; i++ {
		at := old.Add(-time.Duration(i) * model.SpendSpanLength)
		reply(&b, spendID, "msg_old_"+strconv.Itoa(i), "claude-opus-5-5", at.UTC().Format(time.RFC3339), 0, 1, 0, 0, 0, nil)
	}
	reply(&b, spendID, "msg_now", "claude-opus-5-5", "2026-11-17T08:00:00Z", 0, 100, 0, 0, 0, nil)
	tl := tailOf(t, b.Bytes())
	if len(tl.spend.spans) != 1 {
		t.Fatalf("spans = %d; want this week's one", len(tl.spend.spans))
	}
	if u := sumUsage(tl.spend.total); u.out != maxSpendSpans+1+100 {
		t.Fatalf("total out = %d; want every reply in the session's total", u.out)
	}
}

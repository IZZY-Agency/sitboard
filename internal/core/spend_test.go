package core

import (
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

func hour(at time.Time, out int64, usd float64, priced bool) model.SpendSpan {
	return model.SpendSpan{At: at.UTC().Truncate(model.SpendSpanLength), Tokens: model.Tokens{Input: 1, Output: out, CacheRead: 100, CacheWrite: 10}, USD: usd, Priced: priced}
}

func TestCardCarriesItsSessionSpend(t *testing.T) {
	b := newTestBoard(t)
	s := busySession("s1", tNow)
	s.Spend = model.Spend{Tokens: model.Tokens{Input: 3, Output: 400, CacheRead: 5000, CacheWrite: 60}, USD: 2.5, Priced: true, Recorded: true}
	b.applySync(s)
	got := onlyCard(t, b).Spend
	want := contract.Spend{Tokens: contract.Tokens{Input: 3, Output: 400, CacheRead: 5000, CacheWrite: 60}, USD: 2.5, Priced: true, Recorded: true}
	if got != want {
		t.Fatalf("Spend = %+v; want %+v", got, want)
	}
	// Part of it unpriced: the priced amount stays, and the figure says it is not complete.
	s.Spend.Priced = false
	b.applySync(s)
	if got := onlyCard(t, b).Spend; got.Priced || got.USD != 2.5 || got.Tokens.Output != 400 {
		t.Fatalf("unpriced Spend = %+v; want the priced part, marked not priced", got)
	}
}

func TestSpendSummaryByDayAndProject(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	day := func(d int) string { return now.AddDate(0, 0, d).Format(time.DateOnly) }
	a := busySession("a", now)
	a.Spend = model.Spend{Spans: []model.SpendSpan{
		hour(now.AddDate(0, 0, -7), 999, 99, true), // before the 7 days
		hour(now.AddDate(0, 0, -1), 10, 1, true),
		hour(now, 20, 2, true),
	}}
	// An ended session has no card, but its spend still counts.
	ended := session("ended", model.StatusFinished, now)
	ended.Live, ended.Ended, ended.EndedAt = model.Of(false), true, now
	ended.Spend = model.Spend{Spans: []model.SpendSpan{hour(now, 5, 0.5, true)}}
	// Another project, with a model that has no price.
	c := busySession("c", now)
	c.Folder = beta
	c.Spend = model.Spend{Spans: []model.SpendSpan{hour(now, 7, 0, false)}}
	for _, s := range []model.SessionFact{a, ended, c} {
		e.board.applySync(s)
	}
	got := e.board.Snapshot().Spend.Days
	if len(got) != 2 || got[0].Day != day(-1) || got[1].Day != day(0) {
		t.Fatalf("Days = %+v; want yesterday and today", got)
	}
	y := got[0]
	if y.Spend.Tokens.Output != 10 || !y.Spend.Priced || y.Spend.USD != 1 || len(y.Projects) != 1 || y.Projects[0].Name != "alpha" {
		t.Fatalf("yesterday = %+v", y)
	}
	td := got[1]
	if td.Spend.Tokens.Output != 32 || td.Spend.Priced || td.Spend.USD != 2.5 {
		t.Fatalf("today = %+v; want 32 output tokens, the priced $2.5, marked not priced", td.Spend)
	}
	if len(td.Projects) != 2 {
		t.Fatalf("today's projects = %+v", td.Projects)
	}
	pa, pb := td.Projects[0], td.Projects[1]
	if pa.Name != "alpha" || pa.Path != "~/projects/alpha" || pa.ProjectID != contract.ProjectID(alpha) ||
		pa.Spend.Tokens.Output != 25 || !pa.Spend.Priced || pa.Spend.USD != 2.5 {
		t.Fatalf("alpha today = %+v", pa)
	}
	if pb.Name != "beta" || pb.Spend.Priced || pb.Spend.USD != 0 || pb.Spend.Tokens.Output != 7 {
		t.Fatalf("beta today = %+v", pb)
	}
}

func TestSpendSummaryIsEmptyWithoutUse(t *testing.T) {
	b := newTestBoard(t)
	b.applySync(busySession("s1", tNow))
	if d := b.Snapshot().Spend.Days; d == nil || len(d) != 0 {
		t.Fatalf("Days = %#v; want an empty list", d)
	}
}

func TestSpendSummaryLeavesOutSessionsOutsideTheRoots(t *testing.T) {
	t.Setenv("HOME", testHome)
	b := newBoard(t, Options{Now: func() time.Time { return tNow }, Roots: []string{"~/projects/alpha"}})
	in := busySession("in", tNow)
	in.Spend = model.Spend{Spans: []model.SpendSpan{hour(tNow, 10, 1, true)}}
	out := busySession("out", tNow)
	out.Folder = beta
	out.Spend = model.Spend{Spans: []model.SpendSpan{hour(tNow, 777, 7, true)}}
	// A prefix match is not "under", and a session with no folder is outside any root.
	sibling := busySession("sibling", tNow)
	sibling.Folder = alpha + "-other"
	sibling.Spend = out.Spend
	nowhere := busySession("nowhere", tNow)
	nowhere.Folder = ""
	nowhere.Spend = out.Spend
	for _, s := range []model.SessionFact{in, out, sibling, nowhere} {
		b.applySync(s)
	}
	days := b.Snapshot().Spend.Days
	if len(days) != 1 || len(days[0].Projects) != 1 || days[0].Spend.Tokens.Output != 10 || days[0].Projects[0].Name != "alpha" {
		t.Fatalf("Days = %+v; want only the session under the root", days)
	}
}

// Days are the machine's local days: use just before and just after a local
// midnight lands on two days, whatever the zone's offset. Zones whose midnight
// falls at :30 or :45 UTC are the ones a span longer than a quarter of an hour
// would straddle.
func TestSpendSummaryDaysAreLocal(t *testing.T) {
	t.Setenv("HOME", testHome)
	for _, z := range []struct {
		name   string
		offset time.Duration
	}{
		{"UTC+5:30", 5*time.Hour + 30*time.Minute},
		{"UTC+5:45", 5*time.Hour + 45*time.Minute},
		{"UTC+12:45", 12*time.Hour + 45*time.Minute},
		{"UTC-3:30", -(3*time.Hour + 30*time.Minute)},
	} {
		t.Run(z.name, func(t *testing.T) {
			zone := time.FixedZone(z.name, int(z.offset/time.Second))
			now := time.Date(2026, 11, 17, 10, 0, 0, 0, zone)
			b := newBoard(t, Options{Now: func() time.Time { return now }})
			s := busySession("s1", now)
			late := time.Date(2026, 11, 16, 23, 50, 0, 0, zone)
			early := time.Date(2026, 11, 17, 0, 10, 0, 0, zone)
			s.Spend = model.Spend{Spans: []model.SpendSpan{hour(late, 10, 1, true), hour(early, 20, 2, true)}}
			b.applySync(s)
			days := b.Snapshot().Spend.Days
			if len(days) != 2 || days[0].Day != "2026-11-16" || days[0].Spend.Tokens.Output != 10 || days[1].Day != "2026-11-17" || days[1].Spend.Tokens.Output != 20 {
				t.Fatalf("Days = %+v; want 10 on the 16th and 20 on the 17th", days)
			}
		})
	}
}

// The summary covers today and the six days before it, from the first local
// midnight: use in the first quarter hour of that day counts, use in the last
// quarter hour of the day before does not.
func TestSpendSummaryFirstDayStartsAtLocalMidnight(t *testing.T) {
	t.Setenv("HOME", testHome)
	zone := time.FixedZone("UTC+5:30", 5*3600+30*60)
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, zone)
	b := newBoard(t, Options{Now: func() time.Time { return now }})
	s := busySession("s1", now)
	first := time.Date(2026, 11, 11, 0, 0, 0, 0, zone)
	s.Spend = model.Spend{Spans: []model.SpendSpan{
		hour(first.Add(-15*time.Minute), 999, 99, true), // 23:45 on the 10th: out
		hour(first, 10, 1, true),                        // 00:00 on the 11th: in
	}}
	b.applySync(s)
	days := b.Snapshot().Spend.Days
	if len(days) != 1 || days[0].Day != "2026-11-11" || days[0].Spend.Tokens.Output != 10 {
		t.Fatalf("Days = %+v; want only the 11th, with 10", days)
	}
}

// Some use unpriced: the sum keeps the priced part, in the day and the project.
func TestSpendSummaryKeepsThePricedPartOfAnUnpricedDay(t *testing.T) {
	b := newTestBoard(t)
	s := busySession("s1", tNow)
	s.Spend = model.Spend{Spans: []model.SpendSpan{hour(tNow, 10, 1.25, false), hour(tNow.Add(-time.Minute*30), 5, 2, true)}}
	b.applySync(s)
	d := b.Snapshot().Spend.Days
	if len(d) != 1 || d[0].Spend.Priced || d[0].Spend.USD != 3.25 || d[0].Projects[0].Spend.Priced || d[0].Projects[0].Spend.USD != 3.25 {
		t.Fatalf("Days = %+v; want $3.25 not priced", d)
	}
}

func TestSpendSummaryPendingWhileOlderSessionsAreRead(t *testing.T) {
	e := newEnv(t)
	e.board.SetSources([]source.Status{{Kind: source.KindClaude, State: source.StateOK}})
	if !e.board.Snapshot().Spend.Pending {
		t.Fatal("not pending")
	}
	e.board.applySync(model.BackfillDone{Kind: string(source.KindClaude)})
	if e.board.Snapshot().Spend.Pending {
		t.Fatal("still pending")
	}
}

// A session with no spend (here a Codex one) adds nothing to the days,
// and does not make a priced day "no price".
func TestASessionWithNoSpendLeavesTheDaysAlone(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	a := busySession("a", now)
	a.Spend = model.Spend{Spans: []model.SpendSpan{hour(now, 20, 2, true)}}
	cx := busySession("cx", now)
	cx.Key.Tool = model.ToolCodex
	e.board.applySync(a)
	e.board.applySync(cx)
	got := e.board.Snapshot().Spend.Days
	if len(got) != 1 || !got[0].Spend.Priced || got[0].Spend.USD != 2 || len(got[0].Projects) != 1 || !got[0].Projects[0].Spend.Priced {
		t.Fatalf("Days = %+v; want the priced day alone", got)
	}
}

// Codex keeps no price: its tokens sit beside Claude Code's money in each day
// and project, never in it, so a priced day stays priced. A day or a project
// with Codex use alone is listed, with no Claude Code spend.
func TestCodexTokensSitBesideTheMoney(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	day := func(d int) string { return now.AddDate(0, 0, d).Format(time.DateOnly) }
	codex := func(id, folder string, spans ...model.SpendSpan) model.SessionFact {
		s := busySession(id, now)
		s.Key.Tool, s.Folder = model.ToolCodex, folder
		for i := range spans {
			spans[i].USD, spans[i].Priced = 0, false
		}
		s.Spend = model.Spend{Spans: spans}
		return s
	}
	a := busySession("a", now)
	a.Spend = model.Spend{Spans: []model.SpendSpan{hour(now, 20, 2, true)}}
	e.board.applySync(a)
	e.board.applySync(codex("cx", alpha, hour(now.AddDate(0, 0, -1), 4, 0, false), hour(now, 30, 0, false)))
	e.board.applySync(codex("cy", beta, hour(now, 6, 0, false)))
	got := e.board.Snapshot().Spend.Days
	if len(got) != 2 || got[0].Day != day(-1) || got[1].Day != day(0) {
		t.Fatalf("Days = %+v; want yesterday (Codex alone) and today", got)
	}
	y := got[0]
	if y.Spend.Tokens != (contract.Tokens{}) || y.Spend.USD != 0 || !y.Spend.Priced || y.Codex.Output != 4 ||
		len(y.Projects) != 1 || y.Projects[0].Codex.Output != 4 || y.Projects[0].Spend.Tokens != (contract.Tokens{}) {
		t.Fatalf("yesterday = %+v; want Codex's 4 alone", y)
	}
	td := got[1]
	if !td.Spend.Priced || td.Spend.USD != 2 || td.Spend.Tokens.Output != 20 || td.Codex.Output != 36 || td.Codex.CacheRead != 200 {
		t.Fatalf("today = %+v; want Claude Code's priced $2 for 20, and Codex's 36 beside it", td)
	}
	if len(td.Projects) != 2 {
		t.Fatalf("today's projects = %+v", td.Projects)
	}
	pa, pb := td.Projects[0], td.Projects[1]
	if pa.Name != "alpha" || !pa.Spend.Priced || pa.Spend.USD != 2 || pa.Spend.Tokens.Output != 20 || pa.Codex.Output != 30 {
		t.Fatalf("alpha today = %+v", pa)
	}
	if pb.Name != "beta" || pb.Spend.Tokens != (contract.Tokens{}) || pb.Spend.USD != 0 || !pb.Spend.Priced || pb.Codex.Output != 6 {
		t.Fatalf("beta today = %+v; want Codex's 6 alone", pb)
	}
}

// Codex's catch-up of its ended threads (#114) is read once at start: the
// days are pending until Codex says it is done, as for Claude Code. The
// catch-up panel lists no Codex item, so it does not wait for Codex.
func TestSpendSummaryPendingWhileCodexReadsEndedThreads(t *testing.T) {
	e := newEnv(t)
	e.board.SetSources([]source.Status{{Kind: source.KindCodex, State: source.StateOK}})
	if !e.board.Snapshot().Spend.Pending {
		t.Fatal("not pending before BackfillDone of Codex")
	}
	if catchup(t, e.board).Pending {
		t.Fatal("the catch-up panel waits for Codex")
	}
	e.board.applySync(model.BackfillDone{Kind: string(source.KindClaude)})
	if !e.board.Snapshot().Spend.Pending {
		t.Fatal("Claude Code's BackfillDone ended Codex's wait")
	}
	e.board.applySync(model.BackfillDone{Kind: string(source.KindCodex)})
	if e.board.Snapshot().Spend.Pending {
		t.Fatal("still pending after BackfillDone of Codex")
	}
	e.board.SetSources([]source.Status{{Kind: source.KindCodex, State: source.StateMissing}})
	if e.board.Snapshot().Spend.Pending {
		t.Fatal("pending with Codex missing")
	}
}

// A caught-up Codex thread (Backfill, Ended) puts its tokens into the day and
// the project, never money, and is neither a card nor an item of the
// catch-up panel.
func TestCaughtUpCodexThreadCountsTokensAndIsNoCard(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	e.board.SetSources([]source.Status{{Kind: source.KindCodex, State: source.StateOK}})
	cx := busySession("cx", now)
	cx.Key.Tool, cx.Folder = model.ToolCodex, alpha
	cx.Status = model.Unknown[model.Status]()
	cx.Live, cx.Ended, cx.EndedAt, cx.Backfill = model.Of(false), true, now.Add(-3*time.Hour), true
	cx.TurnEnds, cx.CreatedPRs = nil, nil
	cx.Spend = model.Spend{Spans: []model.SpendSpan{hour(now, 12, 0, false)}}
	e.board.applySync(cx)
	snap := e.board.Snapshot()
	if len(snap.Cards) != 0 {
		t.Fatalf("Cards = %+v; want none", snap.Cards)
	}
	if len(snap.Spend.Days) != 1 {
		t.Fatalf("Days = %+v; want today alone", snap.Spend.Days)
	}
	d := snap.Spend.Days[0]
	if d.Codex.Output != 12 || d.Spend.Tokens != (contract.Tokens{}) || d.Spend.USD != 0 ||
		len(d.Projects) != 1 || d.Projects[0].Codex.Output != 12 || d.Projects[0].Spend.USD != 0 {
		t.Fatalf("today = %+v; want Codex's 12 alone, no money", d)
	}
	if items := catchup(t, e.board).Items; len(items) != 0 {
		t.Fatalf("catch-up items = %+v; want none", items)
	}
}

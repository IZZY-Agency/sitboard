package codex

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// TestMeasureStartupRead times the three steps of the start-up read on a real
// Codex home, apart: the walk for older rollouts (discovery), the read of
// them (parse) and the publishing of what was read. It runs only when
// SITBOARD_MEASURE_CODEX_HOME names a Codex home, and logs only counts, sizes
// and durations: no path, name or text (review of #152, round 4, finding 30).
// On a home shaped like the review's workload (328 rollouts, 879 MB) the
// read took about 1.1 s with the cache warm, the same whether or not lines the
// read has no use for were passed over undecoded, so they are decoded.
func TestMeasureStartupRead(t *testing.T) {
	home := os.Getenv("SITBOARD_MEASURE_CODEX_HOME")
	if home == "" {
		t.Skip("set SITBOARD_MEASURE_CODEX_HOME to a Codex home to measure")
	}
	now := time.Now()
	a := newAdapter(t, Options{CodexHome: home, Now: func() time.Time { return now }})
	a.holders = func(context.Context, []string) (map[string]int, bool) { return map[string]int{}, true }
	r := &registry{a: a, emit: func(model.Fact) {}, sessions: map[string]*sess{}, children: map[string]int{}, sent: map[string]model.SessionFact{}, located: map[string]string{}, notFound: map[string]time.Time{}, names: newNames(home + "/session_index.jsonl")}
	ctx := context.Background()

	a.tuis = func(context.Context) ([]tuiProc, bool) { return nil, true }
	start := time.Now()
	r.rescan(ctx) // the live scan: nothing held, so every session it lists has ended
	t.Logf("live scan: %v, %d sessions", time.Since(start), len(r.sessions))
	start = time.Now()
	older, cut := a.olderRollouts(ctx, catchupSince(now))
	discovery := time.Since(start)
	plan := r.planCatchup(older)
	var bytes int64
	for _, j := range plan.jobs {
		if fi, err := os.Stat(j.path); err == nil {
			bytes += fi.Size()
		}
	}
	t.Logf("discovery: %v, %d rollouts found, %d to read (%d MB), cut %v", discovery, len(older), len(plan.jobs), bytes>>20, cut || plan.cut)

	parse := func(label string) catchupResult {
		start := time.Now()
		res := readCatchup(ctx, plan, now)
		t.Logf("parse (%s): %v, %d threads with something to show, cut %v", label, time.Since(start), len(res.reads), res.cut)
		return res
	}
	parse("cold")
	res := parse("warm") // the file cache filled by the first

	start = time.Now()
	r.applyCatchup(res)
	batches := 1
	for r.publishCatchup() {
		batches++
	}
	t.Logf("publish: %v in %d batches", time.Since(start), batches)
}

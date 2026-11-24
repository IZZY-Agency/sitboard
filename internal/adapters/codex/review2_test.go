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
)

// The checked-in fork layout, with a pull request command in the copied
// history, read live from past the first-read cap: the jump passes both
// session_meta lines, and the copied turn and pull request are still the
// parent's (review of #152, round 2).
func TestForkPastTheReadCapLeavesOutItsCopiedTurnsAndPullRequests(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(string(fixtureAt(t, "0.171.0", "fork-spend.jsonl"))), "\n")
	if !strings.Contains(lines[1], `"session_meta"`) || !strings.Contains(lines[4], `"task_complete"`) {
		t.Fatal("fixture changed: want two session_meta lines, then the parent's turn at line 5")
	}
	lines = slices.Insert(lines, 4, ghAt("2026-11-19T09:05:10.000Z", 901))
	content := []byte(strings.Join(lines, "\n") + "\n")
	p := tailerFile(t, content)
	first, second := len(lines[0])+1, len(lines[0])+1+len(lines[1])+1
	// Where the jump lands: inside the fork's own session_meta, at the start
	// of the parent's, inside the parent's, and one byte into the third line.
	for _, jump := range []int{first / 2, first, first + len(lines[1])/2, second + 1} {
		tl := newTailer(p)
		tl.id = idFork
		tl.cap = int64(len(content) - jump)
		if _, err := tl.update(context.Background(), fixedNow); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(tl.d.TurnEnds, []time.Time{at(11, 41)}) {
			t.Errorf("jump to %d: turns %v, want only the fork's own", jump, tl.d.TurnEnds)
		}
		if len(tl.d.CreatedPRs) != 0 {
			t.Errorf("jump to %d: pull requests %v, want none: 901 is the parent's", jump, tl.d.CreatedPRs)
		}
	}
}

// A followed rollout replaced at the same path by a fork with a later
// boundary: what is before the new boundary is the parent's (round 2).
func TestTailerReplacedForkTakesTheNewBoundary(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.jsonl")
	write := func(lines ...string) {
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, p); err != nil { // a new file at the same path
			t.Fatal(err)
		}
	}
	write(metaAt(idFork, "2026-11-20T11:00:00.000Z"), metaAt(idWorker, "2026-11-20T11:00:00.001Z"), doneAt("2026-11-20T11:05:00.000Z"))
	tl := newTailer(p)
	tl.id = idFork
	if got := readAll(t, tl); !slices.Equal(got, []time.Time{at(11, 5)}) {
		t.Fatalf("first file: %v", got)
	}
	write(metaAt(idFork, "2026-11-20T11:20:00.000Z"), metaAt(idWorker, "2026-11-20T11:20:00.001Z"),
		doneAt("2026-11-20T11:10:00.000Z"), ghAt("2026-11-20T11:12:00.000Z", 901), doneAt("2026-11-20T11:30:00.000Z"))
	if got := readAll(t, tl); !slices.Equal(got, []time.Time{at(11, 30)}) {
		t.Errorf("replacement: turns %v, want only the one after its own boundary", got)
	}
	if len(tl.d.CreatedPRs) != 0 {
		t.Errorf("replacement: pull requests %v, want none", tl.d.CreatedPRs)
	}
}

// At the turn cap, a completion written later with an older time (a clock
// set back) does not push out a newer one (round 2).
func TestTailerTurnCapKeepsTheNewestByTime(t *testing.T) {
	var b strings.Builder
	for i := range maxTurns {
		b.WriteString(doneAt(fixedNow.Add(-time.Duration(maxTurns-i)*time.Second).Format(time.RFC3339Nano)) + "\n")
	}
	older := fixedNow.Add(-time.Duration(maxTurns+100) * time.Second)
	b.WriteString(doneAt(older.Format(time.RFC3339Nano)) + "\n")
	p := tailerFile(t, []byte(b.String()))
	tl := newTailer(p)
	got := readAll(t, tl)
	if len(got) != maxTurns || slices.Contains(got, older) || !got[0].Equal(fixedNow.Add(-maxTurns*time.Second)) {
		t.Fatalf("the late older completion pushed out a newer one: %d turns, first %v", len(got), got[0])
	}
	// A newer one does push out the oldest, and the list stays in time order.
	newer := fixedNow.Add(-time.Second / 2)
	appendTo(t, p, []byte(doneAt(newer.Format(time.RFC3339Nano))+"\n"))
	got = readAll(t, tl)
	if len(got) != maxTurns || !slices.IsSortedFunc(got, time.Time.Compare) {
		t.Fatalf("%d turns, sorted %v", len(got), slices.IsSortedFunc(got, time.Time.Compare))
	}
	if !got[len(got)-1].Equal(newer) || !got[0].Equal(fixedNow.Add(-(maxTurns-1)*time.Second)) {
		t.Errorf("first %v last %v", got[0], got[len(got)-1])
	}
}

// liveTail appends lines to a rollout and sets the live first-read cap so
// that a live read of it starts at them (the cut line is the last old one):
// as a resumed session's rollout larger than readCap is read.
func liveTail(t *testing.T, path string, mod time.Time, lines ...string) {
	t.Helper()
	tail := []byte(strings.Join(lines, "\n") + "\n")
	appendTo(t, path, tail)
	old := liveReadCap
	liveReadCap = int64(len(tail)) + 1
	t.Cleanup(func() { liveReadCap = old })
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

// A session resumed after the start-up read planned it and before its result
// is published: its rollout is read again whole, so the live card has what
// lies outside the live tail, each turn and pull request once (round 2).
func TestCatchupResumeBeforePublishingKeepsTheReadHistory(t *testing.T) {
	e := newEnv(t)
	e.addLines(idWorker, fixedNow.Add(-30*time.Minute),
		metaAt(idWorker, "2026-11-20T11:00:00.000Z"),
		ghAt("2026-11-20T11:01:00.000Z", 301),
		doneAt("2026-11-20T11:05:00.000Z"),
		recLine(idWorker, "2026-11-20T11:06:00.000Z", 100, 0, 10))
	e.rescan()
	// The file fits the start-up read's cap, and does not after the resume
	// appended to it: the live read of it has no cap (round 6, finding 39).
	fi, err := os.Stat(e.paths[idWorker])
	if err != nil {
		t.Fatal(err)
	}
	withCatchupLimits(t, catchupMaxFiles, fi.Size()+10)
	ctx := context.Background()
	older, _ := e.a.olderRollouts(ctx, catchupSince(fixedNow))
	res := readCatchup(ctx, e.r.planCatchup(older), fixedNow)
	// Resumed now: the live read sees only what was appended.
	liveTail(t, e.paths[idWorker], fixedNow.Add(-time.Minute), doneAt("2026-11-20T11:50:00.000Z"), recLine(idWorker, "2026-11-20T11:51:00.000Z", 100, 0, 10))
	e.lock(idWorker, 4242)
	e.rescan()
	if f, ok := e.last(idWorker); !ok || !f.Live.V || len(f.CreatedPRs) != 0 {
		t.Fatalf("setup: live card before publishing: %v %+v", ok, f)
	}
	e.r.applyCatchup(res)
	for e.r.publishCatchup() {
	}
	e.rescan() // the live read after the hand-over reads the rollout again whole (43)
	f, ok := e.last(idWorker)
	if !ok || f.Backfill || !f.Live.V {
		t.Fatalf("not the live card: %v %+v", ok, f)
	}
	if !slices.Equal(f.TurnEnds, []time.Time{at(11, 5), at(11, 50)}) {
		t.Errorf("turns %v, want the read one and the live one", f.TurnEnds)
	}
	if len(f.CreatedPRs) != 1 || f.CreatedPRs[0].Number != 301 {
		t.Errorf("pull requests %v, want the read 301", f.CreatedPRs)
	}
	e.rescan() // and it stays with the next scan
	if f, ok := e.last(idWorker); !ok || len(f.CreatedPRs) != 1 || len(f.TurnEnds) != 2 {
		t.Errorf("after a scan: %v %+v", ok, f)
	}
}

// A rollout replaced at its path under the same id (an atomic rename), cut
// short, or rewritten in place to the same size, after the start-up read: the
// live card has only what the file holds now, none of what that read found,
// whether the resume comes before or after the read is published (round 3,
// finding 20; round 5, finding 34).
func TestCatchupResumeHasOnlyWhatTheFileHoldsNow(t *testing.T) {
	newer := []string{metaAt(idWorker, "2026-11-20T11:00:00.000Z"), doneAt("2026-11-20T11:50:00.000Z"), recLine(idWorker, "2026-11-20T11:51:00.000Z", 100, 0, 10)}
	replace := func(t *testing.T, e *env) {
		tmp := e.paths[idWorker] + ".new"
		// Larger than the old file: only its identity tells it apart.
		big := slices.Clone(newer)
		for range 40 {
			big = append(big, `{"timestamp":"2026-11-20T11:49:00.000Z","type":"event_msg","payload":{"type":"agent_message"}}`)
		}
		if err := os.WriteFile(tmp, []byte(strings.Join(big, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, e.paths[idWorker]); err != nil {
			t.Fatal(err)
		}
		e.touch(e.paths[idWorker], fixedNow.Add(-time.Minute))
	}
	truncate := func(t *testing.T, e *env) {
		if err := os.WriteFile(e.paths[idWorker], []byte(strings.Join(newer, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		e.touch(e.paths[idWorker], fixedNow.Add(-time.Minute))
	}
	old := []string{
		metaAt(idWorker, "2026-11-20T11:00:00.000Z"),
		ghAt("2026-11-20T11:01:00.000Z", 301),
		doneAt("2026-11-20T11:05:00.000Z"),
		recLine(idWorker, "2026-11-20T11:06:00.000Z", 100, 0, 10),
		doneAt("2026-11-20T11:07:00.000Z"),
	}
	// The middle changed, the head and the end as they were, the same size,
	// with no scan in between.
	rewrite := func(t *testing.T, e *env) {
		mid := slices.Clone(old)
		mid[1], mid[2] = ghAt("2026-11-20T11:01:00.000Z", 302), doneAt("2026-11-20T11:04:00.000Z")
		f, err := os.OpenFile(e.paths[idWorker], os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt([]byte(strings.Join(mid, "\n")+"\n"), 0); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		e.touch(e.paths[idWorker], fixedNow.Add(-time.Minute))
	}
	for _, change := range []struct {
		name  string
		do    func(*testing.T, *env)
		turns []time.Time
		prs   []int
	}{{"replaced", replace, []time.Time{at(11, 50)}, nil}, {"cut short", truncate, []time.Time{at(11, 50)}, nil}, {"rewritten in the middle", rewrite, []time.Time{at(11, 4), at(11, 7)}, []int{302}}, {"cut short, then grown past the start-up read", func(t *testing.T, e *env) {
		truncate(t, e)
		e.rescan() // the live read sees it shorter
		grown := slices.Clone(newer)
		for range 40 {
			grown = append(grown, `{"timestamp":"2026-11-20T11:52:00.000Z","type":"event_msg","payload":{"type":"agent_message"}}`)
		}
		if err := os.WriteFile(e.paths[idWorker], []byte(strings.Join(grown, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		e.touch(e.paths[idWorker], fixedNow.Add(-time.Minute))
	}, []time.Time{at(11, 50)}, nil}} {
		for _, published := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s, published first %v", change.name, published), func(t *testing.T) {
				e := newEnv(t)
				e.addLines(idWorker, fixedNow.Add(-30*time.Minute), old...)
				e.rescan()
				ctx := context.Background()
				older, _ := e.a.olderRollouts(ctx, catchupSince(fixedNow))
				res := readCatchup(ctx, e.r.planCatchup(older), fixedNow)
				publish := func() {
					e.r.applyCatchup(res)
					for e.r.publishCatchup() {
					}
				}
				if published {
					publish()
				}
				change.do(t, e)
				e.lock(idWorker, 4242)
				e.rescan()
				if !published {
					publish()
				}
				e.rescan()
				f, ok := e.last(idWorker)
				if !ok || !f.Live.V {
					t.Fatalf("not live: %v %+v", ok, f)
				}
				var prs []int
				for _, p := range f.CreatedPRs {
					prs = append(prs, p.Number)
				}
				if !slices.Equal(f.TurnEnds, change.turns) || !slices.Equal(prs, change.prs) {
					t.Errorf("turns %v, pull requests %v: want only the file's now, %v %v", f.TurnEnds, prs, change.turns, change.prs)
				}
			})
		}
	}
}

// A fork whose parent session_meta comes after other records (the spike
// documents it up to three lines after the fork's own), read live from past
// the cap: the head read finds the boundary past a short record; past records
// too long for it (one, or two of 2.1 MiB, the parent's on line four), it
// cannot tell, and takes nothing from before the fork's own start (review of
// #152, rounds 4 and 5, finding 28).
func TestForkHeadSkipsARecordBetweenTheMetas(t *testing.T) {
	record := func(n int) string {
		return `{"timestamp":"2026-11-20T11:20:00.000Z","type":"response_item","payload":{"type":"message","text":"` + strings.Repeat("x", n) + `"}}`
	}
	for _, between := range [][]string{{record(0)}, {record(maxMetaLine)}, {record(2100 << 10), record(2100 << 10)}} {
		lines := slices.Concat([]string{metaAt(idFork, "2026-11-20T11:20:00.000Z")}, between, []string{metaAt(idWorker, "2026-11-20T11:20:00.001Z"),
			doneAt("2026-11-20T11:10:00.000Z"), ghAt("2026-11-20T11:12:00.000Z", 901), doneAt("2026-11-20T11:30:00.000Z")})
		content := []byte(strings.Join(lines, "\n") + "\n")
		p := tailerFile(t, content)
		jump := len(between) + 3 // the newlines, and one byte into the parent's turn
		for _, l := range lines[:len(between)+2] {
			jump += len(l)
		}
		tl := newTailer(p)
		tl.id = idFork
		tl.cap = int64(len(content) - jump)
		if _, err := tl.update(context.Background(), fixedNow); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(tl.d.TurnEnds, []time.Time{at(11, 30)}) || len(tl.d.CreatedPRs) != 0 {
			t.Errorf("%d record(s) between: turns %v, pull requests %v: want only the fork's own", len(between), tl.d.TurnEnds, tl.d.CreatedPRs)
		}
	}
}

// A rollout read at start as ended, shown as a card with liveness unknown
// while the read was published (a failed lock lookup), then known ended
// again: the read's history is published, not handed to a card that is only
// a fallback and goes once the lookup works (review of #152, round 4,
// finding 29).
func TestCatchupKeepsTheReadWhenTheCardIsOnlyAFallback(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(filepath.Join(e.root, "thread-writer-locks"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.addLines(idWorker, fixedNow.Add(-5*time.Minute),
		metaAt(idWorker, "2026-11-20T11:00:00.000Z"),
		ghAt("2026-11-20T11:01:00.000Z", 301),
		doneAt("2026-11-20T11:05:00.000Z"),
		recLine(idWorker, "2026-11-20T11:06:00.000Z", 100, 0, 10))
	e.rescan() // known ended: nobody holds it
	ctx := context.Background()
	older, _ := e.a.olderRollouts(ctx, catchupSince(fixedNow))
	res := readCatchup(ctx, e.r.planCatchup(older), fixedNow)
	if len(res.reads) != 1 {
		t.Fatalf("setup: read %d", len(res.reads))
	}
	e.fail = true // the lookup fails while the read is published
	e.rescan()
	if f, ok := e.last(idWorker); !ok || f.Backfill || f.Live.OK {
		t.Fatalf("setup: want the card with liveness unknown: %v %+v", ok, f)
	}
	e.r.applyCatchup(res)
	for e.r.publishCatchup() {
	}
	e.fail = false // known ended again
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || !f.Backfill || !f.Ended {
		t.Fatalf("want the caught-up fact: %v %+v", ok, f)
	}
	if !slices.Equal(f.TurnEnds, []time.Time{at(11, 5)}) || len(f.CreatedPRs) != 1 || f.Spend.Tokens == (model.Tokens{}) {
		t.Errorf("turns %v, pull requests %v, spend %d: want the read ones", f.TurnEnds, f.CreatedPRs, f.Spend.Tokens)
	}
	if _, card := e.r.sent[idWorker]; card {
		t.Error("a card is still sent")
	}
}

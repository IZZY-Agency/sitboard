package core

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// The read invariant, checked against a model kept apart from the board: the
// test's own source items, its own record of the lists each run returned,
// which of them a run still takes, the acknowledged set the file must hold
// (exact keys and times), and the cap. Random steps: new items, lists, "Caught up" on current, evicted and earlier
// runs' lists (also with no list in between, and at the exact 7-day edge),
// clock moves (forward, past the window, back), reopens with some sources
// late, and small caps. After each step, without making a list:
//   - every item the board takes as read was in a list marked read;
//   - every item in the model's acknowledged set is read;
//   - the file holds exactly the model's acknowledged set, with each item's
//     time, and when the newest list marked read was made.
//
// Each list holds exactly the page and Total the model predicts. Each "Caught
// up" must be taken, stale or full exactly as the model says, and a refused
// one leaves the file's bytes as they were.
func TestCatchupReadInvariantRandomised(t *testing.T) {
	defer func(i, r, l int) { maxCatchupItems, maxCatchupRead, maxShownLists = i, r, l }(maxCatchupItems, maxCatchupRead, maxShownLists)
	maxCatchupItems, maxCatchupRead, maxShownLists = 4, 30, 3

	seen := map[string]int{}
	for seed := range uint64(60) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { runCatchupModel(t, seed, 250, seen) })
	}
	// The sequences reach every case: a test that never hits one proves
	// nothing about it.
	for _, c := range []string{
		"taken", "taken with no list since a clock move", "taken at the 7-day edge", "stale, of an earlier run", "stale, evicted", "full",
		"reopen with a late source", "clock back after a reopen", "read after a reopen", "a list past the page",
	} {
		if seen[c] == 0 {
			t.Errorf("no sequence reached %q: %v", c, seen)
		}
	}
}

type modelItem struct {
	key string
	at  time.Time
}

type modelList struct {
	run   int
	id    string
	asOf  time.Time
	items []modelItem
}

func runCatchupModel(t *testing.T, seed uint64, steps int, seen map[string]int) {
	rng := rand.New(rand.NewPCG(seed, 0x5eed))
	e := &ackEnv{path: t.TempDir() + "/state.json", clock: &fakeClock{t: tNow}, repos: &fakeRepos{repos: map[string]model.Repo{}}, prs: &fakePRs{}}
	t.Setenv("HOME", testHome)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})

	sessions := make([]model.SessionFact, 3)
	for i := range sessions {
		sessions[i] = session(fmt.Sprintf("m%d", i), model.StatusFinished, tNow)
	}
	key := func(i int, at time.Time) string {
		return fmt.Sprintf("turn|%s|%s|%d", sessions[i].Key.Tool, sessions[i].Key.ID, at.UnixNano())
	}
	addTurn := func(i int, at time.Time) {
		if !slices.ContainsFunc(sessions[i].TurnEnds, at.Equal) {
			sessions[i].TurnEnds = append(sessions[i].TurnEnds, at)
		}
	}
	window := func(now time.Time) time.Time { return now.Add(-catchupWindow) }

	// The acknowledged set (item key -> own time) on disk, in the store's
	// memory and on the board: each is pruned by age at its own moments, so a
	// clock set back can bring back what only memory had dropped.
	prune := func(m map[string]time.Time, now time.Time) map[string]time.Time {
		out := map[string]time.Time{}
		for k, at := range m {
			if at.After(window(now)) {
				out[k] = at
			}
		}
		return out
	}
	fileR, storeR, R := map[string]time.Time{}, map[string]time.Time{}, map[string]time.Time{}
	everAcked := map[string]bool{}
	var markedAt time.Time

	run := 0
	var lists []modelList
	visible := make([]bool, len(sessions))
	apply := func(i int) {
		e.board.applySync(sessions[i])
		visible[i] = true
	}
	reopen := func() {
		if e.board != nil {
			e.board.markClosed()
			e.st.Close()
		}
		now := e.clock.Now()
		storeR = prune(fileR, now)
		R = maps.Clone(storeR)
		st, err := state.OpenAt(e.path, e.clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		e.st = st
		e.board = newBoard(t, Options{Repos: e.repos, PRs: e.prs, State: st, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
		run++
		for i := range sessions {
			visible[i] = false
			if run == 1 || rng.IntN(3) != 0 { // some sources are late
				apply(i)
			} else {
				seen["reopen with a late source"]++
			}
		}
	}
	reopen()

	// boardRead is whether the board takes an item as read, without making a
	// list: what it learned, or what it would take on learning it now.
	boardRead := func(k string, at time.Time) bool {
		if l, ok := e.board.learned[k]; ok {
			return l.read
		}
		_, ok := e.board.readSet[state.CatchupKey(k)]
		return ok
	}
	items := func() []modelItem { // the items the board has from its sources
		out := []modelItem{}
		for i, s := range sessions {
			if visible[i] {
				for _, at := range s.TurnEnds {
					out = append(out, modelItem{key(i, at), at})
				}
			}
		}
		return out
	}
	byKey := map[string]modelItem{}
	makeList := func() {
		// The model's page: every item the board has in the window that the
		// model holds unread, newest first (the same project and kind, and
		// the agents' names sort as the sessions do). An item marked read
		// that the model's set has since dropped by age, and the clock set
		// back brings into the window again, may be read or not: the board
		// says, and check holds it to the invariant.
		now := e.clock.Now()
		var want []modelItem
		for _, it := range items() {
			byKey[it.key] = it
			_, acked := R[it.key]
			if it.at.After(window(now)) && !it.at.After(now) && !acked && (!everAcked[it.key] || !boardRead(it.key, it.at)) {
				want = append(want, it)
			}
		}
		slices.SortFunc(want, func(a, b modelItem) int { return cmp.Or(b.at.Compare(a.at), cmp.Compare(a.key, b.key)) })
		c := catchup(t, e.board)
		l := modelList{run: run, id: c.List, asOf: c.AsOf}
		// Map the returned items to keys: the card and the time name a turn.
		for _, ci := range c.Items {
			for i := range sessions {
				if ci.CardID == cardID(sessions[i].Key) {
					l.items = append(l.items, modelItem{key(i, ci.At), ci.At})
				}
			}
		}
		if c.Total != len(want) || !slices.Equal(l.items, want[:min(len(want), maxCatchupItems)]) {
			t.Fatalf("the list holds %v of %d, the model %v of %d", l.items, c.Total, want, len(want))
		}
		if c.Total > len(c.Items) {
			seen["a list past the page"]++
		}
		lists = append(lists, l)
	}
	// onDisk is what the file holds: its acknowledged set and when the
	// newest list marked read was made, decoded from its bytes.
	onDisk := func() (raw []byte, read map[string]time.Time, at time.Time) {
		raw, err := os.ReadFile(e.path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		var doc struct {
			Read map[string]time.Time `json:"catchupRead"`
			At   time.Time            `json:"catchupMarkedAt"`
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
		}
		if doc.Read == nil {
			doc.Read = map[string]time.Time{}
		}
		return raw, doc.Read, doc.At
	}
	hashed := func(m map[string]time.Time) map[string]time.Time {
		out := map[string]time.Time{}
		for k, at := range m {
			out[state.CatchupKey(k)] = at.UTC()
		}
		return out
	}
	sameTimes := func(a, b map[string]time.Time) bool {
		return maps.EqualFunc(a, b, func(x, y time.Time) bool { return x.Equal(y) })
	}

	var latest time.Time
	clockMovedSinceList := false
	reopenedAt := -1
	check := func(step int, what string) {
		t.Helper()
		if now := e.clock.Now(); now.After(latest) {
			latest = now
		}
		for _, it := range items() {
			read := boardRead(it.key, it.at)
			if read && !everAcked[it.key] {
				t.Fatalf("step %d (%s): %s is read, but no list that showed it was marked read", step, what, it.key)
			}
			if _, acked := R[it.key]; acked && !read {
				t.Fatalf("step %d (%s): %s was marked read, and is unread again", step, what, it.key)
			}
			if read && everAcked[it.key] && run > 1 {
				seen["read after a reopen"]++
			}
		}
		if got, want := e.st.CatchupRead(), hashed(prune(storeR, e.clock.Now())); !sameTimes(got, want) {
			t.Fatalf("step %d (%s): the store holds %d acknowledged, the model %d", step, what, len(got), len(want))
		}
		if _, got, at := onDisk(); !sameTimes(got, hashed(fileR)) || !at.Equal(markedAt) {
			t.Fatalf("step %d (%s): the file holds %v marked at %v, the model %v at %v", step, what, got, at, hashed(fileR), markedAt)
		}
	}

	for step := range steps {
		var what string
		switch r := rng.IntN(100); {
		case r < 28:
			what = "new item"
			i := rng.IntN(len(sessions))
			addTurn(i, e.clock.Now().Add(-time.Duration(rng.IntN(180))*time.Minute))
			if visible[i] {
				apply(i)
			}
		case r < 45:
			what = "list"
			makeList()
			clockMovedSinceList = false
		case r < 72:
			what = "caught up"
			if len(lists) == 0 {
				continue
			}
			n := len(lists) - 1 - rng.IntN(min(len(lists), 3))
			if rng.IntN(4) == 0 {
				n = rng.IntN(len(lists))
			}
			l := lists[n]
			edge := false
			if n == len(lists)-1 && len(l.items) > 0 && rng.IntN(5) == 0 {
				// The oldest item of the newest list exactly 7 days old now,
				// with no list made since.
				e.clock.Set(l.items[len(l.items)-1].at.Add(catchupWindow))
				edge = true
			}
			now := e.clock.Now()
			// The model's answer: current lists of this run only, the newest
			// maxShownLists of them; then the cap, after pruning by age.
			var runLists []string
			for _, x := range lists {
				if x.run == run {
					runLists = append(runLists, x.id)
				}
			}
			current := l.run == run && slices.Index(runLists, l.id) >= len(runLists)-maxShownLists
			next := prune(R, now)
			added := false
			for _, it := range l.items {
				if _, ok := next[it.key]; !ok && it.at.After(window(now)) {
					next[it.key] = it.at
					added = true
				}
			}
			wantErr := error(nil)
			switch {
			case !current:
				wantErr = ErrCatchupStale
			case len(next) > maxCatchupRead && (added || l.asOf.After(markedAt)):
				wantErr = ErrCatchupFull
			}
			before, _, _ := onDisk()
			err := e.board.MarkCatchupRead(l.id)
			if !errors.Is(err, wantErr) || (err == nil) != (wantErr == nil) {
				t.Fatalf("step %d: list %s of run %d (now run %d): got %v, the model says %v", step, l.id, l.run, run, err, wantErr)
			}
			switch {
			case err == nil:
				seen["taken"]++
				if clockMovedSinceList {
					seen["taken with no list since a clock move"]++
				}
				if edge {
					seen["taken at the 7-day edge"]++
				}
				if added || l.asOf.After(markedAt) { // one write, pruned by this clock
					R = next
					storeR = prune(next, now)
					fileR = storeR
					if l.asOf.After(markedAt) {
						markedAt = l.asOf
					}
				}
				for _, it := range l.items { // shown and marked: the board may hold it read, also past the window
					everAcked[it.key] = true
				}
			case errors.Is(err, ErrCatchupFull):
				seen["full"]++
			case l.run != run:
				seen["stale, of an earlier run"]++
			default:
				seen["stale, evicted"]++
			}
			if after, _, _ := onDisk(); err != nil && !bytes.Equal(after, before) {
				t.Fatalf("step %d: refused (%v), but the file changed", step, err)
			}

		case r < 84:
			what = "clock"
			clockMovedSinceList = true
			switch rng.IntN(10) {
			case 0, 1:
				e.clock.Set(e.clock.Now().Add(-time.Duration(rng.IntN(60)) * time.Minute))
				if reopenedAt >= 0 && step-reopenedAt < 5 {
					seen["clock back after a reopen"]++
				}
			case 2:
				e.clock.Set(e.clock.Now().Add(time.Duration(2+rng.IntN(5)) * 24 * time.Hour))
			default:
				e.clock.Set(e.clock.Now().Add(time.Duration(rng.IntN(240)) * time.Minute))
			}
		case r < 93:
			what = "reopen"
			reopen()
			reopenedAt = step
		default:
			what = "late source"
			apply(rng.IntN(len(sessions)))
		}
		check(step, what)
	}
}

func cardID(k model.SessionKey) string { return string(k.Tool) + ":" + k.ID }

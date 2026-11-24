package core

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// ackEnv is a board on a state file at a known path, so a test can close the
// file and open it again, as after sitboard stops and starts.
type ackEnv struct {
	path  string
	clock *fakeClock
	repos *fakeRepos
	prs   *fakePRs
	st    state.Store
	board *Board
}

func newAckEnv(t *testing.T) *ackEnv {
	t.Helper()
	t.Setenv("HOME", testHome)
	e := &ackEnv{path: filepath.Join(t.TempDir(), "state.json"), clock: &fakeClock{t: tNow}, repos: &fakeRepos{repos: map[string]model.Repo{}}, prs: &fakePRs{}}
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.prs.prs = []model.PullRequest{prAt(9, model.PRStateOpen, tNow.Add(-5*time.Hour), time.Time{})}
	e.reopen(t)
	return e
}

// reopen stops the board, closes the state file, opens it again from disk
// and starts a new board on it.
func (e *ackEnv) reopen(t *testing.T) {
	t.Helper()
	if e.board != nil {
		e.board.markClosed()
		e.st.Close()
	}
	st, err := state.OpenAt(e.path, e.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	e.st = st
	e.board = newBoard(t, Options{Repos: e.repos, PRs: e.prs, State: st, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
}

// claudeWithPR is an ended Claude session that opened pull request 9 five
// hours ago.
func claudeWithPR() model.SessionFact {
	s := session("claude", model.StatusFinished, tNow.Add(-5*time.Hour))
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 9}}
	s.Live, s.Ended, s.EndedAt, s.Backfill = model.Of(false), true, tNow.Add(-5*time.Hour), true
	return s
}

// codexTurns is an ended Codex session with n turns, the newest a second
// before tNow.
func codexTurns(n int) model.SessionFact {
	s := session("codex", model.StatusFinished, tNow)
	s.Key = model.SessionKey{Tool: model.ToolCodex, ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
	for i := range n {
		s.TurnEnds = append(s.TurnEnds, tNow.Add(-time.Duration(i+1)*time.Second))
	}
	s.Live, s.Ended, s.EndedAt, s.Backfill = model.Of(false), true, tNow, true
	return s
}

func hasPR(c contract.Catchup) bool {
	return slices.ContainsFunc(c.Items, func(it contract.CatchupItem) bool { return it.Kind == contract.CatchupPROpened })
}

// 2,000 newer Codex turns and an older Claude pull request: marking the first
// page read leaves 1,001 items unread behind it. None of them is lost across a close and reopen of the state file
// and a new board, and the page marked read stays read (review of #152, round
// 2, finding 4).
func TestCatchupMarkOfAPageLosesNothingAcrossAReopen(t *testing.T) {
	e := newAckEnv(t)
	pr, turns := claudeWithPR(), codexTurns(2*maxCatchupItems)
	e.board.applySync(pr)
	e.board.applySync(turns)
	e.board.tick() // the pull request lookup
	first := catchup(t, e.board)
	if first.Total != 2*maxCatchupItems+1 || hasPR(first) {
		t.Fatalf("setup: total %d, the pull request on the first page %v", first.Total, hasPR(first))
	}
	if err := e.board.MarkCatchupRead(first.List); err != nil {
		t.Fatal(err)
	}
	if n := len(e.st.CatchupRead()); n != maxCatchupItems {
		t.Fatalf("%d saved as read, want exactly the page's", n)
	}

	e.clock.Set(tNow.Add(time.Hour))
	e.reopen(t)
	e.board.applySync(pr)
	e.board.applySync(turns)
	e.board.tick()
	again := catchup(t, e.board)
	if again.Total != maxCatchupItems+1 {
		t.Fatalf("after the reopen: total %d, want the %d left out", again.Total, maxCatchupItems+1)
	}
	for _, it := range again.Items {
		if it.Kind == contract.CatchupTurnFinished && it.At.After(first.Items[len(first.Items)-1].At.Add(-time.Nanosecond)) {
			t.Fatalf("a turn marked read is listed again: %v", it.At)
		}
	}
	if err := e.board.MarkCatchupRead(again.List); err != nil {
		t.Fatal(err)
	}
	last := catchup(t, e.board)
	if last.Total != 1 || !hasPR(last) {
		t.Fatalf("the pull request: total %d, listed %v", last.Total, hasPR(last))
	}
	e.reopen(t)
	e.board.applySync(pr)
	e.board.applySync(turns)
	e.board.tick()
	if c := catchup(t, e.board); c.Total != 1 || !hasPR(c) {
		t.Fatalf("after a second reopen: total %d, listed %v", c.Total, hasPR(c))
	}
}

// Two tabs: A shows 999 turns and the pull request; a new turn arrives, and B
// shows 1,000 turns, leaving the pull request out. Each tab's "Caught up"
// marks what that tab showed, in either order and again (finding 11).
func TestCatchupTwoTabsMarkWhatEachShowed(t *testing.T) {
	newTurn := tNow.Add(30 * time.Second)
	for _, tc := range []struct {
		name   string
		marks  string // which tabs press "Caught up", in order
		unread []string
	}{
		{"A only", "A", []string{"turn"}},
		{"B only", "B", []string{"pr"}},
		{"A then B", "AB", nil},
		{"B then A", "BA", nil},
		{"A twice", "AA", []string{"turn"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAckEnv(t)
			pr, turns := claudeWithPR(), codexTurns(maxCatchupItems-1)
			e.board.applySync(pr)
			e.board.applySync(turns)
			e.board.tick()
			a := catchup(t, e.board)
			if a.Total != maxCatchupItems || !hasPR(a) {
				t.Fatalf("setup: tab A total %d, pull request %v", a.Total, hasPR(a))
			}
			e.clock.Set(tNow.Add(time.Minute))
			turns.TurnEnds = append([]time.Time{newTurn}, turns.TurnEnds...)
			e.board.applySync(turns)
			b := catchup(t, e.board)
			if b.Total != maxCatchupItems+1 || hasPR(b) {
				t.Fatalf("setup: tab B total %d, pull request %v", b.Total, hasPR(b))
			}
			for _, tab := range tc.marks {
				list := a.List
				if tab == 'B' {
					list = b.List
				}
				if err := e.board.MarkCatchupRead(list); err != nil {
					t.Fatal(err)
				}
			}
			check := func(when string) {
				t.Helper()
				c := catchup(t, e.board)
				got := []string{}
				for _, it := range c.Items {
					switch {
					case it.Kind == contract.CatchupPROpened:
						got = append(got, "pr")
					case it.At.Equal(newTurn):
						got = append(got, "turn")
					default:
						got = append(got, "old turn")
					}
				}
				if !slices.Equal(got, tc.unread) {
					t.Fatalf("%s: unread %v, want %v", when, got, tc.unread)
				}
			}
			check("marked")
			e.clock.Set(tNow.Add(time.Hour))
			e.reopen(t)
			e.board.applySync(pr)
			e.board.applySync(turns)
			e.board.tick()
			check("after a reopen")
		})
	}
}

// unchanged fails the test when the state file holds any acknowledgement.
func unchanged(t *testing.T, e *ackEnv) {
	t.Helper()
	if n := len(e.st.CatchupRead()); n != 0 {
		t.Fatalf("%d acknowledged", n)
	}
	if at := e.st.CatchupMarkedAt(); !at.IsZero() {
		t.Fatalf("marked at %v", at)
	}
}

// A list older than the remembered ones, or one from before a restart (even
// with the clock set back, so its time is later than the fresh list's), marks
// nothing: ErrCatchupStale, the file unchanged, its items
// listed again (review of #152, round 3, finding 17).
func TestCatchupMarkOfAForgottenListMarksNothing(t *testing.T) {
	e := newAckEnv(t)
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-time.Minute)}
	e.board.applySync(s)
	old := catchup(t, e.board)
	for range maxShownLists {
		catchup(t, e.board)
	}
	if err := e.board.MarkCatchupRead(old.List); !errors.Is(err, ErrCatchupStale) {
		t.Fatalf("an evicted list: %v, want stale", err)
	}
	unchanged(t, e)
	if c := catchup(t, e.board); c.Total != 1 {
		t.Fatalf("a forgotten list marked %d item(s) read", 1-c.Total)
	}

	e.clock.Set(tNow.Add(time.Hour))
	before := catchup(t, e.board)
	e.reopen(t)
	e.clock.Set(tNow) // set back: the old list's time is after the fresh one's
	e.board.applySync(s)
	fresh := catchup(t, e.board)
	if !before.AsOf.After(fresh.AsOf) || before.List == fresh.List {
		t.Fatalf("setup: before %v %q, fresh %v %q", before.AsOf, before.List, fresh.AsOf, fresh.List)
	}
	if err := e.board.MarkCatchupRead(before.List); !errors.Is(err, ErrCatchupStale) {
		t.Fatalf("a list from before the restart: %v, want stale", err)
	}
	unchanged(t, e)
	if c := catchup(t, e.board); c.Total != 1 {
		t.Fatal("a list from before the restart marked an item read")
	}
}

// At the file's cap an acknowledgement is refused whole: ErrCatchupFull,
// nothing marked, nothing written; the items stay listed. A smaller list
// that fits is still taken.
func TestCatchupMarkPastTheReadCapIsRefused(t *testing.T) {
	defer func(n int) { maxCatchupRead = n }(maxCatchupRead)
	maxCatchupRead = 5
	e := newAckEnv(t)
	turns := codexTurns(6)
	e.board.applySync(turns)
	six := catchup(t, e.board)
	if err := e.board.MarkCatchupRead(six.List); !errors.Is(err, ErrCatchupFull) {
		t.Fatalf("%v, want full", err)
	}
	unchanged(t, e)
	if c := catchup(t, e.board); c.Total != 6 {
		t.Fatalf("refused, but %d marked read", 6-c.Total)
	}

	e.clock.Set(tNow.Add(time.Minute))
	turns.TurnEnds = turns.TurnEnds[:3]
	e2 := newAckEnv(t)
	e2.board.applySync(turns)
	if err := e2.board.MarkCatchupRead(catchup(t, e2.board).List); err != nil {
		t.Fatal(err)
	}
	turns.TurnEnds = codexTurns(6).TurnEnds
	e2.board.applySync(turns)
	rest := catchup(t, e2.board) // three more: six in all, past five
	if err := e2.board.MarkCatchupRead(rest.List); !errors.Is(err, ErrCatchupFull) {
		t.Fatalf("%v, want full", err)
	}
	if n := len(e2.st.CatchupRead()); n != 3 {
		t.Fatalf("%d acknowledged, want the first three", n)
	}
	if c := catchup(t, e2.board); c.Total != 3 {
		t.Fatalf("%d listed, want the three refused", c.Total)
	}
}

// Finding 16: 2,000 turns and an older pull request; the first page is marked
// read. After a reopen the pull request's source is late, and the next page
// is marked read without it. When it comes back, on a third board, the pull
// request is still unread: nothing but a list that showed it marks it.
func TestCatchupLateSourceStaysUnreadAcrossTwoReopens(t *testing.T) {
	e := newAckEnv(t)
	pr, turns := claudeWithPR(), codexTurns(2*maxCatchupItems)
	e.board.applySync(pr)
	e.board.applySync(turns)
	e.board.tick()
	if err := e.board.MarkCatchupRead(catchup(t, e.board).List); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(tNow.Add(time.Hour))
	e.reopen(t)
	e.board.applySync(turns) // the pull request's session is not read yet
	e.board.tick()
	second := catchup(t, e.board)
	if second.Total != maxCatchupItems || hasPR(second) {
		t.Fatalf("second board: total %d, pull request %v", second.Total, hasPR(second))
	}
	if err := e.board.MarkCatchupRead(second.List); err != nil {
		t.Fatal(err)
	}
	e.reopen(t)
	e.board.applySync(pr)
	e.board.applySync(turns)
	e.board.tick()
	if c := catchup(t, e.board); c.Total != 1 || !hasPR(c) {
		t.Fatalf("third board: total %d, pull request %v", c.Total, hasPR(c))
	}
}

// Finding 18: 4,001 turns. Three pages marked read, a reopen, the next page
// marked read, another reopen: progress holds each time, and only the one
// turn no list showed is left.
func TestCatchupProgressHoldsAcrossShortRuns(t *testing.T) {
	e := newAckEnv(t)
	turns := codexTurns(4*maxCatchupItems + 1)
	load := func() {
		e.board.applySync(turns)
		e.board.tick()
	}
	load()
	for page := range 3 {
		c := catchup(t, e.board)
		if c.Total != 4*maxCatchupItems+1-page*maxCatchupItems {
			t.Fatalf("page %d: total %d", page, c.Total)
		}
		if err := e.board.MarkCatchupRead(c.List); err != nil {
			t.Fatal(err)
		}
	}
	e.reopen(t)
	load()
	c := catchup(t, e.board)
	if c.Total != maxCatchupItems+1 {
		t.Fatalf("after the reopen: total %d, want %d", c.Total, maxCatchupItems+1)
	}
	if err := e.board.MarkCatchupRead(c.List); err != nil {
		t.Fatal(err)
	}
	e.reopen(t)
	load()
	c = catchup(t, e.board)
	if c.Total != 1 || !c.Items[0].At.Equal(turns.TurnEnds[len(turns.TurnEnds)-1].UTC()) {
		t.Fatalf("after the second reopen: total %d %v", c.Total, kindsAt(c))
	}
}

// A mark that cannot be saved marks nothing read in memory either: the page
// says nothing was lost, and the items stay listed.
func TestCatchupMarkNotSavedMarksNothing(t *testing.T) {
	e := newAckEnv(t)
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-time.Minute)}
	e.board.applySync(s)
	shown := catchup(t, e.board)
	e.st.Close()
	if err := e.board.MarkCatchupRead(shown.List); err == nil {
		t.Fatal("a mark after the file closed was taken as saved")
	}
	if c := catchup(t, e.board); c.Total != 1 {
		t.Fatalf("not saved, but marked read in memory: total %d", c.Total)
	}
}

// Finding 25: an item of the list that left the window by the time of
// "Caught up" (exactly 7 days old, and no list made since to forget it)
// takes no room under the cap: the rest of the list fits and is marked.
func TestCatchupMarkAtTheWindowEdgeCountsOnlyWhatIsKept(t *testing.T) {
	defer func(n int) { maxCatchupRead = n }(maxCatchupRead)
	maxCatchupRead = 3
	e := newAckEnv(t)
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-2 * time.Minute), tNow.Add(-time.Minute)}
	e.board.applySync(s)
	if err := e.board.MarkCatchupRead(catchup(t, e.board).List); err != nil {
		t.Fatal(err)
	}
	edge := tNow.Add(-6 * 24 * time.Hour)
	s.TurnEnds = append(s.TurnEnds, edge, tNow.Add(-30*time.Second))
	e.board.applySync(s)
	shown := catchup(t, e.board)
	if shown.Total != 2 {
		t.Fatalf("setup: %d listed", shown.Total)
	}
	e.clock.Set(edge.Add(catchupWindow)) // the edge item is exactly 7 days old now
	if err := e.board.MarkCatchupRead(shown.List); err != nil {
		t.Fatalf("%v: the item past the edge was counted", err)
	}
	if n := len(e.st.CatchupRead()); n != 3 {
		t.Fatalf("%d acknowledged, want 3", n)
	}
}

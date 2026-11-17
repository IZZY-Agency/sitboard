package core

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

func finishedAt(id string, at time.Time) model.SessionFact {
	s := session(id, model.StatusFinished, at)
	s.TurnEnds = []time.Time{at}
	s.TerminalID = termUUID
	return s
}

func TestMarkSeenPersistsAndClearsUnseen(t *testing.T) {
	e := newEnv(t)
	e.board.applySync(finishedAt("s1", tNow.Add(-time.Minute)))
	if c := onlyCard(t, e.board); c.Column != contract.ColumnNeedsYou || !slices.Contains(c.Attention, contract.AttentionUnseen) {
		t.Fatalf("before: %q %v", c.Column, c.Attention)
	}
	if err := e.board.MarkSeen("claude:s1"); err != nil {
		t.Fatal(err)
	}
	if c := onlyCard(t, e.board); c.Column != contract.ColumnIdle || !c.Seen || len(c.Attention) != 0 {
		t.Fatalf("after: %q seen=%v %v", c.Column, c.Seen, c.Attention)
	}
	if _, ok := e.state.SeenAt("claude:s1"); !ok {
		t.Fatal("seen mark not in the state store")
	}
	if err := e.board.MarkSeen("claude:nope"); err == nil {
		t.Fatal("unknown card accepted")
	}
}

func TestSeenSurvivesRestart(t *testing.T) {
	t.Setenv("HOME", testHome)
	file := filepath.Join(t.TempDir(), "state.json")
	open := func() *Board {
		st, err := state.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		return New(Options{State: st, Now: func() time.Time { return tNow }})
	}
	b := open()
	b.applySync(finishedAt("s1", tNow.Add(-time.Minute)))
	if err := b.MarkSeen("claude:s1"); err != nil {
		t.Fatal(err)
	}
	b2 := open()
	b2.applySync(finishedAt("s1", tNow.Add(-time.Minute)))
	if c := onlyCard(t, b2); !c.Seen {
		t.Fatal("seen lost across restart")
	}
}

func TestNewTurnEndMakesCardUnseenAgain(t *testing.T) {
	e := newEnv(t)
	s := finishedAt("s1", tNow.Add(-10*time.Minute))
	e.board.applySync(s)
	if err := e.board.MarkSeen("claude:s1"); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(tNow.Add(5 * time.Minute))
	s.TurnEnds = append(s.TurnEnds, tNow.Add(4*time.Minute))
	s.StatusSince = tNow.Add(4 * time.Minute)
	e.board.applySync(s)
	if c := onlyCard(t, e.board); c.Seen || c.Column != contract.ColumnNeedsYou {
		t.Fatalf("after a new turn: seen=%v %q", c.Seen, c.Column)
	}
}

func TestAgtermSelectionAfterFinishMarksSeen(t *testing.T) {
	e := newEnv(t)
	e.board.applySync(model.TerminalFact{ID: termUUID, HasAgent: true})
	e.board.applySync(finishedAt("s1", tNow.Add(-time.Minute)))
	e.board.applySync(model.TerminalSelected{ID: termUUID, At: tNow})
	if c := onlyCard(t, e.board); !c.Seen || c.Column != contract.ColumnIdle {
		t.Fatalf("seen=%v %q", c.Seen, c.Column)
	}
}

func TestSelectionBeforeFinishDoesNot(t *testing.T) {
	e := newEnv(t)
	e.board.applySync(model.TerminalFact{ID: termUUID, HasAgent: true})
	busy := busySession("s1", tNow.Add(-5*time.Minute))
	busy.TerminalID = termUUID
	e.board.applySync(busy)
	e.board.applySync(model.TerminalSelected{ID: termUUID, At: tNow.Add(-2 * time.Minute)}) // while busy
	e.board.applySync(finishedAt("s1", tNow.Add(-time.Minute)))
	// A selection event timed before the turn end, arriving late, does not count either.
	e.board.applySync(model.TerminalSelected{ID: termUUID, At: tNow.Add(-90 * time.Second)})
	if c := onlyCard(t, e.board); c.Seen || c.Column != contract.ColumnNeedsYou {
		t.Fatalf("seen=%v %q", c.Seen, c.Column)
	}
}

func TestCountsPerProjectAndTotal(t *testing.T) {
	e := newEnv(t)
	e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 101, State: model.PRStateOpen}}
	working := busySession("a1", tNow)
	stale := busySession("a2", tNow.Add(-30*time.Minute))
	review := session("a3", model.StatusFinished, tNow)
	review.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	needs := session("b1", model.StatusBlocked, tNow)
	needs.Folder = beta
	unseen := session("b2", model.StatusFinished, tNow)
	unseen.Folder = beta
	for _, s := range []model.SessionFact{working, stale, review, needs, unseen} {
		e.board.applySync(s)
	}
	e.board.tick()
	snap := e.board.Snapshot()
	if want := (contract.Counts{Working: 2, NeedsYou: 2, InReview: 1, Stale: 1}); snap.Counts != want {
		t.Fatalf("total %+v, want %+v", snap.Counts, want)
	}
	got := map[string]contract.Counts{}
	for _, p := range snap.Projects {
		got[p.Name] = p.Counts
	}
	if want := (contract.Counts{Working: 2, InReview: 1, Stale: 1}); got["alpha"] != want {
		t.Fatalf("alpha %+v, want %+v", got["alpha"], want)
	}
	if want := (contract.Counts{NeedsYou: 2}); got["beta"] != want {
		t.Fatalf("beta %+v, want %+v", got["beta"], want)
	}
}

func TestParentCardIDFormsTree(t *testing.T) {
	b := newTestBoard(t)
	coord := busySession("c", tNow)
	w1 := busySession("w1", tNow)
	w1.Parent = model.Of(claudeKey("c"))
	w2 := busySession("w2", tNow)
	w2.Parent = model.Of(claudeKey("c"))
	for _, s := range []model.SessionFact{coord, w1, w2} {
		b.applySync(s)
	}
	children := 0
	for _, c := range b.Snapshot().Cards {
		if c.Agent.ParentCardID == "claude:c" {
			children++
		}
	}
	if children != 2 {
		t.Fatalf("%d children of the coordinator, want 2", children)
	}
}

func TestTerminalForReturnsOnlyKnownTerminals(t *testing.T) {
	b := newTestBoard(t)
	s := busySession("s1", tNow)
	s.TerminalID = termUUID
	b.applySync(s)
	if _, ok := b.TerminalFor("claude:s1"); ok {
		t.Fatal("terminal returned before it is in the tree")
	}
	b.applySync(model.TerminalFact{ID: termUUID, HasAgent: true})
	if id, ok := b.TerminalFor("claude:s1"); !ok || id != termUUID {
		t.Fatalf("got %q %v", id, ok)
	}
	if _, ok := b.TerminalFor("claude:unknown"); ok {
		t.Fatal("unknown card resolved")
	}
	b.applySync(model.TerminalGone{ID: termUUID})
	if _, ok := b.TerminalFor("claude:s1"); ok {
		t.Fatal("terminal returned after it closed")
	}
}

func TestSourceStatusesAppear(t *testing.T) {
	b := newTestBoard(t)
	b.SetSources([]source.Status{
		{Kind: source.KindGitHub, State: source.StateOff},
		{Kind: source.KindAgterm, State: source.StateOK, Version: "1.0.0"},
		{Kind: source.KindClaude, State: source.StateOK, Version: "2.1.326", Tested: true},
	})
	s := b.Snapshot()
	if len(s.Sources) != 3 || s.Sources[0].Kind != contract.SourceClaude || s.Sources[1].Kind != contract.SourceAgterm || s.Sources[2].Kind != contract.SourceGitHub {
		t.Fatalf("sources %+v", s.Sources)
	}
	if !s.AgtermPresent {
		t.Fatal("agterm ok but AgtermPresent false")
	}
}

// agterm reports the persisted last selection when it is first read. On a
// first run with no state file, a session selected at or after its last turn
// ended is seen, whichever of the two facts arrives first.
func TestPersistedSelectionCountsAsSeenOnFirstRun(t *testing.T) {
	for _, selectionFirst := range []bool{true, false} {
		e := newEnv(t) // a fresh state file that does not exist yet
		end := tNow.Add(-time.Hour)
		facts := []model.Fact{
			model.TerminalFact{ID: termUUID, HasAgent: true},
			finishedAt("s1", end),
		}
		sel := model.TerminalSelected{ID: termUUID, At: end} // at the turn end counts
		if selectionFirst {
			facts = append([]model.Fact{sel}, facts...)
		} else {
			facts = append(facts, sel)
		}
		for _, f := range facts {
			e.board.applySync(f)
		}
		if c := onlyCard(t, e.board); !c.Seen || c.Column != contract.ColumnIdle {
			t.Fatalf("selection first=%v: seen=%v %q, want seen in idle", selectionFirst, c.Seen, c.Column)
		}
	}
}

func TestPersistedSelectionBeforeTheTurnEndedIsNotSeen(t *testing.T) {
	e := newEnv(t)
	e.board.applySync(model.TerminalSelected{ID: termUUID, At: tNow.Add(-2 * time.Hour)})
	e.board.applySync(model.TerminalFact{ID: termUUID, HasAgent: true})
	e.board.applySync(finishedAt("s1", tNow.Add(-time.Hour)))
	if c := onlyCard(t, e.board); c.Seen || c.Column != contract.ColumnNeedsYou {
		t.Fatalf("seen=%v %q, want unseen in needs_you", c.Seen, c.Column)
	}
	// A new turn after a selection makes the card unseen again.
	e.board.applySync(model.TerminalSelected{ID: termUUID, At: tNow})
	if c := onlyCard(t, e.board); !c.Seen {
		t.Fatal("not seen after a selection")
	}
	next := finishedAt("s1", tNow.Add(time.Minute))
	e.clock.Set(tNow.Add(2 * time.Minute))
	e.board.applySync(next)
	if c := onlyCard(t, e.board); c.Seen {
		t.Fatal("still seen after a new turn")
	}
}

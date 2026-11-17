package core

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

func parentOf(t *testing.T, b *Board, id string) contract.ParentState {
	t.Helper()
	c, ok := cardByID(t, b, id)
	if !ok {
		t.Fatalf("no card %s", id)
	}
	return c.Agent.Parent
}

// Item 1: a coordinator hears from workers that are not matched right now.
func TestCoordinatorIsNotHintedByItsOwnUnmatchedWorkers(t *testing.T) {
	b := newTestBoard(t)
	coord := busySession("coord", tNow)
	coord.Name = "coordinator-a"
	coord.SpawnedNames = []string{"worker-a", "worker-b", "worker-c"}
	coord.SpawnedTerminalIDs = []string{termUUID}
	coord.PeerSenders = []string{
		"worker-a",      // shared by two sessions, so not matched
		"worker-b",      // its records are gone, or it has not been read yet
		"worker-t",      // known only by the name of a terminal it spawned
		"coordinator-a", // itself
	}
	dupA := busySession("w1", tNow)
	dupA.Name = "worker-a"
	dupB := busySession("w2", tNow)
	dupB.Name = "worker-a"
	for _, f := range []model.Fact{coord, dupA, dupB, model.TerminalFact{ID: termUUID, Name: "worker-t", HasAgent: true}} {
		b.applySync(f)
	}
	if p := parentOf(t, b, "claude:coord"); p != contract.ParentNone {
		t.Fatalf("coordinator parent %q, want none", p)
	}
	// The terminal it spawned closes: its name still counts as its own child's.
	b.applySync(model.TerminalGone{ID: termUUID})
	coord.PeerSenders = []string{"worker-c"}
	b.applySync(coord)
	if p := parentOf(t, b, "claude:coord"); p != contract.ParentNone {
		t.Fatalf("after the child's terminal closed: parent %q, want none", p)
	}
	// A live session it did not spawn is still a sign.
	other := busySession("o1", tNow)
	other.Name = "coordinator-z"
	b.applySync(other)
	coord.PeerSenders = []string{"coordinator-z"}
	b.applySync(coord)
	if p := parentOf(t, b, "claude:coord"); p != contract.ParentUnknown {
		t.Fatalf("stranger sender: parent %q, want unknown", p)
	}
}

// Item 2: a selection time in the future counts as now.
func TestFutureSelectionIsClampedToNow(t *testing.T) {
	e := newEnv(t)
	e.board.applySync(model.TerminalFact{ID: termUUID, HasAgent: true})
	e.board.applySync(model.TerminalSelected{ID: termUUID, At: tNow.Add(3 * time.Hour)}) // agterm's clock runs fast
	e.board.applySync(finishedAt("s1", tNow.Add(-time.Minute)))
	if c := onlyCard(t, e.board); !c.Seen {
		t.Fatal("not seen after a selection")
	}
	// A new turn after now is not covered by the future selection.
	e.clock.Set(tNow.Add(10 * time.Minute))
	e.board.applySync(finishedAt("s1", tNow.Add(5*time.Minute)))
	if c := onlyCard(t, e.board); c.Seen || c.Column != contract.ColumnNeedsYou {
		t.Fatalf("seen=%v %q: the future selection still covers a new turn", c.Seen, c.Column)
	}
}

// Item 3: a card seen through an agterm selection keeps that in the state
// file, so it stays seen when agterm forgets the selection or sitboard restarts.
func TestSelectionSeenIsPersisted(t *testing.T) {
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
	b.applySync(model.TerminalFact{ID: termUUID, HasAgent: true})
	b.applySync(finishedAt("s1", tNow.Add(-time.Hour)))
	b.applySync(model.TerminalSelected{ID: termUUID, At: tNow.Add(-time.Minute)})
	if c := onlyCard(t, b); !c.Seen {
		t.Fatal("not seen after the selection")
	}
	// agterm forgets the terminal; the mark is in the state file.
	b.applySync(model.TerminalGone{ID: termUUID})
	if c := onlyCard(t, b); !c.Seen || c.Column != contract.ColumnIdle {
		t.Fatalf("after agterm forgot: seen=%v %q", c.Seen, c.Column)
	}
	// A restart without agterm.
	b2 := open()
	b2.applySync(finishedAt("s1", tNow.Add(-time.Hour)))
	if c := onlyCard(t, b2); !c.Seen || c.Column != contract.ColumnIdle {
		t.Fatalf("after a restart: seen=%v %q", c.Seen, c.Column)
	}
}

// Item 4: a parent and child that name each other are both top-level.
func TestParentCycleIsTopLevel(t *testing.T) {
	b := newTestBoard(t)
	a := session("a", model.StatusFinished, tNow)
	a.Name = "worker-a"
	a.SpawnedNames = []string{"worker-b"}
	c := session("b", model.StatusFinished, tNow)
	c.Name = "worker-b"
	c.SpawnedNames = []string{"worker-a"}
	b.applySync(a)
	b.applySync(c)
	for _, id := range []string{"claude:a", "claude:b"} {
		card, _ := cardByID(t, b, id)
		if card.Column != contract.ColumnNeedsYou || card.Agent.Parent != contract.ParentUnknown || card.Agent.ParentCardID != "" {
			t.Errorf("%s: %q parent %q card %q, want needs_you, unknown, no parent card", id, card.Column, card.Agent.Parent, card.Agent.ParentCardID)
		}
	}
	// A three-session loop too.
	parents := map[model.SessionKey]model.Known[model.SessionKey]{
		claudeKey("x"): model.Of(claudeKey("y")),
		claudeKey("y"): model.Of(claudeKey("z")),
		claudeKey("z"): model.Of(claudeKey("x")),
		claudeKey("w"): model.Of(claudeKey("x")), // points into the loop, not part of it
	}
	in := cycles(parents)
	if !in[claudeKey("x")] || !in[claudeKey("y")] || !in[claudeKey("z")] || in[claudeKey("w")] {
		t.Fatalf("cycle members %v", in)
	}
}

// Item 5: with no known turn end, an old selection does not count.
func TestSelectionNeedsAKnownTurnEnd(t *testing.T) {
	e := newEnv(t)
	e.board.applySync(model.TerminalFact{ID: termUUID, HasAgent: true})
	e.board.applySync(model.TerminalSelected{ID: termUUID, At: tNow.Add(-24 * time.Hour)})
	s := session("s1", model.StatusFinished, time.Time{}) // turn end unknown
	s.TerminalID = termUUID
	s.StartedAt, s.LastActivity = tNow.Add(-time.Hour), tNow.Add(-time.Hour)
	e.board.applySync(s)
	if c := onlyCard(t, e.board); c.Seen {
		t.Fatal("an old selection counted with no known turn end")
	}
}

// Item 6: only known status glyphs are trimmed from a terminal title.
func TestTerminalTitleKeepsRealLeadingSymbols(t *testing.T) {
	for raw, want := range map[string]string{
		"✳ Fix the login form":          "Fix the login form",
		"⠐ Fix the login form":          "Fix the login form",
		"◐ Fix the login form":          "Fix the login form",
		"• · Fix":                       "Fix",
		"… Fix":                         "Fix",
		"\U0001F680 Launch the release": "\U0001F680 Launch the release",
		"$ budget report":               "$ budget report",
		"€ pricing page":                "€ pricing page",
		"+ add the export":              "+ add the export",
	} {
		if got := terminalTitle(raw); got != want {
			t.Errorf("%q: got %q, want %q", raw, got, want)
		}
	}
}

package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// A session that spawned others hears from its old workers long after they
// are gone, often under names it never recorded (renamed, or spawned in a way
// the transcript did not show). A sender that is not a live session is no
// sign that such a session is a worker; a live sender it did not spawn is.
func TestCoordinatorIgnoresSendersThatAreNotLiveSessions(t *testing.T) {
	b := newTestBoard(t)
	coord := busySession("coord", tNow)
	coord.Name = "coordinator-a"
	coord.SpawnedNames = []string{"worker-a"}
	coord.PeerSenders = []string{"old-worker"} // no session carries this name
	child := busySession("w1", tNow)
	child.Name = "worker-a"
	endedSender := session("w2", model.StatusFinished, tNow)
	endedSender.Name = "ended-worker"
	endedSender.Ended = true
	for _, f := range []model.Fact{coord, child, endedSender} {
		b.applySync(f)
	}
	if p := parentOf(t, b, "claude:coord"); p != contract.ParentNone {
		t.Fatalf("with a matched child, a sender that is no session: parent %q, want none", p)
	}

	// A sender that is an ended session is not live either.
	coord.PeerSenders = []string{"ended-worker"}
	b.applySync(coord)
	if p := parentOf(t, b, "claude:coord"); p != contract.ParentNone {
		t.Fatalf("sender is an ended session: parent %q, want none", p)
	}

	// No matched child left, but it did spawn: still a coordinator.
	b.applySync(model.SessionGone{Key: claudeKey("w1")})
	coord.PeerSenders = []string{"renamed-worker"}
	b.applySync(coord)
	if p := parentOf(t, b, "claude:coord"); p != contract.ParentNone {
		t.Fatalf("spawned, no child on the board: parent %q, want none", p)
	}

	// A live session it did not spawn messaging it is still a sign.
	other := busySession("o1", tNow)
	other.Name = "coordinator-z"
	b.applySync(other)
	coord.PeerSenders = []string{"old-worker", "coordinator-z"}
	b.applySync(coord)
	if p := parentOf(t, b, "claude:coord"); p != contract.ParentUnknown {
		t.Fatalf("live stranger sender: parent %q, want unknown", p)
	}
}

// A session that spawned nothing and hears from a session that has gone is a
// worker whose coordinator ended: "coordinator: unknown" is the honest answer.
func TestWorkerOfAnEndedCoordinatorIsStillHinted(t *testing.T) {
	b := newTestBoard(t)
	w := busySession("w1", tNow)
	w.Name = "worker-a"
	w.PeerSenders = []string{"gone-coordinator"}
	b.applySync(w)
	if p := parentOf(t, b, "claude:w1"); p != contract.ParentUnknown {
		t.Fatalf("parent %q, want unknown", p)
	}
}

// Only an open pull request still closes issues: a closed one never will, and
// a merged one already did.
func TestIssuesOnlyFromOpenPullRequests(t *testing.T) {
	for _, st := range []model.PRState{model.PRStateClosed, model.PRStateMerged} {
		e := newEnv(t)
		e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 101, State: st, ClosingIssues: []int{7}}}
		s := session("s1", model.StatusFinished, tNow)
		s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
		e.board.applySync(s)
		e.board.tick()
		c := onlyCard(t, e.board)
		if len(c.PullRequests) != 1 {
			t.Fatalf("%s: pull request not linked: %+v", st, c.PullRequests)
		}
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"issues":[]`) {
			t.Fatalf("%s: issues %+v, want an empty list", st, c.Issues)
		}
	}

	e := newEnv(t)
	e.prs.prs = []model.PullRequest{
		{Repo: exRepo, Number: 101, State: model.PRStateOpen, ClosingIssues: []int{7}},
		{Repo: exRepo, Number: 102, State: model.PRStateMerged, ClosingIssues: []int{8}},
	}
	s := session("s1", model.StatusFinished, tNow)
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}, {Repo: exRepo, Number: 102}}
	e.board.applySync(s)
	e.board.tick()
	c := onlyCard(t, e.board)
	if len(c.PullRequests) != 2 || len(c.Issues) != 1 || c.Issues[0].Number != 7 {
		t.Fatalf("open and merged: pull requests %d, issues %+v; want only #7", len(c.PullRequests), c.Issues)
	}
}

// The trade-off: a worker that spawned helpers and whose coordinator has ended
// counts as a coordinator, so it shows no coordinator line, not "unknown".
func TestWorkerWithHelpersAndEndedCoordinatorShowsNone(t *testing.T) {
	b := newTestBoard(t)
	w := busySession("w1", tNow)
	w.Name = "worker-a"
	w.SpawnedNames = []string{"helper"}
	w.PeerSenders = []string{"gone-coordinator"}
	b.applySync(w)
	if p := parentOf(t, b, "claude:w1"); p != contract.ParentNone {
		t.Fatalf("parent %q, want none", p)
	}
}

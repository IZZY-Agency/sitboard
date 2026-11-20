package core

import (
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// rememberUmbrella makes umbrella a remembered umbrella with part as its
// part: a coordinator in umbrella with a worker in part settles, then the
// coordinator ends. The worker in part stays live.
func (e *groupEnv) rememberUmbrella(t *testing.T, umbrella, part string) {
	t.Helper()
	e.agent("u", umbrella)
	e.worker("p", part, "u")
	e.settle(t)
	e.board.applySync(model.SessionGone{Key: claudeKey("u")})
	if _, ok := e.testEnv.state.Umbrellas()[umbrella]; !ok {
		t.Fatal("setup: the umbrella was not remembered")
	}
}

// A remembered umbrella is listed only while one of its parts is on the
// board: with no agent anywhere in it, it is not a project.
func TestRememberedUmbrellaIsListedOnlyWithAPart(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.rememberUmbrella(t, umbrella, part)

	e.board.applySync(model.SessionGone{Key: claudeKey("p")})
	if _, ok := e.projects(t)[pid(umbrella)]; ok {
		t.Fatal("a remembered umbrella with no part on the board is listed")
	}
	e.agent("p", part)
	ps := e.projects(t)
	if _, ok := ps[pid(umbrella)]; !ok {
		t.Fatal("a remembered umbrella is not listed once a part is back")
	}
	if p := ps[pid(part)]; p.ParentID != pid(umbrella) {
		t.Fatalf("the part is back but not grouped: %+v", p)
	}
}

// The 30 days run from the last time an agent sat in the umbrella folder
// itself. Agents working only in its parts do not keep it: an umbrella
// project whose own folder stays unused expires while its parts are busy
// (the case #49 is about).
func TestUmbrellaExpiresWhileOnlyItsPartsAreUsed(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.rememberUmbrella(t, umbrella, part)
	start := e.clock.Now()

	// Daily work in the part only, for 30 days less an hour: still grouped.
	for d := 1; d < 30; d++ {
		e.clock.Set(start.Add(time.Duration(d) * 24 * time.Hour))
		e.agent("p", part)
		if p := e.projects(t)[pid(part)]; p.ParentID != pid(umbrella) {
			t.Fatalf("day %d: no longer grouped: %+v", d, p)
		}
	}
	e.clock.Set(start.Add(state.UmbrellaRetention - time.Hour))
	e.agent("p", part)
	if p := e.projects(t)[pid(part)]; p.ParentID != pid(umbrella) {
		t.Fatalf("an hour before the 30 days: %+v", p)
	}
	e.clock.Set(start.Add(state.UmbrellaRetention + time.Hour))
	e.agent("p", part)
	if p := e.projects(t)[pid(part)]; p.ParentID != "" {
		t.Fatalf("still grouped 30 days after an agent last sat in the umbrella: %+v", p)
	}
	if _, ok := e.testEnv.state.Umbrellas()[umbrella]; ok {
		t.Fatal("the expired umbrella is still in the state file")
	}
}

// An agent sitting in the umbrella folder again (with the grouping qualified
// once more) starts the 30 days again.
func TestUmbrellaRetentionCountsFromTheLastAgentInIt(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.rememberUmbrella(t, umbrella, part)
	start := e.clock.Now()

	e.clock.Set(start.Add(20 * 24 * time.Hour))
	e.agent("u", umbrella)
	e.worker("p", part, "u")
	_ = e.projects(t)
	e.board.applySync(model.SessionGone{Key: claudeKey("u")})

	e.clock.Set(start.Add(state.UmbrellaRetention + 24*time.Hour))
	e.agent("p", part)
	if p := e.projects(t)[pid(part)]; p.ParentID != pid(umbrella) {
		t.Fatalf("expired 31 days after the first use, 11 after the last: %+v", p)
	}
	if _, ok := e.testEnv.state.Umbrellas()[umbrella]; !ok {
		t.Fatal("the renewed umbrella is not in the state file")
	}
}

// Projects are sorted by name, then id, an umbrella with no card of its own
// included.
func TestProjectsAreSortedByNameThenID(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/mid")
	part := e.repo(t, "work/mid/zeta")
	alpha := e.repo(t, "work/alpha")
	same1, same2 := e.repo(t, "one/same"), e.repo(t, "two/same")
	e.rememberUmbrella(t, umbrella, part)
	for i, f := range []string{same2, alpha, same1} {
		e.agent(string(rune('a'+i)), f)
	}
	e.board.tick()
	got := e.board.Snapshot().Projects
	sameFirst, sameSecond := pid(same1), pid(same2)
	if sameFirst > sameSecond {
		sameFirst, sameSecond = sameSecond, sameFirst
	}
	want := []string{pid(alpha), pid(umbrella), sameFirst, sameSecond, pid(part)}
	if len(got) != len(want) {
		t.Fatalf("%d projects, want %d: %+v", len(got), len(want), got)
	}
	for i, p := range got {
		if p.ID != want[i] {
			t.Fatalf("project %d is %s (%s), want %s; all: %v", i, p.Name, p.ID, want[i], names(got))
		}
	}
}

func names(ps []contract.Project) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name + ":" + p.ID
	}
	return out
}

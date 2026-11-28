package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// An answer that says again what is served leaves the revision, so an open
// panel does not fetch again; its freshness (the list's "as of") moves.
func TestAnIdenticalLookupLeavesTheRevision(t *testing.T) {
	e, gh := newPinEnv(t)
	pin(t, e, 77)
	ri := sampleIssues(model.GitHubRepo(exRepo), 12, 48, 45)
	ri.Pinned = []model.PinnedIssue{pinnedAnswer(77, model.PinOpen)}
	gh.set(ri)
	lookAt(e, tNow.Add(issuesEvery))
	rev := e.board.Snapshot().Issues.Rev
	first, err := e.board.Issues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lookAt(e, tNow.Add(2*issuesEvery))
	if n := len(gh.calls()); n != 3 {
		t.Fatalf("%d calls, want 3", n)
	}
	if got := e.board.Snapshot().Issues.Rev; got != rev {
		t.Fatalf("rev %d→%d on an identical answer", rev, got)
	}
	again, err := e.board.Issues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again.Rev != rev || !again.AsOf.After(first.AsOf) {
		t.Fatalf("rev %d (was %d), as of %v (was %v)", again.Rev, rev, again.AsOf, first.AsOf)
	}
	ri.Pinned = []model.PinnedIssue{pinnedAnswer(77, model.PinClosed)}
	gh.set(ri)
	lookAt(e, tNow.Add(3*issuesEvery))
	if e.board.Snapshot().Issues.Rev == rev {
		t.Fatal("a pinned row changed and the revision stayed")
	}
	// A pin with no answer yet (its fallback row) that the forge then finds
	// gone: the row goes once the report is saved, and the revision moves.
	pin(t, e, 99)
	lookAt(e, tNow.Add(4*issuesEvery))
	rev = e.board.Snapshot().Issues.Rev
	ri.Pinned = append(ri.Pinned, pinnedAnswer(99, model.PinGone))
	gh.set(ri)
	lookAt(e, tNow.Add(5*issuesEvery))
	if _, ok := e.state.Pins()[pinKey(99)]; ok || e.board.Snapshot().Issues.Rev == rev {
		t.Fatalf("gone pin kept %v, rev %d", ok, rev)
	}
}

// A closed pin's retention running out while every lookup fails moves the
// revision, and its row is no longer served.
func TestAPinsExpiryDuringAnOutageMovesTheRevision(t *testing.T) {
	e, gh := newPinEnv(t)
	pin(t, e, 77)
	ri := sampleIssues(model.GitHubRepo(exRepo), 12, 48, 45)
	ri.Pinned = []model.PinnedIssue{pinnedAnswer(77, model.PinClosed)}
	gh.set(ri)
	lookAt(e, tNow.Add(issuesEvery)) // closed: kept SeenRetention from now
	gh.mu.Lock()
	gh.err = errors.New("sample outage")
	gh.mu.Unlock()
	at := tNow.Add(2 * issuesEvery)
	lookAt(e, at)
	rev := e.board.Snapshot().Issues.Rev
	if _, pinned := pinnedRowsOf(t, e.board, model.GitHubRepo(exRepo)); len(pinned) != 1 {
		t.Fatalf("pinned %+v", pinned)
	}
	lookAt(e, tNow.Add(issuesEvery+state.SeenRetention)) // not past it yet
	if got := e.board.Snapshot().Issues.Rev; got != rev {
		t.Fatalf("rev %d→%d before the expiry", rev, got)
	}
	lookAt(e, tNow.Add(issuesEvery+state.SeenRetention+time.Second))
	if e.board.Snapshot().Issues.Rev == rev {
		t.Fatal("the expiry left the revision")
	}
	if _, pinned := pinnedRowsOf(t, e.board, model.GitHubRepo(exRepo)); len(pinned) != 0 {
		t.Fatalf("an expired pin is still served: %+v", pinned)
	}
}

// A pinned row outside the list that the forge answered by number carries
// its agents like a listed row, and the link moves the revision.
func TestAPinnedRowFromTheForgeGetsItsAgents(t *testing.T) {
	e, gh := newPinEnv(t)
	pin(t, e, 77)
	ri := sampleIssues(model.GitHubRepo(exRepo), 12, 48, 45)
	ri.Pinned = []model.PinnedIssue{pinnedAnswer(77, model.PinOpen)}
	gh.set(ri)
	lookAt(e, tNow.Add(issuesEvery))
	rev := e.board.Snapshot().Issues.Rev
	closing(e, "c", 101, 77)
	_, pinned := pinnedRowsOf(t, e.board, model.GitHubRepo(exRepo))
	if len(pinned) != 1 || pinned[0].Title == "" ||
		!slices.Equal(pinned[0].Agents, []contract.IssueAgent{{CardID: cardIDOf(t, "c"), Kind: contract.IssueCloses}}) {
		t.Fatalf("pinned %+v", pinned)
	}
	if e.board.Snapshot().Issues.Rev == rev {
		t.Fatal("a link on a pinned row left the revision")
	}
}

// A by-number answer for a pin that is among the listed rows is not served
// (its listed row is), so its coming and going leaves the revision.
func TestARedundantPinAnswerLeavesTheRevision(t *testing.T) {
	e, gh := newPinEnv(t)
	pin(t, e, 48)
	ri := sampleIssues(model.GitHubRepo(exRepo), 12, 48, 45)
	gh.set(ri)
	lookAt(e, tNow.Add(issuesEvery))
	rev := e.board.Snapshot().Issues.Rev
	with := ri
	with.Pinned = []model.PinnedIssue{pinnedAnswer(48, model.PinOpen)}
	for i, answer := range []model.RepoIssues{with, ri, with} {
		gh.set(answer)
		lookAt(e, tNow.Add(time.Duration(i+2)*issuesEvery))
		if got := e.board.Snapshot().Issues.Rev; got != rev {
			t.Fatalf("look %d: rev %d→%d for a pin answer that is not served", i+2, rev, got)
		}
	}
	if n := len(gh.calls()); n != 5 {
		t.Fatalf("%d calls, want 5", n)
	}
	if listed, pinned := pinnedRowsOf(t, e.board, model.GitHubRepo(exRepo)); len(pinned) != 0 || listed[0].Number != 48 || !listed[0].Pinned {
		t.Fatalf("listed %+v pinned %+v", listed, pinned)
	}
}

// The served rows are signed with the pins they come from: a closed mark,
// or a pin made again later (which moves it first), changes the rows served
// with the answers unchanged, and the revision moves.
func TestThePinsAloneMoveTheRevision(t *testing.T) {
	e, gh := newPinEnv(t)
	e.clock.Set(tNow.Add(time.Second))
	pin(t, e, 77)
	e.clock.Set(tNow.Add(2 * time.Second))
	pin(t, e, 99)
	gh.set(sampleIssues(model.GitHubRepo(exRepo), 12, 48, 45))
	lookAt(e, tNow.Add(issuesEvery))
	steps := []struct {
		name   string
		change func() error
		first  int
		closed bool
	}{
		{"a closed mark", func() error {
			return e.state.ReportPins(map[string]state.PinReport{pinKey(99): state.PinClosed})
		}, 99, true},
		{"a pin made again", func() error {
			if err := e.state.SetPin(pinKey(77), false, tNow); err != nil {
				return err
			}
			return e.state.SetPin(pinKey(77), true, tNow.Add(time.Hour))
		}, 77, false},
	}
	for i, s := range steps {
		rev := e.board.Snapshot().Issues.Rev
		if err := s.change(); err != nil {
			t.Fatal(err)
		}
		lookAt(e, tNow.Add(time.Duration(i+2)*issuesEvery))
		if got := e.board.Snapshot().Issues.Rev; got == rev {
			t.Fatalf("%s: the rows served changed and the revision stayed at %d", s.name, rev)
		}
		_, pinned := pinnedRowsOf(t, e.board, model.GitHubRepo(exRepo))
		if len(pinned) != 2 || pinned[0].Number != s.first || (pinned[0].State == contract.OpenIssueClosed) != s.closed {
			t.Fatalf("%s: pinned %+v", s.name, pinned)
		}
	}
}

// Rows dropped by a purge (the login went) that come back the same, once a
// look answers again, are served again: the revision moves at the drop and
// again at their return.
func TestRowsBackAfterAPurgeMoveTheRevision(t *testing.T) {
	e, gh := newPinEnv(t)
	lookAt(e, tNow.Add(issuesEvery))
	gh.mu.Lock()
	gh.err = fmt.Errorf("github: not logged in: %w", source.ErrNotLoggedIn)
	gh.mu.Unlock()
	lookAt(e, tNow.Add(2*issuesEvery))
	s := e.board.Snapshot().Issues
	if s.Purged == 0 || s.Purged != s.Rev {
		t.Fatalf("the drop was not a purge: %+v", s)
	}
	gh.mu.Lock()
	gh.err = nil
	gh.mu.Unlock()
	lookAt(e, tNow.Add(3*issuesEvery))
	if got := e.board.Snapshot().Issues.Rev; got == s.Rev {
		t.Fatalf("the rows came back and the revision stayed at %d", got)
	}
	if listed, _ := pinnedRowsOf(t, e.board, model.GitHubRepo(exRepo)); len(listed) != 2 {
		t.Fatalf("listed %+v", listed)
	}
}

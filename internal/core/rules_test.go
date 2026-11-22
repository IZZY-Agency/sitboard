package core

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

var tNow = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

func st(s model.Status) model.Known[model.Status] { return model.Of(s) }

func openPR(checks model.Checks) model.PullRequest {
	return model.PullRequest{Repo: model.GitHubRepo("example-org/example-repo"), Number: 101, State: model.PRStateOpen, Checks: checks}
}

func closedPR(state model.PRState, closedAgo time.Duration) model.PullRequest {
	return model.PullRequest{Repo: model.GitHubRepo("example-org/example-repo"), Number: 102, State: state, Checks: model.ChecksPassing, ClosedAt: tNow.Add(-closedAgo)}
}

// TestColumnTable is the rule table from the plan (Task E2), one sub-test per
// row. "Seen" compares the seen time with the last turn end; here it is given
// as the result of that comparison.
func TestColumnTable(t *testing.T) {
	type row struct {
		id         string
		tool       model.Known[model.Status] // the agent tool's own record
		term       model.Known[model.Status] // agterm's status, when any
		ended      time.Duration             // >0: ended this long ago
		lastActive time.Duration             // last activity this long ago
		prs        []model.PullRequest
		parent     bool
		seen       bool
		wantCol    contract.Column
		wantAtt    []contract.Attention
		wantOff    bool // no card
	}
	rows := []row{
		{id: "1", tool: st(model.StatusBlocked), prs: []model.PullRequest{openPR(model.ChecksPassing)}, wantCol: contract.ColumnNeedsYou, wantAtt: []contract.Attention{contract.AttentionBlocked}},
		{id: "2", tool: st(model.StatusBusy), lastActive: 5 * time.Minute, seen: true, wantCol: contract.ColumnWorking},
		{id: "3", tool: st(model.StatusBusy), lastActive: 21 * time.Minute, wantCol: contract.ColumnWorking, wantAtt: []contract.Attention{contract.AttentionStale}},
		{id: "4", tool: st(model.StatusBusy), lastActive: 19 * time.Minute, wantCol: contract.ColumnWorking},
		{id: "5", tool: st(model.StatusFinished), prs: []model.PullRequest{closedPR(model.PRStateMerged, 5*time.Hour), closedPR(model.PRStateMerged, 2*time.Hour)}, seen: true, wantCol: contract.ColumnDone},
		{id: "6", tool: st(model.StatusFinished), prs: []model.PullRequest{closedPR(model.PRStateMerged, 25*time.Hour)}, seen: true, wantCol: contract.ColumnIdle},
		{id: "7", tool: st(model.StatusFinished), prs: []model.PullRequest{openPR(model.ChecksPassing)}, seen: true, wantCol: contract.ColumnInReview},
		{id: "8", tool: st(model.StatusFinished), prs: []model.PullRequest{openPR(model.ChecksFailing)}, seen: true, wantCol: contract.ColumnInReview, wantAtt: []contract.Attention{contract.AttentionFailingChecks}},
		{id: "9", tool: st(model.StatusFinished), seen: false, wantCol: contract.ColumnNeedsYou, wantAtt: []contract.Attention{contract.AttentionUnseen}},
		{id: "10", tool: st(model.StatusFinished), parent: true, seen: false, wantCol: contract.ColumnWithCoordinator},
		{id: "11", tool: st(model.StatusFinished), seen: true, wantCol: contract.ColumnIdle},
		{id: "12", tool: st(model.StatusBusy), term: st(model.StatusBlocked), lastActive: time.Minute, wantCol: contract.ColumnNeedsYou, wantAtt: []contract.Attention{contract.AttentionBlocked}},
		{id: "13", tool: st(model.StatusFinished), term: st(model.StatusBusy), seen: true, wantCol: contract.ColumnIdle},
		{id: "14", tool: model.Unknown[model.Status](), wantCol: contract.ColumnIdle},
		{id: "15", tool: st(model.StatusFinished), ended: 3 * time.Hour, prs: []model.PullRequest{openPR(model.ChecksPassing)}, wantCol: contract.ColumnInReview},
		{id: "16", tool: st(model.StatusFinished), ended: 3 * time.Hour, seen: false, wantOff: true},
		{id: "16b", tool: st(model.StatusFinished), ended: 3 * time.Hour, prs: []model.PullRequest{closedPR(model.PRStateMerged, 2*time.Hour)}, seen: false, wantCol: contract.ColumnDone},
		{id: "17", tool: st(model.StatusBusy), lastActive: time.Minute, prs: []model.PullRequest{openPR(model.ChecksFailing)}, parent: true, wantCol: contract.ColumnWorking, wantAtt: []contract.Attention{contract.AttentionFailingChecks}},
	}
	for _, r := range rows {
		t.Run(fmt.Sprintf("row_%s", r.id), func(t *testing.T) {
			in := cardInput{
				status:       mergeStatus(r.tool, r.term),
				prs:          r.prs,
				hasParent:    r.parent,
				seen:         r.seen,
				lastActivity: tNow.Add(-r.lastActive),
				staleAfter:   20 * time.Minute,
			}
			if r.ended > 0 {
				in.ended = true
				in.endedAt = tNow.Add(-r.ended)
			}
			col, att, on := column(in, tNow)
			if r.wantOff {
				if on {
					t.Fatalf("on the board in %q, want no card", col)
				}
				return
			}
			if !on {
				t.Fatal("no card, want one")
			}
			if col != r.wantCol {
				t.Errorf("column %q, want %q", col, r.wantCol)
			}
			if !slices.Equal(att, r.wantAtt) && (len(att) != 0 || len(r.wantAtt) != 0) {
				t.Errorf("attention %v, want %v", att, r.wantAtt)
			}
		})
	}
}

func TestStaleThresholdIsConfigurable(t *testing.T) {
	in := cardInput{status: st(model.StatusBusy), lastActivity: tNow.Add(-6 * time.Minute), staleAfter: 5 * time.Minute}
	if _, att, _ := column(in, tNow); !slices.Contains(att, contract.AttentionStale) {
		t.Fatalf("attention %v, want stale after 5m", att)
	}
	in.staleAfter = 10 * time.Minute
	if _, att, _ := column(in, tNow); slices.Contains(att, contract.AttentionStale) {
		t.Fatalf("attention %v, want no stale with a 10m threshold", att)
	}
}

func TestFutureTimestampNeverNegativeDuration(t *testing.T) {
	// A record carrying a time in the future is not stale and does not make
	// a closed pull request look recent forever.
	in := cardInput{status: st(model.StatusBusy), lastActivity: tNow.Add(3 * time.Hour), staleAfter: 20 * time.Minute}
	if _, att, _ := column(in, tNow); len(att) != 0 {
		t.Fatalf("attention %v for a future activity time", att)
	}
	if got := clampPast(tNow.Add(time.Hour), tNow); got.After(tNow) {
		t.Fatalf("clampPast left %v after now", got)
	}
	if got := clampPast(time.Time{}, tNow); !got.IsZero() {
		t.Fatalf("clampPast turned unknown into %v", got)
	}
}

func TestUnknownStatusIsReportedNotGuessed(t *testing.T) {
	// Row 14: unknown status sits in Idle with no attention, even with a
	// recent activity time that might look like "busy".
	in := cardInput{status: model.Unknown[model.Status](), lastActivity: tNow.Add(-time.Minute), staleAfter: 20 * time.Minute}
	col, att, on := column(in, tNow)
	if !on || col != contract.ColumnIdle || len(att) != 0 {
		t.Fatalf("got %q %v on=%v, want idle, no attention", col, att, on)
	}
	if got := agentStatus(in.status, false); got != contract.AgentStatusUnknown {
		t.Fatalf("agent status %q, want unknown", got)
	}
}

func TestEndedNeverNeedsYou(t *testing.T) {
	for _, s := range []model.Status{model.StatusBlocked, model.StatusBusy, model.StatusFinished} {
		in := cardInput{status: st(s), ended: true, endedAt: tNow.Add(-time.Hour), prs: []model.PullRequest{openPR(model.ChecksPassing)}}
		col, att, on := column(in, tNow)
		if !on || col != contract.ColumnInReview || len(att) != 0 {
			t.Errorf("%s: got %q %v on=%v, want in_review, no attention", s, col, att, on)
		}
	}
}

func TestEndedWindows(t *testing.T) {
	open := cardInput{status: st(model.StatusFinished), ended: true, endedAt: tNow.Add(-8 * 24 * time.Hour), prs: []model.PullRequest{openPR(model.ChecksPassing)}}
	if _, _, on := column(open, tNow); on {
		t.Error("ended 8 days ago with an open pull request: still on the board")
	}
	merged := cardInput{status: st(model.StatusFinished), ended: true, endedAt: tNow.Add(-30 * time.Hour), prs: []model.PullRequest{closedPR(model.PRStateClosed, 25*time.Hour)}}
	if _, _, on := column(merged, tNow); on {
		t.Error("ended, last pull request closed 25h ago: still on the board")
	}
}

func TestSleepGapAloneIsNotStale(t *testing.T) {
	b, clock := newClockBoard(t)
	b.applySync(busySession("s1", tNow))
	b.tick()
	clock.Set(tNow.Add(10 * time.Second))
	b.tick()
	clock.Set(tNow.Add(2 * time.Hour)) // the machine slept; the ticker did not fire
	b.tick()
	if c := onlyCard(t, b); slices.Contains(c.Attention, contract.AttentionStale) {
		t.Fatalf("stale right after a sleep: %v", c.Attention)
	}
}

func TestStaleReturnsAfterResumePlusThreshold(t *testing.T) {
	b, clock := newClockBoard(t)
	b.applySync(busySession("s1", tNow))
	b.tick()
	resume := tNow.Add(2 * time.Hour)
	clock.Set(resume)
	b.tick()
	// Awake again: the ticker fires every 10 s with no new activity.
	for at := resume.Add(10 * time.Second); !at.After(resume.Add(21 * time.Minute)); at = at.Add(10 * time.Second) {
		clock.Set(at)
		b.tick()
		if at.Before(resume.Add(20 * time.Minute)) {
			if c := onlyCard(t, b); slices.Contains(c.Attention, contract.AttentionStale) {
				t.Fatalf("stale %v after resume", at.Sub(resume))
			}
		}
	}
	if c := onlyCard(t, b); !slices.Contains(c.Attention, contract.AttentionStale) {
		t.Fatalf("not stale 21 min after resume: %v", c.Attention)
	}
}

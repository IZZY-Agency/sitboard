package core

import (
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

const (
	doneWindow     = 24 * time.Hour     // a card stays in Done this long after its last pull request closed
	endedOpenLimit = 7 * 24 * time.Hour // an ended session with an open pull request stays this long
)

// cardInput is everything the column rules look at, already joined.
type cardInput struct {
	status       model.Known[model.Status] // effective status, see mergeStatus
	ended        bool
	endedAt      time.Time
	lastActivity time.Time // newest sign of activity; zero when unknown
	resumedAt    time.Time // when the machine last woke from sleep; zero when it did not
	staleAfter   time.Duration
	prs          []model.PullRequest // linked pull requests GitHub answered for
	hasParent    bool                // a coordinator was detected
	seen         bool                // seen since the last turn ended
}

// mergeStatus combines the agent tool's own status with agterm's. "Blocked"
// from any source wins; otherwise the tool's record wins over agterm's.
func mergeStatus(tool, term model.Known[model.Status]) model.Known[model.Status] {
	if (tool.OK && tool.V == model.StatusBlocked) || (term.OK && term.V == model.StatusBlocked) {
		return model.Of(model.StatusBlocked)
	}
	if tool.OK {
		return tool
	}
	return term
}

// column applies the spec's rules in order; the first match wins. onBoard is
// false when the card has left the board.
func column(in cardInput, now time.Time) (col contract.Column, attention []contract.Attention, onBoard bool) {
	var open, closed int
	var lastClosed time.Time
	failing := false
	for _, pr := range in.prs {
		switch pr.State {
		case model.PRStateOpen:
			open++
			if pr.Checks == model.ChecksFailing {
				failing = true
			}
		case model.PRStateMerged, model.PRStateClosed:
			closed++
			if pr.ClosedAt.After(lastClosed) {
				lastClosed = pr.ClosedAt
			}
		}
	}
	// A future close time counts as "now": it never keeps a card in Done longer.
	recentlyClosed := closed > 0 && open == 0 && since(lastClosed, now) < doneWindow

	withFailing := func(att []contract.Attention) []contract.Attention {
		if failing {
			att = append(att, contract.AttentionFailingChecks)
		}
		return att
	}

	// An ended session cannot need the owner and is never Idle. It stays only
	// while its pull requests have something to show.
	if in.ended {
		switch {
		case recentlyClosed:
			return contract.ColumnDone, nil, true
		case open > 0 && since(in.endedAt, now) <= endedOpenLimit:
			return contract.ColumnInReview, withFailing(nil), true
		default:
			return "", nil, false
		}
	}

	if !in.status.OK || !knownStatus(in.status.V) {
		return contract.ColumnIdle, withFailing(nil), true
	}
	switch in.status.V {
	case model.StatusBlocked: // rule 1
		return contract.ColumnNeedsYou, withFailing([]contract.Attention{contract.AttentionBlocked}), true
	case model.StatusBusy: // rule 2
		var att []contract.Attention
		if isStale(in, now) {
			att = append(att, contract.AttentionStale)
		}
		return contract.ColumnWorking, withFailing(att), true
	}
	switch {
	case recentlyClosed: // rule 3
		return contract.ColumnDone, nil, true
	case open > 0: // rule 4
		return contract.ColumnInReview, withFailing(nil), true
	case !in.hasParent && !in.seen: // rule 5
		return contract.ColumnNeedsYou, []contract.Attention{contract.AttentionUnseen}, true
	case in.hasParent: // rule 6
		return contract.ColumnWithCoordinator, nil, true
	default: // rule 7
		return contract.ColumnIdle, nil, true
	}
}

// isStale: busy with no sign of activity for staleAfter, counting a wake from
// sleep as activity so that sleep alone never makes a card stale.
func isStale(in cardInput, now time.Time) bool {
	ref := in.lastActivity
	if in.resumedAt.After(ref) {
		ref = in.resumedAt
	}
	if ref.IsZero() || in.staleAfter <= 0 {
		return false
	}
	return since(ref, now) > in.staleAfter
}

// since is now-t, never negative.
func since(t, now time.Time) time.Duration {
	if d := now.Sub(t); d > 0 {
		return d
	}
	return 0
}

// clampPast keeps a time from being in the future, so the page never shows a
// negative duration. The zero time (unknown) stays zero.
func clampPast(t, now time.Time) time.Time {
	if t.After(now) {
		return now
	}
	return t
}

// agentStatus is the status the page shows.
func agentStatus(s model.Known[model.Status], ended bool) contract.AgentStatus {
	switch {
	case ended:
		return contract.AgentStatusEnded
	case !s.OK:
		return contract.AgentStatusUnknown
	case s.V == model.StatusBusy:
		return contract.AgentStatusBusy
	case s.V == model.StatusBlocked:
		return contract.AgentStatusBlocked
	case s.V == model.StatusFinished:
		return contract.AgentStatusFinished
	default:
		return contract.AgentStatusUnknown
	}
}

func knownStatus(s model.Status) bool {
	return s == model.StatusBusy || s == model.StatusBlocked || s == model.StatusFinished
}

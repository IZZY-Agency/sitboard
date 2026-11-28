// Package app is the seam between the core and the server.
//
// Frozen seam: internal/core implements Board; internal/server consumes it.
// Changes go through the coordinator.
package app

import (
	"context"
	"errors"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// Board is the derived, read-mostly view the server serves. All methods are
// safe for concurrent use.
type Board interface {
	// Snapshot returns the current board. Slices in it are never nil.
	Snapshot() contract.Snapshot

	// Subscribe delivers the latest snapshot after each change, until ctx
	// ends; then the channel is closed. Bursts are coalesced: a slow reader
	// gets the newest snapshot, never a backlog. It does not send the current
	// snapshot on subscribe.
	//
	// Order for callers: Subscribe first, then read Snapshot(). The other
	// order loses a change made between the two calls. Because of this order
	// the channel may then deliver a snapshot whose Seq the caller already
	// has (or an older one); callers drop any snapshot whose Seq is not
	// higher than the last one they sent.
	Subscribe(ctx context.Context) <-chan contract.Snapshot

	Catchup(ctx context.Context) (contract.Catchup, error)
	Leftovers(ctx context.Context) (contract.Leftovers, error)
	// Issues lists the open issues of the projects on the board, and asks
	// for a new look when the list is older than contract.IssuesFreshFor.
	Issues(ctx context.Context) (contract.Issues, error)

	// MarkSeen marks a card seen. Unknown card ids are an error.
	MarkSeen(cardID string) error
	// MarkCatchupRead marks read exactly the items of the catch-up list named
	// list (contract.Catchup.List). ErrCatchupStale when that list is not one
	// of this run's current ones, ErrCatchupFull when marking it would pass the
	// state file's cap: in both cases nothing was marked.
	MarkCatchupRead(list string) error
	// ForgetGrouping forgets the grouping of a project that has parts now:
	// its parts are no longer grouped under it, and its folder does not group
	// again until no agent has sat in it for a while. ErrUnknownProject when
	// the project is not on the board, ErrNotGrouping when it has no parts;
	// any other error means the state file could not be written and nothing
	// changed.
	ForgetGrouping(projectID string) error
	// SetGrouping sets the folder of a grouping on the board, or of a folder
	// listed in Snapshot.GroupingSettings, to auto, always or never.
	// ErrUnknownProject when the id is neither, ErrNotGrouping when it is a
	// project with no parts, ErrTooManySettings for a new setting past the
	// cap; any other error means the state file could not be written and
	// nothing changed.
	SetGrouping(projectID string, mode contract.GroupingMode) error
	// SetIssuePin pins (pinned true) or unpins issue number of repo, a
	// repository the board asks issues about (as Issues names it: forge,
	// host and repo path). Setting what is set already changes nothing.
	// ErrUnknownRepo when the board asks no issues of repo, ErrBadIssue for a
	// number outside 1..2^31-1, ErrTooManyPins for a new pin past the cap;
	// any other error means the state file could not be written and nothing
	// changed.
	SetIssuePin(repo model.ForgeRepo, number int, pinned bool) error

	// JumpTarget resolves a card to where its jump goes: its tmux pane or
	// its agterm session. ok is false when the card is unknown or has
	// neither (no tmux pane, no agterm session in the current tree).
	JumpTarget(cardID string) (target model.FocusTarget, ok bool)
}

// Errors ForgetGrouping and SetGrouping return for a request they refuse.
var (
	ErrUnknownProject  = errors.New("app: unknown project")
	ErrNotGrouping     = errors.New("app: the project is not a grouping")
	ErrTooManySettings = errors.New("app: too many folders have a grouping setting")
)

// Errors SetIssuePin returns for a request it refuses.
var (
	ErrUnknownRepo = errors.New("app: the board asks no issues of that repository")
	ErrBadIssue    = errors.New("app: not an issue number")
	ErrTooManyPins = errors.New("app: too many issues are pinned")
)

// ErrCatchupStale: MarkCatchupRead was given a list that is not current (one
// of an earlier run, or too old). Nothing was marked; the page loads the list
// again.
var ErrCatchupStale = errors.New("app: that catch-up list is not current; load it again")

// ErrCatchupFull: marking the list read would keep more marked items from the
// last 7 days than the state file holds. Nothing was marked: its items stay
// unread.
var ErrCatchupFull = errors.New("app: too many catch-up items marked read in the last 7 days")

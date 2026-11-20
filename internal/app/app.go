// Package app is the seam between the core and the server.
//
// Frozen seam: internal/core implements Board; internal/server consumes it.
// Changes go through the coordinator.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
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

	// MarkSeen marks a card seen. Unknown card ids are an error.
	MarkSeen(cardID string) error
	// MarkCatchupRead moves the catch-up marker to upTo, clamped to now; it
	// never moves the marker back.
	MarkCatchupRead(upTo time.Time) error
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

	// TerminalFor resolves a card to its agterm session id. ok is false when
	// the card is unknown or has no terminal in the current tree.
	TerminalFor(cardID string) (terminalID string, ok bool)
}

// Errors ForgetGrouping and SetGrouping return for a request they refuse.
var (
	ErrUnknownProject  = errors.New("app: unknown project")
	ErrNotGrouping     = errors.New("app: the project is not a grouping")
	ErrTooManySettings = errors.New("app: too many folders have a grouping setting")
)

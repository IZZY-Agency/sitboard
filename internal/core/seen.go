package core

import (
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// "Seen" is kept per card. A card is seen when the owner looked at it at or
// after the end of its agent's last turn; each new turn end makes it unseen.

// lastTurnEndLocked is when the session's last turn ended: the newest recorded
// turn end, or the time its status became "finished" when that is newer.
func (b *Board) lastTurnEndLocked(s model.SessionFact) time.Time {
	var end time.Time
	if n := len(s.TurnEnds); n > 0 {
		end = s.TurnEnds[n-1]
	}
	if s.Status.OK && s.Status.V == model.StatusFinished && s.StatusSince.After(end) {
		end = s.StatusSince
	}
	return end
}

func (b *Board) seenAtLocked(cardID string) (time.Time, bool) {
	if b.opts.State != nil {
		return b.opts.State.SeenAt(cardID)
	}
	at, ok := b.memSeen[cardID]
	return at, ok
}

func (b *Board) setSeenLocked(cardID string, at time.Time) error {
	if prev, ok := b.seenAtLocked(cardID); ok && !at.After(prev) {
		return nil
	}
	if b.opts.State != nil {
		b.writeMu.Lock()
		defer b.writeMu.Unlock()
		return b.opts.State.SetSeen(cardID, at)
	}
	b.memSeen[cardID] = at
	return nil
}

func (b *Board) isSeenLocked(cardID string, s model.SessionFact, now time.Time) bool {
	end := b.lastTurnEndLocked(s)
	// The state file prunes seen marks older than state.SeenRetention, so a
	// turn that ended longer ago counts as seen; otherwise a long-idle card
	// would fall back into Needs you once its mark is pruned.
	if !end.IsZero() && since(end, now) > state.SeenRetention {
		return true
	}
	if at, ok := b.seenAtLocked(cardID); ok && !at.Before(end) {
		return true
	}
	// Selecting the session's terminal in agterm at or after the turn ended
	// counts too. agterm reports the last selection when it is first read, so
	// this holds on a first run with no state file, and whichever of the
	// selection and the session arrives first. Only with a known turn end: an
	// old selection may belong to an earlier session in the same terminal.
	if end.IsZero() || s.TerminalID == "" {
		return false
	}
	sel, ok := b.selected[s.TerminalID]
	if !ok || sel.Before(end) {
		return false
	}
	// Keep it in the state file, so the card stays seen after agterm forgets
	// the selection or sitboard restarts.
	b.queueSeenLocked(cardID, sel)
	return true
}

// selectedLocked records the owner's latest selection of a terminal in agterm.
// A time in the future (a fast clock in agterm) counts as now, so it cannot
// keep a card seen through turns that have not happened yet.
func (b *Board) selectedLocked(sel model.TerminalSelected) {
	if sel.ID == "" || sel.At.IsZero() {
		return
	}
	at := clampPast(sel.At.Round(0), b.now())
	if prev, ok := b.selected[sel.ID]; !ok || at.After(prev) {
		b.selected[sel.ID] = at
	}
}

// queueSeenLocked remembers a seen mark to write after the board lock is
// released (flushSeen). Without a state store it is kept in memory at once.
// Callers hold b.mu.
func (b *Board) queueSeenLocked(cardID string, at time.Time) {
	if b.opts.State == nil {
		if prev, ok := b.memSeen[cardID]; !ok || at.After(prev) {
			b.memSeen[cardID] = at
		}
		return
	}
	if prev, ok := b.seenWrites[cardID]; !ok || at.After(prev) {
		b.seenWrites[cardID] = at
	}
}

// flushSeen writes the queued seen marks to the state file. It is called with
// b.mu released, so a slow disk never holds up the board.
func (b *Board) flushSeen() {
	if b.opts.State == nil {
		return
	}
	b.mu.Lock()
	if len(b.seenWrites) == 0 {
		b.mu.Unlock()
		return
	}
	writes := b.seenWrites
	b.seenWrites = map[string]time.Time{}
	b.mu.Unlock()
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	for id, at := range writes {
		if prev, ok := b.opts.State.SeenAt(id); ok && !at.After(prev) {
			continue
		}
		if err := b.opts.State.SetSeen(id, at); err != nil {
			logf("could not save the seen mark: %v", err)
		}
	}
}

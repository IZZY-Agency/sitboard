package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// catchupWindow is how far back catch-up reaches, whatever the marker says.
// The adapters keep turn times for the same 7 days.
const catchupWindow = 7 * 24 * time.Hour

// maxCatchupItems caps one answer. A week of a busy coordinator's turns can
// run into hundreds; past the cap the oldest items are left out, and Total
// says how many there were.
const maxCatchupItems = 1000

// learnedItem is when the board first listed an item, and the item's own time
// (kept to prune it once it falls out of the window).
type learnedItem struct {
	at      time.Time
	learned time.Time
	id      string // its key in the state file (state.CatchupKey)
}

// Catchup lists what the board learned after the marker, up to catchupWindow
// back: finished turns, pull requests opened and merged, and artifacts
// published, newest first by the time each happened. It is built only from
// facts the board already holds: the turn and publish times the adapters read
// from the records, and what GitHub said about each session's own pull
// requests. Never prompt text: an item carries names, a title, a time and a
// link.
//
// Read or unread is decided by when the board learned an item, not by when it
// happened, so a merge GitHub reports a minute late, or after an outage,
// still shows after the owner marked a newer turn read:
//   - An item learns its time the first time a list includes it: that list's
//     AsOf. Each list's AsOf is later than the one before, so the AsOf the
//     page sends back covers exactly what that list held.
//   - An item that happened before this run of sitboard started takes its own
//     time instead, so a restart does not list again what an earlier run's
//     marker already covered; unless the earlier run saved it as still unread
//     (learned after a marker that is later than the item: see saveUnread).
//     Left uncovered: something that happened before the marker but that no
//     run learned before this one (a merge GitHub reported only after a
//     restart).
//   - Once a list was made or the marker moved in this run, every clock tick
//     also learns what arrived since (learnTick), so what came after
//     "Caught up" is saved as unread even if no list showed it before a stop.
func (b *Board) Catchup(context.Context) (contract.Catchup, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	asOf := now
	if !asOf.After(b.lastAsOf) {
		asOf = b.lastAsOf.Add(time.Nanosecond)
	}
	var marker time.Time
	if b.opts.State != nil {
		marker = b.opts.State.CatchupMarker()
	}
	// After the marker too: what this list learns must be unread, even on a
	// clock that reads the same as when the marker was set.
	if !asOf.After(marker) {
		asOf = marker.Add(time.Nanosecond)
	}
	b.lastAsOf = asOf
	b.listed = true
	c := contract.Catchup{AsOf: asOf.UTC(), Unsaved: b.opts.StateInMemory, Pending: b.backfillPendingLocked()}
	if b.opts.State != nil {
		c.Marker = marker.UTC()
	}
	c.Items = b.collectLocked(now, asOf, marker)
	c.Total = len(c.Items)
	if len(c.Items) > maxCatchupItems {
		c.Items = c.Items[:maxCatchupItems]
	}
	return c, nil
}

// collectLocked lists the unread items, newest first, learning the new ones
// at asOf. Callers hold b.mu.
func (b *Board) collectLocked(now, asOf, marker time.Time) []contract.CatchupItem {
	items := []contract.CatchupItem{}
	from := now.Add(-catchupWindow)
	in := func(at time.Time) bool { return at.After(from) && !at.After(now) }
	unread := func(key string, at time.Time) bool {
		l, ok := b.learned[key]
		if !ok {
			l = learnedItem{at: at, learned: asOf, id: state.CatchupKey(key)}
			// Before this run started and already covered by the marker it
			// started with: read then, read now, unless the last run saved it
			// as unread. Anything else is learned now, even when it happened
			// before the start: a session read in the background after
			// "Caught up" was pressed is still unread.
			if _, saved := b.startUnread[l.id]; !saved && !at.After(b.runStart) && !at.After(b.startMarker) {
				l.learned = at
			}
			b.learned[key] = l
		}
		return l.learned.After(marker)
	}

	onBoard := make(map[model.SessionKey]string, len(b.cardKeys))
	for id, k := range b.cardKeys {
		onBoard[k] = id
	}
	// Sorted, so a pull request or artifact two sessions share is always
	// credited to the same one.
	keys := make([]model.SessionKey, 0, len(b.sessions))
	for k := range b.sessions {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, o model.SessionKey) int {
		return cmp.Or(cmp.Compare(a.Tool, o.Tool), cmp.Compare(a.ID, o.ID))
	})
	prsSeen := map[string]bool{}
	artsSeen := map[string]bool{}

	for _, k := range keys {
		s := b.sessions[k]
		if !b.mayCatchUpLocked(s) {
			continue
		}
		repo := b.repoLocked(s.Folder)
		projectID, project := contract.ProjectID(repo.MainRoot), projectName(repo.MainRoot)
		// A terminal speaks for a session only while the session runs in it: an
		// agterm pane that now runs another session lends nothing to an ended one.
		term, hasTerm := b.terminals[s.TerminalID]
		hasTerm = hasTerm && s.TerminalID != "" && !s.Ended
		name := clean(s.Name)
		if name == "" && hasTerm {
			name = clean(term.Name)
		}
		prs := b.linkedPRsLocked(s, repo)
		ttl, _ := title(prs, s, term, hasTerm)
		item := func(at time.Time, kind contract.CatchupKind, title string) contract.CatchupItem {
			return contract.CatchupItem{
				At:          at.UTC(),
				Kind:        kind,
				CardID:      onBoard[s.Key],
				ProjectID:   projectID,
				ProjectName: project,
				AgentName:   name,
				Title:       title,
			}
		}

		for _, at := range s.TurnEnds {
			key := fmt.Sprintf("turn|%s|%s|%d", s.Key.Tool, s.Key.ID, at.UnixNano())
			if in(at) && unread(key, at) {
				items = append(items, item(at, contract.CatchupTurnFinished, ttl))
			}
		}
		for _, pr := range prs {
			add := func(at time.Time, kind contract.CatchupKind) {
				key := fmt.Sprintf("%s|%s#%d", kind, pr.Repo, pr.Number)
				if !in(at) || prsSeen[key] {
					return
				}
				prsSeen[key] = true
				if !unread(key, at) {
					return
				}
				it := item(at, kind, clean(pr.Title))
				p := pagePR(pr)
				it.PullRequest = &p
				items = append(items, it)
			}
			add(pr.OpenedAt, contract.CatchupPROpened)
			if pr.State == model.PRStateMerged {
				add(pr.ClosedAt, contract.CatchupPRMerged)
			}
		}
		for _, a := range s.Artifacts {
			// A republish is a publish: with its time in the key it is a new
			// item at once, as each finished turn is.
			key := fmt.Sprintf("artifact|%s|%d", a.URL, a.PublishedAt.UnixNano())
			if !model.ValidArtifactURL(a.URL) || !in(a.PublishedAt) || artsSeen[key] {
				continue
			}
			artsSeen[key] = true
			if !unread(key, a.PublishedAt) {
				continue
			}
			it := item(a.PublishedAt, contract.CatchupArtifactPublished, clean(a.Title))
			it.Artifact = &contract.Artifact{Title: clean(a.Title), URL: a.URL}
			items = append(items, it)
		}
	}
	for key, l := range b.learned {
		if !l.at.After(from) {
			delete(b.learned, key)
		}
	}

	slices.SortFunc(items, func(a, o contract.CatchupItem) int {
		return cmp.Or(
			o.At.Compare(a.At),
			cmp.Compare(slices.Index(contract.CatchupKinds, a.Kind), slices.Index(contract.CatchupKinds, o.Kind)),
			cmp.Compare(a.ProjectID, o.ProjectID),
			cmp.Compare(a.AgentName, o.AgentName),
			cmp.Compare(a.CardID, o.CardID),
			cmp.Compare(prKey(a.PullRequest), prKey(o.PullRequest)),
			cmp.Compare(artKey(a.Artifact), artKey(o.Artifact)),
		)
	})
	return items
}

// unreadLocked is what to save as unread although it happened before marker:
// the items learned after it, and those the last run saved that this run has
// not learned yet (a session not read again yet). Callers hold b.mu.
func (b *Board) unreadLocked(marker time.Time) map[string]time.Time {
	out := map[string]time.Time{}
	known := make(map[string]bool, len(b.learned))
	for _, l := range b.learned {
		known[l.id] = true
		if l.learned.After(marker) && !l.at.After(marker) {
			out[l.id] = l.at
		}
	}
	for id, at := range b.startUnread {
		if !known[id] {
			out[id] = at
		}
	}
	return out
}

// learnTick learns what arrived since the last list or tick and saves what
// is unread although older than the marker, in one write, when it holds an
// item the file does not. Nothing is learned before the first list or mark
// of this run: until then the first list learns all of it.
func (b *Board) learnTick() {
	if b.opts.State == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.listed || b.closed {
		return
	}
	now := b.now()
	marker := b.opts.State.CatchupMarker()
	// After every list made (none of them held these) and after the marker.
	at := now
	for _, t := range []time.Time{b.lastAsOf, marker} {
		if !at.After(t) {
			at = t.Add(time.Nanosecond)
		}
	}
	// The next list comes after it too, whatever the clock does next, so
	// "Caught up" on that list covers what it learned here.
	b.lastAsOf = at
	b.collectLocked(now, at, marker)
	unread := b.unreadLocked(marker)
	news := false
	for id := range unread {
		if _, ok := b.unreadSaved[id]; !ok {
			news = true
			break
		}
	}
	if !news {
		return
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if err := b.opts.State.SetCatchup(marker, unread); err != nil {
		logf("could not save the unread catch-up items: %v", err)
		return
	}
	b.unreadSaved = unread
}

// MarkCatchupRead moves the catch-up marker to upTo: the AsOf of the list the
// owner was shown. It is clamped to the newest list made (a forged or skewed
// time cannot cover what no list held; before any list in this run, to now),
// and it never moves back (a second tab with an older list cannot bring read
// items back).
func (b *Board) MarkCatchupRead(upTo time.Time) error {
	if b.opts.State == nil {
		return errors.New("core: no state store")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	at, limit := upTo.Round(0), b.lastAsOf
	if limit.IsZero() {
		limit = b.now()
	}
	if at.After(limit) {
		at = limit
	}
	if !at.After(b.opts.State.CatchupMarker()) {
		return nil
	}
	// The marker and what stays unread behind it, in one write.
	unread := b.unreadLocked(at)
	if err := b.opts.State.SetCatchup(at, unread); err != nil {
		return err
	}
	b.listed, b.unreadSaved = true, unread
	return nil
}

// backfillPendingLocked: Claude Code is present and its adapter has not yet
// read the sessions that had ended before this start. Callers hold b.mu.
func (b *Board) backfillPendingLocked() bool {
	for _, s := range b.sources {
		if s.Kind == contract.SourceClaude {
			return s.State == contract.SourceOK && !b.backfilled[contract.SourceClaude]
		}
	}
	return false
}

// mayCatchUpLocked: a session's history counts when its id has the safe form,
// its folder is within the roots, and git has answered for its folder (so its
// project id is the one the board uses). Ended sessions count too: catch-up
// is where what left the board is still listed. Callers hold b.mu.
func (b *Board) mayCatchUpLocked(s model.SessionFact) bool {
	if s.Headless {
		return false // claude -p and SDK runs are not what the owner looks back on
	}
	if _, err := contract.CardID(string(s.Key.Tool), s.Key.ID); err != nil {
		return false
	}
	if !b.inRoots(s.Folder) {
		return false
	}
	if s.Folder == "" {
		return true
	}
	_, resolved := b.repos[s.Folder]
	return resolved
}

func prKey(p *contract.PullRequest) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%s#%09d", p.Repo, p.Number)
}

func artKey(a *contract.Artifact) string {
	if a == nil {
		return ""
	}
	return a.URL
}

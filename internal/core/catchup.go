package core

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/app"
	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// catchupWindow is how far back catch-up reaches.
// The adapters keep turn times for the same 7 days.
const catchupWindow = 7 * 24 * time.Hour

// maxCatchupItems caps one answer. A week of a busy coordinator's turns can
// run into hundreds; past the cap the oldest items are left out, and Total
// says how many there were. A variable for tests.
var maxCatchupItems = 1000

// learnedItem is a catch-up item the board knows: its own time (kept to prune
// it once it falls out of the window), its key in the state file and whether
// it is read.
type learnedItem struct {
	at   time.Time
	id   string // its key in the state file (state.CatchupKey)
	read bool
}

// listedItem is an item of a list, with its key in the learned map.
type listedItem struct {
	item contract.CatchupItem
	key  string
}

// shownList is a list the page was given: its id, its AsOf and the keys of
// the items it showed. "Caught up" on it marks those read, and nothing else.
type shownList struct {
	id   string
	asOf time.Time
	keys []string      // keys in the learned map
	ids  []learnedItem // each item's key in the state file and own time, so marking needs nothing the board may have forgotten since
}

// maxShownLists is how many of this run's newest lists can be marked read: a
// tab whose list is older than these marks nothing, and is told to load the
// list again (ErrCatchupStale). A variable for tests.
var maxShownLists = 16

// maxCatchupRead is how many acknowledged items the state file keeps; an
// acknowledgement that would pass it is refused whole (ErrCatchupFull). A
// variable for tests.
var maxCatchupRead = state.MaxCatchupRead

// ErrCatchupStale (app.ErrCatchupStale): the list to mark read is not one of
// this run's newest lists (made before a restart, or too long ago). Nothing
// was marked.
var ErrCatchupStale = app.ErrCatchupStale

// ErrCatchupFull (app.ErrCatchupFull): marking the list read would keep more acknowledged items
// than the state file holds. Nothing was marked: its items stay unread.
var ErrCatchupFull = app.ErrCatchupFull

// newListRun is this run's part of every list id: random, so a list from
// another run, whatever its clock said, never names one of this run's.
func newListRun() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Catchup lists the unread items, up to catchupWindow back: finished turns,
// pull requests opened and merged, and artifacts published, newest first by
// the time each happened. It is built only from facts the board already
// holds: the turn and publish times the adapters read from the records, and
// what GitHub said about each session's own pull requests. Never prompt text:
// an item carries names, a title, a time and a link.
//
// The invariant: an item is read only when a list that showed it was marked
// read ("Caught up"), and once marked it stays read across restarts while it
// is in the window. So:
//   - Marking a list read (MarkCatchupRead) adds exactly the items it showed to
//     the acknowledged set the state file keeps (state.CatchupRead), in one
//     write; nothing else ever adds to it, and only items older than the
//     window leave it. Items a full list left out, those another tab showed,
//     those learned since, those whose source is late or not read yet after a
//     restart: all stay unread.
//   - A list is named by an id of this run (List); a list of another run, or
//     one no longer remembered, marks nothing (ErrCatchupStale).
//   - No cap marks anything read: an acknowledgement that would pass the
//     file's cap is refused whole (ErrCatchupFull).
//   - The catch-up marker an older build (0.4.0) left is dropped when the
//     state file is opened (state.Open): what it covered is unread once. A
//     file that holds acknowledgements is written as a version older builds
//     refuse.
func (b *Board) Catchup(context.Context) (contract.Catchup, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	asOf := now
	if !asOf.After(b.lastAsOf) {
		asOf = b.lastAsOf.Add(time.Nanosecond)
	}
	b.lastAsOf = asOf
	b.listSeq++
	id := fmt.Sprintf("%s-%d", b.listRun, b.listSeq)
	c := contract.Catchup{List: id, AsOf: asOf.UTC(), Unsaved: b.opts.State != nil && b.opts.State.InMemory(), Pending: b.backfillPendingLocked(), Partial: len(b.backfillCut) > 0}
	if b.opts.State != nil {
		c.Marker = b.markedAt.UTC()
	}
	rows := b.collectLocked(now)
	c.Total = len(rows)
	shown := rows[:min(len(rows), maxCatchupItems)]
	c.Items = make([]contract.CatchupItem, 0, len(shown))
	keys := make([]string, 0, len(shown))
	ids := make([]learnedItem, 0, len(shown))
	for _, r := range shown {
		c.Items = append(c.Items, r.item)
		keys = append(keys, r.key)
		ids = append(ids, b.learned[r.key])
	}
	b.shownLists = append(b.shownLists, shownList{id: id, asOf: asOf, keys: keys, ids: ids})
	if n := len(b.shownLists); n > maxShownLists {
		b.shownLists = slices.Clone(b.shownLists[n-maxShownLists:])
	}
	return c, nil
}

// collectLocked lists the unread items, newest first, learning the new ones.
// Callers hold b.mu.
func (b *Board) collectLocked(now time.Time) []listedItem {
	items := []listedItem{}
	from := now.Add(-catchupWindow)
	in := func(at time.Time) bool { return at.After(from) && !at.After(now) }
	unread := func(key string, at time.Time) bool {
		l, ok := b.learned[key]
		if !ok {
			l = learnedItem{at: at, id: state.CatchupKey(key)}
			_, l.read = b.readSet[l.id]
			b.learned[key] = l
		}
		return !l.read
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
		prs := b.linkedPRsLocked(s)
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
				items = append(items, listedItem{item(at, contract.CatchupTurnFinished, ttl), key})
			}
		}
		for _, pr := range prs {
			add := func(at time.Time, kind contract.CatchupKind) {
				// GitHub: "<kind>|owner/name#12", byte for byte the key
				// saved read marks were made from before forges.
				key := string(kind) + "|" + model.PRRef{Repo: pr.Repo, Number: pr.Number}.Key()
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
				items = append(items, listedItem{it, key})
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
			items = append(items, listedItem{it, key})
		}
	}
	for key, l := range b.learned {
		if !l.at.After(from) {
			delete(b.learned, key)
		}
	}

	slices.SortFunc(items, func(ra, ro listedItem) int {
		a, o := ra.item, ro.item
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

// MarkCatchupRead marks read the items list showed, and nothing else: it adds
// them to the acknowledged set and saves it in one write, then turns them
// read. Marking a list again, or an older tab's list after a newer one, is
// harmless. A list that is not one of this run's newest (maxShownLists)
// marks nothing: ErrCatchupStale. One that would pass the file's cap marks
// nothing either: ErrCatchupFull. A save that fails marks nothing.
func (b *Board) MarkCatchupRead(list string) error {
	if b.opts.State == nil {
		return errors.New("core: no state store")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	i := slices.IndexFunc(b.shownLists, func(l shownList) bool { return l.id == list })
	if list == "" || i < 0 {
		return ErrCatchupStale
	}
	l := b.shownLists[i]
	from := b.now().Add(-catchupWindow)
	next := make(map[string]time.Time, len(b.readSet)+len(l.keys))
	for id, at := range b.readSet {
		if at.After(from) { // older: out of the window, never listed again
			next[id] = at
		}
	}
	added := false
	for _, li := range l.ids {
		if !li.at.After(from) { // out of the window: the file would drop it, so it takes no room
			continue
		}
		if _, ok := next[li.id]; !ok {
			next[li.id] = li.at
			added = true
		}
	}
	markedAt := b.markedAt
	if l.asOf.After(markedAt) {
		markedAt = l.asOf
	}
	if !added && markedAt.Equal(b.markedAt) {
		return nil // marked before: nothing new
	}
	if len(next) > maxCatchupRead {
		return ErrCatchupFull
	}
	if err := b.opts.State.SetCatchupRead(next, markedAt); err != nil {
		return err // not saved: nothing marked, so the page's "Nothing was lost" holds
	}
	b.readSet, b.markedAt = next, markedAt
	for _, k := range l.keys {
		if li, ok := b.learned[k]; ok {
			li.read = true
			b.learned[k] = li
		}
	}
	return nil
}

// backfillPendingLocked: Claude Code or Codex is present and its adapter has
// not yet read the sessions that had ended before this start. Callers hold
// b.mu.
func (b *Board) backfillPendingLocked() bool {
	for _, s := range b.sources {
		if (s.Kind == contract.SourceClaude || s.Kind == contract.SourceCodex) && s.State == contract.SourceOK && !b.backfilled[s.Kind] {
			return true
		}
	}
	return false
}

// spendPendingLocked: the spend days may still grow, while the sessions that
// had ended before this start are still being read (as for the catch-up
// panel). Callers hold b.mu.
func (b *Board) spendPendingLocked() bool {
	return b.backfillPendingLocked()
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
	return fmt.Sprintf("%s|%s|%s#%09d", p.Forge, p.Host, p.Repo, p.Number)
}

func artKey(a *contract.Artifact) string {
	if a == nil {
		return ""
	}
	return a.URL
}

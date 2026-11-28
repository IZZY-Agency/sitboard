package core

import (
	"cmp"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/app"
	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// Pinned issues (0.6.0, #31): the owner pins an issue of a repository the
// board asks issues about; the state file keeps it (state.Store.Pins), keyed
// by b.issueKey's spelling of the repository, so "Org/Repo" and "org/repo"
// name one pin: "github|github.com|org/repo#145". A GitLab key is the site
// (host with its web port), the mount exactly as the host's login check gave
// it, and the path under it in lower case:
// "gitlab|git.example.com:8443|GitLab/group/app#7" for a GitLab under
// /GitLab; without a mount, "gitlab|gitlab.com|group/app#7". A pinned issue among the listed rows is marked there and
// leads them; one outside them is served in RepoIssues.Pinned as the forge
// last answered it when asked by number (pinlookup.go): its title, labels,
// milestone and state, open or closed. With no such answer (not asked yet,
// no answer, or a forge that cannot ask) it has its number and address only,
// and no state (the page words it "pinned · not in the listed 30"), unless
// the forge reported it closed before (state.Store.PinsClosed): then
// "closed".
//
// Issues (core/issues.go) applies pinnedRowsLocked to each repository's
// answer before it caches it, and sets Unsaved from pinsUnsavedLocked on
// every answer.

// ErrUnknownRepo, ErrBadIssue and ErrTooManyPins are what SetIssuePin refuses.
var (
	ErrUnknownRepo = app.ErrUnknownRepo
	ErrBadIssue    = app.ErrBadIssue
	ErrTooManyPins = app.ErrTooManyPins
)

// errNoStore: a board built with no state store has nowhere to keep a pin.
var errNoStore = errors.New("core: no state store to keep pins in")

// pinKeyOfLocked is the state file's key for issue n of repo, spelled as
// b.issueKey spells the repository. Callers hold b.mu.
func (b *Board) pinKeyOfLocked(repo model.ForgeRepo, n int) string {
	k := b.issueKey(repo)
	return state.PinKey(string(k.Forge), k.Host, k.Path, n)
}

// SetIssuePin pins or unpins issue number of repo (see app.Board). The state
// file is written first; when that fails nothing changes. A change bumps the
// issues revision, so the page fetches the list again.
func (b *Board) SetIssuePin(repo model.ForgeRepo, number int, pinned bool) error {
	if number < 1 || number > math.MaxInt32 {
		return ErrBadIssue
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	known := false
	for _, r := range b.issueReposLocked() {
		if b.issueKey(r) == b.issueKey(repo) {
			repo, known = r, true
			break
		}
	}
	if !known {
		return ErrUnknownRepo
	}
	if b.opts.State == nil {
		return errNoStore
	}
	key := b.pinKeyOfLocked(repo, number)
	if _, had := b.opts.State.Pins()[key]; had == pinned {
		return nil // nothing changes
	}
	b.writeMu.Lock()
	err := b.opts.State.SetPin(key, pinned, b.now())
	b.writeMu.Unlock()
	if errors.Is(err, state.ErrTooManyPins) {
		return ErrTooManyPins
	}
	if err != nil {
		return err
	}
	b.pinChangedLocked(key, pinned)       // an answer asked before says nothing about it now
	b.forgetPinAnswerLocked(repo, number) // nor do the details of an earlier pin of it
	b.issuesRev++                         // what GET /api/v1/issues answers changed
	b.rebuildLocked()
	return nil
}

// pinsUnsavedLocked: the pins live in memory only (--no-state, a state file
// that could not be written at start, or no store at all). Callers hold b.mu.
func (b *Board) pinsUnsavedLocked() bool {
	return b.opts.State == nil || b.opts.State.InMemory()
}

// pinnedRowsLocked marks the pinned rows of listed (repo's listed issues)
// Pinned and moves them first, keeping the order within each part, and
// returns repo's pinned issues outside listed, most recently pinned first,
// each as the latest answer kept for repo says it was (none for a gone one);
// never nil. Callers hold b.mu.
func (b *Board) pinnedRowsLocked(repo model.ForgeRepo, listed []contract.OpenIssue) []contract.OpenIssue {
	out := []contract.OpenIssue{}
	if b.opts.State == nil {
		return out
	}
	pins, closed := b.opts.State.Pins(), b.opts.State.PinsClosed()
	k := b.issueKey(repo)
	at := map[int]time.Time{} // number -> when pinned, of repo's pins
	for key, t := range pins {
		forge, host, path, n, ok := state.ParsePinKey(key)
		if ok && (model.ForgeRepo{Forge: model.Forge(forge), Host: host, Path: path}) == k {
			at[n] = t
		}
	}
	if len(at) == 0 {
		return out
	}
	inList := map[int]bool{}
	for i := range listed {
		if _, ok := at[listed[i].Number]; ok {
			listed[i].Pinned = true
			inList[listed[i].Number] = true
		}
	}
	slices.SortStableFunc(listed, func(x, y contract.OpenIssue) int {
		switch {
		case x.Pinned == y.Pinned:
			return 0
		case x.Pinned:
			return -1
		}
		return 1
	})
	path := pagePath(repo.Path)
	answered := map[int]model.PinnedIssue{}
	if ri := b.issues[k]; ri.State == model.RepoIssuesOK {
		for _, p := range ri.Pinned {
			answered[p.Number] = p
		}
	}
	for n := range at {
		if inList[n] {
			continue
		}
		row := contract.OpenIssue{Number: n, Labels: []string{}, Agents: []contract.IssueAgent{}, Pinned: true}
		if path != "" {
			row.URL = model.ForgeIssueURL(repo, n)
		}
		if _, ok := closed[b.pinKeyOfLocked(repo, n)]; ok {
			row.State = contract.OpenIssueClosed
		}
		if p, ok := answered[n]; ok && p.Number == n && (p.State == model.PinOpen || p.State == model.PinClosed) {
			// The same cleaning and caps as a listed row (pageRepoIssues).
			if rows := pageRepoIssues(repo, "", model.RepoIssues{State: model.RepoIssuesOK, List: []model.OpenIssue{p.OpenIssue}}).Issues; len(rows) == 1 {
				row = rows[0]
				row.Agents, row.Pinned, row.State = []contract.IssueAgent{}, true, contract.OpenIssueOpen
				if p.State == model.PinClosed {
					row.State = contract.OpenIssueClosed
				}
			}
		}
		out = append(out, row)
	}
	slices.SortFunc(out, func(x, y contract.OpenIssue) int {
		return cmp.Or(at[y.Number].Compare(at[x.Number]), cmp.Compare(x.Number, y.Number))
	})
	return out
}

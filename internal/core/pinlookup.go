package core

import (
	"cmp"
	"slices"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// Pinned issues asked of the forge (0.6.0 stream 3b, #31): a look asks each
// repository's pinned numbers in the same call as its listed issues
// (source.IssueLister's pinned). What the forge answers fills the pinned
// rows outside the listed 30 (pinnedRowsLocked) and goes to the state file
// (state.Store.ReportPins): open clears a closed mark, closed marks the pin
// closed (kept 7 days), gone drops it.
//
// A report counts only for an answer the board applies (its generation and
// its repository's denial counter unchanged: applyIssueAnswersLocked), and
// only for a pin as it was when the look began: a pin made, removed or made
// again since (pinChanged) is left for the next look. No answer reports
// nothing.

// pinReports is what answers said about pins, by pin key, to be saved in
// one write.
type pinReports = map[string]state.PinReport

// pinLook is what one batch asked about pins, taken as it began.
type pinLook struct {
	asked  map[model.ForgeRepo][]int // by the repository as asked: its pinned numbers, most recently pinned first
	serial uint64                    // b.pinSerial as it began
}

// pinsToAskLocked is the pins of each GitHub repository of ask (matched by
// issueKey), most recently pinned first, at most model.MaxPinsAsked each.
// GitLab pins are not asked (#182): one outside the listed 30 keeps its
// number and link, and only a listed row says anything about it. Callers
// hold b.mu.
func (b *Board) pinsToAskLocked(ask []model.ForgeRepo) pinLook {
	look := pinLook{asked: map[model.ForgeRepo][]int{}, serial: b.pinSerial}
	if b.opts.State == nil {
		return look
	}
	type pinAt struct {
		n  int
		at time.Time
	}
	byRepo := map[model.ForgeRepo][]pinAt{}
	for key, at := range b.opts.State.Pins() {
		forge, host, path, n, ok := state.ParsePinKey(key)
		if !ok {
			continue
		}
		k := model.ForgeRepo{Forge: model.Forge(forge), Host: host, Path: path}
		byRepo[k] = append(byRepo[k], pinAt{n, at})
	}
	for _, r := range ask {
		if r.Forge != model.ForgeGitHub {
			continue
		}
		pins := byRepo[b.issueKey(r)]
		if len(pins) == 0 {
			continue
		}
		slices.SortFunc(pins, func(x, y pinAt) int { return cmp.Or(y.at.Compare(x.at), cmp.Compare(x.n, y.n)) })
		ns := make([]int, len(pins))
		for i, p := range pins {
			ns[i] = p.n
		}
		look.asked[r] = model.PinsToAsk(ns)
	}
	return look
}

// pinCountsLocked: an answer of look about issue n of repo may count for
// the pin it names, which is pinned now and was so, unchanged, when look
// began (the same pin). byNumber is for an answer asked by number: n must
// also be one look asked. Callers hold b.mu.
func (b *Board) pinCountsLocked(look pinLook, repo model.ForgeRepo, n int, byNumber bool) (string, bool) {
	if b.opts.State == nil {
		return "", false
	}
	key := b.pinKeyOfLocked(repo, n)
	if _, pinned := b.opts.State.Pins()[key]; !pinned || b.pinChanged[key] > look.serial {
		return key, false
	}
	return key, !byNumber || slices.Contains(look.asked[repo], n)
}

// keepPinAnswersLocked is ri with only the answers asked by number that
// count (pinCountsLocked), so a pin made again while the call was out never
// shows the old one. Callers hold b.mu.
func (b *Board) keepPinAnswersLocked(look pinLook, ri model.RepoIssues) model.RepoIssues {
	ri.Pinned = slices.DeleteFunc(slices.Clone(ri.Pinned), func(p model.PinnedIssue) bool {
		_, ok := b.pinCountsLocked(look, ri.Repo, p.Number, true)
		return !ok
	})
	return ri
}

// carryPinAnswersLocked is ri, an OK answer the board applies, with the
// details old (the answer it replaces) held for each pin that ri neither
// answers by number nor lists: an alias that came back with no answer says
// nothing new, so the pin keeps what was last verified about it. Only while
// it is the same pin: an unpin drops its details at once
// (forgetPinAnswerLocked). A repository answered anything else, or dropped,
// holds no Pinned, so it carries nothing. Callers hold b.mu.
func (b *Board) carryPinAnswersLocked(old, ri model.RepoIssues) model.RepoIssues {
	if ri.State != model.RepoIssuesOK || len(old.Pinned) == 0 {
		return ri
	}
	said := map[int]bool{}
	for _, is := range ri.List {
		said[is.Number] = true
	}
	for _, p := range ri.Pinned {
		said[p.Number] = true
	}
	for _, p := range old.Pinned {
		if said[p.Number] || p.State == model.PinGone {
			continue
		}
		ri.Pinned = append(slices.Clone(ri.Pinned), p)
	}
	return ri
}

// forgetPinAnswerLocked drops what the kept answer of repo says about issue
// n by number, at a pin change: a pin made again starts from its number and
// link, never from an earlier pin's details. Callers hold b.mu.
func (b *Board) forgetPinAnswerLocked(repo model.ForgeRepo, n int) {
	k := b.issueKey(repo)
	ri, ok := b.issues[k]
	if !ok || !slices.ContainsFunc(ri.Pinned, func(p model.PinnedIssue) bool { return p.Number == n }) {
		return
	}
	ri.Pinned = slices.DeleteFunc(slices.Clone(ri.Pinned), func(p model.PinnedIssue) bool { return p.Number == n })
	b.noteAnswerLocked(k, ri)
	b.issues[k] = ri
}

// pinReportsLocked adds to reports what ri, an answer of look the board
// applies, says about its repository's pins: a listed (open) row or an open
// pinned answer is open, a closed one closed, a gone one gone, each for a
// pin it counts for (pinCountsLocked). Only a GitHub answer reports: a
// GitLab pin keeps its number and link and nothing else (#182). Callers
// hold b.mu.
func (b *Board) pinReportsLocked(look pinLook, ri model.RepoIssues, reports pinReports) {
	if ri.State != model.RepoIssuesOK || ri.Repo.Forge != model.ForgeGitHub {
		return
	}
	note := func(n int, byNumber bool, rep state.PinReport) {
		if key, ok := b.pinCountsLocked(look, ri.Repo, n, byNumber); ok {
			reports[key] = rep
		}
	}
	for _, is := range ri.List {
		note(is.Number, false, state.PinOpen) // listed: open, asked by number or not
	}
	for _, p := range ri.Pinned { // asked by number: it says more than the list
		switch p.State {
		case model.PinOpen:
			note(p.Number, true, state.PinOpen)
		case model.PinClosed:
			note(p.Number, true, state.PinClosed)
		case model.PinGone:
			note(p.Number, true, state.PinGone)
		}
	}
}

// savePinReportsLocked writes reports to the state file (one write, none
// when nothing changes). A failed write is logged; the pins stay as they
// were. Callers hold b.mu.
func (b *Board) savePinReportsLocked(reports pinReports) {
	if len(reports) == 0 || b.opts.State == nil {
		return
	}
	b.writeMu.Lock()
	err := b.opts.State.ReportPins(reports)
	b.writeMu.Unlock()
	if err != nil {
		logf("could not save what the forge said about pinned issues: %v", err)
	}
}

// pinChangedLocked records that the pin named by key was made (pinned) or
// removed, so an answer of a look that began before says nothing about it.
// Callers hold b.mu.
func (b *Board) pinChangedLocked(key string, pinned bool) {
	b.pinSerial++
	if b.pinChanged == nil {
		b.pinChanged = map[string]uint64{}
	}
	if pinned {
		b.pinChanged[key] = b.pinSerial
	} else {
		delete(b.pinChanged, key)
	}
}

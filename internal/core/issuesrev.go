package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// The issues revision (IssuesSummary.Rev) follows one rule: it moves when
// what GET /api/v1/issues serves changes, never when a look only says again
// what is served. What is served is the state and the repositories
// (issuesSummaryLocked), each repository's rows as the page gets them
// (servedRowsLocked: its listed rows, pinned ones marked, and its pinned
// rows outside them; a by-number answer for a pin already listed is not
// served, so it does not count), and the links of the rows served
// (issueLinks). noteServedLocked keeps the last two as one signature at
// every rebuild, right after the links are built; a purge
// (issuesPurgedLocked) and a pin change (SetIssuePin) move it too, and a
// purge since the last rebuild is carried to the revision the signature
// then gives, so IssuesSummary.Purged stays the revision that dropped the
// rows. Freshness is apart: each answer moves the list's "as of"
// (issuesReadAt), which Issues reads afresh on every call, and the page
// fetches again when its FreshMs runs out.

// noteAnswerLocked records that the answer kept for k is now ri, giving it a
// new version only when it differs from the one kept before: the same answer
// again is not a change. The version only says when a repository's served
// rows must be built again to be signed (servedSigLocked). Callers hold b.mu.
func (b *Board) noteAnswerLocked(k model.ForgeRepo, ri model.RepoIssues) {
	if old, ok := b.issues[k]; ok && reflect.DeepEqual(old, ri) {
		return
	}
	if b.issuesVer == nil {
		b.issuesVer = map[model.ForgeRepo]uint64{}
	}
	b.issuesAnswers++
	b.issuesVer[k] = b.issuesAnswers
}

// servedRowsLocked is repository r's rows as GET /api/v1/issues serves them,
// before their agents (issueAgentsLocked) are added. Callers hold b.mu.
func (b *Board) servedRowsLocked(r model.ForgeRepo) contract.RepoIssues {
	k := b.issueKey(r)
	ri := pageRepoIssues(r, k.Key(), b.issues[k])
	ri.Pinned = b.pinnedRowsLocked(r, ri.Issues)
	return ri
}

// servedSig is the signature of one repository's served rows, with what it
// was built from: the version of its kept answer and its pins.
type servedSig struct {
	ver  uint64
	pins string
	sig  string
}

// servedSigLocked is the signature of r's served rows (servedRowsLocked).
// They are built again only when its kept answer's version or its pins
// (pins: each one's key, when it was pinned, and its closed mark) changed
// since they were last signed. Callers hold b.mu.
func (b *Board) servedSigLocked(r model.ForgeRepo, pins string) string {
	ver := b.issuesVer[b.issueKey(r)]
	if c, ok := b.issuesServed[r]; ok && c.ver == ver && c.pins == pins {
		return c.sig
	}
	raw, err := json.Marshal(b.servedRowsLocked(r))
	if err != nil {
		raw = []byte(err.Error())
	}
	sum := sha256.Sum256(raw)
	c := servedSig{ver: ver, pins: pins, sig: hex.EncodeToString(sum[:16])}
	if b.issuesServed == nil {
		b.issuesServed = map[model.ForgeRepo]servedSig{}
	}
	b.issuesServed[r] = c
	return c.sig
}

// noteServedLocked moves the revision when the signature of what is served
// for repos (the repositories on the board) changed since the last rebuild:
// each one's served rows (servedSigLocked) and the links of the rows served
// (listed: the listed rows, by issue). Callers hold b.mu.
func (b *Board) noteServedLocked(repos []model.ForgeRepo, links issueLinks, listed map[issueRef]model.ForgeRepo) {
	onBoard := map[model.ForgeRepo]bool{}
	for _, r := range repos {
		onBoard[b.issueKey(r)] = true
	}
	pinned := map[string]bool{}
	pinsOf := map[model.ForgeRepo]string{} // by issueKey: its pins, for servedSigLocked
	if b.opts.State != nil {
		pins, closed := b.opts.State.Pins(), b.opts.State.PinsClosed()
		for _, key := range slices.Sorted(maps.Keys(pins)) {
			forge, host, path, _, ok := state.ParsePinKey(key)
			k := model.ForgeRepo{Forge: model.Forge(forge), Host: host, Path: path}
			if !ok || !onBoard[k] {
				continue // dormant: no row is served for it
			}
			pinned[key] = true
			_, isClosed := closed[key]
			pinsOf[k] += key + " " + strconv.FormatInt(pins[key].UnixNano(), 10) + " " + strconv.FormatBool(isClosed) + "\n"
		}
	}
	for k := range b.issuesVer { // first: a dropped answer is signed as none
		if _, ok := b.issues[k]; !ok {
			delete(b.issuesVer, k)
		}
	}
	var sb strings.Builder
	shown := map[model.ForgeRepo]bool{}
	for _, r := range repos {
		k := b.issueKey(r)
		shown[r] = true
		sb.WriteString(k.Key())
		sb.WriteByte('=')
		sb.WriteString(b.servedSigLocked(r, pinsOf[k]))
		sb.WriteByte('\n')
	}
	for r := range b.issuesServed {
		if !shown[r] {
			delete(b.issuesServed, r)
		}
	}
	serves := func(r issueRef) bool {
		if _, ok := listed[r]; ok {
			return true
		}
		return pinned[state.PinKey(string(r.repo.Forge), r.repo.Host, r.repo.Path, r.number)]
	}
	sb.WriteString(links.signature(serves))
	purge := b.issuesPurged != b.issuesSeenPurge // a purge since the last rebuild
	if sig := sb.String(); sig != b.issuesSeenServed {
		b.issuesSeenServed = sig
		b.issuesRev++
		if purge {
			b.issuesPurged = b.issuesRev // the rows it dropped went in this revision
		}
	}
	b.issuesSeenPurge = b.issuesPurged
}

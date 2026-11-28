package core

import (
	"cmp"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// Issue to agent links (#171): which cards are on an issue ("closes": an open
// pull or merge request the session opened closes it) and which looked at it
// ("looked": the session's own gh or glab command named it). Nothing here
// reads a branch name or a prompt, and no title is served on a card.

// issueRef names one issue: its repository by issueKey, and its number.
type issueRef struct {
	repo   model.ForgeRepo
	number int
}

// issueLink is one card on an issue.
type issueLink struct {
	cardID string
	kind   contract.IssueKind
}

// issueLinks maps an issue to the cards on it or that looked at it, closes
// first, then by card id. A card with both kinds is there once, as closes.
type issueLinks map[issueRef][]issueLink

// signature is the links of the issues served as one string that is equal
// exactly when those links are: it tells whether what GET /api/v1/issues
// would answer changed. serves says which issues the endpoint serves rows for.
func (l issueLinks) signature(serves func(issueRef) bool) string {
	refs := make([]issueRef, 0, len(l))
	for r := range l {
		if serves(r) {
			refs = append(refs, r)
		}
	}
	slices.SortFunc(refs, func(x, y issueRef) int {
		return cmp.Or(cmp.Compare(x.repo.Key(), y.repo.Key()), cmp.Compare(x.number, y.number))
	})
	var sb strings.Builder
	for _, r := range refs {
		sb.WriteString(r.repo.Key())
		sb.WriteByte('#')
		sb.WriteString(strconv.Itoa(r.number))
		for _, k := range l[r] {
			sb.WriteByte(' ')
			sb.WriteString(k.cardID)
			sb.WriteByte(':')
			sb.WriteString(string(k.kind))
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// maxTouchFolders caps the working directories, besides the sessions' own
// folders, that issue commands may start a git lookup for on one board; a
// touch from one beyond it stays unknown.
const maxTouchFolders = 32

// touchFolderLocked is the folder a touch with no repository ran in, cleaned,
// or "" when it names none that may be asked about. The folder is looked up
// as itself (never as its parent): a nested checkout is its own repository.
// The lookup, if not made yet and under the cap, starts in the background
// through the board's bounded resolver, whatever projects there are, with no
// filesystem probe of its own; the links come when it answers. It is asked
// again with the session folders (run.go), so a failed or pending lookup is
// unknown until one answers and is retried.
//
// Accepted limit: a touch is credited by the folder's identity as of the
// latest answer, not as of its time. A folder whose repository changes after
// the touch (a nested checkout made there, a changed remote) credits the
// touch to the new repository; binding each touch to the identity at its time
// is not built. Callers hold b.mu.
func (b *Board) touchFolderLocked(t model.IssueTouch) string {
	if t.Number <= 0 || !t.Repo.IsZero() || t.Folder == "" || !filepath.IsAbs(t.Folder) {
		return ""
	}
	folder := filepath.Clean(t.Folder)
	if _, started := b.repoGen[folder]; !started {
		if len(b.touchFolders) >= maxTouchFolders || !b.inRoots(folder) {
			return ""
		}
		b.touchFolders[folder] = true
		b.startResolveAsLocked(folder, true)
	}
	return folder
}

// touchRepoLocked is the repository a "looked at" touch is about, or false
// when it cannot be told. A touch that named its repository (-R or an issue
// address) is that repository, when its forge is the tool's own. One that
// named none is the repository of the folder the call ran in, and only when
// git has answered for that folder, no remote of the checkout names another
// repository (a fork with its upstream: the tool may read either) and the
// folder's repository is on the tool's own forge. A gh touch is also unknown
// when the checkout sets a default repository (remote.*.gh-resolved), which
// gh reads before the remotes; a remote whose push address names another
// repository than its fetch address (either tool may use it) makes the folder
// unknown too. Callers hold b.mu.
func (b *Board) touchRepoLocked(t model.IssueTouch) (model.ForgeRepo, bool) {
	if t.Number <= 0 {
		return model.ForgeRepo{}, false
	}
	if !t.Repo.IsZero() {
		return t.Repo, t.Repo.Forge == t.Forge
	}
	folder := b.touchFolderLocked(t)
	repo, ok := b.repos[folder]
	if folder == "" || !ok || repo.RemotesUnclear || repo.OriginUnclear || (t.Forge == model.ForgeGitHub && repo.GHResolved) {
		return model.ForgeRepo{}, false
	}
	remote := b.shownRemoteLocked(repo)
	if !remote.Valid() || remote.Forge != t.Forge {
		return model.ForgeRepo{}, false
	}
	// Every other remote must be the same repository by the core's own
	// identity (issueKey: the mount exactly, never a folded GitLab mount) or
	// on no forge at all; one whose identity cannot be told is unknown.
	origin := b.issueKey(remote)
	for _, a := range repo.Remotes {
		if a.Repo.IsZero() {
			continue
		}
		other := b.shownAddrLocked(a.Repo, a.Bare)
		if !other.Valid() || b.issueKey(other) != origin {
			return model.ForgeRepo{}, false
		}
	}
	return remote, true
}

// linkIssuesLocked builds the links of the cards just derived (keys: card id
// to session), serves the "looked at" issues on the cards, and sets the
// "no agent seen" counts: per project, and per repository for the summary.
// A coordinator's own issue commands give no "looked at": it views the
// issues it hands out. Callers hold b.mu.
func (b *Board) linkIssuesLocked(v *view, keys map[string]model.SessionKey, parents map[model.SessionKey]model.Known[model.SessionKey], projects map[string]*contract.Project) {
	// The listed rows of the repositories on the board: only they take a
	// "looked at", and only they are counted.
	listed := map[issueRef]model.ForgeRepo{}
	byRepo := map[model.ForgeRepo][]int{} // by issueKey: the listed numbers
	for _, r := range v.issueRepos {
		k := b.issueKey(r)
		ri, ok := b.issues[k]
		if !ok || ri.State != model.RepoIssuesOK {
			continue
		}
		numbers := []int{}
		for _, is := range ri.List {
			if is.Number <= 0 || len(numbers) == model.MaxRepoIssues {
				continue
			}
			numbers = append(numbers, is.Number)
			listed[issueRef{k, is.Number}] = r
		}
		byRepo[k] = numbers
	}

	coordinator := b.coordinatorsLocked(parents)
	links := issueLinks{}
	for i := range v.cards {
		c := &v.cards[i]
		sk := keys[c.ID]
		s, ok := b.sessions[sk]
		if !ok {
			continue
		}
		on := map[issueRef]bool{}
		for _, ref := range closingIssues(b.linkedPRsLocked(s)) {
			r := issueRef{b.issueKey(ref.Repo), ref.Number}
			if !on[r] {
				on[r] = true
				links[r] = append(links[r], issueLink{c.ID, contract.IssueCloses})
			}
		}
		if s.Ended || coordinator[sk] {
			continue
		}
		looked := map[issueRef]bool{}
		for _, t := range s.TouchedIssues {
			repo, ok := b.touchRepoLocked(t)
			if !ok {
				continue
			}
			r := issueRef{b.issueKey(repo), t.Number}
			shown, isListed := listed[r]
			if !isListed || on[r] || looked[r] {
				continue
			}
			looked[r] = true
			links[r] = append(links[r], issueLink{c.ID, contract.IssueLooked})
			c.Issues = append(c.Issues, pageIssue(shown, t.Number, contract.IssueLooked))
		}
	}
	for _, l := range links {
		slices.SortFunc(l, func(x, y issueLink) int {
			return cmp.Or(
				cmp.Compare(slices.Index(contract.IssueKinds, x.kind), slices.Index(contract.IssueKinds, y.kind)),
				cmp.Compare(x.cardID, y.cardID))
		})
	}

	b.issueNoAgent = map[string]int{}
	for k, numbers := range byRepo {
		n := 0
		for _, num := range numbers {
			if len(links[issueRef{k, num}]) == 0 {
				n++
			}
		}
		b.issueNoAgent[k.Key()] = n
	}
	for _, p := range projects {
		if n, ok := b.issueNoAgent[p.IssuesKey]; ok && p.OpenIssues.Known {
			p.IssuesNoAgent = &n
		}
	}

	// A change of the links on a row GET /api/v1/issues serves (a listed one or
	// a pinned one) changes its answer; one on any other issue (a pull request
	// closing an issue outside the listed rows) is on the card only. The
	// revision follows what is served as a whole (issuesrev.go).
	b.issueLinks = links
	b.noteServedLocked(v.issueRepos, links, listed)
}

// issueAgentsLocked gives each row of one repository's answer, the pinned
// ones outside the list too, its agents. key is the repository's issueKey.
// Callers hold b.mu.
func (b *Board) issueAgentsLocked(out *contract.RepoIssues, key model.ForgeRepo) {
	for _, rows := range [][]contract.OpenIssue{out.Issues, out.Pinned} {
		for i := range rows {
			agents := []contract.IssueAgent{}
			for _, l := range b.issueLinks[issueRef{key, rows[i].Number}] {
				agents = append(agents, contract.IssueAgent{CardID: l.cardID, Kind: l.kind})
			}
			rows[i].Agents = agents
		}
	}
}

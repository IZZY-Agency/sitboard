// Package leftover decides which worktrees and branches of a repo are left
// over from merged work, on positive evidence only.
//
// A local branch is a leftover when its newest pull request from the same
// repository is merged into the default branch, none from that name is open,
// the branch adds nothing to that pull request (its tip is the pull request's
// head commit, or one of its own commits before the head: an ancestor of the
// head that the default branch on origin does not have) and no stash belongs
// to it. A worktree on
// such a branch is a leftover when it is not the main checkout, not locked,
// its files are clean (ignored files aside, which are counted) and no process
// works in it. A branch on GitHub is a leftover when it still points at the
// merged pull request's head.
//
// Everything else is left alone: merged work that still holds something is
// counted as held back, with the reason; anything that cannot be told is held
// back too. Nothing here changes a repo, and nothing offers to: it reads and
// lists.
package leftover

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// MaxNames caps the branch names asked about per repo, as the GitHub lookup does.
const MaxNames = 100

// maxStashes caps the stashes looked at; with more, every candidate is held back.
const maxStashes = 50

// Reason says why merged work was held back.
type Reason string

const (
	ReasonCommits Reason = "commits" // commits after the merged pull request
	ReasonChanges Reason = "changes" // modified, staged or untracked files
	ReasonStash   Reason = "stash"   // a stash belongs to the branch
	ReasonInUse   Reason = "inUse"   // a process works in the worktree, or a merge, rebase, bisect... under way uses it
	ReasonLocked  Reason = "locked"  // the worktree is locked
	ReasonNested  Reason = "nested"  // another checkout sits inside the worktree
	ReasonUnknown Reason = "unknown" // git or the process list could not tell
)

// Item is one merged pull request's leftovers in a repo.
type Item struct {
	PR       model.PullRequest
	Branch   string // local branch; "" when only the branch on GitHub is left
	Worktree string // absolute; "" when none
	Ignored  int    // git-ignored entries in the worktree
	// Waiting: a leftover session sits in the worktree, and every process
	// in it is that session's: the worktree is in use by its session only.
	Waiting bool
	Remote  bool // the branch on GitHub still points at the pull request's head
}

// Held is merged work that still holds something, so it is not listed.
type Held struct {
	PR     model.PullRequest
	Branch string
	// Worktrees are the branch's checkouts besides the main one, absolute.
	Worktrees []string
	Reason    Reason
}

// Repo is what the scan needs to know about one repo besides git.
type Repo struct {
	Root string // the main checkout, absolute
	PRs  []model.PullRequest
	// Incomplete names the head branches whose pull requests GitHub may not
	// all have given: merged work on them is held back as unknown.
	Incomplete map[string]bool
	// Bases names the heads that are the base of an open pull request: the
	// branch on GitHub is in use, so it is not listed.
	Bases map[string]bool
	// Procs are this user's running processes with their working folders;
	// ProcsKnown is false when they could not be listed, and then no
	// worktree is listed.
	Procs      []model.Proc
	ProcsKnown bool
	// Sessions are the sessions that are themselves leftovers: a worktree one
	// of them sits in is Waiting, whatever folder its processes report, as
	// long as every process working in it is one of theirs.
	Sessions []Sitter
	// LiveDirs are the folders of every other session and terminal the board
	// knows to be open: a worktree one of them sits in is in use, whatever
	// folder its processes report.
	LiveDirs []string
	// Checkouts are the checkouts of every repo the board knows, besides
	// this repo's own worktrees: a worktree with any checkout beneath it is
	// held back, since that checkout's files are inside it.
	Checkouts []string
}

// Sitter is a leftover session and the processes that are its own.
type Sitter struct {
	Dir string // the session's folder
	// Own are the processes that are the session's: its own process, the
	// ones it started, and the one that started it (its shell). Any other
	// process working in its worktree holds the worktree as in use.
	Own map[int]bool
}

// Names returns the branch names to ask GitHub about: local branches not
// checked out in the main checkout, then origin's, at most MaxNames.
// partial reports that some were left out.
func Names(inv model.RepoInventory) (names []string, partial bool) {
	mainBranch := ""
	for _, w := range inv.Worktrees {
		if w.Main {
			mainBranch, _ = w.Branch.Get()
		}
	}
	seen := map[string]bool{mainBranch: true, "": true}
	add := func(n string) {
		if seen[n] {
			return
		}
		seen[n] = true
		if len(names) == MaxNames {
			partial = true
			return
		}
		names = append(names, n)
	}
	for _, b := range inv.Branches {
		add(b.Name)
	}
	for _, b := range inv.Remote {
		add(b.Name)
	}
	return names, partial
}

// Scan applies the rule to one repo.
func Scan(ctx context.Context, git source.RepoInventory, r Repo, inv model.RepoInventory) (items []Item, held []Held) {
	byBranch := map[string][]model.PullRequest{}
	for _, pr := range r.PRs {
		byBranch[pr.HeadBranch] = append(byBranch[pr.HeadBranch], pr)
	}
	wtOf := map[string][]model.Worktree{} // git lets a branch be checked out in several (worktree add --force)
	busy := map[string]bool{}             // branches an operation under way holds
	mainBranch := ""
	for _, w := range inv.Worktrees {
		for _, b := range w.Busy {
			busy[b] = true
		}
		b, ok := w.Branch.Get()
		if !ok {
			continue // detached: no branch, nothing to tie to a pull request
		}
		if w.Operation {
			busy[b] = true
		}
		if w.Main {
			mainBranch = b
			continue
		}
		wtOf[b] = append(wtOf[b], w)
	}
	originTip := map[string]string{}
	for _, b := range inv.Remote {
		originTip[b.Name] = b.Tip
	}
	// Each question is asked once per look: the stash checks repeat them
	// for every branch.
	type answer struct{ yes, known bool }
	asked := map[[2]string]answer{}
	isAnc := func(a, b string) (bool, bool) {
		if an, ok := asked[[2]string{a, b}]; ok {
			return an.yes, an.known
		}
		yes, err := git.IsAncestor(ctx, r.Root, a, b)
		asked[[2]string{a, b}] = answer{yes, err == nil}
		return yes, err == nil
	}

	done := map[string]bool{}
	for _, br := range inv.Branches {
		if br.Name == mainBranch {
			continue
		}
		wts := wtOf[br.Name]
		if r.Incomplete[br.Name] {
			// Some of its pull requests may be missing, an open one among
			// them: merged work here is held back.
			done[br.Name] = true
			if pr, ok := anyMerged(byBranch[br.Name]); ok {
				h := Held{PR: pr, Branch: br.Name, Reason: ReasonUnknown}
				for _, w := range wts {
					h.Worktrees = append(h.Worktrees, w.Path)
				}
				held = append(held, h)
			}
			continue
		}
		pr, ok := mergedPR(byBranch[br.Name])
		if !ok {
			continue // not merged work: nothing to say
		}
		done[br.Name] = true
		hold := func(reason Reason) {
			h := Held{PR: pr, Branch: br.Name, Reason: reason}
			for _, w := range wts {
				h.Worktrees = append(h.Worktrees, w.Path)
			}
			held = append(held, h)
		}
		var w model.Worktree
		hasWT := len(wts) > 0
		if hasWT {
			w = wts[0]
		}
		if br.Tip != pr.HeadOID {
			in, known := isAnc(br.Tip, pr.HeadOID)
			if !known || !in {
				// The tip is not behind the head (or git cannot tell: the
				// head is not here, or git failed). A branch that carries
				// the head holds more than it; one that does not is a name
				// reused for new work, not this pull request's; when git
				// cannot tell either, it is held back as unknown.
				switch after, ok := isAnc(pr.HeadOID, br.Tip); {
				case ok && after:
					hold(ReasonCommits)
				case !ok || !known:
					hold(ReasonUnknown)
				}
				continue
			}
			// Behind the head: the tip must be one of the pull request's own
			// commits. One the default branch on origin has too is history
			// from before it (a name reused from a stale local default
			// branch starts there), or a merge commit took it: either way
			// nothing ties it to this pull request.
			base, ok := originTip[pr.BaseBranch]
			if !ok {
				hold(ReasonUnknown)
				continue
			}
			shared, known := isAnc(br.Tip, base)
			if !known {
				hold(ReasonUnknown)
				continue
			}
			if shared {
				continue
			}
		}
		if busy[br.Name] {
			hold(ReasonInUse)
			continue
		}
		if br.Symbolic {
			hold(ReasonUnknown) // deleting it would delete or break another ref
			continue
		}
		switch stashed(isAnc, inv.Stashes, br, originTip[pr.BaseBranch]) {
		case yes:
			hold(ReasonStash)
			continue
		case unknown:
			hold(ReasonUnknown)
			continue
		}
		it := Item{PR: pr, Branch: br.Name}
		if hasWT {
			switch {
			case len(wts) > 1:
				hold(ReasonInUse) // another checkout of the branch uses it
				continue
			case w.Locked:
				hold(ReasonLocked)
				continue
			case w.Prunable || w.Bare:
				hold(ReasonUnknown)
				continue
			case holdsCheckout(w.Path, inv.Worktrees, r.Checkouts):
				hold(ReasonNested) // its files would go with this one
				continue
			}
			st, err := git.TreeStatus(ctx, w.Path)
			if err != nil || !sameWorktree(st, w, inv.CommonDir, br) {
				hold(ReasonUnknown)
				continue
			}
			if !st.Clean {
				hold(ReasonChanges)
				continue
			}
			if inside(r.LiveDirs, w.Path) {
				hold(ReasonInUse) // an open session or terminal sits in it
				continue
			}
			if !r.ProcsKnown {
				hold(ReasonUnknown)
				continue
			}
			// A leftover session sits in it: it waits for that session, as
			// long as no other process works in it.
			own := map[int]bool{}
			for _, s := range r.Sessions {
				if inside([]string{s.Dir}, w.Path) {
					it.Waiting = true
					for pid := range s.Own {
						own[pid] = true
					}
				}
			}
			if occupied(r.Procs, w.Path, own) {
				hold(ReasonInUse)
				continue
			}
			it.Worktree, it.Ignored = w.Path, st.Ignored
		}
		remote(&it, r.Bases)
		items = append(items, it)
	}

	// Branches left only on GitHub, whose local branch is gone.
	for _, b := range inv.Remote {
		if done[b.Name] || b.Name == mainBranch {
			continue
		}
		if r.Incomplete[b.Name] {
			// As for a local branch: merged work is held back.
			if pr, ok := anyMerged(byBranch[b.Name]); ok {
				held = append(held, Held{PR: pr, Branch: b.Name, Reason: ReasonUnknown})
			}
			continue
		}
		pr, ok := mergedPR(byBranch[b.Name])
		if !ok {
			continue
		}
		it := Item{PR: pr}
		switch base := remote(&it, r.Bases); {
		case it.Remote:
			items = append(items, it)
		case base:
			held = append(held, Held{PR: pr, Branch: b.Name, Reason: ReasonInUse}) // an open pull request builds on it
		}
	}
	return items, held
}

// sameWorktree: git, asked from the worktree's folder, finds that very
// worktree of this repo, on the branch, at the tip the inventory saw. A
// folder replaced by another checkout, or one whose git metadata is gone (git
// then finds a repo further up), answers otherwise.
func sameWorktree(st model.TreeStatus, w model.Worktree, commonDir string, br model.Branch) bool {
	return st.Top == w.Path && st.CommonDir == commonDir &&
		filepath.Dir(st.GitDir) == filepath.Join(commonDir, "worktrees") &&
		st.Head == "refs/heads/"+br.Name && st.Tip == br.Tip
}

// remote marks the GitHub branch part when the branch there still points at
// the merged head and no open pull request has it as its base. base reports
// that one does: the branch on GitHub is still in use.
func remote(it *Item, bases map[string]bool) (base bool) {
	pr := it.PR
	at := pr.HeadRefOID != "" && pr.HeadRefOID == pr.HeadOID
	it.Remote = at && !bases[pr.HeadBranch]
	return at && bases[pr.HeadBranch]
}

// holdsCheckout reports whether a checkout other than dir, of this repo
// (wts) or any other the board knows (others), sits beneath dir.
func holdsCheckout(dir string, wts []model.Worktree, others []string) bool {
	dir = filepath.Clean(dir)
	beneath := func(p string) bool {
		p = filepath.Clean(p)
		return p != dir && inside([]string{p}, dir)
	}
	for _, w := range wts {
		if beneath(w.Path) {
			return true
		}
	}
	return slices.ContainsFunc(others, beneath)
}

// occupied reports whether a process other than own works in dir or below.
func occupied(procs []model.Proc, dir string, own map[int]bool) bool {
	for _, p := range procs {
		if !own[p.PID] && inside([]string{p.Cwd}, dir) {
			return true
		}
	}
	return false
}

// mergedPR returns the newest pull request of a head branch when it is
// merged into the default branch with a known head, and none is open.
func mergedPR(prs []model.PullRequest) (model.PullRequest, bool) {
	if len(prs) == 0 {
		return model.PullRequest{}, false
	}
	for _, pr := range prs {
		if pr.State == model.PRStateOpen {
			return model.PullRequest{}, false
		}
	}
	newest := slices.MaxFunc(prs, func(a, b model.PullRequest) int {
		if c := a.OpenedAt.Compare(b.OpenedAt); c != 0 {
			return c
		}
		return a.Number - b.Number
	})
	if newest.State != model.PRStateMerged || !newest.BaseIsDefault || newest.HeadOID == "" {
		return model.PullRequest{}, false
	}
	return newest, true
}

// anyMerged returns a merged pull request among prs, the newest.
func anyMerged(prs []model.PullRequest) (model.PullRequest, bool) {
	var m model.PullRequest
	for _, pr := range prs {
		if pr.State == model.PRStateMerged && pr.OpenedAt.Compare(m.OpenedAt) >= 0 {
			m = pr
		}
	}
	return m, m.State == model.PRStateMerged
}

type tri int

const (
	no tri = iota
	yes
	unknown
)

// stashed: a stash belongs to the branch when git's subject names it, or when
// it was made on a commit of the branch that is not on the default branch on
// origin (a stash made on main long ago is everyone's ancestor).
func stashed(isAnc func(a, b string) (bool, bool), stashes []model.Stash, br model.Branch, originDefault string) tri {
	if len(stashes) > maxStashes {
		return unknown
	}
	for _, s := range stashes {
		if strings.HasPrefix(s.Subject, "On "+br.Name+":") || strings.HasPrefix(s.Subject, "WIP on "+br.Name+":") {
			return yes
		}
	}
	for _, s := range stashes {
		if s.Base == "" {
			return unknown // a stash whose base git could not give
		}
		on, ok := isAnc(s.Base, br.Tip)
		if !ok {
			return unknown
		}
		if !on {
			continue
		}
		if originDefault == "" {
			return yes
		}
		shared, ok := isAnc(s.Base, originDefault)
		if !ok {
			return unknown
		}
		if !shared {
			return yes
		}
	}
	return no
}

// inside reports whether any of dirs is dir or below it.
func inside(dirs []string, dir string) bool {
	dir = filepath.Clean(dir)
	for _, d := range dirs {
		d = filepath.Clean(d)
		if d == dir || strings.HasPrefix(d, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

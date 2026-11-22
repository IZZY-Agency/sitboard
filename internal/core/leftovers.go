package core

import (
	"cmp"
	"context"
	"maps"
	"path/filepath"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/leftover"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

const (
	// leftoversEvery: how often the repos on the board are looked at again.
	// Leftovers are housekeeping, never urgent.
	leftoversEvery = 10 * time.Minute
	// leftoverTimeout bounds one whole look; each git or gh call has its own.
	leftoverTimeout = 2 * time.Minute
	// leftoversSoonest: a listed session that closed brings the next look
	// forward (its worktree is no longer waiting), but not sooner than this.
	leftoversSoonest = time.Minute
)

// leftoverRepo is one repo to look at, as the board knows it.
type leftoverRepo struct {
	root, projectID string
	remote          model.ForgeRepo // on GitHub; zero when unclear
	// unclear: a GitHub repo whose origin has more than one address: never
	// looked at, and the list says it is incomplete.
	unclear bool
}

// leftoverSession is an open session that has nothing left to do: in Idle
// with its turn finished, every pull request it created merged at least
// doneWindow ago, no live workers.
type leftoverSession struct {
	cardID, name, folder string
	idleSince            time.Time
	pr                   model.PullRequest // its newest merged pull request
	prs                  []model.PRRef     // every pull request it created
	root                 string            // its repo's main checkout
	pid                  int               // its process; 0 when unknown
}

// leftoversStateLocked says whether a look can be made at all.
func (b *Board) leftoversStateLocked() contract.LeftoversState {
	if _, ok := b.opts.PRs.(source.PREvidence); !ok || b.opts.Inventory == nil || b.githubOffLocked() {
		return contract.LeftoversOff
	}
	for _, s := range b.sources {
		if s.Kind == contract.SourceGit && s.State == contract.SourceMissing {
			return contract.LeftoversNoGit
		}
	}
	return contract.LeftoversReady
}

// startLeftoversLocked starts a look when one is due and none is under way.
// Callers hold b.mu.
func (b *Board) startLeftoversLocked(ctx context.Context, now time.Time) {
	if b.leftoverBusy || b.closed {
		return
	}
	if st := b.leftoversStateLocked(); st != contract.LeftoversReady {
		if b.leftovers.State != st {
			b.leftovers = contract.Leftovers{State: st, Items: []contract.Leftover{}, Held: []contract.HeldCount{}}
		}
		return
	}
	if !b.leftoverAt.IsZero() && !b.leftoversDueLocked(now) {
		return
	}
	repos, sessions, live := b.leftoverInputsLocked()
	if len(repos) == 0 {
		// No repo on the board (yet: they resolve in the background):
		// nothing is left over, and the ten minutes do not start.
		if b.leftovers.State != contract.LeftoversReady || len(b.leftovers.Items) > 0 || len(b.leftovers.Held) > 0 || b.leftovers.Partial {
			b.leftovers = contract.Leftovers{State: contract.LeftoversReady, AsOf: now.UTC(), Items: []contract.Leftover{}, Held: []contract.HeldCount{}}
		}
		return
	}
	b.leftoverAt = now
	b.leftoverSaw = b.mergedPRsLocked()
	b.leftoverBusy = true
	b.resolving.Add(1)
	go func() {
		defer b.resolving.Done()
		lctx, cancel := context.WithTimeout(ctx, leftoverTimeout)
		list := b.lookForLeftovers(lctx, repos, sessions, live)
		cancel()
		list.AsOf = now.UTC()
		b.mu.Lock()
		defer b.mu.Unlock()
		b.leftoverBusy = false
		if b.closed || ctx.Err() != nil {
			return
		}
		b.leftovers = list
		b.rebuildLocked()
	}()
}

// leftoversDueLocked: ten minutes since the last look, or a minute when a
// session in the list has closed since or the board saw a pull request
// merge. Callers hold b.mu.
func (b *Board) leftoversDueLocked(now time.Time) bool {
	gap := since(b.leftoverAt, now)
	return gap >= leftoversEvery || gap >= leftoversSoonest && (b.leftoverSessionGoneLocked() || b.newMergeLocked())
}

// mergedPRsLocked is the set of merged pull requests the board knows, from
// its sessions' lookups. Callers hold b.mu.
func (b *Board) mergedPRsLocked() map[model.PRRef]bool {
	m := map[model.PRRef]bool{}
	for ref, pr := range b.prs {
		if pr.State == model.PRStateMerged {
			m[ref] = true
		}
	}
	return m
}

// newMergeLocked: the board knows of a merged pull request it did not know
// of when the latest look started. Callers hold b.mu.
func (b *Board) newMergeLocked() bool {
	for ref := range b.mergedPRsLocked() {
		if !b.leftoverSaw[ref] {
			return true
		}
	}
	return false
}

// leftoverSessionGoneLocked says whether a session in the latest list has
// ended or left Idle since: the list is out of date. Callers hold b.mu.
func (b *Board) leftoverSessionGoneLocked() bool {
	listed := map[string]bool{}
	for _, it := range b.leftovers.Items {
		for _, p := range it.Parts {
			if p.Kind == contract.LeftoverSession {
				listed[p.CardID] = true
			}
		}
	}
	if len(listed) == 0 {
		return false
	}
	for _, c := range b.view.cards {
		if listed[c.ID] && c.Column == contract.ColumnIdle && c.Agent.Live == contract.Yes {
			delete(listed, c.ID)
		}
	}
	return len(listed) > 0
}

// leftoverInputsLocked collects the repos of the projects on the board, the
// sessions that are leftovers themselves, and the folders of every other
// session and terminal that is open (or may be: liveness unknown), which
// hold the worktrees they sit in. Callers hold b.mu.
func (b *Board) leftoverInputsLocked() ([]leftoverRepo, []leftoverSession, []string) {
	repos := map[string]leftoverRepo{}
	live := map[string]bool{} // card ids with a live session
	for _, c := range b.view.cards {
		if c.Agent.Live == contract.Yes {
			live[c.ID] = true
		}
	}
	// Workers count from the sessions, not the cards: one outside --roots
	// has no card, and still works for its coordinator. One that may be
	// running (liveness unknown) counts.
	parents := b.parentsLocked()
	hasLiveWorker := map[model.SessionKey]bool{}
	for k, s := range b.sessions {
		if p := parents[k]; p.OK && !s.Ended && (!s.Live.OK || s.Live.V) {
			hasLiveWorker[p.V] = true
		}
	}
	var sessions []leftoverSession
	leftoverKeys := map[model.SessionKey]bool{}
	leftoverTerms := map[string]bool{}
	for _, c := range b.view.cards {
		key, ok := b.cardKeys[c.ID]
		if !ok {
			continue
		}
		s := b.sessions[key]
		repo := b.repoLocked(s.Folder)
		// GitHub only: there is no evidence from any other forge yet, so a
		// GitLab repo is never looked at (nothing is offered for removal).
		onGitHub := repo.Remote.Forge == model.ForgeGitHub && repo.Remote.Valid()
		if (onGitHub || repo.OriginUnclear) && filepath.IsAbs(repo.MainRoot) {
			if _, seen := repos[repo.MainRoot]; !seen {
				repos[repo.MainRoot] = leftoverRepo{root: repo.MainRoot, remote: repo.Remote, projectID: c.ProjectID, unclear: repo.OriginUnclear}
			}
		}
		// Evidence from GitHub only, for every change the session created: one
		// merge request (or a change of a forge not listed) is no proof that
		// the work is merged, so the session is no leftover. Its folder still
		// holds its worktree: it is in liveDirs below.
		var newest model.PullRequest
		merged := false
		if onlyGitHub(s.CreatedPRs) {
			newest, merged = b.allMergedLocked(s.CreatedPRs)
		}
		if !sessionLeftover(c, s.Ended, live[c.ID], hasLiveWorker[key], newest, merged, b.opts.Now()) {
			continue
		}
		sessions = append(sessions, leftoverSession{cardID: c.ID, name: c.Agent.Name, folder: s.Folder,
			idleSince: c.Agent.StatusSince, pr: newest, prs: slices.Clone(s.CreatedPRs), root: repo.MainRoot, pid: s.PID})
		leftoverKeys[key] = true
		if s.TerminalID != "" {
			leftoverTerms[s.TerminalID] = true
		}
	}
	var liveDirs []string
	for k, s := range b.sessions {
		if !s.Ended && s.Folder != "" && !leftoverKeys[k] {
			liveDirs = append(liveDirs, s.Folder)
		}
	}
	for id, t := range b.terminals {
		if t.Folder != "" && !leftoverTerms[id] {
			liveDirs = append(liveDirs, t.Folder)
		}
	}
	slices.Sort(liveDirs)
	out := make([]leftoverRepo, 0, len(repos))
	for _, r := range repos {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, c leftoverRepo) int { return cmp.Compare(a.root, c.root) })
	return out, sessions, liveDirs
}

// onlyGitHub reports whether every change is a GitHub pull request.
func onlyGitHub(refs []model.PRRef) bool {
	return !slices.ContainsFunc(refs, func(r model.PRRef) bool { return r.Repo.Forge != model.ForgeGitHub })
}

// allMergedLocked returns the newest of the pull requests a session created,
// when there is at least one and GitHub answered each as merged with a
// merge time; a pull request it has no answer for fails. Pull requests
// tied to the session some other way (its branch) do not count. Callers
// hold b.mu.
// sessionLeftover reports whether the session of card c is a leftover: Idle
// and finished, its terminal live, not ended, without a live worker, and
// every pull request it created merged (merged; newest is the last of them to
// merge) at least doneWindow before now.
func sessionLeftover(c contract.Card, ended, live, liveWorker bool, newest model.PullRequest, merged bool, now time.Time) bool {
	return c.Column == contract.ColumnIdle && c.Agent.Status == contract.AgentStatusFinished && live && !ended && !liveWorker &&
		merged && since(newest.ClosedAt, now) >= doneWindow
}

func (b *Board) allMergedLocked(refs []model.PRRef) (model.PullRequest, bool) {
	var newest model.PullRequest
	for _, ref := range refs {
		pr, ok := b.prs[ref]
		if !ok || pr.State != model.PRStateMerged || pr.ClosedAt.IsZero() {
			return model.PullRequest{}, false
		}
		if pr.ClosedAt.After(newest.ClosedAt) {
			newest = pr
		}
	}
	return newest, len(refs) > 0
}

// lookForLeftovers runs git and gh outside the lock and builds the list.
func (b *Board) lookForLeftovers(ctx context.Context, repos []leftoverRepo, sessions []leftoverSession, live []string) contract.Leftovers {
	list := contract.Leftovers{State: contract.LeftoversReady, Items: []contract.Leftover{}, Held: []contract.HeldCount{}}
	var procs []model.Proc
	known := false
	if b.opts.Folders != nil {
		if p, err := b.opts.Folders.Procs(ctx); err == nil {
			procs, known = p, true
		}
	}
	git := &scanGit{RepoInventory: b.opts.Inventory, trees: map[string]treeAnswer{}}
	// Every repo's inventory first: a worktree with a checkout of any of
	// them beneath it is held back.
	invs := make([]*model.RepoInventory, len(repos))
	var checkouts []string
	for i, r := range repos {
		checkouts = append(checkouts, r.root)
		inv, err := git.Inventory(ctx, r.root)
		// The checkouts it registered count even when the rest of it could
		// not be read (a partial clone, say): one may sit inside a worktree
		// of another repo.
		for _, w := range inv.Worktrees {
			checkouts = append(checkouts, w.Path)
		}
		if err != nil || r.unclear || inv.Origin != r.remote {
			// This repo could not be looked at, its origin names more than
			// one repository, or not the one the board asks GitHub about:
			// nothing from it, and the page says so.
			list.Partial = true
			continue
		}
		invs[i] = &inv
	}
	sitters := make([]leftover.Sitter, len(sessions))
	for i, s := range sessions {
		sitters[i] = leftover.Sitter{Dir: s.folder, Own: ownProcs(procs, s.pid)}
	}
	held := map[contract.HeldReason]int{}
	var heldTrees []string // worktrees held back
	type tree struct {
		path string
		item int // index in list.Items
	}
	var trees []tree                           // worktrees listed
	looked := map[string]model.RepoInventory{} // repos looked at in full this time
	itemOf := map[string]int{}                 // clone and pull request -> index in list.Items
	for i, r := range repos {
		if invs[i] == nil {
			continue
		}
		inv := *invs[i]
		names, partial := leftover.Names(inv)
		if len(names) == 0 {
			looked[r.root] = inv
			continue
		}
		// Fresh evidence only: the board's own lookups may answer from a
		// cache, or with an older answer while GitHub limits the rate.
		ev, err := b.opts.PRs.(source.PREvidence).Evidence(ctx, r.remote, names)
		if err != nil {
			list.Partial = true // no answer, no evidence: nothing from this repo
			continue
		}
		// A head whose pull requests GitHub may not all have given: merged
		// work on it is held back, and work it does not show at all may be
		// missing from the list.
		partial = partial || len(ev.Incomplete) > 0
		list.Partial = list.Partial || partial
		if !partial {
			looked[r.root] = inv
		}
		var sit []leftover.Sitter
		for j, s := range sessions {
			if s.root == r.root {
				sit = append(sit, sitters[j])
			}
		}
		items, hs := leftover.Scan(ctx, git, leftover.Repo{Root: r.root, PRs: ev.PRs, Incomplete: ev.Incomplete, Bases: ev.Bases,
			Procs: procs, ProcsKnown: known, Sessions: sit, LiveDirs: live, Checkouts: checkouts}, inv)
		for _, h := range hs {
			held[contract.HeldReason(h.Reason)]++
			heldTrees = append(heldTrees, h.Worktrees...)
		}
		clone, cloneOK := b.exactHomeRel(r.root)
		for _, it := range items {
			// The page shows every path and name exactly; one it could not
			// show so (outside the home folder, or not text) is held back.
			wt, wtOK := b.exactHomeRel(it.Worktree)
			if it.Worktree != "" && !wtOK || it.Branch != "" && (!cloneOK || !utf8.ValidString(it.Branch)) ||
				it.Remote && !utf8.ValidString(it.PR.HeadBranch) {
				held[contract.HeldUnknown]++
				if it.Worktree != "" {
					heldTrees = append(heldTrees, it.Worktree)
				}
				continue
			}
			item := contract.Leftover{ProjectID: r.projectID, PullRequest: pagePR(it.PR), MergedAt: it.PR.ClosedAt.UTC(),
				Parts: []contract.LeftoverPart{}, Waiting: it.Waiting}
			if it.Worktree != "" {
				trees = append(trees, tree{path: it.Worktree, item: len(list.Items)})
				item.Parts = append(item.Parts, contract.LeftoverPart{Kind: contract.LeftoverWorktree, Path: wt, Ignored: it.Ignored})
			}
			if it.Branch != "" {
				item.Parts = append(item.Parts, contract.LeftoverPart{Kind: contract.LeftoverBranch, Branch: it.Branch, Path: clone})
			}
			if it.Remote {
				item.Parts = append(item.Parts, contract.LeftoverPart{Kind: contract.LeftoverRemoteBranch, Branch: it.PR.HeadBranch})
			}
			itemOf[cloneKey(r.root, model.PRRef{Repo: it.PR.Repo, Number: it.PR.Number})] = len(list.Items)
			list.Items = append(list.Items, item)
		}
	}
	// Sessions. The folder a session sits in decides whether it can be
	// closed: a listed worktree (its files were checked) or its repo's clean
	// main checkout on the default branch; one in a held worktree goes with
	// it; any other is held back. Its own pull requests, in its own clone,
	// decide which row it joins: never one it did not create.
	inTree := map[int]int{} // listed worktree's row -> sessions sitting in it
	joined := map[int]int{} // of those, the ones that joined that row
	for _, s := range sessions {
		if slices.ContainsFunc(heldTrees, func(wt string) bool { return under(s.folder, wt) }) {
			continue // its worktree holds work: counted with it
		}
		sitting, best := -1, ""
		for _, t := range trees {
			if under(s.folder, t.path) && len(t.path) > len(best) {
				sitting, best = t.item, t.path
			}
		}
		if sitting < 0 {
			if inv, ok := looked[s.root]; !ok || !b.onCleanDefault(ctx, git, s, inv) {
				held[contract.HeldUnknown]++
				continue
			}
		}
		if sitting >= 0 {
			inTree[sitting]++
		}
		if i, ok := ownRow(s, sitting, itemOf); ok {
			if i == sitting {
				joined[i]++
			}
			parts := list.Items[i].Parts
			n := 0
			for n < len(parts) && parts[n].Kind == contract.LeftoverSession {
				n++
			}
			list.Items[i].Parts = slices.Insert(parts, n, sessionPart(s))
			continue
		}
		pid := ""
		for _, r := range repos {
			if r.root == s.root {
				pid = r.projectID
			}
		}
		itemOf[cloneKey(s.root, model.PRRef{Repo: s.pr.Repo, Number: s.pr.Number})] = len(list.Items)
		list.Items = append(list.Items, contract.Leftover{ProjectID: pid, PullRequest: pagePR(s.pr), MergedAt: s.pr.ClosedAt.UTC(),
			Parts: []contract.LeftoverPart{sessionPart(s)}})
	}
	for i := range list.Items {
		it := &list.Items[i]
		it.OwnSession = it.Waiting && inTree[i] > 0 && inTree[i] == joined[i]
	}
	for _, r := range contract.HeldReasons {
		if n := held[r]; n > 0 {
			list.Held = append(list.Held, contract.HeldCount{Reason: r, Count: n})
		}
	}
	slices.SortStableFunc(list.Items, func(a, c contract.Leftover) int {
		return cmp.Or(c.MergedAt.Compare(a.MergedAt), cmp.Compare(a.PullRequest.Repo, c.PullRequest.Repo),
			cmp.Compare(a.PullRequest.Number, c.PullRequest.Number))
	})
	return list
}

// ownRow is the row a session joins: one of a pull request it created, in
// its own clone. The row of the worktree it sits in comes first, then the
// one of its newest merged pull request, then the first of the others.
func ownRow(s leftoverSession, sitting int, itemOf map[string]int) (int, bool) {
	rows := map[int]bool{}
	for _, ref := range s.prs {
		if i, ok := itemOf[cloneKey(s.root, ref)]; ok {
			rows[i] = true
		}
	}
	if rows[sitting] {
		return sitting, true
	}
	if i, ok := itemOf[cloneKey(s.root, model.PRRef{Repo: s.pr.Repo, Number: s.pr.Number})]; ok && rows[i] {
		return i, true
	}
	if len(rows) == 0 {
		return 0, false
	}
	return slices.Min(slices.Collect(maps.Keys(rows))), true
}

// ownProcs are the processes that are a session's: its own (pid), every one
// it started, and the one that started it (its shell). None when its process
// is not among procs.
func ownProcs(procs []model.Proc, pid int) map[int]bool {
	own := map[int]bool{}
	children := map[int][]int{}
	parent, found := 0, false
	for _, p := range procs {
		children[p.PPID] = append(children[p.PPID], p.PID)
		if p.PID == pid {
			parent, found = p.PPID, true
		}
	}
	if pid <= 0 || !found {
		return own
	}
	if parent > 1 {
		own[parent] = true
	}
	own[pid] = true
	for queue := []int{pid}; len(queue) > 0; queue = queue[1:] {
		for _, c := range children[queue[0]] {
			if !own[c] {
				own[c] = true
				queue = append(queue, c)
			}
		}
	}
	return own
}

// scanGit is the inventory for one look: each checkout's status is read
// once, however many sessions sit in it.
type scanGit struct {
	source.RepoInventory
	trees map[string]treeAnswer
}

type treeAnswer struct {
	st  model.TreeStatus
	err error
}

func (g *scanGit) TreeStatus(ctx context.Context, dir string) (model.TreeStatus, error) {
	if a, ok := g.trees[dir]; ok {
		return a.st, a.err
	}
	st, err := g.RepoInventory.TreeStatus(ctx, dir)
	g.trees[dir] = treeAnswer{st: st, err: err}
	return st, err
}

// onCleanDefault: the session's folder is in its repo's main checkout (no
// linked worktree inside it holds the folder), which is on the default branch
// its pull request merged into, at the tip the inventory saw, with no
// operation under way and clean files.
func (b *Board) onCleanDefault(ctx context.Context, git source.RepoInventory, s leftoverSession, inv model.RepoInventory) bool {
	var w model.Worktree
	for _, c := range inv.Worktrees {
		if under(s.folder, c.Path) && len(c.Path) > len(w.Path) {
			w = c
		}
	}
	branch, ok := w.Branch.Get()
	if !w.Main || !ok || w.Operation || len(w.Busy) > 0 || !s.pr.BaseIsDefault || branch != s.pr.BaseBranch {
		return false
	}
	tip := ""
	for _, br := range inv.Branches {
		if br.Name == branch {
			tip = br.Tip
		}
	}
	st, err := git.TreeStatus(ctx, w.Path)
	return err == nil && st.Clean && tip != "" && st.Top == w.Path && st.GitDir == inv.CommonDir &&
		st.CommonDir == inv.CommonDir && st.Head == "refs/heads/"+branch && st.Tip == tip
}

// exactHomeRel is homeRel without any cleaning, for a path the reader must
// tell from every other: ok is false for a path outside the home folder
// (homeRel would show its base name only) or one that is not valid UTF-8.
func (b *Board) exactHomeRel(p string) (string, bool) {
	if p == "" || !utf8.ValidString(p) || b.home == "" || b.home == "." || b.home == "/" {
		return "", false
	}
	p = filepath.Clean(p)
	if !under(p, b.home) {
		return "", false
	}
	if p == b.home {
		return "~", true
	}
	rel, err := filepath.Rel(b.home, p)
	if err != nil {
		return "", false
	}
	return "~/" + filepath.ToSlash(rel), true
}

// cloneKey names a pull request in one clone.
func cloneKey(root string, ref model.PRRef) string {
	return root + "\x00" + ref.Key()
}

func sessionPart(s leftoverSession) contract.LeftoverPart {
	return contract.LeftoverPart{Kind: contract.LeftoverSession, CardID: s.cardID, Name: s.name, IdleSince: s.idleSince.UTC()}
}

// Leftovers returns the latest list.
func (b *Board) Leftovers(context.Context) (contract.Leftovers, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.leftovers
	if l.State == "" {
		l.State = contract.LeftoversLoading
	}
	l.Items = slices.Clone(l.Items)
	if l.Items == nil {
		l.Items = []contract.Leftover{}
	}
	if l.Held == nil {
		l.Held = []contract.HeldCount{}
	}
	return l, nil
}

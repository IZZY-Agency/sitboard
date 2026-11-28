package core

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Open issues, read only (spike 15): the repositories of the projects on the
// board are asked about every issuesEvery, when one appears that was not
// asked about, and when the page asks for the list while it is older than
// contract.IssuesFreshFor; never more often than that. Each forge answers in
// batches per look, outside b.mu.
const (
	issuesEvery = 10 * time.Minute
	// issuesTimeout bounds one batch (GitHub's, or one GitLab host's); each
	// gh or glab call has its own.
	issuesTimeout = 2 * time.Minute
	// issuesRefusedHold: a GitLab host that refused glab's token is not asked
	// about issues for this long, unless a login check says yes before.
	issuesRefusedHold = 5 * time.Minute
	// maxIssueRepos caps the repositories one look asks about (GitHub: five
	// calls of 20). With more on the board, each look asks those picked
	// longest ago (never first), so every repository is asked in turn.
	maxIssueRepos = 100
	// maxLabelText caps a label name and a milestone title on the page.
	maxLabelText = 50
)

// issueBatchSlots is how many batches of a look run at once.
var issueBatchSlots = 3

// issueKey is a repository's identity for issues: GitHub ignores case in
// owner and name, so "Org/Repo" and "org/repo" are one repository. A GitLab
// project is its site (host with web port), the mount exactly as the host's
// login check gave it (GitLab under a subfolder answers only that spelling,
// and the adapter checks the mount of every answer exactly), and the path
// under the mount, whose case GitLab ignores: two checkouts of one project
// that differ only in that case are one project, counted once, while
// "/tools/group/app" and "/Tools/group/app" are two. Callers hold b.mu.
func (b *Board) issueKey(r model.ForgeRepo) model.ForgeRepo {
	switch r.Forge {
	case model.ForgeGitHub:
		r.Host, r.Path = strings.ToLower(r.Host), strings.ToLower(r.Path)
	case model.ForgeGitLab:
		if sub := b.glHosts[r.Host].sub; sub != "" {
			// A path outside the mount (another spelling of it) stays as it
			// is: it is a project of its own, which the adapter refuses.
			if rest, ok := strings.CutPrefix(r.Path, sub+"/"); ok {
				r.Path = sub + "/" + strings.ToLower(rest)
			}
			break
		}
		r.Path = strings.ToLower(r.Path)
	}
	return r
}

// issueListerLocked is the lister for repo's forge, or nil when repo is not
// Valid, its forge is off or has no lister, or it is a GitLab repository on a
// host lookups may not go to (providerFor's rule). Callers hold b.mu.
func (b *Board) issueListerLocked(repo model.ForgeRepo) source.IssueLister {
	if !repo.Valid() {
		return nil
	}
	l := b.opts.Issues[repo.Forge]
	if l == nil {
		return nil
	}
	switch repo.Forge {
	case model.ForgeGitHub:
		if b.githubOffLocked() {
			return nil
		}
	case model.ForgeGitLab:
		if b.gitlabOffLocked() || !b.glHosts[repo.Host].ok {
			return nil
		}
		if until, held := b.issuesHold[model.GitLabHostname(repo.Host)]; held && b.now().Before(until) {
			return nil // GitLab refused glab's token: left alone for a while
		}
	default:
		return nil
	}
	return l
}

// issuesOnLocked: some forge with a lister is not switched off. Callers hold
// b.mu.
func (b *Board) issuesOnLocked() bool {
	return b.opts.Issues[model.ForgeGitHub] != nil && !b.githubOffLocked() ||
		b.opts.Issues[model.ForgeGitLab] != nil && !b.gitlabOffLocked()
}

// issueReposOfLocked is the set of repositories asked about: those of the
// projects in remotes (each project's own, as its card shows it), that may
// be asked about now, each once by issueKey, sorted by key. Callers hold
// b.mu.
func (b *Board) issueReposOfLocked(remotes []model.ForgeRepo) []model.ForgeRepo {
	set := map[model.ForgeRepo]model.ForgeRepo{}
	for _, r := range remotes {
		if pagePath(r.Path) == "" || b.issueListerLocked(r) == nil {
			continue
		}
		k := b.issueKey(r)
		if have, ok := set[k]; !ok || r.Key() < have.Key() {
			set[k] = r // one spelling, the same every time
		}
	}
	repos := make([]model.ForgeRepo, 0, len(set))
	for _, r := range set {
		repos = append(repos, r)
	}
	slices.SortFunc(repos, func(x, y model.ForgeRepo) int { return cmp.Compare(b.issueKey(x).Key(), b.issueKey(y).Key()) })
	return repos
}

// issueReposLocked: the published projects' repositories (issueReposOfLocked
// at the last rebuild), less any whose forge or host was turned off since.
// Callers hold b.mu.
func (b *Board) issueReposLocked() []model.ForgeRepo {
	return b.issueReposOfLocked(b.view.issueRepos)
}

// issuesDueLocked says whether a look should start now. Callers hold b.mu.
func (b *Board) issuesDueLocked(repos []model.ForgeRepo, now time.Time) bool {
	if b.issuesAt.IsZero() {
		return true
	}
	gap := since(b.issuesAt, now)
	if gap >= issuesEvery {
		return true
	}
	if gap < contract.IssuesFreshFor {
		return false
	}
	if b.issuesWant {
		return true
	}
	for _, r := range repos {
		if _, ok := b.issuesTried[b.issueKey(r)]; !ok {
			return true
		}
	}
	return false
}

// dropIssuesLocked forgets what was listed for the repositories not in
// repos (their project left the board, or their forge or host may no longer
// be asked). It says whether anything listed went. Callers hold b.mu.
func (b *Board) dropIssuesLocked(repos []model.ForgeRepo) bool {
	keep := map[model.ForgeRepo]bool{}
	for _, r := range repos {
		keep[b.issueKey(r)] = true
	}
	gone := false
	for k := range b.issues {
		if !keep[k] {
			delete(b.issues, k)
			gone = true
		}
	}
	for k := range b.issuesTried {
		if !keep[k] {
			delete(b.issuesTried, k)
		}
	}
	for k := range b.issuesPicked {
		if !keep[k] {
			delete(b.issuesPicked, k)
		}
	}
	for k := range b.issuesReadAt {
		if _, ok := b.issues[k]; !ok {
			delete(b.issuesReadAt, k)
		}
	}
	return gone
}

// issuesChangedLocked marks a new list: the page fetches again when AsOf
// changes. Callers hold b.mu.
func (b *Board) issuesChangedLocked(now time.Time) {
	b.issuesRev++
	b.issuesListAt = now
}

// clearIssuesOffLocked forgets the whole list once issues are off, as a
// purge. rebuildLocked runs it before it publishes, so no snapshot says off
// while the board still holds rows, even with a look under way. Callers
// hold b.mu.
func (b *Board) clearIssuesOffLocked() {
	if b.issuesOnLocked() || (len(b.issues) == 0 && !b.issuesDone && b.issuesListAt.IsZero()) {
		return
	}
	clear(b.issues)
	clear(b.issuesTried)
	clear(b.issuesPicked)
	clear(b.issuesReadAt)
	// What a look under way brings back is of before: GitHub's generation
	// moves here; GitLab's moved when it went off (setGitLabBaseLocked), and
	// with no GitLab lister no GitLab batch runs.
	b.ghGen++
	b.issuesDone, b.issuesAt, b.issuesListAt = false, time.Time{}, time.Time{}
	b.issuesRev++
	b.issuesPurged = b.issuesRev
	b.issuesPage = nil
}

// issuesPurgedLocked marks a new list that lost rows it listed: the page
// drops what it holds of the list at once (IssuesSummary.Purged). Callers
// hold b.mu.
func (b *Board) issuesPurgedLocked(now time.Time) {
	b.issuesChangedLocked(now)
	b.issuesPurged = b.issuesRev
	b.issuesPage = nil // the rendered list holds the rows that went
}

// startIssuesLocked starts a look when one is due and none is under way.
// Callers hold b.mu.
func (b *Board) startIssuesLocked(ctx context.Context, now time.Time) {
	if b.issuesBusy || b.closed {
		return
	}
	if !b.issuesOnLocked() {
		b.clearIssuesOffLocked()
		return
	}
	repos := b.issueReposLocked()
	if b.dropIssuesLocked(repos) {
		b.issuesPurgedLocked(now)
		b.rebuildLocked()
	}
	if len(repos) == 0 {
		// Nothing to ask (yet: repositories resolve in the background); the
		// ten minutes do not start.
		b.issuesDone = true
		return
	}
	if !b.issuesDueLocked(repos, now) {
		return
	}
	// Those picked longest ago first; never picked before all. The order is
	// by when each was picked, not by whether its answer counted: a look
	// whose answer is dropped (its generation moved) leaves its
	// repositories due again (issuesTried), but behind those it left out,
	// so no repository past the cap waits forever.
	slices.SortStableFunc(repos, func(x, y model.ForgeRepo) int {
		return b.issuesPicked[b.issueKey(x)].Compare(b.issuesPicked[b.issueKey(y)])
	})
	if len(repos) > maxIssueRepos {
		repos = repos[:maxIssueRepos]
	}
	// One call per lister, and on GitLab per host: a host whose login went
	// is denied alone (noteHostLocked), as a merge request lookup that
	// finds it gone does, and the other hosts keep theirs. Each batch holds
	// its answer against its generation when the look started (issueGen).
	var order []issueBatch
	at := map[issueBatch]int{}
	var byBatch [][]model.ForgeRepo
	for _, r := range repos {
		k := issueBatch{l: b.issueListerLocked(r), gen: b.issueGenLocked(r)}
		if r.Forge == model.ForgeGitLab {
			k.host = r.Host
		}
		i, ok := at[k]
		if !ok {
			i = len(order)
			at[k] = i
			order = append(order, k)
			byBatch = append(byBatch, nil)
		}
		byBatch[i] = append(byBatch[i], r)
	}
	b.issuesAt, b.issuesWant, b.issuesBusy = now, false, true
	denied := map[model.ForgeRepo]uint64{} // each repository's denial counter as the look starts; read only from here
	for _, r := range repos {
		b.issuesTried[b.issueKey(r)] = now
		b.issuesPicked[b.issueKey(r)] = now
		denied[b.issueKey(r)] = b.issuesDenied[b.issueKey(r)]
	}
	b.resolving.Add(1)
	go func() {
		defer b.resolving.Done()
		// Each batch is applied the moment it returns: a login found gone
		// hides its rows at once, and one host that does not answer holds up
		// no other. At most issueBatchSlots run at a time.
		var wg sync.WaitGroup
		slots := make(chan struct{}, issueBatchSlots)
		for i, k := range order {
			slots <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				b.runIssueBatch(ctx, k, byBatch[i], denied)
			}()
		}
		wg.Wait()
		b.mu.Lock()
		defer b.mu.Unlock()
		b.issuesBusy = false
		if b.closed || ctx.Err() != nil {
			return
		}
		b.issuesDone = true
		b.rebuildLocked()
	}()
}

// issueGen is the one thing an issue answer is held against: GitHub's
// generation (b.ghGen), or the GitLab host name's stamp (hostStampLocked:
// every web port and mount of a name shares one glab login). A login event
// moves it: a refused token, a logout, no logins anywhere, another account,
// a mount that changed, issues or GitLab switched off. An answer of an older
// generation is dropped whole. A repository found gone moves only its own
// counter (deniedLocked), which drops only its answer.
type issueGen struct {
	gh uint64
	gl hostStamp
}

// issueGenLocked is repo's generation now. Callers hold b.mu.
func (b *Board) issueGenLocked(repo model.ForgeRepo) issueGen {
	if repo.Forge == model.ForgeGitLab {
		return issueGen{gl: b.hostStampLocked(repo.Host)}
	}
	return issueGen{gh: b.ghGen}
}

// deniedLocked moves repo's own denial counter (by issueKey): the answer
// for repo of a batch sent before is dropped, and repo left not asked, while
// the other repositories of the batch keep theirs. Every authoritative
// denial moves it, a repeated one too, rows listed or not. Login events
// (a refused token, a logout, no logins, another account) move the host
// name's or GitHub's generation instead (issueGen). Callers hold b.mu.
func (b *Board) deniedLocked(repo model.ForgeRepo) {
	k := b.issueKey(repo)
	if _, known := b.issuesDenied[k]; !known && len(b.issuesDenied) >= 4096 {
		// A cleared count could meet an old one again: every answer under
		// way is dropped instead.
		clear(b.issuesDenied)
		b.ghGen++
		b.glEpoch++
	}
	b.issuesDenied[k]++
}

// deniedSinceLocked: some denial of repo was recorded since its counter
// read was at. Callers hold b.mu.
func (b *Board) deniedSinceLocked(repo model.ForgeRepo, at map[model.ForgeRepo]uint64) bool {
	k := b.issueKey(repo)
	return b.issuesDenied[k] != at[k]
}

// issueBatch is one call of a look: one lister, on GitLab one host (site),
// with the generation it is held against.
type issueBatch struct {
	l    source.IssueLister
	host string // the GitLab host (site) of every repository; "" on GitHub
	gen  issueGen
}

// currentLocked: k's answer may still be applied: the board still runs, its
// generation did not move and, on GitLab, the host may still be asked.
// Callers hold b.mu.
func (k issueBatch) currentLocked(ctx context.Context, b *Board, repo model.ForgeRepo) bool {
	if ctx.Err() != nil || b.closed || b.issueGenLocked(repo) != k.gen {
		return false
	}
	return k.host == "" || !b.gitlabOffLocked() && b.glHosts[k.host].ok
}

// runIssueBatch asks k's lister about repos under its own time and applies
// the answer under b.mu at once. An answer whose generation moved meanwhile
// is dropped whole, and its repositories count as not asked; a repository
// found gone since the look started (its counter moved from denied) loses
// only its own answer. Runs with no lock held.
func (b *Board) runIssueBatch(ctx context.Context, k issueBatch, repos []model.ForgeRepo, denied map[model.ForgeRepo]uint64) {
	stale := func() { // Callers hold b.mu.
		for _, r := range repos {
			delete(b.issuesTried, b.issueKey(r))
		}
	}
	b.mu.Lock()
	var ask []model.ForgeRepo
	if k.currentLocked(ctx, b, repos[0]) {
		// A batch may have waited for a slot: only the repositories still
		// on the board go out; one whose project left is not sent, and
		// stays not asked.
		still := map[model.ForgeRepo]bool{}
		for _, r := range b.issueReposLocked() {
			still[b.issueKey(r)] = true
		}
		for _, r := range repos {
			switch {
			case !still[b.issueKey(r)]:
				delete(b.issuesTried, b.issueKey(r))
			case b.issueListerLocked(r) == k.l:
				ask = append(ask, r)
			}
		}
	} else {
		stale()
	}
	look := b.pinsToAskLocked(ask) // the pins as this batch begins (pinlookup.go)
	b.mu.Unlock()
	if len(ask) == 0 {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, issuesTimeout)
	ans, err := k.l.OpenIssues(lctx, ask, look.asked)
	timedOut := lctx.Err() != nil
	cancel()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || ctx.Err() != nil {
		return
	}
	current := k.currentLocked(ctx, b, ask[0])
	kick := false
	// A repository found gone while the call was out (a merge request
	// lookup's denial) loses its answer alone, and is left not asked; the
	// others of the batch keep theirs.
	ans = slices.DeleteFunc(ans, func(ri model.RepoIssues) bool { return b.deniedSinceLocked(ri.Repo, denied) })
	for _, r := range ask {
		if b.deniedSinceLocked(r, denied) {
			delete(b.issuesTried, b.issueKey(r))
		}
	}
	switch {
	case k.host != "" && errors.Is(err, source.ErrNoLogins):
		// glab is logged in to no host: whatever the generation, every host
		// is denied and loses what it listed, and nothing under way about
		// any host counts (noteHostLocked).
		if !b.gitlabOffLocked() {
			b.noteHostLocked(k.host, err)
			b.refreshGitLabLineLocked()
		}
	case k.host != "" && errors.Is(err, source.ErrLoginChanged):
		// glab answered for another account than before: a new login,
		// whatever the generation. What the old one brought from every site
		// of the host name goes, nothing under way about it counts, and its
		// sites are checked again at once.
		if !b.gitlabOffLocked() {
			b.loginChangedLocked(k.host)
			kick = true
		}
	case k.host != "" && (errors.Is(err, source.ErrTokenRefused) || errors.Is(err, source.ErrNotLoggedIn) && !current):
		// GitLab refused the token, or said the login is gone in an older
		// generation. Whatever the login check that follows finds (it may
		// time out), what was listed from every site of the host name is
		// hidden now, a check under way is void, and the name is left alone
		// for issuesRefusedHold: no issue call, and no check forced by one,
		// before. Only a check made from now releases it.
		if !b.gitlabOffLocked() {
			b.refusedLocked(k.host)
			kick = true
		}
	case errors.Is(err, source.ErrNotLoggedIn) && k.host != "":
		// Whatever came with the error is of a login that is gone: the host
		// name is denied until its next check says otherwise, and what was
		// listed from it goes (forgetHostLocked), on the page at once.
		b.noteHostLocked(k.host, err)
		b.refreshGitLabLineLocked()
	case errors.Is(err, source.ErrNotLoggedIn):
		// GitHub's login is gone: it sees none of what it listed before,
		// asked this time or not, whatever the generation.
		// A purge only when it drops a kept answer: once they are gone, a
		// look logged out again changes nothing served.
		b.ghGen++
		dropped := false
		for key, ri := range b.issues {
			if b.opts.Issues[ri.Repo.Forge] == k.l {
				delete(b.issues, key)
				delete(b.issuesReadAt, key)
				dropped = true
			}
		}
		if dropped {
			b.issuesPurgedLocked(b.now())
		}
		b.issuesDone = true
		if gh := b.opts.Issues[model.ForgeGitHub]; gh == k.l {
			b.ghLogin = loginLost
			b.refreshGitHubLineLocked()
		}
	case !current:
		stale()
	case errors.Is(err, source.ErrNotChecked) && k.host != "":
		// glab made no call (the host's yes ran out), maybe after earlier
		// chunks were answered: what came is of this generation and is
		// applied; the repositories left unanswered are not counted as
		// asked, and the host is checked again at once; once that check
		// passes, a look asks them (the minute still holds).
		b.applyIssueAnswersLocked(k.l, ask, ans, look)
		answered := map[model.ForgeRepo]bool{}
		for _, ri := range ans {
			answered[ri.Repo] = true
		}
		for _, r := range repos {
			if !answered[r] {
				delete(b.issuesTried, b.issueKey(r))
			}
		}
		if d, ok := b.glHosts[k.host]; ok {
			d.at = time.Time{}
			b.glHosts[k.host] = d
		}
		kick = true
	default:
		if err != nil && !errors.Is(err, source.ErrRateLimited) && !timedOut {
			logf("could not list open issues: %v", err)
		}
		b.applyIssueAnswersLocked(k.l, ask, ans, look)
	}
	b.rebuildLocked()
	if kick {
		b.kickRefresh()
	}
}

// refusedLocked: GitLab refused glab's token at host. Every site of its host
// name loses its listed issues at once (glab keeps one login per name), the
// name's generation moves, the name is held for issuesRefusedHold, and each
// of its sites is checked once at once. Callers hold b.mu.
func (b *Board) refusedLocked(host string) {
	name := model.GitLabHostname(host)
	b.bumpHostLocked(name)
	b.issuesHold[name] = b.now().Add(issuesRefusedHold)
	b.hideHostIssuesLocked(name)
	for h, d := range b.glHosts {
		if model.GitLabHostname(h) == name {
			d.at = time.Time{} // one login check at once
			b.glHosts[h] = d
		}
	}
	b.refreshGitLabLineLocked()
}

// loginChangedLocked: glab's login for GitLab host's name is another
// account now. Every site of the name loses what the board kept from it
// (forgetHostLocked: merge requests and issues, as a purge), the name's
// generation moves, and each site is checked once at once. Unlike a
// refusal, the name is not held: the new login may be asked as soon as its
// check says yes. Callers hold b.mu.
func (b *Board) loginChangedLocked(host string) {
	name := model.GitLabHostname(host)
	b.bumpHostLocked(name)
	b.hideHostIssuesLocked(name)
	for h, d := range b.glHosts {
		if model.GitLabHostname(h) == name {
			b.forgetHostLocked(h)
			d.at = time.Time{} // one login check at once
			b.glHosts[h] = d
		}
	}
	b.refreshGitLabLineLocked()
}

// applyIssueAnswersLocked stores the answers of one batch that still count,
// each with the time it arrived, and saves what they say about the pins look
// asked (pinlookup.go). Callers hold b.mu.
func (b *Board) applyIssueAnswersLocked(l source.IssueLister, ask []model.ForgeRepo, ans []model.RepoIssues, look pinLook) {
	reports := pinReports{}
	purged := b.dropIssuesLocked(b.issueReposLocked())
	still := map[model.ForgeRepo]bool{}
	for _, r := range b.issueReposLocked() {
		still[b.issueKey(r)] = true
	}
	arrived := b.now()
	changed, sure := false, false
	for _, ri := range ans {
		// Its project may have left during the look, or its forge or host
		// been turned off or held since (the generation is checked before).
		k := b.issueKey(ri.Repo)
		if !slices.Contains(ask, ri.Repo) || !still[k] || b.issueListerLocked(ri.Repo) != l {
			continue
		}
		// Rows this login can no longer see (gone, or no longer visible to
		// it) leave like any other drop: a purge.
		if old, ok := b.issues[k]; ok && old.State == model.RepoIssuesOK && ri.State != model.RepoIssuesOK {
			purged = true
		}
		ri = b.carryPinAnswersLocked(b.issues[k], b.keepPinAnswersLocked(look, ri))
		b.noteAnswerLocked(k, ri)
		b.issues[k] = ri
		b.issuesReadAt[k] = arrived
		b.pinReportsLocked(look, ri, reports)
		changed, sure = true, true
	}
	b.issuesDone = true
	b.savePinReportsLocked(reports)
	switch {
	case purged:
		b.issuesPurgedLocked(arrived)
	case changed:
		// Freshness only: the revision moves at the rebuild that follows,
		// when what is served changed (issuesrev.go).
		b.issuesListAt = arrived
	}
	if gh := b.opts.Issues[model.ForgeGitHub]; gh != nil && gh == l {
		if sure {
			b.ghLogin = loginWorks
		}
		b.refreshGitHubLineLocked()
	}
}

// hideHostIssuesLocked drops what was listed from every site of GitLab host
// name, and that it was asked, as a purge: the page drops it at once.
// Callers hold b.mu.
func (b *Board) hideHostIssuesLocked(name string) {
	on := func(r model.ForgeRepo) bool {
		return r.Forge == model.ForgeGitLab && model.GitLabHostname(r.Host) == name
	}
	listed := len(b.issues)
	maps.DeleteFunc(b.issues, func(r model.ForgeRepo, _ model.RepoIssues) bool { return on(r) })
	maps.DeleteFunc(b.issuesReadAt, func(r model.ForgeRepo, _ time.Time) bool { return on(r) })
	maps.DeleteFunc(b.issuesTried, func(r model.ForgeRepo, _ time.Time) bool { return on(r) })
	if len(b.issues) != listed {
		b.issuesPurgedLocked(b.now())
	}
}

// loginSeen is what the latest issue look said about a forge's login.
type loginSeen int8

const (
	loginUnknown loginSeen = iota // no look said yet
	loginLost                     // a look was refused: not logged in
	loginWorks                    // a look answered
)

// ghNotLoggedIn is the GitHub source line's detail for a login that is
// gone, as the GitHub provider words it.
var ghNotLoggedIn = "not logged in to " + model.GitHubHost

// refreshGitHubLineLocked keeps the GitHub source line honest about the
// login while sitboard runs: gh is probed once at start, so an issue look
// that confirms the login is gone makes the line say so, and a later look
// that answers puts the probe's line back (or clears the probe's own "not
// logged in"). It runs no gh. Reports whether the line changed. Callers
// hold b.mu.
func (b *Board) refreshGitHubLineLocked() bool {
	base := b.ghBase
	if base.Kind != contract.SourceGitHub || base.State == contract.SourceOff || base.State == contract.SourceMissing {
		return false
	}
	line := base
	switch b.ghLogin {
	case loginLost:
		line.State, line.Detail = contract.SourceFailing, ghNotLoggedIn
	case loginWorks:
		if base.State == contract.SourceFailing && base.Detail == ghNotLoggedIn {
			line.State, line.Detail = contract.SourceOK, ""
		}
	}
	return b.setSourceLineLocked(line)
}

// projectIssuesLocked is the count a project with repository r shows.
// Callers hold b.mu.
func (b *Board) projectIssuesLocked(r model.ForgeRepo) contract.IssueCount {
	ri, ok := b.issues[b.issueKey(r)]
	if !ok || ri.State != model.RepoIssuesOK || b.issueListerLocked(r) == nil {
		return contract.IssueCount{}
	}
	return contract.IssueCount{Known: true, Open: ri.Open}
}

// issuesStateLocked: off, loading until the first look answered, then ready.
// Callers hold b.mu.
func (b *Board) issuesStateLocked() contract.IssuesState {
	switch {
	case !b.issuesOnLocked():
		return contract.IssuesOff
	case !b.issuesDone:
		return contract.IssuesLoading
	}
	return contract.IssuesReady
}

// issuesSummaryLocked is the header's part of the list, for repos (the
// projects of the view being built). Callers hold b.mu.
func (b *Board) issuesSummaryLocked(repos []model.ForgeRepo) contract.IssuesSummary {
	st := b.issuesStateLocked()
	sum := contract.IssuesSummary{State: st, Purged: b.issuesPurged}
	if st == contract.IssuesOff {
		sum.Rev = b.issuesRev
		return sum // with the purge, so a tab drops a list it holds
	}
	sum.AsOf = b.issuesAsOfLocked(repos)
	noAgent, known := 0, false
	for _, r := range repos {
		sum.Open += b.projectIssuesLocked(r).Open
		if n, ok := b.issueNoAgent[b.issueKey(r).Key()]; ok {
			noAgent, known = noAgent+n, true
		}
	}
	if known {
		sum.NoAgent = &noAgent
	}
	// What Issues() returns also changes with the repositories and the state
	// (a repository found, one that left, loading turning ready), not only
	// with the rows: any of them is a new revision.
	if st != b.issuesSeenState || !slices.Equal(repos, b.issuesSeenRepos) {
		b.issuesRev++
		b.issuesSeenState, b.issuesSeenRepos = st, slices.Clone(repos)
	}
	sum.Rev = b.issuesRev
	return sum
}

// issuesAsOfLocked is the list's "as of": when the oldest answer among
// repos was read, so a repository whose latest answer failed does not pass
// for one read with the others; when none was read, when the list last
// changed. Zero for neither. Callers hold b.mu.
func (b *Board) issuesAsOfLocked(repos []model.ForgeRepo) time.Time {
	var at time.Time
	for _, r := range repos {
		k := b.issueKey(r)
		if _, ok := b.issues[k]; !ok {
			continue
		}
		if t, ok := b.issuesReadAt[k]; ok && (at.IsZero() || t.Before(at)) {
			at = t
		}
	}
	if at.IsZero() {
		at = b.issuesListAt
	}
	if at.IsZero() {
		return time.Time{}
	}
	return at.UTC()
}

// issuesPage is the latest list as the page gets it, kept until the list,
// the repositories or the state change, so tabs do not rebuild it each time.
type issuesPage struct {
	rev   uint64
	repos []model.ForgeRepo
	out   contract.Issues
}

// Issues returns the open issues of the repositories of the projects on the
// board. When the list is older than contract.IssuesFreshFor it also asks
// for a new look, which a later snapshot announces (Issues.AsOf). The
// answer is shared: callers must not change it.
func (b *Board) Issues(context.Context) (contract.Issues, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.issuesStateLocked()
	if st == contract.IssuesOff {
		// The current rev: a tab that saw the off purge takes this answer.
		return contract.Issues{State: st, Repos: []contract.RepoIssues{}, Rev: b.issuesRev, Unsaved: b.pinsUnsavedLocked()}, nil
	}
	repos := b.issueReposLocked()
	c := b.issuesPage
	if c == nil || c.rev != b.issuesRev || c.out.State != st || !slices.Equal(c.repos, repos) {
		out := contract.Issues{State: st, Repos: make([]contract.RepoIssues, 0, len(repos))}
		out.AsOf = b.issuesAsOfLocked(repos)
		for _, r := range repos {
			ri := b.servedRowsLocked(r) // before it is cached: pins bump issuesRev
			b.issueAgentsLocked(&ri, b.issueKey(r))
			out.Repos = append(out.Repos, ri)
		}
		c = &issuesPage{rev: b.issuesRev, repos: repos, out: out}
		b.issuesPage = c
	}
	// lookAt: when the latest look started, or starts now (this GET asks for one).
	now, lookAt := b.now(), b.issuesAt
	if !b.issuesBusy && (b.issuesAt.IsZero() || since(b.issuesAt, now) >= contract.IssuesFreshFor) {
		b.issuesWant = true
		select {
		case b.issuesCh <- struct{}{}:
		default:
		}
		lookAt = now
	}
	out := c.out                         // shallow: Repos stays shared
	out.AsOf = b.issuesAsOfLocked(repos) // freshness moves without the rev (issuesrev.go)
	out.Rev = c.rev
	out.FreshMs = max(0, (contract.IssuesFreshFor - since(lookAt, now)).Milliseconds())
	out.Unsaved = b.pinsUnsavedLocked()
	return out, nil
}

// issueHostsLocked lists the GitLab hosts of the repositories with listed
// issues, so the page may link them even with no merge request there.
// Callers hold b.mu.
func (b *Board) issueHostsLocked() []string {
	var hosts []string
	for _, ri := range b.issues {
		r := ri.Repo
		if r.Forge == model.ForgeGitLab && ri.State == model.RepoIssuesOK && len(ri.List) > 0 && b.issueListerLocked(r) != nil {
			hosts = append(hosts, r.Host)
		}
	}
	return hosts
}

// pageRepoIssues is one repository's answer as the page gets it: cleaned
// text, addresses built for its own repository and numbers; pending when the
// forge has not answered (ri is zero).
func pageRepoIssues(r model.ForgeRepo, key string, ri model.RepoIssues) contract.RepoIssues {
	out := contract.RepoIssues{
		Forge: contract.Forge(r.Forge), Host: clean(r.Host), Repo: pagePath(r.Path), Key: key,
		State: contract.RepoIssuesPending, Issues: []contract.OpenIssue{},
	}
	switch ri.State {
	case model.RepoIssuesOK:
		out.State, out.Open = contract.RepoIssuesOK, max(ri.Open, 0)
	case model.RepoIssuesDisabled:
		out.State = contract.RepoIssuesDisabled
		return out
	case model.RepoIssuesNotFound:
		out.State = contract.RepoIssuesNotFound
		return out
	default:
		return out
	}
	for _, is := range ri.List {
		if is.Number <= 0 || len(out.Issues) == model.MaxRepoIssues {
			continue
		}
		oi := contract.OpenIssue{
			Number:    is.Number,
			Title:     clean(is.Title),
			Milestone: capText(clean(is.Milestone), maxLabelText),
			Labels:    []string{},
		}
		if !is.MilestoneDue.IsZero() && oi.Milestone != "" {
			oi.MilestoneDue = is.MilestoneDue.UTC()
		}
		if !is.UpdatedAt.IsZero() {
			oi.UpdatedAt = is.UpdatedAt.UTC()
		}
		if out.Repo != "" {
			oi.URL = model.ForgeIssueURL(r, is.Number)
		}
		for _, l := range is.Labels {
			if l = capText(clean(l), maxLabelText); l != "" && len(oi.Labels) < model.MaxIssueLabels {
				oi.Labels = append(oi.Labels, l)
			}
		}
		out.Issues = append(out.Issues, oi)
	}
	out.Open = max(out.Open, len(out.Issues))
	return out
}

// capText cuts s to n runes, marking the cut with "…".
func capText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

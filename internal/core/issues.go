package core

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
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
	// issuesTimeout bounds one whole look; each gh or glab call has its own.
	issuesTimeout = 2 * time.Minute
	// maxIssueRepos caps the repositories one look asks about (GitHub: five
	// calls of 20). With more on the board, each look asks those asked
	// longest ago (never first), so every repository is answered in turn.
	maxIssueRepos = 100
	// maxLabelText caps a label name and a milestone title on the page.
	maxLabelText = 50
)

// issueKey is a repository's identity for issues: GitHub ignores case in
// owner and name, so "Org/Repo" and "org/repo" are one repository.
func issueKey(r model.ForgeRepo) model.ForgeRepo {
	if r.Forge == model.ForgeGitHub {
		r.Host, r.Path = strings.ToLower(r.Host), strings.ToLower(r.Path)
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
		k := issueKey(r)
		if have, ok := set[k]; !ok || r.Key() < have.Key() {
			set[k] = r // one spelling, the same every time
		}
	}
	repos := make([]model.ForgeRepo, 0, len(set))
	for _, r := range set {
		repos = append(repos, r)
	}
	slices.SortFunc(repos, func(x, y model.ForgeRepo) int { return cmp.Compare(issueKey(x).Key(), issueKey(y).Key()) })
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
		if _, ok := b.issuesTried[issueKey(r)]; !ok {
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
		keep[issueKey(r)] = true
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
	// Those asked longest ago first; never asked before all.
	slices.SortStableFunc(repos, func(x, y model.ForgeRepo) int {
		return b.issuesTried[issueKey(x)].Compare(b.issuesTried[issueKey(y)])
	})
	if len(repos) > maxIssueRepos {
		repos = repos[:maxIssueRepos]
	}
	byLister := map[source.IssueLister][]model.ForgeRepo{}
	var order []source.IssueLister
	stamps := map[string]hostStamp{} // GitLab host -> its stamp now
	for _, r := range repos {
		l := b.issueListerLocked(r)
		if _, ok := byLister[l]; !ok {
			order = append(order, l)
		}
		byLister[l] = append(byLister[l], r)
		if r.Forge == model.ForgeGitLab {
			stamps[r.Host] = b.hostStampLocked(r.Host)
		}
	}
	b.issuesAt, b.issuesWant, b.issuesBusy = now, false, true
	// A purge from elsewhere during the look (a repository a pull request
	// lookup found gone, a host denied, issues switched off and on) makes
	// everything it brings back older than the purge.
	cut := b.issuesPurged
	for _, r := range repos {
		b.issuesTried[issueKey(r)] = now
	}
	// current: repo may still be asked about by l, and nothing about its
	// GitLab host changed since the look started. Callers hold b.mu.
	current := func(r model.ForgeRepo, l source.IssueLister) bool {
		if b.issueListerLocked(r) != l {
			return false
		}
		return r.Forge != model.ForgeGitLab || b.issueHostCurrentLocked(ctx, r.Host, stamps[r.Host])
	}
	b.resolving.Add(1)
	go func() {
		defer b.resolving.Done()
		lctx, cancel := context.WithTimeout(ctx, issuesTimeout)
		type answered struct {
			ri model.RepoIssues
			l  source.IssueLister
		}
		var got []answered
		var lost []source.IssueLister // listers that lost their login
		for _, l := range order {
			b.mu.Lock()
			var ask []model.ForgeRepo
			for _, r := range byLister[l] {
				if current(r, l) {
					ask = append(ask, r)
				}
			}
			b.mu.Unlock()
			if len(ask) == 0 {
				continue
			}
			ans, err := l.OpenIssues(lctx, ask)
			switch {
			case errors.Is(err, source.ErrNotLoggedIn):
				// Whatever came with the error is of a login that is gone.
				lost = append(lost, l)
				continue
			case err != nil && !errors.Is(err, source.ErrRateLimited) && lctx.Err() == nil:
				logf("could not list open issues: %v", err)
			}
			b.mu.Lock()
			for _, ri := range ans {
				// Dropped here already, so no title of a host that went
				// waits in memory for the listers after this one; the
				// final check below decides alone what is listed.
				if slices.Contains(ask, ri.Repo) && current(ri.Repo, l) {
					got = append(got, answered{ri, l})
				}
			}
			b.mu.Unlock()
		}
		cancel()
		b.mu.Lock()
		defer b.mu.Unlock()
		b.issuesBusy = false
		if b.closed || ctx.Err() != nil {
			return
		}
		purged := b.dropIssuesLocked(b.issueReposLocked())
		// A lost login sees none of what it listed before, asked this time or not.
		for k, ri := range b.issues {
			if slices.Contains(lost, b.opts.Issues[ri.Repo.Forge]) {
				delete(b.issues, k)
				purged = true
			}
		}
		changed := false
		still := map[model.ForgeRepo]bool{}
		for _, r := range b.issueReposLocked() {
			still[issueKey(r)] = true
		}
		sure := map[source.IssueLister]bool{} // listers with rows listed: their login works
		if b.issuesPurged != cut {
			got = nil
			b.issuesWant = true // asked again once the minute is over
		}
		for _, a := range got {
			// Its project may have left during the look, or its host been
			// denied or changed since the answer was staged.
			if still[issueKey(a.ri.Repo)] && current(a.ri.Repo, a.l) {
				k := issueKey(a.ri.Repo)
				// Rows this login can no longer see (gone, or no longer
				// visible to it) leave like any other drop: a purge.
				if old, ok := b.issues[k]; ok && old.State == model.RepoIssuesOK && a.ri.State != model.RepoIssuesOK {
					purged = true
				}
				b.issues[k] = a.ri
				changed = true
				sure[a.l] = true
			}
		}
		b.issuesDone = true
		switch {
		case purged:
			b.issuesPurgedLocked(b.now())
		case changed:
			b.issuesChangedLocked(b.now())
		}
		if gh := b.opts.Issues[model.ForgeGitHub]; gh != nil {
			switch {
			case slices.Contains(lost, gh):
				b.ghLogin = loginLost
			case sure[gh]:
				b.ghLogin = loginWorks
			}
			b.refreshGitHubLineLocked()
		}
		b.rebuildLocked()
	}()
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

// issueHostCurrentLocked says whether an issue answer from a lister that
// started at st may be applied for GitLab host: the board still runs, GitLab is
// on, nothing about host changed since st and host may still be asked.
// Unlike merge requests, issues need no session that asks for the host.
// Callers hold b.mu.
func (b *Board) issueHostCurrentLocked(ctx context.Context, host string, st hostStamp) bool {
	return ctx.Err() == nil && !b.closed && !b.gitlabOffLocked() && b.hostStampLocked(host) == st && b.glHosts[host].ok
}

// projectIssuesLocked is the count a project with repository r shows.
// Callers hold b.mu.
func (b *Board) projectIssuesLocked(r model.ForgeRepo) contract.IssueCount {
	ri, ok := b.issues[issueKey(r)]
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
	if !b.issuesListAt.IsZero() {
		sum.AsOf = b.issuesListAt.UTC()
	}
	for _, r := range repos {
		sum.Open += b.projectIssuesLocked(r).Open
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
		return contract.Issues{State: st, Repos: []contract.RepoIssues{}, Rev: b.issuesRev}, nil
	}
	repos := b.issueReposLocked()
	c := b.issuesPage
	if c == nil || c.rev != b.issuesRev || c.out.State != st || !slices.Equal(c.repos, repos) {
		out := contract.Issues{State: st, Repos: make([]contract.RepoIssues, 0, len(repos))}
		if !b.issuesListAt.IsZero() {
			out.AsOf = b.issuesListAt.UTC()
		}
		for _, r := range repos {
			out.Repos = append(out.Repos, pageRepoIssues(r, b.issues[issueKey(r)]))
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
	out := c.out // shallow: Repos stays shared
	out.Rev = c.rev
	out.FreshMs = max(0, (contract.IssuesFreshFor - since(lookAt, now)).Milliseconds())
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
func pageRepoIssues(r model.ForgeRepo, ri model.RepoIssues) contract.RepoIssues {
	out := contract.RepoIssues{
		Forge: contract.Forge(r.Forge), Host: clean(r.Host), Repo: pagePath(r.Path),
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

package core

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// GitLab is the GitLab provider as the core uses it (gitlab.Source).
type GitLab interface {
	source.PRProvider
	// Host says whether merge requests on host may be looked up: nil only
	// for a host glab is logged in to, gitlab.com included. An error wrapping
	// source.ErrNotLoggedIn is a definite no; any other error is no answer.
	// host may carry a web port ("host:8443").
	Host(ctx context.Context, host string) error
	// Subfolder is glab's subfolder setting for host ("gitlab" for GitLab at
	// example.com/gitlab), "" for none or when host is not known now. No call.
	Subfolder(host string) string
	// Failing is the line for the last failed call about host, "" when its
	// last call answered.
	Failing(host string) string
}

const (
	// hostEvery: a GitLab host is asked about again after this. The provider
	// keeps a yes ten minutes and a no one minute itself, so most asks run
	// no glab; a login made meanwhile is noticed within about a minute.
	hostEvery = time.Minute
	// hostSlots caps the GitLab host checks running at once: a host not
	// decided yet may cost a `glab auth status`. A check starts only when a
	// slot is free, so none waits for one with an answer nobody needs.
	hostSlots = 4
	// pushSkew widens a push call's window on each side for clock skew
	// between this machine and GitLab: a merge request that
	// `git push -o merge_request.create` announced counts as the session's
	// only when GitLab created it inside the window (spike 14, 4.1). Built
	// on the plan's recommendation, the owner's confirmation pending.
	pushSkew = 2 * time.Minute
)

// hostDecision is what the core knows about asking a GitLab host.
type hostDecision struct {
	at  time.Time // when it was last asked, answered or not
	ok  bool      // lookups may go there
	sub string    // with ok: glab's subfolder for the host at that yes ("" for none)
	why error     // the last definite no: source.ErrNotLoggedIn
	// unanswered: the last check got no answer (its failure is newer than
	// why, which still decides lookups).
	unanswered bool
}

// gitlabOffLocked: no GitLab provider, or its source line said off
// (--no-gitlab) or missing (no glab) at start. Callers hold b.mu.
func (b *Board) gitlabOffLocked() bool {
	if b.opts.GitLab == nil {
		return true
	}
	return b.glBase.Kind == contract.SourceGitLab && (b.glBase.State == contract.SourceOff || b.glBase.State == contract.SourceMissing)
}

// noteHostLocked records an answer about host: nil is a yes, a definite no
// keeps its reason, anything else is no answer and changes only when it is
// asked next. A definite no also drops what the board knows from that host
// (forgetHostLocked), and holds for every host with the same name and
// another web port too: glab keeps one login per host name.
// source.ErrNoLogins (glab is logged in to no host) does so for every host
// with a yes kept as well, each asked again when its own check is due.
// Callers hold b.mu.
func (b *Board) noteHostLocked(host string, err error) hostDecision {
	d := b.glHosts[host]
	was := d
	d.at, d.unanswered = b.now(), false
	switch {
	case err == nil:
		d.ok, d.why, d.sub = true, nil, b.opts.GitLab.Subfolder(host) // the check just read it
	case errors.Is(err, source.ErrNotLoggedIn):
		d.ok, d.why, d.sub = false, source.ErrNotLoggedIn, ""
	default:
		d.unanswered = true
	}
	if _, known := b.glHosts[host]; !known && len(b.glHosts) >= 256 {
		// Every host's decision goes: what any check or lookup under way
		// learned under one of them is dropped too (hostStamp).
		clear(b.glHosts)
		b.glEpoch++
		// The issues listed from GitLab rested on those decisions: they go
		// too, so a host allowed again later does not show them again.
		gl := func(r model.ForgeRepo) bool { return r.Forge == model.ForgeGitLab }
		listed := len(b.issues)
		maps.DeleteFunc(b.issues, func(r model.ForgeRepo, _ model.RepoIssues) bool { return gl(r) })
		maps.DeleteFunc(b.issuesTried, func(r model.ForgeRepo, _ time.Time) bool { return gl(r) })
		if len(b.issues) != listed {
			b.issuesPurgedLocked(b.now())
		}
	}
	b.glHosts[host] = d
	if d.why != nil {
		b.forgetHostLocked(host)
	}
	if err == nil {
		// One mount decision per name, for lookups and for display: when the
		// subfolder moved, every site on the name takes it, and what was
		// learned or shown under the old one goes (its merge requests, what
		// was asked or found missing, its issues), to be asked again.
		for h, o := range b.glHosts {
			if h == host || !o.ok || o.sub == d.sub || model.GitLabHostname(h) != model.GitLabHostname(host) {
				continue
			}
			o.sub = d.sub
			b.glHosts[h] = o
			b.forgetHostLocked(h)
		}
		if was.ok && was.sub != d.sub {
			b.forgetHostLocked(host)
		}
	}
	all := errors.Is(err, source.ErrNoLogins)
	if all {
		// Nothing learned anywhere before this answer holds any more.
		b.glEpoch++
	}
	if errors.Is(err, source.ErrNotLoggedIn) { // this answer is a definite no (ErrNoLogins is one too)
		for h, o := range b.glHosts {
			if h != host && o.ok && (all || model.GitLabHostname(h) == model.GitLabHostname(host)) {
				o.ok, o.why = false, source.ErrNotLoggedIn
				b.glHosts[h] = o
				b.forgetHostLocked(h)
			}
		}
	}
	return d
}

// forgetHostLocked drops every merge request the board keeps from GitLab
// host, with what it noted about asking there: their titles, issues and
// links leave cards and catch-up, the host leaves the page's list, and a
// login made later asks about them afresh. A check or lookup about host
// under way keeps nothing it learns (bumpHostLocked). Callers hold b.mu.
func (b *Board) forgetHostLocked(host string) {
	b.bumpHostLocked(host)
	on := func(r model.ForgeRepo) bool { return r.Forge == model.ForgeGitLab && r.Host == host }
	maps.DeleteFunc(b.prs, func(r model.PRRef, _ model.PullRequest) bool { return on(r.Repo) })
	maps.DeleteFunc(b.asked, func(r model.PRRef, _ bool) bool { return on(r.Repo) })
	maps.DeleteFunc(b.missingRefs, func(r model.PRRef, _ bool) bool { return on(r.Repo) })
	maps.DeleteFunc(b.lookedUp, func(r model.ForgeRepo, _ time.Time) bool { return on(r) })
	maps.DeleteFunc(b.pending, func(r model.ForgeRepo, _ bool) bool { return on(r) })
	maps.DeleteFunc(b.goneRepos, func(r model.ForgeRepo, _ bool) bool { return on(r) })
	// Issues listed from that host go too, and the page is told.
	listed := len(b.issues)
	maps.DeleteFunc(b.issues, func(r model.ForgeRepo, _ model.RepoIssues) bool { return on(r) })
	maps.DeleteFunc(b.issuesTried, func(r model.ForgeRepo, _ time.Time) bool { return on(r) })
	if len(b.issues) != listed {
		b.issuesPurgedLocked(b.now())
	}
}

// hostStamp is a GitLab host name's generation when a check or lookup about it
// started. Its answer is applied only while the stamp is unchanged: a
// denial or logout found meanwhile (forgetHostLocked) or glab logged in to
// no host each change it, so an older answer never brings back what a newer
// one dropped, nor undoes a newer no.
type hostStamp struct{ epoch, host uint64 }

// hostStampLocked is host's stamp now. Callers hold b.mu.
func (b *Board) hostStampLocked(host string) hostStamp {
	return hostStamp{epoch: b.glEpoch, host: b.glGen[model.GitLabHostname(host)]}
}

// bumpHostLocked changes host's stamp, and with it that of every site on the
// same name with another web port: glab keeps one login, and one subfolder,
// per name, so a denial or a moved mount found for one voids what any of
// them was learning. Callers hold b.mu.
func (b *Board) bumpHostLocked(host string) {
	host = model.GitLabHostname(host) // one generation for every web port of a name
	if _, known := b.glGen[host]; !known && len(b.glGen) >= 4096 {
		clear(b.glGen)
		b.glEpoch++ // a cleared count could meet an old stamp again
	}
	b.glGen[host]++
}

// hostCurrentLocked says whether an answer about host from a check or
// lookup that started at st may be applied: the board still runs (ctx, not
// closed), the GitLab source is on, nothing about host changed since st,
// and a session lookups ask for still needs the host. Callers hold b.mu.
func (b *Board) hostCurrentLocked(ctx context.Context, host string, st hostStamp) bool {
	return ctx.Err() == nil && !b.closed && !b.gitlabOffLocked() && b.hostStampLocked(host) == st && b.gitlabDemandLocked()[host]
}

// setGitLabBaseLocked records the GitLab source line as a probe or the
// start gave it. When it says off or missing, every host check under way is
// called off; nothing under way is applied (hostCurrentLocked). Callers
// hold b.mu.
func (b *Board) setGitLabBaseLocked(line contract.SourceStatus) {
	b.glBase = line
	b.glSrc++
	if b.gitlabOffLocked() {
		b.glEpoch++ // nothing under way counts after a switch back on either
		for _, stop := range b.glAsking {
			stop()
		}
	}
}

// noteGitLabDemandLocked notices, on every rebuild, the GitLab hosts that
// sessions lookups ask for no longer need: each one's stamp changes at
// once, so nothing under way about it counts even if a session needing it
// comes back before it answers, and its check under way is called off.
// With GitLab off it scans nothing (setGitLabBaseLocked called the checks
// off). Callers hold b.mu.
func (b *Board) noteGitLabDemandLocked() {
	if b.gitlabOffLocked() {
		return
	}
	demand := b.gitlabDemandLocked()
	for h := range b.glDemand {
		if !demand[h] {
			b.bumpHostLocked(h)
			if stop := b.glAsking[h]; stop != nil {
				stop()
			}
		}
	}
	b.glDemand = demand
}

// shownRemoteLocked is a resolved repo's remote as the page may show it: a
// GitLab one only on a host lookups may go to, else none, like a remote on no
// forge sitboard knows. A GitLab remote from an ssh address (RemoteBare) gets
// the host's subfolder in front of its path, as the web addresses carry it.
// Callers hold b.mu.
func (b *Board) shownRemoteLocked(repo model.Repo) model.ForgeRepo {
	r := repo.Remote
	if r.Forge != model.ForgeGitLab {
		return r
	}
	if b.gitlabOffLocked() || !b.glHosts[r.Host].ok {
		return model.ForgeRepo{}
	}
	if sub := b.glHosts[r.Host].sub; repo.RemoteBare && sub != "" { // the mount the allowlist holds
		r.Path = sub + "/" + r.Path
	}
	return r
}

// gitlabRefHostsLocked lists, sorted, the hosts of the GitLab merge request
// refs of the sessions lookups would ask for. Callers hold b.mu.
func (b *Board) gitlabRefHostsLocked() []string {
	var hosts []string
	for _, s := range b.sessions {
		if !b.lookupSessionLocked(s) {
			continue
		}
		for _, ref := range s.CreatedPRs {
			if ref.Repo.Forge == model.ForgeGitLab && ref.Repo.Valid() && !slices.Contains(hosts, ref.Repo.Host) {
				hosts = append(hosts, ref.Repo.Host)
			}
		}
	}
	slices.Sort(hosts)
	return hosts
}

// gitlabDemandLocked is the set of GitLab hosts lookups need now: those of
// the merge request refs of the sessions lookups ask for and of their
// folders' remotes (never a session outside the roots). Callers hold b.mu.
func (b *Board) gitlabDemandLocked() map[string]bool {
	b.glScans++
	hosts := map[string]bool{}
	add := func(r model.ForgeRepo) {
		if r.Forge == model.ForgeGitLab && r.Valid() {
			hosts[r.Host] = true
		}
	}
	for _, s := range b.sessions {
		if !b.lookupSessionLocked(s) {
			continue
		}
		for _, ref := range s.CreatedPRs {
			add(ref.Repo)
		}
		if repo, ok := b.repos[s.Folder]; ok {
			add(repo.Remote)
		}
	}
	return hosts
}

// startHostChecksLocked starts, in the background, the checks of the GitLab
// hosts that are due (gitlabDemandLocked, not asked about for hostEvery and
// not being asked now), as many as there are free slots. Hosts never asked
// come first, then the longest unasked, so with more due than slots every
// one gets its turn. The board's loop never waits for a check: its answer
// asks for a refresh, which starts the next. (A check whose host sessions
// no longer need is called off at once: noteGitLabDemandLocked.) With
// GitLab off it looks at no session. Callers hold b.mu.
func (b *Board) startHostChecksLocked(ctx context.Context) {
	if b.gitlabOffLocked() || b.closed || ctx.Err() != nil {
		return
	}
	demand := b.gitlabDemandLocked()
	free := hostSlots - len(b.glAsking)
	if free <= 0 {
		return
	}
	now := b.now()
	var due []string
	for h := range demand {
		if d, asked := b.glHosts[h]; b.glAsking[h] != nil || (asked && since(d.at, now) < hostEvery) {
			continue
		}
		due = append(due, h)
	}
	slices.SortFunc(due, func(x, y string) int {
		dx, ax := b.glHosts[x]
		dy, ay := b.glHosts[y]
		if ax != ay {
			if !ax {
				return -1
			}
			return 1
		}
		return cmp.Or(dx.at.Compare(dy.at), cmp.Compare(x, y))
	})
	for _, h := range due[:min(len(due), free)] {
		cctx, stop := context.WithCancel(ctx)
		b.glAsking[h] = stop
		b.hostChecks.Add(1)
		go b.checkHost(ctx, cctx, h, b.hostStampLocked(h))
	}
}

// checkHost asks the provider about host, under cctx (its own, called off
// with the host's need), and records the answer when it is still current
// (hostCurrentLocked against st, the host's stamp when it started). A host
// lookups may now go to has its refs never asked about marked for a lookup;
// the refresh an applied answer asks for runs them, with the lookups that
// waited for this check (refreshForges), and starts the next check due. An
// answer dropped asks for none: the next tick starts what is due. ctx is
// the board's, cctx derived from it. Runs with no lock held.
func (b *Board) checkHost(ctx, cctx context.Context, host string, st hostStamp) {
	defer b.hostChecks.Done()
	b.mu.Lock()
	ask := b.hostCurrentLocked(cctx, host, st)
	b.mu.Unlock()
	var err error
	if ask {
		err = b.opts.GitLab.Host(cctx, host)
	}
	applied := false
	func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		current := ask && b.hostCurrentLocked(cctx, host, st)
		if stop := b.glAsking[host]; stop != nil {
			stop()
			delete(b.glAsking, host)
		}
		if !current {
			return
		}
		applied = true
		if b.noteHostLocked(host, err).ok {
			b.markHostPendingLocked(host)
		}
		b.glDecided++
		b.refreshGitLabLineLocked()
		b.rebuildLocked()
	}()
	if applied {
		b.flushSeen()
		b.kickRefresh()
	}
}

// markHostPendingLocked flags for a lookup the repos on GitLab host with refs
// never asked about, of the sessions lookups ask for. Callers hold b.mu.
func (b *Board) markHostPendingLocked(host string) {
	for _, s := range b.sessions {
		if !b.lookupSessionLocked(s) {
			continue
		}
		for _, ref := range s.CreatedPRs {
			if ref.Repo.Forge == model.ForgeGitLab && ref.Repo.Host == host && ref.Repo.Valid() && !b.asked[ref] {
				b.pending[ref.Repo] = true
			}
		}
	}
}

// pushCredited says whether pr counts as created by a push that announced
// ref: GitLab's creation time lies inside one of those pushes' calls,
// widened by pushSkew on each side. No creation time, no credit; nor from a
// window with a missing end or one that ends before it starts.
func pushCredited(pr model.PullRequest, ref model.PRRef, pushes []model.PushedRef) bool {
	if pr.OpenedAt.IsZero() {
		return false
	}
	return slices.ContainsFunc(pushes, func(p model.PushedRef) bool {
		if p.Ref != ref || p.From.IsZero() || p.To.IsZero() || p.To.Before(p.From) {
			return false
		}
		return !pr.OpenedAt.Before(p.From.Add(-pushSkew)) && !pr.OpenedAt.After(p.To.Add(pushSkew))
	})
}

// creditPushedLocked takes out of s.CreatedPRs every merge request a push
// announced that GitLab answered for and did not create during one of those
// pushes: GitLab prints the same lines when the branch already had one open.
// Such a ref is remembered for the session, so a later fact from it does
// not bring it back and it is not asked about again, until later proof of
// creation wins: a fact that lists it as created and not as pushed (the
// session's own `glab mr create`), or a push window that matches GitLab's
// creation time. Reports whether s.CreatedPRs changed. Callers hold b.mu.
func (b *Board) creditPushedLocked(s *model.SessionFact) bool {
	pushed := func(r model.PRRef) bool {
		return slices.ContainsFunc(s.PushedPRs, func(p model.PushedRef) bool { return p.Ref == r })
	}
	for _, r := range s.CreatedPRs {
		if b.notOwn[s.Key][r] && !pushed(r) {
			delete(b.notOwn[s.Key], r)
		}
	}
	for _, p := range s.PushedPRs {
		pr, known := b.prs[p.Ref]
		switch {
		case !known:
		case pushCredited(pr, p.Ref, s.PushedPRs):
			delete(b.notOwn[s.Key], p.Ref)
		default:
			if b.notOwn[s.Key] == nil {
				b.notOwn[s.Key] = map[model.PRRef]bool{}
			}
			b.notOwn[s.Key][p.Ref] = true
		}
	}
	if len(b.notOwn[s.Key]) == 0 {
		delete(b.notOwn, s.Key)
	}
	own := b.notOwn[s.Key]
	if len(own) == 0 || !slices.ContainsFunc(s.CreatedPRs, func(r model.PRRef) bool { return own[r] }) {
		return false
	}
	s.CreatedPRs = slices.DeleteFunc(slices.Clone(s.CreatedPRs), func(r model.PRRef) bool { return own[r] })
	return true
}

// creditAllPushedLocked applies creditPushedLocked to every session after
// a lookup answered. Callers hold b.mu.
func (b *Board) creditAllPushedLocked() {
	for k, s := range b.sessions {
		if len(s.PushedPRs) > 0 && b.creditPushedLocked(&s) {
			b.sessions[k] = s
		}
	}
}

// refreshGitLabLineLocked keeps the GitLab source line honest while
// sitboard runs: when glab answered its probe, the line names the first host
// sessions need (sorted) that has something wrong. For a host of a session's
// merge requests that is a definite no (glab is not logged in to it); for any needed host, a session's
// remote included, its last failed call (a check without an answer, a rate
// limit, a lost login). A failure newer than the no wins. One host's answer
// never hides another's failure. With nothing wrong it is the probe's line
// again. It runs no glab. Reports whether the line changed. Callers hold
// b.mu.
func (b *Board) refreshGitLabLineLocked() bool {
	base := b.glBase
	if b.opts.GitLab == nil || base.Kind != contract.SourceGitLab || base.State != contract.SourceOK {
		return false
	}
	line := base
	refs := b.gitlabRefHostsLocked()
	for _, h := range slices.Sorted(maps.Keys(b.gitlabDemandLocked())) {
		d, why := b.glHosts[h], ""
		if slices.Contains(refs, h) { // a remote alone names no host that is not logged in
			if d.why == source.ErrNotLoggedIn {
				why = "not logged in to " + h
			}
		}
		note := why
		if fail := b.opts.GitLab.Failing(h); fail != "" && (d.unanswered || why == "") {
			note = fail // newer than the no: its check since got no answer
		}
		if note != "" {
			line.State, line.Detail = contract.SourceFailing, clean(note)
			break
		}
	}
	return b.setSourceLineLocked(line)
}

// setSourceLineLocked puts line in the place of the source line of its kind,
// or adds it in the page's order, and reports whether that changed anything.
// Callers hold b.mu and rebuild.
func (b *Board) setSourceLineLocked(line contract.SourceStatus) bool {
	line.Ready = b.isReadyLocked(line.Kind, line.State)
	i := slices.IndexFunc(b.sources, func(s contract.SourceStatus) bool { return s.Kind == line.Kind })
	if i >= 0 && b.sources[i] == line {
		return false
	}
	next := slices.Clone(b.sources) // the published view shares the old slice
	if i >= 0 {
		next[i] = line
	} else {
		next = append(next, line)
		slices.SortStableFunc(next, func(a, c contract.SourceStatus) int {
			return slices.Index(contract.SourceKinds, a.Kind) - slices.Index(contract.SourceKinds, c.Kind)
		})
	}
	b.sources = next
	return true
}

// StartGitLabProbe asks the GitLab source for its line in the background
// and shows it. sitboard starts it once it listens, so a slow glab delays
// nothing; the core runs it again while the line says failing, so it
// recovers. It runs with the host checks: when the board stops (ctx ends),
// Run waits a moment for it, and it shows nothing after that.
func (b *Board) StartGitLabProbe(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.startProbeLocked(ctx)
}

// startProbeLocked starts a probe unless one runs, or the board stopped.
// Callers hold b.mu.
func (b *Board) startProbeLocked(ctx context.Context) {
	if b.opts.GitLab == nil || b.closed || ctx.Err() != nil || b.glProbing {
		return
	}
	b.glProbing = true
	b.hostChecks.Add(1)
	go func(src uint64) {
		defer b.hostChecks.Done()
		b.probeGitLab(ctx, src)
		b.mu.Lock()
		b.glProbing = false
		b.mu.Unlock()
	}(b.glSrc)
}

// probeGitLab asks for the GitLab source line and shows it, unless the
// board stopped meanwhile or the line was set anew since the probe started
// (src). Runs with no lock held.
func (b *Board) probeGitLab(ctx context.Context, src uint64) {
	st := b.opts.GitLab.Probe(ctx)
	b.mu.Lock()
	if ctx.Err() != nil || b.closed || b.glSrc != src {
		b.mu.Unlock()
		return
	}
	b.glProbed = b.now()
	b.setGitLabBaseLocked(contract.SourceStatus{
		Kind: contract.SourceGitLab, State: contract.SourceState(st.State),
		Version: clean(st.Version), Detail: clean(st.Detail), Tested: st.Tested,
	})
	b.setSourceLineLocked(b.glBase)
	b.refreshGitLabLineLocked()
	b.rebuildLocked()
	b.mu.Unlock()
	b.flushSeen()
	b.kickRefresh()
}

// startReprobeLocked probes glab again in the background while its line
// says failing, once every hostEvery. Callers hold b.mu.
func (b *Board) startReprobeLocked(ctx context.Context) {
	if b.glBase.Kind != contract.SourceGitLab || b.glBase.State != contract.SourceFailing || since(b.glProbed, b.now()) < hostEvery {
		return
	}
	b.startProbeLocked(ctx)
}

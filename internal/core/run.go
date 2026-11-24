package core

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

const (
	tickEvery   = 10 * time.Second
	sleepGap    = tickEvery + 60*time.Second // a longer gap between ticks means the machine slept
	githubEvery = time.Minute                // per repo with live work
	endedEvery  = 10 * time.Minute           // per repo whose sessions have all ended (catch-up still wants its merges)
	// maxEndedRepos caps the repos asked about only for ended sessions, the
	// newest first. Their references come from the output of the sessions'
	// own gh pr create commands, so a transcript decides which repos sitboard
	// asks about; 20 batched calls every endedEvery is at most 120 an hour,
	// a small part of GitHub's hourly limit, and keeps a first round short.
	maxEndedRepos = 20
	lookupTimeout = 15 * time.Second // the adapter gives up after 10 s; this is a backstop
	// hostStopGrace bounds how long a stopping Run waits for the GitLab
	// host checks and probe it called off (their ctx ended) to return; they
	// apply nothing after the stop. Shorter than sitboard's own wait for Run.
	hostStopGrace = time.Second
)

// Run is the core's clock: every 10 s it re-evaluates the time-based rules,
// and once a minute it asks GitHub and GitLab about the pull and merge
// requests of repos with live work. A new ref is looked up at once. Run
// returns nil when ctx ends.
func (b *Board) Run(ctx context.Context) error {
	defer b.markClosed()
	tick := time.NewTicker(tickEvery)
	defer tick.Stop()
	b.tickCtx(ctx)
	for {
		select {
		case <-ctx.Done():
			b.markClosed()
			b.waitHostChecks(hostStopGrace)
			return nil
		case <-tick.C:
			b.tickCtx(ctx)
		case <-b.refreshCh:
			b.refreshForges(ctx, false)
		case <-b.issuesCh:
			b.mu.Lock()
			b.startIssuesLocked(ctx, b.now())
			b.mu.Unlock()
		}
	}
}

// tick is one beat of the clock, without a caller context, that also waits
// for the git lookups it started. Tests drive it with a fake clock.
// markClosed records that Run returned, so a gone check still running
// changes nothing and writes nothing, and no snapshot is published any more:
// a scheduled publish is cancelled.
func (b *Board) markClosed() {
	b.mu.Lock()
	b.closed = true
	if b.pubTimer != nil {
		b.pubTimer.Stop()
		b.pubTimer = nil
	}
	b.mu.Unlock()
}

// waitHostChecks waits for the host checks and probes in flight, at most d.
// Callers make sure none starts any more (markClosed).
func (b *Board) waitHostChecks(d time.Duration) {
	done := make(chan struct{})
	go func() {
		b.hostChecks.Wait()
		close(done)
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}

func (b *Board) tick() {
	n := b.decided()
	b.tickCtx(context.Background())
	b.settle(n)
}

// settle waits for the git lookups and GitLab host checks in flight; when a
// host check answered since decided was n, it runs the refresh that answer
// asks for, as Run would. Tests only.
func (b *Board) settle(n int) {
	for range 4 {
		b.resolving.Wait()
		b.hostChecks.Wait()
		m := b.decided()
		if m == n {
			return
		}
		n = m
		b.refreshForges(context.Background(), false)
	}
}

func (b *Board) decided() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.glDecided
}

func (b *Board) tickCtx(ctx context.Context) {
	now := b.now()
	b.mu.Lock()
	if !b.lastTick.IsZero() && wallGap(b.lastTick, now) > sleepGap {
		b.resumedAt = now
	}
	b.lastTick = now
	// Branches move: resolve the folders of live sessions again once a minute,
	// in the background; the answers count from the next GitHub lookup on.
	if b.resolved.IsZero() || since(b.resolved, now) >= githubEvery {
		for _, f := range b.liveFoldersLocked() {
			b.startResolveLocked(f)
		}
		b.resolved = now
	}
	b.startLeftoversLocked(ctx, now)
	b.startIssuesLocked(ctx, now)
	b.rebuildLocked()
	b.mu.Unlock()
	b.flushSeen()
	b.flushUmbrellas()
	b.refreshForges(ctx, true)
}

func (b *Board) kickRefresh() {
	select {
	case b.refreshCh <- struct{}{}:
	default:
	}
}

// markPendingLocked flags the repo of any pull request ref never asked about,
// of a session lookups ask for, and reports whether a lookup should start
// now: also when a GitLab ref's host was never asked about (the refresh
// starts its check). Callers hold b.mu.
func (b *Board) markPendingLocked(s model.SessionFact) bool {
	if !b.lookupSessionLocked(s) {
		return false
	}
	kick := false
	for _, ref := range s.CreatedPRs {
		if _, decided := b.glHosts[ref.Repo.Host]; ref.Repo.Forge == model.ForgeGitLab && ref.Repo.Valid() && !decided && !b.gitlabOffLocked() {
			kick = true
		}
		if b.providerFor(ref.Repo) == nil || b.asked[ref] || b.pending[ref.Repo] {
			continue
		}
		b.pending[ref.Repo] = true
		kick = true
	}
	return kick
}

// providerFor is the provider that answers for repo's forge, or nil when
// repo is not Valid, its forge is switched off or has no provider, or it is
// on a GitLab host lookups may not go to (not one glab is logged in to,
// gitlab.com included): its refs are kept
// but never looked up, and never shown. Callers hold b.mu.
func (b *Board) providerFor(repo model.ForgeRepo) source.PRProvider {
	if !repo.Valid() {
		return nil
	}
	switch repo.Forge {
	case model.ForgeGitHub:
		if b.opts.PRs != nil && !b.githubOffLocked() {
			return b.opts.PRs
		}
	case model.ForgeGitLab:
		if !b.gitlabOffLocked() && b.glHosts[repo.Host].ok {
			return b.opts.GitLab
		}
	}
	return nil
}

// lookupReq is what to ask a forge about one repo.
type lookupReq struct {
	p       source.PRProvider // providerFor(repo), taken under b.mu
	numbers []int
	live    bool      // a session that has not ended asks about this repo
	newest  time.Time // when the newest ended session asking about it ended
}

// refreshForges looks up pull and merge requests for repos with live work:
// those due (last asked a minute ago or more) when periodic is set, plus any
// repo with refs never looked up. The checks of GitLab hosts that are due
// start in the background; their answers ask for another refresh. Calls to
// gh and glab run outside the lock.
func (b *Board) refreshForges(ctx context.Context, periodic bool) {
	if b.opts.PRs == nil && b.opts.GitLab == nil {
		return
	}
	now := b.now()
	b.mu.Lock()
	b.startHostChecksLocked(ctx)
	b.startReprobeLocked(ctx)
	lineChanged := b.refreshGitLabLineLocked()
	if lineChanged {
		b.rebuildLocked()
	}
	reqs := b.lookupsLocked()
	for repo, req := range reqs {
		last, asked := b.lookedUp[repo]
		every := githubEvery
		if !req.live {
			every = endedEvery
		}
		due := periodic && (!asked || since(last, now) >= every)
		if !due && !b.pending[repo] {
			delete(reqs, repo)
		}
	}
	b.mu.Unlock()
	if lineChanged {
		b.flushSeen()
	}

	// Repos with live work first, then the ones only ended sessions ask
	// about, newest first: live cards never wait behind the backlog.
	repos := make([]model.ForgeRepo, 0, len(reqs))
	for r := range reqs {
		repos = append(repos, r)
	}
	slices.SortFunc(repos, func(x, y model.ForgeRepo) int {
		a, c := reqs[x], reqs[y]
		if a.live != c.live {
			if a.live {
				return -1
			}
			return 1
		}
		return cmp.Or(c.newest.Compare(a.newest), cmp.Compare(x.Key(), y.Key()))
	})
	for _, repo := range repos {
		if ctx.Err() != nil {
			return
		}
		req := reqs[repo]
		b.mu.Lock()
		allowed := b.providerFor(repo) != nil // a host check may have said no meanwhile
		st := b.hostStampLocked(repo.Host)
		b.mu.Unlock()
		if !allowed {
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		found, err := req.p.Lookup(lctx, repo, req.numbers, nil)
		cancel()
		b.mu.Lock()
		if ctx.Err() != nil || b.closed {
			b.mu.Unlock()
			return
		}
		if repo.Forge == model.ForgeGitLab && !b.hostCurrentLocked(ctx, repo.Host, st) {
			// The host was purged, switched off or left alone meanwhile:
			// what this older lookup learned brings none of it back.
			b.mu.Unlock()
			continue
		}
		if repo.Forge == model.ForgeGitLab && errors.Is(err, source.ErrNotChecked) {
			// The provider's yes for the host ran out, so it made no call:
			// the host's background check renews it (startHostChecksLocked),
			// and its answer asks for the refresh that runs this lookup.
			b.pending[repo] = true
			b.mu.Unlock()
			continue
		}
		b.lookedUp[repo] = now
		delete(b.pending, repo)
		for _, n := range req.numbers {
			b.asked[model.PRRef{Repo: repo, Number: n}] = true
		}
		var partial *source.PartialLookup
		switch {
		case err == nil:
			delete(b.goneRepos, repo)
			b.noteMissingLocked(repo, req.numbers, found)
			b.storePRsLocked(repo, found)
			b.creditAllPushedLocked()
		case errors.As(err, &partial):
			// Some numbers were settled, some not: what was placed is kept,
			// a number GitLab placed elsewhere loses its old copy (and is
			// asked again, not marked missing), one it did not place stays
			// as it was and is asked again; the rest do not exist.
			delete(b.goneRepos, repo)
			settled := slices.DeleteFunc(slices.Clone(req.numbers), func(n int) bool {
				return slices.Contains(partial.Wrong, n) || slices.Contains(partial.Unverified, n)
			})
			b.noteMissingLocked(repo, settled, found)
			b.storePRsLocked(repo, found)
			for _, n := range partial.Wrong {
				ref := model.PRRef{Repo: repo, Number: n}
				if slices.Contains(req.numbers, n) {
					delete(b.prs, ref)
					delete(b.missingRefs, ref)
				}
			}
			b.creditAllPushedLocked()
		case repo.Forge == model.ForgeGitLab && errors.Is(err, source.ErrNotLoggedIn):
			// The login went away since its host was decided: no more
			// lookups there until it is asked about again.
			b.noteHostLocked(repo.Host, err)
		case errors.Is(err, source.ErrRepoNotFound):
			// One such answer is enough: for its ended sessions the repo is
			// not asked again until sitboard restarts, even when the cause
			// passes (a token not yet authorised for the organisation).
			// Live sessions still ask.
			b.goneRepos[repo] = true
			// Its issue rows go too, as a purge: the login no longer sees it.
			if _, listed := b.issues[issueKey(repo)]; listed {
				delete(b.issues, issueKey(repo))
				b.issuesPurgedLocked(now)
			}
		}
		b.refreshGitLabLineLocked()
		b.rebuildLocked()
		b.mu.Unlock()
		b.flushSeen()
	}
}

// githubOffLocked: the GitHub source line says off (--no-github) or missing
// (no gh). Callers hold b.mu.
func (b *Board) githubOffLocked() bool {
	for _, s := range b.sources {
		if s.Kind == contract.SourceGitHub && (s.State == contract.SourceOff || s.State == contract.SourceMissing) {
			return true
		}
	}
	return false
}

// liveFoldersLocked lists the folders of sessions that have not ended.
func (b *Board) liveFoldersLocked() []string {
	set := map[string]bool{}
	for _, s := range b.sessions {
		if !s.Ended && s.Folder != "" && b.inRoots(s.Folder) {
			set[s.Folder] = true
		}
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	slices.Sort(out)
	return out
}

// lookupsLocked groups, per repo, the pull requests of sessions with live
// work: sessions that have not ended, and ended ones for up to
// catchupWindow after they ended, so a merge made after a worker ended still
// reaches the board and catch-up. For an ended session, a pull request GitHub
// already called merged or closed is not asked about again (that state is
// final), nor one GitHub did not find, nor any in a repo GitHub did not find;
// and a repo only ended sessions ask about is asked every endedEvery, not
// every minute. Repos with nothing to ask are absent.
func (b *Board) lookupsLocked() map[model.ForgeRepo]lookupReq {
	reqs := map[model.ForgeRepo]lookupReq{}
	for _, s := range b.sessions {
		if !b.lookupSessionLocked(s) {
			continue
		}
		for _, ref := range s.CreatedPRs {
			p := b.providerFor(ref.Repo)
			if p == nil || ref.Number <= 0 {
				continue
			}
			if s.Ended {
				if pr, ok := b.prs[ref]; (ok && pr.State != model.PRStateOpen) || b.missingRefs[ref] || b.goneRepos[ref.Repo] {
					continue
				}
			}
			r := reqs[ref.Repo]
			r.p = p
			if !slices.Contains(r.numbers, ref.Number) {
				r.numbers = append(r.numbers, ref.Number)
			}
			r.live = r.live || !s.Ended
			if s.Ended && s.EndedAt.After(r.newest) {
				r.newest = s.EndedAt
			}
			reqs[ref.Repo] = r
		}
	}
	var endedOnly []model.ForgeRepo
	for repo, r := range reqs {
		slices.Sort(r.numbers)
		reqs[repo] = r
		if !r.live {
			endedOnly = append(endedOnly, repo)
		}
	}
	if len(endedOnly) > maxEndedRepos {
		slices.SortFunc(endedOnly, func(x, y model.ForgeRepo) int {
			return cmp.Or(reqs[y].newest.Compare(reqs[x].newest), cmp.Compare(x.Key(), y.Key()))
		})
		for _, repo := range endedOnly[maxEndedRepos:] {
			delete(reqs, repo)
		}
	}
	return reqs
}

// lookupSessionLocked says whether s is one whose refs lookups ask about:
// in the roots, and not ended longer ago than the catch-up still wants it.
// Callers hold b.mu.
func (b *Board) lookupSessionLocked(s model.SessionFact) bool {
	if !b.inRoots(s.Folder) {
		return false
	}
	if !s.Ended {
		return true
	}
	endedAt := s.EndedAt
	if endedAt.IsZero() {
		endedAt = s.LastActivity
	}
	return since(endedAt, b.now()) <= max(endedOpenLimit, catchupWindow)
}

// noteMissingLocked records which numbers a successful lookup did not find,
// so ended sessions stop asking about them. Callers hold b.mu.
func (b *Board) noteMissingLocked(repo model.ForgeRepo, numbers []int, found []model.PullRequest) {
	for _, n := range numbers {
		ref := model.PRRef{Repo: repo, Number: n}
		if slices.ContainsFunc(found, func(pr model.PullRequest) bool { return pr.Number == n && (pr.Repo == repo || pr.Repo.IsZero()) }) {
			delete(b.missingRefs, ref)
		} else {
			b.missingRefs[ref] = true
			if repo.Forge == model.ForgeGitLab {
				// A GitLab lookup that answered without the merge request (it
				// is gone, or its address is no longer the one shown) leaves
				// no old copy behind to keep its link and its approval.
				delete(b.prs, ref)
			}
		}
	}
}

// storePRsLocked records a lookup's answer, one pull request at a time.
func (b *Board) storePRsLocked(repo model.ForgeRepo, found []model.PullRequest) {
	for _, pr := range found {
		if pr.Repo.IsZero() {
			pr.Repo = repo
		}
		if pr.Repo != repo || pr.Number <= 0 {
			continue
		}
		b.prs[model.PRRef{Repo: pr.Repo, Number: pr.Number}] = pr
	}
}

// wallGap is b-a on the wall clock, ignoring monotonic readings, which on
// macOS do not advance while the machine sleeps.
func wallGap(a, b time.Time) time.Duration { return b.Round(0).Sub(a.Round(0)) }

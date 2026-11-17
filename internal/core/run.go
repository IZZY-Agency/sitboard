package core

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

const (
	tickEvery     = 10 * time.Second
	sleepGap      = tickEvery + 60*time.Second // a longer gap between ticks means the machine slept
	githubEvery   = time.Minute                // per repo with live work
	lookupTimeout = 15 * time.Second           // the adapter gives up after 10 s; this is a backstop
)

// Run is the core's clock: every 10 s it re-evaluates the time-based rules,
// and once a minute it asks GitHub about the pull requests of repos with live
// work. A new pull request ref is looked up at once. Run returns nil when ctx
// ends.
func (b *Board) Run(ctx context.Context) error {
	defer b.markClosed()
	tick := time.NewTicker(tickEvery)
	defer tick.Stop()
	b.tickCtx(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			b.tickCtx(ctx)
		case <-b.refreshCh:
			b.refreshGitHub(ctx, false)
		}
	}
}

// tick is one beat of the clock, without a caller context, that also waits
// for the git lookups it started. Tests drive it with a fake clock.
// markClosed records that Run returned, so a gone check still running
// changes nothing and writes nothing.
func (b *Board) markClosed() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
}

func (b *Board) tick() {
	b.tickCtx(context.Background())
	b.resolving.Wait()
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
	b.rebuildLocked()
	b.mu.Unlock()
	b.flushSeen()
	b.flushUmbrellas()
	b.refreshGitHub(ctx, true)
}

func (b *Board) kickRefresh() {
	select {
	case b.refreshCh <- struct{}{}:
	default:
	}
}

// markPendingLocked flags the repo of any pull request ref or Codex branch
// never asked about, and reports whether a lookup should start now. Callers
// hold b.mu.
func (b *Board) markPendingLocked(s model.SessionFact) bool {
	kick := false
	flag := func(repo, ask string) {
		if !validRepo(repo) || b.asked[ask] || b.pending[repo] {
			return
		}
		b.pending[repo] = true
		kick = true
	}
	for _, ref := range s.CreatedPRs {
		flag(ref.Repo, askKey(ref.Repo, strconv.Itoa(ref.Number)))
	}
	if s.Key.Tool == model.ToolCodex {
		if r := b.repoLocked(s.Folder); r.Slug != "" && r.Branch.OK {
			flag(r.Slug, askKey(r.Slug, "branch:"+r.Branch.V))
		}
	}
	return kick
}

func askKey(repo, what string) string { return repo + "\x00" + what }

// lookupReq is what to ask GitHub about one repo.
type lookupReq struct {
	numbers  []int
	branches []string
}

// refreshGitHub looks up pull requests for repos with live work: those due
// (last asked a minute ago or more) when periodic is set, plus any repo with
// refs never looked up. Calls to gh run outside the lock.
func (b *Board) refreshGitHub(ctx context.Context, periodic bool) {
	if b.opts.PRs == nil {
		return
	}
	now := b.now()
	b.mu.Lock()
	if b.githubOffLocked() {
		b.mu.Unlock()
		return
	}
	reqs := b.lookupsLocked()
	for repo := range reqs {
		last, asked := b.lookedUp[repo]
		due := periodic && (!asked || since(last, now) >= githubEvery)
		if !due && !b.pending[repo] {
			delete(reqs, repo)
		}
	}
	b.mu.Unlock()

	repos := make([]string, 0, len(reqs))
	for r := range reqs {
		repos = append(repos, r)
	}
	slices.Sort(repos)
	for _, repo := range repos {
		req := reqs[repo]
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		found, err := b.opts.PRs.Lookup(lctx, repo, req.numbers, req.branches)
		cancel()
		b.mu.Lock()
		b.lookedUp[repo] = now
		delete(b.pending, repo)
		for _, n := range req.numbers {
			b.asked[askKey(repo, strconv.Itoa(n))] = true
		}
		for _, br := range req.branches {
			b.asked[askKey(repo, "branch:"+br)] = true
		}
		if err == nil {
			b.storePRsLocked(repo, req, found)
		}
		b.rebuildLocked()
		b.mu.Unlock()
		b.flushSeen()
		if ctx.Err() != nil {
			return
		}
	}
}

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
// work: sessions that have not ended, and ended ones that may still hold a
// card because of their pull requests. Repos with nothing to ask are absent.
func (b *Board) lookupsLocked() map[string]lookupReq {
	now := b.now()
	reqs := map[string]lookupReq{}
	for _, s := range b.sessions {
		if !b.inRoots(s.Folder) {
			continue
		}
		if s.Ended {
			if !keepEnded {
				continue
			}
			endedAt := s.EndedAt
			if endedAt.IsZero() {
				endedAt = s.LastActivity
			}
			if since(endedAt, now) > endedOpenLimit {
				continue
			}
		}
		for _, ref := range s.CreatedPRs {
			if !validRepo(ref.Repo) || ref.Number <= 0 {
				continue
			}
			r := reqs[ref.Repo]
			if !slices.Contains(r.numbers, ref.Number) {
				r.numbers = append(r.numbers, ref.Number)
			}
			reqs[ref.Repo] = r
		}
		if s.Key.Tool == model.ToolCodex && !s.Ended {
			if repo := b.repoLocked(s.Folder); validRepo(repo.Slug) && repo.Branch.OK {
				r := reqs[repo.Slug]
				if !slices.Contains(r.branches, repo.Branch.V) {
					r.branches = append(r.branches, repo.Branch.V)
				}
				reqs[repo.Slug] = r
			}
		}
	}
	for repo, r := range reqs {
		slices.Sort(r.numbers)
		slices.Sort(r.branches)
		reqs[repo] = r
	}
	return reqs
}

// storePRsLocked records a lookup's answer. Branch matches are replaced for
// the branches asked about; numbered pull requests are updated one by one.
func (b *Board) storePRsLocked(repo string, req lookupReq, found []model.PullRequest) {
	branches := b.branchPRs[repo]
	if branches == nil {
		branches = map[string][]model.PullRequest{}
		b.branchPRs[repo] = branches
	}
	for _, br := range req.branches {
		branches[br] = []model.PullRequest{}
	}
	for _, pr := range found {
		if pr.Repo == "" {
			pr.Repo = repo
		}
		if pr.Repo != repo || pr.Number <= 0 {
			continue
		}
		b.prs[model.PRRef{Repo: pr.Repo, Number: pr.Number}] = pr
		if slices.Contains(req.branches, pr.HeadBranch) {
			branches[pr.HeadBranch] = append(branches[pr.HeadBranch], pr)
		}
	}
}

// wallGap is b-a on the wall clock, ignoring monotonic readings, which on
// macOS do not advance while the machine sleeps.
func wallGap(a, b time.Time) time.Duration { return b.Round(0).Sub(a.Round(0)) }

// repoRe is what a GitHub "owner/name" must look like before it reaches gh.
var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// validRepo refuses anything but a plain "owner/name"; repo names come from
// transcripts and git remotes and are untrusted.
func validRepo(repo string) bool {
	if !repoRe.MatchString(repo) {
		return false
	}
	owner, name, _ := strings.Cut(repo, "/")
	return owner != "." && owner != ".." && name != "." && name != ".."
}

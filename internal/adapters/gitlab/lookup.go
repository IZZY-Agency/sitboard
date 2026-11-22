package gitlab

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Result is what one Lookup found.
type Result struct {
	MRs []MergeRequest
	// BranchesComplete is false when the newest merge requests over the
	// branches asked for filled a whole page: older ones, or local ones behind
	// forks, may be missing, so a branch with none here is not known to have
	// none.
	BranchesComplete bool
}

// Lookup returns the merge requests of the project path on host with the
// given numbers, plus the newest ones whose source branch is one of
// branches, with the issues each one closes. Every number is asked about: a
// number asked for and not in the result does not exist in the project.
// Up to 50 numbers and 50 branches go in one call; more make more calls, and
// the lookup fails unless all of them answer. It asks only a host whose
// check (Known, Source.Host) said yes within knownFor, and never checks a
// login itself: with no such yes it makes no call and answers
// errNotChecked, so a caller's loop never waits for `glab auth status`. It
// never answers from an old cache when GitLab cannot be asked.
func (p *Provider) Lookup(ctx context.Context, host, path string, iids []int, branches []string) (Result, error) {
	if p.off {
		return Result{}, errNotAsked
	}
	if !ValidHost(host) || !ValidPath(path) {
		return Result{}, errInvalidArg
	}
	nums, brs := cleanIIDs(iids), cleanBranches(branches)
	if len(nums) == 0 && len(brs) == 0 {
		return Result{BranchesComplete: true}, nil
	}
	key := cacheKey(host, path, nums, brs)
	if p.glab() == "" {
		p.setHealth(source.StateMissing, "glab not found")
		return Result{}, errNoGlab
	}

	proj := host + "\x00" + path + "\x00"
	p.mu.Lock()
	now := p.now()
	h, current := p.answerLocked(host, knownFor)
	st := p.stampLocked(host)
	c, hit := p.cache[key]
	until := p.backoff[host]
	gen := p.forgotten[proj]
	p.mu.Unlock()
	switch {
	case !current:
		return Result{}, errNotChecked
	case h.nowhere:
		return Result{}, errNowhere
	case !h.known:
		return Result{}, errNotKnown
	case h.subfolder:
		return Result{}, errSubfolder
	}
	if hit && now.Sub(c.at) < cacheFor {
		return cloneResult(c.res), nil
	}
	if now.Before(until) {
		return Result{}, errRateLimited
	}

	res := Result{BranchesComplete: true}
	var all []found
	seen := map[int]bool{}
	for k := 0; k*maxIIDs < len(nums) || k*maxBranches < len(brs); k++ {
		pa, err := p.query(ctx, host, path, chunk(nums, k, maxIIDs), chunk(brs, k, maxBranches))
		switch {
		case errors.Is(err, errNoProject):
			p.forget(host, path)
			return Result{}, fmt.Errorf("gitlab: %w", source.ErrRepoNotFound)
		case errors.Is(err, errIncomplete):
			p.setHostHealth(host, "GitLab answered only part of a lookup")
			return Result{}, err
		case errors.Is(err, errRateLimited):
			p.setHostHealth(host, noteRateLimited)
			return Result{}, err
		case errors.Is(err, errUnexpected):
			p.setHostHealth(host, "unexpected answer from glab")
			return Result{}, err
		case err != nil:
			return p.failed(err, host)
		}
		for _, f := range pa.mrs {
			if !seen[f.mr.IID] {
				seen[f.mr.IID] = true
				all = append(all, f)
			}
		}
		if pa.branchFull {
			res.BranchesComplete = false
		}
	}
	slices.SortFunc(all, func(x, y found) int { return x.mr.IID - y.mr.IID })
	mrs, limited, err := p.withClosing(ctx, host, all, st)
	if err != nil {
		return p.failed(err, host)
	}
	res.MRs = mrs
	if limited {
		p.setHostHealth(host, noteRateLimited)
	} else {
		p.setHostHealth(host, "")
		p.setHealth(source.StateOK, "")
	}
	p.mu.Lock()
	for k, v := range p.cache {
		if p.now().Sub(v.at) > 10*time.Minute {
			delete(p.cache, k)
		}
	}
	// An overlapping lookup that found the project gone, or the login gone,
	// since this one started wins: this older answer is returned, not cached.
	if p.forgotten[proj] == gen && p.stampLocked(host) == st {
		p.cache[key] = cached{at: p.now(), res: cloneResult(res)}
	}
	p.mu.Unlock()
	return res, nil
}

// chunk is the k-th slice of at most n items of in, or nil.
func chunk[T any](in []T, k, n int) []T {
	if k*n >= len(in) {
		return nil
	}
	return in[k*n : min((k+1)*n, len(in))]
}

// forget drops every cached answer about the project: it is gone, or the
// account can no longer see it.
func (p *Provider) forget(host, path string) {
	prefix := host + "\x00" + path + "\x00"
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.forgotten) > 4096 {
		clear(p.forgotten) // a lookup in flight across this may cache an old answer once
	}
	p.forgotten[prefix]++
	for k := range p.cache {
		if strings.HasPrefix(k, prefix) {
			delete(p.cache, k)
		}
	}
}

var errUnexpected = errors.New("gitlab: unexpected answer from glab")

// query runs one GraphQL call and reads its answer. An instance that
// refuses the approval fields is asked again without them. An answer with
// data and errors is read by parseAnswer, which keeps it only when the
// errors touch nothing but a pipeline or an approval.
func (p *Provider) query(ctx context.Context, host, path string, nums []int, brs []string) (parsed, error) {
	q := shape{iids: len(nums) > 0, branches: len(brs) > 0, approvals: p.approvalsOn(host)}
	out, err := p.graphql(ctx, host, path, nums, brs, q.approvals)
	if q.approvals && lacksApproval(err) {
		p.mu.Lock()
		p.noApproval[host] = p.now()
		p.mu.Unlock()
		q.approvals = false
		out, err = p.graphql(ctx, host, path, nums, brs, q.approvals)
	}
	if err != nil {
		var ee *runner.ExitError
		if errors.As(err, &ee) {
			pa, perr := parseAnswer(ee.Stdout, host, path, q)
			switch {
			case perr == nil:
				return pa, nil
			case errors.Is(perr, errIncomplete):
				return parsed{}, perr
			}
		}
		return parsed{}, err
	}
	pa, err := parseAnswer(out, host, path, q)
	if err != nil && !errors.Is(err, errNoProject) && !errors.Is(err, errIncomplete) {
		return parsed{}, errUnexpected
	}
	return pa, err
}

// graphql runs the lookup query, unless the host is in its shared backoff:
// checked before every call, since another lookup can meet a 429 meanwhile.
func (p *Provider) graphql(ctx context.Context, host, path string, nums []int, brs []string, approvals bool) ([]byte, error) {
	if p.backingOff(host) {
		return nil, errRateLimited
	}
	args := []string{"api", "graphql", "--hostname", host, "-f", "query=" + buildQuery(nums, len(brs), approvals), "-f", "path=" + path}
	for i, b := range brs {
		args = append(args, "-f", "b"+strconv.Itoa(i)+"="+b)
	}
	return p.exec(ctx, args...)
}

// backingOff says whether host is left alone after a 429.
func (p *Provider) backingOff(host string) bool {
	p.mu.Lock()
	until := p.backoff[host]
	p.mu.Unlock()
	return p.now().Before(until)
}

// approvalsOn says whether host is asked for the approval fields: not when
// it refused them within approvalsRecheck.
func (p *Provider) approvalsOn(host string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	at, ok := p.noApproval[host]
	return !ok || p.now().Sub(at) >= approvalsRecheck
}

func lacksApproval(err error) bool {
	var ee *runner.ExitError
	return errors.As(err, &ee) && lacksApprovalFields(ee.Stdout)
}

// failed records a failed call and says what the caller gets: never an old
// answer.
func (p *Provider) failed(err error, host string) (Result, error) {
	f := readFailure(err, host)
	note := f.note(host)
	p.setHostHealth(host, note)
	switch {
	case f.nowhere:
		// glab's own answer that it has no login at all: every host's yes
		// goes, as when a host check hears it.
		p.loggedInNowhere()
		return Result{}, fmt.Errorf("%w: %s", errNowhere, note)
	case f.loggedOut():
		// The instance refused the login (a 401 glab named) or glab has
		// none for the host: the yes kept for the host goes, so no other
		// project there is asked meanwhile, and what was cached from there.
		p.loggedOut(host)
		return Result{}, fmt.Errorf("%w: %s", errNotKnown, note)
	case f.rateLimited():
		p.limited(host, err)
		return Result{}, errRateLimited
	}
	return Result{}, fmt.Errorf("gitlab: %s: %w", note, unwrapCtx(err))
}

var (
	errClosingBudget  = errors.New("gitlab: no calls left in this lookup")
	errClosingTooLong = errors.New("gitlab: too many closing issues")
)

// withClosing fills in the issues each merge request closes. GraphQL has no
// such field, so each one costs REST calls (one per page of 100): an answer
// is kept until the merge request's state changes, and for an open one at
// most ten minutes. At most maxClosingCalls calls per lookup. When a merge
// request's issues are not known now (not asked yet, budget spent, the call
// failed, more than closingMaxPages pages) ClosingKnown is false: an old
// list is never passed off as the current one. A call that failed waits
// closingRetry before it is tried again, so it cannot use up the budget of
// every lookup; so does a call that ran out of its own time (the caller
// giving up stops the pass). Before each call the host's shared backoff is
// checked: after a 429 (in this lookup or another) no new call starts.
// limited says a 429 was met. An answer is kept only while st holds: not
// when the login was found gone since the lookup started. A call that
// finds the login gone (callFailure.loggedOut) ends the lookup: err, and
// no merge request is returned.
func (p *Provider) withClosing(ctx context.Context, host string, found []found, st authStamp) (mrs []MergeRequest, limited bool, err error) {
	mrs = make([]MergeRequest, 0, len(found))
	calls := 0
	stop := false
	for _, f := range found {
		mr := f.mr
		k := host + "\x00" + strconv.Itoa(f.projectID) + "\x00" + strconv.Itoa(mr.IID)
		p.mu.Lock()
		now := p.now()
		cl, ok := p.closes[k]
		failedAt, failed := p.closingFailed[k]
		p.mu.Unlock()
		fresh := ok && cl.state == string(mr.State) &&
			(mr.State != model.PRStateOpen || now.Sub(cl.at) < closingOpenFor)
		switch {
		case fresh:
			mr.ClosingIssues, mr.ClosingKnown = slices.Clone(cl.issues), true
		case stop || ctx.Err() != nil || (failed && now.Sub(failedAt) < closingRetry):
		default:
			issues, err := p.closing(ctx, host, f.projectID, mr.IID, &calls)
			switch {
			case err == nil:
				p.mu.Lock()
				if len(p.closes) > 4096 {
					clear(p.closes)
				}
				if p.stampLocked(host) == st {
					p.closes[k] = closing{at: p.now(), state: string(mr.State), issues: issues}
				}
				delete(p.closingFailed, k)
				p.mu.Unlock()
				mr.ClosingIssues, mr.ClosingKnown = slices.Clone(issues), true
			case errors.Is(err, errClosingBudget):
				stop = true
			case errors.Is(err, errRateLimited):
				stop, limited = true, true
			case ctx.Err() != nil:
				stop = true
			case readFailure(err, host).loggedOut():
				return nil, false, err
			case readFailure(err, host).rateLimited():
				p.limited(host, err)
				stop, limited = true, true
			default:
				p.mu.Lock()
				if len(p.closingFailed) > 4096 {
					clear(p.closingFailed)
				}
				p.closingFailed[k] = p.now()
				p.mu.Unlock()
			}
		}
		mrs = append(mrs, mr)
	}
	return mrs, limited, nil
}

// closing asks every page of one merge request's closing issues, each page
// one call of the lookup's budget.
func (p *Provider) closing(ctx context.Context, host string, projectID, iid int, calls *int) ([]int, error) {
	issues := []int{}
	for page := 1; page <= closingMaxPages; page++ {
		if *calls >= maxClosingCalls {
			return nil, errClosingBudget
		}
		if p.backingOff(host) {
			return nil, errRateLimited
		}
		*calls++
		out, err := p.exec(ctx, "api", "--hostname", host, closesIssuesPath(projectID, iid, page))
		if err != nil {
			return nil, err
		}
		nums, n, err := parseClosing(out, projectID)
		if err != nil {
			return nil, err
		}
		for _, x := range nums {
			if !slices.Contains(issues, x) {
				issues = append(issues, x)
			}
		}
		if n < closingPerPage {
			return issues, nil
		}
	}
	return nil, errClosingTooLong
}

func cacheKey(host, path string, nums []int, brs []string) string {
	var b strings.Builder
	b.WriteString(host + "\x00" + path)
	for _, n := range nums {
		b.WriteString("\x00!" + strconv.Itoa(n))
	}
	for _, br := range brs {
		b.WriteString("\x00@" + br)
	}
	return b.String()
}

func cloneResult(in Result) Result {
	out := in
	out.MRs = slices.Clone(in.MRs)
	for i := range out.MRs {
		out.MRs[i].ClosingIssues = slices.Clone(out.MRs[i].ClosingIssues)
	}
	return out
}

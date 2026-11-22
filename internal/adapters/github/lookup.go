// Package github looks up pull requests through the user's own gh. It never
// reads, stores or passes a token, and never logs gh's output.
//
// One Lookup is one read-only GraphQL call per repo, always to
// model.GitHubHost: gh's GH_HOST, which the user may have set for another
// host, never sends it elsewhere. Every untrusted value
// (owner, name, branch) travels as a raw string variable (-f), never inside the
// query text and never through gh's typed -F fields, which read files on "@".
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Options configure the provider.
type Options struct {
	Bin string // gh binary; "" means look it up on PATH
	Off bool   // switched off by --no-github: never runs gh
}

const (
	callTimeout      = 10 * time.Second
	cacheFor         = 60 * time.Second
	probeFor         = 60 * time.Second
	rateLimitBackoff = 5 * time.Minute
	maxNumbers       = 100
	maxBranches      = 100
	perBranch        = 5 // newest pull requests looked at per head branch
)

// testedVersions are the gh versions this adapter was run against.
var testedVersions = []string{"2.104.0"}

// env keeps gh quiet and local: no prompts, no update or extension checks, no
// telemetry, no colour, no debug output.
var env = []string{
	"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_NO_EXTENSION_UPDATE_NOTIFIER=1",
	"GH_TELEMETRY=false", "DO_NOT_TRACK=1", "NO_COLOR=1", "GH_DEBUG=", "GH_PAGER=", "GH_SPINNER_DISABLED=1",
}

type cached struct {
	at  time.Time
	prs []model.PullRequest
}

type provider struct {
	bin     string
	off     bool
	timeout time.Duration
	run     runner.Func // nil: real processes
	now     func() time.Time
	found   func(string) string

	mu         sync.Mutex
	cache      map[string]cached    // by lookup key
	backoff    map[string]time.Time // repo → no gh calls before this time
	inflight   map[string]*flight   // by lookup key: a gh call under way
	health     source.State         // from the last lookup; "" until one ran
	healthNote string
	probed     time.Time
	probe      source.Status
}

// New returns the GitHub pull request provider.
func New(opts Options) source.PRProvider {
	bin := opts.Bin
	if bin == "" {
		bin = "gh"
	}
	return &provider{
		bin: bin, off: opts.Off, timeout: callTimeout, now: time.Now, found: runner.Found,
		cache: map[string]cached{}, backoff: map[string]time.Time{}, inflight: map[string]*flight{},
	}
}

func (p *provider) exec(ctx context.Context, args ...string) ([]byte, error) {
	run := p.run
	if run == nil {
		run = runner.Exec(p.timeout, env...)
	}
	return run(ctx, p.bin, args...)
}

// Probe reports whether gh is installed and logged in. The answer is kept for
// a minute; a failing lookup since then overrides it.
func (p *provider) Probe(ctx context.Context) source.Status {
	if p.off {
		return source.Status{Kind: source.KindGitHub, State: source.StateOff, Detail: "switched off"}
	}
	p.mu.Lock()
	st, fresh := p.probe, !p.probed.IsZero() && p.now().Sub(p.probed) < probeFor
	health, note := p.health, p.healthNote
	p.mu.Unlock()

	if !fresh {
		st = p.probeNow(ctx)
		p.mu.Lock()
		p.probe, p.probed = st, p.now()
		p.mu.Unlock()
	}
	if st.State == source.StateOK && health == source.StateFailing {
		st.State, st.Detail = source.StateFailing, note
	}
	return st
}

func (p *provider) probeNow(ctx context.Context) source.Status {
	st := source.Status{Kind: source.KindGitHub}
	if p.found(p.bin) == "" {
		st.State, st.Detail = source.StateMissing, "gh not found"
		return st
	}
	out, err := p.exec(ctx, "--version")
	if err != nil {
		st.State, st.Detail = source.StateFailing, "gh does not answer"
		return st
	}
	// "gh version 2.104.0 (2026-11-12)"
	if f := strings.Fields(string(out)); len(f) >= 3 && f[0] == "gh" && f[1] == "version" {
		st.Version = f[2]
		st.Tested = slices.Contains(testedVersions, st.Version)
	}
	// Only the exit code is read. The output is discarded: it can name the account.
	if _, err := p.exec(ctx, "auth", "status", "--hostname", model.GitHubHost); err != nil {
		st.State, st.Detail = source.StateFailing, classify(err)
		return st
	}
	st.State = source.StateOK
	return st
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// githubSlug is fr's "owner/name" when fr is on GitHub.
func githubSlug(fr model.ForgeRepo) (string, bool) {
	if fr.Forge != model.ForgeGitHub || fr.Host != model.GitHubHost {
		return "", false
	}
	return fr.Path, true
}

func validRepo(repo string) (owner, name string, ok bool) {
	if !repoRe.MatchString(repo) {
		return "", "", false
	}
	owner, name, _ = strings.Cut(repo, "/")
	if strings.HasPrefix(owner, "-") || strings.Trim(owner, ".") == "" || strings.Trim(name, ".") == "" ||
		strings.Contains(repo, "..") {
		return "", "", false
	}
	return owner, name, true
}

// Lookup returns the pull requests of repo with the given numbers, plus the
// newest ones whose head branch is one of branches, in one gh call.
func (p *provider) Lookup(ctx context.Context, fr model.ForgeRepo, numbers []int, branches []string) ([]model.PullRequest, error) {
	if p.off {
		return nil, nil
	}
	repo, ok := githubSlug(fr)
	if !ok {
		return nil, fmt.Errorf("github: not a GitHub repo")
	}
	owner, name, ok := validRepo(repo)
	if !ok {
		return nil, fmt.Errorf("github: not a repo slug")
	}
	nums, brs := cleanNumbers(numbers), cleanBranches(branches)
	if len(nums) == 0 && len(brs) == 0 {
		return nil, nil
	}
	key := cacheKey(repo, nums, brs)

	p.mu.Lock()
	now := p.now()
	c, hit := p.cache[key]
	until := p.backoff[repo]
	p.mu.Unlock()
	if hit && now.Sub(c.at) < cacheFor {
		return slices.Clone(c.prs), nil
	}
	if now.Before(until) {
		if hit {
			return slices.Clone(c.prs), nil
		}
		return nil, errRateLimited
	}
	if p.found(p.bin) == "" {
		p.setHealth(source.StateMissing, "gh not found")
		return nil, errors.New("github: gh not found")
	}

	// Lookups with the same key share one gh call.
	p.mu.Lock()
	if fl, busy := p.inflight[key]; busy {
		p.mu.Unlock()
		select {
		case <-fl.done:
			return slices.Clone(fl.prs), fl.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	fl := &flight{done: make(chan struct{})}
	p.inflight[key] = fl
	p.mu.Unlock()
	fl.prs, fl.err = p.fetch(ctx, key, repo, owner, name, nums, brs, c, hit)
	p.mu.Lock()
	delete(p.inflight, key)
	p.mu.Unlock()
	close(fl.done)
	return slices.Clone(fl.prs), fl.err
}

type flight struct {
	done chan struct{}
	prs  []model.PullRequest
	err  error
}

// fetch runs the one gh call for a lookup and updates the cache and health.
func (p *provider) fetch(ctx context.Context, key, repo, owner, name string, nums []int, brs []string, c cached, hit bool) ([]model.PullRequest, error) {
	args := []string{"api", "graphql", "--hostname", model.GitHubHost, "-f", "query=" + buildQuery(nums, len(brs)), "-f", "owner=" + owner, "-f", "name=" + name}
	for i, b := range brs {
		args = append(args, "-f", "b"+strconv.Itoa(i)+"="+b)
	}
	out, err := p.exec(ctx, args...)
	if err != nil {
		var ee *runner.ExitError
		if errors.As(err, &ee) && hasData(ee.Stdout) {
			out, err = ee.Stdout, nil // partial answer: some numbers not found
		}
	}
	if err != nil {
		note := classify(err)
		if note != noteRepoNotFound { // one repo's problem is not the source's
			p.setHealth(source.StateFailing, note)
		}
		if note == "rate limited" {
			p.mu.Lock()
			p.backoff[repo] = p.now().Add(rateLimitBackoff)
			p.mu.Unlock()
			if hit {
				return slices.Clone(c.prs), nil
			}
			return nil, errRateLimited
		}
		if note == noteRepoNotFound {
			return nil, fmt.Errorf("github: %w", source.ErrRepoNotFound)
		}
		return nil, fmt.Errorf("github: %s: %w", note, unwrapCtx(err))
	}
	prs, err := parse(out, repo)
	if err != nil {
		p.setHealth(source.StateFailing, "unexpected answer from gh")
		return nil, err
	}
	p.setHealth(source.StateOK, "")
	p.mu.Lock()
	for k, v := range p.cache {
		if p.now().Sub(v.at) > 10*time.Minute {
			delete(p.cache, k)
		}
	}
	p.cache[key] = cached{at: p.now(), prs: prs}
	p.mu.Unlock()
	return slices.Clone(prs), nil
}

var errRateLimited = errors.New("github: rate limited")

// noteRepoNotFound is a failure of one repo (gone, renamed, or not visible to
// the account), not of GitHub as a source.
const noteRepoNotFound = "repository not found"

func unwrapCtx(err error) error {
	for _, e := range []error{context.DeadlineExceeded, context.Canceled} {
		if errors.Is(err, e) {
			return e
		}
	}
	return errors.New("gh failed")
}

func (p *provider) setHealth(st source.State, note string) {
	p.mu.Lock()
	p.health, p.healthNote = st, note
	p.mu.Unlock()
}

// classify turns a gh failure into one short line for the page. gh's output is
// read here and dropped.
func classify(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "gh did not answer in time"
	}
	var ee *runner.ExitError
	if !errors.As(err, &ee) {
		return "gh could not run"
	}
	msg := strings.ToLower(string(ee.Stderr) + string(ee.Stdout))
	switch {
	case strings.Contains(msg, "rate limit"):
		return "rate limited"
	case strings.Contains(msg, "gh auth login"), strings.Contains(msg, "not logged in"),
		strings.Contains(msg, "http 401"), strings.Contains(msg, "bad credentials"):
		// Lookups and the probe go to one host only: say which, so an owner
		// logged in elsewhere (an Enterprise host) knows what is missing.
		return "not logged in to " + model.GitHubHost
	case strings.Contains(msg, "could not resolve to a repository"):
		return noteRepoNotFound
	default:
		return "gh failed"
	}
}

func hasData(out []byte) bool {
	var r struct {
		Data *struct {
			Repository json.RawMessage `json:"repository"`
		} `json:"data"`
	}
	return json.Unmarshal(out, &r) == nil && r.Data != nil && len(r.Data.Repository) > 0 &&
		!bytes.Equal(r.Data.Repository, []byte("null"))
}

func cleanNumbers(in []int) []int {
	var out []int
	for _, n := range in {
		if n > 0 {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) > maxNumbers {
		out = out[:maxNumbers]
	}
	return out
}

func cleanBranches(in []string) []string {
	var out []string
	for _, b := range in {
		if b == "" || len(b) > 255 || strings.ContainsAny(b, "\x00\n\r") {
			continue
		}
		out = append(out, b)
	}
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) > maxBranches {
		out = out[:maxBranches]
	}
	return out
}

func cacheKey(repo string, nums []int, brs []string) string {
	var b strings.Builder
	b.WriteString(repo)
	for _, n := range nums {
		b.WriteString("\x00#" + strconv.Itoa(n))
	}
	for _, br := range brs {
		b.WriteString("\x00@" + br)
	}
	return b.String()
}

// closingIssuesReferences is capped at 50: a pull request closing more lists
// only the first 50.
// A pull request's address is not asked for: it is built from the repo and the
// number (model.PullRequestURL).
const prFields = `number title headRefName headRefOid headRef{target{oid}} baseRefName isCrossRepository state isDraft createdAt closedAt mergedAt reviewDecision ` +
	`closingIssuesReferences(first:50){nodes{number repository{nameWithOwner}}} ` +
	`commits(last:1){nodes{commit{statusCheckRollup{state}}}}`

// buildQuery writes the GraphQL query. Only integers this function checked
// and fixed aliases go into the text; branch names are variables $b0, $b1...
func buildQuery(nums []int, nBranches int) string {
	var q strings.Builder
	q.WriteString("query($owner:String!,$name:String!")
	for i := range nBranches {
		fmt.Fprintf(&q, ",$b%d:String!", i)
	}
	q.WriteString("){repository(owner:$owner,name:$name){")
	for i, n := range nums {
		fmt.Fprintf(&q, "n%d:pullRequest(number:%d){...F} ", i, n)
	}
	for i := range nBranches {
		fmt.Fprintf(&q, "b%d:pullRequests(headRefName:$b%d,first:%d,orderBy:{field:CREATED_AT,direction:DESC}){nodes{...F}} ", i, i, perBranch)
	}
	q.WriteString("defaultBranchRef{name}}} fragment F on PullRequest{" + prFields + "}")
	return q.String()
}

type ghPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	HeadRef     *struct {
		Target *struct {
			Oid string `json:"oid"`
		} `json:"target"`
	} `json:"headRef"`
	BaseRefName    string  `json:"baseRefName"`
	CrossRepo      bool    `json:"isCrossRepository"`
	State          string  `json:"state"`
	IsDraft        bool    `json:"isDraft"`
	CreatedAt      string  `json:"createdAt"`
	ClosedAt       *string `json:"closedAt"`
	MergedAt       *string `json:"mergedAt"`
	ReviewDecision *string `json:"reviewDecision"`
	Closing        struct {
		Nodes []struct {
			Number     int `json:"number"`
			Repository struct {
				NameWithOwner string `json:"nameWithOwner"`
			} `json:"repository"`
		} `json:"nodes"`
	} `json:"closingIssuesReferences"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				Rollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

func parse(out []byte, repo string) ([]model.PullRequest, error) {
	var r struct {
		Data struct {
			Repository map[string]json.RawMessage `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, errors.New("github: unexpected answer from gh")
	}
	var def struct {
		Name string `json:"name"`
	}
	if raw, ok := r.Data.Repository["defaultBranchRef"]; ok {
		_ = json.Unmarshal(raw, &def) // null or odd: no default branch known
	}
	seen := map[int]bool{}
	var prs []model.PullRequest
	add := func(g *ghPR) {
		if g == nil || g.Number <= 0 || seen[g.Number] {
			return
		}
		pr, ok := toModel(g, repo, def.Name)
		if !ok {
			return
		}
		seen[g.Number] = true
		prs = append(prs, pr)
	}
	aliases := make([]string, 0, len(r.Data.Repository))
	for k := range r.Data.Repository {
		aliases = append(aliases, k)
	}
	// Number lookups first: they win over the same pull request found by branch.
	slices.SortFunc(aliases, func(a, b string) int {
		an, bn := strings.HasPrefix(a, "n"), strings.HasPrefix(b, "n")
		if an != bn {
			if an {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	for _, alias := range aliases {
		raw := r.Data.Repository[alias]
		switch {
		case alias == "defaultBranchRef":
		case strings.HasPrefix(alias, "n"):
			var g *ghPR
			if json.Unmarshal(raw, &g) == nil {
				add(g)
			}
		case strings.HasPrefix(alias, "b"):
			var conn *struct {
				Nodes []*ghPR `json:"nodes"`
			}
			if json.Unmarshal(raw, &conn) == nil && conn != nil {
				for _, g := range conn.Nodes {
					// A branch name says nothing about a fork's pull request:
					// any fork can have a branch called main.
					if g != nil && !g.CrossRepo {
						add(g)
					}
				}
			}
		}
	}
	slices.SortFunc(prs, func(a, b model.PullRequest) int { return a.Number - b.Number })
	return prs, nil
}

// oidRe is a full commit id as GitHub gives it: SHA-1 or SHA-256.
var oidRe = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func oid(s string) string {
	if oidRe.MatchString(s) {
		return s
	}
	return ""
}

// toModel turns one answer into a pull request; defaultBranch is the repo's
// default branch, "" when GitHub did not say.
func toModel(g *ghPR, repo, defaultBranch string) (model.PullRequest, bool) {
	pr := model.PullRequest{
		Repo: model.GitHubRepo(repo), Number: g.Number, Title: g.Title, URL: model.PullRequestURL(repo, g.Number), HeadBranch: g.HeadRefName,
		Draft: g.IsDraft, OpenedAt: parseTime(g.CreatedAt),
		BaseBranch:    g.BaseRefName,
		BaseIsDefault: defaultBranch != "" && g.BaseRefName == defaultBranch,
	}
	// A fork's head commit and branch live in another repository: they say
	// nothing about a branch of the same name here, so a fork's pull request
	// carries neither (and so never makes a leftover).
	if !g.CrossRepo {
		pr.HeadOID = oid(g.HeadRefOid)
		if g.HeadRef != nil && g.HeadRef.Target != nil {
			pr.HeadRefOID = oid(g.HeadRef.Target.Oid)
		}
	}
	switch g.State {
	case "OPEN":
		pr.State = model.PRStateOpen
	case "MERGED":
		pr.State = model.PRStateMerged
		if g.MergedAt != nil {
			pr.ClosedAt = parseTime(*g.MergedAt)
		}
	case "CLOSED":
		pr.State = model.PRStateClosed
	default:
		return pr, false // a state this adapter does not know: not shown as a guess
	}
	if pr.ClosedAt.IsZero() && pr.State != model.PRStateOpen && g.ClosedAt != nil {
		pr.ClosedAt = parseTime(*g.ClosedAt)
	}
	pr.Checks = model.ChecksNone
	if n := len(g.Commits.Nodes); n > 0 {
		if roll := g.Commits.Nodes[n-1].Commit.Rollup; roll != nil {
			pr.Checks = mapChecks(roll.State)
		}
	}
	if g.ReviewDecision != nil {
		switch *g.ReviewDecision {
		case "APPROVED":
			pr.Review = model.ReviewApproved
		case "CHANGES_REQUESTED":
			pr.Review = model.ReviewChangesRequested
		case "REVIEW_REQUIRED":
			pr.Review = model.ReviewRequired
		}
	}
	for _, n := range g.Closing.Nodes {
		if strings.EqualFold(n.Repository.NameWithOwner, repo) && n.Number > 0 && !slices.Contains(pr.ClosingIssues, n.Number) {
			pr.ClosingIssues = append(pr.ClosingIssues, n.Number)
		}
	}
	return pr, true
}

func mapChecks(state string) model.Checks {
	switch state {
	case "SUCCESS":
		return model.ChecksPassing
	case "FAILURE", "ERROR":
		return model.ChecksFailing
	case "PENDING", "EXPECTED":
		return model.ChecksRunning
	case "":
		return model.ChecksNone
	default:
		return model.ChecksUnknown
	}
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

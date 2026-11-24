package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// issueReposPerCall caps the repositories one gh call asks about. 20
// repositories with 30 issues each cost 6 points of GitHub's 5,000 an hour
// (measured with rateLimit(dryRun: true), spike 15).
const issueReposPerCall = 20

// OpenIssues lists the open issues of repos on GitHub, in one read-only
// GraphQL call per issueReposPerCall repositories. Owners and names travel
// as raw string variables (-f), never inside the query text. A repository
// GitHub says it does not show is answered as not found; one whose answer
// failed for another reason is left out, so the board keeps what it had.
//
// Rate limits are shared with pull request lookups (p.backoff, by repo): a
// repository in a backoff is not asked, and a rate-limited answer here puts
// every repository of this look into one. A lost login is
// source.ErrNotLoggedIn.
func (p *provider) OpenIssues(ctx context.Context, repos []model.ForgeRepo) ([]model.RepoIssues, error) {
	if p.off {
		return nil, nil
	}
	var asks []issueAsk
	seen := map[model.ForgeRepo]bool{}
	for _, r := range repos {
		slug, ok := githubSlug(r)
		if !ok || seen[r] {
			continue
		}
		owner, name, ok := validRepo(slug)
		if !ok {
			continue
		}
		seen[r] = true
		asks = append(asks, issueAsk{repo: r, slug: slug, owner: owner, name: name})
	}
	if len(asks) == 0 {
		return nil, nil
	}
	if len(p.askable(asks)) == 0 {
		return nil, fmt.Errorf("github: %w", source.ErrRateLimited)
	}
	if p.found(p.bin) == "" {
		p.setHealth(source.StateMissing, "gh not found")
		return nil, errors.New("github: gh not found")
	}

	var out []model.RepoIssues
	var firstErr error
	// The health says how the whole look went: one chunk that failed or
	// answered in part is not undone by a later one that answered in full.
	incomplete, failed, answered := false, false, false
	for start := 0; start < len(asks); start += issueReposPerCall {
		// An issue limit that arrived meanwhile (a pull request or Leftovers
		// lookup shares it) holds the rest of this look too; repositories in
		// a backoff of their own only leave their chunk smaller, or empty.
		if p.issuesLimited() {
			return out, fmt.Errorf("github: %w", source.ErrRateLimited)
		}
		chunk := p.askable(asks[start:min(start+issueReposPerCall, len(asks))])
		if len(chunk) == 0 {
			continue
		}
		args := []string{"api", "graphql", "--hostname", model.GitHubHost, "-f", "query=" + buildIssuesQuery(len(chunk))}
		for i, a := range chunk {
			args = append(args, "-f", "o"+strconv.Itoa(i)+"="+a.owner, "-f", "n"+strconv.Itoa(i)+"="+a.name)
		}
		raw, err := p.exec(ctx, args...)
		if err != nil {
			var ee *runner.ExitError
			if errors.As(err, &ee) && hasIssuesData(ee.Stdout) {
				raw, err = ee.Stdout, nil // partial answer: parseIssues reads the errors
			}
		}
		if err != nil {
			note := classify(err)
			if note != noteRepoNotFound {
				p.setHealth(source.StateFailing, note)
				failed = true
			}
			switch {
			case note == "rate limited":
				p.limitIssues(asks)
				return out, fmt.Errorf("github: %w", source.ErrRateLimited)
			case strings.HasPrefix(note, "not logged in"):
				// A login that is gone answers for none of it: the rows of
				// the chunks before are dropped with it.
				return nil, fmt.Errorf("github: %s: %w", note, source.ErrNotLoggedIn)
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("github: %s: %w", note, unwrapCtx(err))
			}
			continue
		}
		repos := make([]model.ForgeRepo, len(chunk))
		for i, a := range chunk {
			repos[i] = a.repo
		}
		got, rateLimited, partial, err := parseIssues(raw, repos)
		if err != nil {
			p.setHealth(source.StateFailing, "unexpected answer from gh")
			failed = true
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, got...)
		if rateLimited {
			p.setHealth(source.StateFailing, "rate limited")
			p.limitIssues(asks)
			return out, fmt.Errorf("github: %w", source.ErrRateLimited)
		}
		if partial {
			// Some repositories did not answer: the rest stands, the health does not say all is well.
			p.setHealth(source.StateFailing, "incomplete answer from gh")
			incomplete = true
			continue
		}
		answered = true
	}
	if answered && !failed && !incomplete {
		p.setHealth(source.StateOK, "")
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	if len(out) == 0 && incomplete {
		return nil, errors.New("github: incomplete answer")
	}
	return out, nil
}

type issueAsk struct {
	repo              model.ForgeRepo
	slug, owner, name string
}

// limitIssues starts a rate-limit backoff for issue lists and for every
// repository in asks, which pull request and Leftovers lookups share.
func (p *provider) limitIssues(asks []issueAsk) {
	p.mu.Lock()
	defer p.mu.Unlock()
	until := p.now().Add(rateLimitBackoff)
	p.issuesUntil = until
	for _, a := range asks {
		p.backoff[backoffKey(a.slug)] = until
	}
}

// issuesLimited says whether the backoff of all issue lists runs.
func (p *provider) issuesLimited() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.now().Before(p.issuesUntil)
}

// askable is the part of asks that may be asked about now: none while the
// issue backoff runs, and not a repository in a backoff of its own.
func (p *provider) askable(asks []issueAsk) []issueAsk {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if now.Before(p.issuesUntil) {
		return nil
	}
	ready := asks[:0:0]
	for _, a := range asks {
		if !now.Before(p.backoff[backoffKey(a.slug)]) {
			ready = append(ready, a)
		}
	}
	return ready
}

// backoffKey is a repository's key in p.backoff: GitHub ignores case in
// owner and name.
func backoffKey(slug string) string { return strings.ToLower(slug) }

// issueFields are asked for each open issue. Labels come sorted by name.
var issueFields = `number title updatedAt milestone{title dueOn} labels(first:` + strconv.Itoa(model.MaxIssueLabels) +
	`,orderBy:{field:NAME,direction:ASC}){nodes{name}}`

// buildIssuesQuery writes the query for n repositories, $o0/$n0 to
// $o<n-1>/$n<n-1>. Only integers and fixed text go into it.
func buildIssuesQuery(n int) string {
	var q strings.Builder
	q.WriteString("query(")
	for i := range n {
		if i > 0 {
			q.WriteString(",")
		}
		fmt.Fprintf(&q, "$o%d:String!,$n%d:String!", i, i)
	}
	q.WriteString("){")
	for i := range n {
		fmt.Fprintf(&q, "r%d:repository(owner:$o%d,name:$n%d){hasIssuesEnabled issues(states:OPEN,first:%d,orderBy:{field:UPDATED_AT,direction:DESC}){totalCount nodes{%s}}} ",
			i, i, i, model.MaxRepoIssues, issueFields)
	}
	q.WriteString("}")
	return q.String()
}

type ghRepoIssues struct {
	HasIssuesEnabled bool `json:"hasIssuesEnabled"`
	Issues           struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Number    int    `json:"number"`
			Title     string `json:"title"`
			UpdatedAt string `json:"updatedAt"`
			Milestone *struct {
				Title string  `json:"title"`
				DueOn *string `json:"dueOn"`
			} `json:"milestone"`
			Labels struct {
				Nodes []struct {
					Name string `json:"name"`
				} `json:"nodes"`
			} `json:"labels"`
		} `json:"nodes"`
	} `json:"issues"`
}

// hasIssuesData: gh failed, yet GitHub answered the query with data (some
// aliases may be null; the errors say why).
func hasIssuesData(out []byte) bool {
	var r struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(out, &r) != nil {
		return false
	}
	_, ok := r.Data["r0"]
	return ok
}

// ghNotFound is the GraphQL error type of a repository GitHub does not show
// (gone, renamed, or not visible to this login); ghRateLimited that of a
// rate limit.
const (
	ghNotFound    = "NOT_FOUND"
	ghRateLimited = "RATE_LIMITED"
)

// parseIssues reads the answer for repos, aliased r0, r1, ... in that order.
// An alias counts only when it is whole: no error at all names it or anything
// under it, or it is null and the one error that names exactly it says
// NOT_FOUND. Any other alias with an error is left out (the board keeps what
// it had) and sets partial. rateLimited: an error said RATE_LIMITED.
func parseIssues(raw []byte, repos []model.ForgeRepo) (out []model.RepoIssues, rateLimited, partial bool, err error) {
	var r struct {
		Data   map[string]*ghRepoIssues `json:"data"`
		Errors []struct {
			Type string `json:"type"`
			Path []any  `json:"path"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.Data == nil {
		return nil, false, false, errors.New("github: unexpected answer")
	}
	notFound := map[string]bool{} // alias -> a NOT_FOUND error names exactly it
	hurt := map[string]bool{}     // alias -> some other error names it or what is under it
	for _, e := range r.Errors {
		if e.Type == ghRateLimited {
			rateLimited = true
		}
		alias := ""
		if len(e.Path) > 0 {
			alias, _ = e.Path[0].(string)
		}
		if alias == "" {
			continue
		}
		if e.Type == ghNotFound && len(e.Path) == 1 {
			notFound[alias] = true
		} else {
			hurt[alias] = true
		}
	}
	out = make([]model.RepoIssues, 0, len(repos))
	for i, repo := range repos {
		alias := "r" + strconv.Itoa(i)
		g, present := r.Data[alias]
		switch {
		case !present:
			return nil, false, false, errors.New("github: unexpected answer")
		case g == nil && notFound[alias] && !hurt[alias]:
			out = append(out, model.RepoIssues{Repo: repo, State: model.RepoIssuesNotFound})
			continue
		case g == nil || hurt[alias] || notFound[alias]:
			partial = true
			continue
		case !g.HasIssuesEnabled:
			out = append(out, model.RepoIssues{Repo: repo, State: model.RepoIssuesDisabled})
			continue
		}
		ri := model.RepoIssues{Repo: repo, State: model.RepoIssuesOK, Open: max(g.Issues.TotalCount, 0)}
		for _, n := range g.Issues.Nodes {
			if n.Number <= 0 || len(ri.List) == model.MaxRepoIssues {
				continue
			}
			is := model.OpenIssue{Number: n.Number, Title: n.Title, UpdatedAt: parseTime(n.UpdatedAt)}
			if n.Milestone != nil {
				is.Milestone = n.Milestone.Title
				if n.Milestone.DueOn != nil {
					is.MilestoneDue = parseTime(*n.Milestone.DueOn)
				}
			}
			for _, l := range n.Labels.Nodes {
				if len(is.Labels) < model.MaxIssueLabels {
					is.Labels = append(is.Labels, l.Name)
				}
			}
			ri.List = append(ri.List, is)
		}
		ri.Open = max(ri.Open, len(ri.List))
		out = append(out, ri)
	}
	return out, rateLimited, partial, nil
}

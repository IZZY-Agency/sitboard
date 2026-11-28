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
//
// The pinned numbers of a repository are asked in its own alias, as
// issue(number:) aliases: the same call, no extra one; only integers go into
// the query text.
func (p *provider) OpenIssues(ctx context.Context, repos []model.ForgeRepo, pinned map[model.ForgeRepo][]int) ([]model.RepoIssues, error) {
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
		asks = append(asks, issueAsk{repo: r, slug: slug, owner: owner, name: name, pins: model.PinsToAsk(pinned[r])})
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
		args := []string{"api", "graphql", "--hostname", model.GitHubHost, "-f", "query=" + buildIssuesQuery(chunk)}
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
		got, rateLimited, partial, err := parseIssues(raw, chunk)
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
	pins              []int // the pinned numbers to ask (model.PinsToAsk)
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

// buildIssuesQuery writes the query for asks, $o0/$n0 to $o<n-1>/$n<n-1>,
// with each repository's pinned numbers as aliases i<number> inside its own
// alias. Only integers and fixed text go into it.
func buildIssuesQuery(asks []issueAsk) string {
	var q strings.Builder
	q.WriteString("query(")
	for i := range asks {
		if i > 0 {
			q.WriteString(",")
		}
		fmt.Fprintf(&q, "$o%d:String!,$n%d:String!", i, i)
	}
	q.WriteString("){")
	for i, a := range asks {
		fmt.Fprintf(&q, "r%d:repository(owner:$o%d,name:$n%d){hasIssuesEnabled issues(states:OPEN,first:%d,orderBy:{field:UPDATED_AT,direction:DESC}){totalCount nodes{%s}}",
			i, i, i, model.MaxRepoIssues, issueFields)
		for _, n := range a.pins {
			fmt.Fprintf(&q, " i%d:issue(number:%d){state %s}", n, n, issueFields)
		}
		q.WriteString("} ")
	}
	q.WriteString("}")
	return q.String()
}

// ghIssue is one issue as issueFields ask it.
type ghIssue struct {
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
}

// openIssue is n as the board keeps it, labels capped.
func (n ghIssue) openIssue() model.OpenIssue {
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
	return is
}

type ghRepoIssues struct {
	HasIssuesEnabled bool `json:"hasIssuesEnabled"`
	Issues           struct {
		TotalCount int       `json:"totalCount"`
		Nodes      []ghIssue `json:"nodes"`
	} `json:"issues"`
}

// ghPinned is a pinned issue asked by number: null when GitHub has no such
// issue there (a NOT_FOUND error names its alias).
type ghPinned struct {
	ghIssue
	State string `json:"state"` // OPEN or CLOSED
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
// (gone, renamed, or not visible to this login), or of an issue number it
// has no issue for (a pull request's number too); ghRateLimited that of a
// rate limit.
//
// A pin is gone on one signal only: its alias i<N> is null and an error of
// type NOT_FOUND names exactly ["r<i>", "i<N>"], inside a repository alias
// that answered (not null, issues on, no error of its own). On GitHub an
// issue is visible to whoever sees its repository, so a login that lost
// access gets the repository alias null with its own error (NOT_FOUND at
// ["r<i>"], or FORBIDDEN for an organisation's SSO), nothing under it is
// resolved, and no pin of it is reported (TestOnlyAPinsOwnNotFoundIsGone).
const (
	ghNotFound    = "NOT_FOUND"
	ghRateLimited = "RATE_LIMITED"
)

// pinAlias is the alias of pinned number n inside its repository's alias.
func pinAlias(n int) string { return "i" + strconv.Itoa(n) }

// parseIssues reads the answer for asks, aliased r0, r1, ... in that order.
// An alias counts only when it is whole: no error at all names it or anything
// under it, or it is null and the one error that names exactly it says
// NOT_FOUND. Any other alias with an error is left out (the board keeps what
// it had) and sets partial. rateLimited: an error said RATE_LIMITED.
//
// An error under one of a repository's pin aliases (i<number>) is that pin's
// alone: a null pin that a NOT_FOUND error names exactly is gone, a pin with
// any other error has no answer, and the repository's own answer stands.
// No pin is gone in a call with an unscoped error (one that names no alias
// of the call, or a rate limit anywhere): the lists that stand are kept,
// but GitHub may have stopped short of the pins. Nor is one gone that its
// repository's own list shows.
func parseIssues(raw []byte, asks []issueAsk) (out []model.RepoIssues, rateLimited, partial bool, err error) {
	var r struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Type string `json:"type"`
			Path []any  `json:"path"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.Data == nil {
		return nil, false, false, errors.New("github: unexpected answer")
	}
	pinOf := make([]map[string]int, len(asks)) // per ask: pin alias -> number
	aliasAt := map[string]int{}
	for i, a := range asks {
		aliasAt["r"+strconv.Itoa(i)] = i
		pinOf[i] = map[string]int{}
		for _, n := range a.pins {
			pinOf[i][pinAlias(n)] = n
		}
	}
	notFound := map[string]bool{} // alias -> a NOT_FOUND error names exactly it
	hurt := map[string]bool{}     // alias -> some other error names it or what is under it
	pinGone := map[[2]string]bool{}
	pinHurt := map[[2]string]bool{}
	unscoped := false
	for _, e := range r.Errors {
		if e.Type == ghRateLimited {
			rateLimited = true
		}
		alias := ""
		if len(e.Path) > 0 {
			alias, _ = e.Path[0].(string)
		}
		if _, ours := aliasAt[alias]; !ours || e.Type == ghRateLimited {
			unscoped = true
		}
		if alias == "" {
			continue
		}
		if i, ok := aliasAt[alias]; ok && len(e.Path) >= 2 {
			if sub, _ := e.Path[1].(string); pinOf[i][sub] > 0 {
				if e.Type == ghNotFound && len(e.Path) == 2 {
					pinGone[[2]string{alias, sub}] = true
				} else {
					pinHurt[[2]string{alias, sub}] = true
				}
				continue
			}
		}
		if e.Type == ghNotFound && len(e.Path) == 1 {
			notFound[alias] = true
		} else {
			hurt[alias] = true
		}
	}
	out = make([]model.RepoIssues, 0, len(asks))
	for i, a := range asks {
		repo := a.repo
		alias := "r" + strconv.Itoa(i)
		rawRepo, present := r.Data[alias]
		var g *ghRepoIssues
		if present {
			if err := json.Unmarshal(rawRepo, &g); err != nil {
				return nil, false, false, errors.New("github: unexpected answer")
			}
		}
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
			ri.List = append(ri.List, n.openIssue())
		}
		ri.Open = max(ri.Open, len(ri.List))
		if len(a.pins) > 0 {
			gone := pinGone
			if unscoped {
				gone = nil
			}
			listed := map[int]bool{}
			for _, is := range ri.List {
				listed[is.Number] = true
			}
			ri.Pinned = parsePinned(rawRepo, alias, a.pins, gone, pinHurt, listed)
		}
		out = append(out, ri)
	}
	return out, rateLimited, partial, nil
}

// parsePinned reads the pin aliases of one repository's answer rawRepo
// (alias): what is missing, has an error of its own, or names another
// number is not answered, and so is a gone one that listed (the numbers of
// the repository's own list) shows.
func parsePinned(rawRepo json.RawMessage, alias string, pins []int, gone, hurt map[[2]string]bool, listed map[int]bool) []model.PinnedIssue {
	var byAlias map[string]json.RawMessage
	if json.Unmarshal(rawRepo, &byAlias) != nil {
		return nil
	}
	var out []model.PinnedIssue
	for _, n := range pins {
		sub := pinAlias(n)
		rawPin, present := byAlias[sub]
		var g *ghPinned
		if present && json.Unmarshal(rawPin, &g) != nil {
			continue
		}
		switch at := [2]string{alias, sub}; {
		case !present || hurt[at]:
		case g == nil && gone[at] && !listed[n]:
			out = append(out, model.PinnedIssue{OpenIssue: model.OpenIssue{Number: n}, State: model.PinGone})
		case g == nil || gone[at] || g.Number != n:
		case g.State == "OPEN":
			out = append(out, model.PinnedIssue{OpenIssue: g.openIssue(), State: model.PinOpen})
		case g.State == "CLOSED":
			out = append(out, model.PinnedIssue{OpenIssue: g.openIssue(), State: model.PinClosed})
		}
	}
	return out
}

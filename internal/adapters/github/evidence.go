package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// evidencePerHead is how many pull requests per head branch name the
// Leftovers question asks for; a head with more is incomplete.
const evidencePerHead = 10

// Evidence asks GitHub, now, for every pull request into repo from each of
// branches (up to evidencePerHead each). It never answers from the cache:
// during a backoff, on a rate limit or on any error in GitHub's answer it
// returns an error, so Leftovers has no evidence rather than old evidence. A
// head with more pull requests than asked for, a null entry, or an entry
// without its fork flag is marked incomplete.
func (p *provider) Evidence(ctx context.Context, fr model.ForgeRepo, branches []string) (model.PREvidence, error) {
	var ev model.PREvidence
	if p.off {
		return ev, errors.New("github: off")
	}
	repo, ok := githubSlug(fr)
	if !ok {
		return ev, fmt.Errorf("github: not a GitHub repo")
	}
	owner, name, ok := validRepo(repo)
	if !ok {
		return ev, fmt.Errorf("github: not a repo slug")
	}
	brs := cleanBranches(branches)
	if len(brs) == 0 {
		return ev, nil
	}
	p.mu.Lock()
	until := p.backoff[backoffKey(repo)]
	now := p.now()
	p.mu.Unlock()
	if now.Before(until) {
		return ev, errRateLimited
	}
	if p.found(p.bin) == "" {
		p.setHealth(source.StateMissing, "gh not found")
		return ev, errors.New("github: gh not found")
	}
	args := []string{"api", "graphql", "--hostname", model.GitHubHost, "-f", "query=" + buildEvidenceQuery(len(brs)), "-f", "owner=" + owner, "-f", "name=" + name}
	for i, b := range brs {
		args = append(args, "-f", "b"+strconv.Itoa(i)+"="+b)
	}
	out, err := p.exec(ctx, args...)
	if err != nil {
		// A partial answer is no answer here.
		note := classify(err)
		if note != noteRepoNotFound {
			p.setHealth(source.StateFailing, note)
		}
		if note == "rate limited" {
			p.mu.Lock()
			p.backoff[backoffKey(repo)] = p.now().Add(rateLimitBackoff)
			p.mu.Unlock()
			return ev, errRateLimited
		}
		if note == noteRepoNotFound {
			return ev, fmt.Errorf("github: %w", source.ErrRepoNotFound)
		}
		return ev, fmt.Errorf("github: %s: %w", note, unwrapCtx(err))
	}
	ev, err = parseEvidence(out, repo, brs)
	if err != nil {
		p.setHealth(source.StateFailing, "unexpected answer from gh")
		return model.PREvidence{}, err
	}
	p.setHealth(source.StateOK, "")
	return ev, nil
}

// buildEvidenceQuery: per head, its pull requests newest first, with whether
// there are more. Branch names are variables, never part of the text.
func buildEvidenceQuery(nBranches int) string {
	var q strings.Builder
	q.WriteString("query($owner:String!,$name:String!")
	for i := range nBranches {
		fmt.Fprintf(&q, ",$b%d:String!", i)
	}
	q.WriteString("){repository(owner:$owner,name:$name){")
	for i := range nBranches {
		// Every state, said outright: an older open pull request on the
		// same head keeps a newer merged one from counting.
		fmt.Fprintf(&q, "b%d:pullRequests(headRefName:$b%d,states:[OPEN,CLOSED,MERGED],first:%d,orderBy:{field:CREATED_AT,direction:DESC}){pageInfo{hasNextPage} nodes{...F}} ",
			i, i, evidencePerHead)
		// Open pull requests built on the head (it is their base): the
		// branch on GitHub is still in use.
		fmt.Fprintf(&q, "o%d:pullRequests(baseRefName:$b%d,states:[OPEN],first:1){totalCount} ", i, i)
	}
	q.WriteString("defaultBranchRef{name}}} fragment F on PullRequest{" + prFields + "}")
	return q.String()
}

// parseEvidence reads the answer for heads brs (in the query's order). Any
// error GitHub reports, or a missing part, fails the whole answer; a head
// whose list may be cut short is marked incomplete, one an open pull request
// has as its base is marked in Bases.
func parseEvidence(out []byte, repo string, brs []string) (model.PREvidence, error) {
	bad := errors.New("github: unexpected answer from gh")
	var r struct {
		Data *struct {
			Repository map[string]json.RawMessage `json:"repository"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(out, &r); err != nil || len(r.Errors) > 0 || r.Data == nil || r.Data.Repository == nil {
		return model.PREvidence{}, bad
	}
	var def struct {
		Name string `json:"name"`
	}
	if raw, ok := r.Data.Repository["defaultBranchRef"]; ok {
		_ = json.Unmarshal(raw, &def) // null: no default branch known, so nothing merged into it
	}
	ev := model.PREvidence{Incomplete: map[string]bool{}, Bases: map[string]bool{}}
	for i, head := range brs {
		var based *struct {
			TotalCount *int `json:"totalCount"`
		}
		if raw, ok := r.Data.Repository["o"+strconv.Itoa(i)]; !ok || json.Unmarshal(raw, &based) != nil || based == nil || based.TotalCount == nil {
			return model.PREvidence{}, bad
		}
		if *based.TotalCount > 0 {
			ev.Bases[head] = true
		}
		raw, ok := r.Data.Repository["b"+strconv.Itoa(i)]
		if !ok {
			return model.PREvidence{}, bad
		}
		var conn *struct {
			PageInfo *struct {
				HasNextPage *bool `json:"hasNextPage"`
			} `json:"pageInfo"`
			Nodes []*struct {
				ghPR
				Cross *bool `json:"isCrossRepository"`
			} `json:"nodes"`
		}
		if err := json.Unmarshal(raw, &conn); err != nil || conn == nil || conn.PageInfo == nil || conn.PageInfo.HasNextPage == nil {
			return model.PREvidence{}, bad
		}
		if *conn.PageInfo.HasNextPage {
			ev.Incomplete[head] = true
		}
		for _, n := range conn.Nodes {
			if n == nil || n.Cross == nil || n.Number <= 0 || n.HeadRefName != head {
				ev.Incomplete[head] = true
				continue
			}
			if *n.Cross {
				continue // a fork's branch of the same name says nothing about this one
			}
			g := n.ghPR
			g.CrossRepo = false // the outer field took isCrossRepository
			pr, ok := toModel(&g, repo, def.Name)
			if !ok {
				ev.Incomplete[head] = true // a state this adapter does not know
				continue
			}
			ev.PRs = append(ev.PRs, pr)
		}
	}
	return ev, nil
}

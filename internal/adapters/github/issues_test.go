package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

var issuesAt = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

// issuesAnswer answers a query for n repositories, every one with one issue.
func issuesAnswer(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"data":{`)
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"r%d":{"hasIssuesEnabled":true,"issues":{"totalCount":1,"nodes":[{"number":%d,"title":"Sample issue","updatedAt":"2026-11-20T09:00:00Z","milestone":null,"labels":{"nodes":[]}}]}}`, i, i+1)
	}
	b.WriteString(`}}`)
	return []byte(b.String())
}

// argValue is the value of the -f field name=... in args.
func argValue(args []string, name string) (string, bool) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-f" && strings.HasPrefix(args[i+1], name+"=") {
			return strings.TrimPrefix(args[i+1], name+"="), true
		}
	}
	return "", false
}

func TestOpenIssuesAsksTwentyRepositoriesPerCallWithNamesAsVariables(t *testing.T) {
	var repos []model.ForgeRepo
	for i := range 25 {
		repos = append(repos, model.GitHubRepo(fmt.Sprintf("sample-org/sample-%02d", i)))
	}
	f := &fakeGh{reply: func(args []string) ([]byte, error) {
		q, _ := argValue(args, "query")
		return issuesAnswer(strings.Count(q, "repository(")), nil
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	got, err := p.OpenIssues(context.Background(), repos, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.apiCalls() != 2 {
		t.Fatalf("gh api calls = %d, want 2 (20 + 5)", f.apiCalls())
	}
	if len(got) != 25 {
		t.Fatalf("answered %d repositories, want 25", len(got))
	}
	for _, c := range f.calls {
		if c[0] != "api" {
			continue
		}
		q, _ := argValue(c, "query")
		if strings.Contains(q, "sample-org") {
			t.Fatalf("an owner went into the query text: %s", q)
		}
		if !slices.Contains(c, "--hostname") || !slices.Contains(c, model.GitHubHost) {
			t.Fatalf("call not pinned to %s: %v", model.GitHubHost, c)
		}
	}
	if o, _ := argValue(f.calls[len(f.calls)-1], "o4"); o != "sample-org" {
		t.Fatalf("o4 = %q", o)
	}
	if n, _ := argValue(f.calls[len(f.calls)-1], "n4"); n != "sample-24" {
		t.Fatalf("n4 = %q, want the 25th repository in the second call", n)
	}
}

func TestOpenIssuesReadsEachRepositorysAnswer(t *testing.T) {
	answer := `{"data":{
	  "r0":{"hasIssuesEnabled":true,"issues":{"totalCount":41,"nodes":[
	    {"number":48,"title":"Export misses the last page","updatedAt":"2026-11-23T08:00:00Z",
	     "milestone":{"title":"0.5.0","dueOn":"2026-12-01T00:00:00Z"},
	     "labels":{"nodes":[{"name":"a"},{"name":"b"},{"name":"c"},{"name":"d"},{"name":"e"},{"name":"f"}]}},
	    {"number":0,"title":"no number","updatedAt":"2026-11-23T08:00:00Z","milestone":null,"labels":{"nodes":[]}},
	    {"number":7,"title":"No milestone","updatedAt":"2026-11-20T08:00:00Z","milestone":{"title":"Later","dueOn":null},"labels":{"nodes":[]}}]}},
	  "r1":null,
	  "r2":{"hasIssuesEnabled":false,"issues":{"totalCount":0,"nodes":[]}}},
	 "errors":[{"type":"NOT_FOUND","path":["r1"]}]}`
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte(answer), Stderr: []byte("gh: Could not resolve to a Repository")}
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	a, b, c := model.GitHubRepo("sample-org/alpha"), model.GitHubRepo("sample-org/gone"), model.GitHubRepo("sample-org/quiet")
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{a, b, c}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Repo != a || got[0].State != model.RepoIssuesOK || got[0].Open != 41 || len(got[0].List) != 2 {
		t.Fatalf("alpha = %+v", got[0])
	}
	first := got[0].List[0]
	if first.Number != 48 || first.Milestone != "0.5.0" || !first.MilestoneDue.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) ||
		len(first.Labels) != model.MaxIssueLabels || !first.UpdatedAt.Equal(time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("first issue = %+v", first)
	}
	if second := got[0].List[1]; second.Number != 7 || second.Milestone != "Later" || !second.MilestoneDue.IsZero() {
		t.Fatalf("second issue = %+v", second)
	}
	if got[1].Repo != b || got[1].State != model.RepoIssuesNotFound {
		t.Fatalf("gone = %+v", got[1])
	}
	if got[2].Repo != c || got[2].State != model.RepoIssuesDisabled {
		t.Fatalf("quiet = %+v", got[2])
	}
	if st := p.Probe(context.Background()); st.State != source.StateOK {
		t.Fatalf("a repository GitHub does not show failed the source: %+v", st)
	}
}

func TestOpenIssuesBacksOffAfterARateLimit(t *testing.T) {
	limited := true
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		if limited {
			return nil, &runner.ExitError{Code: 1, Stderr: []byte("gh: API rate limit exceeded for user. (HTTP 403)")}
		}
		return issuesAnswer(1), nil
	}}
	c := &clock{t: issuesAt}
	p := newTestProvider(f, c)
	repos := []model.ForgeRepo{model.GitHubRepo("sample-org/alpha")}
	if _, err := p.OpenIssues(context.Background(), repos, nil); !errors.Is(err, source.ErrRateLimited) {
		t.Fatalf("err = %v, want rate limited", err)
	}
	limited = false
	c.t = c.t.Add(rateLimitBackoff - time.Second)
	if _, err := p.OpenIssues(context.Background(), repos, nil); !errors.Is(err, source.ErrRateLimited) || f.apiCalls() != 1 {
		t.Fatalf("during the backoff: err %v, %d calls; want no gh call", err, f.apiCalls())
	}
	c.t = c.t.Add(time.Second)
	if got, err := p.OpenIssues(context.Background(), repos, nil); err != nil || len(got) != 1 || f.apiCalls() != 2 {
		t.Fatalf("after the backoff: %+v, %v, %d calls", got, err, f.apiCalls())
	}
}

func TestOpenIssuesSkipsOtherForgesAndBadSlugs(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) { return issuesAnswer(1), nil }}
	p := newTestProvider(f, &clock{t: issuesAt})
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{
		model.GitLabRepo("gitlab.example.com", "group/project"),
		{Forge: model.ForgeGitHub, Host: "github.example.com", Path: "sample-org/alpha"},
		model.GitHubRepo("-flag/alpha"),
		model.GitHubRepo("sample-org/alpha"),
		model.GitHubRepo("sample-org/alpha"),
	}, nil)

	if err != nil || len(got) != 1 || got[0].Repo != model.GitHubRepo("sample-org/alpha") {
		t.Fatalf("got %+v, %v", got, err)
	}
	if n, _ := argValue(f.calls[len(f.calls)-1], "n1"); n != "" {
		t.Fatalf("a second repository was asked: %v", f.calls[len(f.calls)-1])
	}
}

func TestOpenIssuesSwitchedOffRunsNoGh(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) { return issuesAnswer(1), nil }}
	p := newTestProvider(f, &clock{t: issuesAt})
	p.off = true
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{model.GitHubRepo("sample-org/alpha")}, nil)
	if err != nil || got != nil || len(f.calls) != 0 {
		t.Fatalf("got %+v, %v, calls %v", got, err, f.calls)
	}
}

func TestOpenIssuesFailureIsAnError(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stderr: []byte("gh: something broke")}
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{model.GitHubRepo("sample-org/alpha")}, nil)
	if err == nil || got != nil {
		t.Fatalf("got %+v, %v; want an error", got, err)
	}
}

func TestOpenIssuesLeavesOutARepositoryWhoseAnswerFailedForAnotherReason(t *testing.T) {
	answer := `{"data":{
	  "r0":{"hasIssuesEnabled":true,"issues":{"totalCount":1,"nodes":[{"number":3,"title":"Sample issue","updatedAt":"2026-11-23T08:00:00Z","milestone":null,"labels":{"nodes":[]}}]}},
	  "r1":null,
	  "r2":null},
	 "errors":[{"type":"SERVICE_UNAVAILABLE","path":["r1"]}]}`
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte(answer)}
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	a := model.GitHubRepo("sample-org/alpha")
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{a, model.GitHubRepo("sample-org/beta"), model.GitHubRepo("sample-org/gamma")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Repo != a || got[0].State != model.RepoIssuesOK {
		t.Fatalf("got %+v, want only alpha: a null alias without NOT_FOUND is not an answer", got)
	}
}

func TestOpenIssuesHonoursARateLimitThatCameWithData(t *testing.T) {
	answer := `{"data":{
	  "r0":{"hasIssuesEnabled":true,"issues":{"totalCount":1,"nodes":[{"number":3,"title":"Sample issue","updatedAt":"2026-11-23T08:00:00Z","milestone":null,"labels":{"nodes":[]}}]}},
	  "r1":null},
	 "errors":[{"type":"RATE_LIMITED","path":["r1"]}]}`
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte(answer)}
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	a := model.GitHubRepo("sample-org/alpha")
	repos := []model.ForgeRepo{a, model.GitHubRepo("sample-org/beta")}
	got, err := p.OpenIssues(context.Background(), repos, nil)
	if !errors.Is(err, source.ErrRateLimited) || len(got) != 1 || got[0].Repo != a {
		t.Fatalf("got %+v, %v; want alpha kept and a rate limit", got, err)
	}
	if _, err := p.OpenIssues(context.Background(), repos, nil); !errors.Is(err, source.ErrRateLimited) || f.apiCalls() != 1 {
		t.Fatalf("after: %v, %d calls; want no gh call during the backoff", err, f.apiCalls())
	}
}

func TestIssuesAndPullRequestLookupsShareTheRateLimitBackoff(t *testing.T) {
	repo := model.GitHubRepo("sample-org/alpha")
	limit := func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stderr: []byte("gh: API rate limit exceeded for user. (HTTP 403)")}
	}

	// An issue list that hit the limit holds pull request lookups of its repositories.
	f := &fakeGh{reply: limit}
	p := newTestProvider(f, &clock{t: issuesAt})
	if _, err := p.OpenIssues(context.Background(), []model.ForgeRepo{repo}, nil); !errors.Is(err, source.ErrRateLimited) {
		t.Fatalf("issues: %v", err)
	}
	_, _ = p.Lookup(context.Background(), repo, []int{12}, nil)
	if f.apiCalls() != 1 {
		t.Fatalf("a pull request lookup ran during the issues' backoff: %d calls", f.apiCalls())
	}

	// A pull request lookup that hit the limit holds issue lists of its repository.
	g := &fakeGh{reply: limit}
	q := newTestProvider(g, &clock{t: issuesAt})
	_, _ = q.Lookup(context.Background(), repo, []int{12}, nil)
	if g.apiCalls() != 1 {
		t.Fatalf("lookup calls %d", g.apiCalls())
	}
	if _, err := q.OpenIssues(context.Background(), []model.ForgeRepo{repo}, nil); !errors.Is(err, source.ErrRateLimited) || g.apiCalls() != 1 {
		t.Fatalf("issues during the lookup's backoff: %v, %d calls", err, g.apiCalls())
	}
}

func TestOpenIssuesSaysALostLogin(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 4, Stderr: []byte("To get started with GitHub CLI, please run:  gh auth login")}
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	if _, err := p.OpenIssues(context.Background(), []model.ForgeRepo{model.GitHubRepo("sample-org/alpha")}, nil); !errors.Is(err, source.ErrNotLoggedIn) {
		t.Fatalf("err = %v, want not logged in", err)
	}
}

func manyRepos(n int) []model.ForgeRepo {
	var repos []model.ForgeRepo
	for i := range n {
		repos = append(repos, model.GitHubRepo(fmt.Sprintf("sample-org/sample-%02d", i)))
	}
	return repos
}

func TestALimitThatArrivesBetweenChunksHoldsTheRestOfTheLook(t *testing.T) {
	var p *provider
	f := &fakeGh{reply: func(args []string) ([]byte, error) {
		q, _ := argValue(args, "query")
		// A pull request lookup of another repository is rate limited while chunk one runs
		// (written in another case: GitHub ignores it).
		p.mu.Lock()
		p.backoff[backoffKey("Sample-Org/Sample-24")] = issuesAt.Add(time.Hour)
		p.mu.Unlock()
		return issuesAnswer(strings.Count(q, "repository(")), nil
	}}
	p = newTestProvider(f, &clock{t: issuesAt})
	got, err := p.OpenIssues(context.Background(), manyRepos(25), nil)
	if f.apiCalls() != 2 {
		t.Fatalf("gh calls = %d, want 2: chunk two asks only for the repositories not in a backoff", f.apiCalls())
	}
	q, _ := argValue(f.calls[len(f.calls)-1], "query")
	if err != nil || len(got) != 24 || strings.Count(q, "repository(") != 4 {
		t.Fatalf("got %d rows, %v, %d repositories in chunk two; want 24 rows, none for the one in a backoff", len(got), err, strings.Count(q, "repository("))
	}
}

func TestALimitOnALaterChunkHoldsPullRequestLookupsOfEarlierRepositories(t *testing.T) {
	calls := 0
	f := &fakeGh{reply: func(args []string) ([]byte, error) {
		q, _ := argValue(args, "query")
		calls++
		if calls == 2 {
			return nil, &runner.ExitError{Code: 1, Stderr: []byte("gh: API rate limit exceeded for user. (HTTP 403)")}
		}
		return issuesAnswer(strings.Count(q, "repository(")), nil
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	repos := manyRepos(25)
	got, err := p.OpenIssues(context.Background(), repos, nil)
	if !errors.Is(err, source.ErrRateLimited) || len(got) != 20 {
		t.Fatalf("got %d rows, %v; want the first 20 and a rate limit", len(got), err)
	}
	before := f.apiCalls()
	_, _ = p.Lookup(context.Background(), model.GitHubRepo("Sample-Org/sample-00"), []int{12}, nil)
	if f.apiCalls() != before {
		t.Fatalf("a pull request lookup of a repository answered before the limit ran during the backoff")
	}
}

func TestAnAliasWithAnErrorUnderItIsNotAnAnswer(t *testing.T) {
	answer := `{"data":{
	  "r0":{"hasIssuesEnabled":true,"issues":{"totalCount":2,"nodes":[{"number":3,"title":"Sample issue","updatedAt":"2026-11-23T08:00:00Z","milestone":null,"labels":{"nodes":[]}}]}},
	  "r1":{"hasIssuesEnabled":true,"issues":{"totalCount":9,"nodes":[null]}},
	  "r2":null},
	 "errors":[{"type":"SERVICE_UNAVAILABLE","path":["r1","issues","nodes",0]},{"type":"NOT_FOUND","path":["r2","issues"]}]}`
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte(answer)}
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	a := model.GitHubRepo("sample-org/alpha")
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{a, model.GitHubRepo("sample-org/beta"), model.GitHubRepo("sample-org/gamma")}, nil)
	if err != nil || len(got) != 1 || got[0].Repo != a {
		t.Fatalf("got %+v, %v; want only the complete alias: a nested error, and a NOT_FOUND below the repository, are no answers", got, err)
	}
	if p.health != source.StateFailing {
		t.Fatalf("health = %q after an incomplete answer, want failing", p.health)
	}
}

func TestARepositoryThatIsNotFoundAndHurtAtOnceIsNotAnAnswer(t *testing.T) {
	answer := `{"data":{
	  "r0":null,
	  "r1":{"hasIssuesEnabled":true,"issues":{"totalCount":1,"nodes":[{"number":3,"title":"Sample issue","updatedAt":"2026-11-23T08:00:00Z","milestone":null,"labels":{"nodes":[]}}]}}},
	 "errors":[{"type":"NOT_FOUND","path":["r0"]},{"type":"FORBIDDEN","path":["r0"]}]}`
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte(answer)}
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	b := model.GitHubRepo("sample-org/beta")
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{model.GitHubRepo("sample-org/alpha"), b}, nil)
	if err != nil || len(got) != 1 || got[0].Repo != b {
		t.Fatalf("got %+v, %v; want only the complete alias: a NOT_FOUND with another error on the same repository is no verdict that it is gone", got, err)
	}
}

func TestALostLoginAfterAGoodChunkReturnsNoRows(t *testing.T) {
	calls := 0
	f := &fakeGh{reply: func(args []string) ([]byte, error) {
		q, _ := argValue(args, "query")
		calls++
		if calls == 2 {
			return nil, &runner.ExitError{Code: 4, Stderr: []byte("To get started with GitHub CLI, please run:  gh auth login")}
		}
		return issuesAnswer(strings.Count(q, "repository(")), nil
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	got, err := p.OpenIssues(context.Background(), manyRepos(25), nil)
	if !errors.Is(err, source.ErrNotLoggedIn) || len(got) != 0 {
		t.Fatalf("got %d rows, %v; want none: the rows of a login that is gone are dropped", len(got), err)
	}
}

// Rule: a repository in a backoff of its own never keeps the other
// repositories of the same look from being asked.
func TestAChunkWhoseRepositoriesAllCoolDownDoesNotEndTheLook(t *testing.T) {
	f := &fakeGh{reply: func(args []string) ([]byte, error) {
		q, _ := argValue(args, "query")
		return issuesAnswer(strings.Count(q, "repository(")), nil
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	repos := manyRepos(21)
	p.mu.Lock()
	for _, r := range repos[:issueReposPerCall] {
		p.backoff[backoffKey(r.Path)] = issuesAt.Add(time.Hour)
	}
	p.mu.Unlock()
	got, err := p.OpenIssues(context.Background(), repos, nil)
	if err != nil || len(got) != 1 || got[0].Repo != repos[20] || f.apiCalls() != 1 {
		t.Fatalf("got %d rows, %v, %d gh calls; want repository 21 asked and answered once", len(got), err, f.apiCalls())
	}
	if p.health != source.StateOK {
		t.Fatalf("health = %q, want ok", p.health)
	}
}

// Rule: the health after a look says how the whole look went; a later chunk
// that answered in full does not undo an earlier one that did not.
func TestALaterGoodChunkDoesNotHideAnEarlierIncompleteOne(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first func(n int) ([]byte, error)
	}{
		{"partial", func(n int) ([]byte, error) {
			// r0 answers, r1 is hurt: an incomplete answer that keeps r0.
			body := strings.Replace(string(issuesAnswer(n)), `{"data":{`, `{"errors":[{"type":"SERVICE_UNAVAILABLE","path":["r1"]}],"data":{`, 1)
			return nil, &runner.ExitError{Code: 1, Stdout: []byte(body)}
		}},
		{"failed", func(int) ([]byte, error) {
			return nil, &runner.ExitError{Code: 1, Stderr: []byte("HTTP 502")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			f := &fakeGh{reply: func(args []string) ([]byte, error) {
				q, _ := argValue(args, "query")
				calls++
				if calls == 1 {
					return tc.first(strings.Count(q, "repository("))
				}
				return issuesAnswer(strings.Count(q, "repository(")), nil
			}}
			p := newTestProvider(f, &clock{t: issuesAt})
			repos := manyRepos(25)
			got, err := p.OpenIssues(context.Background(), repos, nil)
			if err != nil || f.apiCalls() != 2 {
				t.Fatalf("%v, %d gh calls; want both chunks asked and the rows kept", err, f.apiCalls())
			}
			if tc.name == "partial" && (len(got) != 24 || got[0].Repo != repos[0]) {
				t.Fatalf("got %d rows; want the 19 whole answers of chunk one and the 5 of chunk two", len(got))
			}
			if p.health != source.StateFailing {
				t.Fatalf("health = %q after a look with a %s chunk, want failing", p.health, tc.name)
			}
		})
	}
}

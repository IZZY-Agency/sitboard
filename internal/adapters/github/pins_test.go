package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// pinnedFixture is GitHub's real answer to buildIssuesQuery with pins
// (gh 2.106.0, 27 Nov 2026, a public repository) with placeholder text:
// r0 asked 301 (open), 302 (closed), 303 (a pull request's number) and
// 99999999 (no such number); the last two are null with a NOT_FOUND error
// whose path names the pin alias under r0. gh exits 1 with that answer.
func pinnedFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fixtures", "gh", "2.106.0", "open-issues-pinned.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOpenIssuesReadsGitHubsRealPinnedAnswer(t *testing.T) {
	var query string
	f := &fakeGh{reply: func(args []string) ([]byte, error) {
		query, _ = argValue(args, "query")
		return nil, &runner.ExitError{Code: 1, Stdout: pinnedFixture(t), Stderr: []byte("gh: Could not resolve to an Issue with the number of 303.")}
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	a, b := model.GitHubRepo("sample-org/alpha"), model.GitHubRepo("sample-org/beta")
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{a, b},
		map[model.ForgeRepo][]int{a: {301, 302, 303, 99999999, 0, -4, 1 << 31, 301}, model.GitHubRepo("sample-org/elsewhere"): {5}})
	if err != nil || len(got) != 2 || f.apiCalls() != 1 {
		t.Fatalf("%+v %v, %d calls", got, err, f.apiCalls())
	}
	for _, want := range []string{" i301:issue(number:301){state number title", " i302:issue(number:302){", " i99999999:issue(number:99999999){"} {
		if !strings.Contains(query, want) {
			t.Fatalf("query lacks %q: %s", want, query)
		}
	}
	if strings.Contains(query, "sample-org") || strings.Contains(query, "i0:") || strings.Contains(query, "i-4") || strings.Contains(query, "2147483648") || strings.Count(query, "i301:") != 1 {
		t.Fatalf("only valid numbers, once each, and no names go into the query: %s", query)
	}
	r0, r1 := got[0], got[1]
	if r0.State != model.RepoIssuesOK || r0.Open != 42 || len(r0.List) != 1 || r1.State != model.RepoIssuesOK || r1.Pinned != nil {
		t.Fatalf("repositories %+v / %+v", r0, r1)
	}
	if len(r0.Pinned) != 4 {
		t.Fatalf("pinned %+v", r0.Pinned)
	}
	open, closed, pr, none := r0.Pinned[0], r0.Pinned[1], r0.Pinned[2], r0.Pinned[3]
	if open.Number != 301 || open.State != model.PinOpen || open.Title != "Placeholder issue 301" || len(open.Labels) != 3 || open.UpdatedAt.IsZero() {
		t.Fatalf("open %+v", open)
	}
	if closed.Number != 302 || closed.State != model.PinClosed || closed.Title != "Placeholder issue 302" {
		t.Fatalf("closed %+v", closed)
	}
	if pr.Number != 303 || pr.State != model.PinGone || none.Number != 99999999 || none.State != model.PinGone || pr.Title != "" {
		t.Fatalf("gone %+v %+v", pr, none)
	}
}

// An error of a pin's own leaves that pin unanswered and its repository's
// answer standing; a null with no error, or an answer about another number,
// is no answer either.
func TestAPinsOwnErrorLeavesOnlyItUnanswered(t *testing.T) {
	answer := `{"data":{"r0":{"hasIssuesEnabled":true,"issues":{"totalCount":1,"nodes":[{"number":1,"title":"Sample","updatedAt":"2026-11-20T09:00:00Z","milestone":null,"labels":{"nodes":[]}}]},
	  "i5":null,"i6":{"state":"OPEN","number":6,"title":"x","updatedAt":"2026-11-20T09:00:00Z","milestone":null,"labels":null},"i7":null,"i8":{"state":"OPEN","number":9,"title":"x","updatedAt":"2026-11-20T09:00:00Z","milestone":null,"labels":{"nodes":[]}},
	  "i10":{"state":"MERGED","number":10,"title":"x","updatedAt":"2026-11-20T09:00:00Z","milestone":null,"labels":{"nodes":[]}},
	  "i11":{"state":"CLOSED","number":11,"title":"Sample","updatedAt":"2026-11-20T09:00:00Z","milestone":null,"labels":{"nodes":[]}}}},
	 "errors":[{"type":"FORBIDDEN","path":["r0","i5"]},{"type":"NOT_FOUND","path":["r0","i6","labels"]}]}`
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte(answer), Stderr: []byte("gh: failed")}
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	a := model.GitHubRepo("sample-org/alpha")
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{a}, map[model.ForgeRepo][]int{a: {5, 6, 7, 8, 10, 11}})
	if err != nil || len(got) != 1 || got[0].State != model.RepoIssuesOK || len(got[0].List) != 1 {
		t.Fatalf("the repository's answer stands: %+v %v", got, err)
	}
	if ps := got[0].Pinned; len(ps) != 1 || ps[0].Number != 11 || ps[0].State != model.PinClosed {
		t.Fatalf("pinned %+v", ps)
	}
}

// A repository with no pins is asked as before: no pin alias, no Pinned.
func TestNoPinsAsksAsBefore(t *testing.T) {
	var query string
	f := &fakeGh{reply: func(args []string) ([]byte, error) {
		query, _ = argValue(args, "query")
		return issuesAnswer(1), nil
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{model.GitHubRepo("sample-org/alpha")}, nil)
	if err != nil || len(got) != 1 || got[0].Pinned != nil || strings.Contains(query, "issue(number:") {
		t.Fatalf("%+v %v %s", got, err, query)
	}
}

// A pin alias that does not decode is that pin's alone: its repository's
// list stands, and so do the other repositories of the call.
func TestAMalformedPinLeavesTheListStanding(t *testing.T) {
	row := `{"number":1,"title":"Sample","updatedAt":"2026-11-20T09:00:00Z","milestone":null,"labels":{"nodes":[]}}`
	answer := `{"data":{"r0":{"hasIssuesEnabled":true,"issues":{"totalCount":1,"nodes":[` + row + `]},
	  "i5":{"state":"OPEN","number":"five","title":"x"},"i6":7,"i7":{"state":"CLOSED","number":7,"title":"Sample","updatedAt":"2026-11-20T09:00:00Z","milestone":null,"labels":{"nodes":[]}}},
	 "r1":{"hasIssuesEnabled":true,"issues":{"totalCount":1,"nodes":[` + row + `]}}}}`
	f := &fakeGh{reply: func([]string) ([]byte, error) { return []byte(answer), nil }}
	p := newTestProvider(f, &clock{t: issuesAt})
	a, b := model.GitHubRepo("sample-org/alpha"), model.GitHubRepo("sample-org/beta")
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{a, b}, map[model.ForgeRepo][]int{a: {5, 6, 7}})
	if err != nil || len(got) != 2 {
		t.Fatalf("the call's answer stands: %+v %v", got, err)
	}
	for _, ri := range got {
		if ri.State != model.RepoIssuesOK || len(ri.List) != 1 {
			t.Fatalf("a repository's list was lost: %+v", ri)
		}
	}
	if ps := got[0].Pinned; len(ps) != 1 || ps[0].Number != 7 || ps[0].State != model.PinClosed {
		t.Fatalf("pinned %+v", ps)
	}
}

// Only a pin's own NOT_FOUND makes it gone. A repository the login no
// longer sees (null, NOT_FOUND or FORBIDDEN at its own alias) and one with
// issues turned off report no pin, whatever is said under them.
func TestOnlyAPinsOwnNotFoundIsGone(t *testing.T) {
	answer := `{"data":{"r0":null,"r1":null,"r2":{"hasIssuesEnabled":false,"issues":{"totalCount":0,"nodes":[]},"i5":null},
	 "r3":{"hasIssuesEnabled":true,"issues":{"totalCount":0,"nodes":[]},"i5":null}},
	 "errors":[{"type":"NOT_FOUND","path":["r0"]},{"type":"FORBIDDEN","path":["r1"]},{"type":"NOT_FOUND","path":["r2","i5"]},{"type":"NOT_FOUND","path":["r3","i5"]}]}`
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte(answer), Stderr: []byte("gh: failed")}
	}}
	p := newTestProvider(f, &clock{t: issuesAt})
	repos := []model.ForgeRepo{model.GitHubRepo("sample-org/alpha"), model.GitHubRepo("sample-org/beta"), model.GitHubRepo("sample-org/gamma"), model.GitHubRepo("sample-org/delta")}
	pinned := map[model.ForgeRepo][]int{}
	for _, r := range repos {
		pinned[r] = []int{5}
	}
	got, err := p.OpenIssues(context.Background(), repos, pinned)
	if err != nil {
		t.Fatal(err)
	}
	states := map[model.ForgeRepo]model.RepoIssuesState{}
	for _, ri := range got {
		states[ri.Repo] = ri.State
		if ri.Repo != repos[3] && ri.Pinned != nil {
			t.Fatalf("%s reported a pin: %+v", ri.Repo.Path, ri)
		}
	}
	if len(got) != 3 || states[repos[0]] != model.RepoIssuesNotFound || states[repos[2]] != model.RepoIssuesDisabled {
		t.Fatalf("answers %+v", got)
	}
	if ps := got[2].Pinned; got[2].Repo != repos[3] || len(ps) != 1 || ps[0].Number != 5 || ps[0].State != model.PinGone {
		t.Fatalf("delta's own NOT_FOUND: %+v", got[2])
	}
}

// No pin is gone beside an unscoped error (a rate limit, or one naming no
// alias of the call): the lists still stand. Nor is one gone that its
// repository's own list shows.
func TestNoPinIsGoneOnADoubtfulAnswer(t *testing.T) {
	a := model.GitHubRepo("sample-org/alpha")
	asks := []issueAsk{{repo: a, pins: []int{5, 6, 7}}}
	answer := func(extra string) []byte {
		return []byte(`{"data":{"r0":{"hasIssuesEnabled":true,"issues":{"totalCount":1,"nodes":[{"number":6,"title":"Sample","updatedAt":"2026-11-20T09:00:00Z","milestone":null,"labels":{"nodes":[]}}]},"i5":null,"i6":null,"i7":null}},
		 "errors":[{"type":"NOT_FOUND","path":["r0","i5"]},{"type":"NOT_FOUND","path":["r0","i6"]}` + extra + `]}`)
	}
	got, _, _, err := parseIssues(answer(""), asks)
	if err != nil || len(got) != 1 || len(got[0].Pinned) != 1 || got[0].Pinned[0].Number != 5 || got[0].Pinned[0].State != model.PinGone {
		t.Fatalf("5 is gone, 6 is listed: %+v %v", got, err)
	}
	for _, extra := range []string{`,{"type":"RATE_LIMITED","path":null}`, `,{"type":"RATE_LIMITED","path":["r0","i7"]}`, `,{"type":"INTERNAL","message":"sample"}`, `,{"type":"FORBIDDEN","path":["other"]}`} {
		got, _, _, err := parseIssues(answer(extra), asks)
		if err != nil || len(got) != 1 || got[0].State != model.RepoIssuesOK || len(got[0].List) != 1 {
			t.Fatalf("%s: the list stands: %+v %v", extra, got, err)
		}
		for _, ri := range got {
			if len(ri.Pinned) != 0 {
				t.Fatalf("%s: a pin reported %+v", extra, ri.Pinned)
			}
		}
	}
}

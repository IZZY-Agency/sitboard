package core

import (
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// cardPRs runs one session with the given created pull requests against the
// pull requests GitHub "answers", and returns the card's pull requests and issues.
func cardPRs(t *testing.T, refs []model.PRRef, answer []model.PullRequest) ([]contract.PullRequest, []contract.Issue) {
	t.Helper()
	e := newEnv(t)
	e.prs.prs = answer
	s := session("s1", model.StatusFinished, tNow)
	s.CreatedPRs = refs
	e.board.applySync(s)
	e.board.tick()
	c := onlyCard(t, e.board)
	return c.PullRequests, c.Issues
}

// The page gets a pull request's address only when it is the one built for
// that pull request's own repo and number on github.com.
func TestPullRequestAddressIsTheOneBuiltForIt(t *testing.T) {
	own := "https://github.com/example-org/example-repo/pull/101"
	for _, c := range []struct {
		name, url, want string
	}{
		{"built", own, own},
		{"none", "", ""},
		{"script", "javascript:alert(1)", ""},
		{"another host", "https://evil.example/example-org/example-repo/pull/101", ""},
		{"another pull request", "https://github.com/example-org/example-repo/pull/102", ""},
		{"another repo", "https://github.com/other-org/example-repo/pull/101", ""},
		{"a longer path", own + "/files", ""},
		{"a query", own + "?x=1", ""},
	} {
		prs, _ := cardPRs(t, []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 101}},
			[]model.PullRequest{{Repo: model.GitHubRepo(exRepo), Number: 101, URL: c.url, State: model.PRStateOpen}})
		if len(prs) != 1 || prs[0].URL != c.want {
			t.Errorf("%s: pull requests %+v, want url %q", c.name, prs, c.want)
		}
	}
}

// Issue addresses follow the same rule, from the pull request's repo and the
// issue number; a repo or a number that cannot make one gives none.
func TestIssueAddressIsBuiltOrNone(t *testing.T) {
	_, issues := cardPRs(t, []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 101}},
		[]model.PullRequest{{Repo: model.GitHubRepo(exRepo), Number: 101, State: model.PRStateOpen, ClosingIssues: []int{7, 0, -2}}})
	want := map[int]string{7: "https://github.com/example-org/example-repo/issues/7", 0: "", -2: ""}
	if len(issues) != 3 {
		t.Fatalf("issues %+v", issues)
	}
	for _, is := range issues {
		if is.URL != want[is.Number] {
			t.Errorf("issue %d url %q, want %q", is.Number, is.URL, want[is.Number])
		}
	}

	for _, bad := range []string{"example-org/..", "../example-repo", "example-org/example-repo/pull/9", "-x/example-repo", "example.org/example-repo", "example-org/.", "example-org/example repo", "example-org/x?y", "example-org/x#y", "example-org/x\"onmouseover=1"} {
		prs, issues := cardPRs(t, []model.PRRef{{Repo: model.GitHubRepo(bad), Number: 5}},
			[]model.PullRequest{{Repo: model.GitHubRepo(bad), Number: 5, URL: "https://github.com/" + bad + "/pull/5", State: model.PRStateOpen, ClosingIssues: []int{3}}})
		// Most of these never reach a card; the two that do must carry no link.
		if (bad == "-x/example-repo" || bad == "example.org/example-repo") && (len(prs) != 1 || len(issues) != 1) {
			t.Fatalf("repo %q: expected a pull request and an issue on the card: %+v %+v", bad, prs, issues)
		}
		for _, pr := range prs {
			if pr.URL != "" {
				t.Errorf("repo %q: pull request url %q", bad, pr.URL)
			}
		}
		for _, is := range issues {
			if is.URL != "" {
				t.Errorf("repo %q: issue url %q", bad, is.URL)
			}
		}
	}
}

package github

import (
	"context"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// The fields Leftovers relies on: the head commit GitHub keeps after the
// branch is deleted, whether the base is the default branch, and where the
// head branch on GitHub points now.
func TestLeftoverFields(t *testing.T) {
	head := strings.Repeat("a", 40)
	now := strings.Repeat("b", 40)
	answer := `{"data":{"repository":{
		"defaultBranchRef":{"name":"main"},
		"b0":{"nodes":[
			{"number":7,"title":"t","headRefName":"fix","headRefOid":"` + head + `","headRef":{"target":{"oid":"` + now + `"}},"baseRefName":"main","state":"MERGED","createdAt":"2026-11-01T00:00:00Z","mergedAt":"2026-11-02T00:00:00Z"},
			{"number":6,"title":"t","headRefName":"fix","headRefOid":"not-an-oid","headRef":null,"baseRefName":"release","state":"MERGED","createdAt":"2026-10-01T00:00:00Z","mergedAt":"2026-10-02T00:00:00Z"}
		]}}}}`
	var query string
	p := New(Options{}).(*provider)
	p.found = func(string) string { return "/bin/gh" }
	p.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		for _, a := range args {
			if q, ok := strings.CutPrefix(a, "query="); ok {
				query = q
			}
		}
		return []byte(answer), nil
	}
	prs, err := p.Lookup(context.Background(), model.GitHubRepo("example-org/example-repo"), nil, []string{"fix"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"headRefOid", "headRef{target{oid}}", "baseRefName", "defaultBranchRef{name}"} {
		if !strings.Contains(query, f) {
			t.Errorf("query does not ask for %s", f)
		}
	}
	by := map[int]model.PullRequest{}
	for _, pr := range prs {
		by[pr.Number] = pr
	}
	if pr := by[7]; pr.HeadOID != head || pr.HeadRefOID != now || !pr.BaseIsDefault || pr.BaseBranch != "main" {
		t.Errorf("#7 = %+v", pr)
	}
	if pr := by[6]; pr.HeadOID != "" || pr.HeadRefOID != "" || pr.BaseIsDefault {
		t.Errorf("#6: a malformed id, a deleted branch and another base must read as unknown, gone and not default: %+v", pr)
	}
}

// Without a default branch in the answer no pull request targets it.
func TestNoDefaultBranchMeansNotDefault(t *testing.T) {
	for _, base := range []string{"main", ""} {
		pr, ok := toModel(&ghPR{Number: 1, State: "MERGED", BaseRefName: base}, "example-org/example-repo", "")
		if !ok || pr.BaseIsDefault {
			t.Fatalf("base %q: pr = %+v", base, pr)
		}
	}
}

// A fork's pull request (found by number) names a head commit and branch of
// another repository: it carries neither, so Leftovers never matches it to a
// branch of the same name here.
func TestForkPullRequestHasNoHeadCommit(t *testing.T) {
	head := strings.Repeat("a", 40)
	g := &ghPR{Number: 3, State: "MERGED", HeadRefName: "main", HeadRefOid: head, BaseRefName: "main", CrossRepo: true}
	g.HeadRef = &struct {
		Target *struct {
			Oid string `json:"oid"`
		} `json:"target"`
	}{Target: &struct {
		Oid string `json:"oid"`
	}{Oid: head}}
	pr, ok := toModel(g, "example-org/example-repo", "main")
	if !ok || pr.HeadOID != "" || pr.HeadRefOID != "" {
		t.Fatalf("fork: %+v", pr)
	}
	g.CrossRepo = false
	if pr, _ := toModel(g, "example-org/example-repo", "main"); pr.HeadOID != head || pr.HeadRefOID != head {
		t.Fatalf("same repository: %+v", pr)
	}
}

package github

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

var headA = strings.Repeat("a", 40)

// evNode is one pull request in an evidence answer.
func evNode(number int, head, state string, cross any) map[string]any {
	n := map[string]any{
		"number": number, "title": "Sample", "headRefName": head, "headRefOid": headA,
		"headRef": map[string]any{"target": map[string]any{"oid": headA}}, "baseRefName": "main",
		"state": state, "isDraft": false, "createdAt": "2026-11-01T10:00:00Z", "closedAt": "2026-11-02T10:00:00Z",
		"mergedAt": "2026-11-02T10:00:00Z", "reviewDecision": nil,
		"closingIssuesReferences": map[string]any{"nodes": []any{}}, "commits": map[string]any{"nodes": []any{}},
	}
	if cross != "missing" {
		n["isCrossRepository"] = cross
	}
	return n
}

// evAnswer is gh's answer with one connection per head, in order, and no
// open pull request built on any head.
func evAnswer(t *testing.T, conns ...map[string]any) []byte {
	t.Helper()
	return evAnswerBases(t, nil, conns...)
}

// evAnswerBases is evAnswer where head i is the base of bases[i] open pull
// requests.
func evAnswerBases(t *testing.T, bases map[int]int, conns ...map[string]any) []byte {
	t.Helper()
	repo := map[string]any{"defaultBranchRef": map[string]any{"name": "main"}}
	for i, c := range conns {
		repo["b"+string(rune('0'+i))] = c
		repo["o"+string(rune('0'+i))] = map[string]any{"totalCount": bases[i]}
	}
	b, err := json.Marshal(map[string]any{"data": map[string]any{"repository": repo}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func conn(more bool, nodes ...any) map[string]any {
	return map[string]any{"pageInfo": map[string]any{"hasNextPage": more}, "nodes": nodes}
}

// Every evidence question goes to GitHub: none is answered from the cache.
func TestEvidenceIsNeverCached(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		return evAnswer(t, conn(false, evNode(5, "feat", "MERGED", false))), nil
	}}
	p := newTestProvider(f, &clock{t: time.Now()})
	for range 2 {
		ev, err := p.Evidence(context.Background(), model.GitHubRepo(repo), []string{"feat"})
		if err != nil || len(ev.PRs) != 1 || ev.PRs[0].State != model.PRStateMerged || len(ev.Incomplete) != 0 {
			t.Fatalf("evidence %+v, %v", ev, err)
		}
	}
	if n := f.apiCalls(); n != 2 {
		t.Fatalf("%d gh calls, want 2", n)
	}
	q := ""
	for _, a := range f.calls[0] {
		if s, ok := strings.CutPrefix(a, "query="); ok {
			q = s
		}
	}
	if !strings.Contains(q, "pageInfo{hasNextPage}") || !strings.Contains(q, "first:10") || strings.Contains(q, "feat") {
		t.Fatalf("query: %s", q)
	}
}

// A rate limit, and the back-off after it, give no evidence, never the last answer.
func TestEvidenceIsNeverStale(t *testing.T) {
	limited := false
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		if limited {
			return nil, &runner.ExitError{Code: 1, Stderr: []byte("gh: API rate limit exceeded for user. (HTTP 403)")}
		}
		return evAnswer(t, conn(false, evNode(5, "feat", "MERGED", false))), nil
	}}
	c := &clock{t: time.Now()}
	p := newTestProvider(f, c)
	ctx := context.Background()
	if _, err := p.Evidence(ctx, model.GitHubRepo(repo), []string{"feat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Lookup(ctx, model.GitHubRepo(repo), nil, []string{"feat"}); err != nil {
		t.Fatal(err) // fills the board's cache, which evidence must not use
	}
	limited = true
	if ev, err := p.Evidence(ctx, model.GitHubRepo(repo), []string{"feat"}); err == nil {
		t.Fatalf("rate limited: %+v", ev)
	}
	calls := f.apiCalls()
	if ev, err := p.Evidence(ctx, model.GitHubRepo(repo), []string{"feat"}); err == nil || f.apiCalls() != calls {
		t.Fatalf("during the back-off: %+v %v, %d calls", ev, err, f.apiCalls()-calls)
	}
}

// A head that may have more pull requests than the answer shows is marked:
// more than one page, a null entry, an entry without its fork flag, or one
// for another head. A fork's pull request is left out, not doubted.
func TestEvidenceMarksIncompleteHeads(t *testing.T) {
	// The names in sorted order, as cleanBranches sends them: b0 is "done".
	sorted := []string{"done", "forked", "hole", "many", "nofork", "odd", "other"}
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		return evAnswer(t,
			conn(false, evNode(1, "done", "MERGED", false)),
			conn(false, evNode(6, "forked", "OPEN", true), evNode(7, "forked", "MERGED", false)),
			conn(false, evNode(3, "hole", "MERGED", false), nil),
			conn(true, evNode(2, "many", "MERGED", false)),
			conn(false, evNode(4, "nofork", "MERGED", "missing")),
			conn(false, evNode(8, "odd", "QUEUED", false)),
			conn(false, evNode(5, "elsewhere", "MERGED", false)),
		), nil
	}}
	p := newTestProvider(f, &clock{t: time.Now()})
	ev, err := p.Evidence(context.Background(), model.GitHubRepo(repo), sorted)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"many": true, "hole": true, "nofork": true, "other": true, "odd": true}
	if len(ev.Incomplete) != len(want) {
		t.Errorf("incomplete %v, want %v", ev.Incomplete, want)
	}
	for h := range want {
		if !ev.Incomplete[h] {
			t.Errorf("%s: not marked incomplete", h)
		}
	}
	got := map[int]bool{}
	for _, pr := range ev.PRs {
		got[pr.Number] = true
	}
	if !got[1] || !got[7] || got[6] || got[5] || got[8] {
		t.Errorf("pull requests %v", got)
	}
}

// Any error GitHub reports, or a part of the answer missing, is no evidence.
func TestEvidenceFailsOnAnyError(t *testing.T) {
	good := func(t *testing.T) []byte { return evAnswer(t, conn(false, evNode(1, "feat", "MERGED", false))) }
	cases := map[string]func(t *testing.T) ([]byte, error){
		"errors beside data": func(t *testing.T) ([]byte, error) {
			var m map[string]any
			_ = json.Unmarshal(good(t), &m)
			m["errors"] = []any{map[string]any{"message": "something failed", "path": []any{"repository", "b0"}}}
			b, _ := json.Marshal(m)
			return b, nil
		},
		"partial answer with an exit code": func(t *testing.T) ([]byte, error) {
			return nil, &runner.ExitError{Code: 1, Stdout: good(t), Stderr: []byte("gh: something failed")}
		},
		"head missing": func(t *testing.T) ([]byte, error) {
			return []byte(`{"data":{"repository":{"defaultBranchRef":{"name":"main"},"o0":{"totalCount":0}}}}`), nil
		},
		"no page info": func(t *testing.T) ([]byte, error) {
			return []byte(`{"data":{"repository":{"b0":{"nodes":[]},"o0":{"totalCount":0}}}}`), nil
		},
		"null connection": func(t *testing.T) ([]byte, error) {
			return []byte(`{"data":{"repository":{"b0":null,"o0":{"totalCount":0}}}}`), nil
		},
		"base count missing": func(t *testing.T) ([]byte, error) {
			return []byte(`{"data":{"repository":{"b0":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}`), nil
		},
		"base count null": func(t *testing.T) ([]byte, error) {
			return []byte(`{"data":{"repository":{"b0":{"pageInfo":{"hasNextPage":false},"nodes":[]},"o0":{"totalCount":null}}}}`), nil
		},
		"no repository": func(t *testing.T) ([]byte, error) { return []byte(`{"data":{"repository":null}}`), nil },
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeGh{reply: func([]string) ([]byte, error) { return reply(t) }}
			p := newTestProvider(f, &clock{t: time.Now()})
			if ev, err := p.Evidence(context.Background(), model.GitHubRepo(repo), []string{"feat"}); err == nil {
				t.Fatalf("evidence %+v", ev)
			}
		})
	}
}

func TestEvidenceOffNeverRunsGh(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) { t.Fatal("gh ran"); return nil, nil }}
	p := newTestProvider(f, &clock{t: time.Now()})
	p.off = true
	if _, err := p.Evidence(context.Background(), model.GitHubRepo(repo), []string{"feat"}); err == nil {
		t.Fatal("off answered")
	}
}

// The question names every state outright (open, closed and merged), so an
// older open pull request on a head comes back beside a newer merged one,
// and asks which heads open pull requests are built on.
func TestEvidenceAsksEveryStateAndBases(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		older := evNode(4, "feat", "OPEN", false)
		older["createdAt"], older["closedAt"], older["mergedAt"] = "2026-10-01T10:00:00Z", nil, nil
		return evAnswerBases(t, map[int]int{1: 2},
			conn(false, evNode(9, "feat", "MERGED", false), older),
			conn(false, evNode(3, "stacked", "MERGED", false)),
		), nil
	}}
	p := newTestProvider(f, &clock{t: time.Now()})
	ev, err := p.Evidence(context.Background(), model.GitHubRepo(repo), []string{"feat", "stacked"})
	if err != nil {
		t.Fatal(err)
	}
	q := ""
	for _, a := range f.calls[0] {
		if s, ok := strings.CutPrefix(a, "query="); ok {
			q = s
		}
	}
	for _, want := range []string{"headRefName:$b0,states:[OPEN,CLOSED,MERGED]", "headRefName:$b1,states:[OPEN,CLOSED,MERGED]",
		"o0:pullRequests(baseRefName:$b0,states:[OPEN]", "o1:pullRequests(baseRefName:$b1,states:[OPEN]"} {
		if !strings.Contains(q, want) {
			t.Errorf("query does not ask %s: %s", want, q)
		}
	}
	state := map[int]model.PRState{}
	for _, pr := range ev.PRs {
		state[pr.Number] = pr.State
	}
	if state[4] != model.PRStateOpen || state[9] != model.PRStateMerged || len(ev.Incomplete) != 0 {
		t.Errorf("pull requests %v, incomplete %v", state, ev.Incomplete)
	}
	if !ev.Bases["stacked"] || ev.Bases["feat"] {
		t.Errorf("bases %v", ev.Bases)
	}
}

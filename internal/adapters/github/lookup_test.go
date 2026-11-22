package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

const repo = "example-org/example-repo"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fixtures", "gh", "2.104.0", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeGh records every call and answers with reply.
type fakeGh struct {
	mu    sync.Mutex
	calls [][]string
	reply func(args []string) ([]byte, error) // gh api
	auth  error                               // gh auth status
}

func (f *fakeGh) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	f.mu.Unlock()
	switch args[0] {
	case "--version":
		return []byte("gh version 2.104.0 (2026-11-12)\n"), nil
	case "auth":
		return nil, f.auth
	}
	return f.reply(args)
}

func (f *fakeGh) apiCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == "api" {
			n++
		}
	}
	return n
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestProvider(f *fakeGh, c *clock) *provider {
	p := New(Options{Bin: "gh"}).(*provider)
	p.run = f.run
	p.now = c.now
	p.found = func(string) string { return "/usr/bin/gh" }
	return p
}

func TestLookupByNumberAndByBranchInOneCall(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) { return fixture(t, "lookup.json"), nil }}
	p := newTestProvider(f, &clock{t: time.Now()})
	prs, err := p.Lookup(context.Background(), model.GitHubRepo(repo), []int{102, 101, 101}, []string{"worker-c", "worker-d"})
	if err != nil {
		t.Fatal(err)
	}
	if f.apiCalls() != 1 {
		t.Fatalf("gh api calls = %d, want 1", f.apiCalls())
	}
	var nums []int
	for _, pr := range prs {
		nums = append(nums, pr.Number)
	}
	slices.Sort(nums)
	if !slices.Equal(nums, []int{101, 102, 103, 104}) {
		t.Fatalf("numbers = %v (duplicates must be merged)", nums)
	}

	args := f.calls[0]
	if args[0] != "api" || args[1] != "graphql" || args[2] != "--hostname" || args[3] != "github.com" {
		t.Fatalf("args = %v", args)
	}
	// Untrusted values travel only as raw string variables, never in the query text.
	joined := strings.Join(args, "\x00")
	for _, want := range []string{"owner=example-org", "name=example-repo", "b0=worker-c", "b1=worker-d"} {
		if !strings.Contains(joined, "\x00"+want) {
			t.Errorf("missing raw field %q in %v", want, args)
		}
	}
	for i, a := range args {
		if a == "-F" || a == "--field" {
			t.Errorf("typed field flag used at %d: %v", i, args)
		}
		if strings.HasPrefix(a, "query=") && (strings.Contains(a, "worker-c") || strings.Contains(a, "example-org")) {
			t.Errorf("untrusted value interpolated into the query")
		}
	}
}

func TestMapsChecks(t *testing.T) {
	cases := map[string]model.Checks{
		"SUCCESS": model.ChecksPassing, "FAILURE": model.ChecksFailing, "ERROR": model.ChecksFailing,
		"PENDING": model.ChecksRunning, "EXPECTED": model.ChecksRunning, "": model.ChecksNone,
		"SOMETHING_NEW": model.ChecksUnknown,
	}
	for in, want := range cases {
		if got := mapChecks(in); got != want {
			t.Errorf("mapChecks(%q) = %q, want %q", in, got, want)
		}
	}
	f := &fakeGh{reply: func([]string) ([]byte, error) { return fixture(t, "lookup.json"), nil }}
	prs, _ := newTestProvider(f, &clock{t: time.Now()}).Lookup(context.Background(), model.GitHubRepo(repo), []int{101, 102}, []string{"worker-c", "worker-d"})
	got := map[int]model.Checks{}
	for _, pr := range prs {
		got[pr.Number] = pr.Checks
	}
	want := map[int]model.Checks{101: model.ChecksFailing, 102: model.ChecksPassing, 103: model.ChecksRunning, 104: model.ChecksNone}
	for n, c := range want {
		if got[n] != c {
			t.Errorf("#%d checks = %q, want %q", n, got[n], c)
		}
	}
}

func TestMapsStateAndClosingIssues(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) { return fixture(t, "lookup.json"), nil }}
	prs, err := newTestProvider(f, &clock{t: time.Now()}).Lookup(context.Background(), model.GitHubRepo(repo), []int{101, 102}, []string{"worker-c", "worker-d"})
	if err != nil {
		t.Fatal(err)
	}
	by := map[int]model.PullRequest{}
	for _, pr := range prs {
		by[pr.Number] = pr
	}
	a := by[101]
	if a.State != model.PRStateOpen || a.Review != model.ReviewRequired || !slices.Equal(a.ClosingIssues, []int{7}) ||
		a.Repo != model.GitHubRepo(repo) || a.HeadBranch != "worker-a" || !a.ClosedAt.IsZero() || a.OpenedAt.IsZero() {
		t.Errorf("#101 = %+v", a)
	}
	b := by[102]
	if b.State != model.PRStateMerged || b.Review != model.ReviewApproved || b.ClosedAt.IsZero() {
		t.Errorf("#102 = %+v", b)
	}
	c := by[103]
	if c.State != model.PRStateClosed || !c.Draft || c.Review != model.ReviewNone {
		t.Errorf("#103 = %+v", c)
	}
}

func TestPartialNotFoundKeepsTheRest(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: fixture(t, "partial-not-found.json"), Stderr: []byte("gh: Could not resolve to a PullRequest")}
	}}
	p := newTestProvider(f, &clock{t: time.Now()})
	prs, err := p.Lookup(context.Background(), model.GitHubRepo(repo), []int{102, 999}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].Number != 102 || prs[0].Checks != model.ChecksFailing {
		t.Fatalf("prs = %+v", prs)
	}
	if st := p.Probe(context.Background()); st.State != source.StateOK {
		t.Fatalf("partial answer must not mark GitHub failing: %+v", st)
	}
}

func TestSecondLookupWithin60sIsServedFromCache(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) { return fixture(t, "lookup.json"), nil }}
	c := &clock{t: time.Now()}
	p := newTestProvider(f, c)
	ctx := context.Background()
	if _, err := p.Lookup(ctx, model.GitHubRepo(repo), []int{101, 102}, []string{"worker-c", "worker-d"}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(59 * time.Second)
	prs, err := p.Lookup(ctx, model.GitHubRepo(repo), []int{102, 101}, []string{"worker-d", "worker-c"})
	if err != nil || len(prs) != 4 {
		t.Fatalf("cached lookup = %d prs, %v", len(prs), err)
	}
	if f.apiCalls() != 1 {
		t.Fatalf("api calls = %d, want 1", f.apiCalls())
	}
	c.t = c.t.Add(2 * time.Second)
	if _, err := p.Lookup(ctx, model.GitHubRepo(repo), []int{101, 102}, []string{"worker-c", "worker-d"}); err != nil {
		t.Fatal(err)
	}
	if f.apiCalls() != 2 {
		t.Fatalf("api calls after 61s = %d, want 2", f.apiCalls())
	}
}

func TestOffNeverRunsGh(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) { t.Fatal("gh ran while off"); return nil, nil }}
	p := New(Options{Off: true}).(*provider)
	p.run = f.run
	if st := p.Probe(context.Background()); st.State != source.StateOff {
		t.Fatalf("Probe = %+v", st)
	}
	prs, err := p.Lookup(context.Background(), model.GitHubRepo(repo), []int{1}, []string{"x"})
	if err != nil || prs != nil {
		t.Fatalf("Lookup while off = %v, %v", prs, err)
	}
}

func TestGhMissingReportsMissing(t *testing.T) {
	p := New(Options{Bin: filepath.Join(t.TempDir(), "gh")}).(*provider)
	if st := p.Probe(context.Background()); st.State != source.StateMissing || st.Kind != source.KindGitHub {
		t.Fatalf("Probe = %+v", st)
	}
	if _, err := p.Lookup(context.Background(), model.GitHubRepo(repo), []int{1}, nil); err == nil {
		t.Fatal("Lookup with gh missing must fail")
	}
}

func TestLoggedOutReportsFailing(t *testing.T) {
	loggedOut := &runner.ExitError{Code: 4, Stderr: []byte("To get started with GitHub CLI, please run:  gh auth login")}
	f := &fakeGh{auth: loggedOut, reply: func([]string) ([]byte, error) { return nil, loggedOut }}
	p := newTestProvider(f, &clock{t: time.Now()})
	st := p.Probe(context.Background())
	if st.State != source.StateFailing || st.Detail != "not logged in to github.com" || st.Version != "2.104.0" || !st.Tested {
		t.Fatalf("Probe = %+v", st)
	}
	if _, err := p.Lookup(context.Background(), model.GitHubRepo(repo), []int{1}, nil); err == nil {
		t.Fatal("Lookup while logged out must fail")
	}
}

func TestProbeOK(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) { return nil, nil }}
	st := newTestProvider(f, &clock{t: time.Now()}).Probe(context.Background())
	if st.State != source.StateOK || st.Version != "2.104.0" || !st.Tested {
		t.Fatalf("Probe = %+v", st)
	}
}

func TestSlowGhTimesOutAt10s(t *testing.T) {
	if callTimeout != 10*time.Second {
		t.Fatalf("callTimeout = %v, want 10s", callTimeout)
	}
	dir := t.TempDir()
	slow := filepath.Join(dir, "gh")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := New(Options{Bin: slow}).(*provider)
	p.timeout = 50 * time.Millisecond
	start := time.Now()
	_, err := p.Lookup(context.Background(), model.GitHubRepo(repo), []int{1}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if st := p.Probe(context.Background()); st.State != source.StateFailing {
		t.Fatalf("Probe after timeout = %+v", st)
	}
}

func TestRateLimitBacksOffAndKeepsLastResult(t *testing.T) {
	limited := false
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		if limited {
			return nil, &runner.ExitError{Code: 1, Stderr: []byte("gh: API rate limit exceeded for user. (HTTP 403)")}
		}
		return fixture(t, "lookup.json"), nil
	}}
	c := &clock{t: time.Now()}
	p := newTestProvider(f, c)
	ctx := context.Background()
	if _, err := p.Lookup(ctx, model.GitHubRepo(repo), []int{101, 102}, []string{"worker-c", "worker-d"}); err != nil {
		t.Fatal(err)
	}
	limited = true
	c.t = c.t.Add(2 * time.Minute)
	prs, err := p.Lookup(ctx, model.GitHubRepo(repo), []int{101, 102}, []string{"worker-c", "worker-d"})
	if err != nil || len(prs) != 4 {
		t.Fatalf("rate-limited lookup = %d prs, %v; want the last result", len(prs), err)
	}
	if st := p.Probe(ctx); st.State != source.StateFailing || st.Detail != "rate limited" {
		t.Fatalf("Probe = %+v", st)
	}
	calls := f.apiCalls()
	c.t = c.t.Add(time.Minute) // still inside the back-off
	if _, err := p.Lookup(ctx, model.GitHubRepo(repo), []int{101, 102}, []string{"worker-c", "worker-d"}); err != nil {
		t.Fatal(err)
	}
	if f.apiCalls() != calls {
		t.Fatal("gh was called during the back-off")
	}
	limited = false
	c.t = c.t.Add(rateLimitBackoff)
	if _, err := p.Lookup(ctx, model.GitHubRepo(repo), []int{101, 102}, []string{"worker-c", "worker-d"}); err != nil {
		t.Fatal(err)
	}
	if f.apiCalls() != calls+1 {
		t.Fatal("gh not called after the back-off")
	}
	if st := p.Probe(ctx); st.State != source.StateOK {
		t.Fatalf("Probe after recovery = %+v", st)
	}
}

func TestTitleBytesPassThroughUnchanged(t *testing.T) {
	hostile := "<script>alert(1)</script> \u202eevil\u202c " + strings.Repeat("x", 10000)
	enc, _ := json.Marshal(hostile)
	body := strings.Replace(string(fixture(t, "lookup.json")), `"Second thing"`, string(enc), 1)
	f := &fakeGh{reply: func([]string) ([]byte, error) { return []byte(body), nil }}
	prs, err := newTestProvider(f, &clock{t: time.Now()}).Lookup(context.Background(), model.GitHubRepo(repo), []int{101, 102}, []string{"worker-c", "worker-d"})
	if err != nil {
		t.Fatal(err)
	}
	for _, pr := range prs {
		if pr.Number == 102 && pr.Title != hostile {
			t.Fatalf("title changed: %q", pr.Title[:40])
		}
	}
}

// Hostile branch and repo values never become options of gh: each travels as
// the value of a -f raw field, right after its flag.
func TestHostileValuesStayData(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) { return []byte(`{"data":{"repository":{}}}`), nil }}
	p := newTestProvider(f, &clock{t: time.Now()})
	hostile := []string{"--hostname=evil.example", "-R", "x", "@/etc/passwd", "{owner}", "--jq=.x"}
	if _, err := p.Lookup(context.Background(), model.GitHubRepo("-R/x"), []int{1}, nil); err == nil {
		t.Fatal("repo starting with a dash accepted")
	}
	if _, err := p.Lookup(context.Background(), model.GitHubRepo("--hostname=x/y"), []int{1}, nil); err == nil {
		t.Fatal("repo with an option shape accepted")
	}
	if _, err := p.Lookup(context.Background(), model.GitHubRepo(repo), []int{1}, hostile); err != nil {
		t.Fatal(err)
	}
	args := f.calls[len(f.calls)-1]
	if args[0] != "api" || args[1] != "graphql" || args[2] != "--hostname" || args[3] != "github.com" {
		t.Fatalf("args = %v", args)
	}
	for i := 4; i < len(args); i += 2 {
		if args[i] != "-f" {
			t.Fatalf("argument %d is %q, want -f: %v", i, args[i], args)
		}
		key, _, ok := strings.Cut(args[i+1], "=")
		ours := key == "query" || key == "owner" || key == "name" || strings.HasPrefix(key, "b")
		if !ok || !ours {
			t.Fatalf("field %q is not one of ours", args[i+1])
		}
	}
}

func TestRejectsBadRepoAndCapsInputs(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) { return []byte(`{"data":{"repository":{}}}`), nil }}
	p := newTestProvider(f, &clock{t: time.Now()})
	for _, bad := range []string{"", "a", "a/b/c", "-x/y", "a/../b", "../a"} {
		if _, err := p.Lookup(context.Background(), model.GitHubRepo(bad), []int{1}, nil); err == nil {
			t.Errorf("repo %q accepted", bad)
		}
	}
	// A repo on another forge or host is never asked of gh, even with a path
	// gh would accept.
	for _, other := range []model.ForgeRepo{
		model.GitLabRepo("gitlab.com", repo),
		{Forge: model.ForgeGitHub, Host: "github.example.com", Path: repo},
		{Path: repo},
	} {
		if _, err := p.Lookup(context.Background(), other, []int{1}, nil); err == nil {
			t.Errorf("Lookup accepted %+v", other)
		}
		if _, err := p.Evidence(context.Background(), other, []string{"b"}); err == nil {
			t.Errorf("Evidence accepted %+v", other)
		}
	}
	if f.apiCalls() != 0 {
		t.Fatal("gh ran for a bad repo")
	}
	if prs, err := p.Lookup(context.Background(), model.GitHubRepo(repo), nil, nil); err != nil || prs != nil || f.apiCalls() != 0 {
		t.Fatal("empty lookup must not run gh")
	}
	many := make([]string, 500)
	for i := range many {
		many[i] = strings.Repeat("b", i+1)
	}
	if _, err := p.Lookup(context.Background(), model.GitHubRepo(repo), []int{-1, 0, 5}, append(many, "", "x\x00y")); err != nil {
		t.Fatal(err)
	}
	branchVars := 0
	for _, a := range f.calls[0] {
		if strings.HasPrefix(a, "b") && strings.Contains(a, "=") {
			branchVars++
		}
	}
	if branchVars > maxBranches {
		t.Fatalf("%d branch variables, cap is %d", branchVars, maxBranches)
	}
}

func TestBranchLookupDropsForkPullRequests(t *testing.T) {
	body := `{"data":{"repository":{` +
		`"n0":{"number":110,"title":"own, by number","url":"https://github.com/example-org/example-repo/pull/110","headRefName":"main","isCrossRepository":true,"state":"OPEN","createdAt":"2026-11-16T09:00:00Z"},` +
		`"b0":{"nodes":[` +
		`{"number":111,"title":"from a fork","url":"https://github.com/example-org/example-repo/pull/111","headRefName":"main","isCrossRepository":true,"state":"OPEN","createdAt":"2026-11-16T09:00:00Z"},` +
		`{"number":112,"title":"same repo","url":"https://github.com/example-org/example-repo/pull/112","headRefName":"main","isCrossRepository":false,"state":"OPEN","createdAt":"2026-11-16T09:00:00Z"}]}}}}`
	f := &fakeGh{reply: func(args []string) ([]byte, error) {
		for _, a := range args {
			if strings.HasPrefix(a, "query=") && !strings.Contains(a, "isCrossRepository") {
				t.Error("query does not ask for isCrossRepository")
			}
		}
		return []byte(body), nil
	}}
	prs, err := newTestProvider(f, &clock{t: time.Now()}).Lookup(context.Background(), model.GitHubRepo(repo), []int{110}, []string{"main"})
	if err != nil {
		t.Fatal(err)
	}
	var nums []int
	for _, pr := range prs {
		nums = append(nums, pr.Number)
	}
	if !slices.Equal(nums, []int{110, 112}) {
		t.Fatalf("numbers = %v, want [110 112]: a fork's pull request found by branch name is not ours", nums)
	}
}

func TestRepoNotFoundDoesNotFailTheSource(t *testing.T) {
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte(`{"data":{"repository":null},"errors":[{"type":"NOT_FOUND"}]}`),
			Stderr: []byte("gh: Could not resolve to a Repository with the name 'example-org/gone'.")}
	}}
	p := newTestProvider(f, &clock{t: time.Now()})
	if _, err := p.Lookup(context.Background(), model.GitHubRepo("example-org/gone"), []int{1}, nil); !errors.Is(err, source.ErrRepoNotFound) {
		t.Fatalf("err %v, want source.ErrRepoNotFound", err)
	}
	if st := p.Probe(context.Background()); st.State != source.StateOK {
		t.Fatalf("one missing repo turned the source %s: %+v", st.State, st)
	}
}

func TestConcurrentSameLookupsShareOneCall(t *testing.T) {
	release := make(chan struct{})
	f := &fakeGh{reply: func([]string) ([]byte, error) {
		<-release
		return fixture(t, "lookup.json"), nil
	}}
	p := newTestProvider(f, &clock{t: time.Now()})
	var wg sync.WaitGroup
	results := make([]int, 5)
	for i := range results {
		wg.Go(func() {
			prs, err := p.Lookup(context.Background(), model.GitHubRepo(repo), []int{101, 102}, []string{"worker-c", "worker-d"})
			if err != nil {
				t.Error(err)
			}
			results[i] = len(prs)
		})
	}
	time.Sleep(50 * time.Millisecond) // let all five start and wait on the one call
	close(release)
	wg.Wait()
	if n := f.apiCalls(); n != 1 {
		t.Fatalf("gh api calls = %d, want 1", n)
	}
	for i, n := range results {
		if n != 4 {
			t.Fatalf("lookup %d got %d pull requests", i, n)
		}
	}
}

// A pull request's address is built from the repo and the number for
// github.com; whatever gh might say in a url field is never read.
func TestBuildsThePullRequestAddress(t *testing.T) {
	answer := `{"data":{"repository":{` +
		`"n0":{"number":5,"title":"t","url":"javascript:alert(1)","headRefName":"x","state":"OPEN","createdAt":"2026-11-16T09:00:00Z"},` +
		`"n1":{"number":6,"title":"t","url":"https://evil.example/example-org/example-repo/pull/6","headRefName":"y","state":"MERGED","createdAt":"2026-11-16T09:00:00Z"}}}}`
	f := &fakeGh{reply: func([]string) ([]byte, error) { return []byte(answer), nil }}
	prs, err := newTestProvider(f, &clock{t: time.Now()}).Lookup(context.Background(), model.GitHubRepo(repo), []int{5, 6}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 2 {
		t.Fatalf("prs = %+v", prs)
	}
	for _, pr := range prs {
		if want := fmt.Sprintf("https://github.com/%s/pull/%d", repo, pr.Number); pr.URL != want {
			t.Errorf("#%d URL = %q, want %q", pr.Number, pr.URL, want)
		}
	}
	for _, a := range f.calls[0] {
		if strings.HasPrefix(a, "query=") && regexp.MustCompile(`\burl\b`).MatchString(a) {
			t.Errorf("the query asks gh for a url: %s", a)
		}
	}
}

// The probe asks about github.com only, the one host lookups go to.
func TestProbeNamesTheHost(t *testing.T) {
	f := &fakeGh{}
	newTestProvider(f, &clock{t: time.Now()}).Probe(context.Background())
	var auth [][]string
	for _, c := range f.calls {
		if c[0] == "auth" {
			auth = append(auth, c)
		}
	}
	if len(auth) != 1 || !slices.Equal(auth[0], []string{"auth", "status", "--hostname", "github.com"}) {
		t.Fatalf("auth calls = %v", auth)
	}
}

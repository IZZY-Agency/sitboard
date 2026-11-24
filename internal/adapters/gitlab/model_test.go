package gitlab

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// The core gets merge requests as pull requests: the GitLab repository, the
// number, the address built for it, the state, draft, checks, the approval in
// the place of the review decision, and the issues it closes.
func TestSourceLookupGivesPullRequests(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	s := &Source{p: p}
	repo := model.GitLabRepo(selfHosted, project)
	prs, err := s.Lookup(context.Background(), repo, []int{101, 102, 103, 104, 107}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var nums []int
	for _, pr := range prs {
		nums = append(nums, pr.Number)
		if pr.Repo != repo || pr.URL != model.ChangeRequestURL(repo, pr.Number) || pr.URL == "" {
			t.Errorf("#%d repo %+v url %q", pr.Number, pr.Repo, pr.URL)
		}
		if !slices.Equal(pr.ClosingIssues, []int{7, 8}) {
			t.Errorf("!%d closes %v", pr.Number, pr.ClosingIssues)
		}
	}
	if !slices.Equal(nums, []int{101, 102, 103, 104, 107}) || f.count("graphql") != 1 {
		t.Fatalf("numbers %v, %d calls", nums, f.count("graphql"))
	}
	want := map[int]struct {
		state  model.PRState
		review model.Review
		checks model.Checks
		draft  bool
	}{
		101: {model.PRStateOpen, model.ReviewRequired, model.ChecksRunning, false},
		102: {model.PRStateMerged, model.ReviewApproved, model.ChecksPassing, false},
		103: {model.PRStateOpen, model.ReviewNone, model.ChecksFailing, false},
		104: {model.PRStateOpen, model.ReviewApproved, model.ChecksPassing, false},
		107: {model.PRStateOpen, model.ReviewNone, model.ChecksNone, true},
	}
	for _, pr := range prs {
		w := want[pr.Number]
		if pr.State != w.state || pr.Review != w.review || pr.Checks != w.checks || pr.Draft != w.draft {
			t.Errorf("!%d: %s %q %s draft=%v, want %s %q %s draft=%v", pr.Number, pr.State, pr.Review, pr.Checks, pr.Draft, w.state, w.review, w.checks, w.draft)
		}
	}
	if prs[0].HeadBranch != "feature-a" || prs[0].OpenedAt.IsZero() || prs[1].ClosedAt.IsZero() {
		t.Errorf("branch %q opened %v closed %v", prs[0].HeadBranch, prs[0].OpenedAt, prs[1].ClosedAt)
	}
}

// Approvals in the place of the review decision: GitLab has no "changes
// requested", and no rule or unknown counts show nothing.
func TestReviewOf(t *testing.T) {
	for a, want := range map[Approval]model.Review{
		Approved: model.ReviewApproved, ApprovalRequired: model.ReviewRequired,
		ApprovalNone: model.ReviewNone, ApprovalUnknown: model.ReviewNone,
	} {
		if got := reviewOf(a); got != want {
			t.Errorf("%q: %q, want %q", a, got, want)
		}
	}
}

// Closing issues not known this time are sent as none, never as an old list;
// a merge request no address can be built for is left out.
func TestPullRequestFromMergeRequest(t *testing.T) {
	mr := MergeRequest{Host: selfHosted, Path: project, IID: 5, State: model.PRStateOpen, ClosingIssues: []int{3}, ClosingKnown: false}
	pr, ok := pullRequest(mr)
	if !ok || pr.ClosingIssues != nil {
		t.Errorf("unknown closing issues: %v %v", ok, pr.ClosingIssues)
	}
	mr.ClosingKnown = true
	known, _ := pullRequest(mr)
	if !slices.Equal(known.ClosingIssues, []int{3}) {
		t.Errorf("known closing issues: %v", known.ClosingIssues)
	}
	mr.ClosingIssues[0] = 4
	if !slices.Equal(known.ClosingIssues, []int{3}) {
		t.Errorf("shared slice: %v", known.ClosingIssues)
	}
	for _, bad := range []MergeRequest{{Host: selfHosted, Path: project, IID: 0}, {Host: "github.com", Path: project, IID: 5}, {Host: selfHosted, Path: "../x", IID: 5}} {
		if _, ok := pullRequest(bad); ok {
			t.Errorf("%+v made a pull request", bad)
		}
	}
}

// A repository of another forge is never asked about.
func TestSourceLookupRefusesOtherForges(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	s := &Source{p: p}
	for _, repo := range []model.ForgeRepo{model.GitHubRepo(project), {Forge: model.ForgeGitHub, Host: selfHosted, Path: project}, {}, model.GitLabRepo("github.com", project), model.GitLabRepo(selfHosted, "")} {
		if prs, err := s.Lookup(context.Background(), repo, []int{1}, nil); err == nil || prs != nil {
			t.Errorf("%+v: %v %v", repo, prs, err)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("glab ran: %q", f.calls)
	}
}

// glab's subfolder setting is read as a run of at most 8 path segments, each
// one a GitLab path could start with; anything else is no answer (the host
// gets no lookups), never a half-read prefix.
func TestSourceHostSubfolderIsValidated(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		sub  string
		want string // "" = no answer
	}{
		{"gitlab", "gitlab"},
		{"a/b/c/d/e/f/g/h", "a/b/c/d/e/f/g/h"}, // 8 segments
		{"a/b/c/d/e/f/g/h/i", ""},              // 9
		{"a..b", ""},                           // a dot run inside a segment
		{"a/-b", ""},                           // an option-like segment
		{"a b", ""},                            // a character no path has
		{"a/b%2Fc", ""},
	}
	for _, c := range cases {
		p, f, _ := newUnchecked(t)
		s := &Source{p: p}
		f.sub = map[string]string{selfHosted: c.sub}
		err := s.Host(ctx, selfHosted)
		if c.want == "" {
			if err == nil || errors.Is(err, source.ErrNotLoggedIn) {
				t.Errorf("subfolder %q: %v, want no answer", c.sub, err)
			}
			continue
		}
		if err != nil || s.Subfolder(selfHosted) != c.want {
			t.Errorf("subfolder %q: %v, got %q", c.sub, err, s.Subfolder(selfHosted))
		}
	}
}

// Host tells a definite no (not logged in) from no answer, with the errors
// the core tells apart. A subfolder install is a yes, its subfolder read
// once; a host with a web port is answered for its name.
func TestSourceHost(t *testing.T) {
	ctx := context.Background()
	p, _, _ := newUnchecked(t)
	s := &Source{p: p}
	if err := s.Host(ctx, selfHosted); err != nil {
		t.Errorf("logged in: %v", err)
	}
	for _, h := range []string{GitLabCom, "other.example.com", "github.com"} {
		if err := s.Host(ctx, h); !errors.Is(err, source.ErrNotLoggedIn) {
			t.Errorf("%s: %v, want not logged in", h, err)
		}
	}

	p, f, _ := newUnchecked(t)
	s = &Source{p: p}
	f.sub = map[string]string{selfHosted: "/apps/gitlab/\n"}
	if s.Subfolder(selfHosted) != "" {
		t.Errorf("subfolder before any check: %q", s.Subfolder(selfHosted))
	}
	if err := s.Host(ctx, selfHosted); err != nil {
		t.Errorf("subfolder: %v", err)
	}
	if err := s.Host(ctx, selfHosted+":8443"); err != nil {
		t.Errorf("with a port: %v", err)
	}
	if got := s.Subfolder(selfHosted + ":8443"); got != "apps/gitlab" || s.Subfolder(selfHosted) != got {
		t.Errorf("subfolder %q", got)
	}
	if n := f.count("auth"); n != 1 {
		t.Errorf("%d login checks for one host name, want 1", n)
	}
	for _, h := range []string{selfHosted + ":443", selfHosted + ":0", selfHosted + ":x", selfHosted + "/gitlab"} {
		if err := s.Host(ctx, h); err == nil {
			t.Errorf("%s: yes", h)
		}
	}

	p, f, _ = newUnchecked(t)
	s = &Source{p: p}
	f.sub = map[string]string{selfHosted: "../x"}
	if err := s.Host(ctx, selfHosted); err == nil || errors.Is(err, source.ErrNotLoggedIn) {
		t.Errorf("unreadable subfolder: %v, want no answer", err)
	}

	p, f, _ = newUnchecked(t)
	s = &Source{p: p}
	f.auth[selfHosted] = &runner.ExitError{Code: 1, Stderr: []byte(selfHosted + "\n  x " + selfHosted + ": API call failed: GET https://" + selfHosted + "/api/v4/user: 429 {message: 429 Too Many Requests}\n")}
	if err := s.Host(ctx, selfHosted); !errors.Is(err, source.ErrRateLimited) || errors.Is(err, source.ErrNotLoggedIn) {
		t.Errorf("rate limited: %v", err)
	}
	p, f, _ = newUnchecked(t)
	s = &Source{p: p}
	f.auth[selfHosted] = context.DeadlineExceeded
	if err := s.Host(ctx, selfHosted); err == nil || errors.Is(err, source.ErrNotLoggedIn) {
		t.Errorf("out of time: %v", err)
	}

	p, _, _ = newAdapter(t, Options{Off: true})
	if err := (&Source{p: p}).Host(ctx, GitLabCom); err == nil || errors.Is(err, source.ErrNotLoggedIn) {
		t.Errorf("switched off: %v, want no answer", err)
	}
}

// The adapter's own errors keep their words and are the shared ones underneath.
func TestErrorsAreTheSharedOnes(t *testing.T) {
	for err, shared := range map[error]error{
		errRateLimited: source.ErrRateLimited, errNotKnown: source.ErrNotLoggedIn,
	} {
		if !errors.Is(err, shared) || !errors.Is(err, err) {
			t.Errorf("%v is not %v", err, shared)
		}
	}
	if errRateLimited.Error() != "gitlab: rate limited" || errNotKnown.Error() != "gitlab: glab is not logged in to this host" {
		t.Errorf("words changed: %q %q", errRateLimited, errNotKnown)
	}
}

// glab logged in nowhere: one local auth check says so, and for a minute no
// other host costs a glab call; no project is ever asked about.
func TestLoggedInNowhereAsksNothing(t *testing.T) {
	ctx := context.Background()
	p, f, c := newUnchecked(t)
	nowhere := &runner.ExitError{Code: 1, Stderr: []byte("ERROR\n no GitLab instances have been authenticated with glab; run `glab auth login` to authenticate.\n")}
	f.auth = map[string]error{selfHosted: nowhere, GitLabCom: nowhere, "other.example.com": nowhere}
	s := &Source{p: p}
	for _, h := range []string{selfHosted, GitLabCom, "other.example.com"} {
		if err := s.Host(ctx, h); !errors.Is(err, source.ErrNotLoggedIn) {
			t.Errorf("%s: %v, want not logged in", h, err)
		}
	}
	if _, err := s.Lookup(ctx, model.GitLabRepo(GitLabCom, project), []int{1}, nil); !errors.Is(err, source.ErrNotLoggedIn) {
		t.Errorf("lookup: %v", err)
	}
	if len(f.calls) != 1 || f.count("auth") != 1 {
		t.Fatalf("glab ran %d times: %q", len(f.calls), f.calls)
	}
	c.add(unknownFor)
	f.auth[selfHosted] = nil
	if err := s.Host(ctx, selfHosted); err != nil {
		t.Errorf("logged in a minute later: %v", err)
	}
}

// glab not found: the source says missing, and no question about a host or
// a project starts any process.
func TestNoGlabRunsNothing(t *testing.T) {
	ctx := context.Background()
	p, f, _ := newUnchecked(t)
	p.found = func(string) string { return "" }
	s := &Source{p: p}
	if st := s.Probe(ctx); st.State != source.StateMissing {
		t.Errorf("probe %+v", st)
	}
	for _, h := range []string{selfHosted, GitLabCom} {
		if err := s.Host(ctx, h); err == nil {
			t.Errorf("%s: a yes without glab", h)
		}
		if _, err := s.Lookup(ctx, model.GitLabRepo(h, project), []int{1}, nil); err == nil {
			t.Errorf("%s: a lookup without glab", h)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("processes started: %q", f.calls)
	}
}

// The source list names GitLab; off says switched off and runs nothing.
func TestSourceProbe(t *testing.T) {
	p, _, _ := newAdapter(t, Options{})
	st := (&Source{p: p}).Probe(context.Background())
	if st.Kind != source.KindGitLab || st.State != source.StateOK || st.Version != "1.131.0" || !st.Tested {
		t.Errorf("probe %+v", st)
	}
	p, f, _ := newAdapter(t, Options{Off: true})
	st = (&Source{p: p}).Probe(context.Background())
	if st.Kind != source.KindGitLab || st.State != source.StateOff || len(f.calls) != 0 {
		t.Errorf("off: %+v, %d calls", st, len(f.calls))
	}
}

// On a board whose GitLab has a web port, a failed call shows on its line:
// calls go, and fail, by the host name, so the line for the site and the line
// for the name are one.
func TestSourceFailingWithAWebPort(t *testing.T) {
	ctx := context.Background()
	p, f, _ := newAdapter(t, Options{})
	s := &Source{p: p}
	site := selfHosted + ":8443"
	f.graphql = func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stderr: []byte("glab: 429 Too Many Requests (HTTP 429)\n")}
	}
	if _, err := s.Lookup(ctx, model.GitLabRepo(site, project), []int{1}, nil); err == nil {
		t.Fatal("a rate limit is an error")
	}
	if s.Failing(site) == "" || s.Failing(site) != s.Failing(selfHosted) {
		t.Fatalf("line %q, for the name %q", s.Failing(site), s.Failing(selfHosted))
	}
}

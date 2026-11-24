package core

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// fakeIssues answers OpenIssues from a fixed set and records each call.
type fakeIssues struct {
	mu    sync.Mutex
	by    map[model.ForgeRepo]model.RepoIssues
	calls [][]model.ForgeRepo
	err   error
}

func (f *fakeIssues) OpenIssues(_ context.Context, repos []model.ForgeRepo) ([]model.RepoIssues, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, slices.Clone(repos))
	if f.err != nil {
		return nil, f.err
	}
	var out []model.RepoIssues
	for _, r := range repos {
		if ri, ok := f.by[r]; ok {
			out = append(out, ri)
		}
	}
	return out, nil
}

func (f *fakeIssues) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeIssues) lastCall() []model.ForgeRepo {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

const betaRepo = "example-org/other-repo"

func sampleIssues(repo model.ForgeRepo, open int, numbers ...int) model.RepoIssues {
	ri := model.RepoIssues{Repo: repo, State: model.RepoIssuesOK, Open: open}
	for _, n := range numbers {
		ri.List = append(ri.List, model.OpenIssue{Number: n, Title: "Sample issue", UpdatedAt: tNow.Add(-time.Hour)})
	}
	return ri
}

// newIssuesEnv is newEnv with a GitHub issue lister and two projects on the
// board, alpha (exRepo) and beta (betaRepo).
func newIssuesEnv(t *testing.T) (*testEnv, *fakeIssues) {
	t.Helper()
	e := newEnv(t)
	gh := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{
		model.GitHubRepo(exRepo):   sampleIssues(model.GitHubRepo(exRepo), 12, 48, 45),
		model.GitHubRepo(betaRepo): sampleIssues(model.GitHubRepo(betaRepo), 3, 7),
	}}
	e.board = newBoard(t, Options{
		Repos: e.repos, PRs: e.prs, State: e.state, Now: e.clock.Now, StaleAfter: 20 * time.Minute,
		Issues: map[model.Forge]source.IssueLister{model.ForgeGitHub: gh},
	})
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: model.GitHubRepo(betaRepo), Branch: model.Of("main")})
	a := busySession("a", tNow)
	b := busySession("b", tNow)
	b.Folder = beta
	e.board.applySync(a)
	e.board.applySync(b)
	return e, gh
}

func projectNamed(t *testing.T, b *Board, name string) contract.Project {
	t.Helper()
	for _, p := range b.Snapshot().Projects {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no project %q", name)
	return contract.Project{}
}

func TestIssuesAreAskedForTheBoardsRepositoriesInOneCall(t *testing.T) {
	e, gh := newIssuesEnv(t)
	if s := e.board.Snapshot().Issues; s.State != contract.IssuesLoading {
		t.Fatalf("before the first look: %+v", s)
	}
	e.board.tick()
	if gh.callCount() != 1 || !slices.Equal(gh.lastCall(), []model.ForgeRepo{model.GitHubRepo(exRepo), model.GitHubRepo(betaRepo)}) {
		t.Fatalf("calls %+v", gh.calls)
	}
	if p := projectNamed(t, e.board, "alpha"); p.OpenIssues != (contract.IssueCount{Known: true, Open: 12}) {
		t.Fatalf("alpha %+v", p.OpenIssues)
	}
	if p := projectNamed(t, e.board, "beta"); p.OpenIssues != (contract.IssueCount{Known: true, Open: 3}) {
		t.Fatalf("beta %+v", p.OpenIssues)
	}
	s := e.board.Snapshot().Issues
	if s.State != contract.IssuesReady || s.Open != 15 || !s.AsOf.Equal(tNow) {
		t.Fatalf("summary %+v", s)
	}
	l, err := e.board.Issues(context.Background())
	if err != nil || l.State != contract.IssuesReady || len(l.Repos) != 2 {
		t.Fatalf("list %+v, %v", l, err)
	}
	r := l.Repos[0]
	if r.Repo != exRepo || r.Forge != contract.ForgeGitHub || r.Host != model.GitHubHost || r.State != contract.RepoIssuesOK || r.Open != 12 || len(r.Issues) != 2 {
		t.Fatalf("first repo %+v", r)
	}
	if r.Issues[0].URL != "https://github.com/example-org/example-repo/issues/48" || r.Issues[0].Labels == nil {
		t.Fatalf("first issue %+v", r.Issues[0])
	}
}

func TestIssuesAreAskedAgainAfterTenMinutesAndNotSooner(t *testing.T) {
	e, gh := newIssuesEnv(t)
	e.board.tick()
	e.clock.Set(tNow.Add(issuesEvery - time.Second))
	e.board.tick()
	if gh.callCount() != 1 {
		t.Fatalf("%d calls before ten minutes, want 1", gh.callCount())
	}
	e.clock.Set(tNow.Add(issuesEvery))
	e.board.tick()
	if gh.callCount() != 2 {
		t.Fatalf("%d calls after ten minutes, want 2", gh.callCount())
	}
}

func TestOpeningTheListAsksAgainOnlyWhenItIsOlderThanAMinute(t *testing.T) {
	e, gh := newIssuesEnv(t)
	e.board.tick()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor - time.Second))
	if _, err := e.board.Issues(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.board.issuesCh) != 0 {
		t.Fatal("a fresh list asked for a new look")
	}
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	if gh.callCount() != 1 {
		t.Fatalf("%d calls with nobody asking, want 1", gh.callCount())
	}
	if _, err := e.board.Issues(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.board.issuesCh) != 1 {
		t.Fatal("an old list did not ask for a new look")
	}
	<-e.board.issuesCh // what Run does with it
	e.board.mu.Lock()
	e.board.startIssuesLocked(context.Background(), e.board.now())
	e.board.mu.Unlock()
	e.board.resolving.Wait()
	if gh.callCount() != 2 {
		t.Fatalf("%d calls after the page asked, want 2", gh.callCount())
	}
	if s := e.board.Snapshot().Issues; !s.AsOf.Equal(tNow.Add(contract.IssuesFreshFor)) {
		t.Fatalf("the new look is not announced: %+v", s)
	}
}

func TestANewRepositoryIsAskedAboutAfterAMinute(t *testing.T) {
	e, gh := newIssuesEnv(t)
	e.board.tick()
	gamma := testHome + "/projects/gamma"
	e.repos.set(gamma, model.Repo{Root: gamma, MainRoot: gamma, Remote: model.GitHubRepo("example-org/third-repo"), Branch: model.Of("main")})
	c := busySession("c", tNow)
	c.Folder = gamma
	e.board.applySync(c)
	e.clock.Set(tNow.Add(contract.IssuesFreshFor - time.Second))
	e.board.tick()
	if gh.callCount() != 1 {
		t.Fatalf("%d calls within the minute, want 1", gh.callCount())
	}
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	if gh.callCount() != 2 || len(gh.lastCall()) != 3 {
		t.Fatalf("calls %+v; want a second one with all three repositories", gh.calls)
	}
}

func TestIssuesKeepTheLastAnswerWhenALookFails(t *testing.T) {
	e, gh := newIssuesEnv(t)
	e.board.tick()
	gh.mu.Lock()
	gh.err = errors.New("github: rate limited")
	gh.mu.Unlock()
	e.clock.Set(tNow.Add(issuesEvery))
	e.board.tick()
	if p := projectNamed(t, e.board, "alpha"); p.OpenIssues != (contract.IssueCount{Known: true, Open: 12}) {
		t.Fatalf("alpha after a failed look: %+v", p.OpenIssues)
	}
}

func TestIssuesOffWhenGitHubIsOff(t *testing.T) {
	e, gh := newIssuesEnv(t)
	e.board.SetSources([]source.Status{{Kind: source.KindGitHub, State: source.StateOff, Detail: "switched off"}})
	e.board.tick()
	if gh.callCount() != 0 {
		t.Fatalf("asked GitHub while it is off: %+v", gh.calls)
	}
	if s := e.board.Snapshot().Issues; s.State != contract.IssuesOff || s.Open != 0 || !s.AsOf.IsZero() {
		t.Fatalf("summary %+v", s)
	}
	if p := projectNamed(t, e.board, "alpha"); p.OpenIssues.Known {
		t.Fatalf("alpha %+v", p.OpenIssues)
	}
	l, err := e.board.Issues(context.Background())
	if err != nil || l.State != contract.IssuesOff || len(l.Repos) != 0 || len(e.board.issuesCh) != 0 {
		t.Fatalf("list %+v, %v", l, err)
	}
}

func TestIssuesWithoutAListerAreOff(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.board.applySync(busySession("a", tNow))
	e.board.tick()
	if s := e.board.Snapshot().Issues; s.State != contract.IssuesOff {
		t.Fatalf("summary %+v", s)
	}
}

func TestDisabledAndHiddenRepositoriesHaveNoCount(t *testing.T) {
	e, gh := newIssuesEnv(t)
	gh.by[model.GitHubRepo(exRepo)] = model.RepoIssues{Repo: model.GitHubRepo(exRepo), State: model.RepoIssuesDisabled}
	gh.by[model.GitHubRepo(betaRepo)] = model.RepoIssues{Repo: model.GitHubRepo(betaRepo), State: model.RepoIssuesNotFound}
	e.board.tick()
	for _, name := range []string{"alpha", "beta"} {
		if p := projectNamed(t, e.board, name); p.OpenIssues.Known {
			t.Fatalf("%s %+v", name, p.OpenIssues)
		}
	}
	l, _ := e.board.Issues(context.Background())
	if l.Repos[0].State != contract.RepoIssuesDisabled || l.Repos[1].State != contract.RepoIssuesNotFound {
		t.Fatalf("list %+v", l.Repos)
	}
}

func TestAnUnansweredRepositoryIsPending(t *testing.T) {
	e, gh := newIssuesEnv(t)
	delete(gh.by, model.GitHubRepo(betaRepo))
	e.board.tick()
	l, _ := e.board.Issues(context.Background())
	if l.Repos[1].Repo != betaRepo || l.Repos[1].State != contract.RepoIssuesPending || l.Repos[1].Issues == nil {
		t.Fatalf("list %+v", l.Repos)
	}
}

func TestIssueTextIsCleanedAndCapped(t *testing.T) {
	e, gh := newIssuesEnv(t)
	long := strings.Repeat("x", 80)
	gh.by[model.GitHubRepo(exRepo)] = model.RepoIssues{Repo: model.GitHubRepo(exRepo), State: model.RepoIssuesOK, Open: 1, List: []model.OpenIssue{{
		Number: 3, Title: "a\u202eb\x00c\nd", Milestone: long, MilestoneDue: tNow, Labels: []string{"\u200b", long, "x"},
	}}}
	e.board.tick()
	l, _ := e.board.Issues(context.Background())
	is := l.Repos[0].Issues[0]
	if is.Title != "abc d" || is.Milestone != strings.Repeat("x", maxLabelText)+"…" || !is.MilestoneDue.Equal(tNow) {
		t.Fatalf("issue %+v", is)
	}
	if !slices.Equal(is.Labels, []string{strings.Repeat("x", maxLabelText) + "…", "x"}) {
		t.Fatalf("labels %q", is.Labels)
	}
}

func TestARepositoryThatLeftTheBoardIsDropped(t *testing.T) {
	e, gh := newIssuesEnv(t)
	e.board.tick()
	ended := busySession("b", tNow)
	ended.Folder = beta
	ended.Ended, ended.Live = true, model.Of(false)
	e.board.applySync(ended)
	e.clock.Set(tNow.Add(issuesEvery))
	e.board.tick()
	if !slices.Equal(gh.lastCall(), []model.ForgeRepo{model.GitHubRepo(exRepo)}) {
		t.Fatalf("last call %+v", gh.lastCall())
	}
	l, _ := e.board.Issues(context.Background())
	if len(l.Repos) != 1 || e.board.Snapshot().Issues.Open != 12 {
		t.Fatalf("list %+v", l.Repos)
	}
	e.board.mu.Lock()
	_, kept := e.board.issues[model.GitHubRepo(betaRepo)]
	e.board.mu.Unlock()
	if kept {
		t.Fatal("beta's answer is still kept")
	}
}

// A GitLab lister plugs in by forge: its repositories are asked about only on
// a host glab is logged in to, never with GitLab off.
func TestGitLabIssuesAreAskedOnlyOnAHostThatPassedItsCheck(t *testing.T) {
	gl := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	e, _ := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	repo := model.GitLabRepo(glHost, glPath)
	gl.by[repo] = sampleIssues(repo, 4, 1)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repo, Branch: model.Of("main")})
	e.board.applySync(busySession("a", tNow))
	e.board.tick() // the host check runs; the repository is not shown before it passed
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	if !slices.Equal(gl.lastCall(), []model.ForgeRepo{repo}) {
		t.Fatalf("GitLab calls %+v", gl.calls)
	}
	if p := projectNamed(t, e.board, "alpha"); p.OpenIssues != (contract.IssueCount{Known: true, Open: 4}) {
		t.Fatalf("alpha %+v", p.OpenIssues)
	}
	l, _ := e.board.Issues(context.Background())
	if len(l.Repos) != 1 || l.Repos[0].Issues[0].URL != "https://gitlab.example.com/example-group/example-project/-/issues/1" {
		t.Fatalf("list %+v", l.Repos)
	}
}

func TestGitLabIssuesAreNeverAskedOnAHostWithoutALogin(t *testing.T) {
	gl := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	e, glab := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	glab.setHost(glHost, source.ErrNotLoggedIn)
	repo := model.GitLabRepo(glHost, glPath)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repo, Branch: model.Of("main")})
	e.board.applySync(busySession("a", tNow))
	for i := range 3 {
		e.clock.Set(tNow.Add(time.Duration(i) * issuesEvery))
		e.board.tick()
	}
	if gl.callCount() != 0 {
		t.Fatalf("asked a host without a login: %+v", gl.calls)
	}
}

func TestTurningGitHubOffDropsWhatWasListed(t *testing.T) {
	e, _ := newIssuesEnv(t)
	e.board.tick()
	e.board.SetSources([]source.Status{{Kind: source.KindGitHub, State: source.StateOff, Detail: "switched off"}})
	e.board.tick()
	e.board.mu.Lock()
	n, at := len(e.board.issues), e.board.issuesAt
	e.board.mu.Unlock()
	if n != 0 || !at.IsZero() {
		t.Fatalf("%d answers kept, last look %v", n, at)
	}
}

// With GitLab on, issues stay on; GitHub switched off is still never asked.
func TestGitHubOffIsNotAskedWhileGitLabIsOn(t *testing.T) {
	gh := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	gl := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	e, _ := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitHub: gh, model.ForgeGitLab: gl}
	})
	e.board.SetSources([]source.Status{{Kind: source.KindGitHub, State: source.StateOff, Detail: "switched off"}, glOK})
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.board.applySync(busySession("a", tNow))
	e.board.tick()
	if s := e.board.Snapshot().Issues; s.State == contract.IssuesOff {
		t.Fatalf("issues off while GitLab is on: %+v", s)
	}
	if gh.callCount() != 0 {
		t.Fatalf("asked GitHub while it is off: %+v", gh.calls)
	}
}

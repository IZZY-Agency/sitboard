package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// gatedIssues answers every repository it is asked about with one issue
// titled title; while gate is set, each call waits for it to close.
type gatedIssues struct {
	mu    sync.Mutex
	title string
	open  int
	err   error
	gate  chan struct{}
	asked [][]model.ForgeRepo
	// state, when set, is every answer's state (no rows); withErr: the
	// rows come along with err, as a partly rate-limited look's do.
	state   model.RepoIssuesState
	withErr bool
}

func (g *gatedIssues) OpenIssues(_ context.Context, repos []model.ForgeRepo, _ map[model.ForgeRepo][]int) ([]model.RepoIssues, error) {
	g.mu.Lock()
	gate := g.gate
	g.asked = append(g.asked, slices.Clone(repos))
	g.mu.Unlock()
	if gate != nil {
		<-gate
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil && !g.withErr {
		return nil, g.err
	}
	var out []model.RepoIssues
	for _, r := range repos {
		if g.state != "" && g.state != model.RepoIssuesOK {
			out = append(out, model.RepoIssues{Repo: r, State: g.state})
			continue
		}
		out = append(out, model.RepoIssues{Repo: r, State: model.RepoIssuesOK, Open: g.open, List: []model.OpenIssue{{Number: 1, Title: g.title}}})
	}
	return out, g.err
}

func (g *gatedIssues) set(f func(g *gatedIssues)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f(g)
}

func (g *gatedIssues) calls() [][]model.ForgeRepo {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.asked)
}

// gatedEnv is newEnv with g as the GitHub issue lister and project alpha
// (exRepo) on the board.
func gatedEnv(t *testing.T, g *gatedIssues) *testEnv {
	t.Helper()
	e := newEnv(t)
	e.board = newBoard(t, Options{
		Repos: e.repos, PRs: e.prs, State: e.state, Now: e.clock.Now, StaleAfter: 20 * time.Minute,
		Issues: map[model.Forge]source.IssueLister{model.ForgeGitHub: g},
	})
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.board.applySync(busySession("a", tNow))
	return e
}

func firstTitle(t *testing.T, b *Board) string {
	t.Helper()
	l, err := b.Issues(context.Background())
	if err != nil || len(l.Repos) == 0 || len(l.Repos[0].Issues) == 0 {
		t.Fatalf("list %+v, %v", l, err)
	}
	return l.Repos[0].Issues[0].Title
}

func TestIssuesAsOfMovesWhenTheAnswerComesNotWhenTheLookStarts(t *testing.T) {
	g := &gatedIssues{title: "old title", open: 1}
	e := gatedEnv(t, g)
	e.board.tick()
	first := e.board.Snapshot().Issues.AsOf
	if first.IsZero() || firstTitle(t, e.board) != "old title" {
		t.Fatalf("first look: asOf %v", first)
	}

	gate := make(chan struct{})
	g.set(func(g *gatedIssues) { g.gate, g.title = gate, "new title" })
	e.clock.Set(tNow.Add(issuesEvery))
	e.board.mu.Lock()
	e.board.startIssuesLocked(context.Background(), e.board.now())
	e.board.mu.Unlock()
	// The look is under way: the page that fetches now gets the old list
	// under the old AsOf, so the new answer must come with a new one.
	if s := e.board.Snapshot().Issues.AsOf; !s.Equal(first) {
		t.Fatalf("asOf moved to %v before GitHub answered", s)
	}
	if firstTitle(t, e.board) != "old title" {
		t.Fatal("the list changed before the answer")
	}
	close(gate)
	e.board.resolving.Wait()
	if s := e.board.Snapshot().Issues.AsOf; !s.After(first) {
		t.Fatalf("asOf %v did not move when the answer came (was %v)", s, first)
	}
	if firstTitle(t, e.board) != "new title" {
		t.Fatal("the answer was not listed")
	}
}

func TestALostLoginDropsWhatWasListedAndAFailureKeepsIt(t *testing.T) {
	g := &gatedIssues{title: "private title", open: 4}
	e := gatedEnv(t, g)
	e.board.tick()
	asOf := e.board.Snapshot().Issues.AsOf

	g.set(func(g *gatedIssues) { g.err = errors.New("github: gh failed") })
	e.clock.Set(tNow.Add(issuesEvery))
	e.board.tick()
	if p := projectNamed(t, e.board, "alpha"); p.OpenIssues != (contract.IssueCount{Known: true, Open: 4}) {
		t.Fatalf("a failed look dropped the count: %+v", p.OpenIssues)
	}
	if s := e.board.Snapshot().Issues.AsOf; !s.Equal(asOf) {
		t.Fatalf("a failed look moved asOf to %v", s)
	}
	if firstTitle(t, e.board) != "private title" {
		t.Fatal("a failed look dropped the list")
	}

	g.set(func(g *gatedIssues) { g.err = fmt.Errorf("github: not logged in: %w", source.ErrNotLoggedIn) })
	e.clock.Set(tNow.Add(2 * issuesEvery))
	e.board.tick()
	if p := projectNamed(t, e.board, "alpha"); p.OpenIssues.Known {
		t.Fatalf("after the login went the count stays: %+v", p.OpenIssues)
	}
	l, _ := e.board.Issues(context.Background())
	if len(l.Repos) != 1 || len(l.Repos[0].Issues) != 0 || l.Repos[0].State != contract.RepoIssuesPending {
		t.Fatalf("after the login went: %+v", l)
	}
	if s := e.board.Snapshot().Issues; !s.AsOf.After(asOf) || s.Open != 0 {
		t.Fatalf("the change was not announced: %+v", s)
	}
}

func TestARepositoryInTwoSpellingsIsAskedAndCountedOnce(t *testing.T) {
	g := &gatedIssues{title: "t", open: 5}
	e := gatedEnv(t, g)
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: model.GitHubRepo("Example-Org/Example-Repo"), Branch: model.Of("main")})
	b := busySession("b", tNow)
	b.Folder = beta
	e.board.applySync(b)
	e.board.tick()
	if c := g.calls(); len(c) != 1 || len(c[0]) != 1 {
		t.Fatalf("asked %+v, want one repository", c)
	}
	for _, name := range []string{"alpha", "beta"} {
		if p := projectNamed(t, e.board, name); p.OpenIssues != (contract.IssueCount{Known: true, Open: 5}) {
			t.Fatalf("%s %+v", name, p.OpenIssues)
		}
	}
	if s := e.board.Snapshot().Issues; s.Open != 5 {
		t.Fatalf("summary %+v, want the repository once", s)
	}
}

func TestEveryRepositoryIsAnsweredInTurnPastTheCap(t *testing.T) {
	g := &gatedIssues{title: "t", open: 1}
	e := gatedEnv(t, g)
	n := maxIssueRepos + 1
	for i := range n {
		folder := fmt.Sprintf("%s/projects/p%03d", testHome, i)
		e.repos.set(folder, model.Repo{Root: folder, MainRoot: folder, Remote: model.GitHubRepo(fmt.Sprintf("sample-org/r%03d", i)), Branch: model.Of("main")})
		s := busySession(fmt.Sprintf("p%03d", i), tNow)
		s.Folder = folder
		e.board.applySync(s)
	}
	e.board.tick()
	if c := g.calls(); len(c) != 1 || len(c[0]) != maxIssueRepos {
		t.Fatalf("first look asked %d calls", len(c))
	}
	missing := func() []string {
		var out []string
		for _, p := range e.board.Snapshot().Projects {
			if p.Repo != "" && !p.OpenIssues.Known {
				out = append(out, p.Name)
			}
		}
		return out
	}
	if m := missing(); len(m) != 1+1 { // alpha and one sample repository wait
		t.Fatalf("after the first look, unanswered %v", m)
	}
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	if m := missing(); len(m) != 0 {
		t.Fatalf("after the second look, unanswered %v", m)
	}
	c := g.calls()
	if len(c) != 2 || len(c[1]) != maxIssueRepos {
		t.Fatalf("second look: %d calls", len(c))
	}
	for _, r := range c[1][:2] {
		if slices.Contains(c[0], r) {
			t.Fatalf("second look started with %v, asked before; want the two never asked first", r)
		}
	}
}

func TestTheLastProjectLeavingDropsItsIssues(t *testing.T) {
	g := &gatedIssues{title: "t", open: 2}
	e := gatedEnv(t, g)
	e.board.tick()
	ended := busySession("a", tNow)
	ended.Ended, ended.Live = true, model.Of(false)
	e.board.applySync(ended)
	e.clock.Set(tNow.Add(issuesEvery))
	e.board.tick()
	e.board.mu.Lock()
	left := len(e.board.issues)
	e.board.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d repositories kept after the last project left", left)
	}
}

func TestAWorktreeWithAnotherOriginIsNotAskedAbout(t *testing.T) {
	g := &gatedIssues{title: "t", open: 2}
	e := gatedEnv(t, g)
	wt := alpha + "-wt"
	e.repos.set(wt, model.Repo{Root: wt, MainRoot: alpha, Remote: model.GitHubRepo(betaRepo), Branch: model.Of("feature")})
	s := busySession("w", tNow)
	s.Folder = wt
	e.board.applySync(s)
	e.board.tick()
	shown := projectNamed(t, e.board, "alpha").Repo
	c := g.calls()
	if len(c) != 1 || len(c[0]) != 1 || c[0][0].Path != shown {
		t.Fatalf("asked %+v; the project shows %q only", c, shown)
	}
	l, _ := e.board.Issues(context.Background())
	if len(l.Repos) != 1 || l.Repos[0].Repo != shown {
		t.Fatalf("list %+v", l.Repos)
	}
}

// denyHost turns host down as a failed check does. Callers do not hold b.mu.
func denyHost(b *Board, host string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.glHosts[host] = hostDecision{at: b.now(), why: source.ErrNotLoggedIn}
	b.bumpHostLocked(host)
}

// gitlabIssuesEnv: alpha on the GitLab repository whose host passed its
// check, with gl as the GitLab lister and gh as the GitHub one; beta on a
// GitHub repository.
func gitlabIssuesEnv(t *testing.T, gl, gh source.IssueLister) (*testEnv, model.ForgeRepo) {
	t.Helper()
	e, _ := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl, model.ForgeGitHub: gh}
	})
	repo := model.GitLabRepo(glHost, glPath)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repo, Branch: model.Of("main")})
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: model.GitHubRepo(betaRepo), Branch: model.Of("main")})
	e.board.applySync(busySession("a", tNow))
	b := busySession("b", tNow)
	b.Folder = beta
	e.board.applySync(b)
	e.board.tick() // the host check runs
	return e, repo
}

func TestALateGitLabAnswerAfterTheHostWasDeniedIsDropped(t *testing.T) {
	gl := &gatedIssues{title: "earlier login's title", open: 3}
	gate := make(chan struct{})
	gl.gate = gate
	e, _ := gitlabIssuesEnv(t, gl, &gatedIssues{title: "t", open: 1})
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.mu.Lock()
	e.board.startIssuesLocked(context.Background(), e.board.now())
	e.board.mu.Unlock()
	for len(gl.calls()) == 0 {
		time.Sleep(time.Millisecond)
	}
	denyHost(e.board, glHost)
	close(gate)
	e.board.resolving.Wait()
	e.board.mu.Lock()
	_, kept := e.board.issues[e.board.issueKey(model.GitLabRepo(glHost, glPath))]
	e.board.mu.Unlock()
	if kept {
		t.Fatal("an answer for a host denied meanwhile was kept")
	}
}

func TestAGitLabHostDeniedDuringALookIsNotAsked(t *testing.T) {
	gl := &gatedIssues{title: "t", open: 3}
	gh := &gatedIssues{title: "t", open: 1}
	e, _ := gitlabIssuesEnv(t, gl, gh)
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick() // both asked at once: next time GitHub's repository sorts first
	before, ghBefore := len(gl.calls()), len(gh.calls())
	if before == 0 {
		t.Fatal("GitLab was never asked")
	}
	gate := make(chan struct{})
	gh.set(func(g *gatedIssues) { g.gate = gate })
	oneAtATime(t) // GitLab's batch waits for GitHub's, which is held
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	e.board.mu.Lock()
	e.board.startIssuesLocked(context.Background(), e.board.now())
	e.board.mu.Unlock()
	for len(gh.calls()) == ghBefore {
		time.Sleep(time.Millisecond)
	}
	denyHost(e.board, glHost)
	close(gate)
	e.board.resolving.Wait()
	if n := len(gl.calls()); n != before {
		t.Fatalf("GitLab was asked about a host denied during the look (%d calls)", n-before)
	}
}

func TestAGitLabHostWithListedIssuesMayBeLinked(t *testing.T) {
	gl := &gatedIssues{title: "t", open: 3}
	e, _ := gitlabIssuesEnv(t, gl, &gatedIssues{title: "t", open: 1})
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	if p := projectNamed(t, e.board, "alpha"); !p.OpenIssues.Known {
		t.Fatalf("alpha %+v", p.OpenIssues)
	}
	if hosts := e.board.Snapshot().GitLabHosts; !slices.Contains(hosts, glHost) {
		t.Fatalf("gitlabHosts %v: the page could not link the issues", hosts)
	}
}

func TestAnAnswerForAProjectThatLeftDuringTheLookIsNotKept(t *testing.T) {
	g := &gatedIssues{title: "t", open: 2}
	e := gatedEnv(t, g)
	gate := make(chan struct{})
	g.set(func(g *gatedIssues) { g.gate = gate })
	e.board.mu.Lock()
	e.board.startIssuesLocked(context.Background(), e.board.now())
	e.board.mu.Unlock()
	for len(g.calls()) == 0 {
		time.Sleep(time.Millisecond)
	}
	ended := busySession("a", tNow)
	ended.Ended, ended.Live = true, model.Of(false)
	e.board.Apply(ended) // not applySync: that waits for the look
	close(gate)
	e.board.resolving.Wait()
	e.board.mu.Lock()
	left := len(e.board.issues)
	e.board.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d answers kept for a project that left during the look", left)
	}
}

// rowsAndLost answers with rows and ErrNotLoggedIn together, as an adapter
// might that lost its login after a good chunk.
type rowsAndLost struct{}

func (rowsAndLost) OpenIssues(_ context.Context, repos []model.ForgeRepo, _ map[model.ForgeRepo][]int) ([]model.RepoIssues, error) {
	var out []model.RepoIssues
	for _, r := range repos {
		out = append(out, model.RepoIssues{Repo: r, State: model.RepoIssuesOK, Open: 9, List: []model.OpenIssue{{Number: 1, Title: "private"}}})
	}
	return out, fmt.Errorf("github: not logged in: %w", source.ErrNotLoggedIn)
}

func TestRowsThatCameWithALostLoginAreNotListed(t *testing.T) {
	g := &gatedIssues{title: "t", open: 4}
	e := gatedEnv(t, g)
	e.board.tick()
	e.board.mu.Lock()
	e.board.opts.Issues[model.ForgeGitHub] = rowsAndLost{}
	e.board.mu.Unlock()
	e.clock.Set(tNow.Add(issuesEvery))
	e.board.tick()
	if p := projectNamed(t, e.board, "alpha"); p.OpenIssues.Known {
		t.Fatalf("rows of a login that was lost are listed: %+v", p.OpenIssues)
	}
}

func TestALostLoginDropsEverythingItListedEvenWhatWasNotAskedThisTime(t *testing.T) {
	g := &gatedIssues{title: "t", open: 1}
	e := gatedEnv(t, g)
	for i := range maxIssueRepos {
		folder := fmt.Sprintf("%s/projects/p%03d", testHome, i)
		e.repos.set(folder, model.Repo{Root: folder, MainRoot: folder, Remote: model.GitHubRepo(fmt.Sprintf("sample-org/r%03d", i)), Branch: model.Of("main")})
		s := busySession(fmt.Sprintf("p%03d", i), tNow)
		s.Folder = folder
		e.board.applySync(s)
	}
	e.board.tick()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick() // the repository the first look left out is answered now; 100 listed in all
	e.board.mu.Lock()
	listed := len(e.board.issues)
	e.board.mu.Unlock()
	if listed != maxIssueRepos+1 {
		t.Fatalf("%d repositories listed, want %d", listed, maxIssueRepos+1)
	}
	g.set(func(g *gatedIssues) { g.err = fmt.Errorf("github: not logged in: %w", source.ErrNotLoggedIn) })
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	e.board.tick()
	e.board.mu.Lock()
	left := len(e.board.issues)
	e.board.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d repositories stay listed after the login was lost (this look asked about %d only)", left, maxIssueRepos)
	}
}

func TestAProjectTakesItsRepositoryFromTheMainCheckoutEveryTime(t *testing.T) {
	g := &gatedIssues{title: "t", open: 2}
	e := gatedEnv(t, g)
	for i := range 6 {
		wt := fmt.Sprintf("%s-wt%d", alpha, i)
		e.repos.set(wt, model.Repo{Root: wt, MainRoot: alpha, Remote: model.GitHubRepo(fmt.Sprintf("other-org/wt-%d", i)), Branch: model.Of("feature")})
		s := busySession(fmt.Sprintf("0w%d", i), tNow) // sorts before the main checkout's "a": the key alone would pick a worktree
		s.Folder = wt
		e.board.applySync(s)
	}
	want := model.GitHubRepo(exRepo)
	for i := range 40 {
		e.board.mu.Lock()
		e.board.rebuildLocked()
		repos := e.board.view.issueRepos
		e.board.mu.Unlock()
		if len(repos) != 1 || e.board.issueKey(repos[0]) != e.board.issueKey(want) {
			t.Fatalf("rebuild %d: asks about %v, want only the main checkout's %v", i, repos, want)
		}
		if p := projectNamed(t, e.board, "alpha"); p.Repo != want.Path {
			t.Fatalf("rebuild %d: project shows %q", i, p.Repo)
		}
	}
}

func TestARepositoryThatJoinsOrAnAnswerThatComesIsANewRevision(t *testing.T) {
	g := &gatedIssues{title: "t", open: 2}
	e := gatedEnv(t, g)
	e.board.tick()
	s1 := e.board.Snapshot().Issues
	if again := e.board.Snapshot().Issues; again.Rev != s1.Rev {
		t.Fatalf("rev moved from %d to %d with nothing new", s1.Rev, again.Rev)
	}
	// A project on a second repository joins; its look has not run yet.
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: model.GitHubRepo(betaRepo), Branch: model.Of("main")})
	b := busySession("b", tNow)
	b.Folder = beta
	e.board.applySync(b)
	s2 := e.board.Snapshot().Issues
	if !s2.AsOf.Equal(s1.AsOf) {
		t.Fatalf("asOf moved to %v: the test needs a change that leaves it", s2.AsOf)
	}
	if s2.Rev <= s1.Rev {
		t.Fatalf("rev %d after a repository joined, was %d: an open panel would not fetch again", s2.Rev, s1.Rev)
	}
}

func TestAHostDeniedAndAllowedAgainDuringALookDropsTheAnswerStagedBefore(t *testing.T) {
	gl := &gatedIssues{title: "earlier login's title", open: 3}
	gh := &gatedIssues{title: "t", open: 1}
	e, _ := gitlabIssuesEnv(t, gl, gh)
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick() // both asked once: GitHub's repository sorts first next time
	gate := make(chan struct{})
	gl.set(func(g *gatedIssues) { g.gate = gate })
	e.board.mu.Lock()
	clear(e.board.issues)
	e.board.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	e.board.mu.Lock()
	e.board.startIssuesLocked(context.Background(), e.board.now())
	e.board.mu.Unlock()
	for len(gl.calls()) < 2 {
		time.Sleep(time.Millisecond)
	}
	// GitHub's answer is staged; GitLab's is still out. The host goes and a
	// later login brings it back before the look ends.
	denyHost(e.board, glHost)
	e.board.mu.Lock()
	e.board.glHosts[glHost] = hostDecision{at: e.board.now(), ok: true}
	e.board.mu.Unlock()
	close(gate)
	e.board.resolving.Wait()
	e.board.mu.Lock()
	_, kept := e.board.issues[e.board.issueKey(model.GitLabRepo(glHost, glPath))]
	e.board.mu.Unlock()
	if kept {
		t.Fatal("an answer asked for under an earlier login of the host was kept")
	}
}

func TestADeniedHostLosesTheIssuesListedFromIt(t *testing.T) {
	gl := &gatedIssues{title: "t", open: 3}
	e, repo := gitlabIssuesEnv(t, gl, &gatedIssues{title: "t", open: 1})
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	e.board.mu.Lock()
	_, had := e.board.issues[e.board.issueKey(repo)]
	e.board.mu.Unlock()
	if !had {
		t.Fatal("the GitLab repository was never listed")
	}
	before := e.board.Snapshot().Issues
	e.board.mu.Lock()
	e.board.noteHostLocked(glHost, source.ErrNotLoggedIn)
	_, kept := e.board.issues[e.board.issueKey(repo)]
	e.board.rebuildLocked() // the check's caller does
	e.board.mu.Unlock()
	if kept {
		t.Fatal("the denied host's issues stay in memory")
	}
	if after := e.board.Snapshot().Issues; after.Rev == before.Rev && !after.AsOf.After(before.AsOf) {
		t.Fatalf("the page was not told: %+v then %+v", before, after)
	}
}

func TestAProjectWithNoSessionInTheMainCheckoutTakesTheLowestKeysRepository(t *testing.T) {
	e := newEnv(t)
	e.board = newBoard(t, Options{
		Repos: e.repos, PRs: e.prs, State: e.state, Now: e.clock.Now, StaleAfter: 20 * time.Minute,
		Issues: map[model.Forge]source.IssueLister{model.ForgeGitHub: &gatedIssues{title: "t", open: 2}},
	})
	for i := range 6 {
		wt := fmt.Sprintf("%s-wt%d", alpha, i)
		e.repos.set(wt, model.Repo{Root: wt, MainRoot: alpha, Remote: model.GitHubRepo(fmt.Sprintf("other-org/wt-%d", i)), Branch: model.Of("feature")})
		s := busySession(fmt.Sprintf("w%d", i), tNow)
		s.Folder = wt
		e.board.applySync(s)
	}
	want := model.GitHubRepo("other-org/wt-0")
	for i := range 40 {
		e.board.mu.Lock()
		e.board.rebuildLocked()
		repos := e.board.view.issueRepos
		e.board.mu.Unlock()
		if len(repos) != 1 || e.board.issueKey(repos[0]) != e.board.issueKey(want) {
			t.Fatalf("rebuild %d: asks about %v, want the lowest key's %v", i, repos, want)
		}
	}
}

func TestAStateThatChangesWithNoRowsChangingIsANewRevision(t *testing.T) {
	g := &gatedIssues{title: "t", open: 1, err: errors.New("github: could not ask")}
	e := gatedEnv(t, g)
	before := e.board.Snapshot().Issues
	if before.State != contract.IssuesLoading {
		t.Fatalf("state %q before the first look, want loading", before.State)
	}
	e.board.tick() // the look fails: nothing is listed, and loading turns ready
	after := e.board.Snapshot().Issues
	if after.State != contract.IssuesReady || after.Rev <= before.Rev {
		t.Fatalf("%+v then %+v: an open panel would not fetch the list again", before, after)
	}
}

func TestAnAnswerListedBeforeAHostWasDeniedAndAllowedAgainStaysGone(t *testing.T) {
	gl := &gatedIssues{title: "earlier login's title", open: 3}
	gh := &gatedIssues{title: "t", open: 1}
	e, repo := gitlabIssuesEnv(t, gl, gh)
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	gate := make(chan struct{})
	gh.set(func(g *gatedIssues) { g.gate = gate })
	before := len(gh.calls())
	e.board.mu.Lock()
	clear(e.board.issues)
	delete(e.board.issuesTried, e.board.issueKey(repo)) // never asked: GitLab goes first and GitHub is the one still out
	e.board.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	e.board.mu.Lock()
	e.board.startIssuesLocked(context.Background(), e.board.now())
	e.board.mu.Unlock()
	for len(gh.calls()) == before {
		time.Sleep(time.Millisecond)
	}
	// GitLab's answer is listed the moment it came; GitHub's is still out.
	// The host's login goes (its rows with it) and a later login brings the
	// host back before the look ends: nothing of the earlier login returns.
	e.board.mu.Lock()
	e.board.noteHostLocked(glHost, source.ErrNotLoggedIn)
	e.board.noteHostLocked(glHost, nil)
	e.board.mu.Unlock()
	close(gate)
	e.board.resolving.Wait()
	e.board.mu.Lock()
	_, kept := e.board.issues[e.board.issueKey(repo)]
	e.board.mu.Unlock()
	if kept {
		t.Fatal("an answer staged under an earlier login of the host was installed")
	}
}

func TestAHostThatCameBackIsAskedAgainSoonAsAnAskNeverMade(t *testing.T) {
	gl := &gatedIssues{title: "t", open: 3}
	e, _ := gitlabIssuesEnv(t, gl, &gatedIssues{title: "t", open: 1})
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	asked := len(gl.calls())
	if asked == 0 {
		t.Fatal("the GitLab repository was never asked")
	}
	e.board.mu.Lock()
	e.board.noteHostLocked(glHost, source.ErrNotLoggedIn)
	e.board.glHosts[glHost] = hostDecision{at: e.board.now(), ok: true} // a later login
	e.board.rebuildLocked()
	e.board.mu.Unlock()
	e.clock.Set(tNow.Add(2 * contract.IssuesFreshFor))
	e.board.tick()
	e.board.resolving.Wait()
	if len(gl.calls()) == asked {
		t.Fatal("the repository of a host that was forgotten waits out the ten minutes: its earlier ask still counts")
	}
}

func TestAForgeThatLostItsLoginDoesNotTakeAnotherForgesRowsWithIt(t *testing.T) {
	gl := &gatedIssues{title: "t", open: 3}
	gh := &gatedIssues{title: "t", open: 1}
	e, repo := gitlabIssuesEnv(t, gl, gh)
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	e.board.mu.Lock()
	_, had := e.board.issues[e.board.issueKey(repo)]
	e.board.mu.Unlock()
	if !had {
		t.Fatal("the GitLab repository was never listed")
	}
	gh.set(func(g *gatedIssues) { g.err = fmt.Errorf("github: not logged in: %w", source.ErrNotLoggedIn) })
	gl.set(func(g *gatedIssues) { g.err = errors.New("glab: could not ask") }) // GitLab fails the same look without losing its login
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	e.board.tick()
	e.board.resolving.Wait()
	e.board.mu.Lock()
	_, glKept := e.board.issues[e.board.issueKey(repo)]
	_, ghKept := e.board.issues[e.board.issueKey(model.GitHubRepo(betaRepo))]
	e.board.mu.Unlock()
	if !glKept {
		t.Fatal("GitLab's rows went with GitHub's login")
	}
	if ghKept {
		t.Fatal("GitHub's rows stay after its login was lost")
	}
}

func TestAHostThatIsForgottenAndAllowedAgainAtOnceTellsThePage(t *testing.T) {
	gl := &gatedIssues{title: "t", open: 3}
	e, _ := gitlabIssuesEnv(t, gl, &gatedIssues{title: "t", open: 1})
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	before := e.board.Snapshot().Issues
	e.board.mu.Lock()
	e.board.noteHostLocked(glHost, source.ErrNotLoggedIn)
	e.board.glHosts[glHost] = hostDecision{at: e.board.now(), ok: true} // the same repositories are askable again
	e.board.rebuildLocked()
	e.board.mu.Unlock()
	after := e.board.Snapshot().Issues
	if after.Rev <= before.Rev {
		t.Fatalf("%+v then %+v: the rows of the forgotten host went and the page was not told", before, after)
	}
}

func githubLine(t *testing.T, b *Board) contract.SourceStatus {
	t.Helper()
	for _, s := range b.Snapshot().Sources {
		if s.Kind == contract.SourceGitHub {
			return s
		}
	}
	t.Fatal("no GitHub source line")
	return contract.SourceStatus{}
}

// Rule: after an issue look confirms gh's login is gone, no issue title
// from GitHub is on the board or in its memory and the GitHub source line
// says the login is gone, until a later look answers again.
func TestALostLoginShowsOnTheGitHubLineUntilALookAnswersAgain(t *testing.T) {
	g := &gatedIssues{title: "private title", open: 2}
	e := gatedEnv(t, g)
	e.board.SetSources([]source.Status{{Kind: source.KindGitHub, State: source.StateOK, Version: "2.104.0", Tested: true}})
	e.board.tick()
	if l := githubLine(t, e.board); l.State != contract.SourceOK || firstTitle(t, e.board) != "private title" {
		t.Fatalf("first look: line %+v", l)
	}

	if s := e.board.Snapshot().Issues; s.Purged != 0 {
		t.Fatalf("a look that only added rows marked a purge: %+v", s)
	}
	g.set(func(g *gatedIssues) { g.err = fmt.Errorf("github: not logged in: %w", source.ErrNotLoggedIn) })
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	e.board.tick()
	// The page learns of the drop from the snapshot (Purged), and drops what it holds.
	if s := e.board.Snapshot().Issues; s.Purged == 0 || s.Purged != s.Rev {
		t.Fatalf("the drop was not marked as a purge: %+v", s)
	}
	if l := githubLine(t, e.board); l.State != contract.SourceFailing || l.Detail != "not logged in to github.com" || l.Version != "2.104.0" {
		t.Fatalf("after the login went the line is %+v, want failing: not logged in to github.com", l)
	}
	e.board.mu.Lock()
	left, page := len(e.board.issues), e.board.issuesPage
	e.board.mu.Unlock()
	if page != nil {
		t.Fatal("the rendered list keeps the titles of a lost login until the next GET")
	}
	l, _ := e.board.Issues(context.Background())
	if left != 0 || len(l.Repos) != 1 || len(l.Repos[0].Issues) != 0 || projectNamed(t, e.board, "alpha").OpenIssues.Known {
		t.Fatalf("after the login went: %d rows kept, list %+v", left, l)
	}

	// A look that fails for another reason, or is held by a rate limit, says
	// nothing about the login.
	for i, err := range []error{errors.New("github: gh failed"), fmt.Errorf("github: %w", source.ErrRateLimited)} {
		g.set(func(g *gatedIssues) { g.err = err })
		e.clock.Set(tNow.Add(contract.IssuesFreshFor + time.Duration(2+i)*issuesEvery))
		e.board.tick()
		if l := githubLine(t, e.board); l.State != contract.SourceFailing {
			t.Fatalf("a look that said %v cleared the lost login: %+v", err, l)
		}
	}

	g.set(func(g *gatedIssues) { g.err, g.title = nil, "after login" })
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + 4*issuesEvery))
	e.board.tick()
	if l := githubLine(t, e.board); l.State != contract.SourceOK || l.Detail != "" {
		t.Fatalf("after a look answered again the line is %+v, want ok", l)
	}
	if firstTitle(t, e.board) != "after login" {
		t.Fatal("the new answer was not listed")
	}
	// A probe that ran again (SetSources) keeps what the looks said.
	e.board.SetSources([]source.Status{{Kind: source.KindGitHub, State: source.StateFailing, Detail: "not logged in to github.com"}})
	if l := githubLine(t, e.board); l.State != contract.SourceOK {
		t.Fatalf("a stale probe's lost login hides a look that answered: %+v", l)
	}
}

// Rule: a session anywhere inside the main checkout, a subfolder too,
// decides its project's repository before any linked worktree.
func TestASessionInASubfolderOfTheMainCheckoutDecidesTheRepository(t *testing.T) {
	g := &gatedIssues{title: "t", open: 2}
	e := gatedEnv(t, g)
	sub := alpha + "/src"
	e.repos.set(sub, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	main := busySession("a", tNow)
	main.Folder = sub
	e.board.applySync(main)
	wt := alpha + "-wt"
	e.repos.set(wt, model.Repo{Root: wt, MainRoot: alpha, Remote: model.GitHubRepo("other-org/wt"), Branch: model.Of("feature")})
	w := busySession("0w", tNow) // sorts before "a": the key alone would pick the worktree
	w.Folder = wt
	e.board.applySync(w)
	e.board.tick()
	want := model.GitHubRepo(exRepo)
	if p := projectNamed(t, e.board, "alpha"); p.Repo != want.Path {
		t.Fatalf("project shows %q, want %q", p.Repo, want.Path)
	}
	calls := g.calls()
	if len(calls) == 0 || len(calls[len(calls)-1]) != 1 || e.board.issueKey(calls[len(calls)-1][0]) != e.board.issueKey(want) {
		t.Fatalf("asked about %v, want only %v", calls, want)
	}
}

// fillHostCache notes n made-up GitLab hosts, which clears the host
// decisions once the cache is full.
func fillHostCache(b *Board, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range n {
		b.noteHostLocked(fmt.Sprintf("h%03d.sample.test", i), nil)
	}
}

// Rule: a GitLab issue answer is installed only when the host decision the
// look started under stands unbroken until it is installed; a full host
// cache that drops every decision breaks it, even when the host is allowed
// again before the look ends.
func TestAHostCacheClearedDuringALookDropsTheAnswer(t *testing.T) {
	for _, order := range []string{"evict-stage-allow", "stage-evict-allow"} {
		t.Run(order, func(t *testing.T) {
			gl := &gatedIssues{title: "earlier title", open: 3}
			gh := &gatedIssues{title: "t", open: 1}
			e, repo := gitlabIssuesEnv(t, gl, gh)
			e.clock.Set(tNow.Add(contract.IssuesFreshFor))
			e.board.tick()
			glGate, ghGate := make(chan struct{}), make(chan struct{})
			gl.set(func(g *gatedIssues) {
				if order == "evict-stage-allow" {
					g.gate = glGate
				}
			})
			gh.set(func(g *gatedIssues) { g.gate = ghGate })
			e.board.mu.Lock()
			clear(e.board.issues)
			// GitLab's repository is asked first, GitHub's after it.
			e.board.issuesTried[e.board.issueKey(repo)] = time.Time{}
			e.board.issuesTried[e.board.issueKey(model.GitHubRepo(betaRepo))] = e.board.now()
			e.board.mu.Unlock()
			e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
			glAsked, ghAsked := len(gl.calls()), len(gh.calls())
			e.board.mu.Lock()
			e.board.startIssuesLocked(context.Background(), e.board.now())
			e.board.mu.Unlock()
			if order == "evict-stage-allow" {
				for len(gl.calls()) == glAsked {
					time.Sleep(time.Millisecond)
				}
				fillHostCache(e.board, 256)
				close(glGate)
			}
			for len(gh.calls()) == ghAsked { // GitLab's answer is staged once GitHub is asked
				time.Sleep(time.Millisecond)
			}
			if order == "stage-evict-allow" {
				fillHostCache(e.board, 256)
			}
			e.board.mu.Lock()
			e.board.noteHostLocked(glHost, nil)
			e.board.mu.Unlock()
			close(ghGate)
			e.board.resolving.Wait()
			e.board.mu.Lock()
			_, kept := e.board.issues[e.board.issueKey(repo)]
			_, ghKept := e.board.issues[e.board.issueKey(model.GitHubRepo(betaRepo))]
			e.board.mu.Unlock()
			if len(gl.calls()) != glAsked+1 || !ghKept {
				t.Fatalf("GitLab asked %d times, GitHub's answer kept %v: the look did not run as ordered", len(gl.calls())-glAsked, ghKept)
			}
			if kept {
				t.Fatal("an answer asked for under a host decision the full cache dropped was kept")
			}
		})
	}
}

// Rule: the list says the rev it is of, and how long the server would
// answer a GET without a new look, counted on the server's clock from the
// latest look's start.
func TestTheListSaysItsRevAndHowLongTheServerKeepsIt(t *testing.T) {
	g := &gatedIssues{title: "t", open: 2}
	e := gatedEnv(t, g)
	e.board.tick() // a look at tNow
	e.clock.Set(tNow.Add(20 * time.Second))
	l, _ := e.board.Issues(context.Background())
	if l.Rev != e.board.Snapshot().Issues.Rev || l.Rev == 0 {
		t.Fatalf("list rev %d, summary rev %d", l.Rev, e.board.Snapshot().Issues.Rev)
	}
	if l.FreshMs != 40_000 {
		t.Fatalf("freshMs = %d 20 s after the look, want 40000", l.FreshMs)
	}
	e.clock.Set(tNow.Add(contract.IssuesFreshFor - time.Millisecond))
	if l, _ := e.board.Issues(context.Background()); l.FreshMs != 1 {
		t.Fatalf("freshMs = %d a millisecond before the gate, want 1", l.FreshMs)
	}
	// At a minute this GET asks for a look: that look starts now.
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	if l, _ := e.board.Issues(context.Background()); l.FreshMs != contract.IssuesFreshFor.Milliseconds() {
		t.Fatalf("freshMs = %d for a GET that asks for a look, want a full minute", l.FreshMs)
	}
	// A look under way that started 10 s ago: what is left of its minute.
	e.board.mu.Lock()
	e.board.issuesBusy, e.board.issuesAt = true, e.board.now().Add(-10*time.Second)
	e.board.mu.Unlock()
	if l, _ := e.board.Issues(context.Background()); l.FreshMs != 50_000 {
		t.Fatalf("freshMs = %d while a look of 10 s ago runs, want 50000", l.FreshMs)
	}
	e.board.mu.Lock()
	e.board.issuesBusy = false
	e.board.mu.Unlock()
}

// Rule: rows of a repository the login can no longer see leave like any
// other drop: a purge the page acts on, with the rendered list gone at once.
func TestARepositoryTheLoginNoLongerSeesIsAPurge(t *testing.T) {
	g := &gatedIssues{title: "private title", open: 2}
	e := gatedEnv(t, g)
	e.board.tick()
	firstTitle(t, e.board) // renders the list
	before := e.board.Snapshot().Issues
	g.set(func(g *gatedIssues) { g.state = model.RepoIssuesNotFound })
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	e.board.tick()
	s := e.board.Snapshot().Issues
	if s.Purged <= before.Rev || s.Purged != s.Rev {
		t.Fatalf("not found after rows were listed: %+v (before %+v), want a purge", s, before)
	}
	e.board.mu.Lock()
	page := e.board.issuesPage
	e.board.mu.Unlock()
	if page != nil {
		t.Fatal("the rendered list keeps the titles")
	}
	l, _ := e.board.Issues(context.Background())
	if len(l.Repos) != 1 || l.Repos[0].State != contract.RepoIssuesNotFound || len(l.Repos[0].Issues) != 0 {
		t.Fatalf("list %+v", l)
	}
	// Not found again is no new purge.
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + 2*issuesEvery))
	e.board.tick()
	if p := e.board.Snapshot().Issues.Purged; p != s.Purged {
		t.Fatalf("purged moved to %d on a second not found", p)
	}
}

// Rule: rows listed from a login are evidence it works, even when a later
// part of the same look was rate limited.
func TestRowsListedWithALimitShowTheLoginWorks(t *testing.T) {
	g := &gatedIssues{title: "t", open: 2}
	e := gatedEnv(t, g)
	e.board.SetSources([]source.Status{{Kind: source.KindGitHub, State: source.StateFailing, Detail: "not logged in to github.com"}})
	g.set(func(g *gatedIssues) { g.err, g.withErr = fmt.Errorf("github: %w", source.ErrRateLimited), true })
	e.board.tick()
	if l := githubLine(t, e.board); l.State != contract.SourceOK {
		t.Fatalf("rows were listed, yet the line says %+v", l)
	}
}

// Rule: switching issues off drops every row, and the off summary says so,
// so a tab drops the list it holds.
func TestSwitchingIssuesOffPublishesThePurge(t *testing.T) {
	g := &gatedIssues{title: "t", open: 2}
	e := gatedEnv(t, g)
	e.board.SetSources([]source.Status{{Kind: source.KindGitHub, State: source.StateOK}})
	e.board.tick()
	before := e.board.Snapshot().Issues
	e.board.SetSources([]source.Status{{Kind: source.KindGitHub, State: source.StateOff}})
	e.clock.Set(tNow.Add(time.Minute))
	e.board.tick()
	s := e.board.Snapshot().Issues
	if s.State != contract.IssuesOff || s.Purged <= before.Rev {
		t.Fatalf("off summary %+v (before %+v), want the purge in it", s, before)
	}
}

// Rule: GitLab rows rest on their host's decision: when a full cache drops
// every decision, the rows go too, and allowing the host again does not
// bring them back.
func TestAHostCacheClearDropsTheGitLabRowsListed(t *testing.T) {
	gl := &gatedIssues{title: "earlier title", open: 3}
	e, repo := gitlabIssuesEnv(t, gl, &gatedIssues{title: "t", open: 1})
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	e.board.mu.Lock()
	_, listed := e.board.issues[e.board.issueKey(repo)]
	e.board.mu.Unlock()
	if !listed {
		t.Fatal("setup: GitLab rows not listed")
	}
	before := e.board.Snapshot().Issues.Rev
	fillHostCache(e.board, 256)
	e.board.mu.Lock()
	e.board.noteHostLocked(glHost, nil)
	_, kept := e.board.issues[e.board.issueKey(repo)]
	_, tried := e.board.issuesTried[e.board.issueKey(repo)]
	_, ghKept := e.board.issues[e.board.issueKey(model.GitHubRepo(betaRepo))]
	e.board.rebuildLocked()
	e.board.mu.Unlock()
	if kept || tried || !ghKept {
		t.Fatalf("after the clear and a new yes: GitLab rows kept %v, tried kept %v, GitHub rows kept %v", kept, tried, ghKept)
	}
	if p := e.board.Snapshot().Issues.Purged; p <= before {
		t.Fatalf("purged %d, want past %d", p, before)
	}
}

func listedFor(b *Board, r model.ForgeRepo) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.issues[b.issueKey(r)]
	return ok
}

// Rule: when a pull request lookup finds a repository gone (this login no
// longer sees it), its issue rows leave as a purge, and an issue look that
// started before that brings none of them back.
// gateCh holds gated looks until open, which a test that fails early
// calls too, at its end: its board's cleanup waits for those looks.
type gateCh struct {
	ch   chan struct{}
	once sync.Once
}

func (g *gateCh) open() { g.once.Do(func() { close(g.ch) }) }

func openOnEnd(t *testing.T) *gateCh {
	g := &gateCh{ch: make(chan struct{})}
	t.Cleanup(g.open)
	return g
}

func TestAPullRequestLookupThatFindsTheRepositoryGoneIsAPurge(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(fmt.Sprintf("during a look %v", during), func(t *testing.T) {
			g := &gatedIssues{title: "private title", open: 2}
			e := gatedEnv(t, g)
			e.board.tick()
			repo := model.GitHubRepo(exRepo)
			if !listedFor(e.board, repo) {
				t.Fatal("setup: not listed")
			}
			before := e.board.Snapshot().Issues
			// A pull request never asked about, of a repository this login
			// no longer sees: the next refresh looks it up.
			e.prs.mu.Lock()
			e.prs.err = fmt.Errorf("github: %w", source.ErrRepoNotFound)
			e.prs.mu.Unlock()
			s := busySession("pr", tNow)
			s.CreatedPRs = []model.PRRef{{Repo: repo, Number: 1}}
			e.board.applySync(s)
			gate := openOnEnd(t)
			if during {
				g.set(func(g *gatedIssues) { g.gate = gate.ch })
				e.clock.Set(tNow.Add(issuesEvery))
				e.board.mu.Lock()
				e.board.startIssuesLocked(context.Background(), e.board.now())
				busy := e.board.issuesBusy
				e.board.mu.Unlock()
				if !busy {
					t.Fatal("setup: no look under way")
				}
			} else {
				e.clock.Set(tNow.Add(30 * time.Second))
			}
			calls := e.prs.callsFor(exRepo)
			e.board.refreshForges(context.Background(), false)
			if e.prs.callsFor(exRepo) == calls {
				t.Fatal("setup: the pull request was not looked up")
			}
			if p := e.board.Snapshot().Issues.Purged; p <= before.Rev {
				t.Fatalf("purged %d, want past %d", p, before.Rev)
			}
			if listedFor(e.board, repo) {
				t.Fatal("the issue rows of a repository found gone stay")
			}
			gate.open()
			e.board.resolving.Wait()
			if listedFor(e.board, repo) {
				t.Fatal("a look that started before the purge brought the rows back")
			}
		})
	}
}

// Rule: no snapshot says issues are off while the board still holds rows
// or a rendered list, even with a look under way; and a look started before
// that brings nothing back when issues come on again.
func TestSwitchingOffDuringALookClearsAtOnceAndKeepsItsAnswerOut(t *testing.T) {
	g := &gatedIssues{title: "private title", open: 2}
	e := gatedEnv(t, g)
	on := []source.Status{{Kind: source.KindGitHub, State: source.StateOK}}
	e.board.SetSources(on)
	e.board.tick()
	firstTitle(t, e.board)
	gate := openOnEnd(t)
	g.set(func(g *gatedIssues) { g.gate = gate.ch })
	e.clock.Set(tNow.Add(issuesEvery))
	e.board.mu.Lock()
	e.board.startIssuesLocked(context.Background(), e.board.now()) // a look, held
	busy := e.board.issuesBusy
	e.board.mu.Unlock()
	if !busy {
		t.Fatal("setup: no look under way")
	}
	e.board.SetSources([]source.Status{{Kind: source.KindGitHub, State: source.StateOff}})
	s := e.board.Snapshot().Issues
	e.board.mu.Lock()
	rows, page := len(e.board.issues), e.board.issuesPage
	e.board.mu.Unlock()
	if s.State != contract.IssuesOff || rows != 0 || page != nil || s.Purged == 0 || s.Rev != s.Purged {
		t.Fatalf("off snapshot %+v with %d rows, page %v", s, rows, page != nil)
	}
	// The GET of a tab that saw the purge is current.
	if l, _ := e.board.Issues(context.Background()); l.Rev != s.Rev {
		t.Fatalf("off list rev %d, summary rev %d", l.Rev, s.Rev)
	}
	e.board.SetSources(on)
	gate.open()
	e.board.resolving.Wait()
	if listedFor(e.board, model.GitHubRepo(exRepo)) {
		t.Fatal("the look from before the switch-off brought its rows back")
	}
}

// oneAtATime makes a look run its batches one after another for the test.
func oneAtATime(t *testing.T) {
	t.Helper()
	was := issueBatchSlots
	issueBatchSlots = 1
	t.Cleanup(func() { issueBatchSlots = was })
}

package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Review round 3 of the GitLab issue lister (#174): one generation per glab
// host name (issueGen); each test here fails without its part.

// glPort is glHost on another web port: the same glab login.
const glPort = glHost + ":8443"

// twoPortsEnv: alpha on glHost and beta on glPort, both listed by a first
// look.
func twoPortsEnv(t *testing.T) (*testEnv, *fakeGitLab, *hostIssues, model.ForgeRepo, model.ForgeRepo) {
	t.Helper()
	gl := newHostIssues()
	e, glab := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	glab.setHost(glPort, nil)
	repoA, repoB := model.GitLabRepo(glHost, glPath), model.GitLabRepo(glPort, glPath)
	gl.by[repoA], gl.by[repoB] = sampleIssues(repoA, 4, 1), sampleIssues(repoB, 2, 1)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repoA, Branch: model.Of("main")})
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: repoB, Branch: model.Of("main")})
	b := busySession("b", tNow)
	b.Folder = beta
	e.board.applySync(busySession("a", tNow))
	e.board.applySync(b)
	e.board.tick() // the host checks run
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick() // the first look
	if !listedFor(e.board, repoA) || !listedFor(e.board, repoB) {
		t.Fatalf("setup: listed %v %v", listedFor(e.board, repoA), listedFor(e.board, repoB))
	}
	drain(gl.entered)
	return e, glab, gl, repoA, repoB
}

// P1: the alternate port's refusal hides the rows of both sites at once (one
// glab login), and a check that cannot answer keeps them hidden.
func TestARefusalOnOnePortHidesEverySiteOfTheName(t *testing.T) {
	e, glab, gl, repoA, repoB := twoPortsEnv(t)
	refused := fmt.Errorf("gitlab: %w", source.ErrTokenRefused)
	gate := openOnEnd(t)
	gl.mu.Lock()
	gl.gate[glHost] = gate.ch
	gl.err[glHost], gl.err[glPort] = refused, refused
	gl.mu.Unlock()
	start := tNow.Add(contract.IssuesFreshFor + issuesEvery)
	e.clock.Set(start)
	startLook(e.board)
	waitFor(t, e.board, "the port's refusal applied", func() bool {
		_, ok := e.board.issues[e.board.issueKey(repoB)]
		return !ok
	})
	if listedFor(e.board, repoA) {
		t.Fatal("the other port's rows stay listed after a refusal of the same login")
	}
	gate.open()
	e.board.resolving.Wait()
	gl.mu.Lock()
	clear(gl.err)
	gl.mu.Unlock()
	glab.setHost(glHost, errors.New("glab: timed out")) // no answer
	glab.setHost(glPort, errors.New("glab: timed out"))
	e.board.tick() // the forced checks get no answer
	drain(gl.entered)
	e.clock.Set(start.Add(issuesRefusedHold - time.Second))
	if _, err := e.board.Issues(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.board.tick()
	e.board.resolving.Wait()
	select {
	case h := <-gl.entered:
		t.Fatalf("%s was asked about issues inside the hold", h)
	default:
	}
	if listedFor(e.board, repoA) || listedFor(e.board, repoB) {
		t.Fatalf("rows came back inside the hold: %v %v", listedFor(e.board, repoA), listedFor(e.board, repoB))
	}
}

// P2: a check that began before the refusal and says yes after it does not
// release the hold: only a check made since may.
func TestALatePositiveFromAnOlderCheckKeepsTheHold(t *testing.T) {
	gl := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	e, glab := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	repo := model.GitLabRepo(glHost, glPath)
	gl.by[repo] = sampleIssues(repo, 4, 1)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repo, Branch: model.Of("main")})
	e.board.applySync(busySession("a", tNow))
	e.board.tick() // the host check runs
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick() // the first look
	if !listedFor(e.board, repo) {
		t.Fatal("setup: not listed")
	}
	// A check under way, held in glab.
	hold, entered := make(chan struct{}), make(chan string, 4)
	t.Cleanup(func() {
		select {
		case <-hold:
		default:
			close(hold)
		}
	})
	glab.hmu.Lock()
	glab.holds = map[string]chan struct{}{glHost: hold}
	glab.entered = entered
	glab.hmu.Unlock()
	e.board.mu.Lock()
	d := e.board.glHosts[glHost]
	d.at = time.Time{}
	e.board.glHosts[glHost] = d
	e.board.startHostChecksLocked(context.Background())
	e.board.mu.Unlock()
	<-entered
	// The look that GitLab refuses.
	gl.mu.Lock()
	gl.err = fmt.Errorf("gitlab: %w", source.ErrTokenRefused)
	gl.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	startLook(e.board)
	e.board.resolving.Wait()
	if listedFor(e.board, repo) {
		t.Fatal("setup: the refusal left the rows")
	}
	close(hold) // the older check says yes now
	e.board.hostChecks.Wait()
	e.board.mu.Lock()
	_, held := e.board.issuesHold[model.GitLabHostname(glHost)]
	lister := e.board.issueListerLocked(repo)
	e.board.mu.Unlock()
	if !held || lister != nil {
		t.Fatalf("an older check's yes released the hold: held %v, lister %v", held, lister != nil)
	}
}

// P2: on the first look (nothing listed yet) a pull request lookup finds
// the repository gone; the older issue answer that arrives after is dropped
// whole, and the repository counts as not asked.
func TestAnAccessLossBeforeTheFirstAnswerDropsIt(t *testing.T) {
	g := &gatedIssues{title: "private title", open: 2}
	gate := openOnEnd(t)
	g.gate = gate.ch
	e := gatedEnv(t, g)
	repo := model.GitHubRepo(exRepo)
	// A pull request never asked about, of a repository this login no
	// longer sees: the next refresh looks it up.
	e.prs.mu.Lock()
	e.prs.err = fmt.Errorf("github: %w", source.ErrRepoNotFound)
	e.prs.mu.Unlock()
	s := busySession("pr", tNow)
	s.CreatedPRs = []model.PRRef{{Repo: repo, Number: 1}}
	e.board.applySync(s)
	e.board.mu.Lock()
	e.board.startIssuesLocked(context.Background(), e.board.now())
	busy := e.board.issuesBusy
	e.board.mu.Unlock()
	if !busy || listedFor(e.board, repo) {
		t.Fatalf("setup: busy %v, listed %v", busy, listedFor(e.board, repo))
	}
	waitFor(t, e.board, "the look asked", func() bool { return len(g.calls()) > 0 })
	calls := e.prs.callsFor(exRepo)
	e.board.refreshForges(context.Background(), false)
	if e.prs.callsFor(exRepo) == calls {
		t.Fatal("setup: the pull request was not looked up")
	}
	gate.open()
	e.board.resolving.Wait()
	if listedFor(e.board, repo) {
		t.Fatal("an answer asked before the repository was found gone was applied")
	}
	e.board.mu.Lock()
	_, tried := e.board.issuesTried[e.board.issueKey(repo)]
	e.board.mu.Unlock()
	if tried {
		t.Fatal("a dropped answer's repository counts as asked")
	}
}

// A GitHub logout an issue look finds takes its rows off the page even when
// the generation moved while it was out (another repository found gone).
func TestAGitHubLogoutOfAnOlderGenerationStillHidesTheRows(t *testing.T) {
	g := &gatedIssues{title: "private title", open: 2}
	e := gatedEnv(t, g)
	other := model.GitHubRepo("example-org/example-other")
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: other, Branch: model.Of("main")})
	b := busySession("b", tNow)
	b.Folder = beta
	e.board.applySync(b)
	e.board.tick()
	repo := model.GitHubRepo(exRepo)
	if !listedFor(e.board, repo) || !listedFor(e.board, other) {
		t.Fatal("setup: not listed")
	}
	e.prs.mu.Lock()
	e.prs.err = fmt.Errorf("github: %w", source.ErrRepoNotFound)
	e.prs.mu.Unlock()
	s := busySession("pr", tNow)
	s.CreatedPRs = []model.PRRef{{Repo: other, Number: 1}}
	e.board.applySync(s)
	gate := openOnEnd(t)
	g.set(func(g *gatedIssues) {
		g.gate = gate.ch
		g.err = fmt.Errorf("github: %w", source.ErrNotLoggedIn)
	})
	e.clock.Set(tNow.Add(issuesEvery))
	calls := len(g.calls())
	e.board.mu.Lock()
	e.board.startIssuesLocked(context.Background(), e.board.now())
	e.board.mu.Unlock()
	waitFor(t, e.board, "the look asked", func() bool { return len(g.calls()) > calls })
	e.board.refreshForges(context.Background(), false) // other is gone: the generation moves
	gate.open()
	e.board.resolving.Wait()
	if listedFor(e.board, repo) {
		t.Fatal("a GitHub logout from a look of an older generation left its rows")
	}
}

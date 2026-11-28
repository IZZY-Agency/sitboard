package core

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Review round 2 of the GitLab issue lister (#174): each test here fails
// without the fix it is named for.

// waitFor spins (no clock) until cond holds under b.mu; a look that never
// gets there fails the test instead of hanging it.
func waitFor(t *testing.T, b *Board, what string, cond func() bool) {
	t.Helper()
	for range 2_000_000 {
		b.mu.Lock()
		ok := cond()
		b.mu.Unlock()
		if ok {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("never happened: %s", what)
}

// waitEntered waits until a call about host began.
func waitEntered(t *testing.T, gl *hostIssues, host string) {
	t.Helper()
	for got := range gl.entered {
		if got == host {
			return
		}
	}
}

// P1: host A's login is recorded gone from outside the look (a host check)
// while host B's call is out; B's answer, that its project is no longer
// visible, still stands: A's purge is not B's.
func TestAnExternalPurgeOfOneHostDoesNotVoidAnotherHostsAnswer(t *testing.T) {
	e, _, gl, repoA, repoB := twoHostsEnv(t)
	if repoState(t, e.board, repoB) != contract.RepoIssuesOK {
		t.Fatal("B was not listed by the first look")
	}
	gate := make(chan struct{})
	gl.mu.Lock()
	gl.gate[glOther] = gate
	gl.by[repoB] = model.RepoIssues{Repo: repoB, State: model.RepoIssuesNotFound}
	gl.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	startLook(e.board)
	waitEntered(t, gl, glOther)
	// A host check, not the look, finds A's login gone, now.
	e.board.mu.Lock()
	e.board.noteHostLocked(glHost, errFakeNotLoggedIn)
	e.board.mu.Unlock()
	if listedFor(e.board, repoA) {
		t.Fatal("setup: A's rows stay after its login went")
	}
	close(gate)
	e.board.resolving.Wait()
	if s := repoState(t, e.board, repoB); s != contract.RepoIssuesNotFound {
		t.Fatalf("B's answer was voided by A's purge: state %q", s)
	}
}

// P2: a GitHub login found gone by the first batch hides its rows at once,
// while a GitLab host's call is still out.
func TestAGitHubLoginFoundGoneLeavesThePageWhileAGitLabHostIsStillAsked(t *testing.T) {
	hub := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	lab := newHostIssues()
	e, _ := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitHub: hub, model.ForgeGitLab: lab}
	})
	repoH, repoL := model.GitHubRepo(exRepo), model.GitLabRepo(glHost, glPath)
	hub.by[repoH], lab.by[repoL] = sampleIssues(repoH, 3, 1), sampleIssues(repoL, 4, 1)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repoH, Branch: model.Of("main")})
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: repoL, Branch: model.Of("main")})
	b := busySession("b", tNow)
	b.Folder = beta
	e.board.applySync(busySession("a", tNow))
	e.board.applySync(b)
	e.board.tick() // the host check runs
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick() // the first look lists both
	drain(lab.entered)
	if !listedFor(e.board, repoH) || !listedFor(e.board, repoL) {
		t.Fatal("setup: not both listed")
	}
	gate := make(chan struct{})
	lab.mu.Lock()
	lab.gate[glHost] = gate
	lab.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	hub.mu.Lock()
	hub.err = fmt.Errorf("github: %w", source.ErrNotLoggedIn)
	hub.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	startLook(e.board)
	waitEntered(t, lab, glHost)
	waitFor(t, e.board, "GitHub rows leave while GitLab is out", func() bool {
		_, kept := e.board.issues[e.board.issueKey(repoH)]
		return !kept
	})
	if s := e.board.Snapshot().Issues; s.Open != 4 {
		t.Fatalf("the lost login's rows are still published: open %d", s.Open)
	}
	close(gate)
	e.board.resolving.Wait()
}

// P3: an answer carries the time it arrived, not the time its look started:
// the clock moves while a host's call is out.
func TestALaterBatchCarriesItsOwnAnswerTime(t *testing.T) {
	hub := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}} // answers nothing
	lab := newHostIssues()
	e, _ := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitHub: hub, model.ForgeGitLab: lab}
	})
	repoH, repoL := model.GitHubRepo(exRepo), model.GitLabRepo(glHost, glPath)
	lab.by[repoL] = sampleIssues(repoL, 4, 1)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repoH, Branch: model.Of("main")})
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: repoL, Branch: model.Of("main")})
	b := busySession("b", tNow)
	b.Folder = beta
	e.board.applySync(busySession("a", tNow))
	e.board.applySync(b)
	e.board.tick() // the host check runs
	gate := make(chan struct{})
	lab.mu.Lock()
	lab.gate[glHost] = gate
	lab.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	start := tNow.Add(contract.IssuesFreshFor)
	e.clock.Set(start)
	startLook(e.board)
	waitEntered(t, lab, glHost)
	late := start.Add(2 * time.Minute)
	e.clock.Set(late)
	close(gate)
	e.board.resolving.Wait()
	if got := e.board.Snapshot().Issues.AsOf; !got.Equal(late) {
		t.Fatalf("asOf %v, want the time the answer came, %v (the look started at %v)", got, late, start)
	}
}

// P1: a token GitLab refused hides the host's rows at once whatever the login
// check finds, and the host is left alone for issuesRefusedHold.
func TestARefusedTokenHidesTheRowsAndLeavesTheHostAloneWhateverTheCheckSays(t *testing.T) {
	refused := fmt.Errorf("gitlab: %w", source.ErrTokenRefused)
	setup := func(t *testing.T) (*testEnv, *fakeGitLab, *fakeIssues, model.ForgeRepo) {
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
		if p := projectNamed(t, e.board, "alpha"); !p.OpenIssues.Known || gl.callCount() != 1 {
			t.Fatalf("setup: %+v after %d calls", p.OpenIssues, gl.callCount())
		}
		gl.mu.Lock()
		gl.err = refused
		gl.mu.Unlock()
		e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
		startLook(e.board)
		e.board.resolving.Wait()
		if gl.callCount() != 2 {
			t.Fatalf("setup: the refusing look made %d calls", gl.callCount())
		}
		return e, glab, gl, repo
	}

	t.Run("rows leave before any check answers", func(t *testing.T) {
		e, _, _, repo := setup(t)
		if listedFor(e.board, repo) {
			t.Fatal("a refused token's rows stay listed")
		}
		if p := projectNamed(t, e.board, "alpha"); p.OpenIssues.Known {
			t.Fatalf("alpha still counts: %+v", p.OpenIssues)
		}
	})

	t.Run("a check that cannot answer changes nothing, the host is not asked again", func(t *testing.T) {
		e, glab, gl, repo := setup(t)
		glab.setHost(glHost, errors.New("glab: timed out")) // no answer
		gl.mu.Lock()
		gl.err = nil
		gl.mu.Unlock()
		e.board.tick() // the forced check runs and gets no answer
		checks := len(glab.asked())
		// Inside the hold, with the issue throttle long over: no issue call.
		e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery + issuesRefusedHold - time.Second))
		if _, err := e.board.Issues(context.Background()); err != nil {
			t.Fatal(err)
		}
		e.board.tick()
		if n := gl.callCount(); n != 2 {
			t.Fatalf("the host was asked about issues again inside the hold: %d calls", n)
		}
		if listedFor(e.board, repo) {
			t.Fatal("rows came back inside the hold")
		}
		// No look, so no check forced by one (the periodic check is the
		// provider's own, one a minute).
		if forced := len(glab.asked()) - checks; forced > 1 {
			t.Fatalf("%d checks inside the hold", forced)
		}
		// The hold over: asked again, the rows return.
		e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery + issuesRefusedHold + time.Second))
		if _, err := e.board.Issues(context.Background()); err != nil {
			t.Fatal(err)
		}
		e.board.tick() // the published repositories still lack the host's: this tick rebuilds them
		e.board.tick()
		if n := gl.callCount(); n != 3 || !listedFor(e.board, repo) {
			t.Fatalf("after the hold: %d calls, listed %v", n, listedFor(e.board, repo))
		}
	})

	t.Run("a check that says yes ends the hold", func(t *testing.T) {
		e, _, gl, repo := setup(t)
		gl.mu.Lock()
		gl.err = nil
		gl.mu.Unlock()
		e.board.tick() // the forced check says yes
		e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery + contract.IssuesFreshFor))
		if _, err := e.board.Issues(context.Background()); err != nil {
			t.Fatal(err)
		}
		e.board.tick()
		if n := gl.callCount(); n != 3 || !listedFor(e.board, repo) {
			t.Fatalf("after a yes: %d calls, listed %v", n, listedFor(e.board, repo))
		}
	})

	t.Run("a check that says no denies the host", func(t *testing.T) {
		e, glab, _, repo := setup(t)
		glab.setHost(glHost, errFakeNotLoggedIn)
		e.board.tick()
		e.board.mu.Lock()
		denied := !e.board.glHosts[glHost].ok
		e.board.mu.Unlock()
		if !denied || listedFor(e.board, repo) {
			t.Fatalf("denied %v, listed %v", denied, listedFor(e.board, repo))
		}
	})
}

// P2: a GitLab under a folder answers only that spelling of the mount, so
// "/tools/group/app" and "/Tools/group/app" are two projects, while the case
// of what follows the mount is still one.
func TestTwoCaseDistinctMountsAreTwoProjectsAndOneMountFoldsTheRest(t *testing.T) {
	gl := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	e, glab := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	glab.subs = map[string]string{glHost: "tools"}
	lower, upper, mixed := model.GitLabRepo(glHost, "tools/group/app"), model.GitLabRepo(glHost, "Tools/group/app"), model.GitLabRepo(glHost, "tools/Group/App")
	for i, r := range []model.ForgeRepo{lower, upper, mixed} {
		root := fmt.Sprintf("/work/p%d", i)
		e.repos.set(root, model.Repo{Root: root, MainRoot: root, Remote: r, Branch: model.Of("main")})
		s := busySession(fmt.Sprintf("s%d", i), tNow)
		s.Folder = root
		e.board.applySync(s)
	}
	e.board.tick() // the host check runs: its mount is "tools"
	e.board.mu.Lock()
	got, same := e.board.issueKey(lower), e.board.issueKey(mixed)
	other := e.board.issueKey(upper)
	e.board.mu.Unlock()
	if got != same {
		t.Fatalf("case after the mount made two keys: %+v and %+v", got, same)
	}
	if got == other {
		t.Fatalf("two mounts that differ in case share a key: %+v", got)
	}
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	asked := gl.lastCall()
	var names []string
	for _, r := range asked {
		names = append(names, r.Path)
	}
	if len(asked) != 2 || !strings.Contains(strings.Join(names, " "), "Tools/group/app") || !strings.Contains(strings.Join(names, " "), "tools/") {
		t.Fatalf("asked %v, want one per mount spelling", names)
	}
}

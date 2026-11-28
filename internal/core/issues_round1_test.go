package core

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Review round 1 of the GitLab issue lister (#174): each test here fails
// without the fix it is named for.

const glOther = "gitlab.other.example"

// hostIssues is a GitLab lister that answers per host: it can fail a host's
// call and hold it until a gate closes.
type hostIssues struct {
	mu      sync.Mutex
	by      map[model.ForgeRepo]model.RepoIssues
	err     map[string]error
	gate    map[string]chan struct{}
	entered chan string // the host of each call that began (buffered)
}

func newHostIssues() *hostIssues {
	return &hostIssues{
		by:      map[model.ForgeRepo]model.RepoIssues{},
		err:     map[string]error{},
		gate:    map[string]chan struct{}{},
		entered: make(chan string, 16),
	}
}

func (h *hostIssues) OpenIssues(ctx context.Context, repos []model.ForgeRepo, _ map[model.ForgeRepo][]int) ([]model.RepoIssues, error) {
	host := repos[0].Host
	h.mu.Lock()
	gate, err := h.gate[host], h.err[host]
	h.mu.Unlock()
	h.entered <- host
	if gate != nil {
		<-gate
	}
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []model.RepoIssues
	for _, r := range repos {
		if ri, ok := h.by[r]; ok {
			out = append(out, ri)
		}
	}
	return out, nil
}

// twoHostsEnv: alpha on glHost and beta on glOther, both with a login, both
// listed by a first look. glHost sorts first, so it is asked first.
func twoHostsEnv(t *testing.T) (*testEnv, *fakeGitLab, *hostIssues, model.ForgeRepo, model.ForgeRepo) {
	t.Helper()
	gl := newHostIssues()
	e, glab := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	glab.setHost(glOther, nil)
	repoA, repoB := model.GitLabRepo(glHost, glPath), model.GitLabRepo(glOther, glPath)
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
	drain(gl.entered)
	return e, glab, gl, repoA, repoB
}

func drain(c chan string) {
	for {
		select {
		case <-c:
		default:
			return
		}
	}
}

func startLook(b *Board) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.startIssuesLocked(context.Background(), b.now())
}

func repoState(t *testing.T, b *Board, repo model.ForgeRepo) contract.RepoIssuesState {
	t.Helper()
	l, err := b.Issues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range l.Repos {
		if r.Host == repo.Host && r.Repo == repo.Path {
			return r.State
		}
	}
	return ""
}

// P1: host A loses its login in a look that also learns something about host
// B; A's purge is the look's own, so B's answer (here: the project is gone for
// this login) still stands and B's old rows leave.
func TestAHostLosingItsLoginDoesNotVoidAnotherHostsAnswer(t *testing.T) {
	e, _, gl, repoA, repoB := twoHostsEnv(t)
	if repoState(t, e.board, repoB) != contract.RepoIssuesOK {
		t.Fatal("B was not listed by the first look")
	}
	gl.mu.Lock()
	gl.err[glHost] = fmt.Errorf("gitlab: %w", source.ErrNotLoggedIn)
	gl.by[repoB] = model.RepoIssues{Repo: repoB, State: model.RepoIssuesNotFound}
	gl.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	e.board.tick()
	e.board.mu.Lock()
	_, keptA := e.board.issues[e.board.issueKey(repoA)]
	e.board.mu.Unlock()
	if keptA {
		t.Fatal("A's rows stay after its login went")
	}
	if s := repoState(t, e.board, repoB); s != contract.RepoIssuesNotFound {
		t.Fatalf("B's answer was voided by A's purge: state %q", s)
	}
	if p := projectNamed(t, e.board, "beta"); p.OpenIssues.Known {
		t.Fatalf("beta still counts: %+v", p.OpenIssues)
	}
}

// P2: the denial reaches the published snapshot at once, not when the whole
// look is over.
func TestAHostDeniedInALookLeavesTheSnapshotBeforeTheLookEnds(t *testing.T) {
	e, _, gl, _, _ := twoHostsEnv(t)
	if s := e.board.Snapshot().Issues; s.Open != 6 {
		t.Fatalf("before: %+v", s)
	}
	gate := make(chan struct{})
	gl.mu.Lock()
	gl.err[glHost] = fmt.Errorf("gitlab: %w", source.ErrNotLoggedIn)
	gl.gate[glOther] = gate
	gl.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	startLook(e.board)
	for denied := false; !denied; runtime.Gosched() {
		e.board.mu.Lock()
		denied = !e.board.glHosts[glHost].ok // the denial and its publishing are one lock hold
		e.board.mu.Unlock()
	}
	// B's call is held (batches run side by side): A was denied meanwhile.
	s := e.board.Snapshot().Issues
	al := projectNamed(t, e.board, "alpha")
	close(gate)
	e.board.resolving.Wait()
	if s.Open != 2 || al.OpenIssues.Known {
		t.Fatalf("the denied host's rows are still published while the look runs: open %d, alpha %+v", s.Open, al.OpenIssues)
	}
}

// P2: a host that does not answer holds up no other host: while its call is
// out, another host's answer is listed (batches run side by side, each with a
// context of its own).
func TestAHostThatDoesNotAnswerHoldsUpNoOtherHost(t *testing.T) {
	e, _, gl, _, repoB := twoHostsEnv(t)
	gate := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	gl.mu.Lock()
	gl.gate[glHost] = gate // glHost sorts first
	gl.by[repoB] = sampleIssues(repoB, 9, 1)
	gl.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	startLook(e.board)
	waitFor(t, e.board, "B's new answer while A's call is out", func() bool {
		return e.board.issues[e.board.issueKey(repoB)].Open == 9
	})
	close(gate)
	e.board.resolving.Wait()
}

// P2: a host whose login check ran out (glab made no call) leaves its
// repositories unasked, so the next look after the host's check asks them
// instead of waiting a whole ten minutes.
func TestARepositoryNotAskedBecauseTheHostWasNotCheckedIsAskedAgainSoon(t *testing.T) {
	gl := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	e, _ := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	repo := model.GitLabRepo(glHost, glPath)
	gl.by[repo] = sampleIssues(repo, 4, 1)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repo, Branch: model.Of("main")})
	e.board.applySync(busySession("a", tNow))
	e.board.tick()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	if gl.callCount() != 1 {
		t.Fatalf("calls %d", gl.callCount())
	}
	gl.mu.Lock()
	gl.err = fmt.Errorf("gitlab: %w", source.ErrNotChecked)
	gl.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	e.board.tick()
	if gl.callCount() != 2 {
		t.Fatalf("calls %d", gl.callCount())
	}
	e.board.mu.Lock()
	_, tried := e.board.issuesTried[e.board.issueKey(repo)]
	e.board.mu.Unlock()
	if tried {
		t.Fatal("a repository glab was never asked about counts as asked")
	}
	gl.mu.Lock()
	gl.err = nil
	gl.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery + contract.IssuesFreshFor))
	e.board.tick()
	if gl.callCount() != 3 {
		t.Fatalf("not asked again a minute later: %d calls", gl.callCount())
	}
}

// P2: the host whose check ran out is checked at once, so a login that is
// gone leaves the page without waiting for the host's next turn.
func TestAHostThatWasNotCheckedForAnIssueLookIsCheckedAtOnce(t *testing.T) {
	gl := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	e, glab := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	repo := model.GitLabRepo(glHost, glPath)
	gl.by[repo] = sampleIssues(repo, 4, 1)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repo, Branch: model.Of("main")})
	e.board.applySync(busySession("a", tNow))
	e.board.tick()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	if p := projectNamed(t, e.board, "alpha"); !p.OpenIssues.Known {
		t.Fatalf("not listed: %+v", p.OpenIssues)
	}
	gl.mu.Lock()
	gl.err = fmt.Errorf("gitlab: %w", source.ErrNotChecked)
	gl.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	e.board.mu.Lock()
	e.board.glHosts[glHost] = hostDecision{at: e.board.now(), ok: true} // just checked
	e.board.startIssuesLocked(context.Background(), e.board.now())
	e.board.mu.Unlock()
	e.board.resolving.Wait()
	glab.setHost(glHost, source.ErrNotLoggedIn)
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery + time.Second)) // far short of hostEvery
	e.board.tick()
	e.board.mu.Lock()
	denied := !e.board.glHosts[glHost].ok
	_, kept := e.board.issues[e.board.issueKey(repo)]
	e.board.mu.Unlock()
	if !denied || kept {
		t.Fatalf("host not checked at once: denied %v, rows kept %v", denied, kept)
	}
}

// P2: "as of" is the age of the oldest answer shown, not the time of the
// latest look that changed anything.
func TestIssuesAsOfIsTheOldestAnswerShown(t *testing.T) {
	gl := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	e, _ := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	repoA, repoB := model.GitLabRepo(glHost, glPath), model.GitLabRepo(glHost, "example-group/other-project")
	gl.by[repoA], gl.by[repoB] = sampleIssues(repoA, 4, 1), sampleIssues(repoB, 2, 1)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repoA, Branch: model.Of("main")})
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: repoB, Branch: model.Of("main")})
	b := busySession("b", tNow)
	b.Folder = beta
	e.board.applySync(busySession("a", tNow))
	e.board.applySync(b)
	e.board.tick() // the host check runs
	first := tNow.Add(contract.IssuesFreshFor)
	e.clock.Set(first)
	e.board.tick()
	if got := e.board.Snapshot().Issues.AsOf; !got.Equal(first) {
		t.Fatalf("first look: asOf %v", got)
	}
	delete(gl.by, repoA) // the next look answers only B
	e.clock.Set(first.Add(issuesEvery))
	e.board.tick()
	if got := e.board.Snapshot().Issues.AsOf; !got.Equal(first) {
		t.Fatalf("snapshot asOf %v: A was read at %v", got, first)
	}
	l, err := e.board.Issues(context.Background())
	if err != nil || !l.AsOf.Equal(first) {
		t.Fatalf("list asOf %v, %v", l.AsOf, err)
	}
}

// P2: two origins that differ only in the case of a GitLab path are one
// project: asked once, counted once.
func TestAGitLabProjectInTwoSpellingsIsAskedAndCountedOnce(t *testing.T) {
	gl := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	e, _ := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	lower, upper := model.GitLabRepo(glHost, glPath), model.GitLabRepo(glHost, strings.ToUpper(glPath[:1])+glPath[1:])
	gl.by[lower], gl.by[upper] = sampleIssues(lower, 4, 1), sampleIssues(upper, 4, 1)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: lower, Branch: model.Of("main")})
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: upper, Branch: model.Of("main")})
	b := busySession("b", tNow)
	b.Folder = beta
	e.board.applySync(busySession("a", tNow))
	e.board.applySync(b)
	e.board.tick() // the host check runs
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	if n := len(gl.lastCall()); n != 1 {
		t.Fatalf("one project asked %d times: %+v", n, gl.lastCall())
	}
	if s := e.board.Snapshot().Issues; s.Open != 4 {
		t.Fatalf("counted %d, not 4", s.Open)
	}
}

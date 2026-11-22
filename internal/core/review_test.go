package core

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// On macOS the monotonic clock stops while the machine sleeps, so the tick
// must compare wall times. A real clock's values carry a monotonic reading;
// the board must not keep it.
func TestTickComparesWallClockNotMonotonic(t *testing.T) {
	t.Setenv("HOME", testHome)
	b := newBoard(t, Options{Now: time.Now, StaleAfter: 20 * time.Minute})
	b.tick()
	if strings.Contains(time.Now().String(), "m=") && strings.Contains(b.lastTick.String(), "m=") {
		t.Fatalf("lastTick keeps a monotonic reading: %s", b.lastTick)
	}
	// Go cannot build two readings whose monotonic and wall clocks disagree,
	// so the sleep itself is covered by TestSleepGapAloneIsNotStale; here we
	// pin that every time the tick keeps is wall-only.
	if b.resumedAt.IsZero() && strings.Contains(b.now().String(), "m=") {
		t.Fatalf("now() keeps a monotonic reading: %s", b.now())
	}
}

func TestWorkerWithoutLiveCoordinatorIsTopLevel(t *testing.T) {
	e := newEnv(t)
	gone := session("w1", model.StatusFinished, tNow)
	gone.Parent = model.Of(claudeKey("nobody"))
	endedCoord := session("c2", model.StatusFinished, tNow)
	endedCoord.Ended = true
	orphan := session("w2", model.StatusFinished, tNow)
	orphan.Parent = model.Of(claudeKey("c2"))
	liveCoord := session("c3", model.StatusBusy, tNow)
	child := session("w3", model.StatusFinished, tNow)
	child.Parent = model.Of(claudeKey("c3"))
	for _, s := range []model.SessionFact{gone, endedCoord, orphan, liveCoord, child} {
		e.board.applySync(s)
	}
	for _, id := range []string{"claude:w1", "claude:w2"} {
		c, _ := cardByID(t, e.board, id)
		if c.Column != contract.ColumnNeedsYou || !slices.Contains(c.Attention, contract.AttentionUnseen) {
			t.Errorf("%s: %q %v, want needs_you unseen", id, c.Column, c.Attention)
		}
		if c.Agent.Parent != contract.ParentKnown || c.Agent.ParentCardID != "" {
			t.Errorf("%s: parent %q card %q, want known with no card", id, c.Agent.Parent, c.Agent.ParentCardID)
		}
	}
	if c, _ := cardByID(t, e.board, "claude:w3"); c.Column != contract.ColumnWithCoordinator || c.Agent.ParentCardID != "claude:c3" {
		t.Errorf("w3: %q parent card %q", c.Column, c.Agent.ParentCardID)
	}
}

// gatedRepos holds every Resolve until the test answers it.
type gatedRepos struct {
	mu      sync.Mutex
	calls   []chan model.Repo
	folders []string
	arrived chan struct{}
}

func (g *gatedRepos) Probe(context.Context) source.Status { return source.Status{Kind: source.KindGit} }
func (g *gatedRepos) Resolve(ctx context.Context, folder string) (model.Repo, error) {
	ch := make(chan model.Repo, 1)
	g.mu.Lock()
	g.calls = append(g.calls, ch)
	g.folders = append(g.folders, folder)
	g.mu.Unlock()
	g.arrived <- struct{}{}
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		return model.Repo{}, ctx.Err()
	}
}

func (g *gatedRepos) answer(i int, r model.Repo) {
	g.mu.Lock()
	ch := g.calls[i]
	g.mu.Unlock()
	ch <- r
}

func TestSlowGitDoesNotFreezeTheBoard(t *testing.T) {
	t.Setenv("HOME", testHome)
	g := &gatedRepos{arrived: make(chan struct{}, 8)}
	b := newBoard(t, Options{Repos: g, Now: func() time.Time { return tNow }})

	slow := busySession("s1", tNow)
	done := make(chan struct{})
	go func() {
		b.Apply(slow) // git for alpha never answers until the test says so
		b.Apply(model.TerminalFact{ID: "t1"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Apply waited for git")
	}
	if s := b.Snapshot(); s.PlainShells != 1 {
		t.Fatalf("board did not update while git was busy: %+v", s)
	}
	if n := len(b.Snapshot().Cards); n != 0 {
		t.Fatalf("%d cards before git answered; a card waits for its project", n)
	}
	<-g.arrived
	g.answer(0, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo)})
	b.resolving.Wait()
	if c := onlyCard(t, b); c.ProjectID != contract.ProjectID(alpha) {
		t.Fatalf("project %q", c.ProjectID)
	}
}

func TestStaleGitAnswerIsDropped(t *testing.T) {
	t.Setenv("HOME", testHome)
	g := &gatedRepos{arrived: make(chan struct{}, 8)}
	b := newBoard(t, Options{Repos: g, Now: func() time.Time { return tNow }})
	start := func() {
		b.mu.Lock()
		b.startResolveLocked(alpha)
		b.mu.Unlock()
		<-g.arrived
	}
	start() // call 0: the older lookup
	start() // call 1: the newer lookup, e.g. the minute refresh
	g.answer(1, model.Repo{Root: alpha, MainRoot: alpha, Branch: model.Of("newer")})
	time.Sleep(20 * time.Millisecond)
	g.answer(0, model.Repo{Root: alpha, MainRoot: alpha, Branch: model.Of("older")}) // arrives last
	b.resolving.Wait()
	b.mu.Lock()
	kept := b.repos[alpha].Branch.V
	b.mu.Unlock()
	if kept != "newer" {
		t.Fatalf("kept %q: a late answer to an older lookup overwrote the newer one", kept)
	}
}

func TestInvalidRepoNeverReachesLookup(t *testing.T) {
	e := newEnv(t)
	s := session("s1", model.StatusBusy, tNow)
	for _, bad := range []string{"../x", "x/..", "a/b/c", "a b/c", "evil;rm/x", "-x/y\n", "", "owner"} {
		s.CreatedPRs = append(s.CreatedPRs, model.PRRef{Repo: model.GitHubRepo(bad), Number: 1})
	}
	e.board.applySync(s)
	e.board.tick()
	e.prs.mu.Lock()
	calls := len(e.prs.calls)
	e.prs.mu.Unlock()
	if calls != 0 {
		t.Fatalf("%d lookups for invalid repos: %+v", calls, e.prs.calls)
	}
	s.CreatedPRs = append(s.CreatedPRs, model.PRRef{Repo: model.GitHubRepo("example-org/example.repo_1"), Number: 1})
	e.board.applySync(s)
	e.board.tick()
	if n := e.prs.callsFor("example-org/example.repo_1"); n != 1 {
		t.Fatalf("%d lookups for a valid repo", n)
	}
}

func TestTurnOlderThan7DaysCountsAsSeen(t *testing.T) {
	e := newEnv(t)
	old := session("s1", model.StatusFinished, tNow.Add(-8*24*time.Hour))
	old.TurnEnds = []time.Time{tNow.Add(-8 * 24 * time.Hour)}
	recent := session("s2", model.StatusFinished, tNow.Add(-6*24*time.Hour))
	recent.TurnEnds = []time.Time{tNow.Add(-6 * 24 * time.Hour)}
	e.board.applySync(old)
	e.board.applySync(recent)
	if c, _ := cardByID(t, e.board, "claude:s1"); !c.Seen || c.Column != contract.ColumnIdle {
		t.Errorf("8-day-old turn: seen=%v %q, want seen in idle", c.Seen, c.Column)
	}
	if c, _ := cardByID(t, e.board, "claude:s2"); c.Seen || c.Column != contract.ColumnNeedsYou {
		t.Errorf("6-day-old unseen turn: seen=%v %q, want needs_you", c.Seen, c.Column)
	}
}

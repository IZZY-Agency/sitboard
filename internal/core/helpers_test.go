package core

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/app"
	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

var _ app.Board = (*Board)(nil)

const testHome = "/home/dev"

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// fakeRepos knows a fixed set of repos by folder prefix. It never changes anything.
type fakeRepos struct {
	mu    sync.Mutex
	repos map[string]model.Repo // folder -> repo
	calls int
}

func (f *fakeRepos) Probe(context.Context) source.Status {
	return source.Status{Kind: source.KindGit, State: source.StateOK}
}

func (f *fakeRepos) Resolve(_ context.Context, folder string) (model.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if r, ok := f.repos[folder]; ok {
		return r, nil
	}
	return model.Repo{Root: folder, MainRoot: folder}, nil
}

func (f *fakeRepos) Worktrees(context.Context, string) ([]model.Worktree, error) { return nil, nil }
func (f *fakeRepos) LocalBranches(context.Context, string) ([]string, error)     { return nil, nil }

func (f *fakeRepos) set(folder string, r model.Repo) {
	f.mu.Lock()
	f.repos[folder] = r
	f.mu.Unlock()
}

type lookupCall struct {
	repo     string
	numbers  []int
	branches []string
}

// fakePRs answers from a fixed set and records each call.
type fakePRs struct {
	mu    sync.Mutex
	prs   []model.PullRequest
	calls []lookupCall
}

func (f *fakePRs) Probe(context.Context) source.Status {
	return source.Status{Kind: source.KindGitHub, State: source.StateOK}
}

func (f *fakePRs) Lookup(_ context.Context, repo string, numbers []int, branches []string) ([]model.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, lookupCall{repo, numbers, branches})
	var out []model.PullRequest
	for _, pr := range f.prs {
		if pr.Repo != repo {
			continue
		}
		for _, n := range numbers {
			if pr.Number == n {
				out = append(out, pr)
			}
		}
		for _, br := range branches {
			if pr.HeadBranch == br {
				out = append(out, pr)
			}
		}
	}
	return out, nil
}

func (f *fakePRs) callsFor(repo string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.repo == repo {
			n++
		}
	}
	return n
}

type testEnv struct {
	board *Board
	clock *fakeClock
	repos *fakeRepos
	prs   *fakePRs
	state state.Store
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	t.Setenv("HOME", testHome)
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{
		clock: &fakeClock{t: tNow},
		repos: &fakeRepos{repos: map[string]model.Repo{}},
		prs:   &fakePRs{},
		state: st,
	}
	e.board = New(Options{Repos: e.repos, PRs: e.prs, State: st, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
	return e
}

func newTestBoard(t *testing.T) *Board { return newEnv(t).board }

func newClockBoard(t *testing.T) (*Board, *fakeClock) {
	e := newEnv(t)
	return e.board, e.clock
}

const (
	alpha    = testHome + "/projects/alpha"
	beta     = testHome + "/projects/beta"
	exRepo   = "example-org/example-repo"
	termUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

func claudeKey(id string) model.SessionKey { return model.SessionKey{Tool: model.ToolClaude, ID: id} }

func session(id string, status model.Status, at time.Time) model.SessionFact {
	return model.SessionFact{
		Key:          claudeKey(id),
		Name:         "worker-" + id,
		Model:        model.Of("claude-opus-4-7"),
		Status:       model.Of(status),
		StatusSince:  at,
		StartedAt:    at.Add(-time.Hour),
		LastActivity: at,
		Live:         model.Of(true),
		Folder:       alpha,
	}
}

func busySession(id string, at time.Time) model.SessionFact {
	return session(id, model.StatusBusy, at)
}

func onlyCard(t *testing.T, b *Board) contract.Card {
	t.Helper()
	s := b.Snapshot()
	if len(s.Cards) != 1 {
		t.Fatalf("%d cards, want 1: %+v", len(s.Cards), s.Cards)
	}
	return s.Cards[0]
}

func cardByID(t *testing.T, b *Board, id string) (contract.Card, bool) {
	t.Helper()
	for _, c := range b.Snapshot().Cards {
		if c.ID == id {
			return c, true
		}
	}
	return contract.Card{}, false
}

func hasNoAbsolutePath(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, testHome) {
		t.Fatalf("absolute home path leaked: %s", s)
	}
}

// applySync applies a fact and waits for any git lookup it started, so tests
// can read the board straight after.
func (b *Board) applySync(f model.Fact) {
	b.Apply(f)
	b.resolving.Wait()
}

func mustCardID(t *testing.T, tool, id string) string {
	t.Helper()
	c, err := contract.CardID(tool, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

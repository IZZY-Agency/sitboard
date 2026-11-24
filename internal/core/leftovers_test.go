package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/leftover"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// fakeInventory answers from fixed git facts and counts the looks.
type fakeInventory struct {
	mu      sync.Mutex
	inv     map[string]model.RepoInventory // by main root
	trees   map[string]model.TreeStatus    // by worktree
	looks   int
	ancetor map[[2]string]bool
	read    map[string]int  // status reads by folder
	fail    map[string]bool // by main root: inv is returned with an error
	// treeErr: by worktree, the status read fails so; ancErr: every
	// ancestry question fails so; during runs once, at the first look.
	treeErr map[string]error
	ancErr  error
	during  func()
}

func (f *fakeInventory) Inventory(_ context.Context, root string) (model.RepoInventory, error) {
	f.mu.Lock()
	during := f.during
	f.during = nil
	f.mu.Unlock()
	if during != nil {
		during()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.looks++
	inv, ok := f.inv[root]
	if !ok {
		return inv, errors.New("not a repo")
	}
	if f.fail[root] {
		return inv, errors.New("a partial clone")
	}
	return inv, nil
}

func (f *fakeInventory) IsAncestor(_ context.Context, _, a, b string) (bool, error) {
	if f.ancErr != nil {
		return false, f.ancErr
	}
	return a == b || f.ancetor[[2]string{a, b}], nil
}

func (f *fakeInventory) TreeStatus(_ context.Context, dir string) (model.TreeStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.read == nil {
		f.read = map[string]int{}
	}
	f.read[dir]++
	if err := f.treeErr[dir]; err != nil {
		return model.TreeStatus{}, err
	}
	st, ok := f.trees[dir]
	if !ok {
		return st, errors.New("no such worktree")
	}
	return st, nil
}

func (f *fakeInventory) treeLooks(dir string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.read[dir]
}

func (f *fakeInventory) lookCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.looks
}

// fakeFolders: processes working in cwds, one each (pids 1000 on, parent
// 1), and procs as given.
type fakeFolders struct {
	mu    sync.Mutex
	cwds  []string
	procs []model.Proc
}

func (f *fakeFolders) set(procs ...model.Proc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procs = procs
}

func (f *fakeFolders) Procs(context.Context) ([]model.Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.procs)
	for i, d := range f.cwds {
		out = append(out, model.Proc{PID: 1000 + i, PPID: 1, Cwd: d})
	}
	return out, nil
}

var (
	tipFeat  = strings.Repeat("a", 40)
	tipOther = strings.Repeat("b", 40)
	alphaWT  = testHome + "/projects/alpha-wt"
)

type leftoverEnv struct {
	*testEnv
	inv     *fakeInventory
	folders *fakeFolders
}

// newLeftoverEnv: repo alpha on the board through one session, with a
// worktree alpha-wt on branch feat, whose pull request #5 merged two days ago.
func newLeftoverEnv(t *testing.T, cwds []string) *leftoverEnv {
	t.Helper()
	t.Setenv("HOME", testHome)
	clock := &fakeClock{t: tNow}
	st, err := state.OpenAt(t.TempDir()+"/state.json", clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{clock: clock, repos: &fakeRepos{repos: map[string]model.Repo{}}, prs: &fakePRs{}, state: st}
	inv := &fakeInventory{
		inv: map[string]model.RepoInventory{alpha: {
			Worktrees: []model.Worktree{{Path: alpha, Main: true, Branch: model.Of("main")}, {Path: alphaWT, Branch: model.Of("feat")}},
			Branches:  []model.Branch{{Name: "main", Tip: tipOther}, {Name: "feat", Tip: tipFeat}},
			Origin:    model.GitHubRepo(exRepo), CommonDir: alpha + "/.git",
		}},
		trees: map[string]model.TreeStatus{alphaWT: {Clean: true, Ignored: 3, Top: alphaWT, GitDir: alpha + "/.git/worktrees/alpha-wt",
			CommonDir: alpha + "/.git", Head: "refs/heads/feat", Tip: tipFeat}},
	}
	folders := &fakeFolders{cwds: cwds}
	e.board = newBoard(t, Options{Repos: e.repos, PRs: e.prs, Inventory: inv, Folders: folders, State: st,
		Now: e.clock.Now, StaleAfter: 20 * time.Minute})
	e.board.SetSources([]source.Status{{Kind: source.KindGit, State: source.StateOK}, {Kind: source.KindGitHub, State: source.StateOK}})
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Branch: model.Of("main"), Remote: model.GitHubRepo(exRepo)})
	e.repos.set(alphaWT, model.Repo{Root: alphaWT, MainRoot: alpha, Branch: model.Of("feat"), Remote: model.GitHubRepo(exRepo)})
	e.prs.prs = []model.PullRequest{{Repo: model.GitHubRepo(exRepo), Number: 5, Title: "Sample change", HeadBranch: "feat", State: model.PRStateMerged,
		OpenedAt: tNow.Add(-72 * time.Hour), ClosedAt: tNow.Add(-48 * time.Hour), HeadOID: tipFeat, HeadRefOID: tipFeat,
		BaseBranch: "main", BaseIsDefault: true}}
	return &leftoverEnv{testEnv: e, inv: inv, folders: folders}
}

// workerPID is the process of the worker addWorker adds; workerProcs are it,
// the shell that started it and one it started, all in alpha-wt.
const workerPID = 4100

var workerProcs = []model.Proc{
	{PID: workerPID - 1, PPID: 1, Cwd: alphaWT},
	{PID: workerPID, PPID: workerPID - 1, Cwd: alphaWT},
	{PID: workerPID + 1, PPID: workerPID, Cwd: alphaWT + "/web"},
}

// worker in alpha-wt that created #5, finished an hour ago and was seen.
func (e *leftoverEnv) addWorker(t *testing.T, id string) {
	t.Helper()
	s := session(id, model.StatusFinished, tNow.Add(-time.Hour))
	s.Folder, s.PID = alphaWT, workerPID
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 5}}
	e.board.applySync(s)
	e.board.tick()
	if err := e.board.MarkSeen(mustCardID(t, "claude", id)); err != nil {
		t.Fatal(err)
	}
}

func list(t *testing.T, b *Board) contract.Leftovers {
	t.Helper()
	l, err := b.Leftovers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLeftoverWorktreeWithItsSession(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.folders.set(workerProcs...)
	e.addWorker(t, "s1")
	e.clock.Set(tNow.Add(leftoversEvery))
	e.board.tick()
	e.board.tick() // the count reaches the snapshot after the look

	l := list(t, e.board)
	if l.State != contract.LeftoversReady || len(l.Items) != 1 {
		t.Fatalf("list = %+v", l)
	}
	it := l.Items[0]
	if it.PullRequest.Number != 5 || !it.Waiting || !it.OwnSession {
		t.Fatalf("item = %+v", it)
	}
	kinds := []contract.LeftoverKind{}
	for _, p := range it.Parts {
		kinds = append(kinds, p.Kind)
	}
	want := []contract.LeftoverKind{contract.LeftoverSession, contract.LeftoverWorktree, contract.LeftoverBranch, contract.LeftoverRemoteBranch}
	if len(kinds) != len(want) {
		t.Fatalf("parts = %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("parts = %v, want %v", kinds, want)
		}
	}
	if it.Parts[0].CardID != "claude:s1" || it.Parts[1].Path != "~/projects/alpha-wt" || it.Parts[1].Ignored != 3 {
		t.Fatalf("parts = %+v", it.Parts)
	}
	if n := e.board.Snapshot().Leftovers; n != (contract.LeftoversSummary{Items: 1}) {
		t.Fatalf("snapshot count = %+v", n)
	}
	hasNoAbsolutePath(t, it.Parts[1].Path)
}

// The board's own sessions and terminals count by their folders, whatever
// folder their processes report (none here): an open session or terminal in
// the worktree holds it; the leftover session itself makes it wait.
func TestBoardKnownSessionsHoldTheWorktree(t *testing.T) {
	cases := map[string]struct {
		setup   func(e *leftoverEnv)
		waiting bool
	}{
		"its leftover session": {func(*leftoverEnv) {}, true},
		"its leftover session, in its own terminal": {func(e *leftoverEnv) {
			s := session("s1", model.StatusFinished, tNow.Add(-time.Hour))
			s.Folder, s.TerminalID = alphaWT, termUUID
			s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 5}}
			e.board.applySync(s)
			e.board.applySync(model.TerminalFact{ID: termUUID, Folder: alphaWT, HasAgent: true})
		}, true},
		"another open session": {func(e *leftoverEnv) {
			o := session("s9", model.StatusBusy, tNow)
			o.Folder = alphaWT + "/sub"
			e.board.applySync(o)
		}, false},
		"a session with unknown liveness": {func(e *leftoverEnv) {
			o := session("s9", model.StatusBusy, tNow)
			o.Folder, o.Live = alphaWT, model.Unknown[bool]()
			e.board.applySync(o)
		}, false},
		"a terminal": {func(e *leftoverEnv) {
			e.board.applySync(model.TerminalFact{ID: termUUID, Folder: alphaWT})
		}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newLeftoverEnv(t, nil)
			e.addWorker(t, "s1")
			c.setup(e)
			e.clock.Set(tNow.Add(leftoversEvery))
			e.board.tick()
			l := list(t, e.board)
			if c.waiting {
				if len(l.Items) != 1 || !l.Items[0].Waiting {
					t.Fatalf("list = %+v", l)
				}
				return
			}
			if len(l.Items) != 0 || len(l.Held) != 1 || l.Held[0].Reason != contract.HeldInUse {
				t.Fatalf("list = %+v", l)
			}
		})
	}
}

// A session whose folder is no leftover worktree is offered for closing on
// its own only from its repo's main checkout, on the default branch its pull
// request merged into, with clean files, in a repo looked at in full this
// time. Anything else is counted as held back.
func TestStandaloneSessionNeedsACleanDefaultCheckout(t *testing.T) {
	nested := alpha + "/.claude/worktrees/x"
	cases := map[string]struct {
		folder string
		setup  func(e *leftoverEnv) // before the session, or after it when late
		listed bool
		late   bool
	}{
		"clean default checkout": {alpha, func(*leftoverEnv) {}, true, false},
		"a subfolder of it":      {alpha + "/docs", func(*leftoverEnv) {}, true, false},
		"modified files": {alpha, func(e *leftoverEnv) {
			st := e.inv.trees[alpha]
			st.Clean = false
			e.inv.trees[alpha] = st
		}, false, false},
		"status fails": {alpha, func(e *leftoverEnv) { delete(e.inv.trees, alpha) }, false, false},
		"another branch": {alpha, func(e *leftoverEnv) {
			inv := e.inv.inv[alpha]
			inv.Worktrees[0].Branch = model.Of("other")
			inv.Branches = append(inv.Branches, model.Branch{Name: "other", Tip: tipOther})
			e.inv.inv[alpha] = inv
			st := e.inv.trees[alpha]
			st.Head = "refs/heads/other"
			e.inv.trees[alpha] = st
		}, false, false},
		"detached": {alpha, func(e *leftoverEnv) {
			inv := e.inv.inv[alpha]
			inv.Worktrees[0].Branch = model.Unknown[string]()
			e.inv.inv[alpha] = inv
			st := e.inv.trees[alpha]
			st.Head = ""
			e.inv.trees[alpha] = st
		}, false, false},
		"checkout moved since": {alpha, func(e *leftoverEnv) {
			st := e.inv.trees[alpha]
			st.Tip = tipFeat
			e.inv.trees[alpha] = st
		}, false, false},
		"switched since": {alpha, func(e *leftoverEnv) {
			st := e.inv.trees[alpha]
			st.Head = "refs/heads/other"
			e.inv.trees[alpha] = st
		}, false, false},
		"only the default branch here": {alpha, func(e *leftoverEnv) {
			inv := e.inv.inv[alpha]
			inv.Worktrees, inv.Branches = inv.Worktrees[:1], inv.Branches[:1]
			e.inv.inv[alpha] = inv
		}, true, false},
		"more branches than one look checks": {alpha, func(e *leftoverEnv) {
			inv := e.inv.inv[alpha]
			for i := range leftover.MaxNames {
				inv.Branches = append(inv.Branches, model.Branch{Name: fmt.Sprintf("b%d", i), Tip: tipOther})
			}
			e.inv.inv[alpha] = inv
		}, false, false},
		"a checkout of the default branch that is not the main one": {nested, func(e *leftoverEnv) {
			inv := e.inv.inv[alpha]
			inv.Worktrees = append(inv.Worktrees, model.Worktree{Path: nested, Branch: model.Of("main")})
			e.inv.inv[alpha] = inv
			e.inv.trees[nested] = model.TreeStatus{Clean: true, Top: nested, GitDir: alpha + "/.git", CommonDir: alpha + "/.git",
				Head: "refs/heads/main", Tip: tipOther}
		}, false, false},
		"merge under way": {alpha, func(e *leftoverEnv) {
			inv := e.inv.inv[alpha]
			inv.Worktrees[0].Operation = true
			e.inv.inv[alpha] = inv
		}, false, false},
		"base not the default branch": {alpha, func(e *leftoverEnv) { e.prs.prs[0].BaseIsDefault = false }, false, false},
		"a worktree that is no leftover": {nested, func(e *leftoverEnv) {
			inv := e.inv.inv[alpha]
			inv.Worktrees = append(inv.Worktrees, model.Worktree{Path: nested, Branch: model.Of("other")})
			e.inv.inv[alpha] = inv
		}, false, false},
		"git fails": {alpha, func(e *leftoverEnv) { delete(e.inv.inv, alpha) }, false, false},
		"GitHub fails": {alpha, func(e *leftoverEnv) {
			e.prs.mu.Lock()
			e.prs.err = errors.New("rate limited")
			e.prs.mu.Unlock()
		}, false, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newLeftoverEnv(t, nil)
			e.inv.trees[alpha] = model.TreeStatus{Clean: true, Ignored: 2, Top: alpha, GitDir: alpha + "/.git", CommonDir: alpha + "/.git",
				Head: "refs/heads/main", Tip: tipOther}
			e.repos.set(c.folder, model.Repo{Root: alpha, MainRoot: alpha, Branch: model.Of("main"), Remote: model.GitHubRepo(exRepo)})
			if !c.late {
				c.setup(e)
			}
			s := session("s1", model.StatusFinished, tNow.Add(-time.Hour))
			s.Folder = c.folder
			s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 5}}
			e.board.applySync(s)
			e.board.tick()
			if err := e.board.MarkSeen(mustCardID(t, "claude", "s1")); err != nil {
				t.Fatal(err)
			}
			if c.late {
				c.setup(e)
			}
			e.clock.Set(tNow.Add(leftoversEvery))
			e.board.tick()
			l := list(t, e.board)
			listed := false
			for _, it := range l.Items {
				for _, p := range it.Parts {
					if p.Kind == contract.LeftoverSession {
						listed = true // in the item of its pull request (TestSessionsOfOnePullRequestShareItsItem)
					}
				}
			}
			unknown := 0
			for _, h := range l.Held {
				if h.Reason == contract.HeldUnknown {
					unknown = h.Count
				}
			}
			if listed != c.listed || !c.listed && unknown != 1 || c.listed && unknown != 0 {
				t.Fatalf("listed %v, unknown %d: %+v", listed, unknown, l)
			}
		})
	}
}

// Sessions for one pull request in one clone come in one item, with its
// worktree and branch: the list counts the pull request once.
func TestSessionsOfOnePullRequestShareItsItem(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.inv.trees[alpha] = model.TreeStatus{Clean: true, Top: alpha, GitDir: alpha + "/.git", CommonDir: alpha + "/.git",
		Head: "refs/heads/main", Tip: tipOther}
	for _, id := range []string{"s1", "s3"} {
		s := session(id, model.StatusFinished, tNow.Add(-time.Hour))
		s.Folder = alpha
		s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 5}}
		e.board.applySync(s)
		e.board.tick()
		if err := e.board.MarkSeen(mustCardID(t, "claude", id)); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.Set(tNow.Add(leftoversEvery))
	e.board.tick()
	e.board.tick()
	l := list(t, e.board)
	if len(l.Items) != 1 {
		t.Fatalf("items = %+v", l.Items)
	}
	kinds := []contract.LeftoverKind{}
	for _, p := range l.Items[0].Parts {
		kinds = append(kinds, p.Kind)
	}
	want := []contract.LeftoverKind{contract.LeftoverSession, contract.LeftoverSession, contract.LeftoverWorktree,
		contract.LeftoverBranch, contract.LeftoverRemoteBranch}
	if !slices.Equal(kinds, want) || l.Items[0].Waiting || e.board.Snapshot().Leftovers.Items != 1 {
		t.Fatalf("parts = %v, item %+v", kinds, l.Items[0])
	}
}

// Nothing works in the worktree: it is listed, not waiting, with its branch
// and the branch on GitHub.
func TestLeftoverWorktreeListedWhenFree(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	other := session("s2", model.StatusBusy, tNow)
	e.board.applySync(other) // keeps alpha on the board
	e.board.tick()
	e.board.tick()
	l := list(t, e.board)
	if len(l.Items) != 1 || l.Items[0].Waiting {
		t.Fatalf("list = %+v", l)
	}
	want := []contract.LeftoverPart{
		{Kind: contract.LeftoverWorktree, Path: "~/projects/alpha-wt", Ignored: 3},
		{Kind: contract.LeftoverBranch, Branch: "feat", Path: "~/projects/alpha"},
		{Kind: contract.LeftoverRemoteBranch, Branch: "feat"},
	}
	if !slices.Equal(l.Items[0].Parts, want) {
		t.Fatalf("parts = %+v, want %+v", l.Items[0].Parts, want)
	}
}

// A repo whose origin is no longer the repository the board asked GitHub
// about lists nothing: the evidence would be about another repository.
func TestLeftoversNothingWhenOriginChanged(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	inv := e.inv.inv[alpha]
	inv.Origin = model.GitHubRepo("example-org/other-repo")
	e.inv.inv[alpha] = inv
	e.board.applySync(session("s2", model.StatusBusy, tNow))
	e.board.tick()
	e.board.tick()
	if l := list(t, e.board); l.State != contract.LeftoversReady || len(l.Items) != 0 || !l.Partial {
		t.Fatalf("list = %+v", l)
	}
}

// The session is no leftover while its pull request merged less than a day
// ago, or while it has a live worker; the worktree it sits in is held back.
func TestSessionLeftoverRules(t *testing.T) {
	cases := map[string]func(e *leftoverEnv){
		"merged an hour ago": func(e *leftoverEnv) {
			e.prs.prs[0].ClosedAt = tNow.Add(-time.Hour)
		},
		"has a live worker": func(e *leftoverEnv) {
			w := session("w1", model.StatusBusy, tNow)
			w.Parent = model.Of(claudeKey("s1"))
			e.board.applySync(w)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e := newLeftoverEnv(t, []string{alphaWT})
			setup(e)
			e.addWorker(t, "s1")
			e.clock.Set(tNow.Add(leftoversEvery))
			e.board.tick()
			l := list(t, e.board)
			for _, it := range l.Items {
				for _, p := range it.Parts {
					if p.Kind == contract.LeftoverSession {
						t.Fatalf("session listed: %+v", l)
					}
				}
			}
			if len(l.Held) != 1 || l.Held[0].Reason != contract.HeldInUse {
				t.Fatalf("held = %+v", l.Held)
			}
		})
	}
}

// The one-day rule on its own: an otherwise eligible Idle session is no
// leftover until its last pull request merged a full day ago. (On the board
// such a card sits in Done first, so only this test reaches the rule.)
func TestSessionLeftoverOneDayBoundary(t *testing.T) {
	c := contract.Card{Column: contract.ColumnIdle}
	c.Agent.Status = contract.AgentStatusFinished
	for name, tc := range map[string]struct {
		closed time.Time
		want   bool
	}{
		"a moment before a day": {tNow.Add(-doneWindow + time.Nanosecond), false},
		"exactly a day":         {tNow.Add(-doneWindow), true},
		"merged in the future":  {tNow.Add(time.Hour), false},
	} {
		pr := model.PullRequest{State: model.PRStateMerged, ClosedAt: tc.closed}
		if got := sessionLeftover(c, false, true, false, pr, true, tNow); got != tc.want {
			t.Errorf("%s: leftover = %v, want %v", name, got, tc.want)
		}
	}
	day := model.PullRequest{State: model.PRStateMerged, ClosedAt: tNow.Add(-2 * doneWindow)}
	if !sessionLeftover(c, false, true, false, day, true, tNow) || sessionLeftover(c, false, true, false, day, false, tNow) {
		t.Error("the merged evidence is not what decides")
	}
}

// A session is a leftover only on its own evidence: a known finished status,
// and every pull request it created answered and merged a day ago. A pull
// request GitHub did not answer, an unknown status, or no created pull
// request at all (a branch alone links nothing) leave it an open session,
// which holds its worktree. Claude Code and Codex sessions are alike: both
// report the pull requests they created.
func TestSessionLeftoverNeedsItsOwnEvidence(t *testing.T) {
	cases := map[string]struct {
		session func() model.SessionFact
		waiting bool // listed as a leftover session, its worktree waiting
	}{
		"every created pull request merged": {func() model.SessionFact {
			return createdBy("s1", 5)
		}, true},
		"one created pull request not answered": {func() model.SessionFact {
			return createdBy("s1", 5, 6)
		}, false},
		"status unknown": {func() model.SessionFact {
			s := createdBy("s1", 5)
			s.Status = model.Unknown[model.Status]()
			return s
		}, false},
		"a Codex session whose created pull request merged": {func() model.SessionFact {
			s := createdBy("c1", 5)
			s.Key = model.SessionKey{Tool: model.ToolCodex, ID: "c1"}
			return s
		}, true},
		"no created pull request, the branch's merged": {func() model.SessionFact {
			s := createdBy("c1", 5)
			s.Key, s.CreatedPRs = model.SessionKey{Tool: model.ToolCodex, ID: "c1"}, nil
			return s
		}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newLeftoverEnv(t, nil)
			s := c.session()
			e.board.applySync(s)
			e.board.tick()
			if err := e.board.MarkSeen(mustCardID(t, string(s.Key.Tool), s.Key.ID)); err != nil {
				t.Fatal(err)
			}
			e.clock.Set(tNow.Add(leftoversEvery))
			e.board.tick()
			l := list(t, e.board)
			if c.waiting {
				if len(l.Items) != 1 || !l.Items[0].Waiting || l.Items[0].Parts[0].Kind != contract.LeftoverSession {
					t.Fatalf("list = %+v", l)
				}
				return
			}
			if len(l.Items) != 0 || len(l.Held) != 1 || l.Held[0].Reason != contract.HeldInUse {
				t.Fatalf("list = %+v", l)
			}
		})
	}
}

// createdBy: a session in alpha-wt that created the given pull requests of
// the sample repo, finished an hour ago.
func createdBy(id string, numbers ...int) model.SessionFact {
	s := session(id, model.StatusFinished, tNow.Add(-time.Hour))
	s.Folder = alphaWT
	for _, n := range numbers {
		s.CreatedPRs = append(s.CreatedPRs, model.PRRef{Repo: model.GitHubRepo(exRepo), Number: n})
	}
	return s
}

// A branch checked out twice is held back, and a session in either checkout
// is not offered for closing: its folder holds work.
func TestSessionInASecondCheckoutIsNotALeftover(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	second := testHome + "/projects/alpha-wt2"
	inv := e.inv.inv[alpha]
	inv.Worktrees = append(inv.Worktrees, model.Worktree{Path: second, Branch: model.Of("feat")})
	e.inv.inv[alpha] = inv
	e.repos.set(second, model.Repo{Root: second, MainRoot: alpha, Branch: model.Of("feat"), Remote: model.GitHubRepo(exRepo)})
	s := session("s1", model.StatusFinished, tNow.Add(-time.Hour))
	s.Folder = second
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 5}}
	e.board.applySync(s)
	e.board.tick()
	if err := e.board.MarkSeen(mustCardID(t, "claude", "s1")); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(tNow.Add(leftoversEvery))
	e.board.tick()
	e.board.tick()
	l := list(t, e.board)
	if len(l.Items) != 0 || len(l.Held) != 1 || l.Held[0].Reason != contract.HeldInUse {
		t.Fatalf("list = %+v", l)
	}
}

func TestLeftoversOffWithoutGitHub(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.board.applySync(session("s2", model.StatusBusy, tNow))
	e.board.SetSources([]source.Status{{Kind: source.KindGit, State: source.StateOK}, {Kind: source.KindGitHub, State: source.StateOff}})
	e.board.tick()
	if l := list(t, e.board); l.State != contract.LeftoversOff || len(l.Items) != 0 {
		t.Fatalf("list = %+v", l)
	}
	if n := e.inv.lookCount(); n != 0 {
		t.Fatalf("git was asked %d times with GitHub off", n)
	}
	if e.board.Snapshot().Leftovers != (contract.LeftoversSummary{}) {
		t.Fatal("count with GitHub off")
	}
}

// One look per ten minutes, however often the clock ticks.
func TestLeftoversLookEveryTenMinutes(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.board.applySync(session("s2", model.StatusBusy, tNow))
	for i := range 6 {
		e.clock.Set(tNow.Add(time.Duration(i) * tickEvery))
		e.board.tick()
	}
	if n := e.inv.lookCount(); n != 1 {
		t.Fatalf("%d looks in a minute", n)
	}
	e.clock.Set(tNow.Add(leftoversEvery))
	e.board.tick()
	if n := e.inv.lookCount(); n != 2 {
		t.Fatalf("%d looks after ten minutes", n)
	}
}

func TestLeftoversLoadingBeforeTheFirstLook(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	if l := list(t, e.board); l.State != contract.LeftoversLoading || l.Items == nil || l.Held == nil {
		t.Fatalf("list = %+v", l)
	}
}

// A repo whose git or GitHub could not answer lists nothing, and the list
// says it is partial.
func TestLeftoversPartialWhenARepoCannotAnswer(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.board.applySync(session("s2", model.StatusBusy, tNow))
	e.board.tick()
	e.board.tick()
	if l := list(t, e.board); len(l.Items) != 1 || l.Partial {
		t.Fatalf("answered: list = %+v", l)
	}
	e.prs.mu.Lock()
	e.prs.err = errors.New("rate limited")
	e.prs.mu.Unlock()
	e.clock.Set(tNow.Add(leftoversEvery))
	e.board.tick()
	e.board.tick()
	if l := list(t, e.board); l.State != contract.LeftoversReady || len(l.Items) != 0 || !l.Partial {
		t.Fatalf("GitHub failing: list = %+v", l)
	}
	e.prs.mu.Lock()
	e.prs.err = nil
	e.prs.mu.Unlock()
	delete(e.inv.inv, alpha)
	e.clock.Set(tNow.Add(2 * leftoversEvery))
	e.board.tick()
	e.board.tick()
	if l := list(t, e.board); l.State != contract.LeftoversReady || len(l.Items) != 0 || !l.Partial {
		t.Fatalf("git failing: list = %+v", l)
	}
}

// A head GitHub may not have answered in full holds its merged work back,
// and the list says it may be incomplete: a head whose only pull requests
// were left out shows nothing at all. The repo was not looked at in full, so
// no session is offered from its main checkout.
func TestLeftoversHoldAnIncompleteHead(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.prs.incomplete = map[string]bool{"feat": true}
	e.inv.trees[alpha] = model.TreeStatus{Clean: true, Top: alpha, GitDir: alpha + "/.git", CommonDir: alpha + "/.git",
		Head: "refs/heads/main", Tip: tipOther}
	e.board.applySync(session("s2", model.StatusBusy, tNow))
	s := session("s1", model.StatusFinished, tNow.Add(-time.Hour))
	s.Folder, s.CreatedPRs = alpha, []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 5}}
	e.board.applySync(s)
	e.board.tick()
	if err := e.board.MarkSeen(mustCardID(t, "claude", "s1")); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(tNow.Add(leftoversEvery))
	e.board.tick()
	e.board.tick()
	l := list(t, e.board)
	if len(l.Items) != 0 || len(l.Held) != 1 || l.Held[0] != (contract.HeldCount{Reason: contract.HeldUnknown, Count: 2}) || !l.Partial {
		t.Fatalf("list = %+v", l)
	}
	if n := e.board.Snapshot().Leftovers; n != (contract.LeftoversSummary{Held: 2, Partial: true}) {
		t.Fatalf("snapshot = %+v", n)
	}
}

// Leftovers asks GitHub through Evidence, never through the board's cached
// lookups: a provider without it turns Leftovers off.
func TestLeftoversNeedFreshEvidence(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.board.applySync(session("s2", model.StatusBusy, tNow))
	e.board.tick()
	e.board.tick()
	asked := false
	for _, c := range e.prs.calls {
		asked = asked || c.evidence
		if c.numbers == nil && slices.Contains(c.branches, "feat") && !c.evidence {
			t.Fatalf("leftover names asked through Lookup: %+v", c)
		}
	}
	if !asked {
		t.Fatal("Evidence never asked")
	}
	b := newBoard(t, Options{Repos: e.repos, PRs: lookupOnly{e.prs}, Inventory: e.inv, State: e.state, Now: e.clock.Now})
	b.SetSources([]source.Status{{Kind: source.KindGit, State: source.StateOK}, {Kind: source.KindGitHub, State: source.StateOK}})
	b.tick()
	if l := list(t, b); l.State != contract.LeftoversOff {
		t.Fatalf("without evidence: %+v", l)
	}
}

// lookupOnly is a PR provider that cannot answer Evidence.
type lookupOnly struct{ f *fakePRs }

func (l lookupOnly) Probe(ctx context.Context) source.Status { return l.f.Probe(ctx) }
func (l lookupOnly) Lookup(ctx context.Context, repo model.ForgeRepo, n []int, b []string) ([]model.PullRequest, error) {
	return l.f.Lookup(ctx, repo, n, b)
}

// A look with no repo on the board yet (they resolve in the background) does
// not use up the ten minutes: the first look with a repo comes at the next
// tick.
func TestNoRepoYetDoesNotDelayTheFirstLook(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.board.tick()
	if l := list(t, e.board); l.State != contract.LeftoversReady || len(l.Items) != 0 {
		t.Fatalf("no repo: %+v", l)
	}
	e.board.applySync(session("s2", model.StatusBusy, tNow)) // alpha joins the board
	e.clock.Set(tNow.Add(leftoversSoonest))
	e.board.tick()
	e.board.tick()
	if l := list(t, e.board); len(l.Items) != 1 {
		t.Fatalf("a minute after alpha joined: %+v", l)
	}
}

// The only session on the board sits in a folder git gave no answer about:
// no repo to look at, and the list says it is incomplete until git answers.
func TestOnlyAnUnresolvedFolderMakesTheEmptyListPartial(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.repos.fail = map[string]bool{alpha: true}
	e.board.applySync(session("s2", model.StatusBusy, tNow))
	e.board.tick()
	if l := list(t, e.board); l.State != contract.LeftoversReady || len(l.Items) != 0 || !l.Partial {
		t.Fatalf("no answer: %+v", l)
	}
	if s := e.board.Snapshot(); !s.Leftovers.Partial {
		t.Fatalf("summary = %+v", s.Leftovers)
	}
}

// A pull request the board sees merge brings the next look forward: a minute
// after the last one, not ten.
func TestNewMergeBringsTheLookForward(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.prs.prs[0].State, e.prs.prs[0].ClosedAt = model.PRStateOpen, time.Time{}
	s := session("s2", model.StatusBusy, tNow)
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 5}}
	e.board.applySync(s)
	e.board.tick()
	e.board.tick()
	if l := list(t, e.board); len(l.Items) != 0 {
		t.Fatalf("open: %+v", l)
	}
	looks := e.inv.lookCount()
	e.prs.mu.Lock()
	e.prs.prs[0].State, e.prs.prs[0].ClosedAt = model.PRStateMerged, tNow.Add(-time.Minute)
	e.prs.mu.Unlock()
	step := leftoversSoonest / 3
	for i := 1; i <= 9; i++ {
		e.clock.Set(tNow.Add(time.Duration(i) * step))
		e.board.tick()
		if time.Duration(i)*step < leftoversSoonest && e.inv.lookCount() != looks {
			t.Fatalf("looked again %v after the last look", time.Duration(i)*step)
		}
	}
	if l := list(t, e.board); len(l.Items) != 1 || e.inv.lookCount() != looks+1 {
		t.Fatalf("after the merge: %d looks, %+v", e.inv.lookCount()-looks, l)
	}
	// The board learns of merges once a minute; whenever it does, the look
	// it brings forward still waits a minute after the last one.
	e.board.mu.Lock()
	e.board.prs[model.PRRef{Repo: model.GitHubRepo(exRepo), Number: 77}] = model.PullRequest{Repo: model.GitHubRepo(exRepo), Number: 77, State: model.PRStateMerged}
	soon := e.board.leftoversDueLocked(e.board.leftoverAt.Add(leftoversSoonest / 2))
	later := e.board.leftoversDueLocked(e.board.leftoverAt.Add(leftoversSoonest))
	e.board.mu.Unlock()
	if soon || !later {
		t.Fatalf("due after half a minute %v, after a minute %v", soon, later)
	}
}

// A listed session that closes brings the next look forward: a minute
// later, not ten.
func TestClosedSessionBringsTheLookForward(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.folders.set(workerProcs...)
	e.board.applySync(session("s2", model.StatusBusy, tNow)) // keeps alpha on the board
	e.addWorker(t, "s1")
	e.clock.Set(tNow.Add(leftoversEvery))
	e.board.tick()
	e.board.tick()
	if l := list(t, e.board); len(l.Items) != 1 || !l.Items[0].Waiting {
		t.Fatalf("list = %+v", l)
	}
	looks := e.inv.lookCount()
	e.clock.Set(tNow.Add(leftoversEvery + leftoversSoonest))
	e.board.tick()
	if n := e.inv.lookCount(); n != looks {
		t.Fatalf("looked again with nothing changed: %d", n)
	}
	// The next look starts now; the session closes right after it.
	e.clock.Set(tNow.Add(2 * leftoversEvery))
	e.board.tick()
	looks = e.inv.lookCount()
	s := session("s1", model.StatusFinished, tNow.Add(-time.Hour))
	s.Folder = alphaWT
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 5}}
	s.Ended = true
	e.board.applySync(s)
	e.board.tick()
	if n := e.inv.lookCount(); n != looks {
		t.Fatalf("looked again within a minute: %d", n)
	}
	e.clock.Set(tNow.Add(2*leftoversEvery + leftoversSoonest))
	e.board.tick()
	e.board.tick()
	if n := e.inv.lookCount(); n != looks+1 {
		t.Fatalf("%d looks after the session closed, want %d", n, looks+1)
	}
	for _, it := range list(t, e.board).Items {
		for _, p := range it.Parts {
			if p.Kind == contract.LeftoverSession {
				t.Fatalf("closed session still listed: %+v", it)
			}
		}
	}
}

// A leftover session's worktree waits for it only while every process in
// the worktree is the session's own: its process, the shell that started it
// and the ones it started. Any other process holds it as in use, and so does
// any process when the session's own is not running.
func TestWaitingOnlyForTheSessionsOwnProcesses(t *testing.T) {
	cases := map[string]struct {
		procs   []model.Proc
		waiting bool
	}{
		"its own":          {workerProcs, true},
		"and another":      {append(slices.Clone(workerProcs), model.Proc{PID: 5000, PPID: 1, Cwd: alphaWT + "/web"}), false},
		"its process gone": {workerProcs[2:], false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newLeftoverEnv(t, nil)
			e.folders.set(c.procs...)
			e.addWorker(t, "s1")
			e.clock.Set(tNow.Add(leftoversEvery))
			e.board.tick()
			l := list(t, e.board)
			if c.waiting {
				if len(l.Items) != 1 || !l.Items[0].Waiting {
					t.Fatalf("list = %+v", l)
				}
				return
			}
			if len(l.Items) != 0 || len(l.Held) != 1 || l.Held[0].Reason != contract.HeldInUse {
				t.Fatalf("list = %+v", l)
			}
		})
	}
}

// A session joins only the row of a pull request it created: one sitting
// in another pull request's worktree makes that worktree wait, and gets a
// row of its own.
func TestSessionJoinsOnlyItsOwnPullRequest(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.prs.prs = append(e.prs.prs, model.PullRequest{Repo: model.GitHubRepo(exRepo), Number: 10, Title: "Another change", HeadBranch: "other",
		State: model.PRStateMerged, OpenedAt: tNow.Add(-80 * time.Hour), ClosedAt: tNow.Add(-50 * time.Hour), HeadOID: tipOther,
		BaseBranch: "main", BaseIsDefault: true})
	s := session("s1", model.StatusFinished, tNow.Add(-time.Hour))
	s.Folder, s.PID = alphaWT, workerPID
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 10}} // not #5, whose worktree it sits in
	e.folders.set(workerProcs...)
	e.board.applySync(s)
	// The session that did create #5 sits in the clean main checkout: it
	// joins #5's row, but it is not the one the worktree waits for.
	e.inv.trees[alpha] = model.TreeStatus{Clean: true, Top: alpha, GitDir: alpha + "/.git", CommonDir: alpha + "/.git",
		Head: "refs/heads/main", Tip: tipOther}
	o := session("s2", model.StatusFinished, tNow.Add(-time.Hour))
	o.Folder = alpha
	o.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 5}}
	e.board.applySync(o)
	e.board.tick()
	for _, id := range []string{"s1", "s2"} {
		if err := e.board.MarkSeen(mustCardID(t, "claude", id)); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.Set(tNow.Add(leftoversEvery))
	e.board.tick()
	l := list(t, e.board)
	if len(l.Items) != 2 {
		t.Fatalf("list = %+v", l)
	}
	for _, it := range l.Items {
		switch it.PullRequest.Number {
		case 5:
			if !it.Waiting || it.OwnSession || it.Parts[0].Kind != contract.LeftoverSession || it.Parts[0].CardID != "claude:s2" ||
				it.Parts[1].Kind != contract.LeftoverWorktree {
				t.Errorf("#5 = %+v", it)
			}
		case 10:
			if len(it.Parts) != 1 || it.Parts[0].Kind != contract.LeftoverSession || it.Parts[0].CardID != "claude:s1" {
				t.Errorf("#10 = %+v", it)
			}
		default:
			t.Errorf("item %+v", it)
		}
	}
}

// A coordinator whose live worker works outside --roots (no card on the
// board) still has work going: it is no leftover.
func TestCoordinatorWithAWorkerOutsideTheRoots(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.board.roots = []string{testHome + "/projects"}
	e.folders.set(workerProcs...)
	w := session("w1", model.StatusBusy, tNow)
	w.Folder, w.Parent = "/srv/elsewhere", model.Of(claudeKey("s1"))
	e.board.applySync(w)
	e.addWorker(t, "s1")
	for _, c := range e.board.Snapshot().Cards {
		if c.ID == "claude:w1" {
			t.Fatalf("the worker has a card: %+v", c)
		}
	}
	e.clock.Set(tNow.Add(leftoversEvery))
	e.board.tick()
	l := list(t, e.board)
	if len(l.Items) != 0 || len(l.Held) != 1 || l.Held[0].Reason != contract.HeldInUse {
		t.Fatalf("list = %+v", l)
	}
}

// One look reads each checkout's status once, however many sessions sit in
// it.
func TestOneStatusPerCheckoutPerLook(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.inv.trees[alpha] = model.TreeStatus{Clean: true, Top: alpha, GitDir: alpha + "/.git", CommonDir: alpha + "/.git",
		Head: "refs/heads/main", Tip: tipOther}
	for _, id := range []string{"s1", "s2", "s3"} {
		s := session(id, model.StatusFinished, tNow.Add(-time.Hour))
		s.Folder, s.CreatedPRs = alpha+"/"+id, []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 5}}
		e.repos.set(s.Folder, model.Repo{Root: alpha, MainRoot: alpha, Branch: model.Of("main"), Remote: model.GitHubRepo(exRepo)})
		e.board.applySync(s)
		e.board.tick()
		if err := e.board.MarkSeen(mustCardID(t, "claude", id)); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.Set(tNow.Add(leftoversEvery))
	e.board.tick()
	l := list(t, e.board)
	if len(l.Items) != 1 || len(l.Items[0].Parts) != 6 {
		t.Fatalf("list = %+v", l)
	}
	if n := e.inv.treeLooks(alpha); n != 1 {
		t.Fatalf("%d status reads of the main checkout, want 1", n)
	}
}

// Paths and branch names are shown exactly as they are: two that differ
// only in spaces or an invisible character stay apart. A worktree outside
// the home folder, which would show by its base name only, or a name that is
// not text, is held back.
func TestLeftoverNamesShownExactly(t *testing.T) {
	odd := testHome + "/projects/alpha  wt\u200b"
	cases := map[string]struct {
		path, branch string
		listed       bool
	}{
		"spaces and invisible": {odd, "feat\u200b ", true},
		"outside home":         {"/srv/alpha-wt", "feat", false},
		"branch not text":      {alphaWT, "feat\xff", false},
		"path not text":        {alphaWT + "\xff", "feat", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newLeftoverEnv(t, nil)
			inv := e.inv.inv[alpha]
			inv.Worktrees[1].Path, inv.Worktrees[1].Branch = c.path, model.Of(c.branch)
			inv.Branches[1].Name = c.branch
			e.inv.inv[alpha] = inv
			st := e.inv.trees[alphaWT]
			st.Top, st.Head = c.path, "refs/heads/"+c.branch
			e.inv.trees[c.path] = st
			e.prs.prs[0].HeadBranch = c.branch
			e.board.applySync(session("s2", model.StatusBusy, tNow))
			e.board.tick()
			e.board.tick()
			l := list(t, e.board)
			if !c.listed {
				if len(l.Items) != 0 || len(l.Held) != 1 || l.Held[0].Reason != contract.HeldUnknown {
					t.Fatalf("list = %+v", l)
				}
				return
			}
			if len(l.Items) != 1 {
				t.Fatalf("list = %+v", l)
			}
			p := l.Items[0].Parts
			if p[0].Path != "~/projects/alpha  wt\u200b" || p[1].Branch != c.branch || p[1].Path != "~/projects/alpha" || p[2].Branch != c.branch {
				t.Fatalf("parts = %+v", p)
			}
		})
	}
}

// A checkout of another repo on the board inside a merged worktree holds the
// worktree back, even when that repo cannot be looked at.
// A linked worktree of another repo sits inside the worktree; that repo's
// main clone is elsewhere and the rest of its inventory fails (a partial
// clone): the worktree it lists still holds the outer one back.
func TestWorktreeHoldingAnUnreadableReposCheckout(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	beta, betaWT := testHome+"/projects/beta", alphaWT+"/vendor/beta-wt"
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Branch: model.Of("main"), Remote: model.GitHubRepo("example-org/beta")})
	e.inv.inv[beta] = model.RepoInventory{Worktrees: []model.Worktree{{Path: beta, Main: true, Branch: model.Of("main")}, {Path: betaWT, Branch: model.Of("x")}}}
	e.inv.fail = map[string]bool{beta: true}
	o := session("s2", model.StatusBusy, tNow)
	o.Folder = beta
	e.board.applySync(o)
	e.board.applySync(session("s3", model.StatusBusy, tNow)) // keeps alpha on the board
	e.board.tick()
	e.board.tick()
	l := list(t, e.board)
	if len(l.Items) != 0 || !slices.Contains(l.Held, contract.HeldCount{Reason: contract.HeldNested, Count: 1}) || !l.Partial {
		t.Fatalf("list = %+v", l)
	}
}

// Another repo on the board cannot be read before git lists its worktrees
// (its main checkout is unreadable for a moment): one of them may sit in an
// ignored folder of alpha-wt, where alpha-wt's own status does not show it,
// so alpha-wt is held back and the list says it is incomplete. When the
// listing was whole and only the rest failed, alpha-wt is offered.
func TestUnlistedWorktreesHoldAWorktreeWithIgnoredFiles(t *testing.T) {
	for _, listed := range []bool{false, true} {
		t.Run(fmt.Sprint("listed ", listed), func(t *testing.T) {
			e := newLeftoverEnv(t, nil)
			e.folders.set(workerProcs...)
			e.addWorker(t, "s1")
			beta := testHome + "/projects/beta"
			e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Branch: model.Of("main"), Remote: model.GitHubRepo("example-org/beta")})
			e.inv.inv[beta] = model.RepoInventory{WorktreesListed: listed, Worktrees: []model.Worktree{{Path: beta, Main: true, Branch: model.Of("main")}}}
			e.inv.fail = map[string]bool{beta: true}
			o := session("s2", model.StatusBusy, tNow)
			o.Folder = beta
			e.board.applySync(o)
			e.clock.Set(tNow.Add(leftoversEvery))
			e.board.tick()
			e.board.tick()
			l := list(t, e.board)
			if !l.Partial {
				t.Fatalf("list = %+v", l)
			}
			unknown := slices.Contains(l.Held, contract.HeldCount{Reason: contract.HeldUnknown, Count: 1})
			if listed && (len(l.Items) != 1 || unknown) || !listed && (len(l.Items) != 0 || !unknown) {
				t.Fatalf("list = %+v", l)
			}
		})
	}
}

// A session's folder whose git lookup failed may be in any repo, a GitHub
// clone too: the list says it is incomplete, and a worktree that could hide
// one of that repo's checkouts in its ignored files is held back. Once git
// answers, the list is looked at again within a minute and is whole.
func TestFailedResolveMakesTheListPartial(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.folders.set(workerProcs...)
	e.addWorker(t, "s1")
	beta := e.betaRepo(model.GitHubRepo("example-org/beta"))
	e.repos.fail = map[string]bool{beta: true}
	o := session("s2", model.StatusBusy, tNow)
	o.Folder = beta
	e.board.applySync(o)
	e.clock.Set(tNow.Add(leftoversEvery))
	e.board.tick()
	e.board.tick()
	l := list(t, e.board)
	if !l.Partial || len(l.Items) != 0 || !slices.Contains(l.Held, contract.HeldCount{Reason: contract.HeldUnknown, Count: 1}) {
		t.Fatalf("unresolved: list = %+v", l)
	}
	// git answers.
	e.repos.fail = nil
	e.board.mu.Lock()
	e.board.startResolveLocked(beta)
	e.board.mu.Unlock()
	e.board.resolving.Wait()
	e.clock.Set(tNow.Add(leftoversEvery + leftoversSoonest))
	e.board.tick()
	e.board.tick()
	if l := list(t, e.board); l.Partial || len(l.Items) != 1 {
		t.Fatalf("answered: list = %+v", l)
	}
}

// betaRepo makes repo beta known to git: its main checkout only, with
// origin remote (zero: none). A session in it puts it on the board.
func (e *leftoverEnv) betaRepo(remote model.ForgeRepo) string {
	beta := testHome + "/projects/beta"
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Branch: model.Of("main"), Remote: remote})
	inv := model.RepoInventory{WorktreesListed: true, Worktrees: []model.Worktree{{Path: beta, Main: true, Branch: model.Of("main")}}, CommonDir: beta + "/.git"}
	if remote.Forge == model.ForgeGitHub {
		inv.Origin = remote
	}
	e.inv.inv[beta] = inv
	return beta
}

// A clone whose first git answer has not come yet has no card, and still
// makes the list incomplete: its checkouts could be anywhere. When git
// answers, the next look (a minute on) is whole.
func TestPendingFolderWithNoCardMakesTheListPartial(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.folders.set(workerProcs...)
	e.addWorker(t, "s1")
	beta := e.betaRepo(model.GitHubRepo("example-org/beta"))
	release := make(chan struct{})
	e.repos.mu.Lock()
	e.repos.hold = map[string]chan struct{}{beta: release}
	e.repos.mu.Unlock()
	o := session("s2", model.StatusBusy, tNow)
	o.Folder = beta
	e.board.Apply(o) // git is asked; the answer waits
	if cards := e.board.Snapshot().Cards; len(cards) != 1 {
		t.Fatalf("cards before the answer: %d", len(cards))
	}
	e.clock.Set(tNow.Add(leftoversEvery))
	e.board.tickCtx(context.Background()) // the look starts with beta pending
	close(release)
	e.board.resolving.Wait()
	l := list(t, e.board)
	if !l.Partial || len(l.Items) != 0 || !slices.Contains(l.Held, contract.HeldCount{Reason: contract.HeldUnknown, Count: 1}) {
		t.Fatalf("pending: list = %+v", l)
	}
	e.clock.Set(tNow.Add(leftoversEvery + leftoversSoonest))
	e.board.tick()
	e.board.tick()
	if l := list(t, e.board); l.Partial || len(l.Items) != 1 {
		t.Fatalf("answered: list = %+v", l)
	}
}

// A clone that joins the board while a look runs is not in that look: the
// list it gives says it is incomplete, and the next one comes a minute on.
func TestRepoJoiningDuringALookMakesItPartial(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.folders.set(workerProcs...)
	e.addWorker(t, "s1")
	beta := e.betaRepo(model.GitHubRepo("example-org/beta"))
	e.inv.mu.Lock()
	e.inv.during = func() {
		o := session("s2", model.StatusBusy, tNow)
		o.Folder = beta
		e.board.Apply(o)
	}
	e.inv.mu.Unlock()
	e.clock.Set(tNow.Add(leftoversEvery))
	e.board.tick()
	e.board.tick()
	if l := list(t, e.board); !l.Partial || len(l.Items) != 1 {
		t.Fatalf("joined during the look: list = %+v", l)
	}
	e.clock.Set(tNow.Add(leftoversEvery + leftoversSoonest))
	e.board.tick()
	e.board.tick()
	if l := list(t, e.board); l.Partial || len(l.Items) != 1 {
		t.Fatalf("next look: list = %+v", l)
	}
}

// A check cut short (a worktree's status, or an ancestry question, out of
// time) holds that work back and makes the list incomplete; a commit that is
// not in the repo is an answer: held back, the list whole.
func TestCheckWithNoAnswerMakesTheListPartial(t *testing.T) {
	for name, c := range map[string]struct {
		tree, anc error
		partial   bool
	}{
		"status timed out":   {tree: fmt.Errorf("git: %w", context.DeadlineExceeded), partial: true},
		"status unreadable":  {tree: errors.New("cannot tell"), partial: true},
		"ancestry timed out": {anc: fmt.Errorf("git: %w", context.DeadlineExceeded), partial: true},
		"commit missing":     {anc: errors.New("exit status 128")},
	} {
		t.Run(name, func(t *testing.T) {
			e := newLeftoverEnv(t, nil)
			e.folders.set(workerProcs...)
			e.addWorker(t, "s1")
			if c.tree != nil {
				e.inv.treeErr = map[string]error{alphaWT: c.tree}
			}
			if c.anc != nil {
				inv := e.inv.inv[alpha]
				inv.Branches[1].Tip = tipOther // not the head: the scan asks git
				e.inv.inv[alpha] = inv
				e.inv.ancErr = c.anc
			}
			e.clock.Set(tNow.Add(leftoversEvery))
			e.board.tick()
			e.board.tick()
			l := list(t, e.board)
			if l.Partial != c.partial || len(l.Items) != 0 || !slices.Contains(l.Held, contract.HeldCount{Reason: contract.HeldUnknown, Count: 1}) {
				t.Fatalf("list = %+v", l)
			}
		})
	}
}

// A repo on the board that is not on GitHub (on GitLab, or with no origin)
// is never looked at for leftovers, but its checkouts are read: one inside
// an ignored folder of alpha-wt holds alpha-wt back. When they cannot be
// listed, the list is incomplete and alpha-wt is held back too.
func TestCheckoutsOfReposNotOnGitHubHoldWorktrees(t *testing.T) {
	for name, remote := range map[string]model.ForgeRepo{
		"gitlab":    model.GitLabRepo("gitlab.example.com", "example-group/beta"),
		"no origin": {},
	} {
		t.Run(name, func(t *testing.T) {
			for _, listed := range []bool{true, false} {
				e := newLeftoverEnv(t, nil)
				e.folders.set(workerProcs...)
				e.addWorker(t, "s1")
				beta := e.betaRepo(remote)
				inv := e.inv.inv[beta]
				inv.Worktrees = append(inv.Worktrees, model.Worktree{Path: alphaWT + "/vendor/beta-wt", Branch: model.Of("x")})
				if !listed {
					inv = model.RepoInventory{}
					e.inv.fail = map[string]bool{beta: true}
				}
				e.inv.inv[beta] = inv
				o := session("s2", model.StatusBusy, tNow)
				o.Folder = beta
				e.board.applySync(o)
				e.clock.Set(tNow.Add(leftoversEvery))
				e.board.tick()
				e.board.tick()
				l := list(t, e.board)
				want := contract.HeldCount{Reason: contract.HeldNested, Count: 1}
				if !listed {
					want.Reason = contract.HeldUnknown
				}
				if len(l.Items) != 0 || !slices.Contains(l.Held, want) || l.Partial == listed {
					t.Fatalf("listed %v: list = %+v", listed, l)
				}
			}
		})
	}
}

// A repo whose origin git read as none, and finds on GitHub when it lists the
// checkouts, changed between the two: the list is incomplete.
// The same when origin has several addresses now, one on GitHub, and when a
// read after the origin's (the stashes) failed.
func TestRepoMovedToGitHubMakesTheListPartial(t *testing.T) {
	for name, c := range map[string]struct {
		origin       model.ForgeRepo
		unclear, bad bool
	}{
		"github":              {origin: model.GitHubRepo("example-org/beta")},
		"several addresses":   {unclear: true},
		"github, then failed": {origin: model.GitHubRepo("example-org/beta"), bad: true},
	} {
		t.Run(name, func(t *testing.T) {
			e := newLeftoverEnv(t, nil)
			beta := e.betaRepo(model.ForgeRepo{})
			inv := e.inv.inv[beta]
			inv.Origin, inv.OriginUnclear = c.origin, c.unclear
			e.inv.inv[beta] = inv
			if c.bad {
				e.inv.fail = map[string]bool{beta: true}
			}
			o := session("s2", model.StatusBusy, tNow)
			o.Folder = beta
			e.board.applySync(o)
			e.board.applySync(session("s3", model.StatusBusy, tNow)) // keeps alpha on the board
			e.board.tick()
			e.board.tick()
			if l := list(t, e.board); !l.Partial {
				t.Fatalf("list = %+v", l)
			}
		})
	}
}

// A board that empties after a look is not due for another until it changes.
func TestEmptiedBoardIsNotDueAgain(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	s := session("s3", model.StatusBusy, tNow)
	e.board.applySync(s)
	e.board.tick()
	e.board.tick()
	s.Ended = true
	e.board.applySync(s)
	e.clock.Set(tNow.Add(leftoversSoonest))
	e.board.tick()
	e.board.mu.Lock()
	due := e.board.leftoversDueLocked(tNow.Add(2 * leftoversSoonest))
	e.board.mu.Unlock()
	if due {
		t.Fatal("an empty board is due again")
	}
}

// Two folders of one repo that git answered about differently (its origin
// changed between the answers): the repo is not looked at, and the list
// says it is incomplete.
func TestFoldersDisagreeingAboutARepoMakeItUnclear(t *testing.T) {
	for _, noOrigin := range []string{alpha, alphaWT} {
		t.Run(filepath.Base(noOrigin), func(t *testing.T) {
			e := newLeftoverEnv(t, nil)
			e.folders.set(workerProcs...)
			if noOrigin == alpha {
				e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Branch: model.Of("main")})
			} else {
				e.repos.set(alphaWT, model.Repo{Root: alphaWT, MainRoot: alpha, Branch: model.Of("feat")})
			}
			e.board.applySync(session("s3", model.StatusBusy, tNow))
			e.addWorker(t, "s1")
			e.clock.Set(tNow.Add(leftoversEvery))
			e.board.tick()
			e.board.tick()
			if l := list(t, e.board); !l.Partial || len(l.Items) != 0 {
				t.Fatalf("list = %+v", l)
			}
		})
	}
}

// A look whose own time ran out says the list is incomplete, whatever it
// found.
func TestLookOutOfTimeIsPartial(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.folders.set(workerProcs...)
	e.addWorker(t, "s1")
	b := e.board
	b.mu.Lock()
	repos, sessions, live := b.leftoverInputsLocked()
	b.mu.Unlock()
	if l := b.lookForLeftovers(context.Background(), repos, sessions, live, false); l.Partial || len(l.Items) != 1 {
		t.Fatalf("in time: %+v", l)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if l := b.lookForLeftovers(ctx, repos, sessions, live, false); !l.Partial {
		t.Fatalf("out of time: %+v", l)
	}
}

// The only GitHub project on the board has two origin addresses: it is not
// looked at, and the list says it is incomplete, so the sheet can be opened
// and says so.
func TestUnclearOriginMakesTheListPartial(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Branch: model.Of("main"), OriginUnclear: true})
	inv := e.inv.inv[alpha]
	inv.Origin = model.ForgeRepo{} // as the inventory reads it too
	e.inv.inv[alpha] = inv
	e.board.applySync(session("s3", model.StatusBusy, tNow))
	e.board.tick()
	e.board.tick()
	l := list(t, e.board)
	if l.State != contract.LeftoversReady || len(l.Items) != 0 || !l.Partial {
		t.Fatalf("list = %+v", l)
	}
	if s := e.board.Snapshot(); !s.Leftovers.Partial {
		t.Fatalf("summary = %+v", s.Leftovers)
	}
}

func TestWorktreeHoldingAnotherReposCheckout(t *testing.T) {
	e := newLeftoverEnv(t, nil)
	beta := alphaWT + "/vendor/beta"
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Branch: model.Of("main"), Remote: model.GitHubRepo("example-org/beta")})
	o := session("s2", model.StatusBusy, tNow)
	o.Folder = beta
	e.board.applySync(o)
	e.board.applySync(session("s3", model.StatusBusy, tNow)) // keeps alpha on the board
	e.board.tick()
	e.board.tick()
	l := list(t, e.board)
	if len(l.Items) != 0 || !slices.Contains(l.Held, contract.HeldCount{Reason: contract.HeldNested, Count: 1}) {
		t.Fatalf("list = %+v", l)
	}
}

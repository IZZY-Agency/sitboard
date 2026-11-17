package core

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// groupEnv is a board over a real temporary home folder, so umbrella folders
// exist on disk and can be removed.
type groupEnv struct {
	*testEnv
	home  string
	state string // the state file
}

func newGroupEnv(t *testing.T) *groupEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	file := filepath.Join(t.TempDir(), "state.json")
	e := &groupEnv{home: home, state: file}
	// The state file prunes umbrellas by the real clock, so this board starts
	// at the real time.
	e.testEnv = &testEnv{clock: &fakeClock{t: time.Now().UTC().Truncate(time.Second)}, repos: &fakeRepos{repos: map[string]model.Repo{}}, prs: &fakePRs{}}
	e.reopen(t)
	return e
}

// reopen starts a new board on the same state file, as after a restart.
func (e *groupEnv) reopen(t *testing.T) {
	t.Helper()
	st, err := state.Open(e.state)
	if err != nil {
		t.Fatal(err)
	}
	e.testEnv.state = st
	e.board = New(Options{Repos: e.repos, PRs: e.prs, State: st, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
}

// dir creates a folder under home and returns its path.
func (e *groupEnv) dir(t *testing.T, rel string) string {
	t.Helper()
	p := filepath.Join(e.home, rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// repo creates a git repository folder under home.
func (e *groupEnv) repo(t *testing.T, rel string) string {
	t.Helper()
	p := e.dir(t, rel)
	e.dir(t, filepath.Join(rel, ".git"))
	e.repos.set(p, model.Repo{Root: p, MainRoot: p, Branch: model.Of("main"), Slug: exRepo})
	return p
}

// agent puts a live busy session in folder.
func (e *groupEnv) agent(id, folder string) {
	s := busySession(id, e.clock.Now())
	s.Folder = folder
	e.board.applySync(s)
}

// worker puts a live busy session in folder whose coordinator is coord.
func (e *groupEnv) worker(id, folder, coord string) {
	s := busySession(id, e.clock.Now())
	s.Folder = folder
	s.Parent = model.Of(claudeKey(coord))
	e.board.applySync(s)
}

func (e *groupEnv) projects(t *testing.T) map[string]contract.Project {
	t.Helper()
	e.board.tick()
	out := map[string]contract.Project{}
	for _, p := range e.board.Snapshot().Projects {
		out[p.ID] = p
	}
	return out
}

func pid(folder string) string { return contract.ProjectID(folder) }

// settle lets umbrellaSettle pass with the same agents live, so live
// umbrellas are remembered.
func (e *groupEnv) settle(t *testing.T) {
	t.Helper()
	_ = e.projects(t)
	e.clock.Set(e.clock.Now().Add(umbrellaSettle))
	e.board.mu.Lock()
	live := slices.Collect(maps.Values(e.board.sessions))
	e.board.mu.Unlock()
	for _, s := range live {
		s.LastActivity, s.StatusSince = e.clock.Now(), e.clock.Now()
		e.board.applySync(s)
	}
	_ = e.projects(t)
}

// The real shape: a plain folder with agents in it, repositories directly
// inside it with agents, a repository two levels down, and unrelated
// repositories next to the folder.
func TestUmbrellaGroupsItsRepositories(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	partA := e.repo(t, "work/umbrella/repo-a")
	partB := e.repo(t, "work/umbrella/repo-b")
	deep := e.repo(t, "work/umbrella/group/repo-deep")
	other := e.repo(t, "work/other-repo")
	for i, f := range []string{umbrella, umbrella, umbrella, partA, partB, deep, other} {
		e.agent(string(rune('a'+i)), f)
	}
	ps := e.projects(t)

	u, ok := ps[pid(umbrella)]
	if !ok || u.ParentID != "" || u.Area != "work" || u.Counts.Working != 3 {
		t.Fatalf("umbrella %+v (found %v)", u, ok)
	}
	for _, f := range []string{partA, partB} {
		p := ps[pid(f)]
		if p.ParentID != pid(umbrella) || p.Area != "work" || p.Counts.Working != 1 {
			t.Errorf("part %s: %+v", filepath.Base(f), p)
		}
	}
	if p := ps[pid(deep)]; p.ParentID != "" || p.Area != "group" {
		t.Errorf("a repository two levels down folded: %+v", p)
	}
	if p := ps[pid(other)]; p.ParentID != "" || p.Area != "work" {
		t.Errorf("a repository next to the umbrella: %+v", p)
	}
	for _, c := range e.board.Snapshot().Cards {
		if c.ProjectID == pid(umbrella) && c.Agent.Folder != "~/work/umbrella" {
			t.Errorf("card %s moved to the umbrella", c.ID)
		}
	}
	if len(ps) != 5 {
		t.Errorf("%d projects, want 5", len(ps))
	}
}

// An agent in the home folder makes no umbrella; a project directly under
// home has no area; a hidden folder below home gives no area.
func TestHomeIsNeverAnUmbrellaAndAreaRules(t *testing.T) {
	e := newGroupEnv(t)
	direct := e.repo(t, "repo-h")
	hidden := e.repo(t, ".config/tool/repo-x")
	e.agent("h", e.home)
	e.agent("d", direct)
	e.agent("x", hidden)
	ps := e.projects(t)
	if p := ps[pid(direct)]; p.ParentID != "" || p.Area != "" {
		t.Errorf("a project directly under home: %+v", p)
	}
	if p := ps[pid(hidden)]; p.ParentID != "" || p.Area != "" {
		t.Errorf("a project under a hidden folder: %+v", p)
	}
	if p := ps[pid(e.home)]; p.Area != "" || p.ParentID != "" {
		t.Errorf("the home folder: %+v", p)
	}
}

// A project outside the home folder has no area.
func TestProjectOutsideHomeHasNoArea(t *testing.T) {
	e := newGroupEnv(t)
	outside := filepath.Join(t.TempDir(), "srv", "repo-o") // t.TempDir is not under the test home
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	e.repos.set(outside, model.Repo{Root: outside, MainRoot: outside, Branch: model.Of("main")})
	e.agent("o", outside)
	if p := e.projects(t)[pid(outside)]; p.Area != "" || p.ParentID != "" {
		t.Errorf("outside home: %+v", p)
	}
}

// The umbrella is remembered when its last agent ends, also across a restart,
// and still appears in projects while a part has a card. It is forgotten after
// UmbrellaRetention, or when its folder is gone.
func TestUmbrellaIsRememberedThenForgotten(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.agent("u", umbrella)
	e.worker("p", part, "u") // a coordinator with a worker inside: an umbrella
	e.settle(t)              // the umbrella is seen, settles and is saved on the tick

	e.board.applySync(model.SessionGone{Key: claudeKey("u")})
	ps := e.projects(t)
	if u, ok := ps[pid(umbrella)]; !ok || u.Counts != (contract.Counts{}) {
		t.Fatalf("remembered umbrella missing or not empty: %+v %v", u, ok)
	}
	if p := ps[pid(part)]; p.ParentID != pid(umbrella) {
		t.Fatalf("part no longer folds after the umbrella's last agent ended: %+v", p)
	}

	e.reopen(t)
	e.agent("p", part)
	if p := e.projects(t)[pid(part)]; p.ParentID != pid(umbrella) {
		t.Fatalf("umbrella not remembered across a restart: %+v", p)
	}

	e.clock.Set(e.clock.Now().Add(state.UmbrellaRetention + time.Hour))
	e.agent("p", part) // still live
	ps = e.projects(t)
	if p := ps[pid(part)]; p.ParentID != "" {
		t.Fatalf("umbrella not forgotten by age: %+v", p)
	}
	if _, ok := ps[pid(umbrella)]; ok {
		t.Fatal("a forgotten umbrella is still listed")
	}
}

func TestUmbrellaIsForgottenWhenItsFolderIsGone(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.agent("u", umbrella)
	e.worker("p", part, "u")
	e.settle(t)
	e.board.applySync(model.SessionGone{Key: claudeKey("u")})
	if err := os.RemoveAll(umbrella); err != nil {
		t.Fatal(err)
	}
	// Checked once an hour; forgotten only after umbrellaMisses checks in a row.
	for i := range umbrellaMisses {
		if p := e.projects(t)[pid(part)]; p.ParentID != pid(umbrella) {
			t.Fatalf("forgotten after %d missing checks: %+v", i, p)
		}
		e.clock.Set(e.clock.Now().Add(umbrellaCheckEvery))
	}
	if p := e.projects(t)[pid(part)]; p.ParentID != "" {
		t.Fatalf("umbrella with a gone folder still folds: %+v", p)
	}
	if _, ok := e.testEnv.state.Umbrellas()[umbrella]; ok {
		t.Fatal("a gone umbrella is still in the state file")
	}
}

// A folder missing on one check (a drive unplugged for a moment) is kept.
func TestUmbrellaMissingOnceIsKept(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.agent("u", umbrella)
	e.worker("p", part, "u")
	e.settle(t)
	e.board.applySync(model.SessionGone{Key: claudeKey("u")})
	hidden := umbrella + ".away"
	if err := os.Rename(umbrella, hidden); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(e.clock.Now().Add(umbrellaCheckEvery))
	_ = e.projects(t) // one missing check
	if err := os.Rename(hidden, umbrella); err != nil {
		t.Fatal(err)
	}
	for range umbrellaMisses + 1 {
		e.clock.Set(e.clock.Now().Add(umbrellaCheckEvery))
		_ = e.projects(t)
	}
	if _, ok := e.testEnv.state.Umbrellas()[umbrella]; !ok {
		t.Fatal("an umbrella missing on one check was forgotten")
	}
}

// Rule 2: a plain folder where an agent sits, with no repository parts of its
// own, folds into the nearest umbrella 1 to 3 levels above it. It is not
// remembered as an umbrella of its own (rule 4).
func TestPlainFolderFoldsIntoUmbrellaAbove(t *testing.T) {
	e := newGroupEnv(t)
	outer := e.dir(t, "work/outer")
	inner := e.dir(t, "work/outer/inner")
	e.agent("o", outer)
	e.agent("o2", outer) // two agents at once: an umbrella
	e.agent("i", inner)
	e.settle(t)
	ps := e.projects(t)
	if p := ps[pid(inner)]; p.ParentID != pid(outer) || p.Area != "work" {
		t.Fatalf("plain folder directly inside an umbrella: %+v", p)
	}
	if p := ps[pid(outer)]; p.ParentID != "" {
		t.Fatalf("the umbrella got a parent: %+v", p)
	}
	if _, ok := e.testEnv.state.Umbrellas()[inner]; ok {
		t.Fatal("a folded plain folder was remembered as an umbrella")
	}
	if _, ok := e.testEnv.state.Umbrellas()[outer]; !ok {
		t.Fatal("the top-level umbrella was not remembered")
	}
}

// A working folder two levels below a project folder folds into it; four
// levels down does not.
func TestWorkingFolderDepth(t *testing.T) {
	e := newGroupEnv(t)
	proj := e.dir(t, "work/proj")
	two := e.dir(t, "work/proj/notes/draft")
	four := e.dir(t, "work/proj/a/b/c/d")
	e.agent("p", proj)
	e.agent("p2", proj)
	e.agent("t", two)
	e.agent("f", four)
	ps := e.projects(t)
	if p := ps[pid(two)]; p.ParentID != pid(proj) {
		t.Errorf("two levels down: %+v", p)
	}
	if p := ps[pid(four)]; p.ParentID != "" {
		t.Errorf("four levels down folded: %+v", p)
	}
}

// Under a broad folder with an agent in it, a project folder with repository
// parts stays top-level (keeping its parts), while a partless plain folder
// next to it folds into the broad folder.
func TestProjectFolderWithRepositoriesStaysTopLevel(t *testing.T) {
	e := newGroupEnv(t)
	broad := e.dir(t, "work")
	proj := e.dir(t, "work/proj")
	repo := e.repo(t, "work/proj/repo-a")
	side := e.dir(t, "work/side")
	e.agent("b", broad)
	e.agent("b2", broad)
	e.agent("p", proj)
	e.worker("r", repo, "p")
	e.agent("s", side)
	ps := e.projects(t)
	if p := ps[pid(proj)]; p.ParentID != "" {
		t.Errorf("project folder with a repository part folded: %+v", p)
	}
	if p := ps[pid(repo)]; p.ParentID != pid(proj) {
		t.Errorf("repository part: %+v", p)
	}
	if p := ps[pid(side)]; p.ParentID != pid(broad) {
		t.Errorf("partless plain folder: %+v", p)
	}
}

// Between two nested umbrellas the nearest wins, and the one that gets a part
// stays top-level: a parent never has a parent.
func TestNearestUmbrellaWins(t *testing.T) {
	e := newGroupEnv(t)
	outer := e.dir(t, "work/outer")
	inner := e.dir(t, "work/outer/inner")
	deep := e.dir(t, "work/outer/inner/deep")
	e.agent("o", outer)
	e.agent("o2", outer)
	e.agent("i", inner)
	e.agent("i2", inner)
	e.agent("d", deep)
	ps := e.projects(t)
	if p := ps[pid(deep)]; p.ParentID != pid(inner) {
		t.Errorf("deep: %+v", p)
	}
	if p := ps[pid(inner)]; p.ParentID != "" {
		t.Errorf("an umbrella with a part got a parent: %+v", p)
	}
}

// While a watcher is on its first pass, a session's folder may still be its
// launch folder: nothing is remembered as an umbrella until every source is
// ready, though the grouping already shows.
func TestUmbrellaIsRememberedOnlyOnceReady(t *testing.T) {
	e := newGroupEnv(t)
	e.board.SetSources([]source.Status{{Kind: source.KindClaude, State: source.StateOK}})
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.agent("u", umbrella)
	e.worker("p", part, "u")
	if p := e.projects(t)[pid(part)]; p.ParentID != pid(umbrella) {
		t.Fatalf("not grouped before ready: %+v", p)
	}
	if len(e.testEnv.state.Umbrellas()) != 0 {
		t.Fatal("an umbrella was remembered before the sources were ready")
	}
	e.board.applySync(model.SourceReady{Kind: string(source.KindClaude)})
	e.settle(t)
	if _, ok := e.testEnv.state.Umbrellas()[umbrella]; !ok {
		t.Fatal("not remembered once ready")
	}
}

// A folder that holds an agent only briefly (a session's launch folder
// before its transcript is read, say) is not remembered: an umbrella must
// stay live for umbrellaSettle first.
func TestBriefUmbrellaIsNotRemembered(t *testing.T) {
	e := newGroupEnv(t)
	broad := e.dir(t, "work")
	part := e.repo(t, "work/repo-a")
	e.agent("b", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	if p := e.projects(t)[pid(part)]; p.ParentID != pid(broad) {
		t.Fatalf("not grouped while live: %+v", p)
	}
	e.board.applySync(model.SessionGone{Key: claudeKey("b")})
	e.board.applySync(model.SessionGone{Key: claudeKey("b2")})
	e.settle(t)
	if p := e.projects(t)[pid(part)]; p.ParentID != "" {
		t.Fatalf("a brief umbrella still groups: %+v", p)
	}
	if len(e.testEnv.state.Umbrellas()) != 0 {
		t.Fatal("a brief umbrella was remembered")
	}
}

// Ruling: one agent in a broad folder (a quick question asked there) makes no
// umbrella: it folds nothing and is remembered as nothing.
func TestOneAgentInBroadFolderFoldsNothing(t *testing.T) {
	e := newGroupEnv(t)
	broad := e.dir(t, "projects")
	r1, r2 := e.repo(t, "projects/r1"), e.repo(t, "projects/r2")
	e.agent("q", broad)
	e.agent("a", r1)
	e.agent("b", r2)
	e.settle(t)
	ps := e.projects(t)
	for _, r := range []string{r1, r2} {
		if p := ps[pid(r)]; p.ParentID != "" {
			t.Errorf("%s folded into a folder with one agent: %+v", filepath.Base(r), p)
		}
	}
	if len(e.testEnv.state.Umbrellas()) != 0 {
		t.Fatalf("remembered %v", e.testEnv.state.Umbrellas())
	}
}

// Ruling: a lone coordinator in a plain folder qualifies it when one of its
// workers works inside it, up to exactly 3 levels down. A worker 4 levels
// down, or a 2-cycle of coordinators, does not: the repository under those
// folders stays top-level.
func TestLoneCoordinatorQualifies(t *testing.T) {
	e := newGroupEnv(t)
	three, four, cyc := e.dir(t, "work/three"), e.dir(t, "work/four"), e.dir(t, "work/cycle")
	r3, r4, rc := e.repo(t, "work/three/repo"), e.repo(t, "work/four/repo"), e.repo(t, "work/cycle/repo")
	e.agent("c3", three)
	e.worker("w3", e.dir(t, "work/three/a/b/c"), "c3")
	e.agent("c4", four)
	e.worker("w4", e.dir(t, "work/four/a/b/c/d"), "c4")
	e.worker("x1", cyc, "x2") // x1 and x2 name each other: no coordinator
	e.worker("x2", e.dir(t, "work/cycle/sub"), "x1")
	for i, r := range []string{r3, r4, rc} {
		e.agent(fmt.Sprintf("r%d", i), r)
	}
	ps := e.projects(t)
	if p := ps[pid(r3)]; p.ParentID != pid(three) {
		t.Errorf("worker exactly 3 levels down: %+v", p)
	}
	if p := ps[pid(r4)]; p.ParentID != "" {
		t.Errorf("worker 4 levels down qualified the folder: %+v", p)
	}
	if p := ps[pid(rc)]; p.ParentID != "" {
		t.Errorf("a 2-cycle of coordinators qualified the folder: %+v", p)
	}
}

// Remembered umbrellas are capped in memory as in the file: the newest
// state.MaxUmbrellas stay.
func TestRememberedUmbrellasCappedInMemory(t *testing.T) {
	e := newGroupEnv(t)
	now := e.clock.Now()
	e.board.mu.Lock()
	for i := range state.MaxUmbrellas + 50 {
		e.board.umbrellas[fmt.Sprintf("%s/u%03d", e.home, i)] = now.Add(-time.Duration(i) * time.Minute)
	}
	e.board.noteUmbrellasLocked(nil, now)
	n := len(e.board.umbrellas)
	_, newest := e.board.umbrellas[e.home+"/u000"]
	e.board.mu.Unlock()
	if n != state.MaxUmbrellas || !newest {
		t.Fatalf("%d umbrellas in memory (newest kept: %v), cap %d", n, newest, state.MaxUmbrellas)
	}
}

// A gone check that times out counts nothing and stays busy until its worker
// returns, so a hung mount never piles up workers; after shutdown a check
// changes nothing.
func TestGoneCheckTimeoutAndShutdown(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	oldStat, oldTimeout := statFolder, umbrellaCheckTimeout
	statFolder = func(p string) (os.FileInfo, error) {
		calls.Add(1)
		<-release
		return os.Stat(p)
	}
	umbrellaCheckTimeout = 20 * time.Millisecond
	t.Cleanup(func() { statFolder, umbrellaCheckTimeout = oldStat, oldTimeout })

	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.agent("u", umbrella)
	e.worker("p", part, "u")
	e.settle(t)
	e.board.applySync(model.SessionGone{Key: claudeKey("u")})
	if err := os.RemoveAll(umbrella); err != nil {
		t.Fatal(err)
	}
	for range umbrellaMisses + 1 {
		e.clock.Set(e.clock.Now().Add(umbrellaCheckEvery))
		_ = e.projects(t) // the check times out; the hung worker blocks new ones
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d checks started while one hung, want 1", n)
	}
	if _, ok := e.testEnv.state.Umbrellas()[umbrella]; !ok {
		t.Fatal("a timed-out check forgot an umbrella")
	}

	e.board.mu.Lock()
	e.board.closed = true // Run returned
	e.board.mu.Unlock()
	close(release)
	statFolder = os.Stat
	for range umbrellaMisses + 1 {
		e.board.mu.Lock()
		busy := e.board.goneBusy
		e.board.mu.Unlock()
		for busy {
			time.Sleep(time.Millisecond)
			e.board.mu.Lock()
			busy = e.board.goneBusy
			e.board.mu.Unlock()
		}
		e.clock.Set(e.clock.Now().Add(umbrellaCheckEvery))
		e.board.tick()
	}
	if _, ok := e.testEnv.state.Umbrellas()[umbrella]; !ok {
		t.Fatal("a check after shutdown changed the state file")
	}
}

// Spellings of one folder ("a/", "a/.") are that folder: no project is its own
// parent, none is named ".", and the home folder is never remembered.
func TestFolderSpellingsAreOneFolder(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.agent("u1", umbrella)
	e.agent("u2", umbrella+"/")
	e.agent("p", part)
	e.agent("h", e.home+"/.")
	e.settle(t)
	e.agent("u3", umbrella+"/.")
	for _, p := range e.projects(t) {
		if p.ParentID == p.ID || p.Name == "." {
			t.Fatalf("bad project %+v", p)
		}
	}
	if _, ok := e.testEnv.state.Umbrellas()[e.home+"/."]; ok {
		t.Fatal("the home folder was remembered")
	}
	if got := len(e.testEnv.state.Umbrellas()); got != 1 {
		t.Fatalf("%d umbrellas remembered, want 1", got)
	}
}

// A remembered umbrella that became a repository (git init) is no longer a
// parent once it has a project of its own.
func TestRememberedUmbrellaMustStayPlain(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.agent("u", umbrella)
	e.worker("p", part, "u")
	e.settle(t)
	e.board.applySync(model.SessionGone{Key: claudeKey("u")})
	e.dir(t, "work/umbrella/.git")
	e.repos.set(umbrella, model.Repo{Root: umbrella, MainRoot: umbrella, Branch: model.Of("main")})
	e.agent("u", umbrella)
	e.clock.Set(e.clock.Now().Add(2 * time.Minute)) // live folders are resolved again
	_ = e.projects(t)
	if p := e.projects(t)[pid(part)]; p.ParentID != "" {
		t.Fatalf("a remembered umbrella that is now a repository still folds: %+v", p)
	}
}

// Invariants over random folder trees and spellings: no project is its own
// parent, a parent never has a parent, and every parentId is in the snapshot.
func TestGroupingInvariants(t *testing.T) {
	for seed := range int64(40) {
		rng := rand.New(rand.NewPCG(uint64(seed), 7))
		e := newGroupEnv(t)
		var folders []string
		for range 12 {
			parts := []string{"w"}
			for range 1 + rng.IntN(4) {
				parts = append(parts, string(rune('a'+rng.IntN(3))))
			}
			rel := filepath.Join(parts...)
			if rng.IntN(3) == 0 {
				folders = append(folders, e.repo(t, rel))
			} else {
				folders = append(folders, e.dir(t, rel))
			}
		}
		spell := []string{"", "/", "/."}
		var ids []string
		for i := range 20 {
			f := folders[rng.IntN(len(folders))] + spell[rng.IntN(len(spell))]
			id := fmt.Sprintf("s%02d", i)
			if len(ids) > 0 && rng.IntN(2) == 0 {
				e.worker(id, f, ids[rng.IntN(len(ids))])
			} else {
				e.agent(id, f)
			}
			ids = append(ids, id)
		}
		e.settle(t)
		snap := e.board.Snapshot()
		byID := map[string]contract.Project{}
		for _, p := range snap.Projects {
			byID[p.ID] = p
		}
		for _, p := range snap.Projects {
			if p.ParentID == "" {
				continue
			}
			parent, ok := byID[p.ParentID]
			switch {
			case p.ParentID == p.ID:
				t.Fatalf("seed %d: %s is its own parent", seed, p.Path)
			case !ok:
				t.Fatalf("seed %d: %s has a parent not in the snapshot", seed, p.Path)
			case parent.ParentID != "":
				t.Fatalf("seed %d: %s's parent %s has a parent", seed, p.Path, parent.Path)
			}
		}
	}
}

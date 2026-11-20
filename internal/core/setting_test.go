package core

import (
	"errors"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

func (e *groupEnv) set(t *testing.T, folder string, mode contract.GroupingMode) {
	t.Helper()
	if err := e.board.SetGrouping(pid(folder), mode); err != nil {
		t.Fatalf("SetGrouping(%s, %s): %v", folder, mode, err)
	}
}

func (e *groupEnv) setting(folder string) string {
	e.board.mu.Lock()
	defer e.board.mu.Unlock()
	return e.board.settings[folder]
}

// Never, on a broad folder the issue is about: the grouping splits at once,
// is not remembered again while both agents keep sitting there, not after a
// restart either, and the folder is listed in the snapshot's settings.
func TestNeverSplitsAndStaysSplit(t *testing.T) {
	e, broad, part := broadEnv(t)
	e.set(t, broad, contract.GroupingNever)
	if e.grouped(t, part, broad) {
		t.Fatal("still grouped after never")
	}
	e.settle(t)
	e.settle(t)
	if e.grouped(t, part, broad) {
		t.Fatal("grouped again while its agents sit in it")
	}
	if mem, file := e.remembered(broad); mem || file {
		t.Fatalf("remembered: memory %v, file %v", mem, file)
	}
	if e.held(broad) {
		t.Fatal("never left a forget hold behind")
	}
	want := []contract.GroupingSetting{{ProjectID: pid(broad), Name: "work", Path: "~/work", Mode: contract.GroupingNever}}
	if got := e.board.Snapshot().GroupingSettings; !equalSettings(got, want) {
		t.Fatalf("settings %+v, want %+v", got, want)
	}

	// Weeks later, after a restart, with the agents gone and back.
	e.clock.Set(e.clock.Now().Add(40 * 24 * time.Hour))
	e.reopen(t)
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	e.settle(t)
	if e.grouped(t, part, broad) {
		t.Fatal("grouped again after a restart")
	}
	if got := e.board.Snapshot().GroupingSettings; !equalSettings(got, want) {
		t.Fatalf("after a restart: settings %+v", got)
	}
}

func equalSettings(a, b []contract.GroupingSetting) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A folder set to never has no grouping on the board; it is set back to
// auto by the id in the settings list, and groups again at once when its
// agents qualify it.
func TestNeverIsUndoneByItsListedID(t *testing.T) {
	e, broad, part := broadEnv(t)
	e.set(t, broad, contract.GroupingNever)
	if _, ok := e.projects(t)[pid(broad)]; !ok {
		t.Fatal("setup: the broad folder has cards of its own and should stay a project")
	}
	e.board.applySync(model.SessionGone{Key: claudeKey("b1")})
	e.board.applySync(model.SessionGone{Key: claudeKey("b2")})
	if _, ok := e.projects(t)[pid(broad)]; ok {
		t.Fatal("setup: the folder should have left the board")
	}
	e.set(t, broad, contract.GroupingAuto) // not on the board: found by its setting
	if e.setting(broad) != "" || len(e.board.Snapshot().GroupingSettings) != 0 {
		t.Fatal("the setting is still listed")
	}
	if len(e.testEnv.state.Grouping()) != 0 {
		t.Fatalf("the state file still has %v", e.testEnv.state.Grouping())
	}
	if e.grouped(t, part, broad) {
		t.Fatal("never -> auto remembered a grouping")
	}
	e.agent("b1", broad)
	e.agent("b2", broad)
	if !e.grouped(t, part, broad) {
		t.Fatal("not grouped again on auto")
	}
}

// Always keeps an umbrella project grouped while only its parts are used,
// past the 30 days and across a restart (the other case of #49).
func TestAlwaysKeepsAGroupingPast30Days(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.rememberUmbrella(t, umbrella, part)
	e.set(t, umbrella, contract.GroupingAlways)
	start := e.clock.Now()
	for d := 10; d <= 60; d += 10 {
		e.clock.Set(start.Add(time.Duration(d) * 24 * time.Hour))
		e.agent("p", part)
		if !e.grouped(t, part, umbrella) {
			t.Fatalf("day %d: no longer grouped", d)
		}
	}
	e.reopen(t)
	e.agent("p", part)
	if !e.grouped(t, part, umbrella) {
		t.Fatal("not grouped after a restart")
	}
	if got := e.testEnv.state.Grouping(); !maps.Equal(got, map[string]string{umbrella: state.GroupingAlways}) {
		t.Fatalf("state file %v", got)
	}
}

// Always needs no agent in the folder and no memory: a folder set to always
// groups a single repository inside it with one session. It is listed only
// while a part is on the board; with one session in the folder itself and no
// part it is a plain project, not a grouping.
func TestAlwaysWithOneSessionOrNone(t *testing.T) {
	e, broad, part := broadEnv(t)
	e.set(t, broad, contract.GroupingAlways)
	for _, k := range []string{"b1", "b2", "p"} {
		e.board.applySync(model.SessionGone{Key: claudeKey(k)})
	}
	e.board.mu.Lock()
	clear(e.board.umbrellas) // no memory at all: only the setting
	e.board.mu.Unlock()
	if _, ok := e.projects(t)[pid(broad)]; ok {
		t.Fatal("listed with nothing of it on the board")
	}
	e.agent("p", part)
	if !e.grouped(t, part, broad) {
		t.Fatal("one session in a part: not grouped")
	}
	e.board.applySync(model.SessionGone{Key: claudeKey("p")})
	e.agent("b1", broad)
	ps := e.projects(t)
	if p, ok := ps[pid(broad)]; !ok || p.ParentID != "" {
		t.Fatalf("one session in the folder itself: %+v %v", p, ok)
	}
	for _, p := range ps {
		if p.ParentID != "" {
			t.Fatalf("a part with no part on the board: %+v", p)
		}
	}
}

// Back to auto from always keeps the grouping, remembered from then on: it
// expires 30 days later like any umbrella. Always itself did not age.
func TestAlwaysThenAutoIsRememberedFromThen(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.rememberUmbrella(t, umbrella, part)
	e.set(t, umbrella, contract.GroupingAlways)
	e.clock.Set(e.clock.Now().Add(40 * 24 * time.Hour))
	e.agent("p", part)
	_ = e.projects(t)
	if mem, _ := e.remembered(umbrella); !mem {
		t.Fatal("an umbrella set to always aged out of memory")
	}
	e.set(t, umbrella, contract.GroupingAuto)
	back := e.clock.Now()
	if !e.grouped(t, part, umbrella) {
		t.Fatal("back to auto split the grouping")
	}
	if at, ok := e.testEnv.state.Umbrellas()[umbrella]; !ok || !at.Equal(back) {
		t.Fatalf("remembered in the file at %v %v, want %v", at, ok, back)
	}
	e.clock.Set(back.Add(state.UmbrellaRetention - time.Hour))
	e.agent("p", part)
	if !e.grouped(t, part, umbrella) {
		t.Fatal("expired before 30 days")
	}
	e.clock.Set(back.Add(state.UmbrellaRetention + time.Hour))
	e.agent("p", part)
	if e.grouped(t, part, umbrella) {
		t.Fatal("still grouped 30 days after going back to auto")
	}
}

// Never on a folder inside a grouped parent: the folder is no grouping of its
// own, but as a plain folder with an agent it folds into the parent, and its
// repository (two levels below the parent) stays top-level.
func TestNeverInsideAGroupedParent(t *testing.T) {
	e := newGroupEnv(t)
	outer := e.dir(t, "work/outer")
	inner := e.dir(t, "work/outer/inner")
	repo := e.repo(t, "work/outer/inner/repo-a")
	e.agent("o1", outer)
	e.agent("o2", outer)
	e.agent("i1", inner)
	e.agent("i2", inner)
	e.agent("r", repo)
	ps := e.projects(t)
	if ps[pid(repo)].ParentID != pid(inner) || ps[pid(inner)].ParentID != "" {
		t.Fatalf("setup: inner %+v, repo %+v", ps[pid(inner)], ps[pid(repo)])
	}
	e.set(t, inner, contract.GroupingNever)
	ps = e.projects(t)
	if p := ps[pid(inner)]; p.ParentID != pid(outer) {
		t.Fatalf("inner, set to never: %+v", p)
	}
	if p := ps[pid(repo)]; p.ParentID != "" {
		t.Fatalf("its repository: %+v", p)
	}
}

// Never on the parent of a grouped folder stops only the parent: the folder
// below still groups its parts.
func TestNeverOnAParentOfAGrouping(t *testing.T) {
	e := newGroupEnv(t)
	broad := e.dir(t, "work")
	side := e.dir(t, "work/side")
	proj := e.dir(t, "work/proj")
	repo := e.repo(t, "work/proj/repo-a")
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("s", side)
	e.agent("c", proj)
	e.worker("r", repo, "c")
	if p := e.projects(t)[pid(side)]; p.ParentID != pid(broad) {
		t.Fatalf("setup: side %+v", p)
	}
	e.set(t, broad, contract.GroupingNever)
	ps := e.projects(t)
	if p := ps[pid(side)]; p.ParentID != "" {
		t.Fatalf("side still folds into a folder set to never: %+v", p)
	}
	if p := ps[pid(repo)]; p.ParentID != pid(proj) {
		t.Fatalf("the grouping below stopped: %+v", p)
	}
}

// overlapStore lists a folder as forgotten and as set, as a file edited by
// hand could.
type overlapStore struct {
	state.Store
	forgotten map[string]time.Time
	umbrellas map[string]time.Time
}

func (o overlapStore) Forgotten() map[string]time.Time { return maps.Clone(o.forgotten) }

func (o overlapStore) Umbrellas() map[string]time.Time {
	if o.umbrellas == nil {
		return o.Store.Umbrellas()
	}
	return maps.Clone(o.umbrellas)
}

// A held folder is never a grouping, so the page cannot set it; a file
// edited by hand can still list a folder as both. The setting wins at load,
// as in the store. Forgetting a folder set to always removes its setting
// (auto, held).
func TestSettingAndForgetReplaceEachOther(t *testing.T) {
	e, broad, part := broadEnv(t)
	if err := e.testEnv.state.SetGrouping(broad, state.GroupingAlways, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.board = newBoard(t, Options{Repos: e.repos, PRs: e.prs, State: overlapStore{e.testEnv.state, map[string]time.Time{broad: e.clock.Now()}, nil}, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
	e.agent("p", part)
	if _, held := e.memHold(broad); held {
		t.Fatal("a folder with a setting was loaded as held")
	}
	if !e.grouped(t, part, broad) {
		t.Fatal("a folder set to always does not group")
	}

	e.board = newBoard(t, Options{Repos: e.repos, PRs: e.prs, State: e.testEnv.state, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	_ = e.projects(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	if e.setting(broad) != "" || len(e.testEnv.state.Grouping()) != 0 {
		t.Fatal("forget kept the setting")
	}
	if !e.held(broad) || e.grouped(t, part, broad) {
		t.Fatal("forget of a folder set to always did not hold it")
	}
	if got := e.board.Snapshot().GroupingSettings; len(got) != 0 {
		t.Fatalf("still listed: %+v", got)
	}
}

// Refusals: an id on the board that is no grouping, an unknown id, a mode the
// contract does not list. A no-op writes nothing.
func TestSetGroupingRefusals(t *testing.T) {
	e, broad, part := broadEnv(t)
	if err := e.board.SetGrouping(pid(part), contract.GroupingNever); !errors.Is(err, ErrNotGrouping) {
		t.Fatalf("a part: %v", err)
	}
	if err := e.board.SetGrouping("p_ffffffffffff", contract.GroupingNever); !errors.Is(err, ErrUnknownProject) {
		t.Fatalf("an unknown id: %v", err)
	}
	if err := e.board.SetGrouping(pid(broad), "sometimes"); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
	if len(e.testEnv.state.Grouping()) != 0 || e.setting(broad) != "" {
		t.Fatal("a refusal changed something")
	}
}

// failingSetting is a store whose SetGrouping cannot write.
type failingSetting struct{ state.Store }

func (failingSetting) SetGrouping(string, string, time.Time) error { return errDiskFull }

// When the state file cannot be written nothing changes.
func TestSetGroupingWriteFailureChangesNothing(t *testing.T) {
	e, broad, part := broadEnv(t)
	e.board = newBoard(t, Options{Repos: e.repos, PRs: e.prs, State: failingSetting{e.testEnv.state}, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	_ = e.projects(t)
	if err := e.board.SetGrouping(pid(broad), contract.GroupingAuto); err != nil {
		t.Fatalf("a no-op (auto on a folder with no setting) wrote: %v", err)
	}
	if err := e.board.SetGrouping(pid(broad), contract.GroupingNever); !errors.Is(err, errDiskFull) {
		t.Fatalf("got %v, want the write error", err)
	}
	if !e.grouped(t, part, broad) || e.setting(broad) != "" {
		t.Fatal("a failed write changed the board")
	}
	if mem, _ := e.remembered(broad); !mem {
		t.Fatal("a failed write dropped the memory")
	}
}

// Without a state file settings live in memory, capped like the file.
func TestSetGroupingCapWithoutStateFile(t *testing.T) {
	e, broad, part := broadEnv(t)
	e.board = newBoard(t, Options{Repos: e.repos, PRs: e.prs, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	_ = e.projects(t)
	if err := e.board.SetGrouping(pid(broad), "sometimes"); err == nil || e.setting(broad) != "" {
		t.Fatalf("an unknown mode without a state file: %v, set %q", err, e.setting(broad))
	}
	e.board.mu.Lock()
	for i := range state.MaxGroupingSettings {
		e.board.settings[fmt.Sprintf("%s/f%03d", e.home, i)] = state.GroupingNever
	}
	e.board.mu.Unlock()
	if err := e.board.SetGrouping(pid(broad), contract.GroupingNever); !errors.Is(err, ErrTooManySettings) {
		t.Fatalf("past the cap: %v", err)
	}
	e.board.mu.Lock()
	delete(e.board.settings, e.home+"/f000")
	e.board.mu.Unlock()
	e.set(t, broad, contract.GroupingNever)
	if e.grouped(t, part, broad) {
		t.Fatal("never without a state file did not split")
	}
}

// The tick writes without the board lock. Here it read a stale umbrella as
// too old; in its gap the folder qualifies again and is set to always, then
// back to auto, which remembers it. The late expiry drops it from the file,
// and the next tick writes it back, so memory and file agree.
func TestAutoFromAlwaysSurvivesALateExpiry(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.rememberUmbrella(t, umbrella, part)
	e.board.applySync(model.SessionGone{Key: claudeKey("p")})
	e.clock.Set(e.clock.Now().Add(state.UmbrellaRetention + time.Hour))
	e.inGap(t, func() {
		e.back(umbrella, "u1", "u2")
		e.back(part, "p")
		if err := e.board.SetGrouping(pid(umbrella), contract.GroupingAlways); err != nil {
			t.Errorf("always: %v", err)
		}
		if err := e.board.SetGrouping(pid(umbrella), contract.GroupingAuto); err != nil {
			t.Errorf("auto: %v", err)
		}
	})
	e.board.tick()
	mem, file := e.remembered(umbrella)
	if !mem || !file {
		t.Fatalf("remembered: memory %v, file %v", mem, file)
	}
}

// A folder set to never that a file edited by hand also lists as remembered
// does not group.
func TestNeverWinsOverARememberedUmbrellaAtLoad(t *testing.T) {
	e, broad, part := broadEnv(t)
	if err := e.testEnv.state.SetGrouping(broad, state.GroupingNever, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.board = newBoard(t, Options{Repos: e.repos, PRs: e.prs, State: overlapStore{e.testEnv.state, nil, map[string]time.Time{broad: e.clock.Now()}}, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
	e.agent("p", part)
	if e.grouped(t, part, broad) {
		t.Fatal("a folder set to never grouped as a remembered umbrella")
	}
}

// Always has no effect on a folder that has a project of its own that is no
// longer plain (git init), like a remembered umbrella; the setting stays
// listed so it can be undone.
func TestAlwaysFolderThatBecameARepository(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work/umbrella")
	part := e.repo(t, "work/umbrella/repo-a")
	e.rememberUmbrella(t, umbrella, part)
	e.set(t, umbrella, contract.GroupingAlways)
	e.board.mu.Lock()
	clear(e.board.umbrellas)
	e.board.mu.Unlock()
	e.dir(t, "work/umbrella/.git")
	e.repos.set(umbrella, model.Repo{Root: umbrella, MainRoot: umbrella, Branch: model.Of("main")})
	e.agent("u", umbrella)
	e.clock.Set(e.clock.Now().Add(2 * time.Minute)) // live folders are resolved again
	_ = e.projects(t)
	if e.grouped(t, part, umbrella) {
		t.Fatal("a folder set to always that is now a repository still groups")
	}
	if got := e.board.Snapshot().GroupingSettings; len(got) != 1 || got[0].Mode != contract.GroupingAlways {
		t.Fatalf("settings %+v", got)
	}
}

// The settings list is sorted by name, then project id.
func TestSettingsAreSortedByNameThenID(t *testing.T) {
	e := newGroupEnv(t)
	e.board.mu.Lock()
	for _, rel := range []string{"x/zeta", "x/alpha", "y/alpha", "x/mid"} {
		e.board.settings[e.home+"/"+rel] = state.GroupingNever
	}
	e.board.mu.Unlock()
	e.rebuild()
	got := e.board.Snapshot().GroupingSettings
	a1, a2 := pid(e.home+"/x/alpha"), pid(e.home+"/y/alpha")
	if a1 > a2 {
		a1, a2 = a2, a1
	}
	want := []string{a1, a2, pid(e.home + "/x/mid"), pid(e.home + "/x/zeta")}
	for i, s := range got {
		if s.ProjectID != want[i] {
			t.Fatalf("setting %d is %s (%s), want %s", i, s.Name, s.ProjectID, want[i])
		}
	}
	if got[0].Path != "~/x/alpha" && got[0].Path != "~/y/alpha" {
		t.Fatalf("path %q", got[0].Path)
	}
}

// Once sitboard is stopping (the store is closed), a setting or a forget is
// refused with state.ErrClosed and changes nothing on the board: a change
// the owner made on purpose is reported as not saved, never swallowed.
func TestSettingAndForgetAfterCloseAreRefused(t *testing.T) {
	e, broad, part := broadEnv(t)
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	_ = e.projects(t)
	e.testEnv.state.Close()
	if err := e.board.SetGrouping(pid(broad), contract.GroupingNever); !errors.Is(err, state.ErrClosed) {
		t.Fatalf("SetGrouping after Close: %v", err)
	}
	if err := e.board.ForgetGrouping(pid(broad)); !errors.Is(err, state.ErrClosed) {
		t.Fatalf("ForgetGrouping after Close: %v", err)
	}
	if !e.grouped(t, part, broad) || e.setting(broad) != "" {
		t.Fatal("a refused change changed the board")
	}
	if mem, _ := e.remembered(broad); !mem {
		t.Fatal("a refused change dropped the memory")
	}
}

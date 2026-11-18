package core

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// broadEnv is the case the issue is about: two agents sit in a broad plain
// folder at once, and a repository inside it has an agent, so the folder
// groups the repository and, after umbrellaSettle, is remembered.
func broadEnv(t *testing.T) (e *groupEnv, broad, part string) {
	t.Helper()
	e = newGroupEnv(t)
	broad = e.dir(t, "work")
	part = e.repo(t, "work/repo-a")
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	e.settle(t)
	if p := e.projects(t)[pid(part)]; p.ParentID != pid(broad) {
		t.Fatalf("setup: not grouped: %+v", p)
	}
	if _, ok := e.testEnv.state.Umbrellas()[broad]; !ok {
		t.Fatal("setup: not remembered")
	}
	return e, broad, part
}

func (e *groupEnv) grouped(t *testing.T, part, umbrella string) bool {
	t.Helper()
	return e.projects(t)[pid(part)].ParentID == pid(umbrella)
}

func (e *groupEnv) remembered(umbrella string) (inMemory, inFile bool) {
	e.board.mu.Lock()
	_, inMemory = e.board.umbrellas[umbrella]
	e.board.mu.Unlock()
	_, inFile = e.testEnv.state.Umbrellas()[umbrella]
	return inMemory, inFile
}

func (e *groupEnv) held(umbrella string) bool {
	_, ok := e.testEnv.state.Forgotten()[umbrella]
	return ok
}

// Forgetting while the condition still holds splits the grouping at once,
// and it is not remembered again after umbrellaSettle, though both agents
// still sit in the folder.
func TestForgetSplitsWhileTheConditionHolds(t *testing.T) {
	e, broad, part := broadEnv(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	if p := e.board.Snapshot().Projects; len(p) != 2 {
		t.Fatalf("projects right after forgetting: %+v", p)
	}
	if e.grouped(t, part, broad) {
		t.Fatal("still grouped right after forgetting")
	}
	if mem, file := e.remembered(broad); mem || file {
		t.Fatalf("still remembered: memory %v, file %v", mem, file)
	}
	if !e.held(broad) {
		t.Fatal("the hold is not in the state file")
	}
	for range 3 {
		e.settle(t)
	}
	if e.grouped(t, part, broad) {
		t.Fatal("grouped again while held")
	}
	if mem, file := e.remembered(broad); mem || file {
		t.Fatalf("remembered again while held: memory %v, file %v", mem, file)
	}
}

// The hold is in the state file: a restart with the same agents does not
// group the folder again.
func TestForgetSurvivesARestart(t *testing.T) {
	e, broad, part := broadEnv(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	e.reopen(t)
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	e.settle(t)
	if e.grouped(t, part, broad) {
		t.Fatal("grouped again after a restart")
	}
	if mem, file := e.remembered(broad); mem || file {
		t.Fatalf("remembered after a restart: memory %v, file %v", mem, file)
	}
}

// A coordinator in the folder with a worker inside: the worker ends and a new
// one starts while the coordinator stays. The coordinator still sits in the
// folder, so the hold stays and the new worker does not group it.
func TestForgetHoldsWhileTheCoordinatorStays(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work")
	partA, partB := e.repo(t, "work/repo-a"), e.repo(t, "work/repo-b")
	e.agent("c", umbrella)
	e.worker("w1", partA, "c")
	e.settle(t)
	if err := e.board.ForgetGrouping(pid(umbrella)); err != nil {
		t.Fatal(err)
	}
	e.board.applySync(model.SessionGone{Key: claudeKey("w1")})
	e.settle(t)
	e.settle(t)
	e.worker("w2", partB, "c")
	e.settle(t)
	if e.grouped(t, partB, umbrella) {
		t.Fatal("a new worker grouped the folder again while its coordinator stayed")
	}
	if !e.held(umbrella) {
		t.Fatal("the hold was lifted while the coordinator sat in the folder")
	}
}

// When the last agent leaves the folder and umbrellaSettle passes, the hold
// lifts. Two agents sitting there again are new evidence: the folder groups
// and is remembered as before.
func TestForgetLiftsOnceTheFolderIsEmpty(t *testing.T) {
	e, broad, part := broadEnv(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	e.board.applySync(model.SessionGone{Key: claudeKey("b1")})
	e.board.applySync(model.SessionGone{Key: claudeKey("b2")})
	_ = e.projects(t) // the folder is seen empty
	e.clock.Set(e.clock.Now().Add(umbrellaSettle - time.Second))
	_ = e.projects(t)
	if !e.held(broad) {
		t.Fatal("the hold lifted before the folder was empty for umbrellaSettle")
	}
	e.clock.Set(e.clock.Now().Add(time.Second))
	_ = e.projects(t)
	if e.held(broad) {
		t.Fatal("the hold did not lift after the folder was empty for umbrellaSettle")
	}

	e.agent("b3", broad)
	e.agent("b4", broad)
	e.settle(t)
	if !e.grouped(t, part, broad) {
		t.Fatal("not grouped after the hold lifted")
	}
	if mem, file := e.remembered(broad); !mem || !file {
		t.Fatalf("not remembered after the hold lifted: memory %v, file %v", mem, file)
	}
}

// An agent that briefly leaves and comes back (a restart of the session)
// does not lift the hold: the folder must be empty for umbrellaSettle.
func TestForgetHoldsThroughABriefGap(t *testing.T) {
	e, broad, part := broadEnv(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	e.board.applySync(model.SessionGone{Key: claudeKey("b1")})
	e.board.applySync(model.SessionGone{Key: claudeKey("b2")})
	_ = e.projects(t)
	e.clock.Set(e.clock.Now().Add(umbrellaSettle / 2))
	e.agent("b1", broad)
	e.agent("b2", broad)
	_ = e.projects(t)
	e.clock.Set(e.clock.Now().Add(umbrellaSettle))
	e.settle(t)
	if e.grouped(t, part, broad) || !e.held(broad) {
		t.Fatal("a brief gap lifted the hold")
	}
}

// While a watcher is still on its first pass, a session's folder may not be
// known yet: the hold does not lift, however long that takes.
func TestForgetDoesNotLiftBeforeSourcesSettle(t *testing.T) {
	e, broad, _ := broadEnv(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	e.reopen(t) // a restart; no agent has been read yet
	e.board.SetSources([]source.Status{{Kind: source.KindClaude, State: source.StateOK}})
	for range 5 {
		_ = e.projects(t)
		e.clock.Set(e.clock.Now().Add(umbrellaSettle))
	}
	_ = e.projects(t)
	if !e.held(broad) {
		t.Fatal("the hold lifted before the sources settled")
	}
	e.board.applySync(model.SourceReady{Kind: string(source.KindClaude)})
	_ = e.projects(t)
	if !e.held(broad) {
		t.Fatal("the hold lifted the moment the sources settled")
	}
	e.clock.Set(e.clock.Now().Add(umbrellaSettle))
	_ = e.projects(t)
	if e.held(broad) {
		t.Fatal("the hold did not lift once settled and empty")
	}
}

// Forgetting twice: the second time the project is no grouping any more.
// It still has cards of its own, so it is on the board: ErrNotGrouping.
func TestForgetTwice(t *testing.T) {
	e, broad, _ := broadEnv(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	_ = e.projects(t)
	before := e.testEnv.state.Forgotten()
	e.clock.Set(e.clock.Now().Add(time.Minute))
	if err := e.board.ForgetGrouping(pid(broad)); !errors.Is(err, ErrNotGrouping) {
		t.Fatalf("second forget: %v, want ErrNotGrouping", err)
	}
	if after := e.testEnv.state.Forgotten(); !after[broad].Equal(before[broad]) {
		t.Fatalf("the refused forget wrote the state file: %v -> %v", before, after)
	}
}

// A grouping that is only remembered (its coordinator ended) can be
// forgotten too: it splits at once, the hold lifts after umbrellaSettle
// (nobody sits in the folder), and the folder is not remembered again.
func TestForgetARememberedGrouping(t *testing.T) {
	e := newGroupEnv(t)
	umbrella := e.dir(t, "work")
	part := e.repo(t, "work/repo-a")
	e.agent("c", umbrella)
	e.worker("w", part, "c")
	e.settle(t)
	e.board.applySync(model.SessionGone{Key: claudeKey("c")})
	if !e.grouped(t, part, umbrella) {
		t.Fatal("setup: the remembered grouping does not show")
	}
	if err := e.board.ForgetGrouping(pid(umbrella)); err != nil {
		t.Fatal(err)
	}
	ps := e.projects(t)
	if ps[pid(part)].ParentID != "" {
		t.Fatal("still grouped after forgetting")
	}
	if _, ok := ps[pid(umbrella)]; ok {
		t.Fatal("the forgotten umbrella is still listed")
	}
	e.clock.Set(e.clock.Now().Add(umbrellaSettle))
	_ = e.projects(t)
	if e.held(umbrella) {
		t.Fatal("the hold on an empty folder did not lift")
	}
	e.reopen(t)
	e.agent("w", part)
	e.settle(t)
	if e.grouped(t, part, umbrella) {
		t.Fatal("grouped again after a restart")
	}
	if mem, file := e.remembered(umbrella); mem || file {
		t.Fatalf("remembered again: memory %v, file %v", mem, file)
	}
}

func TestForgetRefusals(t *testing.T) {
	e, _, part := broadEnv(t)
	if err := e.board.ForgetGrouping(pid(part)); !errors.Is(err, ErrNotGrouping) {
		t.Fatalf("a repository: %v, want ErrNotGrouping", err)
	}
	if err := e.board.ForgetGrouping("p_ffffffffffff"); !errors.Is(err, ErrUnknownProject) {
		t.Fatalf("an unknown project: %v, want ErrUnknownProject", err)
	}
	if len(e.testEnv.state.Forgotten()) != 0 {
		t.Fatalf("a refusal wrote %v", e.testEnv.state.Forgotten())
	}
}

// failingForget is a store whose ForgetGrouping cannot write.
type failingForget struct{ state.Store }

var errDiskFull = errors.New("state: write: no space left on device")

func (failingForget) ForgetGrouping(string, time.Time) (time.Time, error) {
	return time.Time{}, errDiskFull
}

// When the state file cannot be written, nothing changes: the grouping stays,
// and it is still remembered.
func TestForgetWriteFailureChangesNothing(t *testing.T) {
	e, broad, part := broadEnv(t)
	e.board = newBoard(t, Options{Repos: e.repos, PRs: e.prs, State: failingForget{e.testEnv.state}, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	_ = e.projects(t)
	if err := e.board.ForgetGrouping(pid(broad)); !errors.Is(err, errDiskFull) {
		t.Fatalf("got %v, want the write error", err)
	}
	if !e.grouped(t, part, broad) {
		t.Fatal("a failed forget split the grouping")
	}
	if mem, _ := e.remembered(broad); !mem {
		t.Fatal("a failed forget dropped the memory")
	}
}

// The board without a state file forgets in memory only.
func TestForgetWithoutStateFile(t *testing.T) {
	e, broad, part := broadEnv(t)
	e.board = newBoard(t, Options{Repos: e.repos, PRs: e.prs, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	e.settle(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	e.settle(t)
	if e.grouped(t, part, broad) {
		t.Fatal("grouped again")
	}
}

// A watcher that fails drops its sessions: the folder looks empty, but that
// is no evidence. The hold stays while the source is failing, and the empty
// time starts again once it recovers.
func TestForgetHoldsWhileAWatcherIsFailing(t *testing.T) {
	e, broad, _ := broadEnv(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	e.board.DropSource(source.KindClaude, "watcher stopped")
	for range 5 {
		_ = e.projects(t)
		e.clock.Set(e.clock.Now().Add(umbrellaSettle))
	}
	_ = e.projects(t)
	if !e.held(broad) {
		t.Fatal("the hold lifted while the Claude watcher was failing")
	}
	e.board.SetSources([]source.Status{{Kind: source.KindClaude, State: source.StateOK}})
	e.board.applySync(model.SourceReady{Kind: string(source.KindClaude)})
	_ = e.projects(t)
	if !e.held(broad) {
		t.Fatal("the hold lifted at once when the watcher recovered")
	}
	e.clock.Set(e.clock.Now().Add(umbrellaSettle))
	_ = e.projects(t)
	if e.held(broad) {
		t.Fatal("the hold did not lift once the watcher recovered and the folder stayed empty")
	}
}

// Nested plain folders w, w/a and w/a/b, two agents in w and in a, one in b:
// b is a part of a. After forgetting a, a and b both fold into w, so the
// words "its parts are no longer grouped under it" stay true (they are not
// "their own projects" here).
func TestForgetANestedGrouping(t *testing.T) {
	e := newGroupEnv(t)
	w, a, b := e.dir(t, "w"), e.dir(t, "w/a"), e.dir(t, "w/a/b")
	e.agent("w1", w)
	e.agent("w2", w)
	e.agent("a1", a)
	e.agent("a2", a)
	e.agent("b1", b)
	ps := e.projects(t)
	if ps[pid(b)].ParentID != pid(a) || ps[pid(a)].ParentID != "" {
		t.Fatalf("setup: a %+v, b %+v", ps[pid(a)], ps[pid(b)])
	}
	if err := e.board.ForgetGrouping(pid(a)); err != nil {
		t.Fatal(err)
	}
	ps = e.projects(t)
	if ps[pid(a)].ParentID != pid(w) || ps[pid(b)].ParentID != pid(w) {
		t.Fatalf("after forgetting a: a %+v, b %+v; want both parts of w", ps[pid(a)], ps[pid(b)])
	}
}

// Memory keeps the same cut as the state file: the newest MaxUmbrellas holds.
// Otherwise past the cap the file would drop a hold that memory still has,
// and a restart would lose it.
func TestForgottenCappedInMemoryLikeTheFile(t *testing.T) {
	e := newGroupEnv(t)
	now := e.clock.Now()
	var b strings.Builder
	b.WriteString(`{"version":1,"seen":{},"forgotten":{`)
	for i := range state.MaxUmbrellas {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:%q", fmt.Sprintf("/held/%03d", i), now.Add(-time.Hour+time.Duration(i)*time.Second).Format(time.RFC3339))
	}
	b.WriteString(`}}`)
	if err := os.WriteFile(e.state, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	e.reopen(t)
	broad, part := e.dir(t, "work"), e.repo(t, "work/repo-a")
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	_ = e.projects(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	e.board.mu.Lock()
	mem := slices.Sorted(maps.Keys(e.board.forgotten))
	e.board.mu.Unlock()
	file := slices.Sorted(maps.Keys(e.testEnv.state.Forgotten()))
	if len(mem) != state.MaxUmbrellas || !slices.Equal(mem, file) {
		t.Fatalf("memory holds %d, the file %d; they differ: %v", len(mem), len(file), !slices.Equal(mem, file))
	}
	if slices.Contains(mem, "/held/000") || !slices.Contains(mem, broad) {
		t.Fatal("the cut did not drop the oldest hold first")
	}
	e.board.mu.Lock()
	_, vacant := e.board.vacantFrom["/held/000"]
	e.board.mu.Unlock()
	if vacant {
		t.Fatal("the cut left the dropped hold's empty-since time behind")
	}
}

// The tick does not hold the board while it waits for the state file: with
// another writer holding the write lock, snapshots are still served.
func TestTickDoesNotHoldTheBoardWhileWriting(t *testing.T) {
	e, broad, _ := broadEnv(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(e.clock.Now().Add(umbrellaRewrite)) // the hold's time is due to be written
	_ = e.board.Snapshot()
	e.board.writeMu.Lock() // a slow disk, in another writer
	done := make(chan struct{})
	go func() {
		e.board.tick()
		close(done)
	}()
	served := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond) // the tick is waiting on the write lock by now
		_ = e.board.Snapshot()
		e.board.Apply(model.SessionGone{Key: claudeKey("nobody")})
		close(served)
	}()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		e.board.writeMu.Unlock()
		t.Fatal("the board waited for the state file")
	}
	e.board.writeMu.Unlock()
	<-done
}

// inGap runs fn in the next tick, between the tick reading its changes and
// writing them, where a ForgetGrouping can fall. It fails the test when the
// tick has nothing to write (no gap).
func (e *groupEnv) inGap(t *testing.T, fn func()) {
	t.Helper()
	ran := false
	e.board.flushGap = func() {
		e.board.flushGap = nil
		ran = true
		fn()
	}
	defer func() { e.board.flushGap = nil }()
	e.board.tick()
	if !ran {
		t.Fatal("the tick wrote nothing")
	}
}

// rebuild derives the board again, as any fact does, without a tick (so
// nothing is written).
func (e *groupEnv) rebuild() { e.board.Apply(model.SessionGone{Key: claudeKey("nobody")}) }

// back puts agents in a folder that is resolved already, without waiting.
func (e *groupEnv) back(folder string, ids ...string) {
	for _, id := range ids {
		s := busySession(id, e.clock.Now())
		s.Folder = folder
		e.board.Apply(s)
	}
}

func (e *groupEnv) memHold(folder string) (time.Time, bool) {
	e.board.mu.Lock()
	defer e.board.mu.Unlock()
	at, ok := e.board.forgotten[folder]
	return at, ok
}

// releaseThenRegroupInGap forgets broad, empties it so its hold is released
// in the next tick, and in that tick's gap sets the clock with setClock, puts
// two agents back and forgets the folder again.
func releaseThenRegroupInGap(t *testing.T, setClock func(e *groupEnv, forgotAt time.Time)) (e *groupEnv, broad, part string) {
	t.Helper()
	e, broad, part = broadEnv(t)
	forgotAt := e.clock.Now()
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	e.board.applySync(model.SessionGone{Key: claudeKey("b1")})
	e.board.applySync(model.SessionGone{Key: claudeKey("b2")})
	_ = e.projects(t)
	e.clock.Set(e.clock.Now().Add(umbrellaSettle))
	e.inGap(t, func() {
		setClock(e, forgotAt)
		e.back(broad, "b3", "b4")
		if err := e.board.ForgetGrouping(pid(broad)); err != nil {
			t.Errorf("forget again: %v", err)
		}
	})
	return e, broad, part
}

// The tick writes without the board lock, so a forget can fall between the
// tick reading a release and writing it. Here the folder's hold was released
// (empty for umbrellaSettle), the agents come back, it groups and is forgotten
// again in that gap: the late release must not drop the new hold.
func TestForgetBetweenAReleaseAndItsWrite(t *testing.T) {
	e, broad, part := releaseThenRegroupInGap(t, func(e *groupEnv, _ time.Time) { e.clock.Set(e.clock.Now().Add(time.Second)) })
	if !e.held(broad) {
		t.Fatal("a release written late dropped the hold made after it")
	}
	e.reopen(t)
	e.agent("b3", broad)
	e.agent("b4", broad)
	e.agent("p", part)
	e.settle(t)
	if e.grouped(t, part, broad) {
		t.Fatal("grouped again after a restart")
	}
}

// The same, with the clock stepped back by more than umbrellaSettle in the
// gap, and with the clock reading exactly the released hold's time: the new
// hold is still stored later than the old one, so the late release keeps it.
func TestForgetInTheGapDoesNotDependOnTheClock(t *testing.T) {
	cases := map[string]func(e *groupEnv, forgotAt time.Time){
		"stepped back":     func(e *groupEnv, _ time.Time) { e.clock.Set(e.clock.Now().Add(-umbrellaSettle - 10*time.Minute)) },
		"the same reading": func(e *groupEnv, forgotAt time.Time) { e.clock.Set(forgotAt) },
	}
	for name, setClock := range cases {
		t.Run(name, func(t *testing.T) {
			e, broad, _ := releaseThenRegroupInGap(t, setClock)
			if !e.held(broad) {
				t.Fatal("a late release dropped the newer hold")
			}
			mem, _ := e.memHold(broad)
			if file := e.testEnv.state.Forgotten()[broad]; !file.Equal(mem) {
				t.Fatalf("memory holds %v, the file %v", mem, file)
			}
		})
	}
}

// Without a state file the hold's time still moves past a release that is
// queued, so memory never holds an older time than it released.
func TestForgetMovesPastAQueuedRelease(t *testing.T) {
	e, broad, part := broadEnv(t)
	e.board = newBoard(t, Options{Repos: e.repos, PRs: e.prs, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	_ = e.projects(t)
	forgotAt := e.clock.Now()
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	e.board.applySync(model.SessionGone{Key: claudeKey("b1")})
	e.board.applySync(model.SessionGone{Key: claudeKey("b2")})
	e.rebuild()
	e.clock.Set(e.clock.Now().Add(umbrellaSettle))
	e.rebuild()           // released in memory, queued
	e.clock.Set(forgotAt) // the clock reads the released time again
	e.back(broad, "b1", "b2")
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	if at, _ := e.memHold(broad); !at.After(forgotAt) {
		t.Fatalf("the new hold %v is not later than the released one %v", at, forgotAt)
	}
}

// A late touch of a forgotten folder never writes an older time over a
// newer one: the hold's time was refreshed and collected by the tick, and in
// its gap the folder empties, is released, groups and is forgotten again.
func TestALateTouchKeepsTheNewerHold(t *testing.T) {
	e, broad, _ := broadEnv(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(e.clock.Now().Add(umbrellaRewrite)) // the next tick refreshes the hold's time
	e.inGap(t, func() {
		e.board.Apply(model.SessionGone{Key: claudeKey("b1")})
		e.board.Apply(model.SessionGone{Key: claudeKey("b2")})
		e.clock.Set(e.clock.Now().Add(umbrellaSettle))
		e.rebuild() // released in memory
		e.back(broad, "b1", "b2")
		if err := e.board.ForgetGrouping(pid(broad)); err != nil {
			t.Errorf("forget again: %v", err)
		}
	})
	mem, _ := e.memHold(broad)
	if file := e.testEnv.state.Forgotten()[broad]; !file.Equal(mem) {
		t.Fatalf("the late touch wrote %v over the newer hold %v", file, mem)
	}
}

// A late umbrella touch of a folder forgotten in the gap is skipped: the
// folder is not remembered in the file, so it does not group again once its
// hold lifts.
func TestALateUmbrellaTouchDoesNotRememberAForgottenFolder(t *testing.T) {
	e, broad, _ := broadEnv(t)
	e.clock.Set(e.clock.Now().Add(umbrellaRewrite)) // the next tick refreshes the umbrella
	e.inGap(t, func() {
		if err := e.board.ForgetGrouping(pid(broad)); err != nil {
			t.Errorf("forget: %v", err)
		}
	})
	if _, ok := e.testEnv.state.Umbrellas()[broad]; ok {
		t.Fatal("a late umbrella touch remembered the forgotten folder in the file")
	}
	if !e.held(broad) {
		t.Fatal("the hold is not in the file")
	}
}

// A late tick (a slow GitHub refresh, a sleep) can carry a release and an
// umbrella touch of the same folder: the hold lifted, the folder grouped again
// for umbrellaSettle and was remembered, all before the tick wrote. The
// release is written first, so the touch is not skipped.
func TestReleaseAndRememberInOneTick(t *testing.T) {
	e, broad, _ := broadEnv(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	e.board.applySync(model.SessionGone{Key: claudeKey("b1")})
	e.board.applySync(model.SessionGone{Key: claudeKey("b2")})
	_ = e.projects(t)
	e.clock.Set(e.clock.Now().Add(umbrellaSettle))
	e.rebuild() // released in memory
	e.back(broad, "b1", "b2")
	e.clock.Set(e.clock.Now().Add(umbrellaSettle))
	e.back(broad, "b1", "b2") // live for umbrellaSettle: remembered in memory
	if mem, _ := e.remembered(broad); !mem {
		t.Fatal("setup: not remembered in memory")
	}
	e.board.tick()
	if e.held(broad) {
		t.Fatal("the release was not written")
	}
	if _, file := e.remembered(broad); !file {
		t.Fatal("the umbrella touch was skipped: memory remembers the folder, the file does not")
	}
}

// failingRelease is a store whose ReleaseForgotten fails the first n times.
type failingRelease struct {
	state.Store
	n atomic.Int32
}

func (f *failingRelease) ReleaseForgotten(held map[string]time.Time) error {
	if f.n.Add(-1) >= 0 {
		return errDiskFull
	}
	return f.Store.ReleaseForgotten(held)
}

// A release that could not be written is tried again on the next tick.
// Otherwise the file keeps a hold memory dropped, and after a restart a
// folder the owner did not forget again stays ungrouped.
func TestAFailedReleaseIsRetried(t *testing.T) {
	e, broad, part := broadEnv(t)
	st := &failingRelease{Store: e.testEnv.state}
	st.n.Store(1)
	e.board = newBoard(t, Options{Repos: e.repos, PRs: e.prs, State: st, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	_ = e.projects(t)
	if err := e.board.ForgetGrouping(pid(broad)); err != nil {
		t.Fatal(err)
	}
	e.board.applySync(model.SessionGone{Key: claudeKey("b1")})
	e.board.applySync(model.SessionGone{Key: claudeKey("b2")})
	_ = e.projects(t)
	e.clock.Set(e.clock.Now().Add(umbrellaSettle))
	_ = e.projects(t) // released in memory; the write fails
	if !e.held(broad) {
		t.Fatal("setup: the failing store released the hold")
	}
	_ = e.projects(t) // tried again
	if e.held(broad) {
		t.Fatal("a failed release was not tried again")
	}
}

// countingStore counts what the board writes, and which forgets fall in a
// tick's gap. It also checks, as each release is written, that a release
// older than the folder's latest hold leaves that hold in the file: nothing
// written late may undo a forget.
type countingStore struct {
	state.Store
	gap                                    atomic.Bool
	forgets, gapForgets, releases, flushes atomic.Int64
	undone                                 atomic.Int64 // late releases that removed a newer hold

	mu   sync.Mutex
	last map[string]time.Time // folder -> the time its latest forget stored
}

func (c *countingStore) ForgetGrouping(p string, at time.Time) (time.Time, error) {
	c.forgets.Add(1)
	if c.gap.Load() {
		c.gapForgets.Add(1)
	}
	stored, err := c.Store.ForgetGrouping(p, at)
	if err == nil {
		c.mu.Lock()
		if c.last == nil {
			c.last = map[string]time.Time{}
		}
		c.last[p] = stored
		c.mu.Unlock()
	}
	return stored, err
}

func (c *countingStore) ReleaseForgotten(held map[string]time.Time) error {
	if len(held) > 0 {
		c.flushes.Add(1)
		c.releases.Add(int64(len(held)))
	}
	err := c.Store.ReleaseForgotten(held)
	file := c.Forgotten()
	c.mu.Lock()
	for p, t := range held {
		if c.last[p].After(t) {
			if _, ok := file[p]; !ok {
				c.undone.Add(1)
			}
		}
	}
	c.mu.Unlock()
	return err
}

// Forgets, ticks and agents coming and going at once, with the folder empty
// long enough for holds to be released, and forgets falling in the ticks'
// gaps, some right ahead of the release their tick writes. At every gap, and
// after every tick's writes, a hold in memory is a hold in the file at a time
// no older (the forget writes first); a release written late never removes a
// newer hold (checked as it is written); once settled, memory and file agree
// both ways. Run with -race.
func TestForgetRaceStress(t *testing.T) {
	e, broad, part := broadEnv(t)
	st := &countingStore{Store: e.testEnv.state}
	e.board = newBoard(t, Options{Repos: e.repos, PRs: e.prs, State: st, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
	e.agent("b1", broad)
	e.agent("b2", broad)
	e.agent("p", part)
	_ = e.projects(t)

	var violations, gapViolations, tickViolations atomic.Int64
	var gaps, releaseGapForgets atomic.Int64
	// agree counts the holds in memory the file lacks, or holds older. With
	// b.mu and the write lock held no forget or write is half done.
	agree := func(count *atomic.Int64) {
		e.board.mu.Lock()
		e.board.writeMu.Lock()
		file := st.Forgotten()
		for f, at := range e.board.forgotten {
			if stored, ok := file[f]; !ok || stored.Before(at) {
				violations.Add(1)
				count.Add(1)
			}
		}
		e.board.writeMu.Unlock()
		e.board.mu.Unlock()
	}
	e.board.flushGap = func() {
		st.gap.Store(true)
		defer st.gap.Store(false)
		agree(&gapViolations) // between reading the changes and writing them
		gaps.Add(1)
		// Memory released the hold and the file still has it: this tick is
		// about to write that release. Two agents come in and the folder is
		// forgotten again before it lands, so the late release must not undo
		// the new hold (countingStore checks it as the release is written).
		if _, inMemory := e.memHold(broad); !inMemory && e.held(broad) {
			n := releaseGapForgets.Load()
			ids := []string{fmt.Sprintf("g%da", n), fmt.Sprintf("g%db", n)}
			e.back(broad, ids...)
			if e.board.ForgetGrouping(pid(broad)) == nil {
				releaseGapForgets.Add(1)
			}
			for _, id := range ids {
				e.board.Apply(model.SessionGone{Key: claudeKey(id)})
			}
			return
		}
		// Every other gap: the agents come back and the folder is forgotten
		// again, as an owner pressing the button at that moment would.
		if gaps.Load()%2 == 0 {
			e.back(broad, "b1", "b2")
			_ = e.board.ForgetGrouping(pid(broad))
		}
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { // the clock of the board
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				e.board.tick()
				agree(&tickViolations) // the tick's writes are all done
			}
		}
	}()
	go func() { // the agents: in, then out for longer than umbrellaSettle
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			e.back(broad, "b1", "b2")
			e.clock.Set(e.clock.Now().Add(time.Minute))
			time.Sleep(time.Millisecond)
			e.board.Apply(model.SessionGone{Key: claudeKey("b1")})
			e.board.Apply(model.SessionGone{Key: claudeKey("b2")})
			for range 3 {
				time.Sleep(time.Millisecond)
				e.clock.Set(e.clock.Now().Add(umbrellaSettle / 2))
			}
		}
	}()
	go func() { // the owner
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = e.board.ForgetGrouping(pid(broad))
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()
	// At least a second, and until each kind of event has happened: a loaded
	// machine takes longer, it does not fail the test.
	exercised := func() bool {
		return st.forgets.Load() > 0 && st.gapForgets.Load() > 0 && st.releases.Load() > 0 && st.flushes.Load() > 0 &&
			releaseGapForgets.Load() > 0
	}
	start := time.Now()
	for time.Since(start) < time.Second || !exercised() && time.Since(start) < time.Minute {
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	e.board.flushGap = nil

	// Settle: the folder empty for longer than umbrellaSettle, every write done.
	e.board.Apply(model.SessionGone{Key: claudeKey("b1")})
	e.board.Apply(model.SessionGone{Key: claudeKey("b2")})
	for range 3 {
		e.board.tick()
		e.clock.Set(e.clock.Now().Add(umbrellaSettle))
	}
	e.board.tick()
	_, inMemory := e.memHold(broad)
	if inMemory != e.held(broad) {
		t.Errorf("held in memory %v, in the file %v", inMemory, e.held(broad))
	}
	if n := st.undone.Load(); n > 0 {
		t.Errorf("%d times a release written late removed a newer hold from the file", n)
	}
	if n := violations.Load(); n > 0 {
		t.Errorf("%d times a hold in memory was missing from the file, or older there (%d in a gap, %d after a tick)",
			n, gapViolations.Load(), tickViolations.Load())
	}
	t.Logf("forgets %d (in a gap %d, ahead of a release in its gap %d), releases %d, ticks writing a release %d, gaps %d",
		st.forgets.Load(), st.gapForgets.Load(), releaseGapForgets.Load(), st.releases.Load(), st.flushes.Load(), gaps.Load())
	for name, n := range map[string]int64{"forgets": st.forgets.Load(), "forgets in a gap": st.gapForgets.Load(),
		"forgets ahead of a release in its gap": releaseGapForgets.Load(), "releases": st.releases.Load(), "ticks writing a release": st.flushes.Load()} {
		if n == 0 {
			t.Errorf("no %s: the stress test did not exercise them", name)
		}
	}
}

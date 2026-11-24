package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/state"
)

func withLockWait(d time.Duration) func(*runEnv) { return func(e *runEnv) { e.lockWait = d } }

// runRefused runs sitboard with home and expects it to stop with an error
// before it prints its link.
func runRefused(t *testing.T, cfg config, home string, opts ...func(*runEnv)) (string, error) {
	t.Helper()
	env := testEnv(home)
	out := &syncBuffer{}
	env.out = out
	for _, o := range opts {
		o(&env)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := run(ctx, cfg, env)
	if err == nil {
		t.Fatalf("ran, want a refusal:\n%s", out.String())
	}
	if linkRe.MatchString(out.String()) {
		t.Errorf("printed a link before refusing:\n%s", out.String())
	}
	return out.String(), err
}

func TestSecondInstanceOnOneStateFileIsRefused(t *testing.T) {
	home := t.TempDir()
	one := startIn(t, cfgWith(0, true), home)
	port := mustAtoi(t, one.port)
	for name, cfg := range map[string]config{
		"any port":                      cfgWith(0, true),
		"the first one's port, default": cfgWith(port, false), // would fall back
		"the first one's port, --port":  cfgWith(port, true),  // would fail to listen
	} {
		t.Run(name, func(t *testing.T) {
			out, err := runRefused(t, cfg, home)
			msg := err.Error()
			for _, want := range []string{
				"in use by another sitboard (process " + strconv.Itoa(os.Getpid()) + " on ",
				"--state <path>", "--no-state",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q: %s", want, msg)
				}
			}
			// The lock comes before the port: no fallback note, no listen error.
			if strings.Contains(out, "is taken") || strings.Contains(msg, "listen") {
				t.Errorf("reached the port:\n%s\n%s", out, msg)
			}
		})
	}
	// The first one is untouched and the lock goes with it.
	one.stop(t)
	startIn(t, cfgWith(0, true), home)
}

func TestQuickRestartWaitsForTheLock(t *testing.T) {
	home := t.TempDir()
	one := startIn(t, cfgWith(0, true), home)
	var stopped atomic.Bool
	go func() {
		time.Sleep(time.Second)
		stopped.Store(true)
		one.stop(t)
	}()
	startIn(t, cfgWith(0, true), home, withLockWait(state.LockWait))
	if !stopped.Load() {
		t.Error("the second board started while the first still held the lock")
	}
}

func TestHeldLockIsRefusedAfterTheWait(t *testing.T) {
	home := t.TempDir()
	startIn(t, cfgWith(0, true), home) // never stops during the test
	began := time.Now()
	_, err := runRefused(t, cfgWith(0, true), home, withLockWait(state.LockWait))
	if !strings.Contains(err.Error(), "in use by another sitboard") {
		t.Fatalf("err %v", err)
	}
	if d := time.Since(began); d < state.LockWait {
		t.Errorf("refused after %v, before the %v wait", d, state.LockWait)
	}
}

func TestNoStateTakesNoLock(t *testing.T) {
	home := t.TempDir()
	cfg := cfgWith(0, true)
	cfg.noState = true
	startIn(t, cfg, home)
	startIn(t, cfg, home) // a second throwaway board on the same home runs too
	one := startIn(t, cfgWith(0, true), home)
	if names := dirNames(t, home); !slices.Equal(names, []string{".local"}) {
		t.Errorf("home holds %v", names)
	}
	one.stop(t)
	// Only the board with a state file made a lock file.
	if _, err := os.Stat(state.LockFile(filepath.Join(defaultStateFolder(home), "sitboard", "state.json"))); err != nil {
		t.Errorf("no lock file for the board with a state file: %v", err)
	}
}

func TestLockUnsupportedWarnsAndRuns(t *testing.T) {
	home := t.TempDir()
	unsupported := func(e *runEnv) {
		e.lock = func(context.Context, string, time.Duration) (*state.FileLock, error) {
			return nil, fmt.Errorf("%w: operation not supported", state.ErrLockUnsupported)
		}
	}
	one := startIn(t, cfgWith(0, true), home, unsupported)
	two := startIn(t, cfgWith(0, true), home, unsupported) // runs as before the lock existed
	for _, r := range []*running{one, two} {
		if n := strings.Count(r.out.String(), "could not lock the state file"); n != 1 {
			t.Errorf("warned %d times:\n%s", n, r.out.String())
		}
	}
}

func TestOtherLockErrorsStopTheStart(t *testing.T) {
	home := t.TempDir()
	broken := func(e *runEnv) {
		e.lock = func(context.Context, string, time.Duration) (*state.FileLock, error) {
			return nil, fmt.Errorf("state: lock: bad file descriptor")
		}
	}
	_, err := runRefused(t, cfgWith(0, true), home, broken)
	if !strings.Contains(err.Error(), "bad file descriptor") {
		t.Errorf("err %v", err)
	}
}

// The state is read only once the lock is held: what the previous holder wrote
// just before letting go (a quick restart) is there, and the first write does
// not take it for a foreign change.
func TestStateIsReadAfterTheLock(t *testing.T) {
	home := t.TempDir()
	env := testEnv(home)
	last := time.Now().UTC().Truncate(time.Second)
	env.lock = func(ctx context.Context, file string, wait time.Duration) (*state.FileLock, error) {
		prev, err := state.Open(file) // the previous holder's last write
		if err != nil {
			return nil, err
		}
		if err := prev.SetSeen("written-while-stopping", last); err != nil {
			return nil, err
		}
		return state.Lock(ctx, file, wait)
	}
	st, _, unlock, err := openState(context.Background(), cfgWith(0, true), env)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if at, ok := st.SeenAt("written-while-stopping"); !ok || !at.Equal(last) {
		t.Errorf("read before the lock: the last write is missing (%v %v)", at, ok)
	}
}

// unlock stops the store's writes before the lock goes.
func TestUnlockStopsWritesFirst(t *testing.T) {
	home := t.TempDir()
	st, _, unlock, err := openState(context.Background(), cfgWith(0, true), testEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSeen("card", time.Now()); err != nil {
		t.Fatal(err)
	}
	file := stateFile(home, "linux", func(string) string { return "" })
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := st.SetSeen("card", time.Now()); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Errorf("wrote after unlock:\n%s", after)
	}
	// And the lock is free for the next one.
	next, _, unlockNext, err := openState(context.Background(), cfgWith(0, true), testEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	defer unlockNext()
	if _, ok := next.SeenAt("card"); !ok {
		t.Error("the next holder does not see the saved mark")
	}
}

// A stop (Ctrl-C) during the start ends it before anything listens, prints a
// link or opens a browser, whether the holder lets go during the wait, never
// does, or the lock comes just as the stop does.
func TestStopDuringTheLockWaitStartsNothing(t *testing.T) {
	for name, c := range map[string]struct {
		letGo time.Duration // 0: the holder never lets go during the test
		wait  time.Duration
		late  bool // the stop comes with the lock
	}{
		"the holder lets go during the wait": {letGo: 500 * time.Millisecond, wait: state.LockWait},
		"the holder never lets go":           {wait: time.Hour},
		"the stop comes with the lock":       {wait: 0, late: true},
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "s.json")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			env := testEnv(t.TempDir())
			out := &syncBuffer{}
			env.out = out
			var opened atomic.Int32
			env.open = func(string) error { opened.Add(1); return nil }
			env.lockWait = c.wait
			if c.late {
				env.lock = func(ctx context.Context, file string, wait time.Duration) (*state.FileLock, error) {
					l, err := state.Lock(ctx, file, wait)
					cancel()
					return l, err
				}
			} else {
				held, err := state.Lock(context.Background(), file, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = held.Release() }()
				if c.letGo > 0 {
					go func() { time.Sleep(c.letGo); _ = held.Release() }()
				}
				go func() { time.Sleep(100 * time.Millisecond); cancel() }() // Ctrl-C
			}
			cfg := cfgWith(0, true)
			cfg.noOpen = false
			cfg.statePath = file
			done := make(chan error, 1)
			go func() { done <- run(ctx, cfg, env) }()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("a stop is not an error: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("run went on after the stop")
			}
			if linkRe.MatchString(out.String()) || opened.Load() != 0 {
				t.Errorf("started after the stop (browser opened %d times):\n%s", opened.Load(), out.String())
			}
		})
	}
}

// slowStore holds a write in progress until told to go on, and checks, as
// the write lands, that the state file's lock is still held.
type slowStore struct {
	state.Store
	mu      sync.Mutex
	entered chan struct{}
	resume  chan struct{}
	held    func() bool
	early   atomic.Bool // the write landed after the lock was let go
}

func (s *slowStore) SetSeen(id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.entered)
	<-s.resume
	if !s.held() {
		s.early.Store(true)
	}
	return s.Store.SetSeen(id, at)
}

func (s *slowStore) Close() {
	s.mu.Lock() // like the file store: waits for the write in progress
	defer s.mu.Unlock()
	s.Store.Close()
}

// unlock stops the store (letting a write in progress finish) before it lets
// go of the lock.
func TestUnlockLetsAWriteInProgressFinishFirst(t *testing.T) {
	home := t.TempDir()
	env := testEnv(home)
	var file string
	slow := &slowStore{entered: make(chan struct{}), resume: make(chan struct{})}
	env.openStore = func(f string) (state.Store, error) {
		file = f
		st, err := state.Open(f)
		slow.Store = st
		return slow, err
	}
	slow.held = func() bool {
		l, err := state.Lock(context.Background(), file, 0)
		if err == nil {
			_ = l.Release()
			return false
		}
		return true
	}
	st, _, unlock, err := openState(context.Background(), cfgWith(0, true), env)
	if err != nil {
		t.Fatal(err)
	}
	wrote := make(chan error, 1)
	go func() { wrote <- st.SetSeen("card", time.Now()) }()
	<-slow.entered
	unlocked := make(chan struct{})
	go func() { unlock(); close(unlocked) }()
	time.Sleep(100 * time.Millisecond) // unlock is under way
	close(slow.resume)
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	<-unlocked
	if slow.early.Load() {
		t.Error("the lock was let go while a write was still in progress")
	}
}

// A state file that could not be written at open (the store keeps this run
// in memory) is said at the start.
func TestStartSaysWhenTheStateFileIsKeptInMemory(t *testing.T) {
	env := testEnv(t.TempDir())
	env.openStore = func(string) (state.Store, error) { return state.Memory(), nil }
	_, notes, unlock, err := openState(context.Background(), cfgWith(0, true), env)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "could not write the state file") }) {
		t.Fatalf("notes %q", notes)
	}
}

// A file of sitboard 0.4.0 or earlier in a folder sitboard cannot write is
// served in memory only, with the warning, and left as it was; a newer file
// there is still refused (review of #152, round 6, finding 41).
func TestStartServesAnOldFileInAnUnwritableFolderInMemory(t *testing.T) {
	for _, c := range []struct {
		name, doc string
		served    bool
	}{
		{"0.4.0 file", `{"version":1,"seen":{},"catchupMarker":"2026-11-17T09:00:00Z"}`, true},
		{"file without a marker", `{"version":1,"seen":{}}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			file := stateFile(home, "linux", func(string) string { return "" })
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(c.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Dir(file), 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Dir(file), 0o700) })
			st, notes, unlock, err := openState(context.Background(), cfgWith(0, true), testEnv(home))
			if !c.served {
				if err == nil {
					unlock()
					t.Fatal("served a file it cannot write")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			if !st.InMemory() || !slices.Contains(notes, memoryNote) {
				t.Fatalf("in memory %v, notes %q", st.InMemory(), notes)
			}
			if b, _ := os.ReadFile(file); string(b) != c.doc {
				t.Fatalf("the file changed: %s", b)
			}
		})
	}
}

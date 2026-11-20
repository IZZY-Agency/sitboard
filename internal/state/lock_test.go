//go:build unix

package state

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var helperLock *FileLock

// A helper process for the tests that need a second process: it takes the
// lock on the file named in SITBOARD_LOCK_HELPER, says "locked", and holds it
// until killed or until its stdin closes (the test binary ended), so no
// helper outlives the tests.
func TestMain(m *testing.M) {
	if file := os.Getenv("SITBOARD_LOCK_SPAWN"); file != "" {
		spawnAndExit(file, os.Getenv("SITBOARD_LOCK_CHILD"))
	}
	if file := os.Getenv("SITBOARD_LOCK_HELPER"); file != "" {
		l, err := Lock(context.Background(), file, 0)
		if err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		helperLock = l // keep it reachable: a collected file would close and drop the lock
		fmt.Println("locked")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// lockInOtherProcess starts a helper that holds the lock on file.
func lockInOtherProcess(t *testing.T, file string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "SITBOARD_LOCK_HELPER="+file)
	if _, err := cmd.StdinPipe(); err != nil { // held open until this process ends
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper: %q %v", line, err)
	}
	return cmd
}

func lockOrFail(t *testing.T, file string, wait time.Duration) *FileLock {
	t.Helper()
	l, err := Lock(context.Background(), file, wait)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Release() })
	return l
}

func TestLockRefusesASecondHolderAndNamesTheFirst(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	lockOrFail(t, file, 0)
	_, err := Lock(context.Background(), file, 0)
	var locked *LockedError
	if !errors.As(err, &locked) || locked.PID != os.Getpid() || locked.File != file {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(err.Error(), "(process "+strconv.Itoa(os.Getpid())+" on "+hostname()+")") {
		t.Errorf("message %q", err.Error())
	}
}

func TestLockAcrossProcessesAndAfterACrash(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	helper := lockInOtherProcess(t, file)
	_, err := Lock(context.Background(), file, 0)
	var locked *LockedError
	if !errors.As(err, &locked) || locked.PID != helper.Process.Pid {
		t.Fatalf("err %v, want the helper's pid %d", err, helper.Process.Pid)
	}
	// A crash: the system drops the lock, the lock file stays with a dead pid.
	_ = helper.Process.Kill()
	_ = helper.Wait()
	if b, _ := os.ReadFile(LockFile(file)); !strings.HasPrefix(string(b), strconv.Itoa(helper.Process.Pid)+"\n") {
		t.Fatalf("lock file holds %q", b)
	}
	lockOrFail(t, file, 0) // at once: the leftover does not hold the lock
	if b, _ := os.ReadFile(LockFile(file)); string(b) != ownHolder() {
		t.Errorf("lock file holds %q after taking it", b)
	}
}

func TestLockLeftoversDoNotBlock(t *testing.T) {
	for name, body := range map[string]string{
		"a dead pid":         strconv.Itoa(deadPID(t)) + "\n" + hostname() + "\n",
		"the longest record": "4294967295\n" + strings.Repeat("h", 255) + "\n",
		"a record cut short": "12345\nhost-na",
		"only a pid":         "12345",
		"empty":              "",
		// A power loss right after a holder wrote its record can leave the
		// file at its new size with zeros in place of the record.
		"one NUL":                 "\x00",
		"NULs the record's size":  strings.Repeat("\x00", len(ownHolder())),
		"NULs up to the limit":    strings.Repeat("\x00", maxHolder),
		"a pid, then NULs":        "12345\x00\x00\x00\x00",
		"a cut record, then NULs": "12345\nhost-na\x00\x00\x00",
		"a record, then NULs":     strconv.Itoa(deadPID(t)) + "\n" + hostname() + "\n\x00\x00",
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(LockFile(file), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			lockOrFail(t, file, 0) // at once: the leftover does not hold the lock
			if b, _ := os.ReadFile(LockFile(file)); string(b) != ownHolder() {
				t.Errorf("lock file holds %q, want only this process", b)
			}
		})
	}
}

// deadPID is the pid of a process that has ended.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestLockNamesNoPidItCannotTrust(t *testing.T) {
	for name, body := range map[string]string{
		"a dead pid":  strconv.Itoa(deadPID(t)) + "\n" + hostname() + "\n",
		"no pid yet":  "",
		"a pid cut":   "123",
		"no host yet": "123\n",
		"a zero pid":  "0\n" + hostname() + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "state.json")
			lockOrFail(t, file, 0)
			// What a holder that has not written its pid yet, or a stale pid, looks like.
			if err := os.WriteFile(LockFile(file), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Lock(context.Background(), file, 0)
			var locked *LockedError
			if !errors.As(err, &locked) || locked.PID != 0 {
				t.Fatalf("err %v", err)
			}
			if strings.Contains(err.Error(), "(process") {
				t.Errorf("names a pid it cannot trust: %q", err.Error())
			}
		})
	}
}

func TestLockWaitsForAHolderThatLetsGo(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	first, err := Lock(context.Background(), file, 0)
	if err != nil {
		t.Fatal(err)
	}
	var released atomic.Bool
	go func() {
		time.Sleep(time.Second)
		released.Store(true)
		_ = first.Release()
	}()
	lockOrFail(t, file, LockWait)
	if !released.Load() {
		t.Error("took the lock while the first holder still had it")
	}
}

func TestLockGivesUpAfterTheWait(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	lockOrFail(t, file, 0)
	began := time.Now()
	_, err := Lock(context.Background(), file, LockWait)
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("err %v", err)
	}
	if d := time.Since(began); d < LockWait {
		t.Errorf("gave up after %v, before the %v wait", d, LockWait)
	}
}

func TestReleaseLetsTheNextOneIn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	l, err := Lock(context.Background(), file, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(LockFile(file)); len(b) != 0 {
		t.Errorf("a released lock file still holds %q", b)
	}
	lockOrFail(t, file, 0)
	if err := l.Release(); err != nil { // twice is harmless
		t.Fatal(err)
	}
}

func TestLockUnsupportedFolder(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.ENOTSUP, syscall.EOPNOTSUPP, syscall.ENOLCK} {
		t.Run(errno.Error(), func(t *testing.T) {
			old := flock
			flock = func(int, int) error { return errno }
			t.Cleanup(func() { flock = old })
			_, err := Lock(context.Background(), filepath.Join(t.TempDir(), "state.json"), LockWait)
			if !errors.Is(err, ErrLockUnsupported) {
				t.Fatalf("err %v", err)
			}
		})
	}
	t.Run("another error", func(t *testing.T) {
		old := flock
		flock = func(int, int) error { return syscall.EBADF }
		t.Cleanup(func() { flock = old })
		_, err := Lock(context.Background(), filepath.Join(t.TempDir(), "state.json"), 0)
		if err == nil || errors.Is(err, ErrLockUnsupported) {
			t.Fatalf("err %v", err)
		}
	})
}

func TestLockFileSitsBesideTheStateFileAndSurvivesWrites(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "state.json")
	lockOrFail(t, file, 0)
	before, err := os.Stat(LockFile(file))
	if err != nil {
		t.Fatal(err)
	}
	if perm := before.Mode().Perm(); perm != 0o600 {
		t.Errorf("lock file mode %o", perm)
	}
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := s.SetSeen(fmt.Sprint("card", i), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(LockFile(file))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("the lock file was replaced: %v", err)
	}
	if _, err := Open(file); err != nil {
		t.Fatal(err)
	}
	_, err = Lock(context.Background(), file, 0)
	if !errors.As(err, new(*LockedError)) {
		t.Errorf("the lock did not hold through the writes: %v", err)
	}
}

// ownHolder is what this process writes into a lock file it holds.
func ownHolder() string { return strconv.Itoa(os.Getpid()) + "\n" + hostname() + "\n" }

func TestLockNamesALiveHolderOfAnotherUser(t *testing.T) {
	// Pid 1 is alive and, for anyone but root, not ours to signal (EPERM):
	// it still counts as a live holder.
	file := filepath.Join(t.TempDir(), "state.json")
	lockOrFail(t, file, 0)
	if err := os.WriteFile(LockFile(file), []byte("1\n"+hostname()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Lock(context.Background(), file, 0)
	var locked *LockedError
	if !errors.As(err, &locked) || locked.PID != 1 {
		t.Fatalf("err %v", err)
	}
}

func TestLockOnAnotherMachineNamesTheMachine(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	lockOrFail(t, file, 0)
	// A shared folder, or this Mac under its old name: the pid cannot be
	// checked from here, so it is named as the lock file gives it, with the
	// machine, which is true either way.
	if err := os.WriteFile(LockFile(file), []byte("4242\nOld-Name.local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Lock(context.Background(), file, 0)
	var locked *LockedError
	if !errors.As(err, &locked) || locked.PID != 4242 || locked.Host != "Old-Name.local" {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(err.Error(), "(process 4242 on Old-Name.local)") {
		t.Errorf("message %q", err.Error())
	}
}

func TestHolderTrustsOnlyAReadingThatStays(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(strconv.Itoa(os.Getpid()) + "\n" + hostname() + "\n"); err != nil {
		t.Fatal(err)
	}
	if pid, _ := holder(f, func() {}); pid != os.Getpid() {
		t.Fatalf("steady reading: pid %d", pid)
	}
	// A new holder replaces a stale pid between the two readings.
	changed := func() {
		_ = f.Truncate(0)
		_, _ = f.WriteAt([]byte("1\n"+hostname()+"\n"), 0)
	}
	if pid, host := holder(f, changed); pid != 0 || host != "" {
		t.Errorf("changing reading: pid %d host %q", pid, host)
	}
}

func TestLockFollowsALockFileReplacedBeforeItWasLocked(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	old := flock
	calls := 0
	flock = func(fd int, how int) error {
		calls++
		if calls == 1 { // another process replaces the lock file in between
			_ = os.Remove(LockFile(file))
			if err := os.WriteFile(LockFile(file), nil, 0o600); err != nil {
				t.Error(err)
			}
		}
		return old(fd, how)
	}
	t.Cleanup(func() { flock = old })
	l := lockOrFail(t, file, 0)
	if calls < 2 || !stillAt(l.f, LockFile(file)) {
		t.Errorf("calls %d; holds the file at the name: %v", calls, stillAt(l.f, LockFile(file)))
	}
	flock = old
	if _, err := Lock(context.Background(), file, 0); !errors.As(err, new(*LockedError)) {
		t.Errorf("the file at the name is not locked: %v", err)
	}
}

func TestLockRetriesAnInterruptedCall(t *testing.T) {
	old := flock
	calls := 0
	flock = func(fd int, how int) error {
		calls++
		if calls == 1 {
			return syscall.EINTR
		}
		return old(fd, how)
	}
	t.Cleanup(func() { flock = old })
	lockOrFail(t, filepath.Join(t.TempDir(), "state.json"), 0)
	if calls != 2 {
		t.Errorf("flock called %d times", calls)
	}
}

// A holder that locked its file read-only wrote nothing: the record there may
// be a leftover's, so a refusal names no one. Here the leftover names this
// very process, which is alive and would be named.
func TestLockNamesNoOneWhenTheHolderCannotWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens any file for writing")
	}
	file := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(LockFile(file), []byte(ownHolder()), 0o400); err != nil {
		t.Fatal(err)
	}
	lockOrFail(t, file, 0) // locked read-only: the record stays
	_, err := Lock(context.Background(), file, 0)
	var locked *LockedError
	if !errors.As(err, &locked) || locked.PID != 0 || locked.Host != "" {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "(process") {
		t.Errorf("names the leftover's process: %q", err.Error())
	}
}

// A lock file whose name keeps changing under Lock ends the start once the
// wait is over, instead of trying for ever.
func TestLockGivesUpOnANameThatKeepsChanging(t *testing.T) {
	old := lockedFileAt
	calls := 0
	lockedFileAt = func(*os.File, string) bool { calls++; return false }
	t.Cleanup(func() { lockedFileAt = old })
	done := make(chan error, 1)
	go func() { _, err := Lock(context.Background(), filepath.Join(t.TempDir(), "state.json"), 0); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "keeps being replaced") {
			t.Fatalf("err %v", err)
		}
		if calls < 2 {
			t.Errorf("gave up after %d tries, want a second one", calls)
		}
	case <-time.After(30 * time.Second): // a guard against a hang, not a measurement
		t.Fatal("Lock never returned")
	}
}

//go:build unix

package state

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// LockWait is how long Lock waits for another holder to let go: a sitboard
// that is stopping may still hold the lock for up to a second.
const LockWait = 3 * time.Second

// lockRetry is how often Lock tries again while it waits.
const lockRetry = 50 * time.Millisecond

// ErrLockUnsupported: the folder of the state file does not support locks
// (some network and FUSE folders). The caller may run without the lock.
var ErrLockUnsupported = errors.New("state: the folder of the state file does not support locks")

// LockedError: another process holds the lock.
type LockedError struct {
	File string
	PID  int    // the pid its lock file names; 0 when it cannot be trusted
	Host string // the machine its lock file names; "" when unknown
}

// Error names the process as the lock file names it, with its machine: on a
// Mac the machine's name can change with the network, so "process 12 on
// old-name" stays true whichever machine that was.
func (e *LockedError) Error() string {
	switch {
	case e.PID > 0 && e.Host != "":
		return fmt.Sprintf("the state file %s is in use by another sitboard (process %d on %s)", e.File, e.PID, e.Host)
	case e.PID > 0:
		return fmt.Sprintf("the state file %s is in use by another sitboard (process %d)", e.File, e.PID)
	}
	return fmt.Sprintf("the state file %s is in use by another sitboard", e.File)
}

// flock is syscall.Flock; tests replace it.
var flock = syscall.Flock

// FileLock is the lock on one state file, held until Release or until the
// process ends: the system drops it when the process dies, so a crash never
// leaves the file locked. Go opens files close-on-exec, so a child process
// (gh, the browser opener) never inherits the lock.
//
// Limits: the holder cannot notice its lock file being deleted (by hand, or
// by a cleaner of temporary folders); a later sitboard then locks a new file.
// Two hard links to one state file are two names with two lock files, so
// they do not exclude each other. On a network folder mounted without
// working locks, flock may only exclude processes of the same machine. In
// these cases the state store's warning on a foreign write (see
// checkForeignLocked) still tells the owner.
type FileLock struct {
	mu       sync.Mutex // Release may be called from more than one goroutine
	f        *os.File
	writable bool // false: opened read-only, the holder is not written
}

// LockFile is where the lock for file is held: next to it, never renamed
// over, so the atomic writes of the state file leave it alone.
func LockFile(file string) string { return file + ".lock" }

// holderForm is what a lock file of sitboard's holds: a pid and a host name
// on two lines, or the start of that (nothing, after a clean stop; part of
// it, when a holder died while writing it), followed by NUL bytes or not: a
// power loss right after a holder wrote its record can leave the file at its
// new size with zeros in place of some or all of the record. Anything else
// there is another program's file (--state ~/proj/Cargo would lock
// Cargo.lock), and is never written. fullHolder is a whole record, the only
// one a holder is named from.
//
// This form is fixed for good: every sitboard since the lock refuses a lock
// file in any other form as not its own. A later build must keep writing
// exactly this record and put anything new in a file of its own; a record
// with another field would make an older sitboard refuse to start after a
// crash of the newer one, until the lock file is removed.
var (
	holderForm = regexp.MustCompile(`\A[0-9]{0,10}(?:\n[^\n]{0,255}\n?)?\z`)
	fullHolder = regexp.MustCompile(`\A[0-9]{1,10}\n[^\n]{0,255}\n\z`)
)

// maxHolder bounds what is read of a lock file.
const maxHolder = 512

// Lock takes an exclusive advisory lock (flock) on LockFile(file), waiting up
// to wait while another process holds it, and no longer than ctx: a stop
// asked for while it waits ends the wait. Once held, the lock file says
// which process and machine hold it, for the other side's message only.
//
// The lock file is opened without following a link and only a regular file
// that is empty or in sitboard's form is used; anything else is refused and
// left as it is. One that cannot be opened for writing is locked read-only,
// without the holder's name; a refusal then names no holder either (see
// lockOrWait).
func Lock(ctx context.Context, file string, wait time.Duration) (*FileLock, error) {
	name := LockFile(file)
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return nil, fmt.Errorf("state: create folder: %w", err)
	}
	deadline := time.Now().Add(wait)
	for restarted := false; ; restarted = true {
		f, writable, err := openLockFile(name)
		if err != nil {
			return nil, err
		}
		// Checked before locking, so another program's file is not even
		// locked, and again once locked, before anything is written.
		err = checkHolderForm(f, name)
		if err == nil {
			err = lockOrWait(ctx, f, file, deadline, writable)
		}
		if err == nil {
			err = checkHolderForm(f, name)
		}
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		// The lock is on the file that was opened. If the name was removed or
		// replaced meanwhile, another process may lock the file now at the
		// name: start again on that one.
		if !lockedFileAt(f, name) {
			_ = f.Close()
			// Once more at least, then no longer than the wait: a name that
			// keeps changing must not hold the start for ever.
			if restarted && !time.Now().Before(deadline) {
				return nil, fmt.Errorf("state: the lock file %s keeps being replaced while sitboard locks it; stop what replaces it, or give sitboard another state file", name)
			}
			continue
		}
		if writable { // ours now: what a crashed holder left is replaced
			if err := f.Truncate(0); err == nil {
				_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"+hostname()+"\n"), 0)
			}
		}
		return &FileLock{f: f, writable: writable}, nil
	}
}

// openLockFile opens name, creating it, without following a link and without
// blocking on a named pipe; it accepts only a regular file. When the file
// cannot be opened for writing (another user's, mode 000, left by a run as
// root), it is opened read-only: flock does not need write access.
func openLockFile(name string) (*os.File, bool, error) {
	const how = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|how, 0o600)
	writable := true
	if errors.Is(err, fs.ErrPermission) {
		f, err = os.OpenFile(name, os.O_RDONLY|how, 0)
		writable = false
	}
	switch {
	case errors.Is(err, syscall.ELOOP):
		return nil, false, fmt.Errorf("state: %s is a link, not sitboard's lock file; move it aside, or give sitboard another state file", name)
	case errors.Is(err, fs.ErrPermission):
		return nil, false, fmt.Errorf("state: cannot open the lock file %s: %w; it holds no state, so remove it, or give sitboard another state file", name, err)
	case err != nil:
		return nil, false, fmt.Errorf("state: open the lock file: %w", err)
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("state: %s is not a regular file, not sitboard's lock file; move it aside, or give sitboard another state file", name)
	}
	if err != nil {
		_ = f.Close()
		return nil, false, err
	}
	return f, writable, nil
}

// checkHolderForm refuses a lock file that holds anything but sitboard's
// form (see holderForm).
func checkHolderForm(f *os.File, name string) error {
	if _, ok := holderRecord(readHolder(f)); ok {
		return nil
	}
	return fmt.Errorf("state: %s holds something that is not sitboard's; it is left as it is: move it aside, or give sitboard another state file", name)
}

// lockOrWait takes the lock on f, trying again until deadline while another
// process holds it, or until ctx ends.
//
// The refusal names the holder only when f could be opened for writing. A
// holder of the same user could then open it so too, and replaced what was
// there with its own record. When f could not be, the holder may have locked
// it read-only and written nothing, so the record there may be a leftover's,
// and naming its process would be wrong; the refusal then names no one. (A
// holder of another user is named only if this one can write its file.)
func lockOrWait(ctx context.Context, f *os.File, file string, deadline time.Time, writable bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("state: stopped while waiting for the lock: %w", err)
		}
		err := flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, syscall.EINTR):
			continue
		case unsupported(err):
			return fmt.Errorf("%w: %v", ErrLockUnsupported, err)
		case !errors.Is(err, syscall.EWOULDBLOCK):
			return fmt.Errorf("state: lock: %w", err)
		}
		if !time.Now().Before(deadline) {
			e := &LockedError{File: file}
			if writable {
				e.PID, e.Host = holder(f, func() { time.Sleep(lockRetry) })
			}
			return e
		}
		t := time.NewTimer(min(lockRetry, time.Until(deadline)))
		select {
		case <-ctx.Done():
			t.Stop()
		case <-t.C:
		}
	}
}

// lockedFileAt is stillAt; tests replace it.
var lockedFileAt = stillAt

// stillAt: f is the file at name.
func stillAt(f *os.File, name string) bool {
	a, err := f.Stat()
	if err != nil {
		return false
	}
	b, err := os.Lstat(name)
	return err == nil && os.SameFile(a, b)
}

// Release empties the lock file and lets go of the lock. Calling it again
// does nothing.
func (l *FileLock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	if l.writable {
		_ = l.f.Truncate(0)
	}
	err := l.f.Close() // closing drops the flock
	l.f = nil
	return err
}

// holder reads who holds the lock, twice with pause between: a holder that
// has just taken it may still be replacing what a crashed one left, so only a
// reading that stays the same is used. A pid that names no live process on
// this machine is a leftover and is not given; a lock file written on another
// machine name (a shared folder, or this Mac before its name changed) gives
// its pid and machine as written, since they cannot be checked from here.
func holder(f *os.File, pause func()) (pid int, host string) {
	first := readHolder(f)
	pause()
	if readHolder(f) != first || !fullHolder.MatchString(first) {
		return 0, ""
	}
	lines := strings.Split(first, "\n")
	pid, err := strconv.Atoi(lines[0])
	if err != nil || pid <= 0 {
		return 0, ""
	}
	host = lines[1]
	if host != "" && host != hostname() {
		return pid, host
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return 0, "" // no such process: a leftover
	}
	return pid, host
}

// holderRecord gives what raw (as readHolder read it) holds without the NUL
// bytes at its end, and whether that is in sitboard's form. A file larger
// than maxHolder never is, NULs or not.
func holderRecord(raw string) (string, bool) {
	if len(raw) > maxHolder {
		return "", false
	}
	rec := strings.TrimRight(raw, "\x00")
	return rec, holderForm.MatchString(rec)
}

// readHolder reads up to maxHolder+1 bytes, so an oversized file is seen as
// one.
func readHolder(f *os.File) string {
	buf := make([]byte, maxHolder+1)
	n, _ := f.ReadAt(buf, 0)
	return string(buf[:n])
}

// hostname is the machine's name, or "" when it cannot be known. It has no
// line break, so it fits holderForm.
func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	h = strings.ReplaceAll(strings.TrimSpace(h), "\n", "?")
	if r := []rune(strings.ToValidUTF8(h, "?")); len(r) > 255 {
		h = string(r[:255])
	}
	return strings.ToValidUTF8(h, "?")
}

func unsupported(err error) bool {
	return errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOLCK)
}

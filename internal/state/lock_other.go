//go:build !unix

package state

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// LockWait: see lock.go.
const LockWait = 3 * time.Second

// ErrLockUnsupported: see lock.go. Systems without flock never lock.
var ErrLockUnsupported = errors.New("state: the folder of the state file does not support locks")

// LockedError: see lock.go.
type LockedError struct {
	File string
	PID  int
	Host string
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("the state file %s is in use by another sitboard", e.File)
}

// FileLock: see lock.go.
type FileLock struct{}

// LockFile: see lock.go.
func LockFile(file string) string { return file + ".lock" }

// Lock: see lock.go. Without flock it always reports ErrLockUnsupported.
func Lock(context.Context, string, time.Duration) (*FileLock, error) { return nil, ErrLockUnsupported }

// Release: see lock.go.
func (l *FileLock) Release() error { return nil }

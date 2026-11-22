// Package runner starts the external tools the adapters read from (git, gh,
// agtermctl, ps, glab). Every call takes an argument list, never a shell string, and
// a timeout. Output is returned to the caller and never logged here.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"os/exec"
	"strings"
	"time"
)

// maxOutput caps what one call may return, so a misbehaving tool cannot fill
// memory. Output past the cap is dropped and the call fails.
const maxOutput = 32 << 20

// stderrCap is what one call keeps of standard error.
const stderrCap = 64 << 10

// ErrTooLarge says a tool printed more than maxOutput bytes.
var ErrTooLarge = errors.New("output too large")

// Func runs name with args and returns its standard output. It is the seam
// tests replace.
type Func func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExitError is a tool that ran and exited non-zero. Stderr is kept, capped, so
// the caller can classify the failure; it is never logged.
type ExitError struct {
	Code   int
	Stdout []byte
	Stderr []byte
}

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// ErrWarned is a tool that exited 0 but wrote to stderr, from ExecQuiet.
var ErrWarned = errors.New("warned on stderr")

// Exec returns a Func that runs real processes with the given timeout per call
// and extra environment entries appended to the inherited environment.
func Exec(timeout time.Duration, env ...string) Func { return execFunc(timeout, false, nil, env) }

// ExecQuiet is Exec for a tool whose answer is incomplete when it warns: a run
// that exits 0 with anything on stderr returns ErrWarned (git status warns
// about a folder it cannot read, and goes on as if it were empty).
func ExecQuiet(timeout time.Duration, env ...string) Func { return execFunc(timeout, true, nil, env) }

// ExecWithout is Exec (ExecQuiet when quiet) that first removes from the
// inherited environment every variable drop says yes to, by name.
func ExecWithout(timeout time.Duration, quiet bool, drop func(name string) bool, env ...string) Func {
	return execFunc(timeout, quiet, drop, env)
}

func execFunc(timeout time.Duration, quiet bool, drop func(string) bool, env []string) Func {
	return run(timeout, quiet, func(cmd *exec.Cmd) {
		if len(env) > 0 || drop != nil {
			cmd.Env = append(without(cmd.Environ(), drop), env...)
		}
	})
}

// ExecEnv returns a Func like Exec whose processes get exactly env as their
// environment, nothing inherited: for a tool that hands its environment on
// to another process.
func ExecEnv(timeout time.Duration, env []string) Func {
	env = append([]string{}, env...) // never nil: a nil Env means inherit all
	return run(timeout, false, func(cmd *exec.Cmd) { cmd.Env = env })
}

// Clean returns a Func like ExecEnv whose processes also run in dir. It is
// for a tool that must not see the owner's shell environment or the folder
// sitboard was started in.
func Clean(timeout time.Duration, dir string, env []string) Func {
	env = append([]string{}, env...) // never nil: a nil Env would inherit
	return run(timeout, false, func(cmd *exec.Cmd) {
		cmd.Env, cmd.Dir = env, dir
	})
}

func run(timeout time.Duration, quiet bool, setup func(*exec.Cmd)) Func {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		setup(cmd)
		cmd.WaitDelay = time.Second
		var out, errb capped
		out.limit, errb.limit = maxOutput, stderrCap
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: %w", name, ctx.Err())
		}
		if out.over {
			return nil, fmt.Errorf("%s: %w", name, ErrTooLarge)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, &ExitError{Code: ee.ExitCode(), Stdout: out.Bytes(), Stderr: errb.Bytes()}
		}
		if err != nil {
			return nil, err
		}
		if quiet && errb.buf.Len() > 0 {
			return nil, fmt.Errorf("%s: %w", name, ErrWarned)
		}
		return out.Bytes(), nil
	}
}

// capped is a buffer that stops growing at limit and remembers that it did.
// The buffer is a named field, not embedded: an embedded bytes.Buffer would
// give capped a ReadFrom, which io.Copy (how Go feeds a process's output to a
// writer) calls instead of Write, and the cap would not hold.
type capped struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); len(p) > room {
		c.over = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *capped) Bytes() []byte { return c.buf.Bytes() }

// without returns env minus the variables drop names; env itself when drop
// is nil.
func without(env []string, drop func(string) bool) []string {
	if drop == nil {
		return env
	}
	var kept []string
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); !drop(name) {
			kept = append(kept, kv)
		}
	}
	return kept
}

// Found reports whether name resolves to an executable. "" means not found.
func Found(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

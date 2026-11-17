// Package runner starts the external tools the adapters read from (git, gh,
// agtermctl, ps). Every call takes an argument list, never a shell string, and
// a timeout. Output is returned to the caller and never logged here.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// maxOutput caps what one call may return, so a misbehaving tool cannot fill
// memory. Output past the cap is dropped and the call fails.
const maxOutput = 32 << 20

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

// Exec returns a Func that runs real processes with the given timeout per call
// and extra environment entries appended to the inherited environment.
func Exec(timeout time.Duration, env ...string) Func {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		if len(env) > 0 {
			cmd.Env = append(cmd.Environ(), env...)
		}
		cmd.WaitDelay = time.Second
		var out, errb capped
		out.limit, errb.limit = maxOutput, 64<<10
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
		return out.Bytes(), nil
	}
}

// capped is a buffer that stops growing at limit and remembers that it did.
type capped struct {
	bytes.Buffer
	limit int
	over  bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.Len(); len(p) > room {
		c.over = true
		if room > 0 {
			c.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return c.Buffer.Write(p)
}

// Found reports whether name resolves to an executable. "" means not found.
func Found(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

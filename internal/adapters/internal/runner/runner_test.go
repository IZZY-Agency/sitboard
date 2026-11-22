package runner

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// ExecQuiet turns a warning on stderr into an error; Exec does not.
func TestExecQuietFailsOnAWarning(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	ctx := context.Background()
	warn := []string{"-c", "echo out; echo careful >&2"}
	if out, err := Exec(5*time.Second)(ctx, sh, warn...); err != nil || string(out) != "out\n" {
		t.Fatalf("Exec: %q %v", out, err)
	}
	if _, err := ExecQuiet(5*time.Second)(ctx, sh, warn...); !errors.Is(err, ErrWarned) {
		t.Fatalf("ExecQuiet with a warning: %v", err)
	}
	if out, err := ExecQuiet(5*time.Second)(ctx, sh, "-c", "echo out"); err != nil || string(out) != "out\n" {
		t.Fatalf("ExecQuiet without one: %q %v", out, err)
	}
}

// The caps hold for a real process: Go copies a process's output into the
// writer with io.Copy, which uses the writer's ReadFrom when it has one, so
// the cap must not be bypassable that way.
func TestOutputCapsHoldForARealProcess(t *testing.T) {
	ctx := context.Background()
	over := strconv.Itoa(maxOutput + 1<<20)
	out, err := Exec(20*time.Second)(ctx, "sh", "-c", "head -c "+over+" /dev/zero")
	if !errors.Is(err, ErrTooLarge) || out != nil {
		t.Fatalf("stdout over the cap: %d bytes, %v; want ErrTooLarge", len(out), err)
	}
	_, err = Clean(20*time.Second, "/", []string{"PATH=/usr/bin:/bin"})(ctx, "sh", "-c", "head -c "+over+" /dev/zero")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Clean: stdout over the cap: %v; want ErrTooLarge", err)
	}
	_, err = Exec(20*time.Second)(ctx, "sh", "-c", "head -c 1048576 /dev/zero >&2; exit 3")
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 || len(ee.Stderr) != stderrCap {
		t.Fatalf("stderr over its cap: %v, %d bytes kept, want %d", err, len(ee.Stderr), stderrCap)
	}
}

// A capped writer has no ReadFrom of its own to hand io.Copy.
func TestCappedHasOnlyWrite(t *testing.T) {
	var c any = &capped{limit: 4}
	if _, ok := c.(io.ReaderFrom); ok {
		t.Fatal("capped exposes ReadFrom")
	}
}

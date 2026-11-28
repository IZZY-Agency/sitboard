package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// A gate that refuses starts no process, and its refusal is what the call
// returns; one that admits starts it; one that neither refuses nor starts is
// ErrNotAdmitted.
func TestAGateDecidesWhetherTheProcessStarts(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "ran")
	refused := errors.New("refused")
	run := Exec(5 * time.Second)
	ctx := WithGate(context.Background(), func(func() error) error { return refused })
	if _, err := run(ctx, "touch", mark); err != refused { //nolint:errorlint // the very value
		t.Fatalf("refused: %v", err)
	}
	if _, err := os.Stat(mark); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused process ran: %v", err)
	}
	ctx = WithGate(context.Background(), func(func() error) error { return nil })
	if _, err := run(ctx, "touch", mark); !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("not started, not refused: %v", err)
	}
	if _, err := os.Stat(mark); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a process the gate did not start ran: %v", err)
	}
	ctx = WithGate(context.Background(), func(start func() error) error { return start() })
	if _, err := run(ctx, "touch", mark); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Fatalf("an admitted process did not run: %v", err)
	}
	if _, err := Exec(5*time.Second)(context.Background(), "touch", mark); err != nil {
		t.Fatalf("no gate: %v", err)
	}
}

// The gate holds only the start: start returns while the process still runs,
// so a gate holding a lock never holds it for the run. The process here
// waits for a file the gate writes after start returns; were the wait under
// the gate, the call would run out of time.
func TestAGateHoldsOnlyTheStart(t *testing.T) {
	dir := t.TempDir()
	go_ := filepath.Join(dir, "go")
	ctx := WithGate(context.Background(), func(start func() error) error {
		if err := start(); err != nil {
			return err
		}
		return os.WriteFile(go_, nil, 0o600)
	})
	out, err := Exec(20*time.Second)(ctx, "sh", "-c", `while [ ! -e "$1" ]; do sleep 0.01; done; echo done`, "sh", go_)
	if err != nil || string(out) != "done\n" {
		t.Fatalf("%q %v", out, err)
	}
}

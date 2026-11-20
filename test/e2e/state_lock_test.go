package e2e

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

var e2eLinkRe = regexp.MustCompile(`http://127\.0\.0\.1:\d+/\S*`)

func buildBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("windows is not targeted")
	}
	bin := filepath.Join(t.TempDir(), "sitboard")
	build := exec.Command("go", "build", "-o", bin, "./cmd/sitboard")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// runUntilLink starts bin and waits for its link; it is stopped at cleanup.
func runUntilLink(t *testing.T, bin string, env []string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
	got := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if e2eLinkRe.MatchString(sc.Text()) {
				got <- true
				for sc.Scan() {
				}
				return
			}
		}
		got <- false
	}()
	select {
	case ok := <-got:
		if !ok {
			t.Fatalf("%v exited without a link", args)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("%v: no link within 15s", args)
	}
	return cmd
}

// A second sitboard on the same state file is refused: on stderr, with exit
// code 1, before it prints a link. With its own state it runs.
func TestSecondBinaryOnOneStateFileIsRefused(t *testing.T) {
	bin := buildBinary(t)
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "HOME="+home, "XDG_STATE_HOME="+filepath.Join(home, ".local", "state"))
	first := runUntilLink(t, bin, env, "--no-open", "--port", "0")

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // a board that is not refused is stopped
	defer cancel()
	second := exec.CommandContext(ctx, bin, "--no-open", "--port", "0")
	second.Env = env
	second.Stdout, second.Stderr = &stdout, &stderr
	began := time.Now()
	err := second.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("second instance: %v\nstdout %s\nstderr %s", err, stdout.String(), stderr.String())
	}
	if d := time.Since(began); d > 10*time.Second {
		t.Errorf("refusal took %v", d)
	}
	msg := stderr.String()
	for _, want := range []string{"in use by another sitboard (process " + strconv.Itoa(first.Process.Pid) + " on ", "--state <path>", "--no-state"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr lacks %q: %s", want, msg)
		}
	}
	if e2eLinkRe.MatchString(stdout.String()) || strings.Contains(stdout.String(), "is taken") {
		t.Errorf("printed before refusing: %s", stdout.String())
	}

	runUntilLink(t, bin, env, "--no-open", "--port", "0", "--no-state")
	runUntilLink(t, bin, env, "--no-open", "--port", "0", "--state", filepath.Join(t.TempDir(), "own.json"))
}

package codex

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
)

// holdersFunc returns, for each of the given lock files that a process holds
// open, that process's pid. ok is false when it could not tell at all.
//
// Codex keeps an exclusive OS lock (flock) on thread-writer-locks/<id>.lock
// for as long as a process writes that thread, and removes the file when it
// stops. sitboard never takes that lock, not even shared for a moment: a
// Codex starting at that instant would fail with "already has an active
// writer". It only asks which process has the file open.
//
// Checked live on Codex 0.171.0 (#111): a terminal session's lock is taken
// when the client starts, before any prompt, and held between turns; but the
// holder is the managed app-server daemon, not the terminal process, and it
// holds the locks of every thread it has loaded (after /new, after /resume, a
// spawned child's own, and after the terminal quit). So the holder's pid says
// nothing about a terminal; see tuis.go. Linux (/proc/<pid>/fd) is built from
// the documented shape and not checked.
type holdersFunc func(ctx context.Context, paths []string) (holders map[string]int, ok bool)

// lsofHolders asks lsof about exactly these files (macOS, and Linux when
// /proc cannot be read). lsof exits 1 when some of them are not open and still
// prints the others; that output is used.
func lsofHolders(run runner.Func) holdersFunc {
	return func(ctx context.Context, paths []string) (map[string]int, bool) {
		if len(paths) == 0 {
			return map[string]int{}, true
		}
		args := append([]string{"-nP", "-w", "-Fpn", "--"}, paths...)
		out, err := run(ctx, "lsof", args...)
		if err != nil {
			var ee *runner.ExitError
			// Exit 1 with nothing on stderr is "some files are not open".
			// With an error message (bad usage, a failure) it says nothing
			// about the sessions: unknown, not "nobody holds a lock". The one
			// message that is not a failure: "status error on <asked file>",
			// a lock Codex removed between the listing and lsof.
			if !errors.As(err, &ee) || ee.Code != 1 || !onlyVanished(ee.Stderr, paths) {
				return nil, false
			}
			out = ee.Stdout
		}
		return parseLsof(out, paths), true
	}
}

// onlyVanished: stderr is empty, or made only of lsof's "status error on
// <path>: ..." lines for files that were asked about.
func onlyVanished(stderr []byte, paths []string) bool {
	for _, l := range strings.Split(string(stderr), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		rest, ok := strings.CutPrefix(l, "lsof: status error on ")
		if !ok {
			return false
		}
		found := false
		for _, p := range paths {
			if strings.HasPrefix(rest, p+":") {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// parseLsof reads lsof's -F output: a "p<pid>" line starts a process, an
// "n<name>" line names one of its files. Only the asked-for paths count.
func parseLsof(out []byte, paths []string) map[string]int {
	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	holders := map[string]int{}
	pid := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		l := sc.Text()
		if len(l) < 2 {
			continue
		}
		switch l[0] {
		case 'p':
			n, err := strconv.Atoi(l[1:])
			if err != nil || n <= 0 {
				n = 0
			}
			pid = n
		case 'n':
			if pid > 0 && want[l[1:]] {
				if _, seen := holders[l[1:]]; !seen {
					holders[l[1:]] = pid
				}
			}
		}
	}
	return holders
}

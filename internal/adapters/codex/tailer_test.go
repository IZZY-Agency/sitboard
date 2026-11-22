package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// turnLines is n finished turns, one minute apart from 11:00 on: a start and
// a finish line each, ending with a newline.
func turnLines(n int) []byte {
	var b bytes.Buffer
	for i := range n {
		at := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute / 10)
		fmt.Fprintf(&b, `{"timestamp":%q,"type":"event_msg","payload":{"type":"task_started","turn_id":"t%d"}}`+"\n", at.Format(time.RFC3339Nano), i)
		fmt.Fprintf(&b, `{"timestamp":%q,"type":"event_msg","payload":{"type":"task_complete","turn_id":"t%d"}}`+"\n", at.Add(time.Second).Format(time.RFC3339Nano), i)
	}
	return b.Bytes()
}

func tailerFile(t *testing.T, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func appendTo(t *testing.T, p string, content []byte) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func readAll(t *testing.T, tl *tailer) []time.Time {
	t.Helper()
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	return tl.d.TurnEnds
}

// A read that fails half-way through a rollout longer than the first-read cap
// is repeated from where it stopped, not from the cap jump again: the turns
// read before the failure are not counted twice.
func TestTailerRetryAfterAnErrorDoesNotCountTurnsTwice(t *testing.T) {
	p := tailerFile(t, turnLines(600)) // 1,200 lines, well over 512
	st, _ := os.Stat(p)
	ref := newTailer(p)
	ref.cap = st.Size() / 2
	want := append([]time.Time(nil), readAll(t, ref)...)
	if len(want) == 0 || len(want) >= 600 {
		t.Fatalf("setup: %d turn ends from the second half", len(want))
	}

	tl := newTailer(p)
	tl.cap = st.Size() / 2
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tl.update(cancelled, fixedNow); err == nil {
		t.Fatal("a cancelled read did not fail")
	}
	if len(tl.d.TurnEnds) == 0 || len(tl.d.TurnEnds) >= len(want) {
		t.Fatalf("setup: the failed read took %d of %d turn ends", len(tl.d.TurnEnds), len(want))
	}
	got := readAll(t, tl)
	if len(got) != len(want) || !got[0].Equal(want[0]) || !got[len(got)-1].Equal(want[len(want)-1]) {
		t.Errorf("after the retry: %d turn ends, want %d", len(got), len(want))
	}
	// And nothing more is added once it has caught up.
	if got := readAll(t, tl); len(got) != len(want) {
		t.Errorf("caught up: %d turn ends, want %d", len(got), len(want))
	}
}

// A last line without its newline waits; it counts once, when finished.
func TestTailerPartialLastLineWaits(t *testing.T) {
	lines := turnLines(1)
	cut := len(lines) - 10 // inside the second line
	p := tailerFile(t, lines[:cut])
	tl := newTailer(p)
	if got := readAll(t, tl); len(got) != 0 || tl.d.Status != model.Of(model.StatusBusy) {
		t.Fatalf("partial line: turn ends %v, status %v", got, tl.d.Status)
	}
	appendTo(t, p, lines[cut:])
	if got := readAll(t, tl); len(got) != 1 || tl.d.Status != model.Of(model.StatusFinished) {
		t.Fatalf("finished line: turn ends %v, status %v", got, tl.d.Status)
	}
}

// A line over maxLine is skipped whole, and the line after it is read.
func TestTailerSkipsALineOverTheLimit(t *testing.T) {
	long := `{"timestamp":"2026-11-20T11:00:00Z","type":"event_msg","payload":{"type":"task_complete","text":"` + strings.Repeat("x", maxLine+1<<10) + `"}}` + "\n"
	p := tailerFile(t, append([]byte(long), turnLines(1)...))
	tl := newTailer(p)
	if got := readAll(t, tl); len(got) != 1 {
		t.Fatalf("turn ends after a long line: %v", got)
	}
	// The same long line arriving in two reads, the end of it after the
	// first: still skipped, and what follows is read.
	p2 := tailerFile(t, []byte(long[:len(long)-10]))
	tl2 := newTailer(p2)
	readAll(t, tl2)
	appendTo(t, p2, append([]byte(long[len(long)-10:]), turnLines(1)...))
	if got := readAll(t, tl2); len(got) != 1 {
		t.Fatalf("turn ends after a long line arriving late: %v", got)
	}
}

// A file first opened is read from the cap before its end; the line that
// begins before that point is cut and dropped.
func TestTailerFirstReadStartsMidFile(t *testing.T) {
	lines := turnLines(4) // 8 lines
	p := tailerFile(t, lines)
	tl := newTailer(p)
	tl.cap = int64(len(lines)) / 2
	got := readAll(t, tl)
	// Only whole lines of the second half count: no more than 2 turns ends, and
	// the cut line is not parsed as a line of its own.
	if len(got) == 0 || len(got) > 2 {
		t.Errorf("turn ends from the second half: %v", got)
	}
	if tl.skipCut {
		t.Error("skipCut left set")
	}
}

// A replaced or cut-short file is read again from its start.
func TestTailerReplacedOrTruncatedFile(t *testing.T) {
	p := tailerFile(t, turnLines(3))
	tl := newTailer(p)
	if got := readAll(t, tl); len(got) != 3 {
		t.Fatalf("first read: %v", got)
	}
	// Truncated, then rewritten shorter: the old turns are gone.
	if err := os.WriteFile(p, turnLines(1), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, tl); len(got) != 1 {
		t.Errorf("after truncation: %v", got)
	}
	// Replaced by another file (a new inode), as long as the old one.
	other := filepath.Join(filepath.Dir(p), "other.jsonl")
	if err := os.WriteFile(other, turnLines(2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, p); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, tl); len(got) != 2 {
		t.Errorf("after replacement: %v", got)
	}
}

// commandLine is a finished command of a Codex rollout, as one line.
func commandLine(t *testing.T, script, output string) []byte {
	t.Helper()
	return commandLineExit(t, script, output, 0)
}

// failedCommandLine is a command that exited with 1, as one line.
func failedCommandLine(t *testing.T, script, output string) []byte {
	t.Helper()
	return commandLineExit(t, script, output, 1)
}

func commandLineExit(t *testing.T, script, output string, code int) []byte {
	t.Helper()
	status := "completed"
	if code != 0 {
		status = "failed"
	}
	payload := map[string]any{"type": "item_completed", "item": map[string]any{
		"type": "CommandExecution", "command": []string{"/bin/zsh", "-lc", script},
		"status": status, "exit_code": code, "aggregated_output": output,
	}}
	b, err := json.Marshal(map[string]any{"timestamp": "2026-11-20T11:01:00.000Z", "type": "event_msg", "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// A pull request is the session's when its own gh pr create announced it;
// the "already exists" answer does not count (the create made nothing, #116),
// and neither does another gh command's output in the same call.
func TestCreatedPRsAreOnlyTheCreatesOwn(t *testing.T) {
	url := func(n string) string { return "https://github.com/example-org/example-repo/pull/" + n }
	var b bytes.Buffer
	b.Write(commandLine(t, "gh pr create --fill", "\nCreating pull request for a into main in example-org/example-repo\n\n"+url("401")+"\n"))
	b.Write(commandLine(t, "gh pr create --fill", "a pull request for branch \"a\" into branch \"main\" already exists:\n"+url("402")+"\n"))
	b.Write(commandLine(t, "gh pr create --fill && gh pr view 404 --json url -q .url", "Creating pull request for b into main\n\n"+url("403")+"\n"+url("404")+"\n"))
	// No terminal: gh prints only the addresses. The create's is the first when
	// it runs first, and unknown after another gh command.
	b.Write(commandLine(t, "gh pr create --fill; gh pr view 406 --json url -q .url", url("405")+"\n"+url("406")+"\n"))
	b.Write(commandLine(t, "gh pr view 407 --json url -q .url; gh pr create --fill", url("407")+"\n"+url("408")+"\n"))
	b.Write(commandLine(t, "gh pr view 407 --json url -q .url", url("407")+"\n"))
	tl := newTailer(tailerFile(t, b.Bytes()))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, pr := range tl.d.CreatedPRs {
		got = append(got, pr.Number)
	}
	if !slices.Equal(got, []int{401, 403, 405}) {
		t.Errorf("created = %v, want [401 403 405]", got)
	}
}

// The same rule down to the Codex session's CreatedPRs, for the shapes a
// refusal takes in one call's output (#116).
func TestCreatedPRsLeaveOutThePRThatAlreadyExists(t *testing.T) {
	url := func(n string) string { return "https://github.com/example-org/example-repo/pull/" + n }
	exists := func(n string) string {
		return "a pull request for branch \"a\" into branch \"main\" already exists:\n" + url(n) + "\n"
	}
	for _, c := range []struct {
		name, cmd, out string
		want           []int
	}{
		{"alone", "gh pr create --fill", exists("501"), nil},
		{"retried, no terminal", "gh pr create --fill; gh pr create --fill", exists("501") + url("502") + "\n", []int{502}},
		{"announced create, then a refused one", "gh pr create -f; gh pr create -f -B dev", "Creating pull request for a into main\n\n" + url("503") + "\n" + exists("501"), []int{503}},
		// The refusal exits nonzero, so the view after || is the one that runs.
		{"refused, then the view that follows ||", "gh pr create --fill || gh pr view --json url -q .url", exists("501") + url("501") + "\n", nil},
		{"refused, then an announced create", "gh pr create -f; gh pr view 504 --json url -q .url; gh pr create -f -B dev", exists("501") + url("504") + "\nCreating pull request for b into dev\n\n" + url("505") + "\n", []int{505}},
		{"a long list, then an announced create", "gh pr list --json url -q '.[].url'; gh pr create -f", manyURLs(40) + "\nCreating pull request for a into main\n\n" + url("506") + "\n", []int{506}},
		{"no terminal, a warning, a bare address", "gh pr create --fill", "Warning: 2 uncommitted changes\n" + url("507") + "\n", []int{507}},
		{"no terminal, a warning, a bare address, then a view", "gh pr create --fill && gh pr view 508 --json url -q .url", "Warning: 2 uncommitted changes\n" + url("507") + "\n" + url("508") + "\n", []int{507}},
		{"unrelated text ending in already exists:, then a view", "gh pr create --fill && gh pr view 508 --json body -q .body", url("507") + "\nThis behavior already exists:\nmore\n", []int{507}},
	} {
		tl := newTailer(tailerFile(t, commandLine(t, c.cmd, c.out)))
		if _, err := tl.update(context.Background(), fixedNow); err != nil {
			t.Fatal(err)
		}
		var got []int
		for _, pr := range tl.d.CreatedPRs {
			got = append(got, pr.Number)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: created = %v, want %v", c.name, got, c.want)
		}
	}
}

// Codex records a command as an argument list, which the adapter quotes back
// into one line: a shell and its script, so everything is in one script and the
// order of the gh commands is known. A command recorded as a plain string can
// hold a script and a command outside it; the order is then not known, and a
// refused create's address, printed again by the view after ||, is not
// credited either (#116).
func TestCreatedPRsOfACommandStringLeaveOutTheRefused(t *testing.T) {
	url := func(n string) string { return "https://github.com/example-org/example-repo/pull/" + n }
	refused := "a pull request for branch \"a\" into branch \"main\" already exists:\n" + url("501") + "\n"
	for _, c := range []struct {
		name, cmd, out string
		want           []int
	}{
		{"refused in a script, the view outside", "bash -c 'gh pr create --fill' || gh pr view --json url -q .url", refused + url("501") + "\n", nil},
		{"refused, the view in a script", "gh pr create --fill || bash -c 'gh pr view --json url -q .url'", refused + url("501") + "\n", nil},
	} {
		payload := map[string]any{"type": "item_completed", "item": map[string]any{
			"type": "CommandExecution", "command": c.cmd, "exit_code": 0, "aggregated_output": c.out,
		}}
		line, err := json.Marshal(map[string]any{"timestamp": "2026-11-20T11:01:00.000Z", "type": "event_msg", "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		tl := newTailer(tailerFile(t, append(line, '\n')))
		if _, err := tl.update(context.Background(), fixedNow); err != nil {
			t.Fatal(err)
		}
		var got []int
		for _, pr := range tl.d.CreatedPRs {
			got = append(got, pr.Number)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: created = %v, want %v", c.name, got, c.want)
		}
	}
}

// manyURLs is n pull request addresses, one per line, of other pull requests.
func manyURLs(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "https://github.com/example-org/example-repo/pull/%d\n", 1000+i)
	}
	return b.String()
}

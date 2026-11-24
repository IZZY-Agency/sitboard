package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// metaAt is threadMeta with the line's own time, as Codex writes it.
func metaAt(id, ts string) string {
	return `{"timestamp":"` + ts + `",` + threadMeta(id, "codex-tui", `"vscode"`)[1:]
}

func doneAt(ts string) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"task_complete"}}`
}

// ghAt is a finished `gh pr create` that printed pull request n's address.
func ghAt(ts string, n int) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","command":["/bin/zsh","-lc","gh pr create --fill"],"aggregated_output":"https://github.com/example-org/example-repo/pull/%d\n"}}}`, ts, n)
}

// A fork repeats its parent's session_meta and history, which is older than
// the fork: the turn and the pull request in it are the parent's, not the
// fork's (review of #152).
func TestCatchupOfAForkLeavesOutItsCopiedTurnsAndPullRequests(t *testing.T) {
	e := newEnv(t)
	e.addLines(idFork, fixedNow.Add(-time.Hour),
		metaAt(idFork, "2026-11-20T11:00:00.000Z"),
		metaAt(idWorker, "2026-11-20T11:00:00.001Z"),
		ghAt("2026-11-19T09:04:00.000Z", 901),
		doneAt("2026-11-19T09:05:20.000Z"),
		ghAt("2026-11-20T11:30:00.000Z", 902),
		recLine(idFork, "2026-11-20T11:40:00.000Z", 100, 0, 10),
		doneAt("2026-11-20T11:41:00.000Z"))
	e.catchup()
	f, ok := e.last(idFork)
	if !ok {
		t.Fatal("no fact for the fork")
	}
	if !slices.Equal(f.TurnEnds, []time.Time{at(11, 41)}) {
		t.Errorf("turns %v, want only the fork's own", f.TurnEnds)
	}
	if len(f.CreatedPRs) != 1 || f.CreatedPRs[0].Number != 902 {
		t.Errorf("pull requests %v, want only the fork's own (902)", f.CreatedPRs)
	}
}

// A resumed session's live read starts near the end of a large rollout; what
// the start-up read found earlier in it stays, each turn and pull request once.
func TestCatchupResumeKeepsCaughtTurnsAndPullRequests(t *testing.T) {
	e := newEnv(t)
	e.addLines(idWorker, fixedNow.Add(-30*time.Minute),
		metaAt(idWorker, "2026-11-20T11:00:00.000Z"),
		ghAt("2026-11-20T11:01:00.000Z", 301),
		doneAt("2026-11-20T11:05:00.000Z"),
		recLine(idWorker, "2026-11-20T11:06:00.000Z", 100, 0, 10))
	e.catchup()
	caught, ok := e.last(idWorker)
	if !ok || !slices.Equal(caught.TurnEnds, []time.Time{at(11, 5)}) || len(caught.CreatedPRs) != 1 {
		t.Fatalf("caught up: %v %+v", ok, caught)
	}
	// Resumed: one more turn and the same pull request again are appended,
	// and the live read starts past what the start-up read found.
	liveTail(t, e.paths[idWorker], fixedNow.Add(-time.Minute), ghAt("2026-11-20T11:40:00.000Z", 301), doneAt("2026-11-20T11:50:00.000Z"), recLine(idWorker, "2026-11-20T11:51:00.000Z", 100, 0, 10))
	e.lock(idWorker, 4242)
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok || f.Backfill || !f.Live.V {
		t.Fatalf("not live: %v %+v", ok, f)
	}
	if !slices.Equal(f.TurnEnds, []time.Time{at(11, 5), at(11, 50)}) {
		t.Errorf("turns %v, want the caught-up one and the live one", f.TurnEnds)
	}
	if len(f.CreatedPRs) != 1 || f.CreatedPRs[0].Number != 301 {
		t.Errorf("pull requests %v, want the caught-up 301 once", f.CreatedPRs)
	}
}

// A session whose rollout holds a pull request and neither a finished turn nor
// a token record (the terminal ended right after the command) still reaches
// the forge lookup, and stays with the next scan.
func TestCatchupPublishesASessionWithOnlyAPullRequest(t *testing.T) {
	e := newEnv(t)
	e.addLines(idWorker, fixedNow.Add(-time.Hour), metaAt(idWorker, "2026-11-20T11:00:00.000Z"), ghAt("2026-11-20T11:30:00.000Z", 301))
	e.catchup()
	f, ok := e.last(idWorker)
	if !ok || len(f.CreatedPRs) != 1 || !f.Backfill || !f.Ended {
		t.Fatalf("PR-only session: %v %+v", ok, f)
	}
	e.rescan()
	if f, ok := e.last(idWorker); !ok || len(f.CreatedPRs) != 1 {
		t.Errorf("after a scan: %v %+v", ok, f)
	}
}

// A `codex exec` run is out of Catch-up, so its pull requests are not asked of
// the forge either; its tokens still count.
func TestCatchupExecRunsPullRequestsAreNotPublished(t *testing.T) {
	exec := strings.Replace(metaAt(idExec, "2026-11-20T11:00:00.000Z"), `"codex-tui"`, `"codex_exec"`, 1)
	exec = strings.Replace(exec, `"vscode"`, `"exec"`, 1)
	only := newEnv(t)
	only.addLines(idExec, fixedNow.Add(-time.Hour), exec, ghAt("2026-11-20T11:30:00.000Z", 301))
	only.catchup()
	if f, ok := only.last(idExec); ok {
		t.Errorf("an exec run with only a pull request has a fact: %+v", f)
	}
	e := newEnv(t)
	e.addLines(idExec, fixedNow.Add(-time.Hour), exec, ghAt("2026-11-20T11:30:00.000Z", 301), recLine(idExec, "2026-11-20T11:40:00.000Z", 100, 0, 10))
	e.catchup()
	f, ok := e.last(idExec)
	if !ok || !f.Headless || len(f.CreatedPRs) != 0 || f.Spend.Tokens.Output != 10 {
		t.Errorf("exec run: %v %+v", ok, f)
	}
}

// A thread resumed after the first displayed midnight's rolling cutoff, in an
// older folder, is found: the window is 168 hours, as for the regular scan.
func TestCatchupFindsAResumedRolloutBetweenTheTwoCutoffs(t *testing.T) {
	since, mid := fixedNow.Add(-turnWindow), firstDisplayedMidnight(fixedNow)
	if !mid.After(since.Add(time.Minute)) {
		t.Skip("the local midnight is not after the rolling cutoff at this time zone")
	}
	e := newEnv(t)
	e.addAt(14, "spend.jsonl", idWorker, since.Add(mid.Sub(since)/2))
	e.catchup()
	if f, ok := e.last(idWorker); !ok || !f.Backfill {
		t.Fatalf("resumed thread between the cutoffs: %v %+v", ok, f)
	}
}

// A title the index holds after thousands of other threads' lines is found for
// a root of an older folder.
func TestCatchupTitleBeyondTheSkippedIDCap(t *testing.T) {
	var b strings.Builder
	for i := range maxSkipped + 500 {
		fmt.Fprintf(&b, `{"id":"%08x-0000-4000-8000-000000000000","thread_name":"other","updated_at":"2026-11-20T11:06:00.000000Z"}`+"\n", i)
	}
	fmt.Fprintf(&b, `{"id":%q,"thread_name":"Found late","updated_at":"2026-11-20T11:06:00.000000Z"}`+"\n", idWorker)
	e := newEnv(t)
	e.addAt(14, "spend.jsonl", idWorker, fixedNow.Add(-26*time.Hour))
	if err := os.WriteFile(filepath.Join(e.root, "session_index.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	e.catchup()
	if f, ok := e.last(idWorker); !ok || f.Name != "Found late" {
		t.Errorf("name %q (%v)", f.Name, ok)
	}
}

// The turn cap keeps the newest turns, and expired turns never take room.
func TestTailerTurnCapKeepsTheNewestAndSkipsExpired(t *testing.T) {
	var b strings.Builder
	for i := range maxTurns + 10 { // expired: older than turnWindow
		b.WriteString(doneAt(fixedNow.Add(-10*24*time.Hour+time.Duration(i)*time.Second).Format(time.RFC3339Nano)) + "\n")
	}
	b.WriteString(doneAt("2026-11-20T11:00:00.000Z") + "\n")
	tl := newTailer(tailerFile(t, []byte(b.String())))
	if got := readAll(t, tl); !slices.Equal(got, []time.Time{at(11, 0)}) {
		t.Errorf("after an expired prefix: %d turns, want the one recent", len(got))
	}
	b.Reset()
	for i := range maxTurns + 1 { // in the window: the oldest goes
		b.WriteString(doneAt(fixedNow.Add(-time.Duration(maxTurns+1-i)*time.Second).Format(time.RFC3339Nano)) + "\n")
	}
	tl = newTailer(tailerFile(t, []byte(b.String())))
	got := readAll(t, tl)
	if len(got) != maxTurns || !got[len(got)-1].Equal(fixedNow.Add(-time.Second)) || !got[0].Equal(fixedNow.Add(-maxTurns*time.Second)) {
		t.Errorf("%d turns, first %v last %v", len(got), got[0], got[len(got)-1])
	}
}

// The start-up read takes what the fact carries and none of the live state:
// the same turns, pull requests, folder, model and tokens as a live read of the
// same rollout, and no status, approval wait or agterm spawn.
func TestStartupReadMatchesALiveReadWithoutLiveState(t *testing.T) {
	content := fixture(t, "worker.jsonl")
	read := func(startup bool) *tailer {
		tl := newTailer(tailerFile(t, content))
		tl.id, tl.startup = idWorker, startup
		if _, err := tl.update(context.Background(), fixedNow); err != nil {
			t.Fatal(err)
		}
		return tl
	}
	live, up := read(false), read(true)
	if len(live.d.SpawnedNames) == 0 {
		t.Fatal("fixture changed: the live read finds no agterm spawn")
	}
	if len(up.d.SpawnedNames) != 0 || len(up.d.SpawnedIDs) != 0 || up.d.Status.OK {
		t.Errorf("start-up read kept live state: %+v", up.d)
	}
	same := slices.Equal(live.d.TurnEnds, up.d.TurnEnds) && slices.Equal(live.d.CreatedPRs, up.d.CreatedPRs) &&
		slices.Equal(live.d.PushedPRs, up.d.PushedPRs) && live.d.Folder == up.d.Folder && live.d.Model == up.d.Model &&
		live.d.Effort == up.d.Effort && live.d.Total == up.d.Total && len(live.d.Spans) == len(up.d.Spans)
	if !same {
		t.Errorf("live %+v\nstart-up %+v", live.d, up.d)
	}
}

// The catch-up reads a root in the start-up mode: no spawn names, no status.
func TestCatchupReadsARootInTheStartupMode(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-time.Hour))
	res := e.catchup()
	found := false
	for _, rd := range res.reads {
		if rd.job.id != idWorker {
			continue
		}
		found = true
		if len(rd.d.SpawnedNames) != 0 || len(rd.d.SpawnedIDs) != 0 || rd.d.Status.OK || len(rd.d.TurnEnds) == 0 {
			t.Errorf("root read %+v", rd.d)
		}
	}
	if !found {
		t.Fatal("the root was not read")
	}
}

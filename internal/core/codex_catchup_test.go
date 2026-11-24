package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/codex"
	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// The real Codex watcher's start-up facts into the real board (#114, #135):
// an ended terminal thread's rollout, older than a quarter hour and with no
// lock folder to tell its liveness, is read once; its use is in the day and
// the project, its finished turn is in Catch-up under its title, as an ended
// Claude Code session's is, the days and the panel are pending until the
// watcher says it is done, and there is no card.
func TestCodexCatchupReachesTheBoardAsDaysAndCatchupWithNoCard(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "codex", "0.171.0", "spend.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// What the records of the thread itself add up to, from the fixture's text.
	var want contract.Tokens
	for _, line := range strings.Split(string(raw), "\n") {
		var rec struct {
			Type    string `json:"type"`
			Payload struct {
				ThreadID string `json:"thread_id"`
				Usage    struct {
					Input       int64 `json:"input_tokens"`
					CachedInput int64 `json:"cached_input_tokens"`
					Output      int64 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil || rec.Type != "token_usage_record" || rec.Payload.ThreadID != id {
			continue
		}
		u := rec.Payload.Usage
		want.Input += u.Input - u.CachedInput
		want.CacheRead += u.CachedInput
		want.Output += u.Output
	}
	if want.Output == 0 {
		t.Fatal("the fixture has no record of its own thread")
	}

	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	e.clock.Set(now)
	home := t.TempDir()
	d := now.Local()
	dir := filepath.Join(home, "sessions", d.Format("2006"), d.Format("01"), d.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-2026-11-20T11-00-00-"+id+".jsonl")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A `codex exec` run that ended too, with a finished turn of its own and
	// no token record: Catch-up leaves it out, as it does a claude -p run.
	execRaw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "codex", "0.171.0", "exec.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	pe := filepath.Join(dir, "rollout-2026-11-20T11-10-00-33333333-3333-4333-8333-333333333333.jsonl")
	if err := os.WriteFile(pe, execRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(pe, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	index := `{"id":"` + id + `","thread_name":"Sample title","updated_at":"2026-11-20T11:22:01Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}

	e.board.SetSources([]source.Status{{Kind: source.KindCodex, State: source.StateOK}})
	if !e.board.Snapshot().Spend.Pending {
		t.Fatal("not pending before the watcher ran")
	}
	w := codex.New(codex.Options{Home: t.TempDir(), CodexHome: home, Now: e.clock.Now})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, ran := make(chan struct{}), make(chan error, 1)
	pendingBeforeDone, panelPendingBeforeDone := false, false
	go func() {
		ran <- w.Run(ctx, func(f model.Fact) {
			if _, ok := f.(model.BackfillDone); ok {
				pendingBeforeDone = e.board.Snapshot().Spend.Pending
				c, err := e.board.Catchup(context.Background())
				panelPendingBeforeDone = err == nil && c.Pending
			}
			e.board.applySync(f)
			if _, ok := f.(model.BackfillDone); ok {
				close(done)
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the watcher never said it was done")
	}
	cancel()
	if err := <-ran; err != nil {
		t.Fatalf("Run = %v", err)
	}

	snap := e.board.Snapshot()
	if !pendingBeforeDone || !panelPendingBeforeDone {
		t.Errorf("days pending %v, panel pending %v until the watcher said it was done", pendingBeforeDone, panelPendingBeforeDone)
	}
	if snap.Spend.Pending {
		t.Error("still pending after the watcher said it was done")
	}
	if len(snap.Cards) != 0 {
		t.Errorf("cards %+v; an ended thread is no card", snap.Cards)
	}
	c := catchup(t, e.board)
	if c.Pending {
		t.Error("the panel still pending after the watcher said it was done")
	}
	if len(c.Items) != 1 {
		t.Fatalf("catch-up items %+v, want the terminal thread's one finished turn and none of the exec run", c.Items)
	}
	it := c.Items[0]
	if it.Kind != contract.CatchupTurnFinished || !it.At.Equal(time.Date(2026, 11, 20, 11, 22, 0, 0, time.UTC)) ||
		it.CardID != "" || it.ProjectName != "alpha" || it.AgentName != "Sample title" {
		t.Errorf("catch-up item %+v, want the turn finished at 11:22 by Sample title in alpha, with no card", it)
	}
	var codexTotal contract.Tokens
	var project contract.Tokens
	for _, day := range snap.Spend.Days {
		codexTotal.Input += day.Codex.Input
		codexTotal.CacheRead += day.Codex.CacheRead
		codexTotal.Output += day.Codex.Output
		for _, pr := range day.Projects {
			if pr.Name == "alpha" {
				project.Input += pr.Codex.Input
				project.CacheRead += pr.Codex.CacheRead
				project.Output += pr.Codex.Output
			}
		}
	}
	if codexTotal != want {
		t.Errorf("Codex day totals %+v, want the thread's records' %+v", codexTotal, want)
	}
	if project != want {
		t.Errorf("project alpha's Codex totals %+v, want %+v", project, want)
	}
}

// Several ended threads through the real watcher into the real board (#114):
// the watcher publishes them as a batch before it says it is done, and the
// board rebuilds once for the batch, not once per thread.
func TestCodexCatchupOfSeveralThreadsArrivesAsOneBatchOneRebuild(t *testing.T) {
	ids := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
	}
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	e.clock.Set(now)
	home := t.TempDir()
	d := now.Local()
	dir := filepath.Join(home, "sessions", d.Format("2006"), d.Format("01"), d.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var want int64
	for i, id := range ids {
		out := int64(10 * (i + 1))
		want += out
		meta := `{"type":"session_meta","payload":{"id":"` + id + `","timestamp":"2026-11-20T11:00:00.000Z","cwd":"/home/dev/projects/alpha","originator":"codex-tui","cli_version":"0.171.0","source":"cli"}}`
		u := `{"input_tokens":0,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":` + strconv.FormatInt(out, 10) + `,"reasoning_output_tokens":0,"total_tokens":` + strconv.FormatInt(out, 10) + `}`
		rec := `{"timestamp":"2026-11-20T11:` + []string{"05", "20", "35"}[i] + `:00.000Z","type":"token_usage_record","payload":{"thread_id":"` + id + `","usage":` + u + `,"thread_token_usage":` + u + `}}`
		p := filepath.Join(dir, "rollout-2026-11-20T11-00-00-"+id+".jsonl")
		if err := os.WriteFile(p, []byte(meta+"\n"+rec+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	e.board.SetSources([]source.Status{{Kind: source.KindCodex, State: source.StateOK}})
	w := codex.New(codex.Options{Home: t.TempDir(), CodexHome: home, Now: e.clock.Now})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, ran := make(chan struct{}), make(chan error, 1)
	var batches, looseBackfill, batchedFacts int
	var seqSteps []uint64
	batchedBeforeDone := true
	go func() {
		ran <- w.Run(ctx, func(f model.Fact) {
			switch f := f.(type) {
			case model.Batch:
				before := e.board.Snapshot().Seq
				e.board.applySync(f)
				seqSteps = append(seqSteps, e.board.Snapshot().Seq-before)
				batches++
				batchedFacts += len(f.Facts)
				return
			case model.SessionFact:
				if f.Backfill {
					looseBackfill++
				}
			case model.BackfillDone:
				batchedBeforeDone = batches > 0
			}
			e.board.applySync(f)
			if _, ok := f.(model.BackfillDone); ok {
				close(done)
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the watcher never said it was done")
	}
	cancel()
	if err := <-ran; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if looseBackfill != 0 {
		t.Errorf("%d caught-up threads sent one by one", looseBackfill)
	}
	if !batchedBeforeDone || batchedFacts != len(ids) {
		t.Errorf("batches %d with %d facts before the done, want the %d threads", batches, batchedFacts, len(ids))
	}
	for _, s := range seqSteps {
		if s != 1 {
			t.Errorf("a batch changed the view %d times, want once: %v", s, seqSteps)
		}
	}
	var got int64
	for _, day := range e.board.Snapshot().Spend.Days {
		got += day.Codex.Output
	}
	if got != want {
		t.Errorf("Codex output on the days %d, want %d", got, want)
	}
}

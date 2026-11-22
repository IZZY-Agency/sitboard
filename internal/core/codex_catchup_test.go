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

// The real Codex watcher's start-up facts into the real board (#114): an
// ended terminal thread's rollout, older than a quarter hour and with no lock
// folder to tell its liveness, is read once; its use is in the day and the
// project, the days are pending until the watcher says it is done, and there
// is no card and no catch-up item.
func TestCodexCatchupReachesTheBoardAsDaysWithNoCard(t *testing.T) {
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

	e.board.SetSources([]source.Status{{Kind: source.KindCodex, State: source.StateOK}})
	if !e.board.Snapshot().Spend.Pending {
		t.Fatal("not pending before the watcher ran")
	}
	w := codex.New(codex.Options{Home: t.TempDir(), CodexHome: home, Now: e.clock.Now})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, ran := make(chan struct{}), make(chan error, 1)
	pendingBeforeDone := false
	go func() {
		ran <- w.Run(ctx, func(f model.Fact) {
			if _, ok := f.(model.BackfillDone); ok {
				pendingBeforeDone = e.board.Snapshot().Spend.Pending
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
	if !pendingBeforeDone {
		t.Error("not pending until the watcher said it was done")
	}
	if snap.Spend.Pending {
		t.Error("still pending after the watcher said it was done")
	}
	if len(snap.Cards) != 0 {
		t.Errorf("cards %+v; an ended thread is no card", snap.Cards)
	}
	if c := catchup(t, e.board); len(c.Items) != 0 {
		t.Errorf("catch-up items %+v", c.Items)
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

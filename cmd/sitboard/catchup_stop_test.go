package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/github"
	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// feedWatcher is a Claude Code that is there and sends one fact when asked.
type feedWatcher struct{ send chan model.Fact }

func (feedWatcher) Kind() source.Kind { return source.KindClaude }
func (feedWatcher) Probe(context.Context) source.Status {
	return source.Status{Kind: source.KindClaude, State: source.StateOK}
}
func (w feedWatcher) Run(ctx context.Context, emit func(model.Fact)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case f := <-w.send:
			emit(f)
		}
	}
}

// Through the real server and state file: "Caught up" on the list the page
// got is saved as acknowledged at once (no later save), a list of before the
// restart is refused with 409 and marks nothing, and the file is written as
// version 3, which older builds refuse.
func TestCaughtUpIsSavedAndAStaleListRefused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	feed := feedWatcher{send: make(chan model.Fact, 1)}
	cfg := cfgWith(0, true)
	cfg.statePath = file
	start := func() *running {
		return startIn(t, cfg, t.TempDir(), func(env *runEnv) {
			env.sources = func(config, string) sources {
				return sources{watchers: []source.Watcher{feed}, repos: quietRepos{}, prs: github.New(github.Options{Off: true})}
			}
		})
	}
	type list struct {
		List  string            `json:"list"`
		Items []json.RawMessage `json:"items"`
	}
	get := func(t *testing.T, r *running) list {
		t.Helper()
		resp, err := client(t, r).Get("http://127.0.0.1:" + r.port + contract.PathCatchup)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var l list
		if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	mark := func(t *testing.T, r *running, id string) int {
		t.Helper()
		base := "http://127.0.0.1:" + r.port
		req, _ := http.NewRequest(http.MethodPost, base+contract.PathCatchupRead, strings.NewReader(`{"list":"`+id+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base)
		resp, err := client(t, r).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	r := start()
	// A session that ended three hours ago.
	at := time.Now().Add(-3 * time.Hour)
	feed.send <- model.SessionFact{Key: model.SessionKey{Tool: model.ToolClaude, ID: "44444444-4444-4444-8444-444444444444"},
		Live: model.Of(false), Ended: true, EndedAt: at, Backfill: true, TurnEnds: []time.Time{at}}
	var shown list
	for range 100 {
		if shown = get(t, r); len(shown.Items) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(shown.Items) != 1 || shown.List == "" {
		t.Fatalf("list %q with %d items", shown.List, len(shown.Items))
	}
	if code := mark(t, r, shown.List); code != http.StatusNoContent {
		t.Fatalf("caught up: %d", code)
	}
	if n := len(get(t, r).Items); n != 0 {
		t.Fatalf("%d items after Caught up", n)
	}
	r.stop(t)

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int                        `json:"version"`
		Read    map[string]json.RawMessage `json:"catchupRead"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 3 || len(doc.Read) != 1 {
		t.Fatalf("file version %d with %d acknowledged", doc.Version, len(doc.Read))
	}

	second := start()
	defer second.stop(t)
	if code := mark(t, second, shown.List); code != http.StatusConflict {
		t.Fatalf("a list of the last run: %d, want 409", code)
	}
	// The same session read again after the restart: still read.
	feed.send <- model.SessionFact{Key: model.SessionKey{Tool: model.ToolClaude, ID: "44444444-4444-4444-8444-444444444444"},
		Live: model.Of(false), Ended: true, EndedAt: at, Backfill: true, TurnEnds: []time.Time{at}}
	for range 100 {
		if len(feed.send) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // applied
	if n := len(get(t, second).Items); n != 0 {
		t.Fatalf("%d items after the restart", n)
	}
}

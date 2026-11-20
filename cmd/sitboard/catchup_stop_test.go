package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/github"
	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/state"
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

// lateStore is slow to give the catch-up marker once the stop has begun, so
// a close that does not wait for the board wins the race.
type lateStore struct {
	state.Store
	slow atomic.Bool
}

func (s *lateStore) CatchupMarker() time.Time {
	if s.slow.Load() {
		time.Sleep(400 * time.Millisecond)
	}
	return s.Store.CatchupMarker()
}

// Through run's own shutdown: what catch-up learned after "Caught up" and
// since the last tick is saved at the stop, because the state file closes
// only after the board stopped.
func TestStopSavesWhatCatchupLearned(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	feed := feedWatcher{send: make(chan model.Fact, 1)}
	store := &lateStore{}
	cfg := cfgWith(0, true)
	cfg.statePath = file
	r := startIn(t, cfg, t.TempDir(), func(env *runEnv) {
		env.sources = func(config, string) sources {
			return sources{watchers: []source.Watcher{feed}, repos: quietRepos{}, prs: github.New(github.Options{Off: true})}
		}
		env.openStore = func(f string) (state.Store, error) {
			st, err := state.Open(f)
			store.Store = st
			return store, err
		}
	})
	c := client(t, r)
	base := "http://127.0.0.1:" + r.port
	resp, err := c.Get(base + contract.PathCatchup)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		AsOf string `json:"asOf"`
	}
	err = json.NewDecoder(resp.Body).Decode(&list)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, base+contract.PathCatchupRead, strings.NewReader(`{"upTo":"`+list.AsOf+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", base)
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("caught up: %d", resp.StatusCode)
	}
	// A session that ended three hours ago, read after "Caught up".
	at := time.Now().Add(-3 * time.Hour)
	s := model.SessionFact{Key: model.SessionKey{Tool: model.ToolClaude, ID: "44444444-4444-4444-8444-444444444444"},
		Live: model.Of(false), Ended: true, EndedAt: at, Backfill: true, TurnEnds: []time.Time{at}}
	feed.send <- s
	time.Sleep(100 * time.Millisecond) // applied
	store.slow.Store(true)
	r.stop(t)

	st, err := state.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(st.CatchupUnread()); n != 1 {
		t.Fatalf("%d saved as unread at the stop", n)
	}
}

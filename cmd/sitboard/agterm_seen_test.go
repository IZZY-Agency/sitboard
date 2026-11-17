package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/agterm"
	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/core"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// fakeAgtermctl writes a stand-in agtermctl that answers from fixed JSON: one
// window whose tree holds the given sessions, and an event stream that stays
// open and quiet. It runs nothing else.
func fakeAgtermctl(t *testing.T, sessions string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"version.json": `{"ok":true,"result":{"app":{"commit":"00000000","version":"0.41.1"},"installed":{"commit":"00000000","version":"0.41.1"}}}`,
		"windows.json": `{"ok":true,"result":{"windows":[{"id":"AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA","open":true,"active":true}]}}`,
		"tree.json":    `{"ok":true,"result":{"tree":{"workspaces":[{"id":"CCCCCCCC-CCCC-4CCC-8CCC-CCCCCCCCCCCC","name":"alpha","sessions":[` + sessions + `]}]}}}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := fmt.Sprintf(`#!/bin/sh
d=%q
case "$1" in
version) cat "$d/version.json" ;;
window) cat "$d/windows.json" ;;
tree) cat "$d/tree.json" ;;
events) exec sleep 30 ;;
*) exit 1 ;;
esac
`, dir)
	bin := filepath.Join(dir, "agtermctl")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

// C4 with the real agterm adapter: its first read reports each terminal's
// persisted last-selected time, and on a first run with no state file a
// session selected at or after its last turn ended is seen.
func TestAgtermPersistedSelectionMarksSeenOnFirstRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows is not targeted")
	}
	const (
		selectedAfter  = "11111111-1111-4111-8111-111111111111"
		selectedBefore = "22222222-2222-4222-8222-222222222222"
	)
	turnEnd := time.Now().Add(-time.Hour).Truncate(time.Second)
	tree := fmt.Sprintf(
		`{"id":%q,"name":"worker-a","title":"worker-a","cwd":"/home/dev/projects/alpha","foreground":["claude"],"lastSelectedAt":%d},`+
			`{"id":%q,"name":"worker-b","title":"worker-b","cwd":"/home/dev/projects/alpha","foreground":["claude"],"lastSelectedAt":%d}`,
		selectedAfter, turnEnd.Add(time.Minute).Unix(), selectedBefore, turnEnd.Add(-time.Minute).Unix())
	adapter := agterm.New(agterm.Options{Bin: fakeAgtermctl(t, tree)})

	st, err := state.Open(filepath.Join(t.TempDir(), "state.json")) // no file yet: a first run
	if err != nil {
		t.Fatal(err)
	}
	board := core.New(core.Options{State: st, StaleAfter: 20 * time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, board.Apply) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	// The Claude Code side, as its adapter reports it (terminal ids in upper case).
	for i, id := range []string{selectedAfter, selectedBefore} {
		board.Apply(model.SessionFact{
			Key:         model.SessionKey{Tool: model.ToolClaude, ID: fmt.Sprintf("s%d", i)},
			Name:        fmt.Sprintf("worker-%c", 'a'+i),
			Status:      model.Of(model.StatusFinished),
			StatusSince: turnEnd,
			TurnEnds:    []time.Time{turnEnd},
			Live:        model.Of(true),
			TerminalID:  strings.ToUpper(id),
		})
	}

	want := map[string]bool{"claude:s0": true, "claude:s1": false}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := map[string]contract.Card{}
		for _, c := range board.Snapshot().Cards {
			got[c.ID] = c
		}
		ok := len(got) == 2
		for id, seen := range want {
			c := got[id]
			if c.Agent.TerminalID == "" || c.Seen != seen {
				ok = false
			}
		}
		if ok {
			if got["claude:s0"].Column != contract.ColumnIdle || got["claude:s1"].Column != contract.ColumnNeedsYou {
				t.Fatalf("columns %q and %q, want idle and needs_you", got["claude:s0"].Column, got["claude:s1"].Column)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cards %+v, want s0 seen and s1 unseen, both joined to their terminal", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

package state

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The catch-up marker and unread items sitboard 0.4.0 kept are dropped at the
// first open, whatever they hold (a marker in the window, out of it, ahead of
// the clock, unreadable; unread items with or without it), and the drop is
// saved at once: a later start, with the clock set back, finds neither (review
// of #152, round 5, findings 22, 31, 32). The rest of the file is kept, at the
// version 0.4.0 still opens.
func TestLegacyMarkerIsDroppedAtFirstOpen(t *testing.T) {
	in := t0.Add(-time.Hour).Format(time.RFC3339Nano)
	unread := fmt.Sprintf(`{%q:%q}`, CatchupKey("pr_opened|o/r#1"), in)
	for name, legacy := range map[string]string{
		"a marker in the window":    fmt.Sprintf(`"catchupMarker":%q`, in),
		"a marker out of it":        fmt.Sprintf(`"catchupMarker":%q`, t0.Add(-8*24*time.Hour).Format(time.RFC3339)),
		"a marker ahead":            fmt.Sprintf(`"catchupMarker":%q`, t0.Add(time.Hour).Format(time.RFC3339)),
		"an unreadable marker":      `"catchupMarker":"yesterday"`,
		"a marker and unread items": fmt.Sprintf(`"catchupMarker":%q,"catchupUnread":%s`, in, unread),
		"unread items alone":        `"catchupUnread":` + unread,
		"unreadable unread items":   `"catchupUnread":[1]`,
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "state.json")
			mustWriteFile(t, file, []byte(fmt.Sprintf(`{"version":2,"seen":{"claude:s1":%q},"grouping":{"/home/dev/w":"never"},%s}`, in, legacy)))
			s, err := openAt(file, func() time.Time { return t0 })
			if err != nil {
				t.Fatal(err)
			}
			if s.InMemory() {
				t.Fatal("the drop was saved, yet the store says it is in memory")
			}
			// What the next start reads, with the clock set back a week.
			r, err := openAt(file, func() time.Time { return t0.Add(-7 * 24 * time.Hour) })
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(file)
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			if doc["catchupMarker"] != nil || doc["catchupUnread"] != nil || string(doc["version"]) != "2" {
				t.Fatalf("the file after the first open: %s", data)
			}
			if _, ok := r.SeenAt("claude:s1"); !ok || len(r.Grouping()) != 1 {
				t.Fatal("the rest of the file was lost")
			}
		})
	}
}

// A drop that cannot be saved leaves the file as it was and keeps this run in
// memory: what it marks read is never taken as saved, and the next start
// drops the marker again.
func TestLegacyDropThatCannotBeSavedKeepsTheRunInMemory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "state.json")
	old := []byte(fmt.Sprintf(`{"version":1,"seen":{},"catchupMarker":%q}`, t0.Add(-time.Hour).Format(time.RFC3339)))
	mustWriteFile(t, file, old)
	if err := os.Chmod(dir, 0o500); err != nil { // the save's temporary file cannot be made
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	var logs strings.Builder
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	s, err := openAt(file, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	if !s.InMemory() || !strings.Contains(logs.String(), "memory only") {
		t.Fatalf("in memory %v, log %q", s.InMemory(), logs.String())
	}
	if err := s.SetCatchupRead(map[string]time.Time{CatchupKey("a"): t0}, t0); err != nil {
		t.Fatal(err)
	}
	if len(s.CatchupRead()) != 1 {
		t.Fatal("the run does not keep what it marked read")
	}
	if data, _ := os.ReadFile(file); string(data) != string(old) {
		t.Fatalf("the file was written: %s", data)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := openAt(file, func() time.Time { return t0 })
	if err != nil || r.InMemory() {
		t.Fatalf("next start: %v, in memory %v", err, r.InMemory())
	}
	if data, _ := os.ReadFile(file); strings.Contains(string(data), "catchupMarker") {
		t.Fatalf("the next start kept the marker: %s", data)
	}
}

// After Close a catch-up save changes nothing and says so.
func TestSetCatchupAfterCloseIsRefused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := openAt(file, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := s.SetCatchupRead(map[string]time.Time{CatchupKey("b"): t0}, t0); err != ErrClosed {
		t.Fatalf("read: err %v", err)
	}
	if len(s.CatchupRead()) != 0 || !s.CatchupMarkedAt().IsZero() {
		t.Fatal("changed in memory")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("written")
	}
}

// The acknowledged catch-up items survive a reopen, as hashes: at most MaxCatchupRead, the newest, and none older
// than SeenRetention. While the file holds one it is version 3, which 0.4.0
// refuses to open; without one it is version 1 again.
func TestCatchupReadSurvivesReopenAndIsBounded(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := openAt(file, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	read := map[string]time.Time{CatchupKey("too old"): t0.Add(-SeenRetention - time.Minute)}
	for i := range MaxCatchupRead + 5 {
		read[CatchupKey(fmt.Sprint("turn|codex|s1|", i))] = t0.Add(-time.Duration(i) * time.Second)
	}
	if err := s.SetCatchupRead(read, t0.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	r, err := openAt(file, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	got := r.CatchupRead()
	if len(got) != MaxCatchupRead || !r.CatchupMarkedAt().Equal(t0.Add(-time.Minute)) {
		t.Fatalf("read %d marked at %v", len(got), r.CatchupMarkedAt())
	}
	if _, ok := got[CatchupKey("turn|codex|s1|0")]; !ok {
		t.Fatal("the newest was dropped")
	}
	if _, ok := got[CatchupKey(fmt.Sprint("turn|codex|s1|", MaxCatchupRead))]; ok {
		t.Fatal("kept one past the cap")
	}
	if _, ok := got[CatchupKey("too old")]; ok {
		t.Fatal("kept one older than the retention")
	}
	data, _ := os.ReadFile(file)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil || string(doc["version"]) != "3" {
		t.Fatalf("version %s (%v), want 3 while it holds acknowledgements", doc["version"], err)
	}
	var onDisk map[string]time.Time
	if err := json.Unmarshal(doc["catchupRead"], &onDisk); err != nil || len(onDisk) != MaxCatchupRead {
		t.Fatalf("the file holds %d read items (%v), want %d", len(onDisk), err, MaxCatchupRead)
	}
	if strings.Contains(string(data), "s1") {
		t.Fatal("the file holds an item's identity in clear")
	}
	// The way 0.4.0 opens a file: it reads up to version 2.
	if v := versionOf(r.(*fileStore).doc); v <= 2 {
		t.Fatalf("written as %d: 0.4.0 would open it and drop the acknowledgements", v)
	}
	if err := r.SetCatchupRead(nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(file)
	if err := json.Unmarshal(data, &doc); err != nil || string(doc["version"]) != "1" {
		t.Fatalf("without acknowledgements: version %s (%v), want 1", doc["version"], err)
	}
}

// The acknowledged set is pruned by each item's own time on every write and
// on opening, whatever is written: 7 days after the last acknowledged item,
// an unrelated write leaves the file without it, as a version older builds
// open again.
func TestCatchupReadIsPrunedByAgeOnAnyWrite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	now := t0
	clock := func() time.Time { return now }
	s, err := openAt(file, clock)
	if err != nil {
		t.Fatal(err)
	}
	read := map[string]time.Time{CatchupKey("turn|codex|s1|1"): t0.Add(-time.Hour), CatchupKey("turn|codex|s1|2"): t0}
	if err := s.SetCatchupRead(read, t0); err != nil {
		t.Fatal(err)
	}
	now = t0.Add(SeenRetention - 30*time.Minute) // the first is past the retention, the second not
	if err := s.SetSeen("claude:other", t0); err != nil {
		t.Fatal(err)
	}
	if n := len(s.CatchupRead()); n != 1 {
		t.Fatalf("%d kept, want 1", n)
	}
	var written struct {
		Read map[string]time.Time `json:"catchupRead"`
	}
	if data, err := os.ReadFile(file); err != nil || json.Unmarshal(data, &written) != nil || len(written.Read) != 1 {
		t.Fatalf("the file holds %d acknowledged (%v), want 1", len(written.Read), err)
	}
	now = t0.Add(SeenRetention + time.Minute)
	r, err := openAt(file, clock)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r.CatchupRead()); n != 0 {
		t.Fatalf("%d kept on opening, want 0", n)
	}
	if err := r.SetSeen("claude:other", t0); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(file)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil || string(doc["version"]) == "3" || doc["catchupRead"] != nil {
		t.Fatalf("version %s, read %s (%v): want an older version without acknowledgements", doc["version"], doc["catchupRead"], err)
	}
}

// A bad read entry is dropped and the rest kept; a bad read set drops only
// itself, never the file.
func TestCatchupReadBadEntries(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	at := t0.Add(-time.Hour).Format(time.RFC3339)
	for _, tc := range []struct {
		doc  string
		read int
	}{
		{fmt.Sprintf(`{"version":1,"seen":{},"catchupRead":{%q:%q,"not-a-key":%q,%q:"soon"}}`, CatchupKey("a"), at, at, CatchupKey("b")), 1},
		{`{"version":1,"seen":{"claude:s1":"` + at + `"},"catchupRead":[1]}`, 0},
	} {
		if err := os.WriteFile(file, []byte(tc.doc), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := openAt(file, func() time.Time { return t0 })
		if err != nil {
			t.Fatalf("%s: %v", tc.doc, err)
		}
		if len(s.CatchupRead()) != tc.read {
			t.Fatalf("%s: read %v", tc.doc, s.CatchupRead())
		}
	}
}

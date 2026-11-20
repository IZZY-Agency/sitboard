package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The unread catch-up items survive a reopen, with the marker, in one file
// written as version 1: older builds still open it (and drop only these).
func TestCatchupUnreadSurvivesReopen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := openAt(file, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	k := CatchupKey("turn|claude|s1|1")
	if err := s.SetCatchup(t0, map[string]time.Time{k: t0.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	r, err := openAt(file, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	if got := r.CatchupUnread(); len(got) != 1 || !got[k].Equal(t0.Add(-time.Hour)) || !r.CatchupMarker().Equal(t0) {
		t.Fatalf("unread %v marker %v", got, r.CatchupMarker())
	}
	data, _ := os.ReadFile(file)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil || string(doc["version"]) != "1" {
		t.Fatalf("version %s (%v)", doc["version"], err)
	}
	if strings.Contains(string(data), "s1") {
		t.Fatal("the file holds the item's identity in clear")
	}
}

// At most MaxCatchupUnread are kept, the newest; and none older than
// SeenRetention.
func TestCatchupUnreadIsBounded(t *testing.T) {
	s, err := openAt(filepath.Join(t.TempDir(), "state.json"), func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCatchup(t0, map[string]time.Time{CatchupKey("too old"): t0.Add(-SeenRetention - time.Minute), CatchupKey("new"): t0}); err != nil {
		t.Fatal(err)
	}
	if got := s.CatchupUnread(); len(got) != 1 {
		t.Fatalf("retention: %v", got)
	}
	in := map[string]time.Time{CatchupKey("too old"): t0.Add(-SeenRetention - time.Minute)}
	for i := range MaxCatchupUnread + 5 {
		in[CatchupKey(fmt.Sprint(i))] = t0.Add(-time.Duration(i) * time.Minute)
	}
	if err := s.SetCatchup(t0, in); err != nil {
		t.Fatal(err)
	}
	got := s.CatchupUnread()
	if len(got) != MaxCatchupUnread {
		t.Fatalf("kept %d", len(got))
	}
	if _, ok := got[CatchupKey("0")]; !ok {
		t.Fatal("the newest was dropped")
	}
	if _, ok := got[CatchupKey(fmt.Sprint(MaxCatchupUnread))]; ok {
		t.Fatal("kept one past the cap")
	}
	if _, ok := got[CatchupKey("too old")]; ok {
		t.Fatal("kept one older than the retention")
	}
}

// A file from before this key loads with none; a bad entry is dropped, the
// rest of the file kept.
func TestCatchupUnreadOlderAndBadFiles(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	at := t0.Add(-time.Hour).Format(time.RFC3339)
	for _, tc := range []struct {
		doc  string
		want int
	}{
		{fmt.Sprintf(`{"version":1,"seen":{"claude:s1":%q},"catchupMarker":%q}`, at, at), 0},
		{`{"version":2,"seen":{},"grouping":{"/a":"never"}}`, 0},
		{fmt.Sprintf(`{"version":1,"seen":{"claude:s1":%q},"catchupUnread":{%q:%q,"not-a-key":%q,%q:"soon"}}`, at, CatchupKey("a"), at, at, CatchupKey("b")), 1},
		{fmt.Sprintf(`{"version":1,"seen":{"claude:s1":%q},"catchupUnread":[1]}`, at), 0},
	} {
		if err := os.WriteFile(file, []byte(tc.doc), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := openAt(file, func() time.Time { return t0 })
		if err != nil {
			t.Fatalf("%s: %v", tc.doc, err)
		}
		if got := s.CatchupUnread(); len(got) != tc.want {
			t.Fatalf("%s: unread %v", tc.doc, got)
		}
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
	if err := s.SetCatchup(t0, map[string]time.Time{CatchupKey("a"): t0}); err != ErrClosed {
		t.Fatalf("err %v", err)
	}
	if len(s.CatchupUnread()) != 0 || !s.CatchupMarker().IsZero() {
		t.Fatal("changed in memory")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("written")
	}
}

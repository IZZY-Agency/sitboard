package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	pinA = PinKey("github", "github.com", "acme/widgets", 145)
	pinB = PinKey("gitlab", "gitlab.example.com:8443", "group/sub/tools", 7)
)

// Pins survive a restart: what SetPin stored opens again, with its time, and
// pinning a pinned issue keeps its first time and writes nothing.
func TestPinsRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	now := t0
	s, err := OpenAt(file, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPin(pinA, true, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPin(pinB, true, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(file)
	if err := s.SetPin(pinA, true, t0.Add(time.Hour)); err != nil { // again
		t.Fatal(err)
	}
	if after, _ := os.Stat(file); !sameFile(before, after) {
		t.Error("pinning a pinned issue wrote the file")
	}
	r, err := OpenAt(file, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Time{pinA: t0, pinB: t0.Add(time.Minute)}
	if got := r.Pins(); len(got) != 2 || !got[pinA].Equal(want[pinA]) || !got[pinB].Equal(want[pinB]) {
		t.Fatalf("reopened pins %v, want %v", got, want)
	}
	if err := r.SetPin(pinA, false, t0); err != nil {
		t.Fatal(err)
	}
	if err := r.SetPin(pinA, false, t0); err != nil { // unpinning an unpinned issue
		t.Fatal(err)
	}
	r2, err := OpenAt(file, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.Pins(); len(got) != 1 || !got[pinB].Equal(want[pinB]) {
		t.Fatalf("after unpinning: %v", got)
	}
}

// While the file holds a pin it is version 4, so 0.5.x (which reads up to
// 3) refuses it instead of dropping the pins; with the last pin gone it is
// written as before (here 1, or 3 with a catch-up acknowledgement), so 0.5.x
// opens it again.
func TestVersionIsFourOnlyWhileAPinIsStored(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s := openStore(t, file)
	if err := s.SetSeen("claude:a", time.Now()); err != nil {
		t.Fatal(err)
	}
	if v := versionOnDisk(t, file); v != 1 {
		t.Fatalf("no pin: version %d, want 1", v)
	}
	if err := s.SetPin(pinA, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if v := versionOnDisk(t, file); v != 4 {
		t.Fatalf("with a pin: version %d, want 4", v)
	}
	if err := s.SetPin(pinA, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if v := versionOnDisk(t, file); v != 1 {
		t.Fatalf("pin removed: version %d, want 1", v)
	}
	if err := s.SetCatchupRead(map[string]time.Time{CatchupKey("x"): time.Now()}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPin(pinB, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if v := versionOnDisk(t, file); v != 4 {
		t.Fatalf("pin and acknowledgement: version %d, want 4", v)
	}
	if err := s.SetPin(pinB, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if v := versionOnDisk(t, file); v != 3 {
		t.Fatalf("acknowledgement only: version %d, want 3", v)
	}
	data, _ := os.ReadFile(file)
	if strings.Contains(string(data), `"pins"`) || strings.Contains(string(data), `"pinsClosed"`) {
		t.Fatalf("a file with no pin names the key: %s", data)
	}
}

// This build opens a version 4 file with its pins, and refuses a version 5
// one unchanged.
func TestVersionFourOpensAndFiveIsRefused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	mustWriteFile(t, file, []byte(`{"version":4,"seen":{},"pins":{"`+pinA+`":"2026-11-17T10:00:00Z"}}`))
	s, err := OpenAt(file, atT0)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Pins(); len(got) != 1 || !got[pinA].Equal(t0) {
		t.Fatalf("pins %v", got)
	}
	newer := `{"version":5,"seen":{},"pins":{"` + pinA + `":"2026-11-17T10:00:00Z"}}`
	mustWriteFile(t, file, []byte(newer))
	if _, err := Open(file); err == nil || !strings.Contains(err.Error(), "newer sitboard (file version 5, this build reads up to 4)") {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != newer {
		t.Fatalf("refusing changed the file: %s", b)
	}
}

// A new pin past MaxPins is refused and changes nothing; an unpin, and a pin
// of an issue pinned already, still go through at the cap.
func TestPinsCap(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s := openStore(t, file)
	for i := range MaxPins {
		if err := s.SetPin(PinKey("github", "github.com", "acme/widgets", i+1), true, t0); err != nil {
			t.Fatalf("pin %d: %v", i+1, err)
		}
	}
	before, _ := os.ReadFile(file)
	extra := PinKey("github", "github.com", "acme/widgets", MaxPins+1)
	if err := s.SetPin(extra, true, t0); !errors.Is(err, ErrTooManyPins) {
		t.Fatalf("pin past the cap: %v", err)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) || len(s.Pins()) != MaxPins {
		t.Fatal("a refused pin changed the store")
	}
	if err := s.SetPin(PinKey("github", "github.com", "acme/widgets", 1), true, t0); err != nil {
		t.Fatalf("pinning a pinned issue at the cap: %v", err)
	}
	if err := s.SetPin(PinKey("github", "github.com", "acme/widgets", 1), false, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPin(extra, true, t0); err != nil {
		t.Fatalf("pin after an unpin: %v", err)
	}
}

// A hand-edited file with more than MaxPins pins keeps the newest, and the
// next write saves the cut.
func TestPinsCappedAtLoad(t *testing.T) {
	pins := map[string]string{}
	for i := range MaxPins + 5 {
		pins[PinKey("github", "github.com", "acme/widgets", i+1)] = t0.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
	}
	data, _ := json.Marshal(map[string]any{"version": 4, "seen": map[string]any{}, "pins": pins})
	file := filepath.Join(t.TempDir(), "state.json")
	mustWriteFile(t, file, data)
	s, err := OpenAt(file, atT0)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Pins()
	if len(got) != MaxPins {
		t.Fatalf("%d pins, want %d", len(got), MaxPins)
	}
	for i := range 5 {
		if _, ok := got[PinKey("github", "github.com", "acme/widgets", i+1)]; ok {
			t.Fatalf("kept the old pin #%d over a newer one", i+1)
		}
	}
}

// Retention on the store's clock: a pin reported closed stays SeenRetention
// from the first report (a later report does not restart it), then is gone
// from Pins and from the file on the next write; reopened before that, it
// stays. "Not found" drops a pin at once. The closed mark survives a restart.
func TestPinRetention(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	now := t0
	clock := func() time.Time { return now }
	s, err := OpenAt(file, clock)
	if err != nil {
		t.Fatal(err)
	}
	pinC := PinKey("github", "github.com", "acme/widgets", 9)
	for _, k := range []string{pinA, pinB, pinC} {
		if err := s.SetPin(k, true, t0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReportPins(map[string]PinReport{pinA: PinClosed, pinC: PinClosed, "github|github.com|acme/other#1": PinClosed}); err != nil {
		t.Fatal(err)
	}
	now = t0.Add(time.Hour)
	if err := s.ReportPins(map[string]PinReport{pinA: PinClosed, pinB: PinGone}); err != nil { // A again: keeps its first time
		t.Fatal(err)
	}
	if got := s.Pins(); len(got) != 2 {
		t.Fatalf("not found was not dropped at once: %v", got)
	}
	if got := s.PinsClosed(); !got[pinA].Equal(t0) || !got[pinC].Equal(t0) || len(got) != 2 {
		t.Fatalf("closed marks %v", got)
	}
	now = t0.Add(2 * time.Hour)
	if err := s.ReportPins(map[string]PinReport{pinC: PinOpen}); err != nil { // reopened
		t.Fatal(err)
	}
	// A restart keeps the mark and its time.
	s, err = OpenAt(file, clock)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.PinsClosed(); len(got) != 1 || !got[pinA].Equal(t0) {
		t.Fatalf("closed marks after a restart %v", got)
	}
	now = t0.Add(SeenRetention - time.Second)
	if _, ok := s.Pins()[pinA]; !ok {
		t.Fatal("dropped before 7 days")
	}
	now = t0.Add(SeenRetention + time.Second)
	if got := s.Pins(); len(got) != 1 || got[pinC].IsZero() {
		t.Fatalf("7 days after closing: %v", got)
	}
	if err := s.SetSeen("claude:x", now); err != nil { // any write saves the drop
		t.Fatal(err)
	}
	data, _ := os.ReadFile(file)
	if strings.Contains(string(data), "#145") || strings.Contains(string(data), "pinsClosed") {
		t.Fatalf("the expired pin is still in the file: %s", data)
	}
	if v := versionOnDisk(t, file); v != 4 {
		t.Fatalf("version %d with a pin left", v)
	}
}

// Only GitHub is asked about a pin by number (#182): a GitLab pin's closed
// mark from an earlier build is not shown, never ages its pin out, and
// leaves the file on the next write; the pin stays. A GitHub pin's mark
// still does all of that.
func TestAGitLabPinsClosedMarkGoesAndItsPinStays(t *testing.T) {
	stamp := t0.Format(time.RFC3339)
	data, _ := json.Marshal(map[string]any{"version": 4, "seen": map[string]any{},
		"pins": map[string]string{pinA: stamp, pinB: stamp}, "pinsClosed": map[string]string{pinA: stamp, pinB: stamp}})
	file := filepath.Join(t.TempDir(), "state.json")
	mustWriteFile(t, file, data)
	now := t0.Add(time.Hour)
	s, err := OpenAt(file, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if got := s.PinsClosed(); len(got) != 1 || !got[pinA].Equal(t0) {
		t.Fatalf("closed marks %v, want GitHub's only", got)
	}
	now = t0.Add(SeenRetention + time.Hour)
	if got := s.Pins(); len(got) != 1 || !got[pinB].Equal(t0) {
		t.Fatalf("pins %v, want the GitLab one kept and the GitHub one aged out", got)
	}
	if err := s.SetSeen("claude:x", now); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(file); strings.Contains(string(raw), "pinsClosed") || !strings.Contains(string(raw), "#7") {
		t.Fatalf("the file: %s", raw)
	}
}

// Nothing reported, or only keys not pinned, writes nothing.
func TestReportPinsWritesOnlyChanges(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := OpenAt(file, atT0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPin(pinA, true, t0); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(file)
	if err := s.ReportPins(map[string]PinReport{pinA: PinOpen, pinB: PinGone}); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(file); !sameFile(before, after) {
		t.Error("a report that changes nothing wrote the file")
	}
}

// SetPin takes only a key in PinKey's form; unreadable entries in the file are
// dropped with a log line that names no key, and the rest is kept.
func TestPinKeysAndUnreadableEntries(t *testing.T) {
	for _, k := range []string{"", "github|github.com|a/b", "github|github.com|a/b#0", "github|github.com|a/b#-1",
		"github|github.com|a/b#2147483648", "github|github.com|a/b#01", "svn|host|a/b#1", "github||a/b#1",
		"github|github.com|#1", "github|github.com|a b#1", "github|github.com|a|b#1", "github|github.com|a/b#1\n",
		"github|github.com|" + strings.Repeat("a", maxPinKey) + "#1"} {
		if err := Memory().SetPin(k, true, t0); err == nil {
			t.Errorf("SetPin took %q", k)
		}
	}
	if f, h, p, n, ok := ParsePinKey(PinKey("gitlab", "git.example.com:8443", "g/s/r", 2147483647)); !ok || f != "gitlab" || h != "git.example.com:8443" || p != "g/s/r" || n != 2147483647 {
		t.Errorf("parse %q %q %q %d %v", f, h, p, n, ok)
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	file := filepath.Join(t.TempDir(), "state.json")
	mustWriteFile(t, file, []byte(fmt.Sprintf(`{"version":4,"seen":{},"pins":{%q:"2026-11-17T10:00:00Z","bad-secret-key":"2026-11-17T10:00:00Z",%q:7},"pinsClosed":"x"}`, pinA, pinB)))
	s, err := OpenAt(file, atT0)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Pins(); len(got) != 1 || !got[pinA].Equal(t0) {
		t.Fatalf("pins %v", got)
	}
	if strings.Contains(logs.String(), "bad-secret-key") || !strings.Contains(logs.String(), "dropped 2 unreadable pinned issue(s)") {
		t.Fatalf("logged %q", logs.String())
	}
}

// --no-state: a memory store keeps pins for the run and says it keeps
// nothing (the board serves that as Issues.Unsaved).
func TestMemoryKeepsPinsForTheRun(t *testing.T) {
	s := MemoryAt(atT0)
	if err := s.SetPin(pinA, true, t0); err != nil {
		t.Fatal(err)
	}
	if got := s.Pins(); len(got) != 1 || !s.InMemory() {
		t.Fatalf("pins %v, in memory %v", got, s.InMemory())
	}
}

// After Close, a pin change on a store with a file is refused with ErrClosed
// and changes nothing.
func TestPinChangesAfterCloseAreRefused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s := openStore(t, file)
	s.Close()
	if err := s.SetPin(pinA, true, t0); !errors.Is(err, ErrClosed) {
		t.Fatalf("err %v", err)
	}
	if len(s.Pins()) != 0 {
		t.Fatal("a refused pin is kept in memory")
	}
}

// A null time is a malformed entry: a null pinned-at drops that pin, a null
// closed mark drops only the mark and leaves its pin.
func TestNullPinTimesAreMalformed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	mustWriteFile(t, file, []byte(fmt.Sprintf(`{"version":4,"seen":{},"pins":{%q:"2026-11-17T10:00:00Z",%q:null},"pinsClosed":{%q:null}}`, pinA, pinB, pinA)))
	s, err := OpenAt(file, func() time.Time { return t0.Add(30 * 24 * time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Pins(); len(got) != 1 || !got[pinA].Equal(t0) {
		t.Fatalf("pins %v", got)
	}
	if got := s.PinsClosed(); len(got) != 0 {
		t.Fatalf("closed marks %v", got)
	}
}

// A hand-edited file may spell a GitHub host and owner/name in any case: on
// load each is the board's lower-case key, and two spellings of one issue
// are one pin (the newer pinned-at) with one closed mark (the earlier), merged
// before the cap. GitLab keys keep their spelling and port.
func TestPinKeysAreCanonicalOnLoad(t *testing.T) {
	pins := map[string]string{
		"github|GitHub.com|Acme/Widgets#145": t0.Add(time.Hour).Format(time.RFC3339),
		pinA:                                 t0.Format(time.RFC3339),
		"gitlab|GitLab.Example.com:8443|Group/Tools#7": t0.Format(time.RFC3339),
	}
	for i := range MaxPins - 2 { // with the merge, exactly the cap
		pins[PinKey("github", "github.com", "acme/widgets", 1000+i)] = t0.Format(time.RFC3339)
	}
	closed := map[string]string{
		"github|github.com|ACME/widgets#145": t0.Add(2 * time.Hour).Format(time.RFC3339),
		pinA:                                 t0.Add(time.Hour).Format(time.RFC3339),
	}
	data, _ := json.Marshal(map[string]any{"version": 4, "seen": map[string]any{}, "pins": pins, "pinsClosed": closed})
	file := filepath.Join(t.TempDir(), "state.json")
	mustWriteFile(t, file, data)
	s, err := OpenAt(file, func() time.Time { return t0.Add(3 * time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	got := s.Pins()
	if len(got) != MaxPins || !got[pinA].Equal(t0.Add(time.Hour)) {
		t.Fatalf("%d pins, %s at %v", len(got), pinA, got[pinA])
	}
	if _, ok := got["gitlab|GitLab.Example.com:8443|Group/Tools#7"]; !ok {
		t.Fatal("a GitLab key was respelled")
	}
	if c := s.PinsClosed(); len(c) != 1 || !c[pinA].Equal(t0.Add(time.Hour)) {
		t.Fatalf("closed marks %v", c)
	}
	// A new pin given in another case is stored in the board's.
	if err := s.SetPin("github|GitHub.com|Acme/Widgets#146", true, t0); !errors.Is(err, ErrTooManyPins) {
		t.Fatalf("at the cap: %v", err)
	}
	if err := s.SetPin("github|GitHub.com|Acme/Widgets#145", true, t0); err != nil { // pinned already, in the board's case
		t.Fatalf("an alias of a pin counts as new: %v", err)
	}
	// The API's lower-case spelling unpins it: nothing of it stays.
	if err := s.SetPin("github|github.com|acme/widgets#145", false, t0); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Pins()[pinA]; ok || len(s.Pins()) != MaxPins-1 {
		t.Fatalf("pins after unpinning %d", len(s.Pins()))
	}
}

// A report on a pin whose 7 days after closing are over finds it gone: a
// late "open" does not bring it back, even with no save in between.
func TestALateReportDoesNotRevive(t *testing.T) {
	now := t0
	s := MemoryAt(func() time.Time { return now })
	if err := s.SetPin(pinA, true, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportPins(map[string]PinReport{pinA: PinClosed}); err != nil {
		t.Fatal(err)
	}
	now = t0.Add(SeenRetention + time.Hour)
	if err := s.ReportPins(map[string]PinReport{pinA: PinOpen}); err != nil {
		t.Fatal(err)
	}
	if got := s.Pins(); len(got) != 0 {
		t.Fatalf("revived: %v", got)
	}
}

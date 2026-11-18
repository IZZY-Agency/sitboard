package state

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/cpucost"
)

var t0 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

func TestMissingFileOpensEmpty(t *testing.T) {
	file := filepath.Join(t.TempDir(), "nested", "state.json")
	s, err := Open(file)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := s.SeenAt("claude:1234"); ok {
		t.Fatal("empty store reports a seen card")
	}
	if !s.CatchupMarker().IsZero() {
		t.Fatal("empty store has a catch-up marker")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("Open created the file; want it created only on the first write (stat err %v)", err)
	}
}

func TestSeenSurvivesReopen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeen("claude:1234", t0); err != nil {
		t.Fatalf("SetSeen: %v", err)
	}
	if err := s.SetCatchupMarker(t0.Add(time.Hour)); err != nil {
		t.Fatalf("SetCatchupMarker: %v", err)
	}

	again, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	at, ok := again.SeenAt("claude:1234")
	if !ok || !at.Equal(t0) {
		t.Fatalf("SeenAt after reopen = %v, %v; want %v, true", at, ok, t0)
	}
	if _, ok := again.SeenAt("claude:9999"); ok {
		t.Fatal("unknown card reported as seen")
	}
	if m := again.CatchupMarker(); !m.Equal(t0.Add(time.Hour)) {
		t.Fatalf("CatchupMarker after reopen = %v", m)
	}
}

func TestStateFileModeIs0600(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeen("claude:1234", t0); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}
}

func TestWriteIsAtomic(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root user on a unix system to make the folder read-only")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "state.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeen("claude:1234", t0); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	// A read-only folder makes the temp-file step fail.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := s.SetSeen("claude:5678", t0); err == nil {
		t.Fatal("SetSeen succeeded in a read-only folder")
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("failed write changed the file:\nbefore %s\nafter  %s", before, after)
	}
	if _, ok := s.SeenAt("claude:5678"); ok {
		t.Fatal("failed write still changed the in-memory state")
	}
}

func TestCorruptFileIsAnError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(file, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(file); err == nil {
		t.Fatal("Open accepted a corrupt state file; it must refuse rather than silently forget what was seen")
	}
}

func TestConcurrentUse(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			id := "claude:" + string(rune('a'+i))
			for range 20 {
				if err := s.SetSeen(id, t0); err != nil {
					t.Error(err)
					return
				}
				s.SeenAt(id)
				s.CatchupMarker()
			}
		})
	}
	wg.Wait()
	for i := range 8 {
		if _, ok := s.SeenAt("claude:" + string(rune('a'+i))); !ok {
			t.Fatalf("card %d lost", i)
		}
	}
}

// A newer sitboard may add fields. Reading its file as version 1 would drop
// them on the next write, so an older build refuses it.
func TestNewerFileVersionIsRefused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	data := `{"version": 2, "seen": {"claude:1234": "2026-11-17T10:00:00Z"}, "pins": ["x"]}`
	if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(file)
	if err == nil {
		t.Fatal("Open accepted a state file from a newer version")
	}
	if !strings.Contains(err.Error(), "newer") {
		t.Fatalf("error %q does not say the file is from a newer version", err)
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != data {
		t.Fatal("refusing the file still changed it")
	}
}

func TestCurrentAndUnversionedFilesOpen(t *testing.T) {
	for _, data := range []string{
		`{"version": 1, "seen": {}}`,
		`{"seen": {}}`,
	} {
		file := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(file); err != nil {
			t.Fatalf("Open(%s): %v", data, err)
		}
	}
}

// Seen marks older than 7 days are dropped when the file is written, so the
// file does not grow forever.
func TestWritePrunesOldSeenEntries(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	fs := s.(*fileStore)
	old := t0.Add(-SeenRetention - time.Minute)
	edge := t0.Add(-SeenRetention + time.Minute)

	// Both marks are fresh when written.
	fs.now = func() time.Time { return old }
	for id, at := range map[string]time.Time{"claude:old": old, "claude:edge": edge} {
		if err := s.SetSeen(id, at); err != nil {
			t.Fatal(err)
		}
	}
	// Seven days later an unrelated write drops the mark that aged out.
	fs.now = func() time.Time { return t0 }
	if err := s.SetCatchupMarker(t0); err != nil {
		t.Fatal(err)
	}

	again, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.SeenAt("claude:old"); ok {
		t.Fatal("a seen mark older than 7 days survived a write")
	}
	if _, ok := again.SeenAt("claude:edge"); !ok {
		t.Fatal("a seen mark inside 7 days was pruned")
	}
	if _, ok := s.SeenAt("claude:old"); ok {
		t.Fatal("the pruned mark is still in memory")
	}
}

// Umbrella folders are remembered with their last-seen time, survive a
// reopen, and can be forgotten.
func TestUmbrellasSurviveReopen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	if err := s.TouchUmbrellas([]string{"/home/dev/work", "/home/dev/other"}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetUmbrellas([]string{"/home/dev/other"}); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.Umbrellas()
	if len(got) != 1 || !got["/home/dev/work"].Equal(at) {
		t.Fatalf("umbrellas %v", got)
	}
	got["/x"] = at // a copy: the store is not changed
	if len(s2.Umbrellas()) != 1 {
		t.Fatal("Umbrellas returned the store's own map")
	}
}

// Entries not seen for UmbrellaRetention are dropped, and at most
// MaxUmbrellas are kept (the most recently seen).
func TestUmbrellasArePrunedAndCapped(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.TouchUmbrellas([]string{"/old"}, now.Add(-UmbrellaRetention-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Umbrellas()["/old"]; ok {
		t.Fatal("an umbrella older than the retention is still read")
	}
	for i := range MaxUmbrellas + 10 {
		if err := s.TouchUmbrellas([]string{fmt.Sprintf("/u%03d", i)}, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Umbrellas()
	if len(got) != MaxUmbrellas {
		t.Fatalf("%d umbrellas, cap %d", len(got), MaxUmbrellas)
	}
	if _, ok := got["/u000"]; ok {
		t.Fatal("the oldest umbrella was kept over newer ones")
	}
	if _, ok := got[fmt.Sprintf("/u%03d", MaxUmbrellas+9)]; !ok {
		t.Fatal("the newest umbrella was dropped")
	}
}

// A state file written before umbrellas existed loads, and keeps its marks.
func TestFileWithoutUmbrellasLoads(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	at := time.Now().UTC().Truncate(time.Second)
	doc := fmt.Sprintf(`{"version":1,"seen":{"claude:s1":%q}}`, at.Format(time.RFC3339))
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Umbrellas()) != 0 {
		t.Fatalf("umbrellas %v", s.Umbrellas())
	}
	if got, ok := s.SeenAt("claude:s1"); !ok || !got.Equal(at) {
		t.Fatalf("seen %v %v", got, ok)
	}
}

// writeUmbrellaFile writes a state file with n umbrellas, one second apart,
// the last one dated far in the future (a wrong clock).
func writeUmbrellaFile(t *testing.T, n int, now time.Time) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "state.json")
	var b strings.Builder
	b.WriteString(`{"version":1,"seen":{},"umbrellas":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		at := now.Add(-time.Duration(i) * time.Second)
		if i == n-1 {
			at = now.Add(50 * 365 * 24 * time.Hour)
		}
		fmt.Fprintf(&b, "%q:%q", fmt.Sprintf("/u/%06d", i), at.Format(time.RFC3339))
	}
	b.WriteString(`}}`)
	if err := os.WriteFile(file, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// A large file is cut at load: the newest MaxUmbrellas only, and a time in
// the future (a wrong clock) counts as now, so it still ages out. Forgetting
// many folders is one write.
func TestUmbrellasCappedAndClampedAtLoad(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s, err := Open(writeUmbrellaFile(t, 100000, now))
	if err != nil {
		t.Fatal(err)
	}
	got := s.Umbrellas()
	if len(got) != MaxUmbrellas {
		t.Fatalf("%d umbrellas after load, cap %d", len(got), MaxUmbrellas)
	}
	for p, at := range got {
		if at.After(time.Now()) {
			t.Fatalf("%s is dated in the future: %v", p, at)
		}
	}
	if _, ok := got["/u/099999"]; !ok {
		t.Fatal("the entry dated in the future was not kept as the newest")
	}
	paths := make([]string, 0, len(got))
	for p := range got {
		paths = append(paths, p)
	}
	if err := s.ForgetUmbrellas(paths); err != nil {
		t.Fatal(err)
	}
	if len(s.Umbrellas()) != 0 {
		t.Fatal("ForgetUmbrellas left entries")
	}
}

// Loading is linear (one pass and one sort): 4 times the entries take about
// 4 times as long, a quadratic step 16 times. The least CPU time of three
// loads at n and 4n is compared, so a loaded machine or the race detector
// does not fail it; the absolute bound only catches a hang.
func TestUmbrellaLoadIsLinear(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	timeOf := func(file string) time.Duration {
		return cpucost.Of(3, func() {
			if _, err := Open(file); err != nil {
				t.Fatal(err)
			}
		})
	}
	t1 := timeOf(writeUmbrellaFile(t, 25000, now))
	t4 := timeOf(writeUmbrellaFile(t, 100000, now))
	t.Logf("load: %s for 25,000 umbrellas, %s for 100,000", t1, t4)
	if t4 > 8*t1+50*time.Millisecond {
		t.Errorf("load grows faster than linear: %s at n, %s at 4n", t1, t4)
	}
	if limit := 10 * time.Second; t4 > limit {
		t.Errorf("loading 100,000 umbrellas took %s", t4)
	}
}

// Only absolute, clean folders other than the root are umbrellas: anything
// else in the file is dropped at load (dated in the future it would rank
// newest and push real entries out of the cap) and never stored.
func TestUmbrellaPathsAreValidated(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	doc := fmt.Sprintf(`{"version":1,"seen":{},"umbrellas":{"":%[1]q,"relative/dir":%[1]q,"/a/b/":%[1]q,"/":%[1]q,"/a/./b":%[1]q,"/home/dev/work":%[2]q}}`, future, past)
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Umbrellas()
	if len(got) != 1 {
		t.Fatalf("umbrellas after load: %v", got)
	}
	if err := s.TouchUmbrellas([]string{"rel", "/x/", "/"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(s.Umbrellas()) != 1 {
		t.Fatalf("invalid paths were stored: %v", s.Umbrellas())
	}
}

package state

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// v010Document is the file format sitboard 0.1.0 reads and writes.
type v010Document struct {
	Version       int                  `json:"version"`
	Seen          map[string]time.Time `json:"seen"`
	CatchupMarker time.Time            `json:"catchupMarker,omitzero"`
	Umbrellas     map[string]time.Time `json:"umbrellas,omitempty"`
}

// A file exactly as 0.1.0 writes it loads, with every mark, and nothing is
// forgotten.
func TestFileWrittenBy010Loads(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	at := time.Now().UTC().Truncate(time.Second)
	old, err := json.MarshalIndent(v010Document{
		Version:       1,
		Seen:          map[string]time.Time{"claude:s1": at},
		CatchupMarker: at.Add(-time.Hour),
		Umbrellas:     map[string]time.Time{"/home/dev/work": at},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, old, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(file)
	if err != nil {
		t.Fatalf("a 0.1.0 file does not load: %v", err)
	}
	if got, ok := s.SeenAt("claude:s1"); !ok || !got.Equal(at) {
		t.Fatalf("seen %v %v", got, ok)
	}
	// Its catch-up marker is dropped at once, and the drop saved.
	if b, _ := os.ReadFile(file); strings.Contains(string(b), "catchupMarker") {
		t.Fatalf("the marker was kept: %s", b)
	}
	if got := s.Umbrellas(); len(got) != 1 || !got["/home/dev/work"].Equal(at) {
		t.Fatalf("umbrellas %v", got)
	}
	if len(s.Forgotten()) != 0 {
		t.Fatalf("forgotten %v", s.Forgotten())
	}
}

// A file with a forgotten folder keeps version 1, so 0.1.0 still opens it;
// it reads everything but the forgotten list.
func TestFileWithForgottenStillOpensIn010(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	if err := s.SetSeen("claude:s1", at); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchUmbrellas([]string{"/home/dev/a", "/home/dev/b"}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForgetGrouping("/home/dev/a", at); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var old v010Document
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}
	if old.Version != 1 {
		t.Fatalf("version %d: 0.1.0 refuses anything above 1", old.Version)
	}
	if _, ok := old.Seen["claude:s1"]; !ok || len(old.Umbrellas) != 1 || old.Umbrellas["/home/dev/b"].IsZero() {
		t.Fatalf("0.1.0 would read %+v", old)
	}
}

// ForgetGrouping moves a folder from the umbrellas to the forgotten list in
// one write; both survive a reopen; a forgotten folder is never touched as an
// umbrella; releasing it lets it be remembered again.
func TestForgetGroupingAndRelease(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second).Add(-3 * time.Hour) // a time in the future is clamped at load
	if err := s.TouchUmbrellas([]string{"/home/dev/work"}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForgetGrouping("/home/dev/work", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Umbrellas()) != 0 {
		t.Fatalf("umbrellas %v", s.Umbrellas())
	}
	if got := s.Forgotten(); len(got) != 1 || !got["/home/dev/work"].Equal(at.Add(time.Minute)) {
		t.Fatalf("forgotten %v", got)
	}
	got := s.Forgotten()
	got["/x"] = at // a copy: the store is not changed
	if len(s.Forgotten()) != 1 {
		t.Fatal("Forgotten returned the store's own map")
	}

	if err := s.TouchUmbrellas([]string{"/home/dev/work"}, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(s.Umbrellas()) != 0 {
		t.Fatal("a forgotten folder was remembered as an umbrella")
	}

	if err := s.TouchForgotten(map[string]time.Time{"/home/dev/work": at.Add(time.Hour), "/home/dev/never": at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := s.Forgotten(); len(got) != 1 || !got["/home/dev/work"].Equal(at.Add(time.Hour)) {
		t.Fatalf("after touch: %v (a folder that was not forgotten must not be added)", got)
	}

	// A release that names an older time than the stored one keeps the hold:
	// the folder was forgotten again after it was released.
	if err := s.ReleaseForgotten(map[string]time.Time{"/home/dev/work": at}); err != nil {
		t.Fatal(err)
	}
	if len(s.Forgotten()) != 1 {
		t.Fatal("a release with an older time dropped a newer hold")
	}
	if err := s.ReleaseForgotten(map[string]time.Time{"/home/dev/work": at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchUmbrellas([]string{"/home/dev/work"}, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Forgotten()) != 0 || len(s.Umbrellas()) != 1 {
		t.Fatalf("after release: forgotten %v, umbrellas %v", s.Forgotten(), s.Umbrellas())
	}
}

// Touching or releasing nothing does not write the file.
func TestForgottenNoOpsDoNotWrite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TouchForgotten(map[string]time.Time{"/home/dev/work": time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseForgotten(map[string]time.Time{"/home/dev/work": time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("a no-op wrote the file (stat err %v)", err)
	}
}

func TestForgetGroupingRefusesABadPath(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "relative", "/a/b/", "/", "/a/../b"} {
		if _, err := s.ForgetGrouping(p, time.Now()); err == nil {
			t.Errorf("%q was accepted", p)
		}
	}
	if len(s.Forgotten()) != 0 {
		t.Fatalf("forgotten %v", s.Forgotten())
	}
}

// Forgotten folders are cut at load like umbrellas: invalid paths dropped, a
// time in the future counts as now, old entries age out, the newest
// MaxUmbrellas kept.
func TestForgottenIsValidatedAndCappedAtLoad(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	now := time.Now().UTC().Truncate(time.Second)
	f := func(t time.Time) string { return t.Format(time.RFC3339) }
	entries := fmt.Sprintf(`"relative":%q,"/":%q,"/old":%q,"/future":%q`, f(now), f(now), f(now.Add(-UmbrellaRetention-time.Hour)), f(now.Add(50*365*24*time.Hour)))
	for i := range MaxUmbrellas + 5 {
		entries += fmt.Sprintf(`,"/f/%04d":%q`, i, f(now.Add(-time.Duration(i+1)*time.Second)))
	}
	if err := os.WriteFile(file, []byte(`{"version":1,"seen":{},"forgotten":{`+entries+`}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Forgotten()
	if len(got) != MaxUmbrellas {
		t.Fatalf("%d forgotten folders, cap %d", len(got), MaxUmbrellas)
	}
	for _, p := range []string{"relative", "/", "/old"} {
		if _, ok := got[p]; ok {
			t.Errorf("%q was kept", p)
		}
	}
	if at, ok := got["/future"]; !ok || at.After(time.Now()) {
		t.Errorf("a future time: %v %v, want clamped to now", at, ok)
	}
}

// "forgotten" is new and optional, and 0.1.0 opens any file that has it, so
// this build does not refuse a file over it: what cannot be decoded is
// dropped, with one log line that names no path or value, and the rest of the
// file (the seen marks above all) is kept.
func TestUnreadableForgottenIsDroppedNotFatal(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	good := at.Format(time.RFC3339)
	cases := []struct {
		name, forgotten string
		want            []string
	}{
		{"a string", `"/home/dev/work"`, nil},
		{"an array", `["/home/dev/work"]`, nil},
		{"a number", `12`, nil},
		{"a bad time", `{"/home/dev/work":"yesterday"}`, nil},
		{"a mixed map", `{"/home/dev/a":"` + good + `","/home/dev/b":"not a time","/home/dev/c":42,"/home/dev/d":null,"/home/dev/e":{"x":1}}`, []string{"/home/dev/a"}}, // null decodes as no time and ages out at once
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "state.json")
			doc := `{"version":1,"seen":{"claude:s1":"` + good + `"},"catchupMarker":"` + good + `","umbrellas":{"/home/dev/u":"` + good + `"},"forgotten":` + c.forgotten + `}`
			if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			var logs strings.Builder
			log.SetOutput(&logs)
			defer log.SetOutput(os.Stderr)
			s, err := Open(file)
			if err != nil {
				t.Fatalf("the file was refused over its forgotten key: %v", err)
			}
			if _, ok := s.SeenAt("claude:s1"); !ok {
				t.Fatal("the seen marks were lost")
			}
			if len(s.Umbrellas()) != 1 {
				t.Fatalf("umbrellas %v", s.Umbrellas())
			}
			got := slices.Sorted(maps.Keys(s.Forgotten()))
			if !slices.Equal(got, c.want) {
				t.Fatalf("forgotten %v, want %v", got, c.want)
			}
			if strings.Contains(logs.String(), "/home/dev") || strings.Contains(logs.String(), "yesterday") {
				t.Fatalf("the log names a path or a value: %q", logs.String())
			}
			if strings.Count(logs.String(), "\n") > 1 {
				t.Fatalf("more than one log line: %q", logs.String())
			}
		})
	}
}

// The keys 0.1.0 also reads keep their strict behaviour: a bad one still
// refuses the file, so seen marks are never dropped silently.
func TestBadExistingKeysStillRefuseTheFile(t *testing.T) {
	for _, doc := range []string{
		`{"version":1,"seen":"x"}`,
		`{"version":1,"seen":{},"umbrellas":["/home/dev/u"]}`,
	} {
		file := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(file); err == nil {
			t.Errorf("%s opened", doc)
		}
	}
}

// A forget stores a time strictly later than the folder's stored one, even
// when the clock gives the same reading or an earlier one, and says which.
func TestForgetIsAlwaysNewerThanTheStoredHold(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	first, err := s.ForgetGrouping("/home/dev/work", at)
	if err != nil || !first.Equal(at) {
		t.Fatalf("first forget stored %v, %v; want %v", first, err, at)
	}
	for _, clock := range []time.Time{at, at.Add(-10 * time.Minute)} {
		before := s.Forgotten()["/home/dev/work"]
		got, err := s.ForgetGrouping("/home/dev/work", clock)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(before.Add(time.Nanosecond)) || !s.Forgotten()["/home/dev/work"].Equal(got) {
			t.Fatalf("clock %v: stored %v, want the nanosecond after %v", clock, got, before)
		}
	}
	// The nanosecond survives the file.
	again, err := Open(s.(*fileStore).path)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Forgotten()["/home/dev/work"].Equal(at.Add(2 * time.Nanosecond)) {
		t.Fatalf("after reopen %v", again.Forgotten()["/home/dev/work"])
	}
}

// A late touch never writes an older time over a newer stored one.
func TestTouchForgottenKeepsANewerTime(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	if _, err := s.ForgetGrouping("/home/dev/work", at); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchForgotten(map[string]time.Time{"/home/dev/work": at.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if got := s.Forgotten()["/home/dev/work"]; !got.Equal(at) {
		t.Fatalf("an older touch wrote %v over %v", got, at)
	}
	if err := s.TouchForgotten(map[string]time.Time{"/home/dev/work": at.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if got := s.Forgotten()["/home/dev/work"]; !got.Equal(at.Add(time.Minute)) {
		t.Fatalf("a newer touch was not written: %v", got)
	}
}

package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// versionOnDisk reads the version the file was written as.
func versionOnDisk(t *testing.T, file string) int {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	return d.Version
}

func openStore(t *testing.T, file string) Store {
	t.Helper()
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A file with no grouping setting is written as version 1, so 0.1.0 and 0.2.0
// (which refuse anything above 1) still open it. Once a setting is stored the
// file is version 2, so they refuse it instead of dropping the setting; with
// the last setting removed it is version 1 again.
func TestVersionIsTwoOnlyWhileASettingIsStored(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s := openStore(t, file)
	at := time.Now().UTC().Truncate(time.Second)
	if err := s.SetSeen("claude:s1", at); err != nil {
		t.Fatal(err)
	}
	if v := versionOnDisk(t, file); v != 1 {
		t.Fatalf("no setting: version %d, want 1", v)
	}
	if err := s.SetGrouping("/home/dev/broad", GroupingNever, at); err != nil {
		t.Fatal(err)
	}
	if v := versionOnDisk(t, file); v != 2 {
		t.Fatalf("with a setting: version %d, want 2", v)
	}
	// Any later write keeps version 2 while the setting is there.
	if err := s.SetSeen("claude:s2", at); err != nil {
		t.Fatal(err)
	}
	if v := versionOnDisk(t, file); v != 2 {
		t.Fatalf("a later write with a setting: version %d, want 2", v)
	}
	r := openStore(t, file) // this build opens its own version 2
	if got := r.Grouping(); !maps.Equal(got, map[string]string{"/home/dev/broad": GroupingNever}) {
		t.Fatalf("after reopen: %v", got)
	}
	if err := r.SetGrouping("/home/dev/broad", GroupingAuto, at); err != nil {
		t.Fatal(err)
	}
	if v := versionOnDisk(t, file); v != 1 {
		t.Fatalf("last setting removed: version %d, want 1", v)
	}
	if data, _ := os.ReadFile(file); bytes.Contains(data, []byte(`"grouping"`)) {
		t.Fatalf("an empty grouping key was written:\n%s", data)
	}
}

// Never: the folder is no longer remembered nor held as forgotten. Always:
// a forget hold is dropped, a remembered umbrella stays. Back to auto from
// always: the folder is remembered as an umbrella seen then; from never:
// nothing is remembered. Each change is one write and survives a reopen.
func TestSetGrouping(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s := openStore(t, file)
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Hour) // load clamps a future time to now
	a, b, c := "/home/dev/a", "/home/dev/b", "/home/dev/c"
	if err := s.TouchUmbrellas([]string{a, b}, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForgetGrouping(c, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := s.SetGrouping(a, GroupingNever, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGrouping(b, GroupingAlways, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGrouping(c, GroupingAlways, at); err != nil {
		t.Fatal(err)
	}
	// In the store as written, before a reopen (which drops such overlaps
	// on its own).
	if _, ok := s.Umbrellas()[a]; ok {
		t.Fatal("never kept the remembered umbrella in the store")
	}
	if _, ok := s.Forgotten()[c]; ok {
		t.Fatal("always kept the forget hold in the store")
	}
	s = openStore(t, file)
	if got, want := s.Grouping(), map[string]string{a: GroupingNever, b: GroupingAlways, c: GroupingAlways}; !maps.Equal(got, want) {
		t.Fatalf("settings %v, want %v", got, want)
	}
	if _, ok := s.Umbrellas()[a]; ok {
		t.Fatal("a folder set to never is still remembered")
	}
	if _, ok := s.Umbrellas()[b]; !ok {
		t.Fatal("setting always dropped the remembered umbrella")
	}
	if _, ok := s.Forgotten()[c]; ok {
		t.Fatal("setting always kept the forget hold")
	}

	later := at.Add(time.Minute)
	if err := s.SetGrouping(c, GroupingAuto, later); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGrouping(a, GroupingAuto, later); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, file)
	if got := s.Grouping(); !maps.Equal(got, map[string]string{b: GroupingAlways}) {
		t.Fatalf("settings %v", got)
	}
	if got, ok := s.Umbrellas()[c]; !ok || !got.Equal(later) {
		t.Fatalf("always -> auto: remembered %v %v, want %v", got, ok, later)
	}
	if _, ok := s.Umbrellas()[a]; ok {
		t.Fatal("never -> auto remembered the folder")
	}
	if _, ok := s.Forgotten()[a]; ok {
		t.Fatal("never -> auto holds the folder")
	}
}

// A folder set to never is skipped by an umbrella touch (a tick that read
// the umbrella before the setting was written), as a forgotten one is.
func TestTouchUmbrellasSkipsAFolderSetToNever(t *testing.T) {
	s := openStore(t, filepath.Join(t.TempDir(), "state.json"))
	at := time.Now().UTC().Truncate(time.Second)
	if err := s.SetGrouping("/home/dev/a", GroupingNever, at); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchUmbrellas([]string{"/home/dev/a", "/home/dev/b"}, at); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Umbrellas()["/home/dev/a"]; ok {
		t.Fatal("a folder set to never was remembered")
	}
	if _, ok := s.Umbrellas()["/home/dev/b"]; !ok {
		t.Fatal("the other folder was not remembered")
	}
}

// Forgetting a grouping is auto, held: it removes the folder's setting.
func TestForgetGroupingRemovesTheSetting(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s := openStore(t, file)
	at := time.Now().UTC().Truncate(time.Second)
	if err := s.SetGrouping("/home/dev/a", GroupingAlways, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForgetGrouping("/home/dev/a", at); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, file)
	if len(s.Grouping()) != 0 {
		t.Fatalf("settings %v", s.Grouping())
	}
	if _, ok := s.Forgotten()["/home/dev/a"]; !ok {
		t.Fatal("not held")
	}
	if v := versionOnDisk(t, file); v != 1 {
		t.Fatalf("version %d after the last setting was forgotten", v)
	}
}

// Bad input is refused and writes nothing; a no-op writes nothing; a new
// setting past the cap is refused, while changing or removing one is not.
func TestSetGroupingRefusalsAndCap(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s := openStore(t, file)
	at := time.Now().UTC().Truncate(time.Second)
	for _, c := range []struct{ path, mode string }{
		{"relative/a", GroupingNever},
		{"/home/dev/a/", GroupingNever},
		{"/", GroupingNever},
		{"/home/dev/a", "sometimes"},
		{"/home/dev/a", ""},
	} {
		if err := s.SetGrouping(c.path, c.mode, at); err == nil {
			t.Errorf("SetGrouping(%q, %q) accepted", c.path, c.mode)
		}
	}
	if err := s.SetGrouping("/home/dev/a", GroupingAuto, at); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("refusals or a no-op wrote the file")
	}

	for i := range MaxGroupingSettings {
		if err := s.SetGrouping(fmt.Sprintf("/home/dev/f%03d", i), GroupingNever, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetGrouping("/home/dev/one-more", GroupingNever, at); err != ErrTooManySettings {
		t.Fatalf("past the cap: %v", err)
	}
	if err := s.SetGrouping("/home/dev/f000", GroupingAlways, at); err != nil {
		t.Fatalf("changing a setting at the cap: %v", err)
	}
	if err := s.SetGrouping("/home/dev/f001", GroupingAuto, at); err != nil {
		t.Fatalf("removing a setting at the cap: %v", err)
	}
	if err := s.SetGrouping("/home/dev/one-more", GroupingNever, at); err != nil {
		t.Fatalf("below the cap again: %v", err)
	}
	if n := len(openStore(t, file).Grouping()); n != MaxGroupingSettings {
		t.Fatalf("%d settings after reopen", n)
	}
}

// A grouping key this build cannot read is dropped with a log line that
// names no path or value; the rest of the file loads. A folder set to never
// that a hand edit also lists as an umbrella or forgotten is only set.
func TestUnreadableGroupingIsDroppedNotFatal(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	for _, c := range []struct {
		name, grouping string
		want           map[string]string
	}{
		{"not an object", `"never"`, nil},
		{"bad entries", `{"/home/dev/a": "never", "/home/dev/b": "sometimes", "/home/dev/c": 3, "rel/d": "always", "/home/dev/e/": "never"}`, map[string]string{"/home/dev/a": GroupingNever}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var logs bytes.Buffer
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })
			file := filepath.Join(t.TempDir(), "state.json")
			data := fmt.Sprintf(`{"version": 2, "seen": {"claude:s1": %q}, "umbrellas": {"/home/dev/a": %q}, "forgotten": {"/home/dev/a": %q}, "grouping": %s}`, at, at, at, c.grouping)
			if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			s := openStore(t, file)
			if got := s.Grouping(); !maps.Equal(got, c.want) {
				t.Fatalf("settings %v, want %v", got, c.want)
			}
			if _, ok := s.SeenAt("claude:s1"); !ok {
				t.Fatal("seen marks lost")
			}
			if c.want != nil {
				if len(s.Umbrellas())+len(s.Forgotten()) != 0 {
					t.Fatalf("a folder set to never still remembered %v or held %v", s.Umbrellas(), s.Forgotten())
				}
			}
			out := logs.String()
			if !strings.Contains(out, "grouping setting") || strings.Contains(out, "/home/dev") || strings.Contains(out, "sometimes") {
				t.Fatalf("log %q", out)
			}
		})
	}
}

// Past the cap at load (a hand edit), the paths that sort last are dropped.
func TestGroupingCappedAtLoad(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	g := map[string]string{}
	for i := range MaxGroupingSettings + 5 {
		g[fmt.Sprintf("/home/dev/f%03d", i)] = GroupingAlways
	}
	data, err := json.Marshal(map[string]any{"version": 2, "seen": map[string]any{}, "grouping": g})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got := openStore(t, file).Grouping()
	if len(got) != MaxGroupingSettings {
		t.Fatalf("%d settings, want %d", len(got), MaxGroupingSettings)
	}
	if _, ok := got[fmt.Sprintf("/home/dev/f%03d", MaxGroupingSettings)]; ok {
		t.Fatal("a path past the cap was kept")
	}
}

// A store with no setting is version 1 from every start (a missing file, an
// empty file, Memory) and never writes version 2.
func TestNoSettingIsVersion1FromEveryStart(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.json")
	mustWriteFile(t, empty, nil)
	starts := map[string]func() (Store, string){
		"missing file": func() (Store, string) { f := filepath.Join(dir, "missing.json"); return openStore(t, f), f },
		"empty file":   func() (Store, string) { return openStore(t, empty), empty },
		"memory":       func() (Store, string) { return Memory(), "" },
	}
	for name, start := range starts {
		t.Run(name, func(t *testing.T) {
			s, file := start()
			if v := s.(*fileStore).doc.Version; v != 1 {
				t.Fatalf("starts at version %d", v)
			}
			if err := s.SetSeen("claude:s1", time.Now()); err != nil {
				t.Fatal(err)
			}
			if v := s.(*fileStore).doc.Version; v != 1 {
				t.Fatalf("version %d after a write", v)
			}
			if file != "" {
				if v := versionOnDisk(t, file); v != 1 {
					t.Fatalf("wrote version %d", v)
				}
			}
		})
	}
}

// A file from a newer sitboard is refused before its "grouping" or
// "forgotten" is read: nothing about its contents is logged.
func TestANewerFileLogsNothingAboutItsContents(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	file := filepath.Join(t.TempDir(), "state.json")
	mustWriteFile(t, file, []byte(`{"version": 3, "seen": {}, "grouping": {"/home/dev/a": "sometimes"}, "forgotten": 7}`))
	if _, err := Open(file); err == nil || !strings.Contains(err.Error(), "newer sitboard") {
		t.Fatalf("err %v", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("logged %q", logs.String())
	}
}

// After Close, the owner's own changes to grouping are refused with
// ErrClosed and change nothing, in memory or on disk; other writes keep
// #60's behaviour (memory only). A memory-only store has nothing to save
// and still takes them.
func TestGroupingChangesAfterCloseAreRefused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s := openStore(t, file)
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	if err := s.SetGrouping("/home/dev/a", GroupingAlways, at); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := s.SetGrouping("/home/dev/a", GroupingNever, at); !errors.Is(err, ErrClosed) {
		t.Fatalf("SetGrouping after Close: %v", err)
	}
	if _, err := s.ForgetGrouping("/home/dev/b", at); !errors.Is(err, ErrClosed) {
		t.Fatalf("ForgetGrouping after Close: %v", err)
	}
	if got := s.Grouping(); !maps.Equal(got, map[string]string{"/home/dev/a": GroupingAlways}) {
		t.Fatalf("settings in memory %v", got)
	}
	if len(s.Forgotten()) != 0 {
		t.Fatalf("forgotten in memory %v", s.Forgotten())
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(after, before) {
		t.Fatalf("wrote after Close:\n%s", after)
	}

	m := Memory()
	m.Close()
	if err := m.SetGrouping("/home/dev/a", GroupingNever, at); err != nil {
		t.Fatalf("memory store SetGrouping after Close: %v", err)
	}
	if _, err := m.ForgetGrouping("/home/dev/b", at); err != nil {
		t.Fatalf("memory store ForgetGrouping after Close: %v", err)
	}
}

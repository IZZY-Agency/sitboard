package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCapWord(t *testing.T) {
	cases := []struct{ in, want string }{
		{"gpt-6.2-sol", "gpt-6.2-sol"},
		{"", ""},
		{"two words", ""},
		{"tab\tseparated", ""},
		{"new\nline", ""},
		{"nul\x00byte", ""},
		{"del\x7fbyte", ""},
		{strings.Repeat("a", maxWord), strings.Repeat("a", maxWord)},
		{strings.Repeat("a", maxWord+1), ""},
	}
	for _, c := range cases {
		if got := capWord(c.in); got != c.want {
			t.Errorf("capWord(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidFolder(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"/home/dev/projects/alpha", "/home/dev/projects/alpha", true},
		{"/home/dev/../dev/alpha/", "/home/dev/alpha", true},
		{"", "", false},
		{"relative/path", "", false},
		{"./here", "", false},
		{"/home/dev/\x00alpha", "", false},
		{"/" + strings.Repeat("a", 4096), "", false},
	}
	for _, c := range cases {
		got, ok := validFolder(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("validFolder(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseTS(t *testing.T) {
	now := fixedNow
	cases := []struct {
		in   string
		want time.Time
	}{
		{"2026-11-20T11:00:00.000Z", time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)},
		{"2026-11-20T11:00:00Z", time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)},
		{"2026-11-21T11:59:00Z", time.Date(2026, 11, 21, 11, 59, 0, 0, time.UTC)}, // within a day ahead: a clock a little off
		{"2026-11-21T12:01:00Z", time.Time{}},                                     // more than a day ahead
		{"2099-01-01T00:00:00Z", time.Time{}},
		{"", time.Time{}},
		{"yesterday", time.Time{}},
	}
	for _, c := range cases {
		if got := parseTS(c.in, now); !got.Equal(c.want) {
			t.Errorf("parseTS(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// Only plain rollout files are listed: a link in a rollout's place is never
// followed, and a compressed one (.jsonl.zst) is never read.
func TestRolloutsListsOnlyPlainFiles(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-time.Hour))
	dir := filepath.Dir(e.paths[idWorker])
	real := filepath.Join(t.TempDir(), "elsewhere.jsonl")
	if err := os.WriteFile(real, fixture(t, "exec.jsonl"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "rollout-2026-11-20T11-00-00-"+idExec+".jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-11-20T11-00-00-"+idChild+".jsonl.zst"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := e.a.rollouts()
	if len(got) != 1 || got[0].id != idWorker {
		t.Errorf("rollouts = %+v, want only the plain file", got)
	}
}

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestStateFlagsParse(t *testing.T) {
	cfg, err := parseArgs([]string{"--state", "/x/s.json"}, &bytes.Buffer{})
	if err != nil || cfg.statePath != "/x/s.json" || cfg.noState {
		t.Fatalf("cfg %+v, err %v", cfg, err)
	}
	cfg, err = parseArgs([]string{"--no-state"}, &bytes.Buffer{})
	if err != nil || !cfg.noState || cfg.statePath != "" {
		t.Fatalf("cfg %+v, err %v", cfg, err)
	}
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"--state", ""}, "--state needs a file path"},
		{[]string{"--state=  "}, "--state needs a file path"},
		{[]string{"--state", "/x/s.json", "--no-state"}, "either --state or --no-state"},
	} {
		var stderr bytes.Buffer
		if _, err := parseArgs(c.args, &stderr); err == nil || !strings.Contains(stderr.String(), c.says) {
			t.Errorf("%q: err %v, stderr %q", c.args, err, stderr.String())
		}
	}
	var help bytes.Buffer
	_, _ = parseArgs([]string{"--help"}, &help)
	for _, want := range []string{"-state file", "-no-state", "Application Support"} {
		if !strings.Contains(help.String(), want) {
			t.Errorf("--help does not mention %q: %s", want, help.String())
		}
	}
}

func TestStatePathResolvesHomeAndRelative(t *testing.T) {
	home := "/home/dev"
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{
		{"~/s/state.json", "/home/dev/s/state.json"},
		{"~", "/home/dev"},
		{"/a/../b/s.json", "/b/s.json"},
		{" s.json ", filepath.Join(wd, " s.json ")}, // taken as given, spaces included
		{"rel/s.json", filepath.Join(wd, "rel", "s.json")},
	} {
		in, want := c[0], c[1]
		if got, err := statePath(in, home); err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"~other/s.json", "/x/newdir/", "rel/", "~/",
		"/x/new/.", "/x/new/..", "x/..", ".", "..", "~/.", "~/.."} {
		if got, err := statePath(bad, home); err == nil {
			t.Errorf("%q gave %q", bad, got)
		}
	}
}

// testEnv is start's environment, for calls that do not start a board.
func testEnv(home string) runEnv {
	return runEnv{home: home, out: &bytes.Buffer{}, sources: fakeSources, goos: "linux", getenv: func(string) string { return "" }}
}

// realTempDir is t.TempDir with links followed (on macOS /var is a link), as
// the start line names the file after following links.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func defaultStateFolder(home string) string { return filepath.Join(home, ".local", "state") }

func TestStateFlagRunsOnItsOwnFile(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(realTempDir(t), "check", "state.json")
	cfg := cfgWith(0, true)
	cfg.statePath = file
	r := startIn(t, cfg, home)
	if !strings.Contains(r.out.String(), "sitboard: state file "+file+"\n") {
		t.Errorf("no start line for the state file:\n%s", r.out.String())
	}
	r.stop(t)
	// The probe leaves nothing; the lock file stays next to the state file.
	if names := dirNames(t, filepath.Dir(file)); !slices.Equal(names, []string{"state.json.lock"}) {
		t.Errorf("the folder holds %v", names)
	}
	if _, err := os.Stat(defaultStateFolder(home)); !os.IsNotExist(err) {
		t.Errorf("the default state folder was touched: %v", err)
	}

	// What the board saves goes to that file.
	st, _, unlock, err := openState(context.Background(), cfg, testEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := st.SetCatchupMarker(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("no state file at --state: %v", err)
	}
	if _, err := os.Stat(defaultStateFolder(home)); !os.IsNotExist(err) {
		t.Errorf("the default state folder was written: %v", err)
	}
}

func TestStateFlagAtTheDefaultFileSaysSo(t *testing.T) {
	home := realTempDir(t)
	cfg := cfgWith(0, true)
	cfg.statePath = stateFile(home, "linux", func(string) string { return "" })
	r := startIn(t, cfg, home)
	if !strings.Contains(r.out.String(), "sitboard: state file "+cfg.statePath+"\n") {
		t.Errorf("no start line for the default file given with --state:\n%s", r.out.String())
	}
}

func TestDefaultStateFileIsNotNamed(t *testing.T) {
	home := t.TempDir()
	r := startIn(t, cfgWith(0, true), home)
	if strings.Contains(r.out.String(), "state file") {
		t.Errorf("the default file is named:\n%s", r.out.String())
	}
	r.stop(t)
	// The probe creates the folder, as the first write would, and leaves
	// nothing in it; the lock file stays next to the state file.
	dir := filepath.Join(defaultStateFolder(home), "sitboard")
	if names := dirNames(t, dir); !slices.Equal(names, []string{"state.json.lock"}) {
		t.Errorf("the folder holds %v", names)
	}
}

func TestNoStateWritesNothing(t *testing.T) {
	home := t.TempDir()
	cfg := cfgWith(0, true)
	cfg.noState = true
	r := startIn(t, cfg, home)
	if !strings.Contains(r.out.String(), "no state file (--no-state)") {
		t.Errorf("no start line for --no-state:\n%s", r.out.String())
	}
	r.stop(t)

	st, _, unlock, err := openState(context.Background(), cfg, testEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := st.SetSeen("card", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCatchupMarker(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.SeenAt("card"); !ok {
		t.Error("the seen mark is not kept in memory")
	}
	if names := dirNames(t, home); len(names) != 0 {
		t.Errorf("--no-state wrote %v into the home folder", names)
	}
}

func TestStateFlagRefusesAnUnusablePath(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(plain, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(t.TempDir(), "package.json")
	if err := os.WriteFile(pkg, []byte(`{"name":"app","scripts":{"build":"vite build"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct{ path, says string }{
		"a folder":               {t.TempDir(), "is a folder"},
		"a file as a parent":     {filepath.Join(plain, "state.json"), "cannot create the folder"},
		"not a state file":       {plain, "not a valid state file"},
		"another program's JSON": {pkg, "not a sitboard state file"},
	}
	if os.Geteuid() != 0 { // root writes into read-only folders
		ro := t.TempDir()
		if err := os.Chmod(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
		cases["a read-only folder"] = struct{ path, says string }{filepath.Join(ro, "state.json"), "cannot write"}
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if name == "not a state file" {
				if err := os.WriteFile(plain, []byte("not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := cfgWith(0, true)
			cfg.statePath = c.path
			env := testEnv(t.TempDir())
			out := &syncBuffer{}
			env.out = out
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := run(ctx, cfg, env)
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("err %v, want %q", err, c.says)
			}
			if linkRe.MatchString(out.String()) {
				t.Errorf("printed a link before refusing:\n%s", out.String())
			}
		})
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// --state through a link uses the link's target: the start line names it, and
// a write replaces the target, never the link.
func TestStateFlagFollowsLinks(t *testing.T) {
	dir := realTempDir(t)
	target := filepath.Join(dir, "real", "state.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"version":1,"seen":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	cfg := cfgWith(0, true)
	cfg.statePath = link
	r := startIn(t, cfg, t.TempDir())
	if !strings.Contains(r.out.String(), "sitboard: state file "+target+"\n") {
		t.Errorf("the start line does not name the target:\n%s", r.out.String())
	}
	r.stop(t)

	st, _, unlock, err := openState(context.Background(), cfg, testEnv(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := st.SetCatchupMarker(time.Now()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(target); !strings.Contains(string(b), "catchupMarker") {
		t.Errorf("the target was not written: %s", b)
	}

	// A link to a missing file is refused.
	dangling := filepath.Join(dir, "dangling.json")
	if err := os.Symlink(filepath.Join(dir, "missing.json"), dangling); err != nil {
		t.Fatal(err)
	}
	cfg.statePath = dangling
	if _, _, _, err := openState(context.Background(), cfg, testEnv(t.TempDir())); err == nil || !strings.Contains(err.Error(), "link to a missing file") {
		t.Errorf("dangling link: %v", err)
	}
}

package state

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

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

func TestCheckWritableCreatesTheFolderAndLeavesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := CheckWritable(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("folder mode %o, want no group or other bits", perm)
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Errorf("left behind %v", names)
	}
}

func TestCheckWritableKeepsAnExistingFileAsItIs(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "state.json")
	body := []byte(`{"version":1,"seen":{}}`)
	if err := os.WriteFile(file, body, 0o640); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckWritable(file); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() || after.Size() != before.Size() {
		t.Errorf("file changed: %v %v %d -> %v %v %d", before.ModTime(), before.Mode(), before.Size(), after.ModTime(), after.Mode(), after.Size())
	}
	if !os.SameFile(before, after) {
		t.Error("file was replaced")
	}
	if got, _ := os.ReadFile(file); !bytes.Equal(got, body) {
		t.Errorf("contents changed: %s", got)
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{"state.json"}) {
		t.Errorf("folder holds %v", names)
	}
}

func TestCheckWritableRefuses(t *testing.T) {
	t.Run("a folder", func(t *testing.T) {
		dir := t.TempDir()
		err := CheckWritable(dir)
		if err == nil || !strings.Contains(err.Error(), "is a folder") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("a file as the parent", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "plain")
		if err := os.WriteFile(parent, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := CheckWritable(filepath.Join(parent, "state.json")); err == nil {
			t.Fatal("accepted a path under a file")
		}
	})
	t.Run("a read-only folder", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into read-only folders")
		}
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		err := CheckWritable(filepath.Join(dir, "state.json"))
		if err == nil || !strings.Contains(err.Error(), "cannot write") {
			t.Fatalf("err %v", err)
		}
	})
}

func TestMemoryStoreKeepsEverythingAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // a store without a path must not write into the working folder
	s := Memory()
	now := time.Now().UTC()
	if err := s.SetSeen("card", now); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchUmbrellas([]string{"/w/a", "/w/b"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForgetGrouping("/w/b", now); err != nil {
		t.Fatal(err)
	}
	if at, ok := s.SeenAt("card"); !ok || !at.Equal(now) {
		t.Errorf("seen %v %v", at, ok)
	}
	if !s.InMemory() {
		t.Error("a memory store says it is saved")
	}
	if u := s.Umbrellas(); len(u) != 1 {
		t.Errorf("umbrellas %v", u)
	}
	if f := s.Forgotten(); len(f) != 1 {
		t.Errorf("forgotten %v", f)
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Errorf("wrote %v", names)
	}
}

func TestCheckWritablePassesAReadOnlyStateFile(t *testing.T) {
	// The write replaces the file by a rename, so the file's own mode does not
	// matter; a probe that opened the file for writing would fail here.
	file := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(file, []byte(`{"version":1,"seen":{}}`), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := CheckWritable(file); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o400 {
		t.Errorf("mode changed to %o", fi.Mode().Perm())
	}
}

func TestCheckWritableRemovesTheFoldersItMadeWhenItFails(t *testing.T) {
	old := createTemp
	createTemp = func(string, string) (*os.File, error) { return nil, os.ErrPermission }
	t.Cleanup(func() { createTemp = old })
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "kept"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckWritable(filepath.Join(base, "kept", "a", "b", "state.json")); err == nil {
		t.Fatal("accepted")
	}
	if names := dirNames(t, filepath.Join(base, "kept")); len(names) != 0 {
		t.Errorf("left %v", names)
	}
}

func TestResolve(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(real, "state.json")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mustLink := func(target, link string) string {
		t.Helper()
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		return link
	}
	fileLink := mustLink(file, filepath.Join(dir, "file-link.json"))
	chain := mustLink(fileLink, filepath.Join(dir, "chain.json"))
	dirLink := mustLink(real, filepath.Join(dir, "dir-link"))
	for _, c := range [][2]string{
		{file, file},
		{fileLink, file},
		{chain, file},
		{filepath.Join(dirLink, "state.json"), file},
		{filepath.Join(dirLink, "new", "deeper", "state.json"), filepath.Join(real, "new", "deeper", "state.json")},
		{filepath.Join(dir, "absent", "state.json"), filepath.Join(dir, "absent", "state.json")},
		{dirLink, real}, // a link to a folder: CheckWritable then refuses it
	} {
		if got, err := Resolve(c[0]); err != nil || got != c[1] {
			t.Errorf("%s: %q %v, want %q", c[0], got, err, c[1])
		}
	}
	dangling := mustLink(filepath.Join(dir, "missing.json"), filepath.Join(dir, "dangling.json"))
	if got, err := Resolve(dangling); err == nil {
		t.Errorf("dangling link resolved to %q", got)
	}
}

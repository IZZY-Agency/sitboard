package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openWatched opens file with its warnings collected.
func openWatched(t *testing.T, file string) (*fileStore, *[]string) {
	t.Helper()
	st, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	s := st.(*fileStore)
	var warnings []string
	s.warnf = func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }
	return s, &warnings
}

func TestOwnWritesNeverWarn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, warnings := openWatched(t, file) // no file yet: the first write creates it
	for i := range 20 {
		if err := s.SetSeen(fmt.Sprint("card", i), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// Reopened from an existing file: the first write does not warn either.
	again, warnings2 := openWatched(t, file)
	if err := again.SetSeen("card", time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(*warnings)+len(*warnings2) != 0 {
		t.Errorf("own writes warned: %q %q", *warnings, *warnings2)
	}
}

func TestForeignChangeWarnsOnceAndIsOverwritten(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, warnings := openWatched(t, file)
	if err := s.SetSeen("mine", time.Now()); err != nil {
		t.Fatal(err)
	}
	other, err := Open(file) // a second program on the same file
	if err != nil {
		t.Fatal(err)
	}
	if err := other.SetSeen("theirs", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeen("mine2", time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(*warnings) != 1 || !strings.Contains((*warnings)[0], "another program changed the state file") {
		t.Fatalf("warnings %q", *warnings)
	}
	if strings.Contains((*warnings)[0], file) {
		t.Errorf("the warning names the path: %q", (*warnings)[0])
	}
	// Written as today: this store's document, the other's mark is gone.
	reread, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reread.SeenAt("theirs"); ok {
		t.Error("merged the other program's mark")
	}
	if _, ok := reread.SeenAt("mine2"); !ok {
		t.Error("this store's write is missing")
	}
	// Once per run: more foreign changes do not warn again.
	for i := range 3 {
		if err := other.SetSeen(fmt.Sprint("theirs", i), time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSeen(fmt.Sprint("mine", i+3), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if len(*warnings) != 1 {
		t.Errorf("warned %d times: %q", len(*warnings), *warnings)
	}
}

func TestForeignChangeKinds(t *testing.T) {
	body := []byte(`{"version":1,"seen":{}}`)
	cases := map[string]struct {
		before func(t *testing.T, file string) // before Open
		change func(t *testing.T, file string) // between Open and the write
	}{
		"created by another program": {
			change: func(t *testing.T, file string) { mustWriteFile(t, file, body) },
		},
		"removed by another program": {
			before: func(t *testing.T, file string) { mustWriteFile(t, file, body) },
			change: func(t *testing.T, file string) {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			},
		},
		"rewritten in place, same size": {
			before: func(t *testing.T, file string) { mustWriteFile(t, file, body) },
			change: func(t *testing.T, file string) {
				f, err := os.OpenFile(file, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = f.WriteAt([]byte(`{"version":1,"seen":{} }`[:len(body)]), 0)
				_ = f.Close()
				later := time.Now().Add(time.Second)
				_ = os.Chtimes(file, later, later)
			},
		},
		"rewritten in place, other size, same time": {
			before: func(t *testing.T, file string) { mustWriteFile(t, file, body) },
			change: func(t *testing.T, file string) {
				fi, err := os.Stat(file)
				if err != nil {
					t.Fatal(err)
				}
				f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = f.WriteString("\n")
				_ = f.Close()
				if err := os.Chtimes(file, fi.ModTime(), fi.ModTime()); err != nil {
					t.Fatal(err)
				}
			},
		},
		"replaced by a rename": {
			before: func(t *testing.T, file string) { mustWriteFile(t, file, body) },
			change: func(t *testing.T, file string) {
				tmp := file + ".other"
				mustWriteFile(t, tmp, body)
				// Same size and time as the file it replaces: only the file
				// itself (device and inode) differs.
				fi, err := os.Stat(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(tmp, fi.ModTime(), fi.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmp, file); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "state.json")
			if c.before != nil {
				c.before(t, file)
			}
			s, warnings := openWatched(t, file)
			c.change(t, file)
			if err := s.SetSeen("card", time.Now()); err != nil {
				t.Fatal(err)
			}
			if len(*warnings) != 1 {
				t.Errorf("warnings %q", *warnings)
			}
		})
	}
}

func TestMemoryStoreNeverChecks(t *testing.T) {
	s := Memory().(*fileStore)
	warned := false
	s.warnf = func(string, ...any) { warned = true }
	if err := s.SetSeen("card", time.Now()); err != nil || warned {
		t.Fatalf("err %v, warned %v", err, warned)
	}
}

func TestCloseStopsSaving(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, warnings := openWatched(t, file)
	if err := s.SetSeen("before", time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := s.SetSeen("after", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.SeenAt("after"); !ok {
		t.Error("a change after Close is not kept in memory")
	}
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Errorf("wrote after Close:\n%s", after)
	}
	if names := dirNames(t, filepath.Dir(file)); len(names) != 1 {
		t.Errorf("folder holds %v", names)
	}
	if len(*warnings) != 0 {
		t.Errorf("warned: %q", *warnings)
	}
}

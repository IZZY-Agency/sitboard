package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A state file written by sitboard 0.1.0 itself (its own state package wrote
// testdata/state-0.1.0.json) opens, with its marks.
func TestA010FileOpens(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "state-0.1.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "state.json")
	mustWriteFile(t, file, data)
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	s, err := OpenAt(file, func() time.Time { return at.Add(24 * time.Hour) }) // a day after it was written
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s.SeenAt("claude:11111111-2222-3333-4444-555555555555"); !ok || !got.Equal(at) {
		t.Errorf("seen %v %v", got, ok)
	}
	if b, _ := os.ReadFile(file); strings.Contains(string(b), "catchupMarker") {
		t.Errorf("the old marker was kept: %s", b)
	}
}

// A state file written by sitboard 0.2.0 itself (its own state package wrote
// testdata/state-0.2.0.json, with a remembered and a forgotten grouping)
// opens, with all it holds. It is opened at a fixed time a day after it was
// written, so the 30-day pruning of groupings never depends on today.
func TestA020FileOpens(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "state-0.2.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "state.json")
	mustWriteFile(t, file, data)
	at := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	s, err := openAt(file, func() time.Time { return at.Add(24 * time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s.SeenAt("claude:11111111-2222-3333-4444-555555555555"); !ok || !got.Equal(at) {
		t.Errorf("seen %v %v", got, ok)
	}
	if b, _ := os.ReadFile(file); strings.Contains(string(b), "catchupMarker") {
		t.Errorf("the old marker was kept: %s", b)
	}
	if got := s.Umbrellas(); len(got) != 1 || !got["/home/dev/work"].Equal(at) {
		t.Errorf("umbrellas %v", got)
	}
	if got := s.Forgotten(); len(got) != 1 || !got["/home/dev/side"].Equal(at.Add(time.Minute)) {
		t.Errorf("forgotten %v", got)
	}
	// Opened 31 days later, both groupings have aged out.
	late, err := openAt(file, func() time.Time { return at.Add(31 * 24 * time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	if u, f := late.Umbrellas(), late.Forgotten(); len(u) != 0 || len(f) != 0 {
		t.Errorf("31 days later: umbrellas %v, forgotten %v", u, f)
	}
}

// Only a sitboard state document opens: other JSON would be replaced by the
// first write. The file is left as it was.
func TestOnlySitboardStateFilesOpen(t *testing.T) {
	for name, c := range map[string]struct {
		data string
		says string // "" opens
	}{
		"a package.json":        {`{"name":"app","scripts":{"build":"vite build"}}`, "it has no version"},
		"a settings file":       {`{"editor.fontSize":14}`, "it has no version"},
		"null":                  {`null`, "it has no version"},
		"a null version":        {`{"version":null,"seen":{}}`, "it has no version"},
		"version 0":             {`{"version":0,"seen":{}}`, "version 0 is not one"},
		"a negative version":    {`{"version":-1,"seen":{}}`, "version -1 is not one"},
		"a string version":      {`{"version":"1","seen":{}}`, "its version is not a number"},
		"a bool version":        {`{"version":true,"seen":{}}`, "its version is not a number"},
		"an object version":     {`{"version":{"major":1},"seen":{}}`, "its version is not a number"},
		"a fractional version":  {`{"version":1.5,"seen":{}}`, "version 1.5 is not written as a whole number"},
		"1.0":                   {`{"version":1.0,"seen":{}}`, "version 1.0 is not written as a whole number"},
		"1e0":                   {`{"version":1e0,"seen":{}}`, "version 1e0 is not written as a whole number"},
		"a huge version":        {`{"version":99999999999999999999,"seen":{}}`, "newer sitboard (file version 99999999999999999999,"},
		"a huge negative":       {`{"version":-99999999999999999999,"seen":{}}`, "version -99999999999999999999 is not one"},
		"an array":              {`[1]`, "not a valid state file"},
		"a newer version":       {`{"version":5,"seen":{}}`, "newer sitboard (file version 5,"},
		"version 2":             {`{"version":2,"seen":{},"grouping":{"/home/dev/broad":"never"}}`, ""},
		"version 2, no setting": {`{"version":2,"seen":{}}`, ""},
		"version 1":             {`{"version":1,"seen":{}}`, ""},
		"version 1, no seen":    {`{"version":1}`, ""},
		"unknown extra keys":    {`{"version":1,"seen":{},"pins":["x"],"later":{"a":1}}`, ""},
		"empty":                 {``, ""},
		"only spaces":           {" \n\t ", ""},
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "state.json")
			mustWriteFile(t, file, []byte(c.data))
			s, err := Open(file)
			if c.says == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if err := s.SetSeen("card", time.Now()); err != nil { // and it can be written
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("err %v, want %q", err, c.says)
			}
			if after, _ := os.ReadFile(file); string(after) != c.data {
				t.Errorf("refusing changed the file: %q", after)
			}
		})
	}
}

func TestCheckWritableRefusesANamedPipe(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	if err := mkfifo(file); err != nil {
		t.Skip("no named pipes here:", err)
	}
	if err := CheckWritable(file); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err %v", err)
	}
}

func TestResolveSaysALoopIsALoop(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(a); err == nil || !strings.Contains(err.Error(), "loop of links") {
		t.Fatalf("err %v", err)
	}
}

// A link in a folder part of the path that leads nowhere is called what it
// is, and nothing is created.
func TestResolveNamesABrokenFolderLink(t *testing.T) {
	for name, c := range map[string]struct {
		make func(dir, link string) error
		says string
	}{
		"to a missing folder": {func(dir, link string) error { return os.Symlink(filepath.Join(dir, "gone"), link) }, "is a link to a missing folder"},
		"to a folder under a file": {func(dir, link string) error {
			if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o600); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(dir, "file", "sub"), link)
		}, "is a link to a missing folder"},
		"a loop": {func(dir, link string) error {
			other := filepath.Join(dir, "other")
			if err := os.Symlink(link, other); err != nil {
				return err
			}
			return os.Symlink(other, link)
		}, "is a loop of links"},
		"into a folder it cannot enter": {func(dir, link string) error {
			closed := filepath.Join(dir, "closed")
			if err := os.MkdirAll(filepath.Join(closed, "sub"), 0o700); err != nil {
				return err
			}
			if err := os.Chmod(closed, 0o000); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(closed, "sub"), link)
		}, "is a link into a folder sitboard has no permission to enter"},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "into a folder it cannot enter" && os.Geteuid() == 0 {
				t.Skip("root enters any folder")
			}
			dir := t.TempDir()
			// Runs before TempDir's removal: a folder left at mode 000 cannot go.
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "closed"), 0o700) })
			link := filepath.Join(dir, "link")
			if err := c.make(dir, link); err != nil {
				t.Fatal(err)
			}
			for _, file := range []string{
				filepath.Join(link, "state.json"),
				filepath.Join(link, "deeper", "state.json"),
			} {
				_, err := Resolve(file)
				if err == nil || !strings.Contains(err.Error(), c.says) || !strings.Contains(err.Error(), "folder "+link+" ") {
					t.Fatalf("%s: err %v, want %q about %s", file, err, c.says, link)
				}
			}
			if _, err := os.Lstat(filepath.Join(dir, "gone")); !os.IsNotExist(err) {
				t.Errorf("a folder was created: %v", err)
			}
		})
	}
}

// A folder part that is a link to a folder resolves to it.
func TestResolveFollowsAFolderLink(t *testing.T) {
	dir := t.TempDir()
	real, link := filepath.Join(dir, "real"), filepath.Join(dir, "link")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(filepath.Join(link, "new", "state.json"))
	want, _ := filepath.EvalSymlinks(real)
	if err != nil || got != filepath.Join(want, "new", "state.json") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func mustWriteFile(t *testing.T, file string, body []byte) {
	t.Helper()
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

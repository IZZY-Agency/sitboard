package lsofname

import (
	"slices"
	"testing"
)

// lsof prints the bytes of a non-ASCII letter as \xHH in the C locale;
// Unescape reads every \ escape as one.
func TestUnescape(t *testing.T) {
	for in, want := range map[string]string{
		`/home/dev/caf\xc3\xa9`: "/home/dev/café",
		`/home/dev/\xd0\xbf\xd1\x80\xd0\xb8\xd0\xb2\xd0\xb5\xd1\x82 dir\x`: `/home/dev/привет dir\x`,
		`/a\tb\nc\\d`:  "/a\tb\nc\\d",
		`/plain^Aname`: `/plain^Aname`,
		`/no escape`:   `/no escape`,
	} {
		if got := Unescape(in); got != want {
			t.Errorf("Unescape(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every escape may be lsof's or the name's own characters (a backslash or a
// caret is printed as it is); only readings that exist are kept, and they
// are dropped on the way, not after every combination is made.
func TestResolve(t *testing.T) {
	fs := map[string][]string{
		"/":  {"w", "x"},
		"/w": {"\xc3\xa9^A", "\xc3\xa9\x01", "\xc3\xa9z", "\xc3\xa9\xc3\xa9"},
	}
	var asked []string
	entries := func(dir string) []string { asked = append(asked, dir); return fs[dir] }
	got, ok := Resolve(`/w/\xc3\xa9^A`, 8, entries)
	if want := []string{"/w/\xc3\xa9\x01", "/w/\xc3\xa9^A"}; !ok || !slices.Equal(got, want) {
		t.Errorf("readings %q (ok %v)", got, ok)
	}
	if len(asked) != 2 {
		t.Errorf("each folder listed once, got %q", asked)
	}
	// Six escapes, one reading on disk: the others go on the way, so the
	// cap counts only readings still alive.
	fs["/w"] = append(fs["/w"], "\xe6\x97\xa5\xe6\x9c\xac")
	if got, ok := Resolve(`/w/\xe6\x97\xa5\xe6\x9c\xac`, 2, entries); !ok || !slices.Equal(got, []string{"/w/\xe6\x97\xa5\xe6\x9c\xac"}) {
		t.Errorf("six escapes: %q %v", got, ok)
	}
	// "é" only starts names in /w: no such folder.
	if got, ok := Resolve(`/w/\xc3\xa9`, 8, entries); !ok || len(got) != 0 {
		t.Errorf("a name's start: %q %v", got, ok)
	}
	if got, ok := Resolve(`/q/\xc3\xa9`, 2, entries); !ok || len(got) != 0 {
		t.Errorf("missing folder: %q %v", got, ok)
	}
	if got, ok := Resolve("/plain", 1, func(string) []string { t.Error("a plain name is not looked up"); return nil }); !ok || !slices.Equal(got, []string{"/plain"}) {
		t.Errorf("plain: %q %v", got, ok)
	}
	fs["/x"] = []string{"\x01\x02", "\x01\\x02", "\\x01\x02", "\\x01\\x02"}
	if _, ok := Resolve(`/x/\x01\x02`, 3, entries); ok {
		t.Error("four readings alive fit in three")
	}
}

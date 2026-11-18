package core

import (
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IZZY-Agency/sitboard/internal/buildinfo"
	"github.com/IZZY-Agency/sitboard/internal/contract"
)

// The snapshot carries the build: version and commit as the linker set them
// in buildinfo, and the platform it runs on. The board reads them once, in
// New, so nothing it runs later reads package state: the values set here are
// put back only after newBoard has closed the board and waited for it.
func TestSnapshotCarriesBuild(t *testing.T) {
	v, c := buildinfo.Version, buildinfo.Commit
	t.Cleanup(func() { buildinfo.Version, buildinfo.Commit = v, c })
	buildinfo.Version, buildinfo.Commit = "v9.8.7", "fedcba9"
	b := newTestBoard(t)
	buildinfo.Version, buildinfo.Commit = "changed later", "later"
	got := b.Snapshot().Build
	want := contract.Build{Version: "v9.8.7", Commit: "fedcba9", OS: runtime.GOOS, Arch: runtime.GOARCH}
	if got != want {
		t.Fatalf("build %+v, want %+v", got, want)
	}
}

// A custom build can set anything through linker flags: version and commit
// are cleaned like other strings and capped.
func TestBuildStringsAreCleanedAndCapped(t *testing.T) {
	b := buildOf("v1\x1b[31m\u202e"+strings.Repeat("9", 500), "c\x00o\nm")
	if strings.ContainsAny(b.Version, "\x1b\u202e") || strings.ContainsAny(b.Commit, "\x00\n") {
		t.Fatalf("not cleaned: %q %q", b.Version, b.Commit)
	}
	if n := utf8.RuneCountInString(b.Version); n > maxBuildText+1 {
		t.Fatalf("version is %d runes, cap %d", n, maxBuildText)
	}
	if b.Commit != "co m" {
		t.Fatalf("commit %q", b.Commit)
	}

	// Multi-byte text is cut by characters, never inside one.
	v := buildOf(strings.Repeat("é日", 100), "c").Version
	if !utf8.ValidString(v) || utf8.RuneCountInString(v) != maxBuildText+1 || !strings.HasSuffix(v, "…") {
		t.Fatalf("capped multi-byte version %q (%d runes)", v, utf8.RuneCountInString(v))
	}
}

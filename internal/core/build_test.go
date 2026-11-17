package core

import (
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IZZY-Agency/sitboard/internal/buildinfo"
	"github.com/IZZY-Agency/sitboard/internal/contract"
)

// The snapshot carries the build: version and commit as buildinfo has them,
// and the platform it runs on.
func TestSnapshotCarriesBuild(t *testing.T) {
	setBuild(t, "v1.2.3", "abc1234")
	got := newTestBoard(t).Snapshot().Build
	want := contract.Build{Version: "v1.2.3", Commit: "abc1234", OS: runtime.GOOS, Arch: runtime.GOARCH}
	if got != want {
		t.Fatalf("build %+v, want %+v", got, want)
	}
}

// A custom build can set anything through linker flags: version and commit
// are cleaned like other strings and capped.
func TestBuildStringsAreCleanedAndCapped(t *testing.T) {
	setBuild(t, "v1\x1b[31m\u202e"+strings.Repeat("9", 500), "c\x00o\nm")
	b := newTestBoard(t).Snapshot().Build
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
	setBuild(t, strings.Repeat("é日", 100), "c")
	v := newTestBoard(t).Snapshot().Build.Version
	if !utf8.ValidString(v) || utf8.RuneCountInString(v) != maxBuildText+1 || !strings.HasSuffix(v, "…") {
		t.Fatalf("capped multi-byte version %q (%d runes)", v, utf8.RuneCountInString(v))
	}
}

func setBuild(t *testing.T, version, commit string) {
	t.Helper()
	v, c := buildinfo.Version, buildinfo.Commit
	buildinfo.Version, buildinfo.Commit = version, commit
	t.Cleanup(func() { buildinfo.Version, buildinfo.Commit = v, c })
}

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func TestDetectSPDX(t *testing.T) {
	for file, want := range map[string]string{
		"testdata/mit.txt":                             "MIT",
		"testdata/bsd-2-clause.txt":                    "BSD-2-Clause",
		"testdata/bsd-3-clause.txt":                    "BSD-3-Clause",
		"testdata/isc.txt":                             "ISC",
		"testdata/apache-2.0.txt":                      "Apache-2.0",
		"../../../web/src/fonts/JetBrainsMono-OFL.txt": "OFL-1.1",
	} {
		got, err := detectSPDX(read(t, file))
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %s", file, got, err, want)
		}
	}
	// Not allowed: an advertising clause (BSD-4-Clause), the JSON licence's
	// "Good, not Evil", a GPL text with an MIT grant, and nothing at all.
	for _, file := range []string{"testdata/bsd-4-clause.txt", "testdata/json.txt", "testdata/gpl-plus-mit.txt"} {
		if got, err := detectSPDX(read(t, file)); err == nil {
			t.Errorf("%s was accepted as %s", file, got)
		}
	}
	if got, err := detectSPDX("All rights reserved.\n"); err == nil {
		t.Errorf("a text with no licence was accepted as %s", got)
	}
	// A licence that is only part of the text is not enough.
	partial := read(t, "testdata/mit.txt") + strings.Repeat("Some other terms apply to this file in full.\n", 40)
	if got, err := detectSPDX(partial); err == nil {
		t.Errorf("a text that is mostly not a licence was accepted as %s", got)
	}
}

func TestApacheNoticeIsAppended(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "LICENSE"), read(t, "testdata/apache-2.0.txt"))
	write(t, filepath.Join(dir, "NOTICE"), "Example Product\nCopyright 2020 Example Author\n")
	text, id, err := licensed(dir)
	if err != nil || id != "Apache-2.0" || !strings.Contains(text, "--- NOTICE ---") || !strings.HasSuffix(text, "Copyright 2020 Example Author\n") {
		t.Fatalf("id %s, err %v, text ends %q", id, err, text[max(0, len(text)-120):])
	}
	// Under another licence a NOTICE file is not part of the licence text.
	mit := t.TempDir()
	write(t, filepath.Join(mit, "LICENSE"), read(t, "testdata/mit.txt"))
	write(t, filepath.Join(mit, "NOTICE"), "unrelated\n")
	if text, _, err := licensed(mit); err != nil || strings.Contains(text, "unrelated") {
		t.Errorf("MIT with a NOTICE file: %v %q", err, text)
	}
}

func TestModuleURL(t *testing.T) {
	for path, want := range map[string]string{
		"github.com/fsnotify/fsnotify": "https://github.com/fsnotify/fsnotify",
		"golang.org/x/sys":             "https://go.googlesource.com/sys",
		"example.com/a/b":              "https://pkg.go.dev/example.com/a/b",
	} {
		if got := moduleURL(path); got != want {
			t.Errorf("moduleURL(%s) = %s, want %s", path, got, want)
		}
	}
}

func TestRepoURL(t *testing.T) {
	for in, want := range map[string]string{
		`"git+https://github.com/sveltejs/svelte.git"`:                        "https://github.com/sveltejs/svelte",
		`{"type":"git","url":"https://github.com/a/b.git","directory":"pkg"}`: "https://github.com/a/b",
		`"github:a/b"`: "https://github.com/a/b",
	} {
		got, err := repoURL([]byte(in))
		if err != nil || got != want {
			t.Errorf("repoURL(%s) = %q, %v; want %s", in, got, err, want)
		}
	}
}

// fakeWeb is a web folder with one npm package (MIT) and one pinned font, and
// a bundle folder whose manifest lists the given modules and assets.
type fakeWeb struct {
	t           *testing.T
	web, bundle string
	table       []font
}

func newFakeWeb(t *testing.T) *fakeWeb {
	root := t.TempDir()
	f := &fakeWeb{t: t, web: filepath.Join(root, "web"), bundle: filepath.Join(root, "out")}
	write(t, filepath.Join(f.web, "node_modules/svelte/package.json"),
		`{"name":"svelte","version":"5.1.2","license":"MIT","repository":{"type":"git","url":"git+https://github.com/sveltejs/svelte.git"}}`)
	write(t, filepath.Join(f.web, "node_modules/svelte/LICENSE.md"), read(t, "testdata/mit.txt"))
	write(t, filepath.Join(f.web, "src/fonts/Mono-OFL.txt"), read(t, "../../../web/src/fonts/JetBrainsMono-OFL.txt"))
	f.table = []font{{Name: "Mono", Version: "1.0", URL: "https://example.com/mono", Licence: "src/fonts/Mono-OFL.txt",
		Files: map[string]string{"mono-latin.woff2": digest("wOF2 mono")}}}
	write(t, filepath.Join(f.bundle, "bundle/assets/index-abc.css"), "body{font-family:Mono}")
	write(t, filepath.Join(f.bundle, "bundle/assets/index-abc.js"), "console.log(1)")
	write(t, filepath.Join(f.bundle, "bundle/assets/mono-latin-AbC_12-x.woff2"), "wOF2 mono")
	return f
}

type asset struct {
	FileName string   `json:"fileName"`
	Sources  []string `json:"sources"`
}

func (f *fakeWeb) manifest(modules []string, assets []asset) {
	abs, _ := filepath.Abs(f.web)
	for i, m := range modules {
		modules[i] = strings.ReplaceAll(m, "WEB", filepath.ToSlash(abs))
	}
	b, _ := json.Marshal(map[string]any{"modules": modules, "assets": assets})
	write(f.t, filepath.Join(f.bundle, "modules.json"), string(b))
}

func (f *fakeWeb) entries() ([]entry, error) { return bundleEntries(f.bundle, f.web, f.table) }

var monoAsset = asset{FileName: "assets/mono-latin-AbC_12-x.woff2", Sources: []string{"src/fonts/mono-latin.woff2"}}

func TestBundleEntries(t *testing.T) {
	f := newFakeWeb(t)
	f.manifest([]string{"WEB/index.html", "WEB/src/main.ts", "WEB/src/App.svelte?svelte&type=style&lang.css",
		"WEB/node_modules/svelte/src/a.js", "WEB/node_modules/svelte/src/b.js"}, []asset{monoAsset})
	es, err := f.entries()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]entry{}
	for _, e := range es {
		got[e.Name] = e
	}
	if s := got["svelte"]; s.Version != "5.1.2" || s.SPDX != "MIT" || s.URL != "https://github.com/sveltejs/svelte" {
		t.Errorf("svelte entry %+v", s)
	}
	if m := got["Mono"]; m.SPDX != "OFL-1.1" || m.Version != "1.0" || len(es) != 2 {
		t.Errorf("entries %+v", es)
	}
}

func TestBundleEntriesRefuse(t *testing.T) {
	cases := map[string]struct {
		modules []string
		assets  []asset
		edit    func(f *fakeWeb)
		want    string
	}{
		"a virtual module (Vite's modulepreload polyfill)": {
			modules: []string{"WEB/src/main.ts", "\x00vite/modulepreload-polyfill.js"}, want: "neither its own source",
		},
		"a module outside web/src and node_modules": {
			modules: []string{"WEB/src/main.ts", "/elsewhere/lib.js"}, want: "neither under web/src",
		},
		"a font inlined into the CSS": {
			modules: []string{"WEB/src/main.ts"},
			edit: func(f *fakeWeb) {
				write(f.t, filepath.Join(f.bundle, "bundle/assets/index-abc.css"), "@font-face{src:url(data:font/woff;base64,AAAA)}")
			},
			want: "inlines a font",
		},
		"a font the table does not know": {
			modules: []string{"WEB/src/main.ts"},
			assets:  []asset{{FileName: "assets/serif-ZZZZZZZZ.woff", Sources: []string{"src/fonts/serif.woff"}}},
			edit: func(f *fakeWeb) {
				write(f.t, filepath.Join(f.bundle, "bundle/assets/serif-ZZZZZZZZ.woff"), "wOFF")
			},
			want: "serif.woff in the web bundle has no entry",
		},
		"a changed font": {
			modules: []string{"WEB/src/main.ts"}, assets: []asset{monoAsset},
			edit: func(f *fakeWeb) {
				write(f.t, filepath.Join(f.bundle, "bundle/assets/mono-latin-AbC_12-x.woff2"), "wOF2 changed")
			},
			want: "not the pinned Mono",
		},
		"an npm package without a licence": {
			modules: []string{"WEB/node_modules/nolicence/index.css"},
			edit: func(f *fakeWeb) {
				write(f.t, filepath.Join(f.web, "node_modules/nolicence/package.json"), `{"name":"nolicence","version":"1.0.0","license":"MIT","repository":"github:a/b"}`)
			},
			want: "want exactly one licence file",
		},
	}
	for name, c := range cases {
		f := newFakeWeb(t)
		f.manifest(c.modules, c.assets)
		if c.edit != nil {
			c.edit(f)
		}
		if _, err := f.entries(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", name, err, c.want)
		}
	}
}

func TestPackageWithOnlyAStylesheetAndAFont(t *testing.T) {
	f := newFakeWeb(t)
	write(t, filepath.Join(f.web, "node_modules/fakepkg/package.json"), `{"name":"fakepkg","version":"1.0.0","license":"MIT","repository":"github:example/fakepkg"}`)
	write(t, filepath.Join(f.web, "node_modules/fakepkg/LICENSE"), read(t, "testdata/mit.txt"))
	write(t, filepath.Join(f.bundle, "bundle/assets/font-ABCDEFGH.woff"), "wOFF")
	f.manifest([]string{"WEB/src/main.ts", "WEB/node_modules/fakepkg/style.css"},
		[]asset{{FileName: "assets/font-ABCDEFGH.woff", Sources: []string{"node_modules/fakepkg/font.woff"}}})
	es, err := f.entries()
	if err != nil || len(es) != 1 || es[0].Name != "fakepkg" {
		t.Fatalf("entries %+v, err %v", es, err)
	}
}

func TestGoLicenseIsFoundAndPinned(t *testing.T) {
	text := read(t, "testdata/bsd-3-clause.txt")
	pinned := digest(text)
	plain := t.TempDir()
	write(t, filepath.Join(plain, "LICENSE"), text)
	if got, err := goLicense(plain, pinned); err != nil || got != text {
		t.Errorf("GOROOT/LICENSE: %v", err)
	}
	brew := t.TempDir()
	write(t, filepath.Join(brew, "LICENSE"), text)
	if got, err := goLicense(filepath.Join(brew, "libexec"), pinned); err != nil || got != text {
		t.Errorf("Homebrew layout: %v", err)
	}
	other := t.TempDir()
	write(t, filepath.Join(other, "LICENSE"), read(t, "testdata/mit.txt"))
	if _, err := goLicense(filepath.Join(other, "libexec"), pinned); err == nil {
		t.Error("a LICENSE that is not Go's was accepted")
	}
}

const fdlibmSource = `// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package math

// The original C code is from FreeBSD and came with this notice.
//
// ====================================================
// Copyright (C) 1993 by Example Systems, Inc. All rights reserved.
//
// Permission to use, copy, modify, and distribute this
// software is freely granted, provided that this notice
// is preserved.
// ====================================================
//
// The method:
//	compute it.
func F() {}

// Example Library Release 2.8
// Copyright 1984, 2000 by Example Author
//
// May be used freely.

var x = 1
`

func TestNoticesIn(t *testing.T) {
	got := noticesIn(fdlibmSource)
	want := []string{
		"Copyright (C) 1993 by Example Systems, Inc. All rights reserved.\n\nPermission to use, copy, modify, and distribute this\nsoftware is freely granted, provided that this notice\nis preserved.\n",
		"Example Library Release 2.8\nCopyright 1984, 2000 by Example Author\n\nMay be used freely.\n",
	}
	if len(got) != len(want) {
		t.Fatalf("notices %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("notice %d:\n%q\nwant\n%q", i, got[i], want[i])
		}
	}
}

func TestStdNoticesArePinned(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "math/a.go"), fdlibmSource)
	write(t, filepath.Join(src, "math/b.go"), fdlibmSource)
	files := []string{"math/a.go", "math/b.go"}
	pins := map[string]bool{}
	for _, n := range noticesIn(fdlibmSource) {
		pins[digest(n)] = true
	}
	es, err := stdNotices(src, files, pins)
	if err != nil || len(es) != 2 || es[0].Name != "math/a.go, math/b.go" {
		t.Fatalf("entries %+v, err %v", es, err)
	}
	// A changed notice, or a pinned one that disappeared, stops it.
	write(t, filepath.Join(src, "math/b.go"), strings.Replace(fdlibmSource, "May be used freely.", "May be used.", 1))
	if _, err := stdNotices(src, files, pins); err == nil || !strings.Contains(err.Error(), "update goNoticeSHA256") {
		t.Errorf("a changed notice was accepted: %v", err)
	}
	if _, err := stdNotices(src, []string{"math/a.go"}, map[string]bool{"0000": true}); err == nil {
		t.Error("unpinned notices were accepted")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	es := []entry{
		{Group: 3, Name: "pkg", Version: "2.0.0", SPDX: "MIT", URL: "https://p", Text: "two\r\ntext  \n\n\n"},
		{Group: 3, Name: "pkg", Version: "1.0.0", SPDX: "MIT", URL: "https://p", Text: "one\n"},
		{Group: 1, Name: "math/a.go", Text: "notice b\n"},
		{Group: 1, Name: "math/a.go", Text: "notice a\n"},
		{Group: 0, Name: "Go", Version: "1.27", SPDX: "BSD-3-Clause", URL: "https://go.dev", Text: "go\n"},
	}
	first, err := render(es)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		es[0], es[i] = es[i], es[0]
		if again, _ := render(es); again != first {
			t.Fatal("the output depends on the input order")
		}
	}
	if strings.Index(first, "## pkg 1.0.0") > strings.Index(first, "## pkg 2.0.0") ||
		strings.Count(first, "## Also in the Go standard library") != 1 ||
		strings.Index(first, "notice a") > strings.Index(first, "notice b") ||
		strings.Contains(first, "\r") || strings.Contains(first, "text  \n") || !strings.HasPrefix(first, "# Third-party notices") {
		t.Errorf("render:\n%s", first)
	}
	if _, err := render(append(es, entry{Group: 3, Name: "pkg", Version: "1.0.0", Text: "again\n"})); err == nil {
		t.Error("two entries for one name and version were accepted")
	}
}

// TestCheckFailsOnDrift runs the generator end to end on this repository's Go
// side with a fake web bundle: written once, -check passes; edited, it fails.
func TestCheckFailsOnDrift(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list for every release platform")
	}
	f := newFakeWeb(t)
	f.manifest([]string{"WEB/src/main.ts", "WEB/node_modules/svelte/src/a.js"}, nil)
	out := filepath.Join(t.TempDir(), "NOTICES.md")
	args := []string{"-root", "../../..", "-bundle", f.bundle, "-web", f.web, "-out", out}
	var stdout, stderr bytes.Buffer
	if rc := run(args, &stdout, &stderr); rc != 0 {
		t.Fatalf("write: %d %s", rc, stderr.String())
	}
	if rc := run(append(args, "-check"), &stdout, &stderr); rc != 0 {
		t.Fatalf("check of a fresh file: %d %s", rc, stderr.String())
	}
	write(t, out, read(t, out)+"\nedited by hand\n")
	stderr.Reset()
	if rc := run(append(args, "-check"), &stdout, &stderr); rc != 1 || !strings.Contains(stderr.String(), "out of date") {
		t.Errorf("check of an edited file: %d %s", rc, stderr.String())
	}
}

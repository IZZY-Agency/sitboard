// Command notices writes THIRD_PARTY_NOTICES.md, the licences of everything
// sitboard ships besides its own code, from the components' own licence files:
//
//   - Go: the standard library and runtime compiled into every binary, with
//     the other notices carried by the standard library files compiled for
//     the release platforms
//   - every module in the build of ./cmd/sitboard, on each release platform
//     (`go list -deps`, not the whole module graph)
//   - every npm package with a module or an asset in the web bundle
//   - every font file in the web bundle
//
// Run from the repository root, through scripts/notices.sh (make notices),
// which builds the web page into a scratch folder with
// scripts/notices-bundle.mjs: that build records every module and asset that
// went into the page (modules.json), CSS and fonts included.
//
//	go run ./internal/tools/notices -bundle <that folder> [-check]
//
// With -check it writes nothing and fails when the file is out of date, so a
// new module, package or font cannot ship without an entry. It stops on a
// licence other than MIT, BSD-2/3-Clause, ISC, Apache-2.0 or OFL-1.1 (matched
// against the canonical texts), on a bundled module that is neither the page's
// own source nor an npm package, on a font or image inlined as a data: URI,
// and when Go's licence or its other notices differ from the pinned ones.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/google/licensecheck"
)

// goLicenseSHA256 pins Go's LICENSE as of the Go release go.mod names. The
// text has changed before (in 2024); a Go upgrade that changes it again
// stops the generator until the new text is reviewed and pinned here.
const goLicenseSHA256 = "911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad"

// goNoticeSHA256 pins the notices of other authors that the compiled
// standard library carries (fdlibm, Cephes, Inferno, fiat-crypto), one digest
// per distinct notice text, so a Go upgrade that changes them stops the
// generator instead of changing the file silently.
var goNoticeSHA256 = map[string]bool{
	"080c994962188f757c3b6bf6a500444078a16a65f993e6c8d9b14ac9a5769f4e": true,
	"1fad26590385d93b00c0d91989ee71cd1c91c74a126485f1b4f0bb44f7a32354": true,
	"97321d2fb4054118aa5da108fa293a341490fe5ca11386c320acc85f7329c056": true,
	"9f7f8dba0edbdc891e676f3c4239bfd485bb71e8bb767f103a3244f853df1b03": true,
	"b010adcefa10bbced885f54291172d9ff42c566670826685e11f65cf0a0cfa8b": true,
	"b29230c2fd1b481ef5306da20b8c49a3c8211255d44db7007156321fb270463d": true,
	"e288ea1b559cffc33e788a2c8d7eddb78df101c298f06986986865d2090046cc": true,
}

// platforms are the release builds; a module used on any of them is listed.
var platforms = [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "arm64"}, {"linux", "amd64"}}

// allowed are the licences sitboard may ship without asking a person.
var allowed = map[string]bool{"MIT": true, "BSD-2-Clause": true, "BSD-3-Clause": true, "ISC": true, "Apache-2.0": true, "OFL-1.1": true}

// font is a font family the web bundle may carry. Its files are pinned by
// digest: a new version of a font changes the digest, and with it this entry.
type font struct {
	Name, Version, URL string
	Licence            string            // licence text file, relative to the web folder
	Files              map[string]string // file name in web/src/fonts -> sha256
}

var fonts = []font{
	{
		Name: "JetBrains Mono", Version: "2.211", URL: "https://github.com/JetBrains/JetBrainsMono",
		Licence: "src/fonts/JetBrainsMono-OFL.txt",
		Files: map[string]string{
			"jetbrains-mono-latin-wght-normal.woff2":     "18be452724bfdc236c074ca94a249a7f41a86752c7d04ab258ce9ed5651f6a7e",
			"jetbrains-mono-latin-ext-wght-normal.woff2": "79bfdab9ba467e26eea4122e6f2567e188dd8a09a8c730d501fc487c4ab99c6e",
		},
	},
	{
		Name: "Space Grotesk", Version: "2.000", URL: "https://github.com/floriankarsten/space-grotesk",
		Licence: "src/fonts/SpaceGrotesk-OFL.txt",
		Files: map[string]string{
			"space-grotesk-latin-wght-normal.woff2":     "0640890476fc1198ab4de571fb658de443c4d85b66466ec09534a8737ab1ce9d",
			"space-grotesk-latin-ext-wght-normal.woff2": "952dddb45d2f96f71cbf3b7f510b24379afc3c89ea02fcf89d377b45d62c0166",
		},
	},
}

// entry is one component in the notices. Group orders the sections: Go,
// Go's other notices, Go modules, npm packages, fonts.
type entry struct {
	Group                    int
	Name, Version, SPDX, URL string
	What                     string
	Text                     string
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("notices", flag.ContinueOnError)
	fl.SetOutput(stderr)
	bundle := fl.String("bundle", "", "the folder scripts/notices-bundle.mjs built (modules.json and bundle/)")
	root := fl.String("root", ".", "the repository root")
	web := fl.String("web", "", "the web folder (default <root>/web)")
	out := fl.String("out", "", "the file to write or check (default <root>/THIRD_PARTY_NOTICES.md)")
	check := fl.Bool("check", false, "write nothing; fail when the file is out of date")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if *web == "" {
		*web = filepath.Join(*root, "web")
	}
	if *out == "" {
		*out = filepath.Join(*root, "THIRD_PARTY_NOTICES.md")
	}
	failf := func(err error) int {
		_, _ = fmt.Fprintln(stderr, "notices:", err)
		return 1
	}
	if *bundle == "" {
		return failf(errors.New("-bundle is required"))
	}
	text, err := generate(*root, *bundle, *web)
	if err != nil {
		return failf(err)
	}
	if *check {
		have, err := os.ReadFile(*out)
		if err != nil || string(have) != text {
			return failf(fmt.Errorf("%s is out of date: run make notices and commit it", *out))
		}
		_, _ = fmt.Fprintf(stdout, "notices: %s is up to date\n", *out)
		return 0
	}
	if err := os.WriteFile(*out, []byte(text), 0o644); err != nil {
		return failf(err)
	}
	_, _ = fmt.Fprintf(stdout, "notices: wrote %s\n", *out)
	return 0
}

func generate(root, bundle, web string) (string, error) {
	goEntries, err := goItself(root)
	if err != nil {
		return "", err
	}
	mods, err := goModules(root)
	if err != nil {
		return "", err
	}
	webEntries, err := bundleEntries(bundle, web, fonts)
	if err != nil {
		return "", err
	}
	es := append(append(goEntries, mods...), webEntries...)
	return render(es)
}

// --- licences ----------------------------------------------------------------------

// refused are phrases that mark a licence sitboard may not ship, whatever a
// match against canonical texts says.
var refused = regexp.MustCompile(`(?i)advertising materials|GNU (Lesser |Affero )?General Public|Commons Clause|Good, not Evil|Server Side Public License|Business Source License`)

// detectSPDX names the licence of a licence text: exactly one licence from
// the allowed set, covering nearly all of the text.
func detectSPDX(text string) (string, error) {
	if m := refused.FindString(text); m != "" {
		return "", fmt.Errorf("the licence text has %q: not an allowed licence; stop and review it by hand", m)
	}
	cov := licensecheck.Scan([]byte(text))
	ids := map[string]bool{}
	for _, m := range cov.Match {
		if !m.IsURL {
			ids[m.ID] = true
		}
	}
	if len(ids) != 1 {
		var names []string
		for id := range ids {
			names = append(names, id)
		}
		sort.Strings(names)
		return "", fmt.Errorf("the licence text matches %v, not exactly one licence: stop and review it by hand", names)
	}
	var id string
	for k := range ids {
		id = k
	}
	if !allowed[id] {
		return "", fmt.Errorf("the licence is %s, not MIT, BSD-2/3-Clause, ISC, Apache-2.0 or OFL-1.1: stop and review it by hand", id)
	}
	if cov.Percent < 90 {
		return "", fmt.Errorf("the licence text is only %.0f%% %s: stop and review it by hand", cov.Percent, id)
	}
	return id, nil
}

// licenceFile reads the one licence file in dir.
func licenceFile(dir string) (string, error) {
	found, err := filesNamed(dir, "LICENSE", "LICENSE.MD", "LICENSE.TXT", "LICENCE", "COPYING")
	if err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", fmt.Errorf("%s: want exactly one licence file, found %v", dir, found)
	}
	b, err := os.ReadFile(filepath.Join(dir, found[0]))
	return string(b), err
}

// licensed reads dir's licence, names it, and for Apache-2.0 appends the
// NOTICE files the licence requires to travel with the work.
func licensed(dir string) (text, id string, err error) {
	text, err = licenceFile(dir)
	if err != nil {
		return "", "", err
	}
	id, err = detectSPDX(text)
	if err != nil {
		return "", "", err
	}
	if id == "Apache-2.0" {
		notices, err := filesNamed(dir, "NOTICE", "NOTICE.MD", "NOTICE.TXT")
		if err != nil {
			return "", "", err
		}
		for _, n := range notices {
			b, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				return "", "", err
			}
			text = strings.TrimRight(text, "\n") + "\n\n--- " + n + " ---\n\n" + string(b)
		}
	}
	return text, id, nil
}

// filesNamed lists the files in dir whose upper-cased name is one of names, sorted.
func filesNamed(dir string, names ...string) ([]string, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, d := range des {
		up := strings.ToUpper(d.Name())
		for _, n := range names {
			if !d.IsDir() && up == n {
				found = append(found, d.Name())
			}
		}
	}
	sort.Strings(found)
	return found, nil
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- Go ------------------------------------------------------------------------------

func goItself(root string) ([]entry, error) {
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return nil, fmt.Errorf("go env GOROOT: %w", err)
	}
	goroot := strings.TrimSpace(string(out))
	text, err := goLicense(goroot, goLicenseSHA256)
	if err != nil {
		return nil, err
	}
	version, err := goVersion(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	es := []entry{{
		Group: 0, Name: "Go", Version: version, SPDX: "BSD-3-Clause", URL: "https://go.dev",
		What: "The Go standard library and runtime, compiled into every sitboard binary.",
		Text: text,
	}}
	files, err := stdFiles(root)
	if err != nil {
		return nil, err
	}
	notices, err := stdNotices(filepath.Join(goroot, "src"), files, goNoticeSHA256)
	if err != nil {
		return nil, err
	}
	return append(es, notices...), nil
}

// goLicense finds Go's LICENSE: in GOROOT, or one folder up where Homebrew
// keeps it (GOROOT is <prefix>/libexec there). Only the file with the pinned
// digest is accepted.
func goLicense(goroot, pinned string) (string, error) {
	for _, p := range []string{filepath.Join(goroot, "LICENSE"), filepath.Join(filepath.Dir(goroot), "LICENSE")} {
		b, err := os.ReadFile(p)
		if err == nil && sha(b) == pinned {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("no LICENSE with Go's pinned sha256 %s in %s or one folder up", pinned, goroot)
}

// goVersion is the go directive of go.mod: the language version the release
// toolchain must provide. It keeps the output the same on every machine.
func goVersion(gomod string) (string, error) {
	b, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	m := regexp.MustCompile(`(?m)^go ([0-9.]+)$`).FindSubmatch(b)
	if m == nil {
		return "", errors.New("go.mod has no go directive")
	}
	return string(m[1]), nil
}

type listed struct {
	Path, Version, Dir string
	Main               bool
	Replace            *listed
}

type pkgInfo struct {
	ImportPath, Dir string
	Standard        bool
	GoFiles, SFiles []string
	Module          *listed
}

// listDeps runs `go list -deps ./cmd/sitboard` for every release platform.
func listDeps(root string) ([]pkgInfo, error) {
	var all []pkgInfo
	for _, p := range platforms {
		cmd := exec.Command("go", "list", "-deps", "-json=ImportPath,Dir,Standard,GoFiles,SFiles,Module", "./cmd/sitboard")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS="+p[0], "GOARCH="+p[1], "CGO_ENABLED=0", "GOFLAGS=-mod=readonly")
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go list -deps for %s/%s: %w", p[0], p[1], err)
		}
		dec := json.NewDecoder(bytes.NewReader(out))
		for dec.More() {
			var pkg pkgInfo
			if err := dec.Decode(&pkg); err != nil {
				return nil, err
			}
			all = append(all, pkg)
		}
	}
	return all, nil
}

// stdFiles lists the standard library files compiled for the release
// platforms, relative to GOROOT/src.
func stdFiles(root string) ([]string, error) {
	pkgs, err := listDeps(root)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, p := range pkgs {
		if !p.Standard {
			continue
		}
		for _, f := range append(append([]string(nil), p.GoFiles...), p.SFiles...) {
			seen[p.ImportPath+"/"+f] = true
		}
	}
	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	sort.Strings(files)
	return files, nil
}

// reForeign is a copyright statement, with a year, (c) or ©.
var reForeign = regexp.MustCompile(`(?i)\bcopyright\b.*(\b(19|20)[0-9]{2}\b|\(c\)|©)`)

// stdNotices finds, in the given standard library files, the notices of other
// authors: in each comment block with a copyright statement that is not the
// Go Authors', the notice runs from that statement (with the title lines just
// above it) to the closing "====" rule or the end of the block. Identical
// notices are listed once with their files. Every distinct notice text must
// be pinned.
func stdNotices(src string, files []string, pinned map[string]bool) ([]entry, error) {
	byText := map[string][]string{}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(f)))
		if err != nil {
			return nil, err
		}
		for _, n := range noticesIn(string(b)) {
			byText[n] = append(byText[n], f)
		}
	}
	var es []entry
	var unpinned []string
	for text, where := range byText {
		if !pinned[sha([]byte(text))] {
			unpinned = append(unpinned, fmt.Sprintf("%s (in %s)", sha([]byte(text)), strings.Join(where, ", ")))
		}
		es = append(es, entry{Group: 1, Name: strings.Join(where, ", "), Text: text})
	}
	if len(unpinned) > 0 || len(byText) != len(pinned) {
		sort.Strings(unpinned)
		return nil, fmt.Errorf("the notices in Go's standard library differ from the pinned ones (%d found, %d pinned); review them and update goNoticeSHA256: %s",
			len(byText), len(pinned), strings.Join(unpinned, "; "))
	}
	return es, nil
}

// noticesIn returns the foreign notices in one source file.
func noticesIn(src string) []string {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	comment := func(i int) (string, bool) {
		t := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(t, "//") {
			return "", false
		}
		t = strings.TrimPrefix(t, "//")
		if strings.HasPrefix(t, " ") || strings.HasPrefix(t, "\t") {
			t = t[1:]
		}
		return strings.TrimRight(t, " \t"), true
	}
	rule := func(s string) bool { return len(s) >= 10 && strings.Trim(s, "=") == "" }
	var out []string
	for i := 0; i < len(lines); i++ {
		c, ok := comment(i)
		if !ok || !reForeign.MatchString(c) || strings.Contains(c, "The Go Authors") {
			continue
		}
		start := i
		for start > 0 {
			prev, ok := comment(start - 1)
			if !ok || prev == "" || rule(prev) {
				break
			}
			start--
		}
		ruled := false
		if start > 0 {
			prev, ok := comment(start - 1)
			ruled = ok && rule(prev)
		}
		end := i
		for end+1 < len(lines) {
			next, ok := comment(end + 1)
			if !ok || (ruled && rule(next)) {
				break
			}
			end++
		}
		var b strings.Builder
		for j := start; j <= end; j++ {
			c, _ := comment(j)
			b.WriteString(c + "\n")
		}
		out = append(out, clean(b.String()))
		i = end
	}
	return out
}

// goModules lists the modules in the build of ./cmd/sitboard on each release
// platform, with their licence files from the module cache.
func goModules(root string) ([]entry, error) {
	pkgs, err := listDeps(root)
	if err != nil {
		return nil, err
	}
	seen := map[string]listed{}
	for _, p := range pkgs {
		if m := p.Module; m != nil && !m.Main {
			if m.Replace != nil {
				return nil, fmt.Errorf("module %s is replaced: review its licence by hand", m.Path)
			}
			seen[m.Path] = *m
		}
	}
	var es []entry
	for _, m := range seen {
		text, id, err := licensed(m.Dir)
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", m.Path, err)
		}
		es = append(es, entry{Group: 2, Name: m.Path, Version: m.Version, SPDX: id, URL: moduleURL(m.Path),
			What: "A Go module compiled into the binary.", Text: text})
	}
	return es, nil
}

func moduleURL(path string) string {
	switch {
	case strings.HasPrefix(path, "github.com/"):
		return "https://" + path
	case strings.HasPrefix(path, "golang.org/x/"):
		return "https://go.googlesource.com/" + strings.TrimPrefix(path, "golang.org/x/")
	}
	return "https://pkg.go.dev/" + path
}

// --- the web bundle --------------------------------------------------------------------

// manifest is what scripts/notices-bundle.mjs records: every module id in the
// build's module graph (absolute paths; virtual modules start with "\x00")
// and every emitted asset with the source files it came from.
type manifest struct {
	Modules []string
	Assets  []struct {
		FileName string
		Sources  []string
	}
}

var (
	fontExt   = regexp.MustCompile(`(?i)\.(woff2?|ttf|otf|eot)$`)
	inlined   = regexp.MustCompile(`(?i)data:(font/|image/|application/(x-)?font)`)
	cssOrJSRe = regexp.MustCompile(`\.(css|js|mjs)$`)
)

// bundleEntries reads the manifest and the built files and returns the npm
// packages and fonts in the page. Everything else must be the page's own
// source under web/src (or web/index.html).
func bundleEntries(dir, web string, table []font) ([]entry, error) {
	b, err := os.ReadFile(filepath.Join(dir, "modules.json"))
	if err != nil {
		return nil, fmt.Errorf("no module list from the notices build: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("modules.json: %w", err)
	}
	if len(m.Modules) == 0 {
		return nil, errors.New("modules.json lists no modules")
	}
	webAbs, err := filepath.Abs(web)
	if err != nil {
		return nil, err
	}
	webAbs = filepath.ToSlash(webAbs)
	pkgs := map[string]bool{}
	// classify places one source path: an npm package, the page's own
	// source, or an error.
	classify := func(p string) error {
		p = strings.SplitN(p, "?", 2)[0]
		if strings.HasPrefix(p, "\x00") || !strings.HasPrefix(p, "/") {
			return fmt.Errorf("the page bundles %q, which is neither its own source nor an npm package: give it an entry or remove it", strings.TrimPrefix(p, "\x00"))
		}
		if idx := strings.LastIndex(p, "/node_modules/"); idx >= 0 {
			parts := strings.SplitN(p[idx+len("/node_modules/"):], "/", 3)
			name := parts[0]
			if strings.HasPrefix(name, "@") && len(parts) > 1 {
				name += "/" + parts[1]
			}
			pkgs[p[:idx]+"/node_modules/"+name] = true
			return nil
		}
		if strings.HasPrefix(p, webAbs+"/src/") || p == webAbs+"/index.html" {
			return nil
		}
		return fmt.Errorf("the page bundles %s, which is neither under web/src nor an npm package", p)
	}
	for _, id := range m.Modules {
		if err := classify(id); err != nil {
			return nil, err
		}
	}
	usedFonts := map[int]bool{}
	for _, a := range m.Assets {
		if len(a.Sources) == 0 {
			return nil, fmt.Errorf("asset %s has no recorded source", a.FileName)
		}
		for _, s := range a.Sources {
			full := filepath.ToSlash(s)
			if !strings.HasPrefix(full, "/") {
				full = webAbs + "/" + strings.TrimPrefix(full, "./")
			}
			if err := classify(full); err != nil {
				return nil, err
			}
			if !fontExt.MatchString(s) || strings.Contains(full, "/node_modules/") {
				continue
			}
			i, err := pinnedFont(filepath.Join(dir, "bundle", a.FileName), filepath.Base(s), table)
			if err != nil {
				return nil, err
			}
			usedFonts[i] = true
		}
	}
	// A font or image inlined into CSS or JavaScript has no asset of its own.
	err = filepath.WalkDir(filepath.Join(dir, "bundle"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !cssOrJSRe.MatchString(p) {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if loc := inlined.FindIndex(body); loc != nil {
			return fmt.Errorf("%s inlines a font or image (%s...): its source and licence cannot be told; make it a file", filepath.Base(p), body[loc[0]:loc[1]])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var es []entry
	for pkg := range pkgs {
		e, err := npmEntry(pkg)
		if err != nil {
			return nil, err
		}
		es = append(es, e)
	}
	for i := range usedFonts {
		f := table[i]
		text, err := os.ReadFile(filepath.Join(web, f.Licence))
		if err != nil {
			return nil, fmt.Errorf("font %s: %w", f.Name, err)
		}
		id, err := detectSPDX(string(text))
		if err != nil {
			return nil, fmt.Errorf("font %s: %w", f.Name, err)
		}
		es = append(es, entry{Group: 4, Name: f.Name, Version: f.Version, SPDX: id, URL: f.URL,
			What: "A font embedded in the web page.", Text: string(text)})
	}
	return es, nil
}

// pinnedFont finds the family of a bundled font file by its source name, and
// requires the bundled bytes to be the pinned ones.
func pinnedFont(path, source string, table []font) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for i, f := range table {
		if want, ok := f.Files[source]; ok {
			if sha(b) != want {
				return 0, fmt.Errorf("font %s is not the pinned %s %s: update its entry (version and digests)", source, f.Name, f.Version)
			}
			return i, nil
		}
	}
	return 0, fmt.Errorf("font %s in the web bundle has no entry: add it to the font table with its licence", source)
}

func npmEntry(dir string) (entry, error) {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return entry{}, fmt.Errorf("a bundled package has no package.json: %w", err)
	}
	var pj struct {
		Name, Version, License string
		Repository             json.RawMessage
	}
	if err := json.Unmarshal(b, &pj); err != nil {
		return entry{}, fmt.Errorf("%s: %w", dir, err)
	}
	text, id, err := licensed(dir)
	if err != nil {
		return entry{}, fmt.Errorf("npm package %s: %w", pj.Name, err)
	}
	if pj.License != id {
		return entry{}, fmt.Errorf("npm package %s declares %q but its licence text is %s", pj.Name, pj.License, id)
	}
	url, err := repoURL(pj.Repository)
	if err != nil {
		return entry{}, fmt.Errorf("npm package %s: %w", pj.Name, err)
	}
	return entry{Group: 3, Name: pj.Name, Version: pj.Version, SPDX: id, URL: url,
		What: "An npm package whose code is in the web page embedded in the binary.", Text: text}, nil
}

// repoURL turns package.json's repository field into a web address.
func repoURL(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		var obj struct{ URL string }
		if err := json.Unmarshal(raw, &obj); err != nil || obj.URL == "" {
			return "", errors.New("no repository in package.json")
		}
		s = obj.URL
	}
	s = strings.TrimPrefix(s, "git+")
	s = strings.TrimSuffix(s, ".git")
	if rest, ok := strings.CutPrefix(s, "github:"); ok {
		s = "https://github.com/" + rest
	}
	s = strings.Replace(s, "git://", "https://", 1)
	if !strings.HasPrefix(s, "https://") {
		return "", fmt.Errorf("repository %q is not a web address", s)
	}
	return s, nil
}

// --- output ----------------------------------------------------------------------------

// clean normalises a licence text: LF line ends, no trailing spaces, one
// final newline.
func clean(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n") + "\n"
}

// render writes the notices in a fixed order: group, name, version, then the
// text itself. Two entries with one name and version are ambiguous: an error.
func render(es []entry) (string, error) {
	es = append([]entry(nil), es...)
	sort.Slice(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return a.Text < b.Text
	})
	for i := 1; i < len(es); i++ {
		a, b := es[i-1], es[i]
		if b.Group != 1 && a.Group == b.Group && a.Name == b.Name && a.Version == b.Version {
			return "", fmt.Errorf("two entries for %s %s", b.Name, b.Version)
		}
	}
	var b strings.Builder
	b.WriteString("# Third-party notices\n\n")
	b.WriteString("sitboard is released under the MIT License (see LICENSE). Its binary also\n")
	b.WriteString("carries the components below, each under its own licence. This file is\n")
	b.WriteString("generated by `make notices` from the components' own licence files; do not\n")
	b.WriteString("edit it by hand. `sitboard --licenses` prints it.\n")
	stdHeading := false
	for _, e := range es {
		if e.Group == 1 {
			// The other authors' notices in Go's standard library, one per
			// distinct text, titled by the files that carry it.
			if !stdHeading {
				b.WriteString("\n## Also in the Go standard library\n\n")
				b.WriteString("Notices of other authors that Go's licence does not cover, carried by the\n")
				b.WriteString("standard library files compiled into the binary.\n")
				stdHeading = true
			}
			fmt.Fprintf(&b, "\n### %s\n\n````text\n%s````\n", e.Name, clean(e.Text))
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n", strings.TrimSpace(e.Name+" "+e.Version))
		if e.What != "" {
			fmt.Fprintf(&b, "%s\n\n", e.What)
		}
		if e.SPDX != "" {
			fmt.Fprintf(&b, "- Licence: %s\n- Source: %s\n\n", e.SPDX, e.URL)
		}
		b.WriteString("````text\n")
		b.WriteString(clean(e.Text))
		b.WriteString("````\n")
	}
	return b.String(), nil
}

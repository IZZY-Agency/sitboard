package facts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tiny = `
facts:
  - id: one
    text: "**Local only.** It listens on ` + "`127.0.0.1`" + `."
    anchors:
      - code: pkg.Listen
  - id: two
    text: Second.
    joins: true
    anchors:
      - value: pkg.every
        is: time.Minute
  - id: three
    text: Nested.
    sub: true
    anchors:
      - test: pkg.TestListen
sections:
  list: [one, two, three]
  para: [one, two]
faq:
  - q: Is it local?
    a: [{say: "Yes."}, three]
`

func parseTiny(t *testing.T, src string) *File {
	t.Helper()
	f, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseRefusesBrokenFiles(t *testing.T) {
	for name, src := range map[string]string{
		"unknown key":      strings.Replace(tiny, "text: Second.", "text: Second.\n    txt: typo", 1),
		"no anchor":        "facts:\n  - id: a\n    text: A.\nsections:\n  s: [a]\n",
		"unused fact":      tiny + "  other: []\n",
		"missing id":       strings.Replace(tiny, "list: [one, two, three]", "list: [one, two, three, four]", 1),
		"two anchor kinds": strings.Replace(tiny, "      - code: pkg.Listen", "      - code: pkg.Listen\n        test: pkg.TestListen", 1),
		"raw html":         strings.Replace(tiny, "text: Second.", "text: Second <b>x</b>.", 1),
		"site anchor":      strings.Replace(tiny, "      - code: pkg.Listen", "      - file: "+WebsiteDir+"/src/faq.ts\n        has: x", 1),
		"bold in a brief":  strings.Replace(tiny, "text: Nested.", "text: Nested.\n    brief: \"**Bold**\"", 1),
		"joins first":      strings.Replace(tiny, "para: [one, two]", "para: [two, one]", 1),
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	parseTiny(t, tiny)
}

func TestReadmeSectionsAreRewritten(t *testing.T) {
	f := parseTiny(t, tiny)
	in := "# x\n\n<!-- facts:list -->\nold\n<!-- /facts:list -->\n\n<!-- facts:para paragraph -->\nold\n<!-- /facts:para -->\nend\n"
	got, err := f.Readme([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := "# x\n\n<!-- facts:list -->\n- **Local only.** It listens on `127.0.0.1`. Second.\n  - Nested.\n<!-- /facts:list -->\n\n" +
		"<!-- facts:para paragraph -->\n**Local only.** It listens on `127.0.0.1`. Second.\n<!-- /facts:para -->\nend\n"
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if _, err := f.Readme([]byte(in + "<!-- facts:nope -->\n<!-- /facts:nope -->")); err == nil {
		t.Error("a README section facts.yaml does not have was accepted")
	}
	// Each section's markers lost, broken or doubled, with the other intact.
	for name, broken := range map[string]string{
		"list pair removed":   strings.Replace(strings.Replace(in, "<!-- facts:list -->\n", "", 1), "<!-- /facts:list -->\n", "", 1),
		"para pair removed":   strings.Replace(strings.Replace(in, "<!-- facts:para paragraph -->\n", "", 1), "<!-- /facts:para -->\n", "", 1),
		"list closing lost":   strings.Replace(in, "<!-- /facts:list -->\n", "", 1),
		"para opening lost":   strings.Replace(in, "<!-- facts:para paragraph -->\n", "", 1),
		"list twice":          in + "<!-- facts:list -->\nold\n<!-- /facts:list -->\n",
		"list closed as para": strings.Replace(in, "<!-- /facts:list -->", "<!-- /facts:para -->", 1),
	} {
		if _, err := f.Readme([]byte(broken)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	site := strings.Replace(tiny, "  para: [one, two]", "  para: [one, two]\n  site-intro: [three]", 1)
	if _, err := parseTiny(t, site).Readme([]byte(in)); err != nil {
		t.Errorf("a website-only section was asked of README: %v", err)
	}
}

func TestRendering(t *testing.T) {
	s := "**Local only.** Uses `a<b` and [docs](https://example.com/x) & more."
	if got, want := HTML(s), `<strong>Local only.</strong> Uses <code>a&lt;b</code> and <a href="https://example.com/x">docs</a> &amp; more.`; got != want {
		t.Errorf("HTML:\n got %s\nwant %s", got, want)
	}
	if got, want := Plain(s), "Local only. Uses a<b and docs & more."; got != want {
		t.Errorf("Plain: got %q, want %q", got, want)
	}
	f := parseTiny(t, tiny)
	if got := f.SiteJSON(); !strings.Contains(string(got), `"a": "Yes. Nested."`) {
		t.Errorf("FAQ answer not built from its parts:\n%s", got)
	}
}

// A code anchor's hash follows the declaration's code, not its comments or
// its place in the file.
func TestHashFollowsTheCode(t *testing.T) {
	dir := t.TempDir()
	write := func(src string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		h, err := NewRepo(dir).Hash("pkg.T.Run")
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	base := write("package pkg\n\ntype T struct{}\n\nfunc (t *T) Run() int { return 1 }\n")
	if write("package pkg\n\n// Run runs.\ntype T struct{}\n\n// Run returns one.\nfunc (t *T) Run() int { return 1 } // one\n") != base {
		t.Error("a comment changed the hash")
	}
	if write("package pkg\n\ntype T struct{}\n\nfunc (t *T) Run() int { return 2 }\n") == base {
		t.Error("a changed body kept the hash")
	}
	if _, err := NewRepo(dir).Hash("pkg.Run"); err == nil {
		t.Error("a method was found as a function")
	}
}

func TestCheckValueAndFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package pkg\n\nimport \"time\"\n\nconst every = time.Minute\n\nvar _ = every\n"
	if err := os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "a_test.go"), []byte("package pkg\n\nimport \"testing\"\n\nfunc TestEvery(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRepo(dir)
	if _, err := r.Check(Anchor{Value: "pkg.every", Is: "time.Minute"}); err != nil {
		t.Error(err)
	}
	if _, err := r.Check(Anchor{Value: "pkg.every", Is: "2 * time.Minute"}); err == nil {
		t.Error("a different value passed")
	}
	if _, err := r.Check(Anchor{Test: "pkg.TestEvery"}); err != nil {
		t.Error(err)
	}
	if _, err := r.Check(Anchor{Test: "pkg.TestGone"}); err == nil {
		t.Error("a missing test passed")
	}
	if _, err := r.Check(Anchor{File: "pkg/a.go", Has: "time.Minute"}); err != nil {
		t.Error(err)
	}
	if _, err := r.Check(Anchor{File: "pkg/a.go", Has: "time.Hour"}); err == nil {
		t.Error("a missing string passed")
	}
}

// A value is what the code evaluates to: changing a constant it is built
// from, with its own text unchanged, fails the fact that states it.
func TestCheckValueFollowsItsInputs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	check := func(knownFor, is string) error {
		t.Helper()
		src := "package pkg\n\nimport \"time\"\n\nconst (\n\tknownFor   = " + knownFor + "\n\trenewAfter = knownFor - 2*time.Minute\n)\n\nvar path = \"/usr/bin\"\nvar names = []string{\"a\"}\n"
		if err := os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := NewRepo(dir).Check(Anchor{Value: "pkg.renewAfter", Is: is})
		return err
	}
	if err := check("10 * time.Minute", "8 * time.Minute"); err != nil {
		t.Error(err)
	}
	if err := check("10 * time.Minute", "480 * time.Second"); err != nil {
		t.Errorf("the same duration written otherwise: %v", err)
	}
	if err := check("12 * time.Minute", "8 * time.Minute"); err == nil || !strings.Contains(err.Error(), "10m0s") {
		t.Errorf("knownFor went to 12 minutes and the eight-minute fact passed: %v", err)
	}
	if err := check("10 * time.Minute", "knownFor - 2*time.Minute"); err == nil {
		t.Error("a stated value that names the code's own constant passed")
	}
	r := NewRepo(dir)
	if _, err := r.Check(Anchor{Value: "pkg.path", Is: `"/usr/bin"`}); err != nil {
		t.Error(err)
	}
	if _, err := r.Check(Anchor{Value: "pkg.names", Is: `[]string{"a"}`}); err != nil {
		t.Errorf("a composite value is compared as text: %v", err)
	}
	if _, err := r.Check(Anchor{Value: "pkg.names", Is: `[]string{"b"}`}); err == nil {
		t.Error("a different composite value passed")
	}
}

// A wording of another fact takes that fact's anchors, so it is confirmed
// against the same code.
func TestOfTakesTheAnchors(t *testing.T) {
	src := strings.Replace(tiny, "faq:\n", "  site-x: [again]\nfaq:\n", 1)
	src = strings.Replace(src, "sections:\n", "  - id: again\n    text: Local, again.\n    of: [one, two]\nsections:\n", 1)
	f := parseTiny(t, src)
	x, _ := f.ByID("again")
	if len(x.Anchors) != 2 || x.Anchors[0].Code != "pkg.Listen" || x.Anchors[1].Value != "pkg.every" {
		t.Errorf("anchors %+v", x.Anchors)
	}
	for name, bad := range map[string]string{
		"own anchors too": strings.Replace(src, "    of: [one, two]\n", "    of: [one, two]\n    anchors:\n      - code: pkg.Listen\n", 1),
		"unknown fact":    strings.Replace(src, "of: [one, two]", "of: [one, nope]", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// A fact's keeps: words must be in each of its wordings and in every wording
// of a fact that words it again (of:), so no shorter form drops them.
func TestKeepsHoldInEveryWording(t *testing.T) {
	src := strings.Replace(tiny, "faq:\n", "  site-x: [again]\nfaq:\n", 1)
	src = strings.Replace(src, "sections:\n", "  - id: again\n    text: Local, except on a NETWORK folder.\n    of: [one]\nsections:\n", 1)
	src = strings.Replace(src, "    anchors:\n      - code: pkg.Listen\n",
		"    short: Local only, except on a `network` folder.\n    brief: Local, but a network folder is not.\n    keeps: [network folder]\n    anchors:\n      - code: pkg.Listen\n", 1)
	src = strings.Replace(src, "It listens on", "It listens, except on a network\n      folder, on", 1)
	parseTiny(t, src)
	for name, bad := range map[string]string{
		"text drops it":         strings.Replace(src, "except on a network\n      folder, on", "on", 1),
		"short drops it":        strings.Replace(src, "Local only, except on a `network` folder.", "Local only.", 1),
		"brief drops it":        strings.Replace(src, "Local, but a network folder is not.", "Local.", 1),
		"a wording of it drops": strings.Replace(src, "Local, except on a NETWORK folder.", "Local.", 1),
		"keeps on a wording":    strings.Replace(src, "    of: [one]\n", "    of: [one]\n    keeps: [local]\n", 1),
		"an empty keeps":        strings.Replace(src, "keeps: [network folder]", "keeps: ['']", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestConfirmedRoundTrip(t *testing.T) {
	c := Confirmed{ConfirmKey("b", "pkg.B"): "2", ConfirmKey("a", "pkg.A"): "1"}
	back, err := ParseConfirmed(c.Format())
	if err != nil || len(back) != 2 || back[ConfirmKey("a", "pkg.A")] != "1" {
		t.Fatalf("round trip: %v %v", back, err)
	}
	if _, err := ParseConfirmed([]byte("a pkg.A\n")); err == nil {
		t.Error("a short line was accepted")
	}
}

// Renaming or retiring a fact: its old id still has lines in confirmed.txt,
// and confirming that id drops them; an id in neither file is refused.
func TestConfirmRetiresAndRenames(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte("package pkg\n\nfunc Listen() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, repo := parseTiny(t, tiny), NewRepo(dir)
	c := Confirmed{ConfirmKey("gone", "pkg.Listen"): "x", ConfirmKey("old-one", "pkg.Listen"): "y"}
	// "one" is "old-one" renamed; "gone" was deleted.
	msgs, err := ConfirmInto(c, f, repo, []string{"one", "old-one", "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 1 || c[ConfirmKey("one", "pkg.Listen")] == "" {
		t.Errorf("confirmed %v", c)
	}
	if strings.Join(msgs, ",") != "confirmed one,retired old-one,retired gone" {
		t.Errorf("messages %v", msgs)
	}
	if _, err := ConfirmInto(c, f, repo, []string{"never-was"}); err == nil {
		t.Error("an id in neither file was accepted")
	}
}

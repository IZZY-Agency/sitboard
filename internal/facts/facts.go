// Package facts holds the one list of what sitboard does, reads, sends and
// shows: facts.yaml. README's generated sections and the website's texts are
// built from it (cmd: internal/facts/gen), and its test checks every fact's
// anchors against the code, so a change to the code a fact rests on fails
// `make check` until someone rereads the fact and confirms it.
//
// A fact has up to three wordings of the same claim: Text (README), Short
// (the website's privacy page and llms.txt) and Brief (the home page and the
// FAQ). A missing Short falls back to Text and a missing Brief to Short.
// Wordings use a small Markdown subset: `code`, **bold** and [text](url);
// Brief wordings use `code` only, because the FAQ's JSON-LD is plain text.
package facts

import (
	_ "embed"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed facts.yaml
var source []byte

// Source is facts.yaml as embedded.
func Source() []byte { return source }

// File is facts.yaml.
type File struct {
	Facts    []Fact              `yaml:"facts"`
	Sections map[string][]string `yaml:"sections"`
	Home     []HomeSection       `yaml:"home"`
	FAQ      []QA                `yaml:"faq"`
	Columns  []Column            `yaml:"columns"`
}

// Column is one of the board's columns, as the page names it.
type Column struct {
	ID    string `yaml:"id" json:"id"`
	Label string `yaml:"label" json:"label"`
}

// Fact is one claim with its wordings and the code it rests on.
type Fact struct {
	ID    string `yaml:"id"`
	Text  string `yaml:"text"`
	Short string `yaml:"short"`
	Brief string `yaml:"brief"`
	// Joins continues the previous fact's list item instead of starting one,
	// so one bullet can rest on facts with different anchors.
	Joins bool `yaml:"joins"`
	// Sub makes the item a nested bullet under the previous one.
	Sub bool `yaml:"sub"`
	// Of names the facts this one words again for another place (a heading,
	// a FAQ answer). It takes their anchors instead of its own, so a change
	// to their code marks this wording as needing a reread too.
	Of []string `yaml:"of"`
	// Keeps lists the words that keep the claim true: an exception or a
	// condition every wording must carry. Each must appear in the fact's
	// text, short and brief, and in every wording of a fact that names this
	// one in of:, so a shorter form cannot drop what the long one qualifies.
	Keeps   []string `yaml:"keeps"`
	Anchors []Anchor `yaml:"anchors"`
}

// Anchor ties a fact to the code. Exactly one of Code, Value, Test, File or
// Field is set.
//
//   - Code: a Go declaration, "<package dir>.<Name>" or
//     "<package dir>.<Type>.<Method>"; its source is hashed, and a change
//     marks the fact as needing a fresh confirm (confirmed.txt).
//   - Value with Is: a Go constant or variable whose value must be Is, a
//     literal value ("8 * time.Minute"), so the number in the text is the
//     code's. The code's expression is evaluated through the package's other
//     constants (eval.go); one that is not a constant is compared as text.
//   - Test: a Go test, "<package dir>.<TestName>", that must exist.
//   - File with Has: a file that must contain Has, for words the page uses.
//   - Field: a page field of internal/contract, "<Type>.<json name>"; every
//     page field must be named by some fact.
type Anchor struct {
	Code  string `yaml:"code"`
	Value string `yaml:"value"`
	Is    string `yaml:"is"`
	Test  string `yaml:"test"`
	File  string `yaml:"file"`
	Has   string `yaml:"has"`
	Field string `yaml:"field"`
}

// HomeSection is the home page's facts for one of its sections.
type HomeSection struct {
	ID     string      `yaml:"id"`
	Title  string      `yaml:"title"`
	Body   []string    `yaml:"body"`
	Points []HomePoint `yaml:"points"`
}

// HomePoint is a key and the facts its value is built from.
type HomePoint struct {
	K string   `yaml:"k"`
	V []string `yaml:"v"`
}

// QA is a FAQ entry; A lists the facts the answer is built from, or literal
// words in "say:" form.
type QA struct {
	Q string `yaml:"q"`
	A []Part `yaml:"a"`
}

// Part is a fact id or, with Say, literal words.
type Part struct {
	ID  string
	Say string
}

// UnmarshalYAML reads a bare string as a fact id and {say: …} as words.
func (p *Part) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		p.ID = n.Value
		return nil
	}
	var m struct {
		Say string `yaml:"say"`
	}
	if err := n.Decode(&m); err != nil {
		return err
	}
	if m.Say == "" {
		return fmt.Errorf("line %d: a FAQ part is a fact id or {say: words}", n.Line)
	}
	p.Say = m.Say
	return nil
}

// Load parses facts.yaml.
func Load() (*File, error) { return Parse(source) }

// Parse parses a facts file and checks that its ids resolve.
func Parse(b []byte) (*File, error) {
	var f File
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("facts.yaml: %w", err)
	}
	if err := f.inherit(); err != nil {
		return nil, err
	}
	return &f, f.validate()
}

// inherit gives a fact with of: the anchors of the facts it words again.
func (f *File) inherit() error {
	var errs []string
	for i := range f.Facts {
		x := &f.Facts[i]
		if len(x.Of) == 0 {
			continue
		}
		if len(x.Anchors) > 0 {
			errs = append(errs, fmt.Sprintf("fact %s: of: and anchors: do not go together; anchor the facts it words", x.ID))
			continue
		}
		for _, id := range x.Of {
			y, ok := f.ByID(id)
			switch {
			case !ok:
				errs = append(errs, fmt.Sprintf("fact %s: of: no fact %q", x.ID, id))
			case len(y.Of) > 0:
				errs = append(errs, fmt.Sprintf("fact %s: of: %s words another fact itself; name that one", x.ID, id))
			default:
				for _, a := range y.Anchors {
					if !slices.Contains(x.Anchors, a) {
						x.Anchors = append(x.Anchors, a)
					}
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("facts.yaml:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// ByID returns the fact with that id.
func (f *File) ByID(id string) (Fact, bool) {
	for _, x := range f.Facts {
		if x.ID == id {
			return x, true
		}
	}
	return Fact{}, false
}

// Form is one of a fact's wordings.
type Form int

// The wordings, longest first.
const (
	FormText Form = iota
	FormShort
	FormBrief
)

// Words returns the fact's wording in that form, falling back to the longer
// one when the form is not given.
func (x Fact) Words(form Form) string {
	switch {
	case form >= FormBrief && x.Brief != "":
		return x.Brief
	case form >= FormShort && x.Short != "":
		return x.Short
	}
	return x.Text
}

var idRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// keeps reports whether the wording w says the words k, compared as plain
// text, ignoring case and line breaks.
func keeps(w, k string) bool {
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(Plain(s)), " ")) }
	return strings.Contains(norm(w), norm(k))
}

func (f *File) validate() error {
	var errs []string
	bad := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	seen := map[string]bool{}
	for _, x := range f.Facts {
		switch {
		case !idRe.MatchString(x.ID):
			bad("fact id %q: use lower-case words joined by hyphens", x.ID)
		case seen[x.ID]:
			bad("fact %s: listed twice", x.ID)
		}
		seen[x.ID] = true
		if strings.TrimSpace(x.Text) == "" {
			bad("fact %s: no text", x.ID)
		}
		if len(x.Anchors) == 0 {
			bad("fact %s: no anchor: name the code, test, value or page field it rests on", x.ID)
		}
		for _, w := range []string{x.Text, x.Short, x.Brief} {
			if err := checkMarkup(w); err != nil {
				bad("fact %s: %v", x.ID, err)
			}
		}
		for _, a := range x.Anchors {
			if err := a.shape(); err != nil {
				bad("fact %s: %v", x.ID, err)
			}
		}
	}
	for _, x := range f.Facts {
		if len(x.Keeps) > 0 && len(x.Of) > 0 {
			bad("fact %s: keeps: goes on the fact it words, not on a wording of it", x.ID)
		}
		for _, k := range x.Keeps {
			if strings.TrimSpace(k) == "" {
				bad("fact %s: an empty keeps: entry", x.ID)
				continue
			}
			for _, y := range f.Facts {
				if y.ID != x.ID && !slices.Contains(y.Of, x.ID) {
					continue
				}
				for _, w := range []struct{ form, s string }{{"text", y.Text}, {"short", y.Short}, {"brief", y.Brief}} {
					if w.s != "" && !keeps(w.s, k) {
						bad("fact %s: its %s does not say %q, which fact %s keeps", y.ID, w.form, k, x.ID)
					}
				}
			}
		}
	}
	used := map[string]bool{}
	use := func(where, id string, plainOnly bool) {
		x, ok := f.ByID(id)
		if !ok {
			bad("%s: no fact %q", where, id)
			return
		}
		used[id] = true
		if plainOnly && hasRichMarkup(x.Words(FormBrief)) {
			bad("%s: fact %s's brief wording has bold or a link; the home page and the FAQ take `code` only", where, id)
		}
	}
	for name, ids := range f.Sections {
		if len(ids) == 0 {
			bad("section %s: empty", name)
		}
		for i, id := range ids {
			use("section "+name, id, false)
			if x, ok := f.ByID(id); ok && i == 0 && x.Joins {
				bad("section %s: starts with fact %s, which joins the item before it", name, id)
			}
			if x, ok := f.ByID(id); ok && x.Joins && x.Sub {
				bad("fact %s: joins and sub do not go together", id)
			}
		}
	}
	for _, h := range f.Home {
		for _, id := range append([]string{h.Title}, h.Body...) {
			if id != "" {
				use("home "+h.ID, id, true)
			}
		}
		for _, p := range h.Points {
			if len(p.V) == 0 {
				bad("home %s: point %q has no fact", h.ID, p.K)
			}
			for _, id := range p.V {
				use("home "+h.ID+" "+p.K, id, true)
			}
		}
	}
	for _, qa := range f.FAQ {
		if len(qa.A) == 0 {
			bad("faq %q: no answer", qa.Q)
		}
		for _, p := range qa.A {
			if p.ID != "" {
				use("faq "+qa.Q, p.ID, true)
			} else if err := checkMarkup(p.Say); err != nil || hasRichMarkup(p.Say) {
				bad("faq %q: say %q: code only", qa.Q, p.Say)
			}
		}
	}
	for _, x := range f.Facts {
		if !used[x.ID] {
			bad("fact %s: not used in any section, home point or FAQ answer", x.ID)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("facts.yaml:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// WebsiteDir is the website's folder, from the top folder. The website is
// not exported and the public tree never names its folder as a path (the
// export refuses one), so paths into it are built from this name.
const WebsiteDir = "site"

// SiteJSONPath is the website's copy of the facts, from the top folder.
const SiteJSONPath = WebsiteDir + "/src/facts.gen.json"

func (a Anchor) shape() error {
	n := 0
	for _, s := range []string{a.Code, a.Value, a.Test, a.File, a.Field} {
		if s != "" {
			n++
		}
	}
	switch {
	case n != 1:
		return fmt.Errorf("anchor %+v: give exactly one of code, value, test, file, field", a)
	case (a.Value != "") != (a.Is != ""):
		return fmt.Errorf("anchor %+v: value and is go together", a)
	case (a.File != "") != (a.Has != ""):
		return fmt.Errorf("anchor %+v: file and has go together", a)
	case strings.HasPrefix(a.File, WebsiteDir+"/"):
		return fmt.Errorf("anchor %s: the website is not exported; anchor on the tool's code", a.File)
	}
	return nil
}

// Key names the anchor in confirmed.txt and in messages.
func (a Anchor) Key() string {
	switch {
	case a.Code != "":
		return "code " + a.Code
	case a.Value != "":
		return "value " + a.Value
	case a.Test != "":
		return "test " + a.Test
	case a.File != "":
		return "file " + a.File
	}
	return "field " + a.Field
}

// ── Markup ──────────────────────────────────────────────────────────────────

var (
	codeRe = regexp.MustCompile("`([^`]+)`")
	boldRe = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	linkRe = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
)

func checkMarkup(s string) error {
	if strings.Count(s, "`")%2 != 0 {
		return fmt.Errorf("unbalanced backtick in %q", s)
	}
	rest := linkRe.ReplaceAllString(s, "$1")
	rest = codeRe.ReplaceAllString(rest, "")
	rest = boldRe.ReplaceAllString(rest, "$1")
	if strings.ContainsAny(rest, "*[]<>") {
		return fmt.Errorf("markup other than `code`, **bold** and [text](url) in %q", s)
	}
	if strings.ContainsAny(s, "\n\t") || strings.Contains(s, "  ") {
		return fmt.Errorf("a line break, tab or double space in %q", s)
	}
	return nil
}

func hasRichMarkup(s string) bool {
	return linkRe.MatchString(s) || boldRe.MatchString(codeRe.ReplaceAllString(s, ""))
}

// HTML renders a wording for the website.
func HTML(s string) string {
	var b strings.Builder
	at := 0
	for _, m := range linkRe.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(inlineHTML(s[at:m[0]]))
		b.WriteString(`<a href="` + html.EscapeString(s[m[4]:m[5]]) + `">` + inlineHTML(s[m[2]:m[3]]) + "</a>")
		at = m[1]
	}
	b.WriteString(inlineHTML(s[at:]))
	return b.String()
}

// inlineHTML renders `code` and **bold**; bold may hold code.
func inlineHTML(s string) string {
	var codes []string
	s = codeRe.ReplaceAllStringFunc(s, func(m string) string {
		codes = append(codes, "<code>"+html.EscapeString(m[1:len(m)-1])+"</code>")
		return "\x00" + strconv.Itoa(len(codes)-1) + "\x00"
	})
	s = boldRe.ReplaceAllString(html.EscapeString(s), "<strong>$1</strong>")
	return slotRe.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.Atoi(m[1 : len(m)-1])
		return codes[n]
	})
}

var slotRe = regexp.MustCompile("\x00[0-9]+\x00")

// Plain renders a wording as text: backticks, bold marks and link targets
// dropped.
func Plain(s string) string {
	s = linkRe.ReplaceAllString(s, "$1")
	s = codeRe.ReplaceAllString(s, "$1")
	return boldRe.ReplaceAllString(s, "$1")
}

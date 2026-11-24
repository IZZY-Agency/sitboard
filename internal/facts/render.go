package facts

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// README marks each generated section with a pair of comments:
//
//	<!-- facts:readme-privacy -->
//	…generated; edit internal/facts/facts.yaml…
//	<!-- /facts:readme-privacy -->
//
// The section is rendered as a bulleted list, or with "paragraph" after the
// name as one paragraph.
var markRe = regexp.MustCompile(`(?s)<!-- facts:([a-z0-9-]+)( paragraph)? -->\n(.*?)<!-- /facts:([a-z0-9-]+) -->`)

// SiteOnly is the prefix of a section only the website shows; every other
// section is README's, and README carries it exactly once.
const SiteOnly = "site-"

var (
	openRe  = regexp.MustCompile(`<!-- facts:([a-z0-9-]+)[^>]*-->`)
	closeRe = regexp.MustCompile(`<!-- /facts:([a-z0-9-]+) -->`)
)

// Readme rewrites the generated sections of a README. Every section it
// names must exist in facts.yaml, and every section facts.yaml gives README
// must be in it once, opened and closed: a lost marker pair would otherwise
// leave that section by hand, outside every check.
func (f *File) Readme(readme []byte) ([]byte, error) {
	var errs []string
	opens, closes, pairs := map[string]int{}, map[string]int{}, map[string]int{}
	for _, g := range openRe.FindAllSubmatch(readme, -1) {
		opens[string(g[1])]++
	}
	for _, g := range closeRe.FindAllSubmatch(readme, -1) {
		closes[string(g[1])]++
	}
	for _, g := range markRe.FindAllSubmatch(readme, -1) {
		pairs[string(g[1])]++
	}
	for name := range f.Sections {
		if strings.HasPrefix(name, SiteOnly) {
			if opens[name]+closes[name] > 0 {
				errs = append(errs, fmt.Sprintf("README: section %s is the website's only", name))
			}
			continue
		}
		if opens[name] != 1 || closes[name] != 1 || pairs[name] != 1 {
			errs = append(errs, fmt.Sprintf("README: section %s must appear once, between <!-- facts:%s --> and <!-- /facts:%s --> (found %d opening, %d closing)", name, name, name, opens[name], closes[name]))
		}
	}
	for name := range opens {
		if _, ok := f.Sections[name]; !ok {
			errs = append(errs, fmt.Sprintf("README: no section %s in facts.yaml", name))
		}
	}
	out := markRe.ReplaceAllFunc(readme, func(m []byte) []byte {
		g := markRe.FindSubmatch(m)
		name, para, end := string(g[1]), len(g[2]) > 0, string(g[4])
		if end != name {
			errs = append(errs, fmt.Sprintf("README: section %s is closed as %s", name, end))
			return m
		}
		ids, ok := f.Sections[name]
		if !ok {
			return m // reported above
		}
		var b strings.Builder
		fmt.Fprintf(&b, "<!-- facts:%s%s -->\n", name, g[2])
		if para {
			words := make([]string, 0, len(ids))
			for _, id := range ids {
				x, _ := f.ByID(id)
				words = append(words, x.Text)
			}
			b.WriteString(strings.Join(words, " ") + "\n")
		} else {
			for _, it := range f.Items(name) {
				if it.Sub {
					b.WriteString("  ")
				}
				b.WriteString("- " + it.Text + "\n")
			}
		}
		fmt.Fprintf(&b, "<!-- /facts:%s -->", name)
		return []byte(b.String())
	})
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return out, nil
}

// Item is one list item of a section: one fact, or several when the later
// ones join it.
type Item struct {
	Sub       bool   `json:"sub"`
	Text      string `json:"text"`
	Short     string `json:"short"`
	ShortHTML string `json:"shortHtml"`
}

// Items renders a section as list items.
func (f *File) Items(section string) []Item {
	var items []Item
	for _, id := range f.Sections[section] {
		x, _ := f.ByID(id)
		if x.Joins && len(items) > 0 {
			last := &items[len(items)-1]
			last.Text += " " + x.Text
			last.Short += " " + x.Words(FormShort)
			last.ShortHTML = HTML(last.Short)
			continue
		}
		items = append(items, Item{Sub: x.Sub, Text: x.Text, Short: x.Words(FormShort), ShortHTML: HTML(x.Words(FormShort))})
	}
	return items
}

// ReadmeSections lists the sections a README marks.
func ReadmeSections(readme []byte) []string {
	var names []string
	for _, g := range markRe.FindAllSubmatch(readme, -1) {
		names = append(names, string(g[1]))
	}
	return names
}

// SiteFact is a fact as the website reads it.
type SiteFact struct {
	Text      string `json:"text"`
	Short     string `json:"short"`
	ShortHTML string `json:"shortHtml"`
	Brief     string `json:"brief"`
}

// SiteHome is a home page section as the website reads it.
type SiteHome struct {
	ID     string      `json:"id"`
	Title  string      `json:"title,omitempty"`
	Body   string      `json:"body,omitempty"`
	Points []SitePoint `json:"points"`
}

// SitePoint is a key and its value.
type SitePoint struct {
	K string `json:"k"`
	V string `json:"v"`
}

// SiteQA is a FAQ entry; the answer is `code`-marked text.
type SiteQA struct {
	Q string `json:"q"`
	A string `json:"a"`
}

// Site is the website's facts.gen.json (SiteJSONPath).
type Site struct {
	Note  string              `json:"//"`
	Facts map[string]SiteFact `json:"facts"`
	Lists map[string][]Item   `json:"lists"`
	Home  []SiteHome          `json:"home"`
	FAQ   []SiteQA            `json:"faq"`
	// Columns are the board's columns in its order, for the home page's
	// Board point.
	Columns []Column `json:"columns"`
	// Plain is every wording and every FAQ "say:" as plain text, for the
	// site's check that a sentence saying "never", "only" or "nothing" comes
	// from this file.
	Plain []string `json:"plain"`
}

func (f *File) brief(ids []string) string {
	words := make([]string, 0, len(ids))
	for _, id := range ids {
		x, _ := f.ByID(id)
		words = append(words, x.Words(FormBrief))
	}
	return Typo(strings.Join(words, " "))
}

var (
	apostropheRe = regexp.MustCompile(`([\p{L}\p{N}.,)])'`)
	openSingleRe = regexp.MustCompile(`'`)
	openDoubleRe = regexp.MustCompile(`"([^"]*)"`)
)

// Typo gives a wording the website's typography: ’ ‘ “ ” for the straight
// quotes README uses, outside `code`.
func Typo(s string) string {
	codes := codeRe.FindAllString(s, -1)
	parts := codeRe.Split(s, -1)
	var b strings.Builder
	for i, p := range parts {
		p = openDoubleRe.ReplaceAllString(p, "“$1”")
		p = apostropheRe.ReplaceAllString(p, "$1’")
		p = openSingleRe.ReplaceAllString(p, "‘")
		b.WriteString(p)
		if i < len(codes) {
			b.WriteString(codes[i])
		}
	}
	return b.String()
}

// SiteJSON renders the website's facts.gen.json, in its typography.
func (f *File) SiteJSON() []byte {
	s := Site{
		Note:    "Generated from internal/facts/facts.yaml by `make facts`; do not edit.",
		Facts:   map[string]SiteFact{},
		Lists:   map[string][]Item{},
		Home:    []SiteHome{},
		FAQ:     []SiteQA{},
		Plain:   []string{},
		Columns: append([]Column{}, f.Columns...),
	}
	for _, x := range f.Facts {
		short := Typo(x.Words(FormShort))
		s.Facts[x.ID] = SiteFact{Text: Typo(x.Text), Short: short, ShortHTML: HTML(short), Brief: Typo(x.Words(FormBrief))}
		for _, w := range []string{x.Text, x.Short, x.Brief} {
			if w != "" {
				s.Plain = append(s.Plain, Plain(Typo(w)))
			}
		}
	}
	names := make([]string, 0, len(f.Sections))
	for name := range f.Sections {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		items := f.Items(name)
		for i := range items {
			items[i].Text, items[i].Short = Typo(items[i].Text), Typo(items[i].Short)
			items[i].ShortHTML = HTML(items[i].Short)
			s.Plain = append(s.Plain, Plain(items[i].Text), Plain(items[i].Short))
		}
		s.Lists[name] = items
	}
	for _, h := range f.Home {
		sh := SiteHome{ID: h.ID, Points: []SitePoint{}}
		if h.Title != "" {
			sh.Title = f.brief([]string{h.Title})
		}
		sh.Body = f.brief(h.Body)
		for _, p := range h.Points {
			sh.Points = append(sh.Points, SitePoint{K: p.K, V: f.brief(p.V)})
			s.Plain = append(s.Plain, p.K)
		}
		s.Home = append(s.Home, sh)
	}
	for _, qa := range f.FAQ {
		words := make([]string, 0, len(qa.A))
		for _, p := range qa.A {
			if p.Say != "" {
				words = append(words, p.Say)
				s.Plain = append(s.Plain, Plain(Typo(p.Say)))
				continue
			}
			x, _ := f.ByID(p.ID)
			words = append(words, x.Words(FormBrief))
		}
		s.FAQ = append(s.FAQ, SiteQA{Q: Typo(qa.Q), A: Typo(strings.Join(words, " "))})
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return append(b, '\n')
}

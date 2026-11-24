package facts

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/server"
)

const root = "../.."

func load(t *testing.T) *File {
	t.Helper()
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func confirmed(t *testing.T) Confirmed {
	t.Helper()
	b, err := os.ReadFile("confirmed.txt")
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseConfirmed(b)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Every anchor resolves: the declaration, test and file exist, and each
// stated value is the code's.
func TestEveryAnchorResolves(t *testing.T) {
	f, repo := load(t), NewRepo(root)
	for _, x := range f.Facts {
		for _, a := range x.Anchors {
			if a.Field != "" {
				continue
			}
			if _, err := repo.Check(a); err != nil {
				t.Errorf("fact %s: %v", x.ID, err)
			}
		}
	}
}

// A change to the code a fact rests on fails here until someone rereads
// the fact, fixes its wording if it is no longer true, and confirms it.
func TestEveryFactIsConfirmedAgainstItsCode(t *testing.T) {
	f, repo, c := load(t), NewRepo(root), confirmed(t)
	want := map[string]bool{}
	for _, x := range f.Facts {
		var stale []string
		for _, a := range x.Anchors {
			if a.Code == "" {
				continue
			}
			key := ConfirmKey(x.ID, a.Code)
			want[key] = true
			h, err := repo.Hash(a.Code)
			if err != nil {
				continue // reported by TestEveryAnchorResolves
			}
			switch got, ok := c[key]; {
			case !ok:
				stale = append(stale, a.Code+" (never confirmed)")
			case got != h:
				stale = append(stale, a.Code+" (changed)")
			}
		}
		if len(stale) > 0 {
			t.Errorf("fact %s rests on code that changed since it was last confirmed: %s.\n"+
				"  Reread its wording in internal/facts/facts.yaml against that code, fix it if it is no longer true,\n"+
				"  then run `make facts-confirm FACTS=%s` and say in the pull request what you checked.",
				x.ID, strings.Join(stale, ", "), x.ID)
		}
	}
	for key := range c {
		if !want[key] {
			t.Errorf("confirmed.txt: %q is no longer an anchor of that fact: run `make facts-confirm FACTS=%s`", key, strings.Fields(key)[0])
		}
	}
}

// pageRoots are the values the server sends to the page, as its routes
// declare them (server.Sends), so a new path is covered without a change here.
func pageRoots(t *testing.T) []any {
	var roots []any
	for _, v := range server.Sends() {
		roots = append(roots, v)
	}
	if len(roots) < 3 {
		t.Fatalf("server.Sends() lists %d paths", len(roots))
	}
	return roots
}

// Every field the page receives is named by a fact, so a new field cannot
// reach the page without a sentence that says so.
func TestEveryPageFieldHasAFact(t *testing.T) {
	f := load(t)
	named := map[string]bool{}
	for _, x := range f.Facts {
		for _, a := range x.Anchors {
			if a.Field != "" {
				named[a.Field] = true
			}
		}
	}
	page := map[string]bool{}
	for _, p := range PageFields(pageRoots(t)...) {
		page[p] = true
		if !named[p] {
			t.Errorf("page field %s is named by no fact: add it to the fact that says what the page gets, or write one", p)
		}
	}
	for n := range named {
		if !page[n] {
			t.Errorf("a fact names page field %s, which the page does not get", n)
		}
	}
}

// The columns facts.yaml lists are the board's, in its order, and the
// wordings that name them all in order say the same.
func TestColumnsAreTheBoards(t *testing.T) {
	f := load(t)
	if len(f.Columns) != len(contract.Columns) {
		t.Fatalf("facts.yaml lists %d columns, the board has %d", len(f.Columns), len(contract.Columns))
	}
	labels := make([]string, len(f.Columns))
	for i, c := range f.Columns {
		if c.ID != string(contract.Columns[i]) {
			t.Errorf("column %d is %s in facts.yaml, %s on the board", i, c.ID, contract.Columns[i])
		}
		labels[i] = c.Label
	}
	order := strings.Join(labels, ", ")
	for _, id := range []string{"board-view", "llms-columns"} {
		x, _ := f.ByID(id)
		if !strings.Contains(x.Text, order) {
			t.Errorf("fact %s does not list the columns in the board's order: %s", id, order)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "web", "src", "data", "derive.ts"))
	if err != nil {
		t.Fatal(err)
	}
	// Each label by its column: COLUMN_META's entry for that id.
	page := pageColumnLabels(string(b))
	if len(page) != len(f.Columns) {
		t.Errorf("web/src/data/derive.ts COLUMN_META has %d column labels, facts.yaml %d", len(page), len(f.Columns))
	}
	for _, c := range f.Columns {
		if got, ok := page[c.ID]; !ok || got != c.Label {
			t.Errorf("column %s: the page's label is %q, facts.yaml says %q (web/src/data/derive.ts COLUMN_META)", c.ID, got, c.Label)
		}
	}
}

var columnMetaRe = regexp.MustCompile(`(?m)^  ([a-z_]+): \{\n    label: '([^']*)',`)

// pageColumnLabels reads the column labels, by column id, from the page's
// COLUMN_META table.
func pageColumnLabels(derive string) map[string]string {
	start := strings.Index(derive, "export const COLUMN_META")
	if start < 0 {
		return nil
	}
	body := derive[start:]
	if end := strings.Index(body, "\n};"); end >= 0 {
		body = body[:end]
	}
	out := map[string]string{}
	for _, m := range columnMetaRe.FindAllStringSubmatch(body, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// A label swapped between two columns keeps both words in the file; the
// check reads them by column and sees it.
func TestColumnLabelsAreReadByColumn(t *testing.T) {
	swapped := "export const COLUMN_META = {\n  needs_you: {\n    label: 'Working',\n  },\n  working: {\n    label: 'Needs you',\n  },\n};\n"
	got := pageColumnLabels(swapped)
	if got["needs_you"] != "Working" || got["working"] != "Needs you" || len(got) != 2 {
		t.Fatalf("read %v", got)
	}
}

// README's generated sections match facts.yaml, and every section README
// carries is one facts.yaml has.
func TestReadmeIsBuiltFromTheFacts(t *testing.T) {
	f := load(t)
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Readme(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(b) {
		t.Error("README.md's generated sections are stale: edit internal/facts/facts.yaml, then run `make facts`")
	}
	if len(ReadmeSections(b)) == 0 {
		t.Error("README.md has no generated section")
	}
}

// The website's copy of the facts is fresh (only where the website is).
func TestSiteIsBuiltFromTheFacts(t *testing.T) {
	path := filepath.Join(root, filepath.FromSlash(SiteJSONPath))
	if _, err := os.Stat(filepath.Join(root, WebsiteDir)); err != nil {
		t.Skip("no website in this checkout")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(load(t).SiteJSON()) {
		t.Errorf("%s is stale: run `make facts`", SiteJSONPath)
	}
}

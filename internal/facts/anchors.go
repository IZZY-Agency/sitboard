package facts

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// A Repo resolves anchors in a checkout of the repository.
type Repo struct {
	Root string
	pkgs map[string]*goPkg
}

type goPkg struct {
	fset  *token.FileSet
	files []*ast.File // without tests
	tests []*ast.File
}

// NewRepo reads anchors below root, the repository's top folder.
func NewRepo(root string) *Repo { return &Repo{Root: root, pkgs: map[string]*goPkg{}} }

func (r *Repo) pkg(dir string) (*goPkg, error) {
	if p, ok := r.pkgs[dir]; ok {
		return p, nil
	}
	full := filepath.Join(r.Root, filepath.FromSlash(dir))
	names, err := filepath.Glob(filepath.Join(full, "*.go"))
	if err != nil || len(names) == 0 {
		return nil, fmt.Errorf("no Go package in %s", dir)
	}
	p := &goPkg{fset: token.NewFileSet()}
	for _, n := range names {
		// Comments are left out, so rewording a comment does not mark a fact.
		f, err := parser.ParseFile(p.fset, n, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(n, "_test.go") {
			p.tests = append(p.tests, f)
		} else {
			p.files = append(p.files, f)
		}
	}
	r.pkgs[dir] = p
	return p, nil
}

// split turns "internal/core.Board.Run" into the package dir and the names.
func split(ref string) (dir string, names []string, err error) {
	slash := strings.LastIndex(ref, "/")
	dot := strings.Index(ref[slash+1:], ".")
	if dot < 0 {
		return "", nil, fmt.Errorf("%q: write <package dir>.<Name>", ref)
	}
	dot += slash + 1
	names = strings.Split(ref[dot+1:], ".")
	if len(names) > 2 {
		return "", nil, fmt.Errorf("%q: at most <Type>.<Method>", ref)
	}
	return ref[:dot], names, nil
}

// decl finds a declaration and returns the node to hash (a FuncDecl, a
// TypeSpec or a ValueSpec) and, for a value, its expression.
func (r *Repo) decl(ref string, tests bool) (ast.Node, ast.Expr, *goPkg, error) {
	dir, names, err := split(ref)
	if err != nil {
		return nil, nil, nil, err
	}
	p, err := r.pkg(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	files := p.files
	if tests {
		files = p.tests
	}
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Name.Name != names[len(names)-1] {
					continue
				}
				if (len(names) == 2) != (d.Recv != nil) {
					continue
				}
				if len(names) == 2 && recvName(d.Recv) != names[0] {
					continue
				}
				return d, nil, p, nil
			case *ast.GenDecl:
				if len(names) != 1 {
					continue
				}
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						if s.Name.Name == names[0] {
							return s, nil, p, nil
						}
					case *ast.ValueSpec:
						for i, n := range s.Names {
							if n.Name != names[0] {
								continue
							}
							var e ast.Expr
							if i < len(s.Values) {
								e = s.Values[i]
							}
							return s, e, p, nil
						}
					}
				}
			}
		}
	}
	return nil, nil, nil, fmt.Errorf("%s: not found", ref)
}

func recvName(l *ast.FieldList) string {
	t := l.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if g, ok := t.(*ast.IndexExpr); ok {
		t = g.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func render(fset *token.FileSet, n ast.Node) string {
	var b bytes.Buffer
	_ = (&printer.Config{Mode: printer.RawFormat}).Fprint(&b, fset, n)
	return b.String()
}

// Hash returns the hash of a code anchor's declaration, as confirmed.txt
// records it.
func (r *Repo) Hash(ref string) (string, error) {
	n, _, p, err := r.decl(ref, false)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(render(p.fset, n)))
	return hex.EncodeToString(sum[:])[:16], nil
}

// Check resolves one anchor that is not a page field; for a code anchor it
// returns the declaration's hash.
func (r *Repo) Check(a Anchor) (hash string, err error) {
	switch {
	case a.Code != "":
		return r.Hash(a.Code)
	case a.Value != "":
		_, e, p, err := r.decl(a.Value, false)
		if err != nil {
			return "", err
		}
		if e == nil {
			return "", fmt.Errorf("%s: has no value in its declaration", a.Value)
		}
		if err := checkValue(p, a.Value, e, a.Is); err != nil {
			return "", err
		}
	case a.Test != "":
		if _, _, _, err := r.decl(a.Test, true); err != nil {
			return "", fmt.Errorf("test %w", err)
		}
	case a.File != "":
		b, err := os.ReadFile(filepath.Join(r.Root, filepath.FromSlash(a.File)))
		if err != nil {
			return "", err
		}
		if !bytes.Contains(b, []byte(a.Has)) {
			return "", fmt.Errorf("%s no longer contains %q", a.File, a.Has)
		}
	}
	return "", nil
}

// ── confirmed.txt ───────────────────────────────────────────────────────────

// Confirmed maps "<fact id> <code ref>" to the hash it was confirmed at.
type Confirmed map[string]string

// ConfirmKey is a confirmed.txt key.
func ConfirmKey(id, ref string) string { return id + " " + ref }

// ParseConfirmed reads confirmed.txt: "<fact id> <code ref> <hash>" lines;
// # starts a comment.
func ParseConfirmed(b []byte) (Confirmed, error) {
	c := Confirmed{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, fmt.Errorf("confirmed.txt:%d: want <fact id> <code ref> <hash>", n)
		}
		c[ConfirmKey(f[0], f[1])] = f[2]
	}
	return c, sc.Err()
}

// ConfirmedHeader opens confirmed.txt.
const ConfirmedHeader = `# Each line: a fact, one of its code anchors, and the hash of that code when
# someone last reread the fact against it. Written by
# ` + "`make facts-confirm FACTS=\"<id> …\"`" + `; never edit by hand.
`

// ConfirmInto records in c the current hash of every code anchor of the
// given facts, dropping their old lines. An id that is no longer in the file
// but still has lines in c is a retired or renamed fact: its lines are
// dropped. An id in neither is a mistake.
func ConfirmInto(c Confirmed, f *File, repo *Repo, ids []string) ([]string, error) {
	var msgs []string
	for _, id := range ids {
		x, ok := f.ByID(id)
		had := false
		for k := range c {
			if strings.HasPrefix(k, id+" ") {
				had = true
			}
		}
		if !ok && !had {
			return nil, fmt.Errorf("no fact %q in facts.yaml or confirmed.txt", id)
		}
		for k := range c {
			if strings.HasPrefix(k, id+" ") {
				delete(c, k)
			}
		}
		if !ok {
			msgs = append(msgs, "retired "+id)
			continue
		}
		for _, a := range x.Anchors {
			if a.Code == "" {
				continue
			}
			h, err := repo.Hash(a.Code)
			if err != nil {
				return nil, fmt.Errorf("fact %s: %w", id, err)
			}
			c[ConfirmKey(id, a.Code)] = h
		}
		msgs = append(msgs, "confirmed "+id)
	}
	return msgs, nil
}

// Format writes confirmed.txt, sorted.
func (c Confirmed) Format() []byte {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(ConfirmedHeader)
	for _, k := range keys {
		b.WriteString(k + " " + c[k] + "\n")
	}
	return []byte(b.String())
}

// ── Page fields ─────────────────────────────────────────────────────────────

// PageFields lists "<Type>.<json name>" for every field reachable from the
// given values' types, the way the page receives them.
func PageFields(roots ...any) []string {
	seen := map[reflect.Type]bool{}
	var out []string
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] || t.PkgPath() == "time" {
			return
		}
		seen[t] = true
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if f.Anonymous && name == "" {
				walk(f.Type)
				continue
			}
			if name == "" {
				name = f.Name
			}
			out = append(out, t.Name()+"."+name)
			walk(f.Type)
		}
	}
	for _, r := range roots {
		walk(reflect.TypeOf(r))
	}
	sort.Strings(out)
	return out
}

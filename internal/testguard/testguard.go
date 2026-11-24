// Package testguard lets a package's tests check how the package's own test
// files are written, for rules a type checker cannot enforce (which
// constructor a test must use). Tests only.
package testguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"testing"
)

// OnlyIn fails t for each use, in the _test.go files of the current folder, of
// one of the names of this package (a call or any other reference; x.Name,
// another package's, does not count) outside the functions allowed to use
// them. Package-level declarations are never allowed to.
func OnlyIn(t *testing.T, names, allowed []string) {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files to read: %v", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			where := "a package-level declaration"
			var skip *ast.Ident // the function's own name
			if fn, ok := d.(*ast.FuncDecl); ok {
				if slices.Contains(allowed, fn.Name.Name) {
					continue
				}
				where, skip = fn.Name.Name, fn.Name
			}
			ast.Inspect(d, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					ast.Inspect(n.X, func(m ast.Node) bool { // n.Sel is another package's or a field
						if id, ok := m.(*ast.Ident); ok && slices.Contains(names, id.Name) {
							t.Errorf("%s: %s uses %s; only %v may", fset.Position(id.Pos()), where, id.Name, allowed)
						}
						return true
					})
					return false
				case *ast.Ident:
					if n != skip && slices.Contains(names, n.Name) {
						t.Errorf("%s: %s uses %s; only %v may", fset.Position(n.Pos()), where, n.Name, allowed)
					}
				}
				return true
			})
		}
	}
}

// NoCall fails t for each use, in the _test.go files of the current folder, of
// pkg.name (for example state.Open): a test must use the variant that takes
// its clock.
func NoCall(t *testing.T, pkg, name string) {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files to read: %v", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
					t.Errorf("%s: uses %s.%s", fset.Position(sel.Pos()), pkg, name)
				}
			}
			return true
		})
	}
}

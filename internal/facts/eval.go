package facts

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"strings"
	"time"
)

// A value anchor compares what the code's expression evaluates to, not its
// text: `renewAfter = knownFor - 2*time.Minute` is eight minutes only while
// knownFor is ten, so the anchor follows knownFor too. Package-level
// constants and variables of the anchor's package, the time units and
// conversions such as time.Duration(n) are resolved; anything else (a
// composite literal, a call) is compared as text.

var timeUnits = map[string]time.Duration{
	"Nanosecond": time.Nanosecond, "Microsecond": time.Microsecond, "Millisecond": time.Millisecond,
	"Second": time.Second, "Minute": time.Minute, "Hour": time.Hour,
}

var conversions = map[string]bool{
	"int": true, "int32": true, "int64": true, "uint": true, "uint32": true, "uint64": true,
	"float64": true, "time.Duration": true,
}

// evaluator evaluates constant expressions of one package; with pkg nil it
// takes literals and the time units only.
type evaluator struct {
	pkg   *goPkg
	depth int
}

func (ev evaluator) eval(e ast.Expr) (constant.Value, error) {
	if ev.depth > 20 {
		return nil, fmt.Errorf("too deep")
	}
	switch e := e.(type) {
	case *ast.BasicLit:
		v := constant.MakeFromLiteral(e.Value, e.Kind, 0)
		if v.Kind() == constant.Unknown {
			return nil, fmt.Errorf("literal %s", e.Value)
		}
		return v, nil
	case *ast.ParenExpr:
		return ev.eval(e.X)
	case *ast.UnaryExpr:
		x, err := ev.eval(e.X)
		if err != nil {
			return nil, err
		}
		if e.Op != token.SUB && e.Op != token.ADD && e.Op != token.NOT {
			return nil, fmt.Errorf("operator %s", e.Op)
		}
		return constant.UnaryOp(e.Op, x, 0), nil
	case *ast.BinaryExpr:
		x, err := ev.eval(e.X)
		if err != nil {
			return nil, err
		}
		y, err := ev.eval(e.Y)
		if err != nil {
			return nil, err
		}
		switch e.Op {
		case token.ADD, token.SUB, token.MUL, token.QUO:
		default:
			return nil, fmt.Errorf("operator %s", e.Op)
		}
		op := e.Op
		if op == token.QUO && x.Kind() == constant.Int && y.Kind() == constant.Int {
			if constant.Sign(y) == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			op = token.QUO_ASSIGN // integer division
		}
		return constant.BinaryOp(x, op, y), nil
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok && id.Name == "time" {
			if d, ok := timeUnits[e.Sel.Name]; ok {
				return constant.MakeInt64(int64(d)), nil
			}
		}
		return nil, fmt.Errorf("%s", exprString(e))
	case *ast.CallExpr:
		if len(e.Args) == 1 && conversions[exprString(e.Fun)] {
			return ev.eval(e.Args[0])
		}
		return nil, fmt.Errorf("call %s", exprString(e.Fun))
	case *ast.Ident:
		switch e.Name {
		case "true":
			return constant.MakeBool(true), nil
		case "false":
			return constant.MakeBool(false), nil
		}
		if ev.pkg == nil {
			return nil, fmt.Errorf("%s: name a literal value, not a name from the code", e.Name)
		}
		x, ok := ev.pkg.value(e.Name)
		if !ok {
			return nil, fmt.Errorf("%s: not a package-level value", e.Name)
		}
		return evaluator{pkg: ev.pkg, depth: ev.depth + 1}.eval(x)
	}
	return nil, fmt.Errorf("%T", e)
}

func exprString(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	}
	return fmt.Sprintf("%T", e)
}

// value finds a package-level constant's or variable's expression.
func (p *goPkg) value(name string) (ast.Expr, bool) {
	for _, f := range p.files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, s := range g.Specs {
				vs, ok := s.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					if n.Name == name && i < len(vs.Values) {
						return vs.Values[i], true
					}
				}
			}
		}
	}
	return nil, false
}

// checkValue compares a value anchor's expression with its stated value.
func checkValue(p *goPkg, ref string, e ast.Expr, is string) error {
	got, err := evaluator{pkg: p}.eval(e)
	if err != nil {
		// Not a constant expression: the text must match.
		if text := render(p.fset, e); text != is {
			return fmt.Errorf("%s is %s now, the fact says %s: reread the fact's wording, then update its is: line", ref, text, is)
		}
		return nil
	}
	ie, err := parser.ParseExpr(is)
	if err != nil {
		return fmt.Errorf("%s: is: %q does not parse: %v", ref, is, err)
	}
	want, err := evaluator{}.eval(ie)
	if err != nil {
		return fmt.Errorf("%s: is: %q: %v", ref, is, err)
	}
	if !sameValue(got, want) {
		return fmt.Errorf("%s is %s now, the fact says %s: reread the fact's wording, then update its is: line",
			ref, describe(got, is), describe(want, is))
	}
	return nil
}

func sameValue(a, b constant.Value) bool {
	if a.Kind() != b.Kind() {
		if (a.Kind() == constant.Int || a.Kind() == constant.Float) && (b.Kind() == constant.Int || b.Kind() == constant.Float) {
			return constant.Compare(constant.ToFloat(a), token.EQL, constant.ToFloat(b))
		}
		return false
	}
	return constant.Compare(a, token.EQL, b)
}

// describe prints a value, as a duration when the fact states one.
func describe(v constant.Value, is string) string {
	if strings.Contains(is, "time.") {
		if n, ok := constant.Int64Val(v); ok {
			return time.Duration(n).String()
		}
	}
	return v.ExactString()
}

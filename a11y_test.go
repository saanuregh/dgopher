package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// namedControls are MyGo's controls that need a name for assistive
// technology, which they have no text of their own to give.
var namedControls = []string{"TextInput", "TextAreaBase", "TextArea", "Select", "Autocomplete", "NumberInput", "TokenField",
	"Segmented", "Slider", "Switch", "DateInput", "ColorPicker", "ButtonBase"}

// Every control the screen reader announces has a name: its Label, or
// the Field it is the first control of.
//
// A wrapper that names its control with one of its parameters, as
// widgets.ToolButton's label, passes the name on to its callers: a call
// of it with "" there is unnamed too. Not checked: a name that is a
// variable or an expression empty at run time, and a control built in
// one statement and named in another.
func TestControlsAreNamed(t *testing.T) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	err := filepath.WalkDir("internal", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		files[path] = f
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	wrappers := map[string]int{}
	for _, f := range files {
		labelWrappers(f, wrappers)
	}
	for _, f := range files {
		var stack []ast.Node
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if control(call) != "" && !named(stack) && !firstInField(stack) {
				t.Errorf("%s: ui.%s has no name: give it a Label, or put it first in a Field", fset.Position(call.Pos()), control(call))
			}
			if name, i, ok := wrapperCall(f, call, wrappers); ok && i < len(call.Args) && emptyString(call.Args[i]) && !named(stack) && !firstInField(stack) {
				t.Errorf("%s: %s names its control with an empty label: give it one", fset.Position(call.Pos()), name)
			}
			return true
		})
	}
}

// labelWrappers adds to wrappers the functions of f that return a control
// named by one of their parameters, as "package.Function", with that
// parameter's index.
func labelWrappers(f *ast.File, wrappers map[string]int) {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil {
			continue
		}
		params := map[string]int{}
		i := 0
		for _, field := range fn.Type.Params.List {
			for _, name := range field.Names {
				params[name.Name] = i
				i++
			}
			if len(field.Names) == 0 {
				i++
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			// A control's chain: ui.X(...).A(...).Label(param)...
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Label" || len(call.Args) != 1 || !chainOfControl(sel.X) {
				return true
			}
			if id, ok := call.Args[0].(*ast.Ident); ok {
				if at, ok := params[id.Name]; ok {
					wrappers[f.Name.Name+"."+fn.Name.Name] = at
				}
			}
			return true
		})
	}
}

// chainOfControl reports whether e is a control's call, or calls chained
// on one.
func chainOfControl(e ast.Expr) bool {
	for {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return false
		}
		if control(call) != "" {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		e = sel.X
	}
}

// wrapperCall reports whether call is of a wrapper, from its package or
// another, with its name and the index of its label parameter.
func wrapperCall(f *ast.File, call *ast.CallExpr, wrappers map[string]int) (string, int, bool) {
	var name string
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		name = f.Name.Name + "." + fun.Name
	case *ast.SelectorExpr:
		pkg, ok := fun.X.(*ast.Ident)
		if !ok {
			return "", 0, false
		}
		name = pkg.Name + "." + fun.Sel.Name
	default:
		return "", 0, false
	}
	i, ok := wrappers[name]
	return name, i, ok
}

// emptyString reports whether e is the literal "".
func emptyString(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && (lit.Value == `""` || lit.Value == "``")
}

// control is the name of the control a call makes, "" for another call.
func control(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "ui" || !slices.Contains(namedControls, sel.Sel.Name) {
		return ""
	}
	return sel.Sel.Name
}

// named reports whether the control at the top of the stack has a Label
// set in the chain of calls on it.
func named(stack []ast.Node) bool {
	// Up the chain: control(...).Font(...).Label(...) nests the control's
	// call in selectors and calls of its own.
	for i := len(stack) - 2; i >= 0; i-- {
		switch n := stack[i].(type) {
		case *ast.SelectorExpr:
			if n.Sel.Name == "Label" {
				return true
			}
		case *ast.CallExpr:
			if _, ok := n.Fun.(*ast.SelectorExpr); !ok || stack[i+1] != n.Fun {
				return false
			}
		default:
			return false
		}
	}
	return false
}

// firstInField reports whether the control at the top of the stack is the
// first control the function of a ui.Field builds, in it or in rows and
// columns within it, which the field's text names.
func firstInField(stack []ast.Node) bool {
	call := stack[len(stack)-1].(*ast.CallExpr)
	for i := len(stack) - 2; i > 0; i-- {
		fn, ok := stack[i].(*ast.FuncLit)
		if !ok {
			continue
		}
		// A function of a row or a column within the field's: further up.
		field, ok := stack[i-1].(*ast.CallExpr)
		if !ok {
			continue
		}
		if sel, ok := field.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Field" || len(field.Args) != 3 || field.Args[2] != fn {
			continue
		}
		var first *ast.CallExpr
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && first == nil && control(c) != "" {
				first = c
			}
			return first == nil
		})
		return first == call
	}
	return false
}

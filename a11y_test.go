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
func TestControlsAreNamed(t *testing.T) {
	fset := token.NewFileSet()
	err := filepath.WalkDir("internal", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		var stack []ast.Node
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			if call, ok := n.(*ast.CallExpr); ok && control(call) != "" && !named(stack) && !firstInField(stack) {
				t.Errorf("%s: ui.%s has no name: give it a Label, or put it first in a Field", fset.Position(call.Pos()), control(call))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
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

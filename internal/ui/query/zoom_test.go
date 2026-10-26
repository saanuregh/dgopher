package query

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// Pinching the editor zooms the editors' text, and pinching the results
// the grids': each its own setting, in whole sizes.
func TestPinchZoomsTheText(t *testing.T) {
	a, tt, q, _ := numbersEditor(t, 3, "SELECT * FROM n")
	runWith(t, tt, q, "SELECT", RunStatement)
	s := a.Settings()
	editor, ok := tt.Find("SQL editor")
	if !ok {
		t.Fatal("no editor")
	}
	tt.Move(editor.X+editor.W/2, editor.Y+editor.H/2)
	tt.Pinch(editor.X+editor.W/2, editor.Y+editor.H/2, 1.2)
	if s.EditorFont != 16 || s.GridFont != 0 {
		t.Fatalf("pinched the editor: editor font %v, grid font %v; want 16 and the default", s.EditorFont, s.GridFont)
	}
	results, ok := tt.Find("Results")
	if !ok {
		t.Fatal("no results")
	}
	tt.Move(results.X+results.W/2, results.Y+results.H/2)
	tt.Pinch(results.X+results.W/2, results.Y+results.H/2, 0.8)
	if s.GridFont != 10 || s.EditorFont != 16 {
		t.Fatalf("pinched the grid: grid font %v, editor font %v; want 10 and 16", s.GridFont, s.EditorFont)
	}
}

// ⌘= and ⌘- zoom the text of what has the focus, the editor or the grid,
// and ⌘K ⌘0 puts it back; ⌘ with the wheel zooms the editor.
func TestZoomKeys(t *testing.T) {
	a, tt, q, _ := numbersEditor(t, 3, "SELECT * FROM n")
	runWith(t, tt, q, "SELECT", RunStatement)
	s := a.Settings()
	q.Editor.WantFocus = true
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyEqual)
	tt.Key(ui.Cmd, ui.KeyEqual)
	tt.Key(ui.Cmd, ui.KeyMinus)
	if s.EditorFont != 14 || s.GridFont != 0 {
		t.Fatalf("keys in the editor: editor font %v, grid font %v; want 14 and the default", s.EditorFont, s.GridFont)
	}
	if !q.editorInput(ui.InputEvent{Kind: ui.InputScroll, DY: -75, Mods: ui.Cmd, Precise: true}) || s.EditorFont != 15 {
		t.Fatalf("⌘ with the wheel up: editor font %v, want 15", s.EditorFont)
	}
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Key(ui.Cmd, ui.Key0)
	if s.EditorFont != 13 {
		t.Fatalf("⌘K ⌘0 in the editor: editor font %v, want 13", s.EditorFont)
	}
	if err := tt.Click("row 1"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyMinus)
	if s.GridFont != 11.5 || s.EditorFont != 13 {
		t.Fatalf("⌘- in the grid: grid font %v, editor font %v; want 11.5 and 13", s.GridFont, s.EditorFont)
	}
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Key(ui.Cmd, ui.Key0)
	if s.GridFont != 0 {
		t.Fatalf("⌘K ⌘0 in the grid: grid font %v, want the default", s.GridFont)
	}
}

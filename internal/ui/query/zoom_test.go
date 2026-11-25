package query

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// A pinch over the editor or over the results, which take their own
// input, zooms the window, and so does the wheel with ⌘ held; the text's
// sizes stay as the settings have them.
func TestGesturesOverEditorAndResultsZoomTheWindow(t *testing.T) {
	a, tt, q, _ := numbersEditor(t, 3, "SELECT * FROM n")
	runWith(t, tt, q, "SELECT", RunStatement)
	s := a.Settings()
	editorFont, gridFont := s.EditorFont, s.GridFont
	editor, ok := tt.Find("SQL editor")
	if !ok {
		t.Fatal("no editor")
	}
	tt.Move(editor.X+editor.W/2, editor.Y+editor.H/2)
	tt.Pinch(editor.X+editor.W/2, editor.Y+editor.H/2, 1.2)
	if a.Zoomed != 1 {
		t.Fatalf("pinched out over the editor: the window zoomed %d steps, want 1", a.Zoomed)
	}
	results, ok := tt.Find("Results")
	if !ok {
		t.Fatal("no results")
	}
	tt.Move(results.X+results.W/2, results.Y+results.H/2)
	tt.Pinch(results.X+results.W/2, results.Y+results.H/2, 0.8)
	if a.Zoomed != 0 {
		t.Fatalf("pinched in over the results: the window is %d steps from where it was, want 0", a.Zoomed)
	}
	if !q.editorInput(ui.InputEvent{Kind: ui.InputScroll, DY: -75, Mods: ui.Cmd, Precise: true}) || a.Zoomed != 1 {
		t.Fatalf("⌘ with the wheel up over the editor: the window zoomed %d steps, want 1", a.Zoomed)
	}
	if s.EditorFont != editorFont || s.GridFont != gridFont {
		t.Errorf("the gestures changed the text's sizes: editor %v, grid %v", s.EditorFont, s.GridFont)
	}
}

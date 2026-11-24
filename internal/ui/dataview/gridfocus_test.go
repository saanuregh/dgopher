package dataview

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// A cell edited in place and kept with Enter leaves the keys with the
// grid: the next arrow moves the chosen cell.
func TestKeysReturnToTheGridAfterAnEdit(t *testing.T) {
	_, tt, tb, _ := openNumbers(t, 80, 100)
	tt.Frame()
	g := tb.view.grid
	h, ok := tt.Find("label")
	if !ok {
		t.Fatal("no label column")
	}
	tt.ClickAt(h.X+10, h.Y+h.H+12)
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if g.SelRow != 1 {
		t.Fatalf("before the edit, Down chose row %d, want 1", g.SelRow)
	}
	first, _ := g.List.Visible()
	tt.Key(0, ui.KeyF2)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("edited")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if g.editing != nil || g.edits.count() != 1 {
		t.Fatalf("Enter: editing %v, %d changes", g.editing != nil, g.edits.count())
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if now, _ := g.List.Visible(); g.SelRow != 2 || now != first {
		t.Errorf("after the edit, Down chose row %d (want 2) and the rows start at %d (want %d)", g.SelRow, now, first)
	}
}

// Esc leaves the edit and the keys with the grid too.
func TestKeysReturnToTheGridAfterEscape(t *testing.T) {
	_, tt, tb, _ := openNumbers(t, 80, 100)
	tt.Frame()
	g := tb.view.grid
	h, _ := tt.Find("label")
	tt.ClickAt(h.X+10, h.Y+h.H+12)
	tt.Frame()
	tt.Key(0, ui.KeyF2)
	tt.Frame()
	tt.Type("dropped")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if g.editing != nil || g.edits.count() != 0 || g.SelRow != 1 {
		t.Errorf("after Esc, editing %v, %d changes, Down chose row %d (want 1)", g.editing != nil, g.edits.count(), g.SelRow)
	}
}

// The record view's fields get the keys back the same way.
func TestKeysReturnToTheRecordAfterAnEdit(t *testing.T) {
	_, tt, tb, _ := openNumbers(t, 80, 100)
	g := tb.view.grid
	g.SelRow, g.mode = 0, viewRecord
	tt.Frame()
	// A click on the second field's name gives its list the keys.
	if err := tt.Click("label"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyF2)
	tt.Frame()
	if g.editing == nil || g.recRow != 1 {
		t.Fatalf("F2 on the record's field %d: editing %v", g.recRow, g.editing != nil)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("edited")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if g.editing != nil || g.edits.count() != 1 || g.recRow != 0 {
		t.Errorf("after the edit, editing %v, %d changes, Up chose field %d (want 0)", g.editing != nil, g.edits.count(), g.recRow)
	}
}

// Tab shows the chosen row as a record and Tab again the grid: the keys
// follow, so the second Tab is the record's and the arrows the grid's.
func TestTabGoesToTheRecordAndBack(t *testing.T) {
	_, tt, tb, _ := openNumbers(t, 80, 100)
	tt.Frame()
	g := tb.view.grid
	h, ok := tt.Find("label")
	if !ok {
		t.Fatal("no label column")
	}
	tt.ClickAt(h.X+10, h.Y+h.H+12)
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	tt.Frame()
	if g.mode != viewRecord {
		t.Fatalf("Tab in the grid: mode %v, want the record", g.mode)
	}
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	tt.Frame()
	if g.mode != viewGrid {
		t.Fatalf("Tab in the record: mode %v, want the grid", g.mode)
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if g.SelRow != 1 {
		t.Errorf("back in the grid, Down chose row %d, want 1", g.SelRow)
	}
}

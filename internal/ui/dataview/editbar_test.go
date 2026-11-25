package dataview

import (
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// The first change of a cell moves no row: what waits to be applied shows
// in the bar under the rows, with the buttons that add and delete them.
func TestEditBarKeepsRowsInPlace(t *testing.T) {
	_, tt, tb, _ := openNumbers(t, 5, 100)
	tt.Frame()
	before, _ := tt.Find("row 2")
	add, ok := tt.Access("Add Row")
	if !ok || add.Bounds.Y < before.Y {
		t.Fatalf("Add Row at %v, above the rows at %v", add.Bounds, before)
	}
	v := tb.view
	v.grid.setValue(&v.src, 0, 1, db.Typed("edited"))
	after := testutil.Settled(t, tt, "row 2")
	if after.Y != before.Y {
		t.Errorf("the rows moved from %v to %v as a change came", before.Y, after.Y)
	}
	for _, name := range []string{"1 pending change", "Undo", "Discard All", "Review and Apply"} {
		r, ok := tt.Find(name)
		if !ok || r.Y < after.Y {
			t.Errorf("%s at %v (found %v), not under the rows at %v", name, r, ok, after.Y)
		}
	}
	testutil.Snapshot(t, tt, "edit-bar-pending")
	if err := tt.Click("Discard All"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if v.grid.edits.count() != 0 || tt.HasText("Review and Apply") {
		t.Errorf("%d changes after Discard All", v.grid.edits.count())
	}
}

// Rows that cannot be edited say why in the bar, beside the buttons that
// would edit them, which stay in their place, unavailable.
func TestEditBarSaysWhyRowsAreReadOnly(t *testing.T) {
	_, tt, tb, _ := openNumbers(t, 5, 100)
	tb.view.source.Conn.Config.ReadOnly = true
	tt.Frame()
	why, ok := tt.Find("The connection is read-only.")
	add, _ := tt.Access("Add Row")
	if !ok || why.Y < add.Bounds.Y-8 || why.Y > add.Bounds.Y+add.Bounds.H {
		t.Fatalf("the reason at %v (found %v), not beside Add Row at %v", why, ok, add.Bounds)
	}
	testutil.Settled(t, tt, "The connection is read-only.")
	testutil.Snapshot(t, tt, "edit-bar-read-only")
	if !add.Disabled || !add.Focusable {
		t.Errorf("Add Row disabled %v, focusable %v: unavailable, the keys still reach its reason", add.Disabled, add.Focusable)
	}
	if err := tt.Click("Add Row"); err == nil {
		tt.Frame()
	}
	if n := tb.view.grid.edits.count(); n != 0 {
		t.Errorf("Add Row added %d rows to rows that are read-only", n)
	}
}

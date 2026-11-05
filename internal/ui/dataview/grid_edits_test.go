package dataview

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// openCellEditor starts editing a cell of a table's rows and types text
// into it, leaving the editor open.
func openCellEditor(t *testing.T, tt *ui.Tester, v *Viewer, row, col int, text string) {
	t.Helper()
	v.grid.startEdit(&v.src, row, col)
	tt.Frame()
	tt.Frame()
	tt.Type(text)
	tt.Frame()
	if v.grid.editing == nil || !strings.HasSuffix(v.grid.editing.value, text) {
		t.Fatalf("editor %+v", v.grid.editing)
	}
}

// ⌘S with a cell editor open applies what was typed in it, rather than
// dropping it.
func TestApplyKeyCommitsOpenCellEditor(t *testing.T) {
	a, tt, tb, file := openNumbers(t, 3, 100)
	v := tb.view
	openCellEditor(t, tt, v, 0, 1, "typed")
	tt.Key(ui.Cmd, ui.KeyS)
	testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the apply", func() bool { return !v.applying && !v.loading && v.grid.edits.count() == 0 })
	if got := labelOf(t, file, 1); got != "row 1typed" {
		t.Fatalf("label %q", got)
	}
}

// Reading the rows again, or closing, with a cell editor open counts what
// was typed as a pending change, and asks.
func TestOpenCellEditorCountsAsPending(t *testing.T) {
	a, tt, tb, _ := openNumbers(t, 3, 100)
	v := tb.view
	openCellEditor(t, tt, v, 0, 1, "typed")
	if tb.CloseReason() == "" {
		t.Error("closing would drop the typed value unasked")
	}
	tb.RequestReload()
	if a.Pending == nil || a.Pending.N != 1 {
		t.Fatalf("refresh asked %+v", a.Pending)
	}
	a.Pending.Cancel()
	a.Pending = nil
	tt.Frame()
	if v.grid.edits.updates[0][1] != db.Typed("row 1typed") {
		t.Fatalf("Cancel lost the typed value: %+v", v.grid.edits.updates)
	}
}

// Copying from the row above or below never puts an added row's default,
// or a pending DEFAULT, into a read row.
func TestCopyFromNeighbourKeepsDefaultsOutOfReadRows(t *testing.T) {
	g, src := editableGrid()
	g.addRow(src)
	g.editing = nil
	g.SelRow, g.selCol = 1, 2 // the last read row, above the added one
	g.copyFromNeighbour(src, 1)
	if v, ok := g.edits.updates[1][2]; ok {
		t.Errorf("an added row's default copied: %#v", v)
	}
	g.setCellDefault(src, 0, 2)
	g.SelRow = 1
	g.copyFromNeighbour(src, -1)
	if v, ok := g.edits.updates[1][2]; ok {
		t.Errorf("DEFAULT copied into a read row: %#v", v)
	}
	// Into the added row, a DEFAULT is its default.
	g.setCellDefault(src, 1, 2)
	g.SelRow = 2
	g.copyFromNeighbour(src, -1)
	if v := g.edits.inserted[0][2]; v != unset {
		t.Errorf("added row holds %#v", v)
	}
}

// Delete on a selection marks every row of it for deletion when some are
// not marked, and restores them all when all are.
func TestDeleteMarksMixedSelection(t *testing.T) {
	g, src := editableGrid()
	g.edits.deleted[0] = true
	g.selection.Add(0)
	g.selection.Add(1)
	g.deleteSelected(src)
	if !g.edits.deleted[0] || !g.edits.deleted[1] {
		t.Fatalf("mixed: %v", g.edits.deleted)
	}
	g.deleteSelected(src)
	if len(g.edits.deleted) != 0 {
		t.Fatalf("all marked: %v", g.edits.deleted)
	}
}

// A row deleted and added again with the same key applies: deletes go
// first, then updates, then inserts.
func TestApplyDeletesBeforeInserts(t *testing.T) {
	a, tt, tb, file := openNumbers(t, 3, 100)
	v := tb.view
	v.grid.edits.deleted[0] = true
	v.grid.addRow(&v.src)
	v.grid.editing = nil
	v.grid.edits.inserted[0][0] = db.Typed("1")
	v.grid.edits.inserted[0][1] = db.Typed("again")
	v.Review(nil)
	testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil || len(a.Reports) > 0 || len(a.Errors) > 0 })
	if a.Confirm == nil {
		t.Fatalf("no review: %+v %+v", a.Reports, a.Errors)
	}
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the apply", func() bool { return !v.applying && !v.loading })
	if v.grid.edits.count() != 0 {
		t.Fatalf("not applied: %v", a.Reports)
	}
	if got := labelOf(t, file, 1); got != "again" {
		t.Fatalf("label %q", got)
	}
}

// Enter on a NULL cell's editor, untouched, sets it to the empty text;
// leaving the editor untouched keeps NULL.
func TestEnterOnNullCellSetsEmptyText(t *testing.T) {
	g, src := editableGrid()
	g.startEdit(src, 0, 2)
	g.commitEdit(src, false)
	if _, ok := g.edits.updates[0][2]; ok {
		t.Fatal("leaving the editor changed NULL")
	}
	g.startEdit(src, 0, 2)
	g.commitEdit(src, true)
	if v := g.edits.updates[0][2]; v != db.Typed("") {
		t.Fatalf("Enter set %#v", v)
	}
	// A column of another type keeps NULL: '' is not a value of it.
	src.Cols[0].Type, src.Rows[0][0] = "INTEGER", nil
	g.startEdit(src, 0, 0)
	g.commitEdit(src, true)
	if v, ok := g.edits.updates[0][0]; ok {
		t.Fatalf("Enter on an INTEGER NULL set %#v", v)
	}
}

func TestHoldsText(t *testing.T) {
	for typ, want := range map[string]bool{
		"TEXT": true, "VARCHAR": true, "varchar(20)": true, "NVARCHAR": true, "VARCHAR2": true, "bpchar": true,
		"CHAR": true, "LONGTEXT": true, "CLOB": true, "String": true, "Nullable(String)": true, "citext": true, "": true,
		"INTEGER": false, "int8": false, "NUMERIC": false, "DATE": false, "timestamp": false, "BOOLEAN": false, "uuid": false, "jsonb": false,
	} {
		if got := holdsText(typ); got != want {
			t.Errorf("holdsText(%q) = %v, want %v", typ, got, want)
		}
	}
}

// Scrolled to the end of the rows read with changes pending, the status
// says why no more rows come.
func TestScrollWithPendingChangesSaysWhy(t *testing.T) {
	_, tt, tb, _ := openNumbers(t, 120, 100)
	v := tb.view
	v.grid.setValue(&v.src, 0, 1, db.Typed("edited"))
	v.grid.List.ScrollToEnd()
	for range 5 {
		tt.Frame()
	}
	if !testutil.HasTextContaining(tt, pendingHoldsRows) || len(v.src.Rows) != 100 {
		t.Fatalf("%d rows; texts %q", len(v.src.Rows), tt.Texts())
	}
}

package dataview

import (
	"slices"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

func TestDateText(t *testing.T) {
	for _, c := range []struct {
		text     string
		withTime bool
		want     string // after moving a day on and to 09:30
	}{
		{"2026-10-09", false, "2026-10-10"},
		{"2026-10-09 23:15:42.123+02", true, "2026-10-10 09:30:42.123+02"},
		{"2026-10-09T08:00:00Z", true, "2026-10-10T09:30:00Z"},
	} {
		d, when, ok := parseDateText(c.text, c.withTime)
		if !ok {
			t.Fatalf("%q unread", c.text)
		}
		when = time.Date(when.Year(), when.Month(), when.Day()+1, 9, 30, 0, 0, time.Local)
		if got := d.text(when); got != c.want {
			t.Errorf("%q: %q", c.text, got)
		}
	}
	if _, _, ok := parseDateText("yesterday", false); ok {
		t.Fatal("a word read as a date")
	}
}

// The value editor offers the form a column's type suits, which writes
// the text saved.
func TestValueEditorForms(t *testing.T) {
	a, tt, cn := designHost(t)
	cn.DB.SQL.Exec(`CREATE TABLE things (id INTEGER PRIMARY KEY, due DATE, done BOOLEAN, meta TEXT)`)
	cn.DB.SQL.Exec(`INSERT INTO things (due, done, meta) VALUES ('2026-01-31', 0, '{"tags": ["a", "b"], "n": 1}')`)
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "things", Kind: db.KindTable, Rows: -1}, PageData)
	v := a.Tabs[0].(*TableTab).view
	testutil.WaitFor(t, tt, "the rows", func() bool { return len(v.src.Rows) == 1 && v.columns != nil })
	col := func(name string) int {
		return slices.IndexFunc(v.src.Cols, func(c db.ColumnInfo) bool { return c.Name == name })
	}

	openValueEditor(a, v.grid, &v.src, 0, col("due"))
	e := a.Dialogs().valueEdit
	if e.form != formCalendar {
		t.Fatalf("due: %q %v", e.form, e.forms)
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "value-calendar")
	e.when = e.when.AddDate(0, 0, 1)
	tt.Frame()
	if err := tt.Click("Save"); err != nil {
		t.Fatal(err)
	}
	if got := v.grid.edits.updates[0][col("due")]; got != db.Typed("2026-02-01") {
		t.Fatalf("due saved as %v", got)
	}

	openValueEditor(a, v.grid, &v.src, 0, col("done"))
	e = a.Dialogs().valueEdit
	if e.form != formSwitch || e.truth != 1 {
		t.Fatalf("done: %q %d", e.form, e.truth)
	}
	e.truth = 0
	tt.Frame()
	tt.Click("Save")
	if got := v.grid.edits.updates[0][col("done")]; got != db.Typed("1") {
		t.Fatalf("done saved as %v", got)
	}

	openValueEditor(a, v.grid, &v.src, 0, col("meta"))
	e = a.Dialogs().valueEdit
	tt.Frame()
	testutil.Snapshot(t, tt, "value-tree")
	if e.form != formTree || !tt.HasText("tags: [2]") || !tt.HasText(`0: "a"`) {
		t.Fatalf("meta: %q %q", e.form, tt.Texts())
	}
	e.open = false
	tt.Frame()
}

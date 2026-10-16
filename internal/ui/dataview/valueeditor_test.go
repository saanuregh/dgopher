package dataview

import (
	"slices"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
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
	tt.Advance(time.Second)
	tt.Frame()
	testutil.Snapshot(t, tt, "value-tree")
	if e.form != formTree || !tt.HasText(`"tags"`) || !tt.HasText(`"a"`) {
		t.Fatalf("meta: %q %q", e.form, tt.Texts())
	}
	e.open = false
	tt.Frame()
}

func TestJSONTreeReadsAgainWhenTextChanges(t *testing.T) {
	var tree jsonTree
	tree.read(`{"b":1,"a":{"c":[1]}}`, 1)
	if tree.node == nil || tree.node.children[0].key != "b" {
		t.Fatalf("read %+v, %v", tree.node, tree.err)
	}
	if a := tree.node.children[1]; !tree.folded[a.id] || tree.folded[tree.node.id] {
		t.Fatalf("folded %v: the top open, the next level folded", tree.folded)
	}
	tree.folded[tree.node.id] = true
	tree.read(`{"b":1,"a":{"c":[1]}}`, 1)
	if !tree.folded[tree.node.id] {
		t.Fatal("the same text forgot its folds")
	}
	tree.read(`{"c":3}`, 1)
	if len(tree.node.children) != 1 || tree.node.children[0].key != "c" {
		t.Fatalf("after the text changed: %+v", tree.node)
	}
	tree.read(`{`, 1)
	if tree.node != nil || tree.err == nil {
		t.Fatal("broken JSON read")
	}
}

// Escape closes the value editor while its text has the keys; typed in,
// the value saves.
func TestValueEditorTextKeys(t *testing.T) {
	a := NewFakeHost(t)
	g, src := editableGrid()
	tt := ui.NewTester(func(c *ui.Context) {
		if a.dialogs.valueEdit != nil {
			valueEditorView(a, c)
		}
	}, 800, 600)
	openValueEditor(a, g, src, 1, 1)
	e := a.dialogs.valueEdit
	tt.Frame()
	tt.Frame()
	if !e.area.Focused() {
		t.Fatal("the text has not the keys")
	}
	// Escape closes the dialog at once, with text selected too.
	tt.Command("selectAll")
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()
	if a.dialogs.valueEdit != nil {
		t.Fatal("Escape left the value editor open")
	}

	openValueEditor(a, g, src, 1, 1)
	e = a.dialogs.valueEdit
	tt.Frame()
	tt.Frame()
	tt.Type("z")
	tt.Frame()
	if e.text != "zb" {
		t.Fatalf("typed: %q", e.text)
	}
	// Tab goes on to the dialog's buttons, as in a text field.
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if e.text != "zb" || e.area.Focused() {
		t.Fatalf("Tab: %q, the text still has the keys: %v", e.text, e.area.Focused())
	}
	if err := tt.Click("Save"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := g.edits.updates[1][1]; got != db.Typed("zb") {
		t.Fatalf("saved %#v", got)
	}
}

// An editable value starts as the database gives it, never in the grid's
// display format, which would be written back as the value.
func TestValueEditorStartsWithRawValue(t *testing.T) {
	a := NewFakeHost(t)
	a.settings.ViewFormat.GroupNumbers = true
	g, src := editableGrid()
	src.Rows[0][0] = int64(1234567)
	openValueEditor(a, g, src, 0, 0)
	if got := a.dialogs.valueEdit.text; got != "1234567" {
		t.Fatalf("editable value editor text %q, want 1234567", got)
	}
}

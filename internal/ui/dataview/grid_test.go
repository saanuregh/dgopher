package dataview

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/export"
	"dgopher/internal/settings"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// A column is numeric by its type, else by its values: its NULLs then
// line up with its numbers.
func TestNullTakesColumnAlignment(t *testing.T) {
	src := Source{
		Cols: []db.ColumnInfo{{Name: "id"}, {Name: "n"}, {Name: "s"}, {Name: "typed", Type: "INTEGER"}, {Name: "empty"}},
		Rows: [][]any{
			{int64(1), nil, "text", nil, nil},
			{int64(22), int64(7), nil, nil, nil},
		},
	}
	g := NewGrid()
	want := []bool{true, true, false, true, false}
	for col, w := range want {
		if got := g.numericColumn(&src, col); got != w {
			t.Errorf("column %s numeric = %v, want %v", src.Cols[col].Name, got, w)
		}
	}
}

func editableGrid() (*Grid, *Source) {
	src := &Source{
		Cols: []db.ColumnInfo{{Name: "id", Type: "INTEGER"}, {Name: "name", Type: "TEXT"}, {Name: "note", Type: "TEXT"}},
		Rows: [][]any{{int64(1), "a", nil}, {int64(2), "b", "x"}},
	}
	g := NewGrid()
	g.edits = newPendingEdits()
	g.ViewOrder(src)
	return g, src
}

// Pasted cells become pending edits from the chosen cell, past the end as
// new rows.
func TestPasteMakesPendingEdits(t *testing.T) {
	g, src := editableGrid()
	g.SelRow, g.selCol = 1, 1
	rows, err := parseClipboard("bee\tnote b\ncee\tNULL\n", pasteOptions{NullText: "NULL"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := g.paste(src, rows, pasteOptions{NullText: "NULL"})
	if err != nil || n != 4 {
		t.Fatalf("pasted %d: %v", n, err)
	}
	if v := g.edits.updates[1][1]; v != db.Typed("bee") {
		t.Fatalf("row 2 name %#v", v)
	}
	if len(g.edits.inserted) != 1 || g.edits.inserted[0][1] != db.Typed("cee") || g.edits.inserted[0][2] != nil || g.edits.inserted[0][0] != unset {
		t.Fatalf("inserted %#v", g.edits.inserted)
	}
	// A read-only grid refuses.
	g.readOnly = "no key"
	if _, err := g.paste(src, rows, pasteOptions{}); err == nil {
		t.Fatal("pasted into read-only rows")
	}
}

// Advanced paste with a header matches columns by name, as new rows.
func TestPasteAddsRows(t *testing.T) {
	g, src := editableGrid()
	text := "note;id\n\"semi;colon\";7\n"
	rows, err := parseClipboard(text, pasteOptions{Delimiter: ';', Header: true})
	if err != nil {
		t.Fatal(err)
	}
	n, err := g.paste(src, rows, pasteOptions{Header: true, Insert: true})
	if err != nil || n != 2 || len(g.edits.inserted) != 1 {
		t.Fatalf("pasted %d rows %d: %v", n, len(g.edits.inserted), err)
	}
	r := g.edits.inserted[0]
	if r[0] != db.Typed("7") || r[2] != db.Typed("semi;colon") || r[1] != unset {
		t.Fatalf("row %#v", r)
	}
}

func TestAdvancedCopyOptions(t *testing.T) {
	g, src := editableGrid()
	g.selection.Add(0)
	g.selection.Add(1)
	got := g.copyAdvanced(src, settings.CopyOptions{Delimiter: ';', Header: true, RowNumbers: true, QuoteAll: true, NullText: "<null>"})
	want := "\"#\";\"id\";\"name\";\"note\"\n\"1\";\"1\";\"a\";\"<null>\"\n\"2\";\"2\";\"b\";\"x\"\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestGenerateSQL(t *testing.T) {
	src := &Source{
		Cols: []db.ColumnInfo{{Name: "id"}, {Name: "name"}, {Name: "note"}},
		Rows: [][]any{{int64(1), "O'Hara", nil}, {int64(2), "b", "x"}},
	}
	g := sqlGen{dialect: db.DialectOf(db.Postgres), engine: db.Postgres, schema: "s", table: "t", key: []string{"id"}}
	cases := map[string]string{
		"select": `SELECT * FROM "s"."t" WHERE "id" IN (1, 2);`,
		"insert": "INSERT INTO \"s\".\"t\" (\"id\", \"name\", \"note\") VALUES (1, 'O''Hara', NULL);\nINSERT INTO \"s\".\"t\" (\"id\", \"name\", \"note\") VALUES (2, 'b', 'x');",
		"update": "UPDATE \"s\".\"t\" SET \"name\" = 'O''Hara', \"note\" = NULL WHERE \"id\" = 1;\nUPDATE \"s\".\"t\" SET \"name\" = 'b', \"note\" = 'x' WHERE \"id\" = 2;",
		"delete": "DELETE FROM \"s\".\"t\" WHERE \"id\" = 1;\nDELETE FROM \"s\".\"t\" WHERE \"id\" = 2;",
	}
	for kind, want := range cases {
		if got := g.generate(kind, src, []int{0, 1}); got != want {
			t.Errorf("%s:\n%s\nwant\n%s", kind, got, want)
		}
	}
	// Without a key, every column matches, and a comment says so.
	g.key = nil
	got := g.generate("delete", src, []int{0})
	if got != "-- No unique key: the row is matched on every column.\nDELETE FROM \"s\".\"t\" WHERE \"id\" = 1 AND \"name\" = 'O''Hara' AND \"note\" IS NULL;" {
		t.Errorf("no key: %s", got)
	}
}

func TestDuplicateAndRevert(t *testing.T) {
	g, src := editableGrid()
	g.keyCols = map[int]bool{0: true}
	g.duplicateRow(src, 1, g.keyCols)
	if len(g.edits.inserted) != 1 || g.edits.inserted[0][0] != unset || g.edits.inserted[0][1] != "b" {
		t.Fatalf("duplicate %#v", g.edits.inserted)
	}
	g.setValue(src, 0, 1, db.Typed("changed"))
	g.setDefault(src, 0, 2)
	if g.edits.updates[0][2] != db.Default {
		t.Fatal("set to default")
	}
	g.revertCell(src, 0, 1)
	if _, ok := g.edits.updates[0][1]; ok {
		t.Fatal("revert cell")
	}
	g.edits.deleted[1] = true
	g.revertRow(src, 0)
	g.revertRow(src, 1)
	g.revertRow(src, 2) // the duplicate
	if g.edits.count() != 0 {
		t.Fatalf("left %d changes", g.edits.count())
	}
}

func TestSelectionStats(t *testing.T) {
	g, src := editableGrid()
	g.selCol = 0
	g.selection.Add(0)
	g.selection.Add(1)
	if got := g.SelectionStats(src); got != "2 values · sum 3 · avg 1.5 · min 1 · max 2" {
		t.Fatalf("numbers: %q", got)
	}
	g.selCol = 2 // one NULL, one text
	if got := g.SelectionStats(src); got != "2 rows · 1 NULL" {
		t.Fatalf("text: %q", got)
	}
	g.selection.Clear()
	if got := g.SelectionStats(src); got != "" {
		t.Fatalf("one row: %q", got)
	}
}

// Filters on results act on the rows read; orders sort by several columns.
func TestResultFilterClientSide(t *testing.T) {
	src := &Source{
		Cols: []db.ColumnInfo{{Name: "n"}, {Name: "s"}},
		Rows: [][]any{{int64(3), "Apple"}, {int64(1), nil}, {int64(2), "banana"}, {int64(2), "apricot"}},
	}
	g := NewGrid()
	view := func() []int { g.orderKey = ""; return append([]int(nil), g.ViewOrder(src)...) }
	g.addCond(rowCond{col: 0, op: ">", vals: []any{int64(1)}})
	if got := view(); !slices.Equal(got, []int{0, 2, 3}) {
		t.Fatalf("> 1: %v", got)
	}
	g.addCond(rowCond{col: 1, op: "contains", vals: []any{"AP"}})
	if got := view(); !slices.Equal(got, []int{0, 3}) {
		t.Fatalf("contains: %v", got)
	}
	g.clearConds(1)
	g.addCond(rowCond{col: 1, op: "null"})
	g.clearConds(0)
	if got := view(); !slices.Equal(got, []int{1}) {
		t.Fatalf("is null: %v", got)
	}
	g.clearConds(-1)
	g.addCond(rowCond{col: 1, op: "in", vals: []any{"Apple", "banana"}})
	if got := view(); !slices.Equal(got, []int{0, 2}) {
		t.Fatalf("in: %v", got)
	}
	g.clearConds(-1)
	g.sort = ui.SortOrder{Column: colID(0), Then: []ui.SortKey{{Column: colID(1), Descending: true}}}
	if got := view(); !slices.Equal(got, []int{1, 2, 3, 0}) {
		t.Fatalf("multi sort: %v", got)
	}
}

// In a table, a filter from the menu is SQL in the filter bar.
func TestFilterMenuBuildsWhere(t *testing.T) {
	cases := []struct {
		engine db.Engine
		cond   rowCond
		want   string
	}{
		{db.Postgres, rowCond{col: 0, op: "=", vals: []any{"O'Hara"}}, `"name" = 'O''Hara'`},
		{db.Postgres, rowCond{col: 0, op: "<>", vals: []any{int64(3)}}, `"name" <> 3`},
		{db.Postgres, rowCond{col: 0, op: "null"}, `"name" IS NULL`},
		{db.Postgres, rowCond{col: 0, op: "notnull"}, `"name" IS NOT NULL`},
		{db.Postgres, rowCond{col: 0, op: "in", vals: []any{int64(1), int64(2)}}, `"name" IN (1, 2)`},
		{db.Postgres, rowCond{col: 0, op: "contains", vals: []any{"ab"}}, `CAST("name" AS TEXT) ILIKE '%ab%'`},
		{db.Postgres, rowCond{col: 0, op: "contains", vals: []any{`50%_\x`}}, `CAST("name" AS TEXT) ILIKE '%50\%\_\\x%'`},
		{db.SQLite, rowCond{col: 0, op: "contains", vals: []any{"5%"}}, `CAST("name" AS TEXT) LIKE '%5\%%' ESCAPE '\'`},
		{db.MySQL, rowCond{col: 0, op: "contains", vals: []any{"5%"}}, "CAST(`name` AS CHAR) LIKE '%5\\\\%%'"},
		{db.MySQL, rowCond{col: 0, op: "contains", vals: []any{"ab"}}, "CAST(`name` AS CHAR) LIKE '%ab%'"},
		{db.ClickHouse, rowCond{col: 0, op: "contains", vals: []any{"ab"}}, "toString(`name`) ILIKE '%ab%'"},
	}
	for _, tc := range cases {
		if got := condSQL(db.DialectOf(tc.engine), tc.engine, "name", tc.cond); got != tc.want {
			t.Errorf("%s %s: %s, want %s", tc.engine, tc.cond.op, got, tc.want)
		}
	}
}

func TestHeaderMenuHidesAndPins(t *testing.T) {
	a := NewFakeHost(t)
	src := &Source{
		Cols: []db.ColumnInfo{{Name: "id"}, {Name: "empty"}, {Name: "name"}},
		Rows: [][]any{{int64(1), nil, "a"}, {int64(2), nil, "b"}},
	}
	g := NewGrid()
	tt := ui.NewTester(func(c *ui.Context) { g.View(c, a, src) }, 800, 300)
	tt.Frame()
	if err := tt.RightClick("name"); err != nil {
		t.Fatal(err)
	}
	items := strings.Join(tt.Menu(), "|")
	for _, want := range []string{"Copy Column Name", "Pin Column", "Fit Width to Values", "Hide Column", "Hide Columns with No Data"} {
		if !strings.Contains(items, want) {
			t.Fatalf("menu %s lacks %s", items, want)
		}
	}
	if err := tt.ChooseMenuItem("Hide Columns with No Data"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !g.hidden[1] || tt.HasText("empty") {
		t.Fatal("the empty column still shows")
	}
	tt.RightClick("name")
	tt.ChooseMenuItem("Pin Column")
	tt.Frame()
	if !g.pinned[2] {
		t.Fatal("not pinned")
	}
	// Moving right from id skips the hidden column.
	g.selCol = 0
	g.moveCol(src, 1)
	if g.selCol != 2 {
		t.Fatalf("moved to %d", g.selCol)
	}
}

func TestFitWidths(t *testing.T) {
	a := NewFakeHost(t)
	src := &Source{Cols: []db.ColumnInfo{{Name: "a"}}, Rows: [][]any{{strings.Repeat("x", 80)}}}
	g := NewGrid()
	if w := g.fitWidth(a, src, 0); w != float32(80*7+28) {
		t.Fatalf("fit %v", w)
	}
	g.viewW = 560
	g.fitToScreen(src)
	if w := g.List.Columns.Widths[colID(0)]; w != 500 {
		t.Fatalf("screen %v", w)
	}
}

func TestTextViewAligned(t *testing.T) {
	a := NewFakeHost(t)
	g, src := editableGrid()
	order := g.ViewOrder(src)
	got := g.plainText(a, src, order, 10)
	want := "id | name | note\n---+------+-----\n 1 | a    | NULL\n 2 | b    | x   \n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if !strings.HasSuffix(g.plainText(a, src, order, 1), "… 1 more rows\n") {
		t.Fatal("no note of the rows left out")
	}
}

func TestRecordViewEditsAndMoves(t *testing.T) {
	a := NewFakeHost(t)
	g, src := editableGrid()
	g.SelRow = 0
	g.mode = viewRecord
	tt := ui.NewTester(func(c *ui.Context) { g.View(c, a, src) }, 700, 400)
	tt.Frame()
	if !tt.HasText("Row 1 of 2") || !tt.HasText("name") || !tt.HasText("a") {
		t.Fatalf("record: %q", tt.Texts())
	}
	testutil.Snapshot(t, tt, "record-view")
	if err := tt.Click("Next row"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Row 2 of 2") || !tt.HasText("b") {
		t.Fatalf("next: %q", tt.Texts())
	}
	g.startEdit(src, 1, 1)
	g.editing.value = "bee"
	g.commitEdit(src)
	if g.edits.updates[1][1] != db.Typed("bee") {
		t.Fatal("an edit in the record is not pending")
	}
	g.mode = viewText
	tt.Frame()
	testutil.Snapshot(t, tt, "text-view")
}

func TestUndoRedoPendingEdits(t *testing.T) {
	g, src := editableGrid()
	g.checkpoint()
	g.setValue(src, 0, 1, db.Typed("one"))
	g.addRow(src) // checkpoints itself
	g.editing = nil
	if g.edits.count() != 2 {
		t.Fatalf("count %d", g.edits.count())
	}
	g.undo()
	if g.edits.count() != 1 || len(g.edits.inserted) != 0 {
		t.Fatalf("after undo: %d", g.edits.count())
	}
	g.undo()
	if g.edits.count() != 0 {
		t.Fatal("second undo")
	}
	g.redo()
	g.redo()
	if g.edits.count() != 2 || g.edits.updates[0][1] != db.Typed("one") {
		t.Fatalf("after redo: %d", g.edits.count())
	}
	// A new change after an undo drops what could be redone.
	g.undo()
	g.checkpoint()
	g.setValue(src, 1, 1, db.Typed("two"))
	g.redo()
	if len(g.edits.inserted) != 0 {
		t.Fatal("redo after a new change")
	}
	// Read again, the rows are others: nothing from before can come back.
	g.reset()
	g.edits.clear()
	g.undo()
	if g.edits.count() != 0 {
		t.Fatal("undo after the rows were read again")
	}
}

func TestCopyFromRowAbove(t *testing.T) {
	g, src := editableGrid()
	g.SelRow, g.selCol = 1, 1
	g.copyFromNeighbour(src, -1)
	if g.edits.updates[1][1] != "a" {
		t.Fatalf("copied %#v", g.edits.updates[1][1])
	}
}

func TestGridShortcuts(t *testing.T) {
	a := NewFakeHost(t)
	g, src := editableGrid()
	view := func(c *ui.Context) {
		g.View(c, a, src)
		if a.dialogs.valueEdit != nil {
			valueEditorView(a, c)
		}
		if a.dialogs.goTo != nil {
			goToView(a, c)
		}
	}
	tt := ui.NewTester(view, 800, 300)
	tt.Frame()
	if err := tt.Click("a"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyDown)
	tt.Frame()
	if g.SelRow != 1 {
		t.Fatalf("⌘↓: row %d", g.SelRow)
	}
	tt.Key(ui.Cmd, ui.Key2)
	tt.Frame()
	if g.sort.Column != colID(g.selCol) || g.sort.Descending {
		t.Fatalf("⌘2: %+v", g.sort)
	}
	tt.Key(ui.Cmd, ui.KeyD) // the value of the row above
	tt.Frame()
	if g.edits.count() != 1 {
		t.Fatalf("⌘D: %d changes", g.edits.count())
	}
	tt.Key(ui.Cmd, ui.KeyZ)
	tt.Frame()
	if g.edits.count() != 0 {
		t.Fatal("⌘Z")
	}
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Frame()
	if a.dialogs.valueEdit == nil {
		t.Fatal("⇧↵: no value editor")
	}
	testutil.Snapshot(t, tt, "value-editor")
	a.dialogs.valueEdit = nil
	g.goTo(src, "1", false)
	g.goTo(src, "na", true)
	if g.SelRow != 0 || g.selCol != 1 {
		t.Fatalf("go to: row %d col %d", g.SelRow, g.selCol)
	}
}

func TestFilterVariants(t *testing.T) {
	// A NULL among the values chosen is IS NULL, in SQL and on the client.
	c := rowCond{col: 0, op: "in", vals: []any{"a", nil}}
	if got := condSQL(db.DialectOf(db.Postgres), db.Postgres, "x", c); got != `("x" IN ('a') OR "x" IS NULL)` {
		t.Fatalf("sql %s", got)
	}
	if !c.matches([]any{nil}) || !c.matches([]any{"a"}) || c.matches([]any{"b"}) {
		t.Fatal("client")
	}
	if got := condSQL(db.DialectOf(db.Postgres), db.Postgres, "x", rowCond{col: 0, op: "in", vals: []any{nil}}); got != `"x" IS NULL` {
		t.Fatalf("only NULL: %s", got)
	}
	// A typed value is a number when it reads as one.
	if typedFilterValue("42") != int64(42) || typedFilterValue("abc") != "abc" || typedFilterValue("007") != "007" {
		t.Fatal("typed values")
	}
}

func TestFilterCompletion(t *testing.T) {
	cols := []db.ColumnInfo{{Name: "customer_id"}, {Name: "created_at"}, {Name: "status"}}
	history := []string{"status = 'new'"}
	if got := filterSuggestions("", cols, history); !slices.Equal(got, []string{"status = 'new'"}) {
		t.Fatalf("empty: %v", got)
	}
	if got := filterSuggestions("total > 0 AND c", cols, history); !slices.Equal(got, []string{"customer_id", "created_at"}) {
		t.Fatalf("word: %v", got)
	}
	if got := completeFilter("total > 0 AND cus", "customer_id"); got != "total > 0 AND customer_id" {
		t.Fatalf("complete: %q", got)
	}
}

func TestFilterHistory(t *testing.T) {
	a := NewFakeHost(t)
	p := a.Project
	for i := range 25 {
		rememberFilter(p, "conn/s.t", fmt.Sprintf("x = %d", i))
	}
	rememberFilter(p, "conn/s.t", "x = 3")
	h := filterHistory(p, "conn/s.t")
	if len(h) != 20 || h[0] != "x = 3" || slices.Contains(h[1:], "x = 3") {
		t.Fatalf("history %v", h)
	}
	if len(filterHistory(p, "conn/s.other")) != 0 {
		t.Fatal("history of another table")
	}
}

func TestCalcPanel(t *testing.T) {
	src := &Source{Cols: []db.ColumnInfo{{Name: "n"}}, Rows: [][]any{{int64(4)}, {int64(1)}, {nil}, {int64(4)}, {int64(6)}}}
	g := NewGrid()
	for i := range src.Rows {
		g.selection.Add(i)
	}
	s := g.calc(src)
	if s.count != 4 || s.nulls != 1 || s.distinct != 3 || s.sum != 15 || s.min != 1 || s.max != 6 || s.median != 4 || s.mode != "4" || s.modeCount != 2 {
		t.Fatalf("%+v", s)
	}
}

func TestGroupingPanel(t *testing.T) {
	src := &Source{Cols: []db.ColumnInfo{{Name: "a"}, {Name: "b"}}, Rows: [][]any{{"x", int64(1)}, {"y", int64(1)}, {"x", int64(1)}, {"x", int64(2)}}}
	g := NewGrid()
	got := g.groups(src, []int{0, 1})
	if len(got) != 3 || got[0].count != 2 || got[0].vals[0] != "x" || got[0].vals[1] != int64(1) {
		t.Fatalf("%+v", got)
	}
}

func TestValuePanelViewers(t *testing.T) {
	if viewerFor(`{"a":1}`, []byte(`{"a":1}`)) != "JSON" || viewerFor("<a><b/></a>", []byte("<a><b/></a>")) != "XML" ||
		viewerFor([]byte{0, 1, 2}, []byte{0, 1, 2}) != "Hex" || viewerFor("plain", []byte("plain")) != "Text" {
		t.Fatal("viewers")
	}
	if got := prettyXML("<a><b>1</b></a>"); got != "<a>\n  <b>1</b>\n</a>" {
		t.Fatalf("xml %q", got)
	}
	a := NewFakeHost(t)
	g, src := editableGrid()
	src.Rows[0][2] = `{"k":[1,2]}`
	g.SelRow, g.selCol, g.ShowValue = 0, 2, true
	tt := ui.NewTester(func(c *ui.Context) { g.View(c, a, src) }, 900, 360)
	tt.Frame()
	if g.viewer != "JSON" || !testutil.HasTextContaining(tt, `"k": [`) {
		t.Fatalf("viewer %q: %q", g.viewer, tt.Texts())
	}
	testutil.Snapshot(t, tt, "value-panel")
}

func TestGenerateTableTemplates(t *testing.T) {
	cols := []db.ColumnInfo{{Name: "id"}, {Name: "name"}}
	fks := []db.ForeignKey{{Columns: []string{"owner_id"}, RefSchema: "s", RefTable: "owners", RefColumns: []string{"id"}}}
	cases := []struct {
		engine db.Engine
		kind   string
		want   string
	}{
		{db.Postgres, "select", "SELECT \"id\", \"name\"\nFROM \"s\".\"t\";"},
		{db.Postgres, "insert", "INSERT INTO \"s\".\"t\" (\"id\", \"name\")\nVALUES (:id, :name);"},
		{db.Postgres, "update", "UPDATE \"s\".\"t\"\nSET \"name\" = :name\nWHERE \"id\" = :id;"},
		{db.Postgres, "delete", "DELETE FROM \"s\".\"t\"\nWHERE \"id\" = :id;"},
		{db.Postgres, "upsert", "INSERT INTO \"s\".\"t\" (\"id\", \"name\")\nVALUES (:id, :name)\nON CONFLICT (\"id\") DO UPDATE SET \"name\" = EXCLUDED.\"name\";"},
		{db.MySQL, "upsert", "INSERT INTO `s`.`t` (`id`, `name`)\nVALUES (:id, :name)\nON DUPLICATE KEY UPDATE `name` = VALUES(`name`);"},
		{db.Postgres, "join", "SELECT *\nFROM \"s\".\"t\" t\nJOIN \"s\".\"owners\" \"owners\" ON \"owners\".\"id\" = t.\"owner_id\";"},
	}
	for _, tc := range cases {
		g := sqlGen{dialect: db.DialectOf(tc.engine), engine: tc.engine, schema: "s", table: "t", key: []string{"id"}}
		if got := g.template(tc.kind, cols, fks); got != tc.want {
			t.Errorf("%s %s:\n%s\nwant\n%s", tc.engine, tc.kind, got, tc.want)
		}
	}
}

func TestColorByValue(t *testing.T) {
	g, src := editableGrid()
	g.colorRules = []colorRule{{Column: "name", Value: "b", Color: "#fde68a"}}
	if c, ok := g.rowColor(src, 1); !ok || c != "#fde68a" {
		t.Fatalf("row 2: %v %v", c, ok)
	}
	if _, ok := g.rowColor(src, 0); ok {
		t.Fatal("row 1 colored")
	}
}

func TestCopyAsFormats(t *testing.T) {
	g, src := editableGrid()
	g.selection.Add(0)
	if got := g.copySelection(src, export.TSV, true); got != "id\tname\tnote\n1\ta\tNULL" {
		t.Fatalf("tsv %q", got)
	}
	a := NewFakeHost(t)
	if got := g.copyText(a, src, "text"); !strings.HasPrefix(got, "id | name | note\n") {
		t.Fatalf("text %q", got)
	}
}

func TestPasteSkipsHiddenColumns(t *testing.T) {
	g, src := editableGrid()
	g.hidden = map[int]bool{1: true}
	g.SelRow, g.selCol = 0, 0
	rows, _ := parseClipboard("10\tnoted", pasteOptions{})
	if _, err := g.paste(src, rows, pasteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.edits.updates[0][1]; ok || g.edits.updates[0][2] != db.Typed("noted") {
		t.Fatalf("pasted %#v", g.edits.updates[0])
	}
}

func TestDeleteAddedRows(t *testing.T) {
	g, src := editableGrid()
	for i := range 3 {
		g.addRow(src)
		g.edits.inserted[i][1] = db.Typed(fmt.Sprint("new", i))
	}
	g.editing = nil
	g.selection.Clear()
	g.selection.Add(2) // the first added row
	g.selection.Add(3) // the second
	g.deleteSelected(src)
	if len(g.edits.inserted) != 1 || g.edits.inserted[0][1] != db.Typed("new2") {
		t.Fatalf("left %#v", g.edits.inserted)
	}
}

func TestTemplatesWithOnlyKeyColumns(t *testing.T) {
	cols := []db.ColumnInfo{{Name: "id"}}
	g := sqlGen{dialect: db.DialectOf(db.Postgres), engine: db.Postgres, schema: "s", table: "t", key: []string{"id"}}
	if got := g.template("upsert", cols, nil); !strings.HasSuffix(got, `ON CONFLICT ("id") DO NOTHING;`) {
		t.Errorf("upsert %s", got)
	}
	if got := g.template("update", cols, nil); !strings.HasPrefix(got, "--") {
		t.Errorf("update %s", got)
	}
	fks := []db.ForeignKey{{Columns: []string{"a"}, RefTable: "user", RefColumns: []string{"id"}}, {Columns: []string{"b"}, RefTable: "user", RefColumns: []string{"id"}}}
	if got := g.template("join", cols, fks); !strings.Contains(got, `"user" ON "user"."id"`) || !strings.Contains(got, `"user_2" ON "user_2"."id" = t."b"`) {
		t.Errorf("join %s", got)
	}
}

// A chart lays out text or time along x and the numbers as series: bars
// for categories, lines over time.
func TestChartDetect(t *testing.T) {
	bars := &Source{
		Cols: []db.ColumnInfo{{Name: "country", Type: "TEXT"}, {Name: "customers", Type: "INT8"}, {Name: "revenue", Type: "NUMERIC"}},
		Rows: [][]any{{"BR", int64(2), "120.50"}, {"DE", int64(3), "99.10"}},
	}
	var s Chart
	s.detect(bars)
	if s.kind != chartBar || s.x != "country" || len(s.series()) != 2 {
		t.Errorf("categories: kind %d, x %q, series %v", s.kind, s.x, s.series())
	}
	lines := &Source{
		Cols: []db.ColumnInfo{{Name: "day", Type: "TIMESTAMP"}, {Name: "orders", Type: "INT8"}, {Name: "avg_total", Type: "NUMERIC"}},
		Rows: [][]any{{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), int64(5), "10.5"}},
	}
	s = Chart{}
	s.detect(lines)
	if s.kind != chartLine || s.x != "day" || len(s.series()) != 2 {
		t.Errorf("over time: kind %d, x %q, series %v", s.kind, s.x, s.series())
	}
}

// A histogram counts each value once, in bins of round bounds.
func TestHistogramBins(t *testing.T) {
	var pts []chartPoint
	for i := range 100 {
		pts = append(pts, chartPoint{ys: []float64{float64(i)}, ok: []bool{true}})
	}
	pts = append(pts, chartPoint{ys: []float64{0}, ok: []bool{false}})
	bins := histogramBins(pts)
	total := 0.0
	for _, b := range bins {
		total += b.ys[0]
	}
	if total != 100 || len(bins) < 5 || bins[0].label != "0 – 10" || bins[0].ys[0] != 10 || bins[len(bins)-1].ys[0] == 0 {
		t.Fatalf("%d bins of %v values: %+v", len(bins), total, bins)
	}
	if one := histogramBins([]chartPoint{{ys: []float64{7}, ok: []bool{true}}}); len(one) == 0 || one[0].ys[0] != 1 {
		t.Fatalf("a single value: %+v", one)
	}
}

// Every kind of chart draws rows of positive and negative values, and
// NULLs, without failing.
func TestChartKinds(t *testing.T) {
	src := &Source{
		Cols: []db.ColumnInfo{{Name: "day", Type: "DATE"}, {Name: "gain", Type: "INT8"}, {Name: "loss", Type: "INT8"}},
	}
	for i := range 30 {
		var loss any = int64(-i % 7)
		if i%5 == 0 {
			loss = nil
		}
		src.Rows = append(src.Rows, []any{time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC), int64(i * i % 17), loss})
	}
	for kind, name := range chartKinds {
		s := Chart{}
		tt := ui.NewTester(func(c *ui.Context) {
			if s.colsOf == "" {
				s.detect(src)
				s.kind = kind
			}
			ChartView(c, &s, src)
		}, 800, 420)
		tt.Frame()
		tt.Move(400, 200) // the tooltip too
		tt.Frame()
		testutil.Snapshot(t, tt, "chart-"+strings.ReplaceAll(strings.ToLower(name), " ", "-"))
	}
}

// The kinds a chart picks hold for the column types and values Postgres
// returns, not only for the made-up ones above.
func TestChartDetectPostgres(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	ctx := context.Background()
	d, err := db.Open(ctx, testutil.PGConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, c := range []struct {
		sql  string
		kind int
	}{
		{"SELECT country, count(*) AS customers, sum(o.total) / 100 AS revenue_hundreds FROM shop.customers c JOIN shop.orders o ON o.customer_id = c.id GROUP BY 1 ORDER BY 1", chartBar},
		{"SELECT date_trunc('day', placed_at) AS day, count(*) AS orders, avg(total) AS avg_total FROM shop.orders GROUP BY 1 ORDER BY 1", chartLine},
	} {
		cur, err := s.Query(ctx, c.sql)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := cur.Fetch(100)
		cur.Close()
		if err != nil {
			t.Fatal(err)
		}
		var ch Chart
		ch.detect(&Source{Cols: cur.Columns, Rows: rows})
		if ch.kind != c.kind || len(ch.series()) != 2 {
			t.Errorf("%s: kind %d, series %v, x %q", c.sql, ch.kind, ch.series(), ch.x)
		}
	}
}

// Enter starts editing the chosen cell, as F2 does, in the grid and in
// the record view.
func TestEnterEditsCell(t *testing.T) {
	for _, key := range []ui.Key{ui.KeyF2, ui.KeyEnter} {
		for _, mode := range []int{viewGrid, viewRecord} {
			a := NewFakeHost(t)
			g, src := editableGrid()
			g.SelRow, g.mode = 0, mode
			tt := ui.NewTester(func(c *ui.Context) { g.View(c, a, src) }, 700, 400)
			tt.Frame()
			at, ok := tt.Find("a")
			if !ok {
				t.Fatalf("mode %d: no cell a in %q", mode, tt.Texts())
			}
			tt.ClickAt(at.X+2, at.Y+at.H/2)
			tt.Key(0, key)
			tt.Frame()
			if g.editing == nil {
				t.Errorf("key %v, mode %d: no edit started", key, mode)
			}
		}
	}
}

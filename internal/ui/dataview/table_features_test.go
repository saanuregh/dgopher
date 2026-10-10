package dataview

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// Deleting rows that other tables point at says so in the review.
func TestDeleteReviewListsReferences(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := NewFakeHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.View, 1360, 860)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "shop", Name: "customers", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && tb.columns != nil })
	tb.view.grid.SelRow = 0
	tb.view.grid.selection.Clear()
	tb.view.grid.selection.Add(0)
	tb.view.grid.deleteSelected(&tb.view.src)
	tb.view.Review(nil)
	testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
	if !strings.Contains(strings.Join(a.Confirm.Reasons, "\n"), "shop.orders points at these rows (customer_id → id)") {
		t.Fatalf("reasons %q", a.Confirm.Reasons)
	}
	a.Confirm = nil
}

// A table's Diagram shows the tables next to it by foreign key.
func TestTableDiagramShowsNeighbours(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := NewFakeHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.View, 1360, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "shop", Name: "customers", Kind: db.KindTable, Rows: -1}, pageDiagram)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "the diagram", func() bool { return tb.diagram != nil && !tb.diagram.loading })
	if tb.diagram.err != "" {
		t.Fatal(tb.diagram.err)
	}
	names := map[string]*erTable{}
	for _, e := range tb.diagram.tables {
		names[e.obj.Name] = e
	}
	if names["customers"] == nil || names["orders"] == nil || names["orders"].x >= names["customers"].x {
		t.Fatalf("tables %v", names)
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "table-diagram")
}

// Navigate shows the rows of other tables that refer to a row.
func TestShowReferencingRows(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := NewFakeHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.View, 1360, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "shop", Name: "customers", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && tb.columns != nil })
	tb.view.loadRefs(nil)
	testutil.WaitFor(t, tt, "references", func() bool { return tb.view.refs != nil })
	where, ok := tb.view.referencingWhere(tb.view.refs[0], 0)
	if !ok || !strings.HasPrefix(where, `"customer_id" = `) {
		t.Fatalf("where %q", where)
	}
	tb.view.openTableWhere(tb.view.refs[0].Schema, tb.view.refs[0].Table, where)
	ot, isTable := a.ActiveTab().(*TableTab)
	if !isTable || ot.Object.Name != "orders" {
		t.Fatalf("opened %+v", a.ActiveTab())
	}
	testutil.WaitFor(t, tt, "orders", func() bool { return ot.view.src.Rows != nil })
	if ot.view.where != where {
		t.Fatalf("filter %q", ot.view.where)
	}
}

// In a table the Filter menu filters on the server, joined to what was
// typed, and the order sorts by several columns.
func TestTableFilterAndOrderMenus(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := NewFakeHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.View, 1360, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "shop", Name: "orders", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && tb.columns != nil && !tb.view.loading })
	col := func(name string) int {
		for i, c := range tb.view.src.Cols {
			if c.Name == name {
				return i
			}
		}
		t.Fatalf("no column %s", name)
		return -1
	}
	tb.view.typedWhere, tb.view.where = "total > 0", "total > 0"
	tb.view.addFilter(rowCond{col: col("status"), op: "=", vals: []any{"new"}})
	if tb.view.where != `(total > 0) AND "status" = 'new'` || tb.view.whereIn != tb.view.where {
		t.Fatalf("where %q", tb.view.where)
	}
	testutil.WaitFor(t, tt, "filtered rows", func() bool { return !tb.view.loading && tb.view.src.Rows != nil })
	for _, r := range tb.view.src.Rows {
		if r[col("status")] != "new" {
			t.Fatalf("a row with status %v", r[col("status")])
		}
	}
	tb.view.clearFilters(col("status"))
	if tb.view.where != "total > 0" {
		t.Fatalf("after removing the column's filter: %q", tb.view.where)
	}
	tb.view.grid.sort = ui.SortOrder{Column: colID(col("status")), Then: []ui.SortKey{{Column: colID(col("total")), Descending: true}}}
	if q := tb.view.selectSQL(); !strings.HasSuffix(q, `ORDER BY "status", "total" DESC`) {
		t.Fatalf("order: %s", q)
	}
}

// The distinct values of a table's column come from the server, with
// their counts, and filter it when ticked.
func TestDistinctValuesFilter(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := NewFakeHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.View, 1360, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "shop", Name: "orders", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && tb.columns != nil && !tb.view.loading })
	col := -1
	for i, c := range tb.view.src.Cols {
		if c.Name == "status" {
			col = i
		}
	}
	apply, _ := tb.view.grid.filterFuncs()
	openDistinct(a, tb.view.grid, &tb.view.src, col, apply)
	testutil.WaitFor(t, tt, "the values", func() bool { return a.dialogs.distinct != nil && !a.dialogs.distinct.loading })
	if a.dialogs.distinct.err != "" || len(a.dialogs.distinct.values) == 0 {
		t.Fatalf("values %+v %s", a.dialogs.distinct.values, a.dialogs.distinct.err)
	}
	var total int64
	for _, v := range a.dialogs.distinct.values {
		total += v.count
	}
	if total == 0 {
		t.Fatal("no counts")
	}
	testutil.Snapshot(t, tt, "distinct-values")
	first := a.dialogs.distinct.values[0]
	a.dialogs.distinct.values[0].checked = true
	a.dialogs.distinct.apply(rowCond{col: col, op: "in", vals: []any{first.v}})
	testutil.WaitFor(t, tt, "filtered rows", func() bool { return !tb.view.loading && strings.Contains(tb.view.where, "IN (") })
	if int64(len(tb.view.src.Rows)) != min(first.count, int64(a.settings.PageSize)) {
		t.Fatalf("%d rows, want %d", len(tb.view.src.Rows), first.count)
	}
}

// The References panel lists the rows of other tables that refer to the
// chosen row.
func TestReferencesPanel(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := NewFakeHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.View, 1360, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "shop", Name: "customers", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && tb.columns != nil && !tb.view.loading })
	tb.view.grid.SelRow, tb.view.grid.ShowValue, tb.view.grid.panel = 0, true, slices.Index(panelNames, "References")
	testutil.WaitFor(t, tt, "the references", func() bool { return !tb.view.grid.refsLoading && tb.view.grid.refsRow == tb.view.grid.Order[0] })
	if tb.view.grid.refsErr != "" || len(tb.view.grid.refsShown) != 1 || tb.view.grid.refsShown[0].title != "shop.orders" || len(tb.view.grid.refsShown[0].rows) == 0 {
		t.Fatalf("refs %+v %s", tb.view.grid.refsShown, tb.view.grid.refsErr)
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "references-panel")
	// Grouping on the server counts the whole table.
	statusCol := -1
	a.OpenTable(cn, "", db.Object{Schema: "shop", Name: "orders", Kind: db.KindTable, Rows: -1}, PageData)
	ot := a.ActiveTab().(*TableTab)
	testutil.WaitFor(t, tt, "orders", func() bool { return ot.view.src.Rows != nil && !ot.view.loading })
	for i, c := range ot.view.src.Cols {
		if c.Name == "status" {
			statusCol = i
		}
	}
	var groups []groupRow
	ot.view.groupOnServer([]int{statusCol}, func(g []groupRow, err error) {
		if err != nil {
			t.Error(err)
		}
		groups = g
	})
	testutil.WaitFor(t, tt, "groups", func() bool { return groups != nil })
	var total int64
	for _, g := range groups {
		total += g.count
	}
	if total <= int64(len(ot.view.src.Rows)) {
		t.Fatalf("server groups count %d rows, no more than the %d read", total, len(ot.view.src.Rows))
	}
}

// A table without a key becomes editable with a virtual key, kept in the
// project's file for the team.
func TestVirtualKeyMakesTableEditable(t *testing.T) {
	a := NewFakeHost(t)
	file := filepath.Join(t.TempDir(), "nokey.sqlite")
	os.WriteFile(file, nil, 0o600)
	setup, _ := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	setup.SQL.Exec("CREATE TABLE log (code TEXT, note TEXT)")
	setup.SQL.Exec("INSERT INTO log VALUES ('a', 'x'), ('b', 'y')")
	setup.Close()
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.View, 1200, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "log", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && tb.columns != nil && !tb.view.loading })
	if tb.view.readOnlyReason() == "" {
		t.Fatal("a table without a key is editable")
	}
	if err := setVirtualKey(a, tb.view, []string{"code"}); err != nil {
		t.Fatal(err)
	}
	if why := tb.view.readOnlyReason(); why != "" {
		t.Fatalf("still read-only: %s", why)
	}
	raw, _ := os.ReadFile(filepath.Join(a.Project.Dir, project.File))
	if !strings.Contains(string(raw), `"virtualKeys"`) || !strings.Contains(string(raw), `"lite/main.log"`) {
		t.Fatalf("%s:\n%s", project.File, raw)
	}
	tb.view.grid.setValue(&tb.view.src, 1, 1, db.Typed("changed"))
	tb.view.Review(nil)
	testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
	if !strings.Contains(a.Confirm.Preview, `WHERE "code" = 'b'`) || !strings.Contains(strings.Join(a.Confirm.Reasons, " "), "virtual key") {
		t.Fatalf("review %q %q", a.Confirm.Preview, a.Confirm.Reasons)
	}
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "apply", func() bool { return !tb.view.applying && tb.view.grid.edits.count() == 0 })
	// Listed again, the project keeps the key.
	p, _, err := project.Load(a.Project.Dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if got := p.VirtualKeys["lite/main.log"]; len(got) != 1 || got[0] != "code" {
		t.Fatalf("virtual keys after reopening: %v", p.VirtualKeys)
	}
}

// A filter the table refuses is shown, and nothing else runs it: not the
// distinct values, the grouping, or an export.
func TestRefusedFilterRunsNowhere(t *testing.T) {
	a := NewFakeHost(t)
	file := filepath.Join(t.TempDir(), "t.sqlite")
	os.WriteFile(file, nil, 0o600)
	setup, err := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	setup.SQL.Exec("CREATE TABLE t (a INT)")
	setup.Close()
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.View, 1000, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "t", Kind: db.KindTable, Rows: -1}, PageData)
	var tb *TableTab
	for _, x := range a.Tabs {
		if tab, ok := x.(*TableTab); ok {
			tb = tab
		}
	}
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Cols != nil && !tb.view.loading })
	tb.view.where, tb.view.whereIn = "1=1; DELETE FROM t", "1=1; DELETE FROM t"
	tb.view.reload()
	tt.Frame()
	if !testutil.HasTextContaining(tt, "Could not read the rows") {
		t.Fatalf("the refused filter is not shown: %q", tb.view.err)
	}
	var derr error
	tb.view.distinctValues(0, func(_ []distinctValue, err error) { derr = err })
	var gerr error
	tb.view.groupOnServer([]int{0}, func(_ []groupRow, err error) { gerr = err })
	if derr == nil || gerr == nil {
		t.Fatal("the refused filter ran for distinct values or grouping")
	}
	if src := tb.view.exportSource(); src.SQL != "" || rerunReason(src) == "" {
		t.Fatalf("export would run %q", src.SQL)
	}
}

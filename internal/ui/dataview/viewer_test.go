package dataview

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// openNumbers opens a table of n numbered rows, read pageSize at a time.
func openNumbers(t *testing.T, n, pageSize int) (*FakeHost, *ui.Tester, *TableTab, string) {
	t.Helper()
	a := NewFakeHost(t)
	a.Settings().PageSize = pageSize
	file := filepath.Join(t.TempDir(), "n.sqlite")
	os.WriteFile(file, nil, 0o600)
	setup, _ := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	setup.SQL.Exec("CREATE TABLE n (id INTEGER PRIMARY KEY, label TEXT)")
	for i := 1; i <= n; i++ {
		setup.SQL.Exec("INSERT INTO n VALUES (?, ?)", i, fmt.Sprintf("row %d", i))
	}
	setup.Close()
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.View, 1200, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "n", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && tb.columns != nil && !tb.view.loading })
	return a, tt, tb, file
}

func labelOf(t *testing.T, file string, id int) string {
	t.Helper()
	d, err := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var s string
	d.SQL.QueryRow("SELECT label FROM n WHERE id = ?", id).Scan(&s)
	return s
}

// Reading the rows again with changes pending asks first: Cancel keeps
// them, Discard drops them and reads, Apply reviews them, then reads.
func TestPendingChangesAskApplyDiscardCancel(t *testing.T) {
	a, tt, tb, file := openNumbers(t, 3, 100)
	v := tb.view
	v.grid.setValue(&v.src, 0, 1, db.Typed("edited"))

	tb.RequestReload()
	if a.Pending == nil || a.Pending.N != 1 {
		t.Fatalf("no question about the pending change: %+v", a.Pending)
	}
	a.Pending.Cancel()
	a.Pending = nil
	tt.Frame()
	if v.grid.edits.count() != 1 || v.loading {
		t.Fatal("Cancel dropped the change or read the rows")
	}

	tb.RequestReload()
	a.Pending.Discard()
	a.Pending = nil
	testutil.WaitFor(t, tt, "the rows read again", func() bool { return !v.loading })
	if v.grid.edits.count() != 0 || labelOf(t, file, 1) != "row 1" {
		t.Fatal("Discard kept the change or wrote it")
	}

	v.grid.setValue(&v.src, 0, 1, db.Typed("applied"))
	tb.RequestReload()
	a.Pending.Apply()
	a.Pending = nil
	testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the rows read again", func() bool { return !v.applying && !v.loading && v.grid.edits.count() == 0 })
	if labelOf(t, file, 1) != "applied" || v.src.Rows[0][1] != "applied" {
		t.Fatalf("Apply: %q in the database, %v shown", labelOf(t, file, 1), v.src.Rows[0][1])
	}
}

// openCustomers opens the seeded customers, 2500 rows read a page of
// 100 at a time.
func openCustomers(t *testing.T) (*FakeHost, *ui.Tester, *TableTab) {
	t.Helper()
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := NewFakeHost(t)
	a.Settings().PageSize = 100
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.View, 1360, 860)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "shop", Name: "customers", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && tb.columns != nil && !tb.view.loading })
	return a, tt, tb
}

// A header sorts the rows in memory once every row is read, and on the
// server while more remain; with changes pending, the server sort asks
// first, and Cancel puts the old order back.
func TestSmartSort(t *testing.T) {
	_, tt, tb, _ := openNumbers(t, 3, 100)
	v := tb.view
	cursor := v.cursor
	if err := tt.Click("label"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if v.cursor != cursor || v.loading || len(sortKeys(v.grid.sort, 2)) != 1 {
		t.Fatal("sorting rows all read went to the server, or did not sort")
	}

	a, tt, tb := openCustomers(t)
	v = tb.view
	if v.done {
		t.Fatal("every customer was read at once")
	}
	cursor = v.cursor
	if err := tt.Click("name"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the rows read in order", func() bool { return v.cursor != cursor && !v.loading })
	if ran := lastStatement(a); !strings.Contains(ran, `ORDER BY "name"`) {
		t.Fatalf("the server was not asked for the order: %q", ran)
	}

	v.grid.setValue(&v.src, 0, 1, db.Typed("edited"))
	before := cloneSort(v.grid.sort)
	cursor = v.cursor
	// As a header click leaves it: a second click right after the first
	// would be a double click to the tester, whose clock does not move.
	v.grid.sort, v.grid.sortChanged = ui.SortOrder{Column: colID(2)}, true
	testutil.WaitFor(t, tt, "the question about the pending change", func() bool { return a.Pending != nil })
	a.Pending.Cancel()
	tt.Frame()
	if !sameSort(v.grid.sort, before) || v.cursor != cursor || v.grid.edits.count() != 1 {
		t.Fatal("Cancel did not keep the order and the change")
	}
}

// The table's rows filter on what was read, chart as a result's do, and
// Fetch All reads the rest.
func TestTableToolbarAndStatus(t *testing.T) {
	_, tt, _, _ := openNumbers(t, 5, 100)
	if err := tt.Click("Filter rows"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Type("row 4")
	tt.Frame()
	if !tt.HasText("1 of 5 rows") {
		t.Fatalf("no filtered count: %q", tt.Texts())
	}
	if err := tt.Click("Chart"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Series") {
		t.Fatalf("no chart: %q", tt.Texts())
	}

	_, tt, tb := openCustomers(t)
	v := tb.view
	if err := tt.Click("Fetch All"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "every row", func() bool { return v.done && len(v.src.Rows) == 2500 })
}

// lastStatement is the last statement the host recorded as run.
func lastStatement(a *FakeHost) string {
	ran := ""
	for _, e := range a.Events {
		if e.Kind == audit.KindStatement {
			ran = e.Statement
		}
	}
	return ran
}

// What asked about pending changes goes on after Discard, and after
// Apply once they are written.
func TestCheckPendingRunsThen(t *testing.T) {
	a, tt, tb, file := openNumbers(t, 3, 100)
	v := tb.view
	ran := 0
	then := func() { ran++ }
	v.grid.setValue(&v.src, 0, 1, db.Typed("dropped"))
	v.CheckPending(then, nil)
	a.Pending.Discard()
	a.Pending = nil
	if ran != 1 || v.grid.edits.count() != 0 {
		t.Fatalf("Discard: then ran %d times, %d changes left", ran, v.grid.edits.count())
	}
	v.grid.setValue(&v.src, 0, 1, db.Typed("written"))
	v.CheckPending(then, nil)
	a.Pending.Apply()
	a.Pending = nil
	testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the apply", func() bool { return !v.applying && ran == 2 })
	if labelOf(t, file, 1) != "written" {
		t.Fatalf("Apply: %q in the database", labelOf(t, file, 1))
	}
}

// While changes are being written, a sort does not ask about them: it
// goes back, and what runs after the apply stays.
func TestSortWhileApplying(t *testing.T) {
	a, tt, tb, _ := openNumbers(t, 3, 100)
	v := tb.view
	v.truncated = true // the server sorts
	v.grid.setValue(&v.src, 0, 1, db.Typed("edited"))
	v.applying = true
	after := false
	v.afterApply = func() { after = true }
	before := cloneSort(v.grid.sort)
	v.grid.sort, v.grid.sortChanged = ui.SortOrder{Column: colID(1)}, true
	tt.Frame()
	if a.Pending != nil || !sameSort(v.grid.sort, before) || v.grid.edits.count() != 1 {
		t.Fatalf("a sort while applying: asked %v, sort %+v, %d changes", a.Pending != nil, v.grid.sort, v.grid.edits.count())
	}
	v.afterApply()
	if !after {
		t.Fatal("what runs after the apply was replaced")
	}
	v.applying = false
}

// Rows that stopped before the last, at the limit or closed by another
// statement, sort on the server: the rows in memory are not all of them.
func TestSmartSortCutShort(t *testing.T) {
	a, tt, tb, _ := openNumbers(t, 3, 100)
	v := tb.view
	v.truncated = true
	cursor := v.cursor
	if err := tt.Click("label"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the rows read in order", func() bool { return v.cursor != cursor && !v.loading })
	if ran := lastStatement(a); !strings.Contains(ran, `ORDER BY "label"`) {
		t.Fatalf("ran %q", ran)
	}
}

// Cancel on the question about pending changes keeps the filter as it
// was, typed or from the Filter menu.
func TestFilterCancelKeepsFilter(t *testing.T) {
	a, tt, tb, _ := openNumbers(t, 3, 100)
	v := tb.view
	v.grid.setValue(&v.src, 0, 1, db.Typed("edited"))
	if err := tt.Click("Filter"); err != nil {
		t.Fatal(err)
	}
	tt.Type("id > 1")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.Pending == nil {
		t.Fatal("no question")
	}
	a.Pending.Cancel()
	a.Pending = nil
	tt.Frame()
	if v.where != "" || v.typedWhere != "" || v.whereIn != "" || v.count != -1 {
		t.Fatalf("after Cancel: where %q, typed %q, box %q, count %d", v.where, v.typedWhere, v.whereIn, v.count)
	}
	v.addFilter(rowCond{col: 0, op: "=", vals: []any{int64(2)}})
	a.Pending.Cancel()
	a.Pending = nil
	if v.where != "" || len(v.menuFilters) != 0 {
		t.Fatalf("after Cancel of a menu filter: where %q, %d menu filters", v.where, len(v.menuFilters))
	}
	if v.grid.edits.count() != 1 {
		t.Fatal("the change went")
	}
}

// A read that does not start leaves no Fetch All waiting for the next one.
func TestFetchAllForgottenWhenReadRefused(t *testing.T) {
	a, _, tb, _ := openNumbers(t, 3, 100)
	v := tb.view
	v.grid.setValue(&v.src, 0, 1, db.Typed("edited"))
	v.where = "1=1; DELETE FROM n"
	v.FetchAll()
	a.Pending.Discard()
	if v.allAfterRead || !strings.HasPrefix(v.err, "Filter:") {
		t.Fatalf("all after read %v, err %q", v.allAfterRead, v.err)
	}
}

// A filter cannot turn a read-only connection's read-only mode off.
func TestFilterCannotLeaveReadOnly(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := NewFakeHost(t)
	cfg := testutil.PGConfig()
	cfg.ReadOnly = true
	cn := a.AddConn(cfg)
	tt := ui.NewTester(a.View, 1360, 860)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "shop", Name: "customers", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && tb.columns != nil && !tb.view.loading })
	events := len(a.Events)
	tb.view.where = "set_config('default_transaction_read_only', 'off', false) = 'off'"
	tb.view.reload()
	if !strings.HasPrefix(tb.view.err, "Filter:") || tb.view.loading || len(a.Events) != events {
		t.Fatalf("err %q, loading %v, events %+v", tb.view.err, tb.view.loading, a.Events[events:])
	}
}

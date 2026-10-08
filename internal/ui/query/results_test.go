package query

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// numbersEditor opens an editor on a SQLite file whose table n holds rows
// numbered from 11, labelled "row 1" on, and runs setup there first.
func numbersEditor(t *testing.T, rows int, text string, setup ...string) (*fakeQueryHost, *ui.Tester, *Tab, string) {
	t.Helper()
	a := newFakeQueryHost(t)
	file := filepath.Join(t.TempDir(), "n.sqlite")
	os.WriteFile(file, nil, 0o600)
	d, err := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{"CREATE TABLE n (id INTEGER PRIMARY KEY, label TEXT)"}
	for i := 1; i <= rows; i++ {
		stmts = append(stmts, fmt.Sprintf("INSERT INTO n VALUES (%d, 'row %d')", 10+i, i))
	}
	for _, s := range append(stmts, setup...) {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, text)
	testutil.WaitFor(t, tt, "the schemas", func() bool { return cn.DefaultSchema != "" })
	return a, tt, q, file
}

// sqliteValue reads one value from a SQLite file, on a connection of its own.
func sqliteValue(t *testing.T, file, query string) string {
	t.Helper()
	d, err := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var s string
	if err := d.SQL.QueryRow(query).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// editCell types value into the cell showing text, as F2 does.
func editCell(t *testing.T, tt *ui.Tester, text, value string) {
	t.Helper()
	r, ok := tt.Find(text)
	if !ok {
		t.Fatalf("no cell shows %q: %q", text, tt.Texts())
	}
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Key(0, ui.KeyF2)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type(value)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
}

// waitEditable waits for a result's table to be read, which its edits need.
func waitEditable(t *testing.T, tt *ui.Tester) {
	t.Helper()
	testutil.WaitFor(t, tt, "an editable result", func() bool { return tt.HasText("Double-click a cell to edit") })
}

// A result of one table edits as the table does: ⌘S reviews the change,
// applies it on the editor's session, reads the rows again and saves the
// file. Under manual commit the change stays in the editor's transaction,
// which Roll Back undoes.
func TestQueryResultEditApply(t *testing.T) {
	a, tt, q, file := numbersEditor(t, 3, "SELECT * FROM n")
	runWith(t, tt, q, "SELECT", RunStatement)
	waitEditable(t, tt)
	r := q.results[0]
	editCell(t, tt, "row 2", "edited")
	if r.view.PendingCount() != 1 {
		t.Fatalf("%d pending changes", r.view.PendingCount())
	}
	tt.Key(ui.Cmd, ui.KeyS)
	testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
	if !strings.Contains(a.Confirm.Preview, "UPDATE") || !strings.Contains(a.Confirm.Title, "to n?") {
		t.Fatalf("review %q: %q", a.Confirm.Title, a.Confirm.Preview)
	}
	if a.saves != 0 {
		t.Fatal("the file was saved before the changes were applied")
	}
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "applied, read again and saved", func() bool { return a.saves == 1 && !q.Busy() })
	if got := sqliteValue(t, file, "SELECT label FROM n WHERE id = 12"); got != "edited" {
		t.Fatalf("the database holds %q", got)
	}
	if r.view.PendingCount() != 0 || db.Display(r.view.Rows()[1][1]) != "edited" {
		t.Fatalf("after the apply: %d pending, rows %v", r.view.PendingCount(), r.view.Rows())
	}
	if !slices.ContainsFunc(a.Events, func(e audit.Event) bool {
		return e.Kind == audit.KindEdit && strings.Contains(e.Statement, `"n" SET "label"`)
	}) {
		t.Fatalf("no audited edit: %+v", a.Events)
	}

	t.Run("postgres manual commit", func(t *testing.T) {
		testutil.Integration(t)
		testutil.SeedPostgres(t)
		cfg := testutil.PGConfig()
		cfg.Env = db.Production // manual commit
		ctx := context.Background()
		d, err := db.Open(ctx, cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{"DROP TABLE IF EXISTS shop.rv_edit", "CREATE TABLE shop.rv_edit (id INT PRIMARY KEY, v TEXT)",
			"INSERT INTO shop.rv_edit VALUES (1, 'alpha'), (2, 'beta'), (3, 'gamma')"} {
			if _, err := d.SQL.Exec(s); err != nil {
				t.Fatal(err)
			}
		}
		value := func() string {
			var s string
			d.SQL.QueryRow("SELECT v FROM shop.rv_edit WHERE id = 2").Scan(&s)
			return s
		}
		defer d.Close()
		a := newFakeQueryHost(t)
		cn := a.AddConn(cfg)
		tt := ui.NewTester(a.view, 1200, 760)
		q := newEditor(t, a, tt, cn, "SELECT * FROM shop.rv_edit ORDER BY id")
		runWith(t, tt, q, "SELECT", RunStatement)
		waitEditable(t, tt)
		editCell(t, tt, "beta", "edited")
		tt.Key(ui.Cmd, ui.KeyS)
		testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
		a.Confirm.OnConfirm()
		a.Confirm = nil
		testutil.WaitFor(t, tt, "the apply", func() bool { return a.saves == 1 && !q.Busy() })
		if q.Tx == db.TxNone || value() != "beta" {
			t.Fatalf("manual commit: tx %v, others see %q", q.Tx, value())
		}
		if db.Display(q.results[0].view.Rows()[1][1]) != "edited" {
			t.Fatalf("the editor's session does not see its change: %v", q.results[0].view.Rows())
		}
		q.FinishTx(false, func(error) {})
		testutil.WaitFor(t, tt, "the rollback", func() bool { return q.Tx == db.TxNone && !q.Busy() })
		if value() != "beta" {
			t.Fatalf("after the rollback: %q", value())
		}
	})
}

// A result says why it cannot be edited: a join, an aggregate, a group;
// a column the table does not have is read-only on its own; a table
// without a key offers a virtual key, after which its rows edit.
func TestQueryResultReadOnlyReasons(t *testing.T) {
	a, tt, q, _ := numbersEditor(t, 3, "", "CREATE TABLE m (x INT, n_id INT)", "CREATE TABLE nk (a INT, b TEXT)", "INSERT INTO nk VALUES (1, 'one')")
	for _, c := range []struct{ sql, want string }{
		{"SELECT n.id, m.x FROM n JOIN m ON m.n_id = n.id", "The rows join more than one table."},
		{"SELECT count(*) FROM n", "The rows are computed by an aggregate (count)."},
		{"SELECT label FROM n GROUP BY label", "The rows are grouped (GROUP BY)."},
	} {
		q.Editor.Text = c.sql
		runWith(t, tt, q, "SELECT", RunStatement)
		tt.Frame()
		if !tt.HasText(c.want) {
			t.Fatalf("%s: no %q in %q", c.sql, c.want, tt.Texts())
		}
	}

	q.Editor.Text = "SELECT * FROM nk"
	runWith(t, tt, q, "SELECT", RunStatement)
	testutil.WaitFor(t, tt, "the virtual key offer", func() bool { return tt.HasText("Define a Virtual Key…") })
	if err := tt.Click("Define a Virtual Key…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("a  INT"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("Save"); err != nil {
		t.Fatal(err)
	}
	waitEditable(t, tt)
	if got := a.Project.VirtualKeys["lite/main.nk"]; len(got) != 1 || got[0] != "a" {
		t.Fatalf("virtual keys %v", a.Project.VirtualKeys)
	}

	q.Editor.Text = "SELECT id, label AS l, upper(label) FROM n"
	runWith(t, tt, q, "SELECT", RunStatement)
	waitEditable(t, tt)
	r := q.results[0]
	editCell(t, tt, "row 1", "edited")
	if r.view.PendingCount() != 0 {
		t.Fatal("an alias of a column was edited")
	}
	if !tt.HasText("l is renamed from label.") {
		t.Fatalf("no reason for the alias: %q", tt.Texts())
	}
	editCell(t, tt, "ROW 2", "edited")
	if r.view.PendingCount() != 0 {
		t.Fatal("an expression was edited")
	}
	editCell(t, tt, "13", "23")
	if r.view.PendingCount() != 1 {
		t.Fatal("the table's own column did not edit")
	}
}

// A WHERE typed on a result wraps its statement, which keeps the values of
// its parameters; Count counts the wrapped rows.
func TestQueryResultWhereFilter(t *testing.T) {
	a, tt, q, _ := numbersEditor(t, 5, "SELECT * FROM n WHERE id <= :last")
	testutil.SetCaret(tt, &q.Editor, 0)
	q.Run(RunStatement)
	a.dialogs.params.fields[0].value = "13"
	a.dialogs.params.submit()
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && len(q.results) == 1 })
	r := q.results[0]
	if len(r.view.Rows()) != 3 {
		t.Fatalf("rows %v", r.view.Rows())
	}
	if err := tt.Click("Filter"); err != nil {
		t.Fatal(err)
	}
	tt.Type("label <> 'row 2'")
	tt.Key(0, ui.KeyEnter)
	testutil.WaitFor(t, tt, "the filtered rows", func() bool { return !q.Busy() && len(r.view.Rows()) == 2 })
	var ran string
	for _, e := range a.Events {
		if e.Kind == audit.KindStatement {
			ran = e.Statement
		}
	}
	if !strings.HasPrefix(ran, "SELECT * FROM (\nSELECT * FROM n WHERE id <= ") || !strings.HasSuffix(ran, "\n) q WHERE label <> 'row 2'") || strings.Contains(ran, ":last") {
		t.Fatalf("ran %q", ran)
	}
	if err := tt.Click("Count"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the count", func() bool { return tt.HasText("of 2") })
}

// Running again with changes pending asks first: Cancel keeps the result
// and its changes, Discard runs; a script asks too.
func TestRerunAsksPending(t *testing.T) {
	a, tt, q, file := numbersEditor(t, 3, "SELECT * FROM n")
	runWith(t, tt, q, "SELECT", RunStatement)
	waitEditable(t, tt)
	r := q.results[0]
	editCell(t, tt, "row 2", "edited")
	q.Run(RunStatement)
	if a.Pending == nil || a.Pending.N != 1 || q.Running {
		t.Fatalf("no question before the run: %+v, running %v", a.Pending, q.Running)
	}
	a.Pending.Cancel()
	a.Pending = nil
	tt.Frame()
	if q.results[0] != r || r.view.PendingCount() != 1 {
		t.Fatal("Cancel dropped the result or its change")
	}
	q.Run(RunStatement)
	a.Pending.Discard()
	a.Pending = nil
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && len(q.results) == 1 && q.results[0] != r })
	if got := sqliteValue(t, file, "SELECT label FROM n WHERE id = 12"); got != "row 2" {
		t.Fatalf("Discard wrote the change: %q", got)
	}
	waitEditable(t, tt)
	editCell(t, tt, "row 2", "edited")
	q.Run(RunScript)
	if a.Pending == nil || q.Running {
		t.Fatal("a script ran without asking")
	}
	a.Pending.Cancel()
}

// The rows a write returned are not read again to filter, count or sort:
// Refresh runs the statement again only once confirmed.
func TestWriteResultNoFilter(t *testing.T) {
	a, tt, q, file := numbersEditor(t, 3, "INSERT INTO n (label) VALUES ('new') RETURNING id, label")
	runWith(t, tt, q, "INSERT", RunStatement)
	r := q.results[0]
	if r.view == nil || len(r.view.Rows()) != 1 {
		t.Fatalf("no returned rows: %+v", q.results)
	}
	tt.Frame()
	if _, ok := tt.Find("Count"); ok {
		t.Fatal("Count is offered on the rows of an INSERT")
	}
	events := len(a.Events)
	if err := tt.Click("Filter"); err != nil {
		t.Fatal(err)
	}
	tt.Type("id > 0")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(a.Events) != events || q.Busy() {
		t.Fatalf("a filter ran the INSERT again: %+v", a.Events[events:])
	}
	if err := tt.Click("Refresh"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Confirm == nil || !strings.Contains(a.Confirm.Preview, "INSERT") {
		t.Fatalf("Refresh ran the INSERT without asking: %+v", a.Confirm)
	}
	if got := sqliteValue(t, file, "SELECT count(*) FROM n"); got != "4" {
		t.Fatalf("%s rows before the confirmation", got)
	}
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && len(q.results) == 1 && q.results[0] != r })
	if got := sqliteValue(t, file, "SELECT count(*) FROM n"); got != "5" {
		t.Fatalf("%s rows after the confirmed refresh", got)
	}
}

// A column binds to the table's only when the statement reads it as it
// is: a computed or renamed column that takes a column's name is not that
// column, and a computed key leaves the result read-only.
func TestQueryResultComputedColumns(t *testing.T) {
	_, tt, q, file := numbersEditor(t, 3, "SELECT id + 1 AS id, label FROM n")
	runWith(t, tt, q, "SELECT", RunStatement)
	testutil.WaitFor(t, tt, "the reason", func() bool { return tt.HasText("The key column id is not among the result's columns.") })
	editCell(t, tt, "row 2", "edited")
	if n := q.results[0].view.PendingCount(); n != 0 {
		t.Fatalf("%d changes to rows found by a computed key", n)
	}

	q.Editor.Text = "SELECT n.id, label AS label, label AS other FROM n"
	runWith(t, tt, q, "SELECT", RunStatement)
	waitEditable(t, tt)
	editCell(t, tt, "row 2", "edited")
	tt.Key(ui.Cmd, ui.KeyS)
	testutil.WaitFor(t, tt, "the review", func() bool { return q.a.(*fakeQueryHost).Confirm != nil })
	if p := q.a.(*fakeQueryHost).Confirm.Preview; !strings.Contains(p, `"label" = 'edited'`) || !strings.Contains(p, `"id" = 12`) {
		t.Fatalf("review %q", p)
	}
	q.a.(*fakeQueryHost).Confirm.OnConfirm()
	testutil.WaitFor(t, tt, "the apply", func() bool { return !q.Busy() && q.results[0].view.PendingCount() == 0 })
	if got := sqliteValue(t, file, "SELECT label FROM n WHERE id = 12"); got != "edited" {
		t.Fatalf("the database holds %q", got)
	}
}

// An unqualified table is the one the editor's session finds, after its
// own SET search_path; reads on another connection, as Count, are then
// refused, since they would find another table.
func TestQueryResultEditorSchema(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	d, err := db.Open(context.Background(), testutil.PGConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, s := range []string{"DROP SCHEMA IF EXISTS rvprobe CASCADE", "CREATE SCHEMA rvprobe",
		"CREATE TABLE rvprobe.rvprobe_t (id INT PRIMARY KEY, v TEXT)", "INSERT INTO rvprobe.rvprobe_t VALUES (1, 'probe')",
		"DROP TABLE IF EXISTS public.rvprobe_t", "CREATE TABLE public.rvprobe_t (id INT PRIMARY KEY, v TEXT)", "INSERT INTO public.rvprobe_t VALUES (1, 'public')"} {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		d, _ := db.Open(context.Background(), testutil.PGConfig(), nil)
		d.SQL.Exec("DROP SCHEMA IF EXISTS rvprobe CASCADE")
		d.SQL.Exec("DROP TABLE IF EXISTS public.rvprobe_t")
		d.Close()
	})
	value := func(table string) string {
		var s string
		d.SQL.QueryRow("SELECT v FROM " + table + " WHERE id = 1").Scan(&s)
		return s
	}
	a := newFakeQueryHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "SET search_path TO rvprobe;\n\nSELECT * FROM RVPROBE_T;")
	testutil.WaitFor(t, tt, "the schemas", func() bool { return cn.DefaultSchema != "" })
	runWith(t, tt, q, "SET", RunScript)
	waitEditable(t, tt)
	editCell(t, tt, "probe", "edited")
	tt.Key(ui.Cmd, ui.KeyS)
	testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the apply", func() bool { return !q.Busy() && a.saves == 1 })
	if value("rvprobe.rvprobe_t") != "edited" || value("public.rvprobe_t") != "public" {
		t.Fatalf("rvprobe %q, public %q", value("rvprobe.rvprobe_t"), value("public.rvprobe_t"))
	}
}

// Count runs on the editor's own session, so it counts what the result
// read: the tables of the editor's search_path, and the rows its open
// transaction added, temp tables too.
func TestCountSeesEditorState(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	d, err := db.Open(context.Background(), testutil.PGConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, s := range []string{"DROP SCHEMA IF EXISTS rvcount CASCADE", "CREATE SCHEMA rvcount",
		"CREATE TABLE rvcount.rvc_a (id INT)", "CREATE TABLE rvcount.rvc_b (id INT)",
		"INSERT INTO rvcount.rvc_a VALUES (1), (2), (3)", "INSERT INTO rvcount.rvc_b VALUES (1), (2), (3)",
		"DROP TABLE IF EXISTS public.rvc_a, public.rvc_b", "CREATE TABLE public.rvc_a (id INT)", "CREATE TABLE public.rvc_b (id INT)",
		"INSERT INTO public.rvc_a VALUES (1)", "INSERT INTO public.rvc_b VALUES (1)"} {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		d, _ := db.Open(context.Background(), testutil.PGConfig(), nil)
		d.SQL.Exec("DROP SCHEMA IF EXISTS rvcount CASCADE")
		d.SQL.Exec("DROP TABLE IF EXISTS public.rvc_a, public.rvc_b")
		d.Close()
	})
	a := newFakeQueryHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "SET search_path TO rvcount;\n\nBEGIN;\n\nINSERT INTO rvc_a VALUES (4);\n\nINSERT INTO rvc_b VALUES (4);\n\n"+
		"CREATE TEMP TABLE rvc_tmp AS SELECT 1 AS x;\n\nSELECT * FROM rvc_a JOIN rvc_b USING (id);\n\nSELECT * FROM rvc_tmp;")
	t.Cleanup(func() { q.sess.Close() })
	testutil.WaitFor(t, tt, "the schemas", func() bool { return cn.DefaultSchema != "" })
	runWith(t, tt, q, "SET", RunScript)
	if len(q.results) < 2 {
		t.Fatalf("results %d", len(q.results))
	}
	for i, want := range map[int]string{len(q.results) - 2: "of 4", len(q.results) - 1: "of 1"} {
		q.resultIdx = i
		tt.Frame()
		if err := tt.Click("Count"); err != nil {
			t.Fatalf("result %d: %v %q", i, err, tt.Texts())
		}
		testutil.WaitFor(t, tt, "the count "+want, func() bool { return tt.HasText(want) })
	}
}

// While a result still reads its rows, a statement on the session would
// end them: Count waits for them all, with the reason.
func TestCountRefusedWhileCursorOpen(t *testing.T) {
	testutil.Integration(t)
	a := newFakeQueryHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "SELECT g FROM generate_series(1, 5000) g")
	runWith(t, tt, q, "SELECT", RunStatement)
	r := q.results[0]
	if r.view.Done() {
		t.Fatal("the rows were all read at once")
	}
	if _, ok := tt.Find("Count"); ok || !strings.Contains(q.cursorOpen(), "Fetch All") {
		t.Fatalf("Count offered while the cursor is open: %q", q.cursorOpen())
	}
	if err := tt.Click("Fetch All"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "every row", func() bool { return r.view.Done() && !q.Busy() })
	if err := tt.Click("Count"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the count", func() bool { return tt.HasText("of 5000") })
}

// A run waits for a count on the session: cancelled, the count would
// abort the editor's transaction.
func TestRunRefusedWhileCounting(t *testing.T) {
	testutil.Integration(t)
	a := newFakeQueryHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "BEGIN;\n\nSELECT pg_sleep(1)::text AS x;\n\nSELECT 42 AS y")
	t.Cleanup(func() { q.sess.Close() })
	runWith(t, tt, q, "BEGIN", RunScript)
	q.resultIdx = len(q.results) - 2
	tt.Frame()
	if err := tt.Click("Count"); err != nil {
		t.Fatal(err)
	}
	runWith(t, tt, q, "SELECT 42", RunStatement)
	if len(a.Errors) == 0 || !strings.Contains(a.Errors[len(a.Errors)-1], "A count is running") {
		t.Fatalf("errors %q", a.Errors)
	}
	testutil.WaitFor(t, tt, "the count", func() bool { return !q.Busy() })
	if q.Tx != db.TxOpen || len(a.Errors) != 1 {
		t.Fatalf("tx %v, errors %q", q.Tx, a.Errors)
	}
	// Closing the tab while it counts says nothing of the cancelled count.
	runWith(t, tt, q, "SELECT pg_sleep", RunStatement)
	if err := tt.Click("Count"); err != nil {
		t.Fatal(err)
	}
	if !q.Busy() {
		t.Fatal("no count on its way")
	}
	q.Close()
	time.Sleep(300 * time.Millisecond)
	for range 3 {
		tt.Frame()
	}
	if len(a.Errors) != 1 {
		t.Fatalf("errors after closing %q", a.Errors)
	}
}

// A database of one connection reads its rows ahead: they hold nothing,
// and Count runs while the grid still pages through them.
func TestCountWithRowsReadAhead(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "mem", Name: "mem", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "WITH RECURSIVE g(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM g WHERE x < 5000) SELECT x FROM g")
	runWith(t, tt, q, "WITH", RunStatement)
	if q.results[0].view.Done() || q.cursorOpen() != "" {
		t.Fatalf("done %v, cursor %q", q.results[0].view.Done(), q.cursorOpen())
	}
	if err := tt.Click("Count"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the count", func() bool { return tt.HasText("of 5000") })
}

// A table's name matches as the engine folds it.
func TestFindObjectFolds(t *testing.T) {
	objs := []db.Object{{Name: "orders"}, {Name: "Mixed"}}
	for _, c := range []struct {
		engine db.Engine
		name   string
		quoted bool
		want   string
	}{
		{db.Postgres, "ORDERS", false, "orders"},
		{db.Postgres, "Mixed", false, ""},
		{db.Postgres, "Mixed", true, "Mixed"},
		{db.Postgres, "ORDERS", true, ""},
		{db.MySQL, "MIXED", false, "Mixed"},
		{db.SQLite, "Orders", true, "orders"},
		{db.ClickHouse, "Orders", false, ""},
		{db.ClickHouse, "orders", false, "orders"},
	} {
		o, ok := findObject(objs, c.name, c.quoted, c.engine)
		if ok != (c.want != "") || ok && o.Name != c.want {
			t.Errorf("%s %q quoted %v: %q %v", c.engine, c.name, c.quoted, o.Name, ok)
		}
	}
}

// An apply stopped midway, as by closing the editor, rolls back what it
// wrote: nothing of the batch stays, and the session has no transaction.
// On MySQL the end of the context kills only the statement waiting, so
// the rollback must still be sent.
func TestQueryResultApplyCancelledRollsBack(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	for _, c := range []struct {
		cfg     db.Config
		waiting string
	}{
		{testutil.PGConfig(), "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE 'UPDATE%rv_cancel%'"},
		{db.Config{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"},
			"SELECT count(*) FROM information_schema.processlist WHERE state = 'updating' AND info LIKE '%rv_cancel%WHERE `id` = 2'"},
	} {
		t.Run(string(c.cfg.Engine), func(t *testing.T) {
			ctx := context.Background()
			d, err := db.Open(ctx, c.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			for _, s := range []string{"DROP TABLE IF EXISTS shop.rv_cancel", "CREATE TABLE shop.rv_cancel (id INT PRIMARY KEY, v TEXT)",
				"INSERT INTO shop.rv_cancel VALUES (1, 'alpha'), (2, 'beta'), (3, 'gamma')"} {
				if _, err := d.SQL.Exec(s); err != nil {
					t.Fatal(err)
				}
			}
			a := newFakeQueryHost(t)
			cn := a.AddConn(c.cfg)
			tt := ui.NewTester(a.view, 1200, 760)
			q := newEditor(t, a, tt, cn, "SELECT * FROM shop.rv_cancel ORDER BY id")
			runWith(t, tt, q, "SELECT", RunStatement)
			waitEditable(t, tt)
			editCell(t, tt, "alpha", "one")
			editCell(t, tt, "beta", "two")
			// Another connection holds the second row: the batch waits there.
			holder, err := d.SQL.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback()
			if _, err := holder.Exec("UPDATE shop.rv_cancel SET v = 'held' WHERE id = 2"); err != nil {
				t.Fatal(err)
			}
			tt.Key(ui.Cmd, ui.KeyS)
			testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
			a.Confirm.OnConfirm()
			a.Confirm = nil
			r := q.results[0]
			testutil.WaitFor(t, tt, "the second row waited for", func() bool {
				var n int
				d.SQL.QueryRow(c.waiting).Scan(&n)
				return n == 1
			})
			if !r.view.Applying() {
				t.Fatal("the apply is not on its way")
			}
			r.view.Release()()
			testutil.WaitFor(t, tt, "the apply's end", func() bool { return !r.view.Applying() })
			holder.Rollback()
			var v string
			d.SQL.QueryRow("SELECT v FROM shop.rv_cancel WHERE id = 1").Scan(&v)
			if v != "alpha" || q.Tx != db.TxNone || q.sess.Tx() != db.TxNone {
				t.Fatalf("after the cancelled apply: row 1 %q, tx %v, session tx %v", v, q.Tx, q.sess.Tx())
			}
			var outcome audit.Event
			for _, e := range a.Events {
				if e.Kind == audit.KindEdit && e.Statement == "" {
					outcome = e
				}
			}
			if !strings.HasSuffix(outcome.Detail, "rolled back") || outcome.Error == "" {
				t.Fatalf("audited outcome %+v", outcome)
			}
		})
	}
}

// An apply stopped between two of its statements, before the second is
// sent, rolls the first back: the stopped context cannot send ROLLBACK.
func TestQueryResultApplyStoppedBetweenStatements(t *testing.T) {
	a, tt, q, file := numbersEditor(t, 3, "SELECT * FROM n")
	runWith(t, tt, q, "SELECT", RunStatement)
	waitEditable(t, tt)
	editCell(t, tt, "row 1", "one")
	editCell(t, tt, "row 2", "two")
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a.afterRun = func(kind string) {
		if kind == audit.KindEdit {
			once.Do(func() {
				close(paused)
				<-resume
			})
		}
	}
	tt.Key(ui.Cmd, ui.KeyS)
	testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
	a.Confirm.OnConfirm()
	a.Confirm = nil
	r := q.results[0]
	<-paused
	r.view.Release()()
	close(resume)
	testutil.WaitFor(t, tt, "the apply's end", func() bool { return !r.view.Applying() })
	if got := sqliteValue(t, file, "SELECT label FROM n WHERE id = 11"); got != "row 1" || q.sess.Tx() != db.TxNone {
		t.Fatalf("after the stopped apply: row 1 %q, session tx %v", got, q.sess.Tx())
	}
}

// A run that drops two results with changes asks for each, then runs.
func TestRunAsksEachResult(t *testing.T) {
	a, tt, q, _ := numbersEditor(t, 3, "SELECT * FROM n")
	runWith(t, tt, q, "SELECT", RunStatement)
	waitEditable(t, tt)
	editCell(t, tt, "row 1", "first")
	runWith(t, tt, q, "SELECT", RunNewTab)
	waitEditable(t, tt)
	editCell(t, tt, "row 2", "second")
	first, second := q.results[0], q.results[1]
	if first.view.PendingCount() != 1 || second.view.PendingCount() != 1 {
		t.Fatalf("pending %d and %d", first.view.PendingCount(), second.view.PendingCount())
	}
	q.Run(RunStatement)
	for i := range 2 {
		p := a.Pending
		if p == nil || q.Running {
			t.Fatalf("question %d not asked, running %v", i+1, q.Running)
		}
		a.Pending = nil
		p.Discard()
	}
	testutil.WaitFor(t, tt, "the run", func() bool {
		return !q.Running && len(q.results) == 1 && q.results[0] != first && q.results[0] != second
	})
}

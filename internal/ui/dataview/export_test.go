package dataview

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/export"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

func TestExportFileNamePattern(t *testing.T) {
	now := time.Date(2026, 10, 8, 14, 5, 6, 0, time.UTC)
	if got := expandPattern("${table}_${timestamp}", "orders", "prod", now); got != "orders_20261008-140506" {
		t.Fatalf("got %q", got)
	}
	if got := expandPattern("${connection}/${date}", "t", "a:b", now); got != "a_b_2026-10-08" {
		t.Fatalf("unsafe characters: %q", got)
	}
}

func TestExportRefusesWrites(t *testing.T) {
	cn := &connection.Conn{Config: db.Config{Engine: db.SQLite}}
	if rerunReason(ExportSource{Conn: cn, SQL: "SELECT 1"}) != "" {
		t.Fatal("a read is refused")
	}
	for _, sql := range []string{"DELETE FROM t RETURNING *", "INSERT INTO t VALUES (1)", ""} {
		if rerunReason(ExportSource{Conn: cn, SQL: sql}) == "" {
			t.Errorf("%q would run again", sql)
		}
	}
}

// exportHost is a host with a SQLite file of 1,200 rows, and the source
// of an export of their query, of which a page was read.
func exportHost(t *testing.T) (*FakeHost, *ui.Tester, ExportSource) {
	t.Helper()
	a := NewFakeHost(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	setup, _ := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	setup.SQL.Exec("CREATE TABLE n (i INTEGER, s TEXT)")
	setup.SQL.Exec("WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM r WHERE i < 1200) INSERT INTO n SELECT i, 'row ' || i FROM r")
	setup.Close()
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.View, 1100, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	const query = "SELECT * FROM n ORDER BY i"
	rs, err := cn.DB.SQL.Query(query + " LIMIT 500")
	if err != nil {
		t.Fatal(err)
	}
	var page [][]any
	for rs.Next() {
		var i int64
		var s string
		rs.Scan(&i, &s)
		page = append(page, []any{i, s})
	}
	rs.Close()
	cols := []db.ColumnInfo{{Name: "i", Type: "INTEGER"}, {Name: "s", Type: "TEXT"}}
	return a, tt, ExportSource{Conn: cn, Name: "numbers", SQL: query, Cols: cols,
		RowsRead: func() [][]any { return page }, Read: len(page)}
}

func runExportTo(t *testing.T, a *FakeHost, tt *ui.Tester, src ExportSource, format export.Format, all bool) string {
	t.Helper()
	OpenExport(a, src)
	x := a.dialogs.export
	x.format, x.all, x.folder = string(format), all, t.TempDir()
	tt.Frame()
	path := x.pathOf(src.Name)
	runExport(a, x)
	testutil.WaitFor(t, tt, "the export", func() bool { return !x.running && (a.dialogs.export == nil || x.err != "") })
	if x.err != "" {
		t.Fatal(x.err)
	}
	return path
}

// An export runs the statement again for every row, not only those read.
func TestExportReRunsFullQuery(t *testing.T) {
	a, tt, src := exportHost(t)
	path := runExportTo(t, a, tt, src, export.CSV, true)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 1201 {
		t.Fatalf("%d lines, want a header and 1,200 rows", lines)
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 { // Windows has no Unix modes
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	// Only the rows read, when asked.
	path = runExportTo(t, a, tt, src, export.CSV, false)
	data, _ = os.ReadFile(path)
	if lines := strings.Count(string(data), "\n"); lines != src.Read+1 {
		t.Fatalf("rows read: %d lines", lines)
	}
}

func TestExportParquet(t *testing.T) {
	a, tt, src := exportHost(t)
	path := runExportTo(t, a, tt, src, export.Parquet, true)
	d, err := db.Open(context.Background(), db.Config{Name: "d", Engine: db.DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int64
	if err := d.SQL.QueryRow("SELECT count(*) FROM read_parquet('" + strings.ReplaceAll(path, "'", "''") + "')").Scan(&n); err != nil || n != 1200 {
		t.Fatalf("parquet rows %d: %v", n, err)
	}
	audited := false
	for _, e := range a.Events {
		audited = audited || e.Kind == audit.KindExport && strings.Contains(e.Detail, "Parquet to "+path)
	}
	if !audited {
		t.Fatalf("no audit of the export: %+v", a.Events)
	}
}

// A statement run again stops at the export's limit, and the export says
// that rows were left; at a limit the rows do not reach, it says nothing.
func TestExportStopsAtItsLimit(t *testing.T) {
	a, tt, src := exportHost(t)
	for _, c := range []struct {
		limit   int
		lines   int
		stopped bool
	}{{100, 101, true}, {5000, 1201, false}} {
		OpenExport(a, src)
		x := a.dialogs.export
		x.format, x.all, x.folder, x.limited, x.limit = string(export.CSV), true, t.TempDir(), true, float64(c.limit)
		tt.Frame()
		path := x.pathOf(src.Name)
		runExport(a, x)
		testutil.WaitFor(t, tt, "the export", func() bool { return !x.running })
		if x.err != "" {
			t.Fatal(x.err)
		}
		data, _ := os.ReadFile(path)
		toast := a.Toasts[len(a.Toasts)-1]
		if lines := strings.Count(string(data), "\n"); lines != c.lines || strings.Contains(toast, "limit") != c.stopped {
			t.Fatalf("limit %d: %d lines, toast %q", c.limit, lines, toast)
		}
		if a.Settings().Export.Limit() != c.limit {
			t.Fatalf("the limit %d is not kept: %+v", c.limit, a.Settings().Export)
		}
	}
	last := a.Events[len(a.Events)-1]
	if strings.Contains(last.Detail, "limit") {
		t.Fatalf("an export within its limit is audited as stopped: %q", last.Detail)
	}
}

func TestExportExcel(t *testing.T) {
	a, tt, src := exportHost(t)
	path := runExportTo(t, a, tt, src, export.XLSX, true)
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "xl/worksheets/sheet1.xml" {
			continue
		}
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		if rows := strings.Count(string(data), "<row "); rows != 1201 {
			t.Fatalf("%d rows, want a header and 1,200", rows)
		}
		return
	}
	t.Fatal("no sheet in the workbook")
}

// Tables chosen export each to a file of its own, its INSERTs into its
// own name; the limit stops each; one left out is not written.
func TestExportTables(t *testing.T) {
	a, tt, src := exportHost(t)
	for _, q := range []string{"CREATE TABLE m (x TEXT)", "INSERT INTO m VALUES ('a'), ('b')", "CREATE TABLE skipped (y)"} {
		if _, err := src.Conn.DB.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var srcs []ExportSource
	for _, name := range []string{"n", "m", "skipped"} {
		srcs = append(srcs, ExportSource{Conn: src.Conn, Name: name, SQL: "SELECT * FROM " + name})
	}
	OpenExportTables(a, "Export tables of main", srcs, []bool{true, true, false})
	x := a.dialogs.export
	dir := t.TempDir()
	x.format, x.folder, x.pattern, x.limited, x.limit = string(export.SQL), dir, "${table}", true, 100
	tt.Frame()
	testutil.Snapshot(t, tt, "export-tables")
	if !tt.HasText("2 of 3 chosen, each to a file of its own") {
		t.Fatalf("texts %q", tt.Texts())
	}
	runExport(a, x)
	testutil.WaitFor(t, tt, "the export", func() bool { return !x.running })
	if x.err != "" {
		t.Fatal(x.err)
	}
	n, _ := os.ReadFile(filepath.Join(dir, "n.sql"))
	m, _ := os.ReadFile(filepath.Join(dir, "m.sql"))
	if strings.Count(string(n), "INSERT INTO \"n\" (") != 100 || strings.Count(string(m), "INSERT INTO \"m\" (") != 2 {
		t.Fatalf("n.sql:\n%.300s\nm.sql:\n%s", n, m)
	}
	if _, err := os.Stat(filepath.Join(dir, "skipped.sql")); err == nil {
		t.Fatal("a table left out was exported")
	}
	if toast := a.Toasts[len(a.Toasts)-1]; toast != "Exported 102 rows of 2 tables: the limit of 100 rows stopped n, with rows left" {
		t.Fatalf("toast %q", toast)
	}
	if len(a.Events) < 2 || !strings.Contains(a.Events[len(a.Events)-2].Detail, "n.sql") || !strings.Contains(a.Events[len(a.Events)-1].Detail, "m.sql") {
		t.Fatalf("audit %+v", a.Events)
	}
}

// The formula guard is off at first; once chosen, the next export keeps
// it, and the file's text is guarded.
func TestFormulaGuardRemembered(t *testing.T) {
	a, tt, src := exportHost(t)
	src.SQL, src.Name, src.RowsRead, src.Read = "SELECT '=1+1' AS f, -5 AS n", "guarded", nil, 0
	OpenExport(a, src)
	if a.dialogs.export.guard {
		t.Fatal("the formula guard is on by default")
	}
	a.Settings().Export.FormulaGuard = true
	path := runExportTo(t, a, tt, src, export.CSV, true)
	data, _ := os.ReadFile(path)
	if string(data) != "f,n\n'=1+1,-5\n" {
		t.Fatalf("got %q", data)
	}
	if !a.Settings().Export.FormulaGuard {
		t.Fatal("the formula guard is not kept")
	}
	a.dialogs.export = nil
	OpenExport(a, src)
	if !a.dialogs.export.guard {
		t.Fatal("the formula guard is not remembered")
	}
}

func TestDuckDBExportStyle(t *testing.T) {
	cn := &connection.Conn{Config: db.Config{Engine: db.DuckDB}}
	if got := SQLOptionsFor(cn, "", "t").Engine; got != db.DuckDB {
		t.Fatalf("DuckDB exports in style %q", got)
	}
}

// A source's schema qualifies the table of the SQL INSERTs; cleared, the
// table goes bare; each table of Export Tables keeps its own schema.
func TestExportSQLNamesSchema(t *testing.T) {
	a, tt, src := exportHost(t)
	src.Schema = "s"
	path := runExportTo(t, a, tt, src, export.SQL, false)
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `INSERT INTO "s"."numbers" (`) {
		t.Fatalf("with schema:\n%.300s", data)
	}

	OpenExport(a, src)
	x := a.dialogs.export
	x.format, x.all, x.folder, x.schema = string(export.SQL), false, t.TempDir(), ""
	tt.Frame()
	path = x.pathOf(src.Name)
	runExport(a, x)
	testutil.WaitFor(t, tt, "the export", func() bool { return !x.running })
	if x.err != "" {
		t.Fatal(x.err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `INSERT INTO "numbers" (`) {
		t.Fatalf("cleared schema:\n%.300s", data)
	}

	if _, err := src.Conn.DB.SQL.Exec("CREATE TABLE m (x TEXT); INSERT INTO m VALUES ('a')"); err != nil {
		t.Fatal(err)
	}
	srcs := []ExportSource{
		{Conn: src.Conn, Schema: "one", Name: "n", SQL: "SELECT * FROM n"},
		{Conn: src.Conn, Schema: "two", Name: "m", SQL: "SELECT * FROM m"},
	}
	OpenExportTables(a, "Export tables", srcs, []bool{true, true})
	x = a.dialogs.export
	dir := t.TempDir()
	x.format, x.folder, x.pattern = string(export.SQL), dir, "${table}"
	tt.Frame()
	runExport(a, x)
	testutil.WaitFor(t, tt, "the export", func() bool { return !x.running })
	if x.err != "" {
		t.Fatal(x.err)
	}
	n, _ := os.ReadFile(filepath.Join(dir, "n.sql"))
	m, _ := os.ReadFile(filepath.Join(dir, "m.sql"))
	if !strings.Contains(string(n), `INSERT INTO "one"."n" (`) || !strings.Contains(string(m), `INSERT INTO "two"."m" (`) {
		t.Fatalf("n.sql:\n%.300s\nm.sql:\n%s", n, m)
	}
}

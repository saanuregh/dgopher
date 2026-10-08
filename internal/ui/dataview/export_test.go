package dataview

import (
	"context"
	"os"
	"path/filepath"
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
	path := x.path()
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
	if info.Mode().Perm() != 0o600 {
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

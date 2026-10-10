package app

import (
	"context"
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

// exportDuckDB writes a DuckDB backup folder holding a table t.
func exportDuckDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	src, err := db.Open(ctx, db.Config{Name: "src", Engine: db.DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dir := filepath.Join(t.TempDir(), "backup")
	for _, q := range []string{`CREATE TABLE t AS SELECT 7 AS a`, "EXPORT DATABASE " + db.Literal(db.DuckDB, dir) + " (FORMAT parquet)"} {
		if _, err := src.SQL.ExecContext(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	return dir
}

// DuckDB's sessions share one connection: a restore while an editor holds
// a transaction open there would run inside it, to be committed or rolled
// back with the editor's work. It is refused, before it is asked and again
// as it runs.
func TestRestoreRefusedWithOpenTransaction(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	folder := exportDuckDB(t)
	stages := t.TempDir()
	t.Setenv("TMPDIR", stages)
	cn := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	editor, err := cn.DB.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer editor.Close()
	begin := func() {
		t.Helper()
		for _, q := range []string{"BEGIN", "CREATE TABLE pending (a INT)"} {
			if _, err := editor.Exec(ctx, q); err != nil {
				t.Fatal(q, err)
			}
		}
	}
	tables := func() string {
		t.Helper()
		var names string
		if err := cn.DB.SQL.QueryRow(`SELECT coalesce(string_agg(table_name, ',' ORDER BY table_name), '') FROM duckdb_tables()`).Scan(&names); err != nil {
			t.Fatal(err)
		}
		return names
	}
	asked := func(what string) {
		t.Helper()
		testutil.WaitFor(t, tt, what, func() bool { return !a.backup.running && (a.backup.err != "" || a.confirm != nil) })
	}

	begin()
	a.openBackup(cn, "", true)
	b := a.backup
	b.path = folder
	a.startBackup(b)
	asked("the refusal")
	if a.confirm != nil || !strings.Contains(b.err, "transaction") {
		t.Fatalf("asked %v, error %q", a.confirm != nil, b.err)
	}
	if editor.Tx() != db.TxOpen {
		t.Fatal("the editor's transaction ended")
	}

	if _, err := editor.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	a.startBackup(b)
	asked("the question")
	if a.confirm == nil {
		t.Fatalf("not asked: %q", b.err)
	}
	begin() // after the question, before it runs
	a.confirm.OnConfirm()
	a.confirm = nil
	testutil.WaitFor(t, tt, "the refusal", func() bool { return !b.running && (b.err != "" || b.done) })
	if b.done || !strings.Contains(b.err, "transaction") {
		t.Fatalf("restored %v, error %q", b.done, b.err)
	}
	if editor.Tx() != db.TxOpen {
		t.Fatal("the editor's transaction ended")
	}
	if _, err := editor.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if names := tables(); names != "" {
		t.Fatalf("tables after the refusals: %q", names)
	}

	a.startBackup(b)
	asked("the question")
	if a.confirm == nil {
		t.Fatalf("not asked: %q", b.err)
	}
	a.confirm.OnConfirm()
	a.confirm = nil
	testutil.WaitFor(t, tt, "the restore", func() bool { return !b.running && (b.err != "" || b.done) })
	if b.err != "" {
		t.Fatal(b.err)
	}
	var v int
	if err := cn.DB.SQL.QueryRow(`SELECT a FROM t`).Scan(&v); err != nil || v != 7 {
		t.Fatalf("restored %d: %v", v, err)
	}
	events, _ := cn.Project.Audit.Read(0)
	if last := events[0]; last.Kind != audit.KindRestore || !strings.Contains(last.Statement, "COPY FROM DATABASE") || last.Error != "" {
		t.Fatalf("audited %+v", last)
	}
	if left, _ := os.ReadDir(stages); len(left) > 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// A SQL file restores as Run SQL File runs it, asked once, after reading it
// through, as every restore is: always, the connection's name typed on
// production.
func TestSQLRestoreConfirms(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	sql := filepath.Join(t.TempDir(), "rows.sql")
	if err := os.WriteFile(sql, []byte("INSERT INTO t VALUES (1);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, env := range []db.Environment{db.Development, db.Production} {
		file := filepath.Join(t.TempDir(), "x.sqlite")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		cn := addConn(a, db.Config{ID: "lite-" + string(env), Name: "lite " + string(env), Engine: db.SQLite, Database: file, Env: env})
		a.openBackup(cn, "", true)
		a.backup.path = sql
		a.startBackup(a.backup)
		// It asks once, after reading the file through, as a restore: on
		// development too, where the file's INSERT alone would not ask.
		x := a.sqlFile
		if a.confirm != nil || x == nil || x.path != sql || a.backup.open {
			t.Fatalf("%s: asked before the file was read: %+v", env, a.confirm)
		}
		testutil.WaitFor(t, tt, "the survey", func() bool { return !x.reading })
		a.confirmSQLFile(x)
		if a.confirm == nil || !strings.HasPrefix(a.confirm.Title, "Restore into") || a.confirm.Action != "Restore" ||
			!strings.Contains(strings.Join(a.confirm.Reasons, " "), "Restoring writes") || a.confirm.TypeName != (env == db.Production) {
			t.Fatalf("%s: %+v", env, a.confirm)
		}
		a.confirm, a.sqlFile, a.backup = nil, nil, nil
	}
	ro := addConn(a, db.Config{ID: "ro", Name: "ro", Engine: db.SQLite, Database: filepath.Join(t.TempDir(), "ro.sqlite"), ReadOnly: true})
	a.openBackup(ro, "", true)
	if a.backup != nil {
		t.Fatal("a read-only connection restores")
	}
}

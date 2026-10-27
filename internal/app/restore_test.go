package app

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/testutil/optionalserver"

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
	connectNow(t, a, tt, cn)
	editor, err := cn.DB().Session(ctx)
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
		if err := cn.DB().SQL.QueryRow(`SELECT coalesce(string_agg(table_name, ',' ORDER BY table_name), '') FROM duckdb_tables()`).Scan(&names); err != nil {
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
	if err := cn.DB().SQL.QueryRow(`SELECT a FROM t`).Scan(&v); err != nil || v != 7 {
		t.Fatalf("restored %d: %v", v, err)
	}
	events, _ := cn.Project.Audit.Read(0, false)
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
		file := writeSQL(t, "x.sqlite", "")
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

// A YugabyteDB restore runs a statement at a time, not in one
// transaction: the job, and its failure, say that what was restored so
// far stays.
func TestRestoreNotInOneTransactionSaysWhatStays(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "yugabytedb")
	bin := t.TempDir()
	release := filepath.Join(bin, "release")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'pg_restore (PostgreSQL) 16.4'; exit 0; fi\n" +
		"i=0; while [ ! -f '" + release + "' ] && [ $i -lt 600 ]; do sleep 0.05; i=$((i+1)); done\necho 'pg_restore: error: could not execute query' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "pg_restore"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	archive := filepath.Join(t.TempDir(), "x.dump")
	if err := os.WriteFile(archive, []byte("PGDMP-not-really"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "yb", Name: "yb", Engine: db.YugabyteDB, Host: "127.0.0.1", Port: 15433, User: "yugabyte", Password: "dgopher", Database: "shop"})
	tt := ui.NewTester(a.view, 1200, 800)
	connectNow(t, a, tt, cn)
	a.openBackup(cn, "shop", true)
	b := a.backup
	b.path = archive
	a.startBackup(b)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.confirm != nil || b.err != "" })
	if a.confirm == nil {
		t.Fatalf("not asked: %q", b.err)
	}
	a.confirm.OnConfirm()
	a.confirm = nil
	const stays = "stopping it, or an error, keeps what was restored so far."
	testutil.WaitFor(t, tt, "the job", func() bool { return len(a.runningJobs()) > 0 })
	if title := a.runningJobs()[0].title; !strings.HasSuffix(title, stays) || strings.Contains(title, "rolls it back") {
		t.Fatalf("job %q", title)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the failure", func() bool { return !b.running && (b.err != "" || b.done) })
	if !strings.Contains(b.err, "What was restored before the error stays") {
		t.Fatalf("error %q", b.err)
	}
}

// A YugabyteDB backup that pg_dump 17 or later writes as an archive, with
// no ysql_dump installed, asks first: the archive restores only into
// PostgreSQL. Declining runs nothing.
func TestBackupAsksBeforePgDumpArchiveOfYugabyteDB(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "yugabytedb")
	bin := t.TempDir()
	ran := filepath.Join(bin, "ran")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'pg_dump (PostgreSQL) 18.6'; exit 0; fi\n: > '" + ran + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "pg_dump"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "yb", Name: "yb", Engine: db.YugabyteDB, Host: "127.0.0.1", Port: 15433, User: "yugabyte", Password: "dgopher", Database: "shop"})
	tt := ui.NewTester(a.view, 1200, 800)
	connectNow(t, a, tt, cn)
	a.openBackup(cn, "shop", false)
	b := a.backup
	formats, _ := backupRules(db.YugabyteDB)
	b.format = slices.Index(formats, db.BackupArchive)
	b.path = filepath.Join(t.TempDir(), "x.dump")
	a.startBackup(b)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.confirm != nil || !b.running })
	if a.confirm == nil || !slices.ContainsFunc(a.confirm.Reasons, func(r string) bool { return strings.Contains(r, "restores only into PostgreSQL") }) {
		t.Fatalf("not asked: %+v, error %q", a.confirm, b.err)
	}
	r := a.confirm
	r.Open, a.confirm = false, nil
	r.OnCancel()
	testutil.WaitFor(t, tt, "the end", func() bool { return !b.running })
	if _, err := os.Stat(ran); err == nil || b.done || b.err != "" {
		t.Fatalf("ran %v, done %v, error %q", err == nil, b.done, b.err)
	}
}

// A TimescaleDB restore runs pg_restore between timescaledb_pre_restore()
// and timescaledb_post_restore() on the database restored into: while it
// runs, the database is set to restore, and TimescaleDB is told so in
// pg_restore's own session; after it, the setting is gone, and no
// connection of the pool is left restoring, those opened meanwhile
// included, so that a hypertable's rows go into chunks. Both calls are
// audited with the run.
func TestRestoreOfTimescaleDBWrapsPgRestore(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "timescaledb")
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql is not installed")
	}
	bin := t.TempDir()
	seen, started, proceed := filepath.Join(bin, "seen"), filepath.Join(bin, "started"), filepath.Join(bin, "proceed")
	// The fake pg_restore records what the database is set to, and its
	// own PGOPTIONS, then waits for the test to open connections.
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'pg_restore (PostgreSQL) 18.6'; exit 0; fi\n" +
		"{ echo \"$PGOPTIONS\"; PGOPTIONS= psql 'host=127.0.0.1 port=15434 user=postgres dbname=shop' -Atc " +
		"\"SELECT array_to_string(setconfig, ',') FROM pg_db_role_setting WHERE setdatabase = (SELECT oid FROM pg_database WHERE datname = 'shop') AND setrole = 0\"; } > '" + seen + "' 2>&1\n" +
		"touch '" + started + "'\nfor i in $(seq 200); do [ -e '" + proceed + "' ] && exit 0; sleep 0.1; done\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "pg_restore"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	archive := filepath.Join(t.TempDir(), "x.dump")
	if err := os.WriteFile(archive, []byte("PGDMP-not-really"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "ts", Name: "ts", Engine: db.TimescaleDB, Host: "127.0.0.1", Port: 15434, User: "postgres", Password: "dgopher", Database: "shop"})
	tt := ui.NewTester(a.view, 1200, 800)
	connectNow(t, a, tt, cn)
	pool := cn.DB().SQL
	ctx := context.Background()
	t.Cleanup(func() {
		// Not restoring, which would leave TimescaleDB's catalog holding
		// the hypertable dropped.
		c, err := pool.Conn(ctx)
		if err != nil {
			return
		}
		defer c.Close()
		c.ExecContext(ctx, "ALTER DATABASE shop RESET timescaledb.restoring")
		c.ExecContext(ctx, "SET timescaledb.restoring = off")
		c.ExecContext(ctx, "DROP SCHEMA IF EXISTS it_ts_restore CASCADE")
	})
	for _, q := range []string{"DROP SCHEMA IF EXISTS it_ts_restore CASCADE", "CREATE SCHEMA it_ts_restore",
		"CREATE TABLE it_ts_restore.m (t timestamptz NOT NULL, v int)", "SELECT create_hypertable('it_ts_restore.m', 't')"} {
		if _, err := pool.ExecContext(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	// held takes n connections of the pool at once, so that new ones
	// open, and gives them back when done.
	held := func(n int, each func(c *sql.Conn)) {
		t.Helper()
		var conns []*sql.Conn
		for range n {
			c, err := pool.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			conns = append(conns, c)
		}
		for _, c := range conns {
			each(c)
			c.Close()
		}
	}
	a.openBackup(cn, "shop", true)
	b := a.backup
	b.path = archive
	a.startBackup(b)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.confirm != nil || b.err != "" })
	if a.confirm == nil {
		t.Fatalf("not asked: %q", b.err)
	}
	a.confirm.OnConfirm()
	a.confirm = nil
	testutil.WaitFor(t, tt, "pg_restore", func() bool { _, err := os.Stat(started); return err == nil || b.err != "" })
	held(4, func(*sql.Conn) {})
	if err := os.WriteFile(proceed, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the restore", func() bool { return !b.running && (b.err != "" || b.done) })
	if b.err != "" {
		t.Fatal(b.err)
	}
	held(4, func(c *sql.Conn) {
		var restoring string
		if err := c.QueryRowContext(ctx, "SHOW timescaledb.restoring").Scan(&restoring); err != nil || restoring != "off" {
			t.Errorf("a connection of the pool is restoring: %q %v", restoring, err)
		}
		if _, err := c.ExecContext(ctx, "INSERT INTO it_ts_restore.m VALUES (now(), 1)"); err != nil {
			t.Error(err)
		}
	})
	var outside, rows int
	if err := pool.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM ONLY it_ts_restore.m), (SELECT count(*) FROM it_ts_restore.m)").Scan(&outside, &rows); err != nil ||
		outside != 0 || rows != 4 {
		t.Errorf("%d of %d rows outside the chunks: %v", outside, rows, err)
	}
	out, err := os.ReadFile(seen)
	if err != nil || string(out) != "-c timescaledb.restoring=on\ntimescaledb.restoring=on\n" {
		t.Fatalf("pg_restore saw %q: %v", out, err)
	}
	var settings string
	if err := cn.DB().SQL.QueryRow(`SELECT coalesce((SELECT array_to_string(setconfig, ',') FROM pg_db_role_setting
WHERE setdatabase = (SELECT oid FROM pg_database WHERE datname = 'shop') AND setrole = 0), '')`).Scan(&settings); err != nil || settings != "" {
		t.Fatalf("after the restore the database is set to %q: %v", settings, err)
	}
	events, _ := cn.Project.Audit.Read(0, false)
	if last := events[0]; last.Kind != audit.KindRestore || !strings.Contains(last.Statement, "timescaledb_pre_restore") ||
		!strings.Contains(last.Statement, "timescaledb_post_restore") || last.Error != "" {
		t.Fatalf("audited %+v", last)
	}
}

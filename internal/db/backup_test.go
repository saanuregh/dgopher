package db

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A tool's password goes in its environment, or MySQL's option file,
// never on its command line, which others may read.
func TestPlanBackupKeepsPasswordsOffTheCommandLine(t *testing.T) {
	ctx := context.Background()
	pg := &DB{Config: Config{Engine: Postgres, Host: "db.example.com", Port: 5432, User: "app", Password: "s3cr\"et", Database: "shop", TLS: TLSVerifyFull, CAFile: "/ca.pem"}}
	run, err := PlanBackup(ctx, pg, BackupOptions{Format: BackupArchive, Content: BackupSchemaOnly, Path: "/tmp/shop.dump"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(run.Shown(), "s3cr") || !slices.Contains(run.Env, `PGPASSWORD=s3cr"et`) || !slices.Contains(run.Env, "PGSSLMODE=verify-full") ||
		!slices.Contains(run.Argv, "--schema-only") || !slices.Contains(run.Argv, "custom") {
		t.Fatalf("%+v", run)
	}
	my := &DB{Config: Config{Engine: MySQL, Host: "127.0.0.1", Port: 3306, User: "root", Password: `p"a\ss`, TLS: TLSRequire}}
	if _, err := PlanBackup(ctx, my, BackupOptions{Format: BackupSQL, Path: "/tmp/x.sql"}); err == nil {
		t.Fatal("a MySQL backup without a database")
	}
	run, err = PlanBackup(ctx, my, BackupOptions{Format: BackupSQL, Content: BackupDataOnly, Database: "shop", Path: "/tmp/x.sql"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(run.Shown(), "p\"a") || run.MySQLDefaults != "[client]\npassword=\"p\\\"a\\\\ss\"\n" ||
		!slices.Contains(run.Argv, "--no-create-info") || !slices.Contains(run.Argv, "--ssl-mode=REQUIRED") || run.Argv[len(run.Argv)-1] != "shop" {
		t.Fatalf("%+v", run)
	}
	if _, err := PlanBackup(ctx, &DB{Config: Config{Engine: ClickHouse}}, BackupOptions{Format: BackupSQL, Path: "x"}); err == nil {
		t.Fatal("ClickHouse backed up")
	}
}

// SQLite backs up as a copy of its database, DuckDB as a folder it
// imports again.
func TestNativeBackups(t *testing.T) {
	ctx := context.Background()
	lite := newSQLiteDB(t)
	mustExec(t, lite, `CREATE TABLE t (a INT)`, `INSERT INTO t VALUES (7)`)
	copyPath := filepath.Join(t.TempDir(), "copy.sqlite")
	run, err := PlanBackup(ctx, lite, BackupOptions{Format: BackupCopy, Path: copyPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lite.SQL.ExecContext(ctx, run.Statement); err != nil {
		t.Fatal(run.Statement, err)
	}
	cp, err := Open(ctx, Config{Name: "c", Engine: SQLite, Database: copyPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	var a int
	if err := cp.SQL.QueryRow(`SELECT a FROM t`).Scan(&a); err != nil || a != 7 {
		t.Fatalf("the copy: %d %v", a, err)
	}

	duck, err := Open(ctx, Config{Name: "d", Engine: DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer duck.Close()
	mustExec(t, duck, `CREATE TABLE t AS SELECT 7 AS a`)
	dir := filepath.Join(t.TempDir(), "export")
	run, _ = PlanBackup(ctx, duck, BackupOptions{Format: BackupParquet, Path: dir})
	if _, err := duck.SQL.ExecContext(ctx, run.Statement); err != nil {
		t.Fatal(run.Statement, err)
	}
	other, err := Open(ctx, Config{Name: "o", Engine: DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	run, _ = PlanRestore(ctx, other, "", dir, false)
	if _, err := other.SQL.ExecContext(ctx, run.Statement); err != nil {
		t.Fatal(run.Statement, err)
	}
	if err := other.SQL.QueryRow(`SELECT a FROM t`).Scan(&a); err != nil || a != 7 {
		t.Fatalf("imported: %d %v", a, err)
	}
}

// pg_dump's archive of a database restores, with pg_restore, into
// another; the tool's progress is told line by line.
func TestIntegrationPostgresBackup(t *testing.T) {
	integration(t)
	ctx := context.Background()
	cfg := Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dgopher", Database: "postgres"}
	d, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{"DROP DATABASE IF EXISTS it_backup", "DROP DATABASE IF EXISTS it_restore", "CREATE DATABASE it_backup", "CREATE DATABASE it_restore"} {
		if _, err := d.SQL.ExecContext(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	defer func() {
		d.Close()
		cleanup, _ := Open(ctx, cfg, nil)
		cleanup.SQL.ExecContext(ctx, "DROP DATABASE IF EXISTS it_backup WITH (FORCE)")
		cleanup.SQL.ExecContext(ctx, "DROP DATABASE IF EXISTS it_restore WITH (FORCE)")
		cleanup.Close()
	}()
	src, err := d.Database(ctx, "it_backup")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, src, `CREATE TABLE kept (a int)`, `INSERT INTO kept VALUES (42)`)
	path := filepath.Join(t.TempDir(), "it.dump")
	run, err := PlanBackup(ctx, d, BackupOptions{Format: BackupArchive, Database: "it_backup", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	if err := RunTool(ctx, run, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	if !IsArchive(path) || len(lines) == 0 {
		t.Fatalf("archive %v, progress %q", IsArchive(path), lines)
	}
	run, err = PlanRestore(ctx, d, "it_restore", path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunTool(ctx, run, func(string) {}); err != nil {
		t.Fatal(err)
	}
	dst, err := d.Database(ctx, "it_restore")
	if err != nil {
		t.Fatal(err)
	}
	var a int
	if err := dst.SQL.QueryRowContext(ctx, `SELECT a FROM kept`).Scan(&a); err != nil || a != 42 {
		t.Fatalf("restored: %d %v", a, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

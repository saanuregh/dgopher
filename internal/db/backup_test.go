package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

// fixedRoute is an SSH tunnel's or a proxy's local port.
type fixedRoute int

func (r fixedRoute) LocalPort() int { return int(r) }
func (fixedRoute) Close() error     { return nil }

// A database's name is a value of libpq's connection string, quoted: a bare
// --dbname holding = or starting postgresql:// is read as a connection
// string, which can name another server and send it the password.
func TestPgToolsQuoteDatabase(t *testing.T) {
	ctx := context.Background()
	pg := &DB{Config: Config{Engine: Postgres, Host: "db.example.com", Port: 5432, User: "app", Password: "pw", Database: "shop", TLS: TLSRequire}}
	for name, want := range map[string]string{
		"":                               `dbname='shop'`,
		"x=1 host=127.0.0.1 port=1":      `dbname='x=1 host=127.0.0.1 port=1'`,
		"postgresql://evil.example.com/": `dbname='postgresql://evil.example.com/'`,
		"it's":                           `dbname='it\'s'`,
		`back\slash' host=evil`:          `dbname='back\\slash\' host=evil'`,
	} {
		backup, err := PlanBackup(ctx, pg, BackupOptions{Format: BackupArchive, Database: name, Path: "-x.dump"})
		if err != nil {
			t.Fatal(err)
		}
		restore, err := PlanRestore(ctx, pg, name, "-x.dump", false)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range []ToolRun{backup, restore} {
			if i := slices.Index(run.Argv, "--dbname"); i < 0 || run.Argv[i+1] != want {
				t.Errorf("%q: %q", name, run.Argv)
			}
		}
		// pg_restore reads an archive named as an option as the archive.
		if n := len(restore.Argv); restore.Argv[n-2] != "--" || restore.Argv[n-1] != "-x.dump" {
			t.Errorf("%q: %q", name, restore.Argv)
		}
	}
}

// mysqldump reads the names after -- as names, never as options: a schema
// named -Ar/tmp/x.sql would dump every database to that file.
func TestMysqldumpEndsOptions(t *testing.T) {
	ctx := context.Background()
	my := &DB{Config: Config{Engine: MySQL, Host: "127.0.0.1", Port: 3306, User: "root", Database: "--all-databases", TLS: TLSRequire}}
	for database, want := range map[string]string{"-Ar/tmp/x.sql": "-Ar/tmp/x.sql", "": "--all-databases"} {
		run, err := PlanBackup(ctx, my, BackupOptions{Format: BackupSQL, Database: database, Path: "/tmp/x.sql"})
		if err != nil {
			t.Fatal(err)
		}
		if n := len(run.Argv); !slices.Equal(run.Argv[n-3:], []string{"--databases", "--", want}) {
			t.Errorf("%q: %q", database, run.Argv)
		}
	}
}

// pg_dump and pg_restore log in with the connection's password alone,
// never one ~/.pgpass holds for whatever server a pulled project names.
func TestPgToolsIgnorePgpass(t *testing.T) {
	ctx := context.Background()
	for _, pw := range []string{"", "pw"} {
		pg := &DB{Config: Config{Engine: Postgres, Host: "db.example.com", Port: 5432, User: "app", Password: pw, Database: "shop"}}
		backup, err := PlanBackup(ctx, pg, BackupOptions{Format: BackupSQL, Path: "/tmp/x.sql"})
		if err != nil {
			t.Fatal(err)
		}
		restore, err := PlanRestore(ctx, pg, "", "/tmp/x.dump", false)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range []ToolRun{backup, restore} {
			if !slices.Contains(run.Env, "PGPASSFILE="+os.DevNull) {
				t.Errorf("password %q: %q", pw, run.Env)
			}
		}
	}
}

// fakePgTools puts on the PATH a pg_dump and a pg_restore that say they
// are of a PostgreSQL version.
func fakePgTools(t *testing.T, version string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tools are shell scripts")
	}
	dir := t.TempDir()
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		script := "#!/bin/sh\necho '" + tool + " (PostgreSQL) " + version + "'\n"
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// Under verify-full without a CA file the tools trust the system's
// certificate authorities, as the app does, rather than whatever
// ~/.postgresql/root.crt holds; tools too old for that are refused.
func TestPgToolsSystemRoots(t *testing.T) {
	ctx := context.Background()
	plan := func(cfg Config, route route) (ToolRun, ToolRun, error) {
		d := &DB{Config: cfg, route: route}
		backup, err := PlanBackup(ctx, d, BackupOptions{Format: BackupArchive, Path: "/tmp/x.dump"})
		if err != nil {
			return backup, ToolRun{}, err
		}
		restore, err := PlanRestore(ctx, d, "", "/tmp/x.dump", false)
		return backup, restore, err
	}
	cfg := Config{Engine: Postgres, Host: "db.example.com", Port: 5432, User: "app", Password: "pw", Database: "shop", TLS: TLSVerifyFull}
	fakePgTools(t, "16.4 (Ubuntu 16.4-1)")
	backup, restore, err := plan(cfg, fixedRoute(40000))
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []ToolRun{backup, restore} {
		// Through the tunnel the tool dials its local port, and checks
		// the certificate against the server's own name.
		if !slices.Contains(run.Env, "PGSSLROOTCERT=system") || !slices.Contains(run.Env, "PGSSLMODE=verify-full") ||
			!slices.Contains(run.Env, "PGHOSTADDR=127.0.0.1") || run.Argv[slices.Index(run.Argv, "--host")+1] != "db.example.com" {
			t.Errorf("%q %q", run.Argv, run.Env)
		}
	}
	withCA := cfg
	withCA.CAFile = "/ca.pem"
	backup, restore, err = plan(withCA, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []ToolRun{backup, restore} {
		if !slices.Contains(run.Env, "PGSSLROOTCERT=/ca.pem") || slices.Contains(run.Env, "PGSSLROOTCERT=system") {
			t.Errorf("with a CA file: %q", run.Env)
		}
	}
	require := cfg
	require.TLS = TLSRequire
	backup, _, err = plan(require, nil)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(backup.Env, func(e string) bool { return strings.HasPrefix(e, "PGSSLROOTCERT=") }) {
		t.Errorf("require: %q", backup.Env)
	}
	fakePgTools(t, "15.4")
	if _, _, err := plan(cfg, nil); err == nil || !strings.Contains(err.Error(), "CA file") {
		t.Fatalf("pg_dump 15 under verify-full without a CA file: %v", err)
	}
	if _, _, err := plan(withCA, nil); err != nil {
		t.Fatalf("pg_dump 15 with a CA file: %v", err)
	}
}

// mysqldump checks the server's certificate only against a CA file:
// verify-full without one is refused before it runs. Through a tunnel it
// checks the CA alone, as the name it dials is not the server's.
func TestMysqldumpVerifyFullNeedsCA(t *testing.T) {
	ctx := context.Background()
	cfg := Config{Engine: MySQL, Host: "db.example.com", Port: 3306, User: "root", Password: "pw", Database: "shop", TLS: TLSVerifyFull}
	if _, err := PlanBackup(ctx, &DB{Config: cfg}, BackupOptions{Format: BackupSQL, Path: "/tmp/x.sql"}); err == nil || !strings.Contains(err.Error(), "CA file") {
		t.Fatalf("verify-full without a CA file: %v", err)
	}
	cfg.CAFile = "/ca.pem"
	for _, c := range []struct {
		route route
		mode  string
	}{{nil, "--ssl-mode=VERIFY_IDENTITY"}, {fixedRoute(40000), "--ssl-mode=VERIFY_CA"}} {
		d := &DB{Config: cfg, route: c.route}
		run, err := PlanBackup(ctx, d, BackupOptions{Format: BackupSQL, Path: "/tmp/x.sql"})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(run.Argv, c.mode) || !slices.Contains(run.Argv, "--ssl-ca=/ca.pem") || MysqldumpChecksCAOnly(d) != (c.route != nil) {
			t.Errorf("%q", run.Argv)
		}
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
	if err := restoreFolder(ctx, other, dir); err != nil {
		t.Fatal(err)
	}
	if err := other.SQL.QueryRow(`SELECT a FROM t`).Scan(&a); err != nil || a != 7 {
		t.Fatalf("imported: %d %v", a, err)
	}
}

// restoreFolder restores a DuckDB backup folder into d as the app does.
func restoreFolder(ctx context.Context, d *DB, folder string) error {
	run, err := PlanRestore(ctx, d, "", folder, false)
	if err != nil {
		return err
	}
	return RestoreDuckDB(ctx, d, run)
}

// writeBackupFolder writes a DuckDB backup folder whose schema.sql holds
// the statements given.
func writeBackupFolder(t *testing.T, schema string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.sql"), []byte(schema), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "load.sql"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A DuckDB backup folder from elsewhere restores without reaching any file
// outside it: its schema.sql runs as it is, and COPY … TO or ATTACH there
// would write any file the user can.
func TestDuckDBRestoreSandboxed(t *testing.T) {
	ctx := context.Background()
	outside := t.TempDir()
	csv := filepath.Join(outside, "rows.csv")
	if err := os.WriteFile(csv, []byte("a\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := Open(ctx, Config{Name: "src", Engine: DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	mustExec(t, src,
		`CREATE SEQUENCE seq START 10`,
		`CREATE TYPE mood AS ENUM ('ok', 'sad')`,
		`CREATE TABLE t (id INTEGER PRIMARY KEY DEFAULT nextval('seq'), name VARCHAR, m mood)`,
		`INSERT INTO t (name, m) VALUES ('a', 'ok'), ('b', 'sad')`,
		`CREATE INDEX t_name ON t (name)`,
		`CREATE VIEW v AS SELECT name FROM t`,
		`CREATE MACRO twice(x) AS x * 2`)
	genuine := filepath.Join(t.TempDir(), "it's a backup")
	mustExec(t, src, "EXPORT DATABASE "+Literal(DuckDB, genuine)+" (FORMAT parquet)")
	stages := t.TempDir()
	t.Setenv("TMPDIR", stages) // where the import's database is kept meanwhile

	for name, schema := range map[string]string{
		"COPY TO": "CREATE TABLE kept (a INT);\nCOPY (SELECT 'pwned') TO " + Literal(DuckDB, filepath.Join(outside, "pwned.csv")) + ";\n",
		"ATTACH":  "CREATE TABLE kept (a INT);\nATTACH " + Literal(DuckDB, filepath.Join(outside, "pwned.csv")) + " AS evil;\nCREATE TABLE evil.x (a INT);\n",
		"a view":  "CREATE TABLE kept (a INT);\nCREATE VIEW o AS SELECT * FROM read_csv(" + Literal(DuckDB, csv) + ");\n",
	} {
		user, err := Open(ctx, Config{Name: "user", Engine: DuckDB, Database: ":memory:"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = restoreFolder(ctx, user, writeBackupFolder(t, schema))
		if err == nil || !strings.Contains(err.Error(), "outside its folder") {
			t.Errorf("%s: %v", name, err)
		}
		if name == "a view" && (err == nil || !strings.Contains(err.Error(), "view")) {
			t.Errorf("a view over a file outside: %v", err)
		}
		if _, err := os.Stat(filepath.Join(outside, "pwned.csv")); err == nil {
			t.Fatalf("%s wrote a file outside the folder", name)
		}
		var n int
		if err := user.SQL.QueryRow(`SELECT count(*) FROM duckdb_tables() WHERE table_name = 'kept'`).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s restored part: %d %v", name, n, err)
		}
		user.Close()
	}

	file := filepath.Join(t.TempDir(), "user's.duckdb")
	if err := CreateFile(ctx, DuckDB, file); err != nil {
		t.Fatal(err)
	}
	user, err := Open(ctx, Config{Name: "user", Engine: DuckDB, Database: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer user.Close()
	if err := restoreFolder(ctx, user, genuine); err != nil {
		t.Fatal(err)
	}
	var rows, doubled, indexes int
	var view, mood string
	var next int64
	if err := user.SQL.QueryRow(`SELECT (SELECT count(*) FROM t), (SELECT min(name) FROM v), twice(21), (SELECT max(m)::VARCHAR FROM t),
		(SELECT count(*) FROM duckdb_indexes() WHERE index_name = 't_name'), nextval('seq')`).Scan(&rows, &view, &doubled, &mood, &indexes, &next); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || view != "a" || doubled != 42 || mood != "sad" || indexes != 1 || next != 12 {
		t.Fatalf("restored: %d rows, view %q, macro %d, enum %q, %d indexes, nextval %d", rows, view, doubled, mood, indexes, next)
	}
	if _, err := user.SQL.Exec(`INSERT INTO t (id, name) VALUES (10, 'again')`); err == nil {
		t.Fatal("the primary key was not restored")
	}
	// A transaction no session saw begin is read on the connection itself.
	mustExec(t, user, "BEGIN")
	if err := restoreFolder(ctx, user, genuine); !errors.Is(err, ErrRestoreInTransaction) {
		t.Fatalf("restored inside a transaction: %v", err)
	}
	mustExec(t, user, "ROLLBACK")
	if left, _ := os.ReadDir(stages); len(left) > 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// pg_dump's archive of a database restores, with pg_restore, into
// another; the tool's progress is told line by line. A database named as
// a connection string is only a name to both tools.
func TestIntegrationPostgresBackup(t *testing.T) {
	integration(t)
	ctx := context.Background()
	cfg := Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dgopher", Database: "postgres"}
	d, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	const hostile = "zz_task13_x=1 host=127.0.0.1 port=1"
	quoted := DialectOf(Postgres).Quote(hostile)
	for _, q := range []string{"DROP DATABASE IF EXISTS it_backup", "DROP DATABASE IF EXISTS it_restore", "DROP DATABASE IF EXISTS " + quoted,
		"CREATE DATABASE it_backup", "CREATE DATABASE it_restore", "CREATE DATABASE " + quoted} {
		if _, err := d.SQL.ExecContext(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	defer func() {
		d.Close()
		cleanup, _ := Open(ctx, cfg, nil)
		cleanup.SQL.ExecContext(ctx, "DROP DATABASE IF EXISTS it_backup WITH (FORCE)")
		cleanup.SQL.ExecContext(ctx, "DROP DATABASE IF EXISTS it_restore WITH (FORCE)")
		cleanup.SQL.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)")
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
	// Through the hostile name: restored into it, backed up from it.
	run, err = PlanRestore(ctx, d, hostile, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunTool(ctx, run, func(string) {}); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "hostile.dump")
	run, err = PlanBackup(ctx, d, BackupOptions{Format: BackupArchive, Database: hostile, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := RunTool(ctx, run, func(string) {}); err != nil {
		t.Fatal(err)
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

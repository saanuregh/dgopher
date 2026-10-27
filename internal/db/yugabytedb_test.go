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
	"time"

	"dgopher/internal/lists"
	"dgopher/internal/sqltext"

	"github.com/jackc/pgx/v5/pgconn"
)

// Plans as YugabyteDB 2025.1 printed them for it_plan (20,000 rows of id
// and g, g = i % 1000), by EXPLAIN (ANALYZE, DIST, VERBOSE, FORMAT JSON)
// unless named otherwise.
const (
	// SELECT * FROM it_plan WHERE g = 5: the filter runs in storage,
	// which scans every row.
	yugabyteAnalyzedScan = `[
  {
    "Plan": {
      "Node Type": "Seq Scan",
      "Parallel Aware": false,
      "Async Capable": false,
      "Relation Name": "it_plan",
      "Schema": "public",
      "Alias": "it_plan",
      "Startup Cost": 0.00,
      "Total Cost": 2050.00,
      "Plan Rows": 20000,
      "Plan Width": 8,
      "Actual Startup Time": 6.453,
      "Actual Total Time": 6.458,
      "Actual Rows": 20,
      "Actual Loops": 1,
      "Output": ["id", "g"],
      "Storage Filter": "(it_plan.g = 5)",
      "Storage Table Read Requests": 1,
      "Storage Table Read Execution Time": 6.296,
      "Storage Table Rows Scanned": 20000
    },
    "Query Identifier": -4428919010736307071,
    "Planning Time": 0.699,
    "Triggers": [
    ],
    "Execution Time": 6.497,
    "Storage Read Requests": 1,
    "Storage Read Execution Time": 6.296,
    "Storage Rows Scanned": 20000,
    "Storage Write Requests": 0,
    "Catalog Read Requests": 9,
    "Catalog Read Execution Time": 3.157,
    "Catalog Write Requests": 0,
    "Storage Flush Requests": 0,
    "Storage Execution Time": 9.453,
    "Peak Memory Usage": 24
  }
]`
	// The same, by EXPLAIN (VERBOSE, FORMAT JSON): not run.
	yugabyteExplainedScan = `[
  {
    "Plan": {
      "Node Type": "Seq Scan",
      "Parallel Aware": false,
      "Async Capable": false,
      "Relation Name": "it_plan",
      "Schema": "public",
      "Alias": "it_plan",
      "Startup Cost": 0.00,
      "Total Cost": 2050.00,
      "Plan Rows": 20000,
      "Plan Width": 8,
      "Output": ["id", "g"],
      "Storage Filter": "(it_plan.g = 5)"
    },
    "Query Identifier": -4428919010736307071
  }
]`
	// The same, with an index it_plan_g on g.
	yugabyteAnalyzedIndex = `[
  {
    "Plan": {
      "Node Type": "Index Scan",
      "Parallel Aware": false,
      "Async Capable": false,
      "Scan Direction": "NoMovement",
      "Index Name": "it_plan_g",
      "Relation Name": "it_plan",
      "Schema": "public",
      "Alias": "it_plan",
      "Startup Cost": 0.00,
      "Total Cost": 28.50,
      "Plan Rows": 200,
      "Plan Width": 8,
      "Actual Startup Time": 0.310,
      "Actual Total Time": 0.313,
      "Actual Rows": 20,
      "Actual Loops": 1,
      "Output": ["id", "g"],
      "Index Cond": "(it_plan.g = 5)",
      "Rows Removed by Index Recheck": 0,
      "Storage Table Read Requests": 1,
      "Storage Table Read Execution Time": 0.136,
      "Storage Table Rows Scanned": 20,
      "Storage Index Read Requests": 1,
      "Storage Index Read Execution Time": 0.109,
      "Storage Index Rows Scanned": 20
    },
    "Query Identifier": -4428919010736307071,
    "Planning Time": 1.982,
    "Triggers": [
    ],
    "Execution Time": 0.342,
    "Storage Read Requests": 2,
    "Storage Read Execution Time": 0.245,
    "Storage Rows Scanned": 40,
    "Storage Write Requests": 0,
    "Catalog Read Requests": 8,
    "Catalog Read Execution Time": 2.318,
    "Catalog Write Requests": 0,
    "Storage Flush Requests": 0,
    "Storage Execution Time": 2.562,
    "Peak Memory Usage": 24
  }
]`
)

func TestYugabyteDBExplain(t *testing.T) {
	for analyze, prefix := range map[bool]string{false: "EXPLAIN (VERBOSE, FORMAT JSON) ", true: "EXPLAIN (ANALYZE, DIST, VERBOSE, FORMAT JSON) "} {
		run, ok := Explain(YugabyteDB, "SELECT 1", analyze)
		if !ok || run.Statement != prefix+"SELECT 1" || run.Offset != len(prefix) {
			t.Errorf("analyze %v: %+v %v", analyze, run, ok)
		}
		if a := sqltext.Classify(run.Statement, LexDialect(YugabyteDB)); a.Class != sqltext.Read {
			t.Errorf("%s classified %+v", run.Statement, a)
		}
	}
}

// A filter YugabyteDB runs in storage is the step's detail, as
// PostgreSQL's own filter is.
func TestYugabyteDBParsePlan(t *testing.T) {
	r, _ := SQL(YugabyteDB)
	for text, want := range map[string]struct {
		op, target, detail string
		analyzed           bool
	}{
		yugabyteAnalyzedScan:  {"Seq Scan", "it_plan", "Storage Filter: (it_plan.g = 5)", true},
		yugabyteExplainedScan: {"Seq Scan", "it_plan", "Storage Filter: (it_plan.g = 5)", false},
		yugabyteAnalyzedIndex: {"Index Scan", "it_plan using it_plan_g", "Index Cond: (it_plan.g = 5)", true},
	} {
		p, ok := r.ParsePlan([]string{"QUERY PLAN"}, [][]any{{text}})
		if !ok || p.Root == nil || p.Analyzed != want.analyzed {
			t.Fatalf("plan %+v %v", p, ok)
		}
		if n := p.Root; n.Op != want.op || n.Target != want.target || n.Detail != want.detail {
			t.Errorf("root %q %q %q, want %+v", n.Op, n.Target, n.Detail, want)
		}
	}
}

// A scan whose storage filter keeps few of the rows it reads is advised
// an index on the filter's columns, once it ran; PostgreSQL's advice
// holds for the rest.
func TestYugabyteDBAdvice(t *testing.T) {
	r, _ := SQL(YugabyteDB)
	texts := func(text string) []string {
		p, _ := r.ParsePlan(nil, [][]any{{text}})
		return lists.Map(Advise(YugabyteDB, p), func(a Advice) string { return a.Text })
	}
	if got := texts(yugabyteAnalyzedScan); len(got) != 1 || !strings.Contains(got[0], "Seq Scan reads every row of it_plan") || !strings.Contains(got[0], "an index on (g)") {
		t.Errorf("scan: %q", got)
	}
	for name, text := range map[string]string{"not run": yugabyteExplainedScan, "index": yugabyteAnalyzedIndex} {
		if got := texts(text); len(got) != 0 {
			t.Errorf("%s: %q", name, got)
		}
	}
	// A filter left to the query layer, as PostgreSQL's.
	local := `[{"Plan": {"Node Type": "Seq Scan", "Relation Name": "t", "Plan Rows": 20000, "Actual Rows": 20, "Actual Loops": 1,
  "Filter": "(t.g = 5)", "Rows Removed by Filter": 19980, "Storage Table Rows Scanned": 20000}}]`
	if got := texts(local); len(got) != 1 || !strings.Contains(got[0], "an index on (g)") {
		t.Errorf("local filter: %q", got)
	}
}

// YugabyteDB runs a schema change in a transaction of its own, which
// commits at once and leaves the open transaction open: its schema
// changes are not all or nothing, and commit nothing implicitly.
func TestYugabyteDBTransactions(t *testing.T) {
	if YugabyteDB.TransactionalDDL() || !SchemaChangesCommit(YugabyteDB) {
		t.Error("schema changes are transactional")
	}
	if !YugabyteDB.ServerEnforcesReadOnly() || !YugabyteDB.Savepoints() {
		t.Error("read-only or savepoints lost")
	}
	for _, sql := range []string{"CREATE TABLE t (a INT)", "DROP TABLE t", "ALTER TABLE t ADD b INT", "TRUNCATE t", "INSERT INTO t VALUES (1)"} {
		if CommitsImplicitly(YugabyteDB, sql) {
			t.Errorf("%q commits implicitly", sql)
		}
	}
	r, _ := SQL(YugabyteDB)
	for sql, want := range map[string]bool{
		"CREATE TABLEGROUP g": true, "create  tablegroup g": true, "CREATE DATABASE d": true, "CREATE TABLESPACE s LOCATION '/x'": true,
		"VACUUM t": true, "CREATE INDEX CONCURRENTLY i ON t (a)": true,
		"DROP TABLEGROUP g": false, "ALTER TABLEGROUP g OWNER TO u": false, "CREATE TABLE t (a INT) TABLEGROUP g": false,
		// Inside a transaction YugabyteDB builds an index without online
		// backfill, which writes to the table meanwhile can leave
		// inconsistent.
		"CREATE INDEX i ON t (a)": true, "create unique index i ON t (a)": true, "CREATE INDEX IF NOT EXISTS i ON t (a)": true,
		"CREATE INDEX NONCONCURRENTLY i ON t (a)": false, "CREATE UNIQUE INDEX NONCONCURRENTLY i ON t (a)": false,
		"CREATE TABLE index (a INT)": false, "DROP INDEX i": false, "SELECT 1": false,
	} {
		if got := r.OutsideTransaction(sql); got != want {
			t.Errorf("%q: outside a transaction %v, want %v", sql, got, want)
		}
	}
	pg, _ := SQL(Postgres)
	if pg.OutsideTransaction("CREATE TABLEGROUP g") || pg.OutsideTransaction("CREATE INDEX i ON t (a)") {
		t.Error("PostgreSQL's CREATE TABLEGROUP or CREATE INDEX runs outside a transaction")
	}
}

func TestYugabyteDBActivityPages(t *testing.T) {
	r, _ := SQL(YugabyteDB)
	from := map[string]string{
		"Sessions (this node)": "pg_stat_activity", "Locks": "pg_locks",
		"Statistics (this node)": "pg_stat_statements", "Wait Events (this node)": "yb_active_session_history",
	}
	var names []string
	for _, p := range r.ActivityPages() {
		names = append(names, p.Name)
		if !strings.Contains(p.Query, from[p.Name]) || p.Hint == "" {
			t.Errorf("%s reads %s, hint %q", p.Name, p.Query, p.Hint)
		}
		if p.Kill != nil {
			if got := p.Kill([]any{int64(42)}); got != "SELECT pg_terminate_backend(42)" {
				t.Errorf("%s ends a session with %q", p.Name, got)
			}
			if got := p.Stop([]any{int64(42)}); got != "SELECT pg_cancel_backend(42)" {
				t.Errorf("%s stops a query with %q", p.Name, got)
			}
		}
	}
	if !slices.Equal(names, []string{"Sessions (this node)", "Locks", "Statistics (this node)", "Wait Events (this node)"}) {
		t.Errorf("pages %q", names)
	}
	// A lock held on another node names its transaction, and no session
	// here to stop.
	locks := r.ActivityPages()[1]
	if !strings.Contains(locks.Query, "l.ybdetails->>'transactionid' AS transaction") {
		t.Errorf("Locks shows no transaction: %s", locks.Query)
	}
	if stop, kill := locks.Stop([]any{nil}), locks.Kill([]any{nil}); stop != "" || kill != "" || !strings.Contains(locks.NoSession, "yb_cancel_transaction") {
		t.Errorf("a row without a pid: stop %q, kill %q, said %q", stop, kill, locks.NoSession)
	}
}

func TestYugabyteDBCreateFileNamesYugabyteDB(t *testing.T) {
	r, _ := SQL(YugabyteDB)
	if err := r.CreateFile(context.Background(), "x"); err == nil || !strings.HasPrefix(err.Error(), "YugabyteDB ") {
		t.Fatalf("%v", err)
	}
}

// YugabyteDB has no VACUUM, REINDEX or CLUSTER that does anything: it
// collects garbage itself.
func TestYugabyteDBMaintenance(t *testing.T) {
	r, _ := SQL(YugabyteDB)
	got := lists.Map(r.MaintenanceCommands(`"t"`), func(c MaintenanceCommand) string { return c.SQL })
	if !slices.Equal(got, []string{`ANALYZE "t"`}) {
		t.Errorf("commands %q", got)
	}
}

// A YugabyteDB backup is ysql_dump's where it is installed, else
// pg_dump's; a restore with pg_restore runs statement by statement, as
// YugabyteDB aborts one transaction holding them all.
func TestYugabyteDBBackup(t *testing.T) {
	d := &DB{Config: Config{Name: "y", Engine: YugabyteDB, Host: "127.0.0.1", Port: 5433, User: "yugabyte", Database: "shop"}}
	r, _ := SQL(YugabyteDB)
	ctx := context.Background()
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	run, err := r.PlanBackup(ctx, d, BackupOptions{Format: BackupArchive, Content: BackupAll, Path: "/tmp/x.dump"})
	if err != nil || run.Argv[0] != "pg_dump" || !slices.Contains(run.Argv, "custom") {
		t.Fatalf("without ysql_dump: %q %v", run.Argv, err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ysql_dump"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run, err = r.PlanBackup(ctx, d, BackupOptions{Format: BackupSQL, Content: BackupSchemaOnly, Path: "/tmp/x.sql"})
	if err != nil || run.Argv[0] != "ysql_dump" || !slices.Contains(run.Argv, "plain") || !slices.Contains(run.Argv, "--schema-only") {
		t.Fatalf("with ysql_dump: %q %v", run.Argv, err)
	}
	run, err = r.PlanRestore(ctx, d, "shop", "/tmp/x.dump", false)
	if err != nil || run.Argv[0] != "pg_restore" || slices.Contains(run.Argv, "--single-transaction") || !slices.Contains(run.Argv, "--exit-on-error") {
		t.Fatalf("restore: %q %v", run.Argv, err)
	}
	if r.BackupChoices() != (BackupChoices{Content: true, RestoreKeepsPart: true}) || !strings.Contains(r.BackupNote(false), "ysql_dump") ||
		!strings.Contains(r.BackupNote(true), "pg_restore 16") {
		t.Errorf("choices %+v, notes %q %q", r.BackupChoices(), r.BackupNote(false), r.BackupNote(true))
	}
}

// ysql_dump says its version as YSQL's.
// Under verify-full without a CA file, ysql_dump, of PostgreSQL 15 or
// older, cannot check the certificate against the system's authorities:
// pg_dump backs up instead where it is 16 or later, else the backup is
// refused, naming the CA file. A pg_dump 17 or later archive is asked
// about first, as it restores only into PostgreSQL.
func TestYugabyteDBBackupTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake tools are shell scripts")
	}
	ctx := context.Background()
	install := func(versions map[string]string) {
		t.Helper()
		dir := t.TempDir()
		for tool, version := range versions {
			script := "#!/bin/sh\necho '" + version + "'\n"
			if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", dir)
	}
	r, _ := SQL(YugabyteDB)
	cfg := Config{Name: "y", Engine: YugabyteDB, Host: "yb.example.com", Port: 5433, User: "yugabyte", Database: "shop", TLS: TLSVerifyFull}
	plan := func(cfg Config, format BackupFormat) (ToolRun, error) {
		return r.PlanBackup(ctx, &DB{Config: cfg}, BackupOptions{Format: format, Content: BackupAll, Path: "/tmp/x"})
	}
	ysql := "ysql_dump (YSQL) 15.12-YB-2025.2.8.0-b0"
	for _, c := range []struct {
		name     string
		tools    map[string]string
		cfg      Config
		format   BackupFormat
		tool     string
		warns    bool
		errorHas string
	}{
		{"ysql_dump with a CA file", map[string]string{"ysql_dump": ysql, "pg_dump": "pg_dump (PostgreSQL) 18.6"}, withCAFile(cfg), BackupArchive, "ysql_dump", false, ""},
		{"ysql_dump without verify-full", map[string]string{"ysql_dump": ysql}, withTLS(cfg, TLSRequire), BackupArchive, "ysql_dump", false, ""},
		{"falls back to pg_dump 16", map[string]string{"ysql_dump": ysql, "pg_dump": "pg_dump (PostgreSQL) 16.4"}, cfg, BackupArchive, "pg_dump", false, ""},
		{"falls back to pg_dump 18", map[string]string{"ysql_dump": ysql, "pg_dump": "pg_dump (PostgreSQL) 18.6"}, cfg, BackupArchive, "pg_dump", true, ""},
		{"pg_dump 15 too old", map[string]string{"ysql_dump": ysql, "pg_dump": "pg_dump (PostgreSQL) 15.8"}, cfg, BackupArchive, "", false, "CA file"},
		{"no pg_dump", map[string]string{"ysql_dump": ysql}, cfg, BackupArchive, "", false, "CA file"},
		{"pg_dump 18 alone", map[string]string{"pg_dump": "pg_dump (PostgreSQL) 18.6"}, withTLS(cfg, ""), BackupArchive, "pg_dump", true, ""},
		{"pg_dump 18 writing SQL", map[string]string{"pg_dump": "pg_dump (PostgreSQL) 18.6"}, withTLS(cfg, ""), BackupSQL, "pg_dump", false, ""},
		{"pg_dump 16 alone", map[string]string{"pg_dump": "pg_dump (PostgreSQL) 16.4"}, withTLS(cfg, ""), BackupArchive, "pg_dump", false, ""},
	} {
		install(c.tools)
		run, err := plan(c.cfg, c.format)
		switch {
		case c.errorHas != "":
			if err == nil || !strings.Contains(err.Error(), c.errorHas) || !strings.Contains(err.Error(), "ysql_dump 15") {
				t.Errorf("%s: %q %v", c.name, run.Argv, err)
			}
		case err != nil || run.Argv[0] != c.tool:
			t.Errorf("%s: %q %v", c.name, run.Argv, err)
		case c.warns != strings.Contains(run.Warning, "restores only into PostgreSQL"):
			t.Errorf("%s: warning %q", c.name, run.Warning)
		}
	}
}

func withCAFile(c Config) Config { c.CAFile = "/ca.pem"; return c }

func withTLS(c Config, mode TLSMode) Config { c.TLS = mode; return c }

func TestPgVersionPatternReadsYSQL(t *testing.T) {
	for out, want := range map[string]string{
		"ysql_dump (YSQL) 15.12-YB-2025.2.8.0-b0\n": "15", "pg_dump (PostgreSQL) 18.6\n": "18",
	} {
		if m := pgVersionPattern.FindStringSubmatch(out); m == nil || m[1] != want {
			t.Errorf("%q: %q", out, m)
		}
	}
}

// YugabyteDB's sizes add a table's secondary indexes, which
// pg_total_relation_size leaves out there, and are none for a table that
// has none, as one of a colocated database; PostgreSQL's query is as it
// was.
func TestYugabyteDBObjectsSizes(t *testing.T) {
	done := errors.New("done")
	q := &queryFailing{errs: []error{done, done, done}}
	(yugabytedbDialect{}).Objects(context.Background(), q, "public")
	(postgresDialect{}).Objects(context.Background(), q, "public")
	(yugabytedbDialect{}).ObjectNames(context.Background(), q, "public")
	if yb := q.asked[0]; !strings.Contains(yb, "COALESCE(pg_total_relation_size(c.oid) + (SELECT COALESCE(sum(pg_total_relation_size(i.indexrelid)), 0)") {
		t.Errorf("YugabyteDB's objects:\n%s", yb)
	}
	if pg := q.asked[1]; !strings.Contains(pg, "CASE WHEN c.relkind IN ('r','m','p') THEN pg_total_relation_size(c.oid) ELSE -1 END,\n  COALESCE(obj_description(c.oid, 'pg_class'), ''),") {
		t.Errorf("PostgreSQL's objects:\n%s", pg)
	}
	if names := q.asked[2]; strings.Contains(names, "pg_total_relation_size") || strings.Contains(names, "obj_description") {
		t.Errorf("names alone:\n%s", names)
	}
}

// YugabyteDB's own roles are left out, as PostgreSQL's pg_ roles are.
func TestYugabyteDBListAccounts(t *testing.T) {
	q := &queryFailing{errs: []error{errors.New("done")}}
	r, _ := SQL(YugabyteDB)
	r.ListAccounts(context.Background(), q)
	if !strings.Contains(q.asked[0], "r.rolname !~ '^pg_' AND r.rolname NOT IN ('yb_db_admin', 'yb_extension', 'yb_fdw')") {
		t.Errorf("%s", q.asked[0])
	}
}

// The app's own reads of YugabyteDB's catalog and activity classify as
// reads.
func TestYugabyteDBOwnReadsClassifyAsReads(t *testing.T) {
	r, _ := SQL(YugabyteDB)
	q := &queryFailing{errs: []error{errors.New("done")}}
	(yugabytedbDialect{}).Objects(context.Background(), q, "public")
	reads := q.asked
	for _, page := range r.ActivityPages() {
		reads = append(reads, page.Query)
	}
	for _, sql := range reads {
		if a := sqltext.Classify(sql, LexDialect(YugabyteDB)); a.Class != sqltext.Read || a.Dangerous {
			t.Errorf("%+v:\n%s", a, sql)
		}
	}
}

// A table of a colocated database has no size of its own, and a table
// with a secondary index counts it; both list.
func TestIntegrationYugabyteDBCatalog(t *testing.T) {
	integration(t)
	d := open(t, fixtureOf(t, YugabyteDB))
	ctx := context.Background()
	d.SQL.ExecContext(ctx, "DROP DATABASE IF EXISTS it_colocated")
	mustExec(t, d, "CREATE DATABASE it_colocated WITH COLOCATION = true")
	defer func() {
		d.Close()
		cleanup, _ := Open(ctx, yugabytedbConfig, nil)
		cleanup.SQL.ExecContext(ctx, "DROP DATABASE IF EXISTS it_colocated")
		cleanup.Close()
	}()
	colocated, err := d.Database(ctx, "it_colocated")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, colocated, "CREATE TABLE shared (a int PRIMARY KEY)", "INSERT INTO shared VALUES (1)")
	objs, err := colocated.Dialect.Objects(ctx, colocated.SQL, "public")
	if err != nil || len(objs) != 1 || objs[0].Name != "shared" || objs[0].Bytes != -1 {
		t.Fatalf("colocated objects %+v: %v", objs, err)
	}
	objs, err = d.Dialect.Objects(ctx, d.SQL, "it")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(objs, func(o Object) bool { return o.Name == "orders" })
	if i < 0 || objs[i].Bytes < 0 {
		t.Fatalf("orders sized %+v", objs)
	}
}

// Locks shows a session waiting for a row another holds, and whom it
// waits for; End Session ends the holder's session, and Cancel Query a
// running statement. Every other page reads.
func TestIntegrationYugabyteDBActivity(t *testing.T) {
	integration(t)
	d := open(t, fixtureOf(t, YugabyteDB))
	ctx := context.Background()
	r, _ := SQL(YugabyteDB)
	holder, _ := d.Session(ctx)
	waiter, _ := d.Session(ctx)
	defer waiter.Close()
	defer holder.Close()
	for _, st := range []struct {
		s   *Session
		sql string
	}{{holder, "BEGIN"}, {holder, "UPDATE it.customers SET name = 'held' WHERE id = 1"}, {waiter, "BEGIN"}} {
		if _, err := st.s.Exec(ctx, st.sql); err != nil {
			t.Fatalf("%s: %v", st.sql, err)
		}
	}
	pidOf := func(s *Session) string {
		c, err := s.Query(ctx, "SELECT pg_backend_pid()")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		rows, _ := c.Fetch(1)
		return Display(rows[0][0])
	}
	held, waits := pidOf(holder), pidOf(waiter)
	waited := make(chan error, 1)
	go func() {
		_, err := waiter.Exec(ctx, "UPDATE it.customers SET name = 'waits' WHERE id = 1")
		waited <- err
	}()
	read := func(page ActivityPage) [][]any {
		rows, err := d.SQL.QueryContext(ctx, page.Query)
		if err != nil {
			t.Fatalf("%s: %v", page.Name, err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var got [][]any
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			got = append(got, vals)
		}
		return got
	}
	pages := map[string]ActivityPage{}
	for _, page := range r.ActivityPages() {
		pages[page.Name] = page
		// Before 2025.1 the server samples no waits unless told to.
		if page.Name == "Wait Events (this node)" {
			var pg *pgconn.PgError
			if rows, err := d.SQL.QueryContext(ctx, page.Query); err == nil {
				rows.Close()
			} else if !errors.As(err, &pg) || pg.Code != "55000" {
				t.Fatalf("%s: %v", page.Name, err)
			}
			continue
		}
		read(page)
	}
	waiting := func() []any {
		for _, row := range read(pages["Locks"]) {
			if Display(row[0]) == waits && Display(row[7]) == held {
				return row
			}
		}
		return nil
	}
	// pg_locks lists a transaction's locks once it is a second old
	// (yb_locks_min_txn_age).
	row := waiting()
	for deadline := time.Now().Add(10 * time.Second); row == nil && time.Now().Before(deadline); row = waiting() {
		time.Sleep(200 * time.Millisecond)
	}
	if row == nil {
		t.Fatalf("no wait of %s for %s among %v", waits, held, read(pages["Locks"]))
	}
	if !slices.ContainsFunc(read(pages["Sessions (this node)"]), func(r []any) bool { return Display(r[0]) == held }) {
		t.Errorf("the holder %s is not among the sessions", held)
	}
	// YugabyteDB stops a statement waiting for a lock only once the wait
	// ends: ending the holder's session ends it.
	if _, err := d.SQL.ExecContext(ctx, pages["Sessions (this node)"].Kill([]any{held})); err != nil {
		t.Fatal(err)
	}
	if err := <-waited; err != nil {
		t.Fatalf("the waiting statement after the holder's End Session: %v", err)
	}
	if err := holder.Commit(ctx); !errors.Is(err, ErrTxLost) {
		t.Fatalf("the holder's commit after End Session: %v", err)
	}
	waiter.Rollback(ctx)
	sleeping := make(chan error, 1)
	go func() {
		_, err := waiter.Exec(ctx, "SELECT pg_sleep(10)")
		sleeping <- err
	}()
	time.Sleep(500 * time.Millisecond)
	if _, err := d.SQL.ExecContext(ctx, pages["Sessions (this node)"].Stop([]any{waits})); err != nil {
		t.Fatal(err)
	}
	if err := <-sleeping; err == nil || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("the sleeping statement after Cancel Query: %v", err)
	}
}

// A Repeatable Read transaction that writes a row another committed
// since it began fails with SQLSTATE 40001, described as one to roll back
// and run again. (From 2025.2 the default is Read Committed, which reads
// the row again instead.)
func TestIntegrationYugabyteDBConflictHint(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, YugabyteDB))
	a, _ := d.Session(ctx)
	defer a.Close()
	b, _ := d.Session(ctx)
	defer b.Close()
	var failed error
	for _, st := range []struct {
		s   *Session
		sql string
	}{
		{a, "BEGIN ISOLATION LEVEL REPEATABLE READ"}, {a, "SELECT name FROM it.customers WHERE id = 2"},
		{b, "UPDATE it.customers SET name = 'b' WHERE id = 2"},
		{a, "UPDATE it.customers SET name = 'a' WHERE id = 2"},
	} {
		if _, err := st.s.Exec(ctx, st.sql); err != nil {
			failed = err
			break
		}
	}
	a.Rollback(ctx)
	var pg *pgconn.PgError
	if !errors.As(failed, &pg) || pg.Code != "40001" {
		t.Fatalf("not a serialization failure: %v", failed)
	}
	if info := DescribeError(failed); !strings.HasPrefix(info.Hint, "The transaction could not be serialized with others and was aborted") {
		t.Fatalf("described %+v", info)
	}
}

// pg_dump, where ysql_dump is not installed, backs up a YugabyteDB
// database as an archive.
func TestIntegrationYugabyteDBBackup(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, YugabyteDB))
	r, _ := SQL(YugabyteDB)
	path := filepath.Join(t.TempDir(), "it.dump")
	run, err := r.PlanBackup(ctx, d, BackupOptions{Format: BackupArchive, Content: BackupAll, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := RunTool(ctx, run, func(string) {}); err != nil {
		t.Fatalf("%q: %v", run.Argv, err)
	}
	if !IsArchive(path) {
		t.Fatalf("%s wrote no archive", run.Argv[0])
	}
}

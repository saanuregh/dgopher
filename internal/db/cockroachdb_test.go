package db

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/lists"
	"dgopher/internal/sqltext"

	"github.com/jackc/pgx/v5/pgconn"
)

// Plans as CockroachDB 26.3 printed them, a row a line, for queries on
// it_plan (20,000 rows of id and g) and it_p2 (3,000 rows, by id, with an
// index by_g on g).
var (
	// EXPLAIN (VERBOSE) SELECT * FROM it_plan WHERE g = 5
	cockroachExplainedScan = []string{
		"distribution: local",
		"vectorized: true",
		"",
		"• filter",
		"│ columns: (id, g)",
		"│ estimated row count: 20",
		"│ filter: g = 5",
		"│",
		"└── • scan",
		"      columns: (id, g)",
		"      estimated row count: 20,000 (100% of the table; stats collected 0 seconds ago)",
		"      table: it_plan@it_plan_pkey",
		"      spans: FULL SCAN",
		"",
		"index recommendations: 1",
		"1. type: index creation",
		"   SQL command: CREATE INDEX ON shop.public.it_plan (g) STORING (id);",
	}
	// EXPLAIN ANALYZE (VERBOSE) SELECT p.g, count(*) FROM it_plan p JOIN it_p2 q ON q.g = p.g WHERE q.id > 10
	// GROUP BY p.g ORDER BY 2 DESC LIMIT 3
	cockroachAnalyzedJoin = []string{
		"planning time: 647µs",
		"execution time: 7ms",
		"distribution: local",
		"vectorized: true",
		"plan type: generic, re-optimized",
		"rows decoded from KV: 22,990 (947 KiB, 22,990 KVs, 2 gRPC calls)",
		"cumulative time spent in KV: 5ms",
		"maximum memory usage: 2.2 MiB",
		"DistSQL network usage: 0 B (0 messages)",
		"kv cpu time: 2ms",
		"sql cpu time: 5ms",
		"isolation level: serializable",
		"priority: normal",
		"quality of service: regular",
		"",
		"• top-k",
		"│ columns: (g, count)",
		"│ sql nodes: n1",
		"│ vectorized batch count: 1",
		"│ execution time: 11µs",
		"│ estimated max memory allocated: 10 KiB",
		"│ sql cpu time: 11µs",
		"│ actual row count: 3",
		"│ estimated row count: 3",
		"│ order: -count_rows",
		"│ k: 3",
		"│",
		"└── • group (hash)",
		"    │ columns: (g, count_rows)",
		"    │ sql nodes: n1",
		"    │ vectorized batch count: 1",
		"    │ execution time: 1ms",
		"    │ estimated max memory allocated: 1.1 MiB",
		"    │ estimated max sql temp disk usage: 0 B",
		"    │ sql cpu time: 1ms",
		"    │ actual row count: 100",
		"    │ estimated row count: 100",
		"    │ aggregate 0: count_rows()",
		"    │ group by: g",
		"    │",
		"    └── • project",
		"        │ columns: (g)",
		"        │",
		"        └── • hash join (inner)",
		"            │ columns: (g, id, g)",
		"            │ sql nodes: n1",
		"            │ vectorized batch count: 63",
		"            │ execution time: 363µs",
		"            │ estimated max memory allocated: 150 KiB",
		"            │ sql cpu time: 362µs",
		"            │ actual row count: 59,800",
		"            │ estimated row count: 59,800",
		"            │ equality: (g) = (g)",
		"            │",
		"            ├── • scan",
		"            │     columns: (g)",
		"            │     sql nodes: n1",
		"            │     kv nodes: n1",
		"            │     vectorized batch count: 20",
		"            │     KV time: 4ms",
		"            │     KV rows decoded: 20,000",
		"            │     KV pairs read: 20,000",
		"            │     KV bytes read: 850 KiB",
		"            │     KV gRPC calls: 1",
		"            │     estimated max memory allocated: 880 KiB",
		"            │     sql cpu time: 2ms",
		"            │     MVCC step count (ext/int): 20,000/20,000",
		"            │     MVCC seek count (ext/int): 1/1",
		"            │     actual row count: 20,000",
		"            │     estimated row count: 20,000 (100% of the table; stats collected 0 seconds ago)",
		"            │     table: it_plan@it_plan_pkey",
		"            │     spans: FULL SCAN",
		"            │",
		"            └── • scan",
		"                  columns: (id, g)",
		"                  sql nodes: n1",
		"                  kv nodes: n1",
		"                  vectorized batch count: 3",
		"                  KV time: 927µs",
		"                  KV rows decoded: 2,990",
		"                  KV pairs read: 2,990",
		"                  KV bytes read: 97 KiB",
		"                  KV gRPC calls: 1",
		"                  estimated max memory allocated: 130 KiB",
		"                  sql cpu time: 368µs",
		"                  MVCC step count (ext/int): 2,990/2,990",
		"                  MVCC seek count (ext/int): 1/1",
		"                  actual row count: 2,990",
		"                  estimated row count: 2,990 (100% of the table; stats collected 0 seconds ago)",
		"                  table: it_p2@it_p2_pkey",
		"                  spans: /11-",
	}
	// EXPLAIN (VERBOSE) SELECT * FROM it_p2 WHERE g = 5
	cockroachExplainedIndex = []string{
		"distribution: local",
		"vectorized: true",
		"",
		"• scan",
		"  columns: (id, g)",
		"  estimated row count: 30 (1.0% of the table; stats collected 0 seconds ago)",
		"  table: it_p2@by_g",
		"  spans: /5-/6",
	}
	// EXPLAIN (OPT) SELECT * FROM it_p2 WHERE g = 5, which is no tree of steps
	cockroachOptimizerPlan = []string{
		"scan it_p2@by_g",
		" └── constraint: /2/1: [/5 - /5]",
	}
)

// planRows are a plan's lines as the rows of its one column.
func planRows(lines []string) [][]any {
	return lists.Map(lines, func(l string) []any { return []any{l} })
}

func TestCockroachDBParsePlan(t *testing.T) {
	r, _ := SQL(CockroachDB)
	parse := func(lines []string) *Plan {
		t.Helper()
		p, ok := r.ParsePlan([]string{"info"}, planRows(lines))
		if !ok || p.Root == nil {
			t.Fatalf("not read as a plan: %q", lines[:3])
		}
		return p
	}

	p := parse(cockroachExplainedScan)
	root := p.Root
	if p.Analyzed || root.Op != "filter" || root.Detail != "g = 5" || root.Rows != 20 || root.ActualRows != -1 || root.Time != -1 || len(root.Children) != 1 {
		t.Fatalf("root %+v", root)
	}
	if root.Prop("columns") != "(id, g)" || root.Prop("distribution") != "local" ||
		root.Prop("index recommendation") != "CREATE INDEX ON shop.public.it_plan (g) STORING (id);" {
		t.Fatalf("root props %q", root.Props)
	}
	if scan := root.Children[0]; scan.Op != "scan" || scan.Target != "it_plan using it_plan_pkey" || scan.Detail != "FULL SCAN" ||
		scan.Rows != 20000 || len(scan.Children) != 0 || scan.Prop("columns") != "(id, g)" {
		t.Fatalf("scan %+v", scan)
	}

	p = parse(cockroachAnalyzedJoin)
	root = p.Root
	if !p.Analyzed || root.Op != "top-k" || root.Detail != "-count_rows" || root.Rows != 3 || root.ActualRows != 3 || !near(root.Time, 0.011) {
		t.Fatalf("analyzed root %+v", root)
	}
	if root.Prop("planning time") != "647µs" || root.Prop("execution time") != "11µs" {
		t.Fatalf("the root's own time first, then the plan's: %q", root.Props)
	}
	group := root.Children[0]
	if group.Op != "group (hash)" || group.Detail != "g" || group.ActualRows != 100 || !near(group.Time, 1) || len(group.Children) != 1 {
		t.Fatalf("group %+v", group)
	}
	join := group.Children[0].Children[0]
	if join.Op != "hash join (inner)" || join.Detail != "(g) = (g)" || join.ActualRows != 59800 || len(join.Children) != 2 {
		t.Fatalf("join %+v", join)
	}
	left, right := join.Children[0], join.Children[1]
	if left.Target != "it_plan using it_plan_pkey" || left.ActualRows != 20000 || left.Time != -1 || left.Prop("KV time") != "4ms" ||
		right.Target != "it_p2 using it_p2_pkey" || right.Detail != "/11-" || right.ActualRows != 2990 {
		t.Fatalf("scans %+v, %+v", left, right)
	}

	p = parse(cockroachExplainedIndex)
	if p.Root.Op != "scan" || p.Root.Target != "it_p2 using by_g" || p.Root.Rows != 30 || len(p.Root.Children) != 0 {
		t.Fatalf("index scan %+v", p.Root)
	}

	for _, lines := range [][]string{cockroachOptimizerPlan, {""}, {"distribution: local"}, {`[{"Plan": {}}]`}} {
		if _, ok := r.ParsePlan([]string{"info"}, planRows(lines)); ok {
			t.Errorf("%q read as a plan", lines)
		}
	}
}

func TestCockroachDBExplain(t *testing.T) {
	for analyze, prefix := range map[bool]string{false: "EXPLAIN (VERBOSE) ", true: "EXPLAIN ANALYZE (VERBOSE) "} {
		run, ok := Explain(CockroachDB, "SELECT 1", analyze)
		if !ok || run.Statement != prefix+"SELECT 1" || run.Offset != len(prefix) {
			t.Errorf("analyze %v: %+v %v", analyze, run, ok)
		}
		// Both read as reads: Explain Analyze runs only what reads.
		if a := sqltext.Classify(run.Statement, LexDialect(CockroachDB)); a.Class != sqltext.Read {
			t.Errorf("%s classified %+v", run.Statement, a)
		}
	}
}

func TestCockroachDBAdvice(t *testing.T) {
	r, _ := SQL(CockroachDB)
	texts := func(lines []string) string {
		p, _ := r.ParsePlan(nil, planRows(lines))
		return strings.Join(lists.Map(Advise(CockroachDB, p), func(a Advice) string { return a.Text }), "\n")
	}
	got := texts(cockroachExplainedScan)
	for _, want := range []string{"full scan reads every row of it_plan:", "recommends an index: CREATE INDEX ON shop.public.it_plan (g) STORING (id);"} {
		if !strings.Contains(got, want) {
			t.Errorf("scan: %q, want %q", got, want)
		}
	}
	if got := texts(cockroachExplainedIndex); got != "" {
		t.Errorf("index scan: %q", got)
	}
}

// CockroachDB commits the open transaction before a schema change
// (autocommit_before_ddl), so its schema changes are not all or nothing,
// and refuses some statements in a transaction.
func TestCockroachDBTransactions(t *testing.T) {
	if CockroachDB.TransactionalDDL() || !SchemaChangesCommit(CockroachDB) {
		t.Error("schema changes are transactional")
	}
	for sql, want := range map[string]bool{
		"CREATE TABLE t (a INT)": true, "create temp table t (a int)": true, "ALTER TABLE t ADD COLUMN b INT": true,
		"DROP TABLE t": true, "TRUNCATE t": true, "GRANT SELECT ON t TO u": true, "REVOKE SELECT ON t FROM u": true,
		"COMMENT ON TABLE t IS 'x'": true, "ANALYZE t": true, "CREATE STATISTICS s FROM t": true, "REFRESH MATERIALIZED VIEW v": true,
		"REASSIGN OWNED BY u TO v": true, "/* c */ CREATE INDEX ON t (a)": true,
		"INSERT INTO t VALUES (1)": false, "SELECT 1": false, "SET search_path TO x": false, "BEGIN": false, "SAVEPOINT s": false,
	} {
		if got := CommitsImplicitly(CockroachDB, sql); got != want {
			t.Errorf("%q: commits implicitly %v, want %v", sql, got, want)
		}
		if CommitsImplicitly(Postgres, sql) {
			t.Errorf("PostgreSQL %q commits implicitly", sql)
		}
	}
	r, _ := SQL(CockroachDB)
	for sql, want := range map[string]bool{
		"SET CLUSTER SETTING sql.defaults.distsql = off": true, "RESET CLUSTER SETTING x": true,
		"BACKUP INTO 'nodelocal://1/b'": true, "RESTORE FROM LATEST IN 'nodelocal://1/b'": true,
		"IMPORT INTO t CSV DATA ('nodelocal://1/t.csv')": true, "EXPORT INTO CSV 'nodelocal://1/e' FROM SELECT 1": true,
		"CREATE CHANGEFEED FOR t INTO 'kafka://k'": true,
		"VACUUM t": false, "CREATE DATABASE d": false, "SET search_path TO x": false, "CREATE TABLE t (a INT)": false,
	} {
		if got := r.OutsideTransaction(sql); got != want {
			t.Errorf("%q: outside a transaction %v, want %v", sql, got, want)
		}
	}
}

func TestCockroachDBActivityPages(t *testing.T) {
	r, _ := SQL(CockroachDB)
	from := map[string]string{"Sessions": "cluster_sessions", "Locks": "cluster_locks", "Statistics": "statement_statistics", "Jobs": "[SHOW JOBS]"}
	var names []string
	for _, p := range r.ActivityPages() {
		names = append(names, p.Name)
		if !strings.Contains(p.Query, from[p.Name]) || strings.Contains(p.Query, "pg_stat") || p.Hint == "" {
			t.Errorf("%s reads %s, hint %q", p.Name, p.Query, p.Hint)
		}
		if p.Kill != nil {
			id := []any{"17a8'e3"}
			if got := p.Kill(id); got != "CANCEL SESSION '17a8''e3'" {
				t.Errorf("%s ends a session with %q", p.Name, got)
			}
			if got := p.Stop(id); !strings.HasPrefix(got, "CANCEL QUERIES (") || !strings.HasSuffix(got, "session_id = '17a8''e3')") {
				t.Errorf("%s stops a query with %q", p.Name, got)
			}
		}
	}
	if !slices.Equal(names, []string{"Sessions", "Locks", "Statistics", "Jobs"}) {
		t.Errorf("pages %q", names)
	}
}

func TestCockroachDBCreateFileNamesCockroachDB(t *testing.T) {
	r, _ := SQL(CockroachDB)
	if err := r.CreateFile(context.Background(), "x"); err == nil || !strings.HasPrefix(err.Error(), "CockroachDB ") {
		t.Fatalf("%v", err)
	}
}

// A CockroachDB backup is pg_dump's schema, in CockroachDB's pg_dump
// compatibility, which it sets only for a client named pg_dump; it
// restores with Run SQL File.
func TestCockroachDBBackup(t *testing.T) {
	d := &DB{Config: Config{Name: "c", Engine: CockroachDB, Host: "127.0.0.1", Port: 26257, User: "root", Database: "shop"}}
	r, _ := SQL(CockroachDB)
	run, err := r.PlanBackup(context.Background(), d, BackupOptions{Format: BackupSQL, Content: BackupAll, Path: "/tmp/x.sql"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(run.Argv, "--schema-only") || !slices.Contains(run.Argv, "plain") || run.Argv[0] != "pg_dump" {
		t.Errorf("argv %q", run.Argv)
	}
	if !slices.Contains(run.Env, "PGOPTIONS=-c pg_dump_compatibility=cockroachdb") || !slices.Contains(run.Env, "PGAPPNAME="+appName) {
		t.Errorf("env %q", run.Env)
	}
	if _, err := r.PlanBackup(context.Background(), d, BackupOptions{Format: BackupArchive, Path: "/tmp/x.dump"}); err == nil {
		t.Error("an archive planned")
	}
	if _, err := r.PlanRestore(context.Background(), d, "shop", "/tmp/x.sql", false); err == nil || !strings.Contains(err.Error(), "Run SQL File") {
		t.Errorf("restore: %v", err)
	}
	if r.BackupChoices() != (BackupChoices{}) || !strings.Contains(r.BackupNote(false), "26.3") {
		t.Errorf("choices %+v, note %q", r.BackupChoices(), r.BackupNote(false))
	}
}

// CockroachDB has no VACUUM or REINDEX: it collects garbage itself.
func TestCockroachDBMaintenance(t *testing.T) {
	r, _ := SQL(CockroachDB)
	got := lists.Map(r.MaintenanceCommands(`"t"`), func(c MaintenanceCommand) string { return c.SQL })
	if !slices.Equal(got, []string{`ANALYZE "t"`}) {
		t.Errorf("commands %q", got)
	}
}

// Two transactions that read what the other then writes cannot both
// commit under SERIALIZABLE: CockroachDB fails one with SQLSTATE 40001,
// which is described as one to roll back and run again.
func TestIntegrationCockroachDBConflictHint(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, CockroachDB))
	a, _ := d.Session(ctx)
	defer a.Close()
	b, _ := d.Session(ctx)
	defer b.Close()
	var failed error
	for _, st := range []struct {
		s   *Session
		sql string
	}{
		{a, "BEGIN"}, {a, "SELECT name FROM it.customers WHERE id = 1"},
		{b, "BEGIN"}, {b, "SELECT name FROM it.customers WHERE id = 2"},
		{a, "UPDATE it.customers SET name = 'a' WHERE id = 2"},
		{b, "UPDATE it.customers SET name = 'b' WHERE id = 1"},
		{a, "COMMIT"}, {b, "COMMIT"},
	} {
		if _, err := st.s.Exec(ctx, st.sql); err != nil {
			if failed != nil {
				t.Fatalf("%s: a second failure: %v", st.sql, err)
			}
			failed = err
		}
	}
	var pg *pgconn.PgError
	if !errors.As(failed, &pg) || pg.Code != "40001" {
		t.Fatalf("not a serialization failure: %v", failed)
	}
	if info := DescribeError(failed); !strings.HasPrefix(info.Hint, "The transaction could not be serialized with others and was aborted") {
		t.Fatalf("described %+v", info)
	}
}

// CockroachDB refuses a table without a primary key between statements:
// a new primary key replaces the old in one statement.
func TestCockroachDBPrimaryKeyChangesInOneStatement(t *testing.T) {
	was := TableDesign{Schema: "public", Name: "t", PrimaryKeyName: "t_pkey", Columns: []ColumnDesign{
		{Name: "id", Was: "id", Type: "INT8", PrimaryKey: true}, {Name: "qty", Was: "qty", Type: "INT8"}}}
	now := clone(was)
	now.Columns[0].PrimaryKey, now.Columns[1].PrimaryKey = false, true
	ch, err := AlterTableChange(cockroachdbDialect{}, was, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`ALTER TABLE "public"."t" DROP CONSTRAINT "t_pkey", ADD PRIMARY KEY ("qty")`}
	if got := lists.Map(ch.Steps, func(s Step) string { return s.SQL }); !slices.Equal(got, want) || ch.Atomic {
		t.Errorf("steps %q (atomic %v), want %q", got, ch.Atomic, want)
	}
	ch, _ = AlterTableChange(postgresDialect{}, was, now)
	if got := lists.Map(ch.Steps, func(s Step) string { return s.SQL }); len(got) != 2 || !ch.Atomic {
		t.Errorf("PostgreSQL's steps %q (atomic %v)", got, ch.Atomic)
	}
}

// The new key is made before the old key's columns are dropped, from
// columns made and changed first, which a primary key's must be; the
// table cannot lose its primary key. PostgreSQL's order is kept.
func TestCockroachDBPrimaryKeyMoves(t *testing.T) {
	was := TableDesign{Schema: "public", Name: "t", PrimaryKeyName: "t_pkey", Columns: []ColumnDesign{
		{Name: "id", Was: "id", Type: "INT8", PrimaryKey: true}, {Name: "b", Was: "b", Type: "INT8", Nullable: true}}}
	now := clone(was)
	now.Columns = []ColumnDesign{{Name: "c", Was: "b", Type: "INT8", PrimaryKey: true},
		{Name: "k", Type: "INT8", PrimaryKey: true}, {Name: "note", Type: "STRING", Nullable: true}}
	steps := func(d Dialect) []string {
		t.Helper()
		ch, err := AlterTableChange(d, was, now)
		if err != nil {
			t.Fatal(err)
		}
		return lists.Map(ch.Steps, func(s Step) string { return s.SQL })
	}
	want := []string{
		`ALTER TABLE "public"."t" RENAME COLUMN "b" TO "c"`,
		`ALTER TABLE "public"."t" ALTER COLUMN "c" SET NOT NULL`,
		`ALTER TABLE "public"."t" ADD COLUMN "k" INT8 NOT NULL`,
		`ALTER TABLE "public"."t" DROP CONSTRAINT "t_pkey", ADD PRIMARY KEY ("c", "k")`,
		`ALTER TABLE "public"."t" DROP COLUMN "id"`,
		`ALTER TABLE "public"."t" ADD COLUMN "note" STRING`,
	}
	if got := steps(cockroachdbDialect{}); !slices.Equal(got, want) {
		t.Errorf("steps\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	pgWant := []string{
		`ALTER TABLE "public"."t" DROP CONSTRAINT "t_pkey"`,
		`ALTER TABLE "public"."t" RENAME COLUMN "b" TO "c"`,
		`ALTER TABLE "public"."t" DROP COLUMN "id"`,
		`ALTER TABLE "public"."t" ALTER COLUMN "c" SET NOT NULL`,
		`ALTER TABLE "public"."t" ADD COLUMN "k" INT8 NOT NULL`,
		`ALTER TABLE "public"."t" ADD COLUMN "note" STRING`,
		`ALTER TABLE "public"."t" ADD PRIMARY KEY ("c", "k")`,
	}
	if got := steps(postgresDialect{}); !slices.Equal(got, pgWant) {
		t.Errorf("PostgreSQL's steps\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(pgWant, "\n"))
	}
	now = clone(was)
	now.Columns[0].PrimaryKey = false
	if _, err := AlterTableChange(cockroachdbDialect{}, was, now); err == nil || !strings.Contains(err.Error(), "primary key") {
		t.Errorf("removing the primary key: %v", err)
	}
	if _, err := AlterTableChange(postgresDialect{}, was, now); err != nil {
		t.Errorf("PostgreSQL removing the primary key: %v", err)
	}
}

// Through the designer, a primary key moves to another column while the
// old key's column is dropped: the server refuses to drop a column of the
// key, so the key is replaced first.
func TestIntegrationCockroachDBPrimaryKeyMoves(t *testing.T) {
	integration(t)
	d := open(t, fixtureOf(t, CockroachDB))
	mustExec(t, d, "DROP TABLE IF EXISTS it.pk_moves", "CREATE TABLE it.pk_moves (id INT8 PRIMARY KEY, b INT8 NOT NULL)", "INSERT INTO it.pk_moves VALUES (1, 10)")
	defer mustExec(t, d, "DROP TABLE IF EXISTS it.pk_moves")
	was := TableDesign{Schema: "it", Name: "pk_moves", PrimaryKeyName: "pk_moves_pkey", Columns: []ColumnDesign{
		{Name: "id", Was: "id", Type: "INT8", PrimaryKey: true}, {Name: "b", Was: "b", Type: "INT8"}}}
	now := clone(was)
	now.Columns = []ColumnDesign{{Name: "b", Was: "b", Type: "INT8", PrimaryKey: true}}
	ch, err := AlterTableChange(d.Dialect, was, now)
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := apply(t, d, ch); err != nil {
		t.Fatalf("%v:\n%s", err, strings.Join(ran, "\n"))
	}
	var ddl string
	if err := d.SQL.QueryRow("SELECT create_statement FROM [SHOW CREATE TABLE it.pk_moves]").Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "PRIMARY KEY (b ASC)") || strings.Contains(ddl, "id INT8") {
		t.Errorf("after the change:\n%s", ddl)
	}
}

// Each activity page reads on the server; Locks shows a session waiting
// for a row another holds, and whom it waits for; Cancel Query stops the
// waiting statement, End Session ends the holder's session.
func TestIntegrationCockroachDBActivity(t *testing.T) {
	integration(t)
	d := open(t, fixtureOf(t, CockroachDB))
	ctx := context.Background()
	r, _ := SQL(CockroachDB)
	holder, _ := d.Session(ctx)
	waiter, _ := d.Session(ctx)
	// The holder closes first, which ends its transaction, so that the
	// waiter's statement ends and the waiter can close.
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
	sessionOf := func(s *Session) string {
		c, err := s.Query(ctx, "SHOW session_id")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		rows, _ := c.Fetch(1)
		return Display(rows[0][0])
	}
	held, waits := sessionOf(holder), sessionOf(waiter)
	waited := make(chan error, 1)
	go func() {
		_, err := waiter.Exec(ctx, "UPDATE it.customers SET name = 'waits' WHERE id = 1")
		waited <- err
	}()
	// read lists a page's rows, a line each: its first column, and the
	// lock's state and whom it waits for, where the page has them.
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
		read(page)
	}
	waiting := func() []any {
		for _, row := range read(pages["Locks"]) {
			if Display(row[0]) == waits && Display(row[6]) == held {
				return row
			}
		}
		return nil
	}
	row := waiting()
	for deadline := time.Now().Add(4 * time.Second); row == nil && time.Now().Before(deadline); row = waiting() {
		time.Sleep(100 * time.Millisecond)
	}
	if row == nil {
		t.Fatalf("no wait of %s for %s among %v", waits, held, read(pages["Locks"]))
	}
	if !slices.ContainsFunc(read(pages["Sessions"]), func(r []any) bool { return Display(r[0]) == held }) {
		t.Errorf("the holder %s is not among the sessions", held)
	}
	if _, err := d.SQL.ExecContext(ctx, pages["Locks"].Stop(row)); err != nil {
		t.Fatal(err)
	}
	if err := <-waited; err == nil || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("the waiting statement after Cancel Query: %v", err)
	}
	if _, err := d.SQL.ExecContext(ctx, pages["Sessions"].Kill([]any{held})); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); !errors.Is(err, ErrTxLost) {
		t.Fatalf("the holder's commit after End Session: %v", err)
	}
}

// The catalog reads CockroachDB's own: estimated rows, sizes where the
// user may read them, tables' and views' CREATE, and no hidden column.
func TestIntegrationCockroachDBCatalog(t *testing.T) {
	integration(t)
	d := open(t, fixtureOf(t, CockroachDB))
	ctx := context.Background()
	mustExec(t, d, "ANALYZE it.customers", "CREATE TABLE it.no_key (a INT)")
	// The statistics reach the nodes' caches a moment after ANALYZE.
	var objs []Object
	i := -1
	for deadline := time.Now().Add(30 * time.Second); i < 0 || objs[i].Rows != 3 && time.Now().Before(deadline); {
		time.Sleep(200 * time.Millisecond)
		var err error
		if objs, err = d.Dialect.Objects(ctx, d.SQL, "it"); err != nil {
			t.Fatal(err)
		}
		i = slices.IndexFunc(objs, func(o Object) bool { return o.Name == "customers" })
	}
	if i < 0 || objs[i].Rows != 3 || objs[i].Bytes < 0 || slices.ContainsFunc(objs, func(o Object) bool { return o.Kind == KindView && o.Rows != -1 }) {
		t.Fatalf("objects %+v", objs)
	}
	for _, o := range []Object{objs[i], {Schema: "it", Name: "big_orders", Kind: KindView}} {
		ddl, err := d.Dialect.DDL(ctx, d.SQL, "it", o)
		if err != nil || !strings.HasPrefix(ddl, "CREATE ") || !strings.Contains(ddl, o.Name) || !strings.HasSuffix(ddl, ";\n") {
			t.Fatalf("%s: %q %v", o.Name, ddl, err)
		}
	}
	cols, err := d.Dialect.Columns(ctx, d.SQL, "it", "no_key")
	if err != nil || len(cols) != 1 || cols[0].Name != "a" || cols[0].PrimaryKey {
		t.Fatalf("columns %+v %v", cols, err)
	}
	schemas, err := d.Dialect.Schemas(ctx, d.SQL)
	if err != nil || slices.Contains(schemas, "crdb_internal") || slices.Contains(schemas, "pg_extension") || !slices.Contains(schemas, "it") {
		t.Fatalf("schemas %q %v", schemas, err)
	}
}

func TestCockroachDBFileReaders(t *testing.T) {
	r, _ := SQL(CockroachDB)
	if !slices.Equal(r.FileReadingFunctions(), []string{"read_file"}) {
		t.Errorf("%q", r.FileReadingFunctions())
	}
}

// queryFailing fails each query with the next of its errors, and keeps
// the queries asked.
type queryFailing struct {
	errs  []error
	asked []string
}

func (q *queryFailing) QueryContext(_ context.Context, query string, _ ...any) (*sql.Rows, error) {
	q.asked = append(q.asked, query)
	err := q.errs[0]
	q.errs = q.errs[1:]
	return nil, err
}

// The objects are read again without sizes only when sizes were refused
// for want of a privilege; any other error is the one reported.
func TestCockroachDBObjectsWithoutSizes(t *testing.T) {
	refused, lost := &pgconn.PgError{Code: "42501"}, errors.New("connection lost")
	q := &queryFailing{errs: []error{lost}}
	if _, err := (cockroachdbDialect{}).Objects(context.Background(), q, "public"); err != lost || len(q.asked) != 1 {
		t.Errorf("error %v after %d queries", err, len(q.asked))
	}
	q = &queryFailing{errs: []error{refused, lost}}
	if _, err := (cockroachdbDialect{}).Objects(context.Background(), q, "public"); err != lost || len(q.asked) != 2 ||
		!strings.Contains(q.asked[0], "SHOW RANGES") || strings.Contains(q.asked[1], "SHOW RANGES") {
		t.Errorf("error %v after queries %q", err, q.asked)
	}
}

// The app's own reads of CockroachDB's catalog and activity, crdb_internal
// functions among them, classify as reads.
func TestCockroachDBOwnReadsClassifyAsReads(t *testing.T) {
	r, _ := SQL(CockroachDB)
	q := &queryFailing{errs: []error{&pgconn.PgError{Code: "42501"}, errors.New("done")}}
	(cockroachdbDialect{}).Objects(context.Background(), q, "public")
	reads := append(q.asked, cockroachSearch)
	for _, page := range r.ActivityPages() {
		reads = append(reads, page.Query)
	}
	for _, sql := range reads {
		if a := sqltext.Classify(sql, LexDialect(CockroachDB)); a.Class != sqltext.Read || a.Dangerous {
			t.Errorf("%+v:\n%s", a, sql)
		}
	}
}

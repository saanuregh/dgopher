package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scansText writes scans as "schema.table (step) rows" each, for a test
// to compare.
func scansText(scans []Scan) string {
	var out []string
	for _, s := range scans {
		out = append(out, fmt.Sprintf("%s.%s (%s) %s", s.Schema, s.Table, s.Step, strconv.FormatFloat(s.Rows, 'f', -1, 64)))
	}
	return strings.Join(out, "; ")
}

// planLines are an EXPLAIN's rows of one text column, a line each.
func planLines(lines ...string) [][]any {
	var rows [][]any
	for _, l := range lines {
		rows = append(rows, []any{l})
	}
	return rows
}

// postgresScanPlan is EXPLAIN (VERBOSE, FORMAT JSON) of a join of a
// table read whole with one read by its key.
const postgresScanPlan = `[{"Plan": {"Node Type": "Hash Join", "Join Type": "Inner", "Plan Rows": 12, "Total Cost": 40,
  "Plans": [
    {"Node Type": "Seq Scan", "Parallel Aware": false, "Relation Name": "orders", "Schema": "shop", "Alias": "o", "Plan Rows": 12, "Total Cost": 30,
     "Filter": "(o.total > 100)"},
    {"Node Type": "Hash", "Plan Rows": 1, "Plans": [
      {"Node Type": "Index Scan", "Relation Name": "customers", "Schema": "shop", "Index Name": "customers_pkey", "Plan Rows": 1}]}]}}]`

// Each engine's scans are the steps of its plan that read a table from
// storage, whole, with the rows they read where the plan tells them; the
// PostgreSQL family's, and DuckDB's of a table named with its schema, are
// told by the catalog (-1 until then).
func TestScansOf(t *testing.T) {
	showplan := func(file string) [][]any {
		b, err := os.ReadFile(filepath.Join("testdata", "sqlserver_plans", file+".xml"))
		if err != nil {
			t.Fatal(err)
		}
		return [][]any{{string(b)}}
	}
	for _, c := range []struct {
		e     Engine
		names [][]string
		rows  [][][]any
		want  string
	}{
		{Postgres, [][]string{{"QUERY PLAN"}}, [][][]any{planLines(postgresScanPlan)}, "shop.orders (Seq Scan) -1"},
		{TimescaleDB, [][]string{{"QUERY PLAN"}}, [][][]any{planLines(postgresScanPlan)}, "shop.orders (Seq Scan) -1"},
		{YugabyteDB, [][]string{{"QUERY PLAN"}}, [][][]any{planLines(postgresScanPlan)}, "shop.orders (Seq Scan) -1"},
		{CockroachDB, [][]string{{"info"}}, [][][]any{planLines(cockroachExplainedScan...)}, ".it_plan (FULL SCAN) 20000"},
		{MySQL, [][]string{{"EXPLAIN"}}, [][][]any{planLines("-> Sort: t.v  (cost=110 rows=1000)\n" +
			"    -> Table scan on <temporary>  (cost=2.5 rows=10)\n" +
			"        -> Filter: (orders.total > 100)  (cost=101 rows=333)\n" +
			"            -> Table scan on orders  (cost=101 rows=1000)\n")}, ".orders (Table scan) 1000"},
		{MariaDB, [][]string{{"EXPLAIN"}}, [][][]any{planLines(mariadbAnalyzedJoin)}, ".pl_o (Table scan) 6000"},
		{MariaDB, [][]string{{"EXPLAIN"}}, [][][]any{planLines(mariadbAnalyzedUnion)}, ".pl_c (Table scan) 2000"},
		{TiDB, [][]string{{"TiDB_JSON"}}, [][][]any{planLines(tidbExplainedScan)}, ".it_plan (TableFullScan) 10000"},
		{SQLServer, [][]string{{sqlserverPlanColumn}}, [][][]any{showplan("estimated-join-1")}, "dbo.plan_groups (Clustered Index Scan) 1000"},
		{SQLServer, [][]string{{sqlserverPlanColumn}}, [][][]any{showplan("estimated-convert-1")}, "dbo.plan_rows (Index Scan) 50000"},
		{SQLServer, [][]string{{sqlserverPlanColumn}}, [][][]any{showplan("estimated-seek-1")}, ""},
		{Oracle, [][]string{oraclePlanCols}, [][][]any{oraclePlanRows}, "DGOPHER.cap_a (TABLE ACCESS FULL) 20000"},
		{DuckDB, [][]string{{"explain_key", "explain_value"}}, [][][]any{{{"physical_plan", `[{"name": "PROJECTION", "children": [
			{"name": "SEQ_SCAN ", "children": [], "extra_info": {"Table": "orders", "Type": "Sequential Scan", "Estimated Cardinality": "5000"}}],
			"extra_info": {"Projections": "id"}}]`}}}, ".orders (SEQ_SCAN) 5000"},
		{DuckDB, [][]string{{"explain_key", "explain_value"}}, [][][]any{{{"analyzed_plan", duckdb15AnalyzedPlan}}}, "main.it_plan (SEQ_SCAN) -1"},
		{ClickHouse, [][]string{{"database", "table", "parts", "rows", "marks"}},
			[][][]any{{{"shop", "events", uint64(3), uint64(5_000_000), uint64(611)}, {"shop", "users", uint64(1), uint64(20), uint64(1)}}},
			"shop.events (ReadFromMergeTree) 5000000; shop.users (ReadFromMergeTree) 20"},
		{SQLite, [][]string{{"id", "parent", "notused", "detail"}}, [][][]any{{{int64(2), int64(0), int64(0), "SCAN orders"}}}, ""},
	} {
		r, _ := SQL(c.e)
		if got := scansText(r.ScansOf(c.names, c.rows)); got != c.want {
			t.Errorf("%s: %q, want %q", c.e, got, c.want)
		}
	}
}

// scanCheckEngines are the engines the scan check's pins cover: every
// engine, and one no build knows.
var scanCheckEngines = append(Engines(), "unknown")

// TestPinScanCheck pins the run that tells each engine's scans, and the
// catalog read of the rows of those its plan does not tell.
func TestPinScanCheck(t *testing.T) {
	check := func(e Engine) string {
		r, ok := SQL(e)
		if !ok {
			return "none"
		}
		run, ok := r.ScanCheck("SELECT * FROM t")
		if !ok {
			return "none"
		}
		rows, args := r.TableRowsSQL([]Scan{{Schema: "shop", Table: "orders", Step: "Seq Scan", Rows: -1},
			{Schema: "shop", Table: "orders", Step: "Seq Scan", Rows: -1}, {Schema: "shop", Table: "lines", Step: "Seq Scan", Rows: 5}})
		return fmt.Sprintf("before=%q statement=%q offset=%d read=%v discard=%d after=%q rows=%q args=%q",
			run.Before, run.Statement, run.Offset, run.Read != "", len(run.Discard), run.After, rows, args)
	}
	pinValues(t, "scan check", map[string]string{
		"postgres":    "before=[] statement=\"EXPLAIN (VERBOSE, FORMAT JSON) SELECT * FROM t\" offset=31 read=false discard=0 after=[] rows=\"SELECT n.nspname, c.relname, c.reltuples FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE (n.nspname, c.relname) IN (($1, $2))\" args=[\"shop\" \"orders\"]",
		"cockroachdb": "before=[] statement=\"EXPLAIN (VERBOSE) SELECT * FROM t\" offset=18 read=false discard=0 after=[] rows=\"\" args=[]",
		"yugabytedb":  "before=[] statement=\"EXPLAIN (VERBOSE, FORMAT JSON) SELECT * FROM t\" offset=31 read=false discard=0 after=[] rows=\"SELECT n.nspname, c.relname, c.reltuples FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE (n.nspname, c.relname) IN (($1, $2))\" args=[\"shop\" \"orders\"]",
		"timescaledb": "before=[] statement=\"EXPLAIN (VERBOSE, FORMAT JSON) SELECT * FROM t\" offset=31 read=false discard=0 after=[] rows=\"SELECT n.nspname, c.relname, c.reltuples FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE (n.nspname, c.relname) IN (($1, $2))\" args=[\"shop\" \"orders\"]",
		"mysql":       "before=[] statement=\"EXPLAIN FORMAT=TREE SELECT * FROM t\" offset=20 read=false discard=0 after=[] rows=\"\" args=[]",
		"mariadb":     "before=[] statement=\"EXPLAIN FORMAT=JSON SELECT * FROM t\" offset=20 read=false discard=0 after=[] rows=\"\" args=[]",
		"tidb":        "before=[] statement=\"EXPLAIN FORMAT='tidb_json' SELECT * FROM t\" offset=27 read=false discard=0 after=[] rows=\"\" args=[]",
		"clickhouse":  "before=[] statement=\"EXPLAIN ESTIMATE SELECT * FROM t\" offset=17 read=false discard=0 after=[] rows=\"\" args=[]",
		"sqlserver":   "before=[\"SET SHOWPLAN_XML ON\"] statement=\"SELECT * FROM t\" offset=0 read=false discard=0 after=[\"SET SHOWPLAN_XML OFF\"] rows=\"\" args=[]",
		"oracle":      "before=[] statement=\"EXPLAIN PLAN SET STATEMENT_ID = 'DGOPHER' FOR SELECT * FROM t\" offset=46 read=true discard=1 after=[] rows=\"\" args=[]",
		"redis":       "none",
		"sqlite":      "none",
		"duckdb":      "before=[] statement=\"EXPLAIN (FORMAT JSON) SELECT * FROM t\" offset=22 read=false discard=0 after=[] rows=\"SELECT schema_name, table_name, estimated_size FROM duckdb_tables() WHERE (schema_name = ? AND table_name = ?)\" args=[\"shop\" \"orders\"]",
		"unknown":     "none",
	}, func(add func(key, got string)) {
		for _, e := range scanCheckEngines {
			add(string(e), check(e))
		}
	})
}

// TestPinPreviewNewName pins the alias of a SET column's new value in
// Preview Changes.
func TestPinPreviewNewName(t *testing.T) {
	pinValues(t, "preview new name", map[string]string{
		"postgres":    "\"total → new\" \"a\"\"b → new\"",
		"cockroachdb": "\"total → new\" \"a\"\"b → new\"",
		"yugabytedb":  "\"total → new\" \"a\"\"b → new\"",
		"timescaledb": "\"total → new\" \"a\"\"b → new\"",
		"mysql":       "`total → new` `a\"b → new`",
		"mariadb":     "`total → new` `a\"b → new`",
		"tidb":        "`total → new` `a\"b → new`",
		"clickhouse":  "`total → new` `a\"b → new`",
		"sqlserver":   "[total → new] [a\"b → new]",
		"oracle":      "\"total_new\" \"a\"\"b_new\"",
		"sqlite":      "\"total → new\" \"a\"\"b → new\"",
		"duckdb":      "\"total → new\" \"a\"\"b → new\"",
	}, func(add func(key, got string)) {
		for _, e := range scanCheckEngines {
			if r, ok := SQL(e); ok {
				add(string(e), r.PreviewNewName("total")+" "+r.PreviewNewName(`a"b`))
			}
		}
	})
}

// openFileDB opens an engine's database in a file of the test's folder.
func openFileDB(t *testing.T, e Engine, name string) (*DB, *Session) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), name)
	if r, _ := SQL(e); r.CreateFile(ctx, path) != nil {
		t.Fatalf("%s: cannot create %s", e, path)
	}
	d, err := Open(ctx, Config{Name: name, Engine: e, Database: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return d, s
}

// sentStatements collects what LargeScans reports it sent.
type sentStatements []string

func (s *sentStatements) ran(sql string, _ time.Duration, err error) {
	*s = append(*s, sql+" "+errText(err))
}

// DuckDB's catalog tells the rows a scan reads, which a filter does not
// lessen: those over the limit are returned, largest first, a statement's
// placeholders taking its values; inside the transaction its sessions
// share, nothing is sent around the plan's reads, and the transaction
// stays open.
func TestLargeScansDuckDB(t *testing.T) {
	ctx := context.Background()
	_, s := openFileDB(t, DuckDB, "scan.duckdb")
	for _, q := range []string{
		`CREATE TABLE big AS SELECT range AS id FROM range(10000)`,
		`CREATE TABLE small AS SELECT range AS id FROM range(2000)`,
	} {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	var sent sentStatements
	scans, ok, err := LargeScans(ctx, s, `SELECT * FROM small, big`, 1000, sent.ran)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if got := scansText(scans); got != "main.big (SEQ_SCAN) 10000; main.small (SEQ_SCAN) 2000" {
		t.Errorf("scans %q", got)
	}
	if len(sent) != 2 || !strings.HasPrefix(sent[0], "EXPLAIN (FORMAT JSON) SELECT") || !strings.HasPrefix(sent[1], "SELECT schema_name") ||
		!strings.HasSuffix(sent[0], " ok") || !strings.HasSuffix(sent[1], " ok") {
		t.Errorf("sent %q", sent)
	}
	if scans, ok, err := LargeScans(ctx, s, `SELECT * FROM big`, 10000, sent.ran); err != nil || !ok || len(scans) != 0 {
		t.Errorf("at the limit: %v %v %v", scans, ok, err)
	}
	scans, ok, err = LargeScans(ctx, s, `SELECT * FROM big WHERE id % 7 = ?`, 1000, sent.ran, int64(3))
	if got := scansText(scans); err != nil || !ok || got != "main.big (SEQ_SCAN) 10000" {
		t.Errorf("filtered, with a value: %q %v %v", got, ok, err)
	}
	if err := s.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	sent = nil
	if scans, ok, err := LargeScans(ctx, s, `SELECT * FROM big`, 1000, sent.ran); err != nil || !ok || len(scans) != 1 {
		t.Errorf("in a transaction: %v %v %v", scans, ok, err)
	}
	if len(sent) != 2 || s.Tx() != TxOpen {
		t.Errorf("in a transaction: sent %q, %v", sent, s.Tx())
	}
	if err := s.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

// SQLite estimates no rows: its scans are not checked, and nothing is
// sent.
func TestLargeScansSQLite(t *testing.T) {
	ctx := context.Background()
	_, s := openFileDB(t, SQLite, "scan.sqlite")
	var sent sentStatements
	scans, ok, err := LargeScans(ctx, s, `SELECT 1`, 1000, sent.ran)
	if scans != nil || ok || err != nil || len(sent) != 0 {
		t.Errorf("%v %v %v, sent %q", scans, ok, err, sent)
	}
}

// Inside a transaction, what a failed read did goes, behind its
// savepoint, and the transaction goes on with what it held before; a read
// that succeeds keeps what it did.
func TestReadAsideRollsBackToSavepoint(t *testing.T) {
	ctx := context.Background()
	_, s := openFileDB(t, SQLite, "aside.sqlite")
	if _, err := s.Exec(ctx, `CREATE TABLE t (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	failed := errors.New("the read failed")
	err := s.ReadAside(ctx, func(ctx context.Context) error {
		if _, err := s.Exec(ctx, `INSERT INTO t VALUES (2)`); err != nil {
			return err
		}
		return failed
	})
	if !errors.Is(err, failed) {
		t.Fatalf("ReadAside: %v", err)
	}
	if err := s.ReadAside(ctx, func(ctx context.Context) error {
		_, err := s.Exec(ctx, `INSERT INTO t VALUES (3)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if s.Tx() != TxOpen {
		t.Fatalf("transaction %v", s.Tx())
	}
	if row, _ := queryOne(t, s, `SELECT group_concat(id) FROM t`); Display(row[0]) != "1,3" {
		t.Errorf("rows %v", row)
	}
	if err := s.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

// Inside a PostgreSQL transaction, which an error aborts, a read that
// fails behind ReadAside leaves the transaction usable, with its changes.
func TestIntegrationReadAsideKeepsTransaction(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, err := Open(ctx, fixtureOf(t, Postgres).cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Rollback(ctx)
	for _, q := range []string{`CREATE TEMP TABLE aside_t (id int)`, `INSERT INTO aside_t VALUES (1)`} {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	err = s.ReadAside(ctx, func(ctx context.Context) error {
		c, err := s.Query(ctx, `SELECT * FROM no_such_table_here`)
		if err == nil {
			c.Close()
		}
		return err
	})
	if err == nil {
		t.Fatal("the read of a missing table did not fail")
	}
	if _, err := s.Exec(ctx, `INSERT INTO aside_t VALUES (2)`); err != nil || s.Tx() != TxOpen {
		t.Fatalf("after the failed read: %v %v", err, s.Tx())
	}
	if row, _ := queryOne(t, s, `SELECT count(*) FROM aside_t`); Display(row[0]) != "2" {
		t.Errorf("rows %v", row)
	}
	// LargeScans' own failure leaves it usable too.
	var sent sentStatements
	if _, ok, err := LargeScans(ctx, s, `SELECT * FROM no_such_table_here`, 1, sent.ran); !ok || err == nil {
		t.Fatalf("LargeScans of a missing table: %v %v", ok, err)
	}
	if row, _ := queryOne(t, s, `SELECT count(*) FROM aside_t`); s.Tx() != TxOpen || Display(row[0]) != "2" {
		t.Errorf("after LargeScans failed: %v %v", s.Tx(), row)
	}
}

// PostgreSQL's scans read the rows the catalog holds of their table, its
// statistics, not the plan's rows after the filter.
func TestIntegrationLargeScansPostgres(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, err := Open(ctx, fixtureOf(t, Postgres).cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	mustExec(t, d, `DROP SCHEMA IF EXISTS it_scan CASCADE`, `CREATE SCHEMA it_scan`,
		`CREATE TABLE it_scan.big AS SELECT g AS id FROM generate_series(1, 5000) g`,
		`CREATE TABLE it_scan.small AS SELECT g AS id FROM generate_series(1, 50) g`,
		`ANALYZE it_scan.big`, `ANALYZE it_scan.small`)
	// Deferred after Close, so it runs before it.
	defer mustExec(t, d, `DROP SCHEMA IF EXISTS it_scan CASCADE`)
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var sent sentStatements
	stmt := `SELECT * FROM it_scan.big b JOIN it_scan.small s ON s.id = b.id WHERE b.id % 7 = 0`
	scans, ok, err := LargeScans(ctx, s, stmt, 1000, sent.ran)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if got := scansText(scans); got != "it_scan.big (Seq Scan) 5000" {
		t.Errorf("scans %q", got)
	}
	if len(sent) != 2 || !strings.HasPrefix(sent[0], "EXPLAIN (VERBOSE, FORMAT JSON) ") || !strings.Contains(sent[1], "reltuples") {
		t.Errorf("sent %q", sent)
	}
	if scans, _, err := LargeScans(ctx, s, stmt, 10, sent.ran); err != nil || scansText(scans) != "it_scan.big (Seq Scan) 5000; it_scan.small (Seq Scan) 50" {
		t.Errorf("under a low limit: %q %v", scansText(scans), err)
	}
	if err := s.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if scans, _, err := LargeScans(ctx, s, `DELETE FROM it_scan.big`, 1000, sent.ran); err != nil || scansText(scans) != "it_scan.big (Seq Scan) 5000" || s.Tx() != TxOpen {
		t.Errorf("a DELETE in a transaction: %q %v %v", scansText(scans), err, s.Tx())
	}
	if row, _ := queryOne(t, s, `SELECT count(*) FROM it_scan.big`); Display(row[0]) != "5000" {
		t.Errorf("the explained DELETE ran: %v", row)
	}
	if err := s.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

// A read aside that meets the refusal of a connection made again leaves
// that refusal for the statement it was read for: the read sent nothing,
// and the statement must not run past what the lost session had set.
func TestReadAsideLeavesSessionReset(t *testing.T) {
	ctx := context.Background()
	_, s := openFileDB(t, SQLite, "reset.sqlite")
	s.resetPending = true
	if err := s.ReadAside(ctx, func(ctx context.Context) error {
		_, err := s.Exec(ctx, `SELECT 1`)
		return err
	}); !errors.Is(err, ErrSessionReset) {
		t.Fatalf("ReadAside: %v", err)
	}
	if _, err := s.Exec(ctx, `SELECT 1`); !errors.Is(err, ErrSessionReset) {
		t.Fatalf("the statement after the read ran past the reset: %v", err)
	}
}

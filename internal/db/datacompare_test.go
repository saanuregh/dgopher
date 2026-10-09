package db

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestComparableText(t *testing.T) {
	at := time.Date(2024, 3, 1, 9, 30, 15, 0, time.FixedZone("x", 3600))
	for _, c := range []struct {
		a     any
		aType string
		b     any
		bType string
		same  bool
	}{
		{int64(5), "BIGINT", "5.00", "DECIMAL(10,2)", true},
		{1.1, "DOUBLE", "1.1", "FLOAT", true},
		{"1", "BOOLEAN", true, "BOOLEAN", true},
		{"0", "BOOLEAN", true, "BOOLEAN", false},
		{at, "TIMESTAMPTZ", "2024-03-01 08:30:15+00:00", "TIMESTAMPTZ", true},
		{at, "TIMESTAMP", "2024-03-01 09:30:15.000000", "TIMESTAMP", true},
		{at, "DATE", "2024-03-01", "DATE", true},
		{`{"b": 1, "a": [1, 2]}`, "JSON", `{"a":[1,2],"b":1}`, "JSON", true},
		{"ABC-1", "UUID", "abc-1", "UUID", true},
		{"a", "VARCHAR", "A", "VARCHAR", false},
		{nil, "VARCHAR", "", "VARCHAR", false},
		{nil, "BIGINT", nil, "DECIMAL(10,2)", true},
		{[]byte("hi"), "BLOB", "hi", "BLOB", true},
	} {
		if got := sameValue(c.a, c.aType, c.b, c.bType); got != c.same {
			t.Errorf("%v (%s) and %v (%s): same %v, want %v", c.a, c.aType, c.b, c.bType, got, c.same)
		}
	}
}

// TestCompareRows compares a SQLite table with DuckDB's, across engines,
// then makes the target as the source and finds them alike.
func TestCompareRows(t *testing.T) {
	ctx := context.Background()
	exec := func(d *DB, stmts ...string) {
		t.Helper()
		for _, q := range stmts {
			if _, err := d.SQL.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	src, err := Open(ctx, sqliteFile(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	// An unsized NUMERIC is text to CanonicalType: its values still
	// compare as numbers with DuckDB's DECIMAL.
	exec(src, `CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, price NUMERIC, sold BOOLEAN, extra TEXT)`,
		`INSERT INTO items VALUES (1, 'pen', 1.5, 1, 'x'), (2, 'ink', 3, 0, NULL), (3, 'pad', 2.25, 1, NULL)`)
	dst, err := Open(ctx, Config{Name: "dst", Engine: DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	exec(dst, `CREATE TABLE items (id INTEGER PRIMARY KEY, name VARCHAR, price DECIMAL(10,2), sold BOOLEAN, stock INTEGER)`,
		`INSERT INTO items VALUES (1, 'pen', 1.50, true, 7), (2, 'quill', 3.00, false, 1), (4, 'nib', 0.5, true, 2)`)

	compare := func() *RowComparison {
		t.Helper()
		r, err := CompareRows(ctx, CompareTable{DB: src, Schema: "main", Table: "items"}, CompareTable{DB: dst, Schema: "main", Table: "items"}, func(int64) {})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := compare()
	if r.Same != 1 || r.Changed != 1 || r.OnlyInSource != 1 || r.OnlyInTarget != 1 || !r.Complete() {
		t.Fatalf("same %d, changed %d, only in source %d, only in target %d", r.Same, r.Changed, r.OnlyInSource, r.OnlyInTarget)
	}
	if want := []string{"id", "name", "price", "sold"}; !slices.Equal(r.Columns, want) {
		t.Fatalf("columns %v, want %v", r.Columns, want)
	}
	for _, d := range r.Differences {
		if d.Kind == ChangeUpdate && (len(d.Changed) != 1 || r.Columns[d.Changed[0]] != "name") {
			t.Fatalf("changed columns %v, want name only", d.Changed)
		}
	}

	stmts, err := r.Target.Statements(r.Changes(true, true, true))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := dst.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	if err := sess.ApplyEdits(ctx, stmts, func(int, int64, time.Duration, error) { ran++ }); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	if ran != 3 {
		t.Fatalf("%d statements ran, want 3", ran)
	}
	if r := compare(); r.Same != 3 || r.Changed+r.OnlyInSource+r.OnlyInTarget != 0 {
		t.Fatalf("after the sync: same %d, changed %d, only in source %d, only in target %d", r.Same, r.Changed, r.OnlyInSource, r.OnlyInTarget)
	}
	var stock int
	if err := dst.SQL.QueryRowContext(ctx, `SELECT stock FROM items WHERE id = 1`).Scan(&stock); err != nil || stock != 7 {
		t.Fatalf("a column only the target has: %d, %v; want 7 kept", stock, err)
	}

	// The source's rows must match the target's one to one.
	for _, c := range []struct{ rows, want string }{
		{`(1, 'a'), (1, 'b')`, "more than once"},
		{`(NULL, 'a')`, "NULL"},
	} {
		exec(src, `DROP TABLE IF EXISTS loose`, `CREATE TABLE loose (id INTEGER, name TEXT)`, `INSERT INTO loose VALUES `+c.rows)
		_, err := CompareRows(ctx, CompareTable{DB: src, Schema: "main", Table: "loose"}, CompareTable{DB: dst, Schema: "main", Table: "items"}, func(int64) {})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("source rows %s: %v, want %q", c.rows, err, c.want)
		}
	}
}

// TestApplyEditsRollsBack checks a statement changing other than the rows
// it wants undoes those before it.
func TestApplyEditsRollsBack(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, sqliteFile(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.SQL.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT); INSERT INTO t VALUES (1, 'a')`); err != nil {
		t.Fatal(err)
	}
	sess, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	err = sess.ApplyEdits(ctx, []Statement{
		{SQL: `INSERT INTO t VALUES (2, 'b')`, Want: 1},
		{SQL: `UPDATE t SET v = 'c' WHERE id = 9`, Want: 1},
	}, func(int, int64, time.Duration, error) {})
	if err == nil {
		t.Fatal("an update changing no row was applied")
	}
	var n int
	if err := d.SQL.QueryRowContext(ctx, `SELECT count(*) FROM t`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d rows, %v; want the insert rolled back", n, err)
	}
	if sess.Tx() != TxNone {
		t.Fatal("the transaction stayed open")
	}
}

// TestIntegrationCompareRows compares a PostgreSQL table with MySQL's,
// alike in values each engine writes its own way, then syncs them.
func TestIntegrationCompareRows(t *testing.T) {
	integration(t)
	ctx := context.Background()
	pg := open(t, fixture{cfg: Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dbgopher", Database: "postgres"},
		setup: []string{`DROP SCHEMA IF EXISTS cmp CASCADE`, `CREATE SCHEMA cmp`,
			`CREATE TABLE cmp.t (id int PRIMARY KEY, ok boolean, total numeric, at timestamptz, day date, doc jsonb, name text)`,
			`INSERT INTO cmp.t VALUES
				(1, true, 1.50, '2024-03-01 09:30:00+01', '2024-03-01', '{"b": 1, "a": [1, 2]}', 'pen'),
				(2, false, 3, '2024-03-02 00:00:00+00', '2024-03-02', 'null', 'ink'),
				(3, NULL, NULL, NULL, NULL, NULL, 'pad')`}})
	my := open(t, fixture{cfg: Config{Name: "mysql", Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"},
		setup: []string{`DROP TABLE IF EXISTS cmp_t`,
			`CREATE TABLE cmp_t (id int PRIMARY KEY, ok tinyint(1), total decimal(10,2), at datetime(6), day date, doc json, name text)`,
			`INSERT INTO cmp_t VALUES
				(1, 1, 1.5, '2024-03-01 08:30:00', '2024-03-01', '{"a": [1, 2], "b": 1}', 'pen'),
				(2, 0, 3.00, '2024-03-02 00:00:00', '2024-03-02', 'null', 'quill'),
				(9, 1, 0, NULL, NULL, NULL, 'nib')`}})
	compare := func() *RowComparison {
		t.Helper()
		r, err := CompareRows(ctx, CompareTable{DB: pg, Schema: "cmp", Table: "t"}, CompareTable{DB: my, Schema: "shop", Table: "cmp_t"}, func(int64) {})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := compare()
	if r.Same != 1 || r.Changed != 1 || r.OnlyInSource != 1 || r.OnlyInTarget != 1 {
		for _, d := range r.Differences {
			t.Logf("%v: %v / %v, changed %v", d.Kind, d.Source, d.Target, d.Changed)
		}
		t.Fatalf("same %d, changed %d, only in source %d, only in target %d", r.Same, r.Changed, r.OnlyInSource, r.OnlyInTarget)
	}
	stmts, err := r.Target.Statements(r.Changes(true, true, true))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := my.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.ApplyEdits(ctx, stmts, func(int, int64, time.Duration, error) {}); err != nil {
		t.Fatal(err)
	}
	if r := compare(); r.Same != 3 || r.Changed+r.OnlyInSource+r.OnlyInTarget != 0 {
		for _, d := range r.Differences {
			t.Logf("%v: %v / %v, changed %v", d.Kind, d.Source, d.Target, d.Changed)
		}
		t.Fatalf("after the sync: same %d, changed %d, only in source %d, only in target %d", r.Same, r.Changed, r.OnlyInSource, r.OnlyInTarget)
	}
}

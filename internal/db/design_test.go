package db

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseSQLiteTable(t *testing.T) {
	for _, c := range []struct {
		create string
		checks []string
		auto   []string
		unkept string
	}{
		{`CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, qty INT CHECK (qty > 0), "Note" TEXT DEFAULT (CAST('1' AS TEXT)),
  CONSTRAINT "positive" CHECK (id > 0 AND (qty < 100)))`, []string{"qty > 0", "positive:id > 0 AND (qty < 100)"}, []string{"id"}, ""},
		{`CREATE TABLE t (a TEXT COLLATE NOCASE)`, nil, nil, "a collation"},
		{`CREATE TABLE t (a INT, b INT GENERATED ALWAYS AS (a * 2))`, nil, nil, "a computed column"},
		{`CREATE TABLE t (a INT UNIQUE ON CONFLICT REPLACE)`, nil, nil, "an ON CONFLICT clause"},
		{`CREATE TABLE t (a INT) WITHOUT ROWID`, nil, nil, "WITHOUT ROWID or STRICT"},
		{`CREATE TABLE t (a INT, FOREIGN KEY (a) REFERENCES p (id) DEFERRABLE INITIALLY DEFERRED)`, nil, nil, "a deferred foreign key"},
	} {
		got := parseSQLiteTable(c.create)
		var checks []string
		for _, ch := range got.checks {
			s := ch.Expression
			if ch.Name != "" {
				s = ch.Name + ":" + s
			}
			checks = append(checks, s)
		}
		if !slices.Equal(checks, c.checks) || !slices.Equal(got.autoIncrement, c.auto) || got.unkept != c.unkept {
			t.Errorf("%s:\n checks %q, auto %q, unkept %q", c.create, checks, got.autoIncrement, got.unkept)
		}
	}
}

// newSQLiteDB opens a new SQLite database file.
func newSQLiteDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(context.Background(), sqliteFile(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// apply runs a change on a session of its own, and gives the statements
// it ran.
func apply(t *testing.T, d *DB, ch SchemaChange) ([]string, error) {
	t.Helper()
	ctx := context.Background()
	sess, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var ran []string
	err = sess.Apply(ctx, ch, func(stmt string, _ int64, _ time.Duration, err error) { ran = append(ran, stmt) })
	return ran, err
}

func mustExec(t *testing.T, d *DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatal(s, err)
		}
	}
}

func TestSQLiteDesign(t *testing.T) {
	ctx := context.Background()
	d := newSQLiteDB(t)
	design := TableDesign{Schema: "main", Name: "orders", Columns: []ColumnDesign{
		{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
		{Name: "customer", Type: "INTEGER", Nullable: true},
		{Name: "qty", Type: "INTEGER", Default: "1"},
		{Name: "note", Type: "TEXT", Nullable: true},
	},
		Indexes:     []IndexDesign{{Name: "orders_customer", Columns: []string{"customer"}}},
		ForeignKeys: []ForeignKeyDesign{{Name: "orders_customer_fkey", Columns: []string{"customer"}, RefSchema: "main", RefTable: "customers", RefColumns: []string{"id"}, OnDelete: "CASCADE"}},
		Checks:      []CheckDesign{{Name: "qty_positive", Expression: "qty > 0"}},
	}
	mustExec(t, d, `CREATE TABLE customers (id INTEGER PRIMARY KEY)`, `INSERT INTO customers VALUES (1), (2)`)
	ch, err := NewTableChange(d.Dialect, design)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apply(t, d, ch); err != nil {
		t.Fatalf("%v\n%s", err, ch.Text())
	}
	mustExec(t, d, `INSERT INTO orders (customer, qty, note) VALUES (1, 2, 'a'), (2, 3, NULL)`,
		`CREATE TRIGGER orders_touch AFTER UPDATE ON orders BEGIN SELECT 1; END`,
		`CREATE VIEW big_orders AS SELECT id, qty FROM orders WHERE qty > 2`)
	was, err := ReadTableDesign(ctx, d, Object{Schema: "main", Name: "orders", Kind: KindTable})
	if err != nil {
		t.Fatal(err)
	}
	if !was.Columns[0].AutoIncrement || len(was.Checks) != 1 || was.Checks[0].Expression != "qty > 0" ||
		len(was.ForeignKeys) != 1 || was.ForeignKeys[0].OnDelete != "CASCADE" || was.sqliteUnkept != "" {
		t.Fatalf("read %+v", was)
	}

	// Adding a column and dropping one alone are made in place.
	now := clone(was)
	now.Columns = append(slices.Delete(now.Columns, 3, 4), ColumnDesign{Name: "placed", Type: "TEXT", Nullable: true})
	ch, err = AlterTableChange(d.Dialect, was, now)
	if err != nil || ch.rebuilds() {
		t.Fatalf("%v:\n%s", err, ch.Text())
	}
	if _, err := apply(t, d, ch); err != nil {
		t.Fatalf("%v\n%s", err, ch.Text())
	}

	// Renaming a column and changing another's type makes the table again,
	// its index, trigger, view and rows kept.
	was, _ = ReadTableDesign(ctx, d, Object{Schema: "main", Name: "orders", Kind: KindTable})
	now = clone(was)
	now.Columns[2].Name, now.Columns[2].Type = "quantity", "REAL"
	now.Checks = nil
	ch, err = AlterTableChange(d.Dialect, was, now)
	if err != nil || !ch.rebuilds() {
		t.Fatalf("%v:\n%s", err, ch.Text())
	}
	ran, err := apply(t, d, ch)
	if err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(ran, "\n"))
	}
	var n int
	if err := d.SQL.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('orders_customer', 'orders_touch', 'big_orders')`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("%d of the index, trigger and view kept: %v\n%s", n, err, strings.Join(ran, "\n"))
	}
	var qty float64
	if err := d.SQL.QueryRow(`SELECT sum(quantity) FROM big_orders`).Scan(&qty); err != nil || qty != 3 {
		t.Fatalf("rows: %v %v", qty, err)
	}
	after, _ := ReadTableDesign(ctx, d, Object{Schema: "main", Name: "orders", Kind: KindTable})
	if after.Columns[2].Type != "REAL" || len(after.Checks) != 0 || !after.Columns[0].AutoIncrement || after.ForeignKeys[0].OnDelete != "CASCADE" {
		t.Fatalf("after %+v", after)
	}
	var fk int
	d.SQL.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Fatal("foreign keys are left off")
	}

	// A rebuild that would break a key rolls back.
	now = clone(after)
	now.ForeignKeys = append(now.ForeignKeys, ForeignKeyDesign{Columns: []string{"quantity"}, RefSchema: "main", RefTable: "customers", RefColumns: []string{"id"}})
	ch, _ = AlterTableChange(d.Dialect, after, now)
	if _, err := apply(t, d, ch); err == nil || !strings.Contains(err.Error(), "breaks foreign keys") {
		t.Fatalf("a broken key: %v", err)
	}
	if again, _ := ReadTableDesign(ctx, d, Object{Schema: "main", Name: "orders", Kind: KindTable}); len(again.ForeignKeys) != 1 {
		t.Fatal("a failed rebuild was kept")
	}

	// What a rebuild could not keep refuses it.
	mustExec(t, d, `CREATE TABLE names (n TEXT COLLATE NOCASE, m TEXT)`)
	names, _ := ReadTableDesign(ctx, d, Object{Schema: "main", Name: "names", Kind: KindTable})
	now = clone(names)
	now.Columns[1].Nullable = false
	if _, err := AlterTableChange(d.Dialect, names, now); err == nil || !strings.Contains(err.Error(), "a collation") {
		t.Fatalf("an unkept collation: %v", err)
	}
}

// clone copies a design, so that changing one leaves the other.
func clone(t TableDesign) TableDesign {
	t.Columns = slices.Clone(t.Columns)
	t.Indexes = slices.Clone(t.Indexes)
	t.ForeignKeys = slices.Clone(t.ForeignKeys)
	t.Checks = slices.Clone(t.Checks)
	return t
}

package db

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// PostgreSQL's explicit cast cuts a value to a character type's length,
// and cuts or pads a bit string, without an error: a type change to one
// converts as the server does when assigning, which refuses what does not
// fit. Others need their cast: no other conversion exists.
func TestModifyColumnUsing(t *testing.T) {
	for _, c := range []struct {
		e        Engine
		was, now string
		using    bool
	}{
		{Postgres, "varchar(50)", "varchar(5)", false},
		{Postgres, "text", "varchar(5)", false},
		{Postgres, "char(10)", "char(3)", false},
		{Postgres, "char(10)", "varchar(3)", false},
		{Postgres, "integer", "varchar(2)", false},
		{Postgres, "integer", "text", true},
		{Postgres, "bit(8)", "bit(4)", false},
		{Postgres, "varbit(8)", "varbit(4)", false},
		{Postgres, "bit varying(8)", "bit(4)", false},
		{Postgres, "CHARACTER VARYING(50)", "character varying(5)", false},
		{Postgres, "bpchar(4)", "character(2)", false},
		{Postgres, "pg_catalog.varchar(50)", "pg_catalog.varchar(5)", false},
		{Postgres, "text", "character  varying (5)", false},
		{Postgres, "pg_catalog.bit(8)", "PG_CATALOG.bit(4)", false},
		{Postgres, "text", "integer", true},
		{Postgres, "varchar(10)", "integer", true},
		{Postgres, "text", "jsonb", true},
		{Postgres, "text", "boolean", true},
		{Postgres, "integer", "bit(4)", true},
		{Postgres, "integer", "bigint", true},
		{Postgres, "numeric(10,4)", "numeric(10,2)", true},
		{Postgres, "text", "varchar(5)[]", true},
		{DuckDB, "VARCHAR", "INTEGER", true},
		{DuckDB, "INTEGER", "VARCHAR", true},
	} {
		was, now := ColumnDesign{Name: "c", Type: c.was}, ColumnDesign{Name: "c", Type: c.now}
		steps, err := modifyColumnSQL(DialectOf(c.e), TableDesign{Name: "t"}, was, now)
		if err != nil || len(steps) != 1 {
			t.Fatalf("%s %s → %s: %q %v", c.e, c.was, c.now, steps, err)
		}
		if strings.Contains(steps[0], " USING ") != c.using {
			t.Errorf("%s %s → %s: %s, want USING %v", c.e, c.was, c.now, steps[0], c.using)
		}
	}
}

// On the server, shortening a column or a bit string that holds a longer
// value fails and keeps the value; a conversion that needs its cast still
// converts.
func TestIntegrationPostgresTypeChangeKeepsValues(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, err := Open(ctx, Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dgopher", Database: "postgres"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	mustExec(t, d, `DROP SCHEMA IF EXISTS it_using CASCADE`, `CREATE SCHEMA it_using`,
		`CREATE TABLE it_using.t (s varchar(50), n text, b bit(8))`,
		`INSERT INTO it_using.t VALUES ('abcdefghijkl', '12', B'10101010')`)
	defer mustExec(t, d, `DROP SCHEMA it_using CASCADE`)
	read := func() TableDesign {
		t.Helper()
		objs, err := d.Dialect.Objects(ctx, d.SQL, "it_using")
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(objs, func(o Object) bool { return o.Name == "t" })
		if i < 0 {
			t.Fatal("no table")
		}
		got, err := ReadTableDesign(ctx, d, objs[i])
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	change := func(col int, typ string) error {
		t.Helper()
		was := read()
		now := clone(was)
		now.Columns[col].Type = typ
		ch, err := AlterTableChange(d.Dialect, was, now)
		if err != nil {
			t.Fatal(err)
		}
		_, err = apply(t, d, ch)
		return err
	}
	var s, n, b string
	values := func() {
		t.Helper()
		if err := d.SQL.QueryRowContext(ctx, `SELECT s::text, n::text, b::text FROM it_using.t`).Scan(&s, &n, &b); err != nil {
			t.Fatal(err)
		}
	}
	if err := change(0, "varchar(5)"); err == nil {
		values()
		t.Fatalf("shortening to varchar(5) went through: %q", s)
	}
	if err := change(2, "bit(4)"); err == nil {
		values()
		t.Fatalf("shortening to bit(4) went through: %q", b)
	}
	if err := change(1, "integer"); err != nil {
		t.Fatalf("text to integer: %v", err)
	}
	values()
	if s != "abcdefghijkl" || n != "12" || b != "10101010" {
		t.Fatalf("values %q %q %q", s, n, b)
	}
}

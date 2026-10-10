package export

import (
	"bytes"
	"context"
	"database/sql"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

var byteValues = [][]byte{[]byte(`\x41`), []byte(`a\b`), []byte(`\\`), []byte(`\101`), []byte("plain"), {0xff, 0x00, '\''}}

func byteRows() [][]any {
	var rows [][]any
	for i, v := range byteValues {
		rows = append(rows, []any{i, v})
	}
	return rows
}

// TestSQLiteBlobLoadsAsBlob checks exported bytes load into SQLite as a
// BLOB of the same bytes, not as text.
func TestSQLiteBlobLoadsAsBlob(t *testing.T) {
	conn, err := sql.Open("sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec(`CREATE TABLE t (id INTEGER, b BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(render(t, SQL, []string{"id", "b"}, byteRows(), Options{Table: "t"})); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(`SELECT typeof(b), b FROM t ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	i := 0
	for ; rows.Next(); i++ {
		var kind string
		var b []byte
		if err := rows.Scan(&kind, &b); err != nil {
			t.Fatal(err)
		}
		if kind != "blob" || !bytes.Equal(b, byteValues[i]) {
			t.Errorf("row %d: %s % x, want blob % x", i, kind, b, byteValues[i])
		}
	}
	if i != len(byteValues) {
		t.Errorf("%d rows back of %d", i, len(byteValues))
	}
}

// TestIntegrationPostgresByteaRoundTrip checks exported bytea loads back
// as the same bytes, whatever standard_conforming_strings is.
func TestIntegrationPostgresByteaRoundTrip(t *testing.T) {
	testutil.Integration(t)
	ctx := context.Background()
	d, err := db.Open(ctx, testutil.PGConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	conn, err := d.SQL.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	script := render(t, SQL, []string{"id", "b"}, byteRows(), Options{Table: "zz_bytes", Engine: db.Postgres})
	for _, standard := range []string{"on", "off"} {
		for _, q := range []string{`SET standard_conforming_strings = ` + standard, `DROP TABLE IF EXISTS zz_bytes`, `CREATE TABLE zz_bytes (id integer, b bytea)`, script} {
			if _, err := conn.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		rows, err := conn.QueryContext(ctx, `SELECT b FROM zz_bytes ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		i := 0
		for ; rows.Next(); i++ {
			var b []byte
			if err := rows.Scan(&b); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(b, byteValues[i]) {
				t.Errorf("standard_conforming_strings %s, row %d: % x, want % x", standard, i, b, byteValues[i])
			}
		}
		rows.Close()
		if i != len(byteValues) {
			t.Errorf("%d rows back of %d", i, len(byteValues))
		}
	}
	if _, err := conn.ExecContext(ctx, `DROP TABLE zz_bytes`); err != nil {
		t.Error(err)
	}
}

// Text holding NUL loads into SQLite as the same text: SQLite's literals
// have no escape for it.
func TestSQLiteNULTextLoads(t *testing.T) {
	conn, err := sql.Open("sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	want := []string{"a\x00b", "\x00it's\x00"}
	if _, err := conn.Exec(`CREATE TABLE t (id INTEGER, s TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(render(t, SQL, []string{"id", "s"}, [][]any{{0, want[0]}, {1, want[1]}}, Options{Table: "t"})); err != nil {
		t.Fatal(err)
	}
	for i, w := range want {
		var got string
		if err := conn.QueryRow(`SELECT s FROM t WHERE id = ?`, i).Scan(&got); err != nil || got != w {
			t.Errorf("row %d: %q, %v; want %q", i, got, err, w)
		}
	}
}

// ClickHouse reads NUL in a string as \0, as db.Literal writes it.
func TestClickHouseNULEscaped(t *testing.T) {
	if got := sqlLiteral("a\x00b", db.ClickHouse); got != `'a\0b'` {
		t.Fatalf("%q", got)
	}
}

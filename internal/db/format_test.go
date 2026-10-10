package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	"dgopher/internal/sqltext"
)

func TestLiteral(t *testing.T) {
	cases := []struct {
		e    Engine
		v    any
		want string
	}{
		{Postgres, nil, "NULL"},
		{Postgres, int64(42), "42"},
		{Postgres, "it's", "'it''s'"},
		{Postgres, `a\'b`, `E'a\\''b'`},
		{Postgres, `\' OR TRUE --`, `E'\\'' OR TRUE --'`},
		{DuckDB, "it's", "'it''s'"},
		{DuckDB, `C:\dir\`, `E'C:\\dir\\'`},
		{SQLite, "x'); DROP TABLE t; --", "'x''); DROP TABLE t; --'"},
		{SQLite, `a\'b`, `'a\''b'`},
		{MySQL, `a\'b`, `'a\\''b'`},
		{MySQL, "nul\x00", `'nul\0'`},
		{ClickHouse, `a\'b`, `'a\\\'b'`},
		{DuckDB, true, "TRUE"},
		{DuckDB, "a\x00b", `('a' || chr(0) || 'b')`},
		{DuckDB, "\x00", `(chr(0))`},
		{DuckDB, `\` + "\x00\x00'", `(E'\\' || chr(0) || chr(0) || '''')`},
		{Postgres, "a\x00b", `('a' || chr(0) || 'b')`},
		{ClickHouse, "a\x00b", `'a\0b'`},
	}
	for _, c := range cases {
		if got := Literal(c.e, c.v); got != c.want {
			t.Errorf("Literal(%s, %q) = %s, want %s", c.e, c.v, got, c.want)
		}
	}
}

// TestQuoteClickHouse checks a ClickHouse name quotes as one identifier
// that reads back as itself, since ClickHouse reads backslash escapes
// inside backticks; MySQL reads a backslash there as itself.
func TestQuoteClickHouse(t *testing.T) {
	cases := []struct{ name, want string }{
		{"`", "````"},
		{`\`, "`\\\\`"},
		{`x\`, "`x\\\\`"},
		{`a\\b`, "`a\\\\\\\\b`"},
		{"\n", "`\n`"},
		{"\x00", "`\x00`"},
		{`"`, "`\"`"},
		{`'`, "`'`"},
		{"x\\`; DROP TABLE t; --", "`x\\\\``; DROP TABLE t; --`"},
	}
	for _, c := range cases {
		got := DialectOf(ClickHouse).Quote(c.name)
		if got != c.want {
			t.Errorf("Quote(%q) = %q, want %q", c.name, got, c.want)
		}
		if toks := sqltext.Tokenize(got, sqltext.ClickHouse); len(toks) != 1 || toks[0].Kind != sqltext.QuotedIdent {
			t.Errorf("Quote(%q) = %q lexes as %d tokens", c.name, got, len(toks))
		}
		if back := sqltext.DecodeQuoted(got, sqltext.ClickHouse); back != c.name {
			t.Errorf("Quote(%q) = %q reads back as %q", c.name, got, back)
		}
	}
	if got := DialectOf(MySQL).Quote("a\\b`"); got != "`a\\b```" {
		t.Errorf("MySQL Quote = %q, want the backslash kept as it is", got)
	}
}

func TestLiteralRoundTrip(t *testing.T) {
	if testing.Short() {
		return
	}
	integration(t)
	hostile := []string{`it's`, `back\slash`, `\'; DROP TABLE x; --`, "new\nline", `trailing\`, `''`}
	for _, f := range fixtures(t) {
		d := open(t, f)
		values := hostile
		// PostgreSQL text cannot hold NUL.
		if f.cfg.Engine != Postgres {
			values = append(values[:len(values):len(values)], "nul\x00byte", "\x00", `\`+"\x00'")
		}
		for _, s := range values {
			var got string
			if err := d.SQL.QueryRow("SELECT " + Literal(f.cfg.Engine, s)).Scan(&got); err != nil || got != s {
				t.Errorf("%s: %q came back as %q (%v)", f.cfg.Name, s, got, err)
			}
		}
	}
}

// TestLiteralNULLoads checks text holding NUL loads back equal in the
// engines that run without a server.
func TestLiteralNULLoads(t *testing.T) {
	for _, c := range []struct {
		e      Engine
		driver string
	}{{DuckDB, "duckdb"}, {SQLite, "sqlite"}} {
		conn, err := sql.Open(c.driver, "")
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		for _, s := range []string{"nul\x00byte", "\x00", `\` + "\x00'", "\x00\x00"} {
			var got string
			if err := conn.QueryRow("SELECT " + Literal(c.e, s)).Scan(&got); err != nil || got != s {
				t.Errorf("%s: %q came back as %q (%v)", c.e, s, got, err)
			}
		}
	}
}

func TestRedact(t *testing.T) {
	cfg := Config{Password: "hunter2!", SSH: SSHConfig{Password: "s3cr3t"}}
	err := redact(errorString("auth failed for postgres://u:hunter2%21@h/db and ssh s3cr3t"), cfg)
	if got := err.Error(); got != "auth failed for postgres://u:•••@h/db and ssh •••" {
		t.Fatalf("redacted %q", got)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

func TestUpdateToDefault(t *testing.T) {
	target := EditTarget{Dialect: DialectOf(Postgres), Schema: "s", Table: "t", Key: []string{"id"},
		Columns: []Column{{Name: "id", Type: "integer"}, {Name: "status", Type: "text"}}}
	stmts, err := target.Statements([]Change{{Kind: ChangeUpdate, Key: []any{int64(1)}, Values: map[string]any{"status": Default}}})
	if err != nil || len(stmts) != 1 || stmts[0].SQL != `UPDATE "s"."t" SET "status" = DEFAULT WHERE "id" = $1` || len(stmts[0].Args) != 1 {
		t.Fatalf("%+v %v", stmts, err)
	}
}

func TestLiteralOfBytes(t *testing.T) {
	b := []byte{0x01, 0xab, 'A'}
	want := map[Engine]string{
		Postgres:   `E'\\x01ab41'::bytea`,
		MySQL:      `X'01ab41'`,
		SQLite:     `X'01ab41'`,
		DuckDB:     `'\x01\xAB\x41'::BLOB`,
		ClickHouse: `unhex('01ab41')`,
	}
	for e, w := range want {
		if got := Literal(e, b); got != w {
			t.Errorf("%s: %s, want %s", e, got, w)
		}
	}
}

// TestStatementScript checks a statement's parameters are written as
// literals of its engine, and placeholders inside quoted names are not.
func TestStatementScript(t *testing.T) {
	pg := Statement{SQL: `UPDATE "t" SET "a$1" = $2::text::numeric WHERE "id" = $1`, Args: []any{int64(7), Typed("1.5")}}
	if got, want := pg.Script(Postgres), `UPDATE "t" SET "a$1" = '1.5'::text::numeric WHERE "id" = 7;`; got != want {
		t.Errorf("PostgreSQL: %s, want %s", got, want)
	}
	my := Statement{SQL: "INSERT INTO `t` (`a?`, `b`, `c`) VALUES (?, ?, ?)", Args: []any{`it's \ here`, nil, []byte{1}}}
	if got, want := my.Script(MySQL), "INSERT INTO `t` (`a?`, `b`, `c`) VALUES ('it''s \\\\ here', NULL, X'01');"; got != want {
		t.Errorf("MySQL: %s, want %s", got, want)
	}
	if got, want := my.Preview(), "INSERT INTO `t` (`a?`, `b`, `c`) VALUES ('it''s \\ here', NULL, '\x01');"; got != want {
		t.Errorf("preview: %q, want %q", got, want)
	}
}

func TestExistingRowChanges(t *testing.T) {
	stmts := []Statement{{SQL: "UPDATE t SET a = 1 WHERE id = 1"}, {SQL: "INSERT INTO t (a) VALUES (1)"}, {SQL: "DELETE FROM t WHERE id = 2"}}
	if n := ExistingRowChanges(stmts); n != 2 {
		t.Fatalf("existing %d", n)
	}
}

// TestCursorTextIsString pins the rule export relies on: a text column
// reads as a string even when the driver sends bytes, and only a binary
// column reads as bytes, so bytes can be written as binary literals.
// SQLite keeps a value's own type, whatever its column declares: a BLOB
// in an untyped or TEXT column is read as bytes, text in a BLOB column as
// text.
func TestCursorSQLiteStorageClass(t *testing.T) {
	d, err := Open(context.Background(), Config{Name: "lite", Engine: SQLite, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []string{`CREATE TABLE t (u, x TEXT, b BLOB)`, `INSERT INTO t VALUES (x'616263', x'616263', 'abc')`} {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	c, err := s.Query(ctx, `SELECT u, x, b FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Fetch(10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	for i, want := range []any{[]byte("abc"), []byte("abc"), "abc"} {
		if fmt.Sprintf("%T %v", rows[0][i], rows[0][i]) != fmt.Sprintf("%T %v", want, want) {
			t.Errorf("column %d read as %T %v, want %T", i, rows[0][i], rows[0][i], want)
		}
	}
}

func TestCursorTextIsString(t *testing.T) {
	binary := map[Engine]string{Postgres: "bytea", MySQL: "BLOB", SQLite: "BLOB", DuckDB: "BLOB"}
	servers := os.Getenv("DGOPHER_IT") != ""
	if servers {
		integration(t)
	}
	for _, f := range fixtures(t) {
		if f.cfg.Engine == ClickHouse {
			continue
		}
		if f.cfg.Engine == Postgres || f.cfg.Engine == MySQL {
			if !servers {
				continue
			}
		}
		d := open(t, f)
		ctx := context.Background()
		s, err := d.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		bin := Literal(f.cfg.Engine, []byte("abc"))
		for _, q := range []string{`DROP TABLE IF EXISTS zz_cursor`,
			`CREATE TABLE zz_cursor (t TEXT, v VARCHAR(10), b ` + binary[f.cfg.Engine] + `)`,
			`INSERT INTO zz_cursor VALUES ('abc', 'abc', ` + bin + `)`} {
			if _, err := s.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %s: %v", f.cfg.Name, q, err)
			}
		}
		c, err := s.Query(ctx, `SELECT t, v, b FROM zz_cursor`)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := c.Fetch(10)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: %v %v", f.cfg.Name, rows, err)
		}
		if _, ok := rows[0][0].(string); !ok {
			t.Errorf("%s: TEXT read as %T", f.cfg.Name, rows[0][0])
		}
		if _, ok := rows[0][1].(string); !ok {
			t.Errorf("%s: VARCHAR read as %T", f.cfg.Name, rows[0][1])
		}
		if b, ok := rows[0][2].([]byte); !ok || string(b) != "abc" {
			t.Errorf("%s: binary read as %T %v", f.cfg.Name, rows[0][2], rows[0][2])
		}
		if _, err := s.Exec(ctx, `DROP TABLE zz_cursor`); err != nil {
			t.Error(err)
		}
		s.Close()
	}
}

func BenchmarkBytesLiteralDuckDB64K(b *testing.B) {
	blob := make([]byte, 64<<10)
	for i := range blob {
		blob[i] = byte(i)
	}
	b.ReportAllocs()
	for b.Loop() {
		bytesLiteral(DuckDB, blob)
	}
}

func BenchmarkTextLiteralPostgresBackslash(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		textLiteral(Postgres, `C:\path\it's`)
	}
}

package db_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/sqltext"
	"dgopher/internal/testutil"
)

// serverAnswer is what a server did with one query: its column names and
// first row, or the error it returned.
type serverAnswer struct {
	columns []string
	row     []string
	err     error
}

func ask(ctx context.Context, d *db.DB, query string) serverAnswer {
	rows, err := d.SQL.QueryContext(ctx, query)
	if err != nil {
		return serverAnswer{err: err}
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return serverAnswer{err: err}
	}
	var row []string
	if rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return serverAnswer{err: err}
		}
		for _, v := range vals {
			row = append(row, v.String)
		}
	}
	if err := rows.Err(); err != nil {
		return serverAnswer{err: err}
	}
	return serverAnswer{columns: cols, row: row}
}

func tokensOf(src string, d sqltext.Dialect) []sqltext.Token {
	var out []sqltext.Token
	for _, t := range sqltext.Tokenize(src, d) {
		if t.Kind != sqltext.Whitespace {
			out = append(out, t)
		}
	}
	return out
}

func describe(toks []sqltext.Token) string {
	s := ""
	for _, t := range toks {
		s += fmt.Sprintf("[%d %q] ", t.Kind, t.Text)
	}
	return s
}

func commentTexts(toks []sqltext.Token) []string {
	var out []string
	for _, t := range toks {
		if t.Kind == sqltext.Comment {
			out = append(out, t.Text)
		}
	}
	return out
}

func hasToken(toks []sqltext.Token, k sqltext.Kind, text string) bool {
	for _, t := range toks {
		if t.Kind == k && t.Text == text {
			return true
		}
	}
	return false
}

type lexCase struct {
	query string
	check func(t *testing.T, a serverAnswer, toks []sqltext.Token)
}

// columnsAgree checks that the server returned n columns and that our lexer
// leaves n-1 top-level commas outside comments.
func columnsAgree(n int) func(*testing.T, serverAnswer, []sqltext.Token) {
	return func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
		t.Helper()
		if a.err != nil {
			t.Fatalf("server error: %v", a.err)
		}
		if len(a.columns) != n {
			t.Fatalf("server returned %d columns %q, want %d", len(a.columns), a.columns, n)
		}
		commas := 0
		for _, tok := range toks {
			if tok.Kind == sqltext.Punct && tok.Text == "," {
				commas++
			}
		}
		if commas != n-1 {
			t.Errorf("server returned %d columns, our lexer sees %d commas: %s", n, commas, describe(toks))
		}
	}
}

// columnsRecorded asserts our lexer's comma count matches whatever column
// count the server returned.
func columnsRecorded(t *testing.T, a serverAnswer, toks []sqltext.Token) {
	t.Helper()
	if a.err != nil {
		t.Fatalf("server error: %v", a.err)
	}
	t.Logf("server returned %d columns %q", len(a.columns), a.columns)
	columnsAgree(len(a.columns))(t, a, toks)
}

func oneComment(text string) func(*testing.T, serverAnswer, []sqltext.Token) {
	return func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
		t.Helper()
		if a.err != nil {
			t.Fatalf("server error: %v", a.err)
		}
		if c := commentTexts(toks); len(c) != 1 || c[0] != text {
			t.Errorf("want one Comment %q, got %s", text, describe(toks))
		}
	}
}

func columnNamed(name string, kind sqltext.Kind, tokText string) func(*testing.T, serverAnswer, []sqltext.Token) {
	return func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
		t.Helper()
		if a.err != nil {
			t.Fatalf("server error: %v", a.err)
		}
		if len(a.columns) != 1 || a.columns[0] != name {
			t.Fatalf("server columns %q, want [%q]", a.columns, name)
		}
		if !hasToken(toks, kind, tokText) {
			t.Errorf("want token kind %d %q, got %s", kind, tokText, describe(toks))
		}
	}
}

func valueIsString(value, tokText string, d sqltext.Dialect, query string) func(*testing.T, serverAnswer, []sqltext.Token) {
	return func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
		t.Helper()
		if a.err != nil {
			t.Fatalf("server error: %v", a.err)
		}
		if len(a.row) != 1 || a.row[0] != value {
			t.Fatalf("server row %q, want [%q]", a.row, value)
		}
		if !hasToken(toks, sqltext.String, tokText) {
			t.Errorf("want one String %q, got %s", tokText, describe(toks))
		}
		if n := len(sqltext.Split(query, d)); n != 1 {
			t.Errorf("Split gives %d statements, want 1", n)
		}
	}
}

func runLexCases(t *testing.T, d *db.DB, dialect sqltext.Dialect, cases []lexCase) {
	ctx := context.Background()
	for _, c := range cases {
		t.Run(fmt.Sprintf("%q", c.query), func(t *testing.T) {
			a := ask(ctx, d, c.query)
			c.check(t, a, tokensOf(c.query, dialect))
		})
	}
}

func openFor(t *testing.T, cfg db.Config) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("open %s: %v", cfg.Name, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestIntegrationLexingConformance(t *testing.T) {
	testutil.Integration(t)

	pgCases := []lexCase{
		{"SELECT 1 -- c\r, 2", func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
			columnsAgree(2)(t, a, toks)
			if c := commentTexts(toks); len(c) != 1 || c[0] != "-- c" {
				t.Errorf("want Comment %q ending before \\r, got %s", "-- c", describe(toks))
			}
			if !hasToken(toks, sqltext.Number, "2") {
				t.Errorf("want Number 2, got %s", describe(toks))
			}
		}},
		{"SELECT 1 AS 🙂$a$", columnNamed("🙂$a$", sqltext.Identifier, "🙂$a$")},
		{"SELECT 1 /* a /* b */ c */", oneComment("/* a /* b */ c */")},
		{`SELECT U&'d\0061t'`, valueIsString("dat", `U&'d\0061t'`, sqltext.Postgres, `SELECT U&'d\0061t'`)},
	}
	t.Run("Postgres", func(t *testing.T) {
		runLexCases(t, openFor(t, testutil.PGConfig()), sqltext.Postgres, pgCases)
	})

	t.Run("MySQL", func(t *testing.T) {
		d := openFor(t, db.Config{Name: "mysql", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dgopher", Database: "shop"})
		runLexCases(t, d, sqltext.MySQL, []lexCase{
			{"SELECT 1--1", func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
				if a.err != nil || len(a.row) != 1 || a.row[0] != "2" {
					t.Fatalf("server row %q err %v, want [2]", a.row, a.err)
				}
				if c := commentTexts(toks); len(c) != 0 {
					t.Errorf("want no Comment, got %s", describe(toks))
				}
			}},
			{"SELECT 1 -- c\r, 2", func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
				columnsAgree(1)(t, a, toks)
				if c := commentTexts(toks); len(c) != 1 || c[0] != "-- c\r, 2" {
					t.Errorf("want Comment to end of text, got %s", describe(toks))
				}
			}},
			{"SELECT 1 /* a /* b */ , 2", func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
				columnsAgree(2)(t, a, toks)
				if c := commentTexts(toks); len(c) != 1 || c[0] != "/* a /* b */" {
					t.Errorf("want Comment ending at first */, got %s", describe(toks))
				}
			}},
		})
	})

	t.Run("ClickHouse", func(t *testing.T) {
		d := openFor(t, db.Config{Name: "ch", Engine: db.ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dgopher", Database: "default"})
		runLexCases(t, d, sqltext.ClickHouse, []lexCase{
			{"SELECT 1 #x", func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
				if a.err == nil {
					t.Fatalf("server accepted %q (columns %q); plan assumed an error", "SELECT 1 #x", a.columns)
				}
				if c := commentTexts(toks); len(c) != 0 {
					t.Errorf("server rejects it, so #x is not a comment; got %s", describe(toks))
				}
			}},
			{"SELECT 1 # x", oneComment("# x")},
			{"SELECT 1 /* a /* b */ c */", oneComment("/* a /* b */ c */")},
			{`SELECT 1 AS "x\"y"`, func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
				if a.err != nil {
					t.Fatalf("server error: %v", a.err)
				}
				if !hasToken(toks, sqltext.QuotedIdent, `"x\"y"`) {
					t.Errorf("want one QuotedIdent, got %s", describe(toks))
				}
			}},
			{"SELECT $$a;b$$", valueIsString("a;b", "$$a;b$$", sqltext.ClickHouse, "SELECT $$a;b$$")},
			{"SELECT 1 AS a$b", columnNamed("a$b", sqltext.Identifier, "a$b")},
			{"SELECT 1--1", func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
				if a.err != nil || len(a.row) != 1 || a.row[0] != "1" {
					t.Fatalf("server row %q err %v, want [1]", a.row, a.err)
				}
				if c := commentTexts(toks); len(c) != 1 || c[0] != "--1" {
					t.Errorf("want Comment --1, got %s", describe(toks))
				}
			}},
			{"SELECT 1 AS 1abc", func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
				t.Logf("server: columns %q err %v", a.columns, a.err)
				isIdent := hasToken(toks, sqltext.Identifier, "1abc")
				if a.err == nil && !isIdent {
					t.Errorf("server accepts alias 1abc, our lexer: %s", describe(toks))
				}
				if a.err != nil && isIdent {
					t.Errorf("server rejects 1abc, our lexer makes it an Identifier: %s", describe(toks))
				}
			}},
		})
	})

	t.Run("SQLite", func(t *testing.T) {
		d := openFor(t, db.Config{Name: "lite", Engine: db.SQLite, Database: ":memory:"})
		runLexCases(t, d, sqltext.SQLite, []lexCase{
			{"SELECT 1 AS [a b]", columnNamed("a b", sqltext.QuotedIdent, "[a b]")},
			{"SELECT 1 AS `a b`", columnNamed("a b", sqltext.QuotedIdent, "`a b`")},
			{"SELECT 1 -- c\r, 2", columnsRecorded},
			{"SELECT 1 /* a /* b */ , 2", columnsRecorded},
		})
	})

	t.Run("DuckDB", func(t *testing.T) {
		d := openFor(t, db.Config{Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
		runLexCases(t, d, sqltext.Postgres, []lexCase{
			{"SELECT 1 -- c\r, 2", columnsRecorded},
			{"SELECT 1 /* a /* b */ c */", func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
				t.Logf("server: columns %q err %v", a.columns, a.err)
				nested := len(commentTexts(toks)) == 1 && commentTexts(toks)[0] == "/* a /* b */ c */"
				if (a.err == nil) != nested {
					t.Errorf("server err %v, our tokens: %s", a.err, describe(toks))
				}
			}},
			{"SELECT 1 AS 🙂$a$", func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
				t.Logf("server: columns %q err %v", a.columns, a.err)
				isIdent := hasToken(toks, sqltext.Identifier, "🙂$a$")
				if (a.err == nil && len(a.columns) == 1 && a.columns[0] == "🙂$a$") != isIdent {
					t.Errorf("server columns %q err %v, our tokens: %s", a.columns, a.err, describe(toks))
				}
			}},
			{"SELECT $$a;b$$", func(t *testing.T, a serverAnswer, toks []sqltext.Token) {
				t.Logf("server: row %q err %v", a.row, a.err)
				isString := hasToken(toks, sqltext.String, "$$a;b$$")
				if (a.err == nil && len(a.row) == 1 && a.row[0] == "a;b") != isString {
					t.Errorf("server row %q err %v, our tokens: %s", a.row, a.err, describe(toks))
				}
			}},
		})
	})
}

// TestIntegrationLexingClickHouseNames checks names holding backslashes and
// backticks round-trip through CREATE, SELECT, Maintenance's OPTIMIZE and
// DROP, each of which stays one statement for the server and our lexer.
func TestIntegrationLexingClickHouseNames(t *testing.T) {
	testutil.Integration(t)
	ctx := context.Background()
	d := openFor(t, db.Config{Name: "ch", Engine: db.ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dgopher", Database: "default"})
	dialect := db.DialectOf(db.ClickHouse)
	run := func(t *testing.T, query string) {
		t.Helper()
		if n := len(sqltext.Split(query+";\n", sqltext.ClickHouse)); n != 1 {
			t.Fatalf("%q splits into %d statements", query, n)
		}
		if _, err := d.SQL.ExecContext(ctx, query); err != nil {
			t.Fatalf("%q: %v", query, err)
		}
	}
	const database = `zz_task8_db\`
	run(t, "CREATE DATABASE "+dialect.Quote(database))
	t.Cleanup(func() { d.SQL.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+dialect.Quote(database)) })

	names := []string{"zz_task8_`", `zz_task8_\`, `zz_task8_x\`, `zz_task8_a\\b`, "zz_task8_\n", "zz_task8_\x00", "zz_task8_\t",
		`zz_task8_"`, "zz_task8_'", "zz_task8_ü", "zz_task8_x\\`; SELECT 1; --"}
	for _, name := range names {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			table := db.QualifiedName(dialect, database, name)
			run(t, "CREATE TABLE "+table+" (id UInt8) ENGINE = MergeTree ORDER BY id")
			run(t, "INSERT INTO "+table+" VALUES (7)")
			var got string
			if err := d.SQL.QueryRowContext(ctx, "SELECT name FROM system.tables WHERE database = "+db.Literal(db.ClickHouse, database)+
				" AND name = "+db.Literal(db.ClickHouse, name)).Scan(&got); err != nil || got != name {
				t.Fatalf("server stored %q (%v), want %q", got, err, name)
			}
			var id int
			if err := d.SQL.QueryRowContext(ctx, "SELECT id FROM "+table).Scan(&id); err != nil || id != 7 {
				t.Fatalf("SELECT gave %d (%v)", id, err)
			}
			// As maintenanceCommands (internal/app/nav.go) builds them.
			run(t, "OPTIMIZE TABLE "+table)
			run(t, "OPTIMIZE TABLE "+table+" FINAL")
			run(t, "DROP TABLE "+table)
		})
	}
	var left int
	if err := d.SQL.QueryRowContext(ctx, "SELECT count() FROM system.tables WHERE database = "+db.Literal(db.ClickHouse, database)).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d tables left (%v)", left, err)
	}
}

// TestIntegrationLiteralWithoutStandardStrings checks PostgreSQL literals
// read back as written in a session that turned standard_conforming_strings
// off, which any statement may do.
func TestIntegrationLiteralWithoutStandardStrings(t *testing.T) {
	testutil.Integration(t)
	ctx := context.Background()
	d := openFor(t, testutil.PGConfig())
	conn, err := d.SQL.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET standard_conforming_strings = off"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`it's`, `back\slash`, `\' OR TRUE --`, `trailing\`, `\\`, `''`, `\x41`, "new\nline"} {
		var got string
		if err := conn.QueryRowContext(ctx, "SELECT "+db.Literal(db.Postgres, s)).Scan(&got); err != nil || got != s {
			t.Errorf("%q came back as %q (%v)", s, got, err)
		}
		var matches int
		if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM (VALUES ('a'), ('b')) v(x) WHERE x = "+db.Literal(db.Postgres, s)).Scan(&matches); err != nil || matches != 0 {
			t.Errorf("filter on %q matched %d rows (%v)", s, matches, err)
		}
	}
	b := []byte{0x00, 'A', '\\', 0xff, '\''}
	var got []byte
	if err := conn.QueryRowContext(ctx, "SELECT "+db.Literal(db.Postgres, b)).Scan(&got); err != nil || string(got) != string(b) {
		t.Errorf("% x came back as % x (%v)", b, got, err)
	}
}

package sqltext

import (
	"fmt"
	"reflect"
	"testing"
)

// Code points the servers were found to read as spaces outside strings and
// quoted names (TestIntegrationLexingSpaces in internal/db checks them).
var (
	sqlServerSpaces  = []rune{0x00, 0x01, 0x08, 0x0E, 0x1F, 0x85, 0xA0, 0x1680, 0x2000, 0x2005, 0x200A, 0x200B, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000}
	clickHouseSpaces = []rune{0x85, 0xA0, 0x180E, 0x2000, 0x200A, 0x200B, 0x200C, 0x200D, 0x2028, 0x2029, 0x202F, 0x205F, 0x2060, 0x3000, 0xFEFF}
	duckDBSpaces     = []rune{0xA0, 0x2000, 0x200A, 0x200B, 0x202F, 0x205F, 0x2060, 0x3000, 0xFEFF}
)

func TestServerSpaces(t *testing.T) {
	spaced := []Kind{Keyword, Number}
	for _, r := range sqlServerSpaces {
		t.Run(fmt.Sprintf("TSQL U+%04X", r), func(t *testing.T) {
			src := "SELECT" + string(r) + "1"
			toks := Tokenize(src, TSQL)
			checkCoverage(t, src, toks)
			if got := kinds(toks); !reflect.DeepEqual(got, spaced) {
				t.Errorf("Tokenize(%q) kinds %v, want %v", src, got, spaced)
			}
			if a := Classify("SELECT 1"+string(r)+"DELETE FROM t", TSQL); a.Class != Write {
				t.Errorf("SELECT 1<U+%04X>DELETE FROM t classified %v, want Write", r, a.Class)
			}
			if a := Classify("SELECT 1 AS a"+string(r)+"DELETE FROM t", TSQL); a.Class != Write {
				t.Errorf("a name followed by U+%04X and DELETE classified %v, want Write", r, a.Class)
			}
		})
	}
	for _, r := range []rune{0x7F, 0x180E, 0x200C, 0xFEFF} {
		if toks := Tokenize("SELECT 1 AS a"+string(r), TSQL); toks[len(toks)-1].Kind == Whitespace {
			t.Errorf("TSQL U+%04X read as a space; SQL Server does not", r)
		}
	}
	// SQL Server runs a batch with no ASCII space in it, as SELECT<U+0001>1,
	// as a call of the procedure the whole text names.
	for _, src := range []string{"SELECT\x011", "SELECT\u200b1", "SELECT 1 AS a\x01"} {
		if a := Classify(src, TSQL); a.Class != Write {
			t.Errorf("Classify(%q, TSQL) = %v, want Write", src, a.Class)
		}
	}
	if a := Classify("SELECT 1 AS a\u00a0", TSQL); a.Class != Read {
		t.Errorf("a no-break space after a name classified %v, want Read", a.Class)
	}

	for _, r := range clickHouseSpaces {
		t.Run(fmt.Sprintf("ClickHouse U+%04X", r), func(t *testing.T) {
			src := "SELECT" + string(r) + "1"
			if got := kinds(Tokenize(src, ClickHouse)); !reflect.DeepEqual(got, spaced) {
				t.Errorf("Tokenize(%q) kinds %v, want %v", src, got, spaced)
			}
			if got := kinds(Tokenize("SELECT a"+string(r)+"b", ClickHouse)); !reflect.DeepEqual(got, []Kind{Keyword, Identifier, Identifier}) {
				t.Errorf("a name does not end at U+%04X: %v", r, got)
			}
		})
	}
	if got := kinds(Tokenize("SELECT\u16801", ClickHouse)); !reflect.DeepEqual(got, []Kind{Identifier}) {
		t.Errorf("ClickHouse U+1680 read as a space: %v", got)
	}

	// SQLite reads a byte order mark as a space where a token starts, and
	// as part of a name or number after one.
	if got := kinds(Tokenize("SELECT 1, \ufeff1", SQLite)); !reflect.DeepEqual(got, []Kind{Keyword, Number, Punct, Number}) {
		t.Errorf("SQLite byte order mark at a token start: %v", got)
	}
	if got := kinds(Tokenize("SELECT\ufeff1", SQLite)); !reflect.DeepEqual(got, []Kind{Identifier}) {
		t.Errorf("SQLite byte order mark after a word: %v", got)
	}

	// DuckDB reads these as spaces, PostgreSQL as part of a name, and both
	// are lexed as Postgres: a statement holding one outside quotes is a
	// write.
	for _, r := range duckDBSpaces {
		for _, src := range []string{
			"WITH x AS (SELECT 1)" + string(r) + "DELETE FROM t",
			"EXPLAIN ANALYZE" + string(r) + "DELETE FROM t",
			"SELECT 1 AS a" + string(r) + "b",
		} {
			if a := Classify(src, Postgres); a.Class != Write {
				t.Errorf("Classify(%q, Postgres) = %v, want Write", src, a.Class)
			}
		}
	}
	for _, src := range []string{"SELECT '\u00a0'", `SELECT 1 AS "a` + "\u00a0" + `b"`, "SELECT 1 -- \u00a0", "SELECT $$\u3000$$"} {
		if a := Classify(src, Postgres); a.Class != Read {
			t.Errorf("Classify(%q, Postgres) = %v, want Read", src, a.Class)
		}
	}
	// MySQL, MariaDB and TiDB read none of them as spaces.
	if a := Classify("SELECT 1 AS a\u00a0b", MySQL); a.Class != Read {
		t.Errorf("MySQL name holding U+00A0 classified %v, want Read", a.Class)
	}
}

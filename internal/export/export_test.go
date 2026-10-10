package export

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"dgopher/internal/db"
)

var (
	sampleTime    = time.Date(2026, 10, 8, 12, 30, 45, 123456789, time.UTC)
	sampleColumns = []string{"null", "text", "bin", "ts", "i", "u", "f", "b", "nested"}
	sampleRow     = []any{nil, []byte("héllo"), []byte{0xff, 0x00, 0x01}, sampleTime, int64(-42), uint8(7), 3.5, true, map[string]any{"a": []any{1, "x"}}}
)

func render(t *testing.T, f Format, cols []string, rows [][]any, opt Options) string {
	t.Helper()
	s, err := Text(f, cols, rows, opt)
	if err != nil {
		t.Fatalf("%s: %v", f, err)
	}
	return s
}

func TestFormatMetadata(t *testing.T) {
	want := []struct{ ext, label string }{{"csv", "CSV"}, {"tsv", "TSV"}, {"json", "JSON"}, {"jsonl", "JSON Lines"}, {"sql", "SQL INSERT"}, {"md", "Markdown"}, {"xlsx", "Excel workbook"}, {"parquet", "Parquet"}, {"duckdb", "DuckDB database"}}
	fs := Formats()
	if len(fs) != len(want) {
		t.Fatalf("Formats() = %v", fs)
	}
	for i, f := range fs {
		if f.Extension() != want[i].ext || f.Label() != want[i].label {
			t.Errorf("%d: %q %q", i, f.Extension(), f.Label())
		}
	}
	if _, err := NewWriter(&bytes.Buffer{}, Format("xml"), nil, Options{}); err == nil {
		t.Error("unknown format accepted")
	}
}

func TestCSVRoundTrip(t *testing.T) {
	out := render(t, CSV, sampleColumns, [][]any{sampleRow, {"a,b", "q\"t", "line\nbreak", nil, 0, 0, 0.0, false, []int{1}}}, Options{Header: true, NullText: "NULL"})
	recs, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"NULL", "héllo", `\xff0001`, "2026-10-08T12:30:45.123456789Z", "-42", "7", "3.5", "true", `{"a":[1,"x"]}`}
	if strings.Join(recs[0], "|") != strings.Join(sampleColumns, "|") || strings.Join(recs[1], "|") != strings.Join(want, "|") {
		t.Errorf("got %q", recs)
	}
	if recs[2][0] != "a,b" || recs[2][1] != `q"t` || recs[2][2] != "line\nbreak" || recs[2][8] != "[1]" {
		t.Errorf("row 2 %q", recs[2])
	}
}

func TestTSV(t *testing.T) {
	out := render(t, TSV, []string{"a", "b", "c"}, [][]any{{"x\ty", "1\n2", `back\slash`}, {nil, true, 1.25}}, Options{Header: true})
	want := "a\tb\tc\nx\\ty\t1\\n2\tback\\\\slash\n\ttrue\t1.25\n"
	if out != want {
		t.Errorf("got %q", out)
	}
	out = render(t, TSV, sampleColumns, [][]any{sampleRow}, Options{})
	if strings.Count(out, "\n") != 1 || strings.Count(out, "\t") != len(sampleColumns)-1 {
		t.Errorf("row not one line: %q", out)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	out := render(t, JSON, sampleColumns, [][]any{sampleRow, sampleRow}, Options{})
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r := got[0]
	if len(got) != 2 || r["null"] != nil || r["text"] != "héllo" || r["bin"] != `\xff0001` || r["ts"] != "2026-10-08T12:30:45.123456789Z" ||
		r["i"] != -42.0 || r["u"] != 7.0 || r["f"] != 3.5 || r["b"] != true {
		t.Errorf("got %#v", r)
	}
	if nested, ok := r["nested"].(map[string]any); !ok || nested["a"].([]any)[1] != "x" {
		t.Errorf("nested %#v", r["nested"])
	}
	if strings.Index(out, `"null"`) > strings.Index(out, `"nested"`) {
		t.Error("column order not preserved")
	}
}

func TestJSONSpecialsAndDuplicates(t *testing.T) {
	out := render(t, JSON, []string{"a", "a", "a", "a_2"}, [][]any{{math.NaN(), math.Inf(1), json.RawMessage(`{"k":1}`), float32(1.5)}}, Options{})
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r := got[0]
	if r["a"] != "NaN" || r["a_2"] != "+Inf" || r["a_3"].(map[string]any)["k"] != 1.0 || r["a_2_2"] != 1.5 {
		t.Errorf("got %#v", r)
	}
}

func TestJSONLines(t *testing.T) {
	out := render(t, JSONLines, sampleColumns, [][]any{sampleRow, sampleRow}, Options{})
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %q", out)
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil || m["i"] != -42.0 {
			t.Errorf("%v %#v", err, m)
		}
	}
}

func TestEmpty(t *testing.T) {
	if s := render(t, JSON, []string{"a"}, nil, Options{}); strings.TrimSpace(s) != "[]" {
		t.Errorf("json %q", s)
	}
	if s := render(t, CSV, []string{"a", "b"}, nil, Options{Header: true}); s != "a,b\n" {
		t.Errorf("csv %q", s)
	}
	if s := render(t, JSONLines, []string{"a"}, nil, Options{}); s != "" {
		t.Errorf("jsonl %q", s)
	}
	if s := render(t, SQL, []string{"a"}, nil, Options{}); s != "" {
		t.Errorf("sql %q", s)
	}
	if s := render(t, Markdown, []string{"a"}, nil, Options{}); s != "| a |\n| --- |\n" {
		t.Errorf("md %q", s)
	}
}

func TestMarkdown(t *testing.T) {
	out := render(t, Markdown, []string{"a|b", "c"}, [][]any{{"x|y", "1\n2"}, {nil, 5}}, Options{NullText: "NULL"})
	want := "| a\\|b | c |\n| --- | --- |\n| x\\|y | 1<br>2 |\n| NULL | 5 |\n"
	if out != want {
		t.Errorf("got %q", out)
	}
	out = render(t, Markdown, sampleColumns, [][]any{sampleRow}, Options{})
	if !strings.Contains(out, `\xff0001`) || !strings.Contains(out, "2026-10-08T12:30:45.123456789Z") {
		t.Errorf("got %q", out)
	}
}

func TestSQLValues(t *testing.T) {
	// sampleRow's text column is bytes, which come only from a binary
	// column, so it is written as bytes too.
	cases := map[db.Engine][2]string{
		"":            {`X'68c3a96c6c6f'`, `X'ff0001'`},
		db.Postgres:   {`E'\\x68c3a96c6c6f'::bytea`, `E'\\xff0001'::bytea`},
		db.MySQL:      {`X'68c3a96c6c6f'`, `X'ff0001'`},
		db.ClickHouse: {`unhex('68c3a96c6c6f')`, `unhex('ff0001')`},
	}
	for lit, want := range cases {
		text, bin := want[0], want[1]
		out := render(t, SQL, sampleColumns, [][]any{sampleRow}, Options{Engine: lit})
		want := `INSERT INTO "exported" ("null", "text", "bin", "ts", "i", "u", "f", "b", "nested") VALUES (NULL, ` + text + `, ` + bin +
			`, '2026-10-08T12:30:45.123456789Z', -42, 7, 3.5, TRUE, '{"a":[1,"x"]}');` + "\n"
		if out != want {
			t.Errorf("%q:\n got %s\nwant %s", lit, out, want)
		}
	}
	out := render(t, SQL, []string{"we\"ird"}, [][]any{{1}}, Options{Table: "t", Quote: func(s string) string { return "`" + s + "`" }})
	if out != "INSERT INTO `t` (`we\"ird`) VALUES (1);\n" {
		t.Errorf("got %q", out)
	}
	if out := render(t, SQL, []string{"a"}, [][]any{{"we\"ird"}}, Options{}); !strings.Contains(out, `("a")`) {
		t.Errorf("got %q", out)
	}
	if out := render(t, SQL, []string{`a"b`}, [][]any{{math.NaN()}}, Options{}); out != `INSERT INTO "exported" ("a""b") VALUES ('NaN');`+"\n" {
		t.Errorf("got %q", out)
	}
}

// unquote parses a single-quoted literal under style's escaping rules and reports
// the decoded value and the rest after the closing quote.
func unquote(t *testing.T, s string, style db.Engine) (string, string) {
	t.Helper()
	if !strings.HasPrefix(s, "'") && !strings.HasPrefix(s, "E'") {
		t.Fatalf("not quoted: %q", s)
	}
	backslash := style == db.MySQL || style == db.ClickHouse
	if strings.HasPrefix(s, "E'") {
		s, backslash = s[1:], true
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case backslash && c == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case '0':
				b.WriteByte(0)
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 'Z':
				b.WriteByte(0x1a)
			default:
				b.WriteByte(s[i])
			}
		case c == '\'' && i+1 < len(s) && s[i+1] == '\'':
			b.WriteByte('\'')
			i++
		case c == '\'':
			return b.String(), s[i+1:]
		default:
			b.WriteByte(c)
		}
	}
	t.Fatalf("unterminated: %q", s)
	return "", ""
}

var hostileStrings = []string{
	`'); DROP TABLE x; --`,
	`it's`,
	`\`,
	`\'`,
	`\\'; DROP TABLE x; --`,
	"nul\x00byte",
	"line\nbreak\r\n",
	"ctrl-z\x1a",
	`''''`,
	`end\`,
}

func TestSQLHostileStrings(t *testing.T) {
	for _, lit := range []db.Engine{db.SQLite, db.Postgres, db.MySQL, db.ClickHouse} {
		for _, h := range hostileStrings {
			{
				out := render(t, SQL, []string{"c"}, [][]any{{h}}, Options{Engine: lit})
				prefix := `INSERT INTO "exported" ("c") VALUES (`
				// PostgreSQL text cannot hold NUL, nor can standard literals,
				// SQLite's: it is written as chr(0), char(0).
				if strings.ContainsRune(h, 0) && (lit == db.Postgres || lit == db.SQLite) {
					if out != prefix+db.Literal(lit, h)+");\n" {
						t.Errorf("got %q", out)
					}
					continue
				}
				if !strings.HasPrefix(out, prefix) {
					t.Fatalf("got %q", out)
				}
				got, rest := unquote(t, strings.TrimPrefix(out, prefix), lit)
				if got != h || rest != ");\n" {
					t.Errorf("literal %q, %q: decoded %q rest %q", lit, h, got, rest)
				}
				if lit == db.MySQL && (strings.ContainsRune(out, 0) || strings.Contains(out, "\n)")) {
					t.Errorf("mysql raw control byte in %q", out)
				}
				if escaped := strings.HasPrefix(out, prefix+"E'"); escaped != (lit == db.Postgres && strings.Contains(h, `\`)) {
					t.Errorf("literal %q, %q: %q", lit, h, out)
				}
			}
		}
	}
}

// TestBytesExportAsBytes checks bytes are written in each engine's binary
// form even when they are valid UTF-8: the cursor makes text columns
// strings, so bytes come from a binary column, and text there would load
// as other bytes (PostgreSQL reads '\x41' into a bytea as "A").
func TestBytesExportAsBytes(t *testing.T) {
	values := [][]byte{[]byte(`\x41`), []byte(`a\b`), []byte(`\\`), []byte(`\101`), []byte("plain")}
	for _, v := range values {
		h := fmt.Sprintf("%x", v)
		want := map[db.Engine]string{
			"":            `X'` + h + `'`,
			db.Postgres:   db.Literal(db.Postgres, v),
			db.MySQL:      `X'` + h + `'`,
			db.ClickHouse: `unhex('` + h + `')`,
			db.DuckDB:     db.Literal(db.DuckDB, v),
		}
		for lit, w := range want {
			if got := sqlLiteral(v, lit); got != w {
				t.Errorf("literal %q, %q: %s, want %s", lit, v, got, w)
			}
		}
	}
}

// TestMySQLExportDoublesQuotes checks a MySQL dump's strings end where
// they should under NO_BACKSLASH_ESCAPES too, where a backslash is text and
// only a doubled quote escapes a quote.
func TestMySQLExportDoublesQuotes(t *testing.T) {
	prefix := `INSERT INTO "exported" ("c") VALUES (`
	if out := render(t, SQL, []string{"c"}, [][]any{{"it's"}}, Options{Engine: db.MySQL}); out != prefix+`'it''s');`+"\n" {
		t.Errorf("got %q", out)
	}
	for _, h := range hostileStrings {
		out := render(t, SQL, []string{"c"}, [][]any{{h}}, Options{Engine: db.MySQL})
		if _, rest := unquote(t, strings.TrimPrefix(out, prefix), db.SQLite); rest != ");\n" {
			t.Errorf("%q: %q ends early, leaving %q", h, out, rest)
		}
	}
}

// TestDuckDBBytea checks a DuckDB dump loads back into DuckDB as the same
// strings and bytes.
func TestDuckDBBytea(t *testing.T) {
	conn, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`CREATE TABLE t (id INTEGER, s VARCHAR, b BLOB)`); err != nil {
		t.Fatal(err)
	}
	values := append([]string{"héllo", "A\\", string([]byte{0x41, 0x5c, 0xff, 0x00, '\''})}, hostileStrings...)
	var rows [][]any
	for i, v := range values {
		s := any(v)
		if !utf8.ValidString(v) {
			s = nil
		}
		rows = append(rows, []any{i, s, []byte(v)})
	}
	script := render(t, SQL, []string{"id", "s", "b"}, rows, Options{Table: "t", Engine: db.DuckDB})
	if _, err := conn.Exec(script); err != nil {
		t.Fatalf("%v\n%s", err, script)
	}
	got, err := conn.Query(`SELECT s, b FROM t ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	i := 0
	for ; got.Next(); i++ {
		var s sql.NullString
		var b []byte
		if err := got.Scan(&s, &b); err != nil {
			t.Fatal(err)
		}
		if want := rows[i][1]; s.Valid != (want != nil) || s.Valid && s.String != want {
			t.Errorf("row %d: string %q, want %q", i, s.String, want)
		}
		if !bytes.Equal(b, []byte(values[i])) {
			t.Errorf("row %d: bytes % x, want % x", i, b, values[i])
		}
	}
	if err := got.Err(); err != nil || i != len(rows) {
		t.Errorf("%d rows back of %d (%v)", i, len(rows), err)
	}
}

func TestRowLengthMismatch(t *testing.T) {
	for _, f := range Formats() {
		if NeedsFile(f) {
			continue
		}
		w, err := NewWriter(&bytes.Buffer{}, f, []string{"a", "b"}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Write([]any{1}); err == nil {
			t.Errorf("%s accepted short row", f)
		}
	}
}

type stringerValue struct{}

func (stringerValue) String() string { return "custom" }

func TestStringerAndPointers(t *testing.T) {
	s := "ptr"
	var nilPtr *int
	out := render(t, CSV, []string{"a", "b", "c", "d"}, [][]any{{stringerValue{}, &s, nilPtr, struct{ X int }{1}}}, Options{NullText: "N"})
	if out != "custom,ptr,N,{1}\n" {
		t.Errorf("got %q", out)
	}
	out = render(t, JSON, []string{"a"}, [][]any{{stringerValue{}}}, Options{})
	if !strings.Contains(out, `"custom"`) {
		t.Errorf("got %q", out)
	}
}

// The INSERT target is quoted with the dialect's quoting: a hostile table
// name stays one name.
func TestInsertTargetQuoted(t *testing.T) {
	hostile := "t (a) VALUES (1); DROP TABLE users; --"
	for _, c := range []struct {
		engine db.Engine
		schema string
		want   string
	}{
		{db.Postgres, "s", `INSERT INTO "s"."t (a) VALUES (1); DROP TABLE users; --" ("a") VALUES`},
		{db.MySQL, "", "INSERT INTO `t (a) VALUES (1); DROP TABLE users; --` (`a`) VALUES"},
		{db.ClickHouse, "d", "INSERT INTO `d`.`t (a) VALUES (1); DROP TABLE users; --` (`a`) VALUES"},
		{db.SQLite, "", `INSERT INTO "t (a) VALUES (1); DROP TABLE users; --" ("a") VALUES`},
	} {
		out := render(t, SQL, []string{"a"}, [][]any{{int64(1)}}, Options{Schema: c.schema, Table: hostile, Quote: db.DialectOf(c.engine).Quote})
		if !strings.HasPrefix(out, c.want) {
			t.Errorf("%s: %q", c.engine, out)
		}
	}
	if out := render(t, SQL, []string{"a"}, [][]any{{int64(1)}}, Options{Table: `x"y`}); !strings.HasPrefix(out, `INSERT INTO "x""y" ("a")`) {
		t.Errorf("default quoting: %q", out)
	}
}

func TestFormulaGuard(t *testing.T) {
	row := []any{"=1+1", "+a", "-b", "@c", "\tt", "\rr", "\nn", "＝x", "＋x", "－x", "＠x", "plain", int64(-5), -2.5, []byte("=b"), nil}
	cols := make([]string, len(row))
	for i := range cols {
		cols[i] = fmt.Sprint("c", i)
	}
	for _, f := range []Format{CSV, TSV} {
		off := render(t, f, cols, [][]any{row}, Options{})
		if strings.Contains(off, "'") {
			t.Errorf("%s: guarded by default: %q", f, off)
		}
		on := render(t, f, cols, [][]any{row}, Options{FormulaGuard: true})
		for _, want := range []string{"'=1+1", "'+a", "'-b", "'@c", "'＝x", "'＋x", "'－x", "'＠x"} {
			if !strings.Contains(on, want) {
				t.Errorf("%s: no %q in %q", f, want, on)
			}
		}
		if strings.Contains(on, "'-5") || strings.Contains(on, "'-2.5") || strings.Contains(on, "'plain") {
			t.Errorf("%s: a number or plain text changed: %q", f, on)
		}
		if strings.Count(on, "'") != 12 {
			t.Errorf("%s: %d guards in %q", f, strings.Count(on, "'"), on)
		}
	}
	quoted := render(t, CSV, []string{"a"}, [][]any{{"=x"}}, Options{FormulaGuard: true, QuoteChar: '\''})
	if !strings.Contains(quoted, "'''=x'") {
		t.Errorf("custom quote: %q", quoted)
	}
}

func TestMarkdownEscapes(t *testing.T) {
	out := render(t, Markdown, []string{"a"}, [][]any{{`<img src=x onerror=alert(1)> & \|`}}, Options{})
	want := "| a |\n| --- |\n| &lt;img src=x onerror=alert(1)> &amp; \\\\\\| |\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

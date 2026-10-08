package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
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
	cases := map[Literal]string{
		LiteralStandard:   `X'ff0001'`,
		LiteralPostgres:   `'\xff0001'::bytea`,
		LiteralMySQL:      `X'ff0001'`,
		LiteralClickHouse: `unhex('ff0001')`,
	}
	for lit, bin := range cases {
		out := render(t, SQL, sampleColumns, [][]any{sampleRow}, Options{Literal: lit})
		want := `INSERT INTO exported ("null", "text", "bin", "ts", "i", "u", "f", "b", "nested") VALUES (NULL, 'héllo', ` + bin +
			`, '2026-10-08T12:30:45.123456789Z', -42, 7, 3.5, TRUE, '{"a":[1,"x"]}');` + "\n"
		if out != want {
			t.Errorf("%d:\n got %s\nwant %s", lit, out, want)
		}
	}
	out := render(t, SQL, []string{"we\"ird"}, [][]any{{1}}, Options{Table: "`t`", Quote: func(s string) string { return "`" + s + "`" }})
	if out != "INSERT INTO `t` (`we\"ird`) VALUES (1);\n" {
		t.Errorf("got %q", out)
	}
	if out := render(t, SQL, []string{"a"}, [][]any{{"we\"ird"}}, Options{}); !strings.Contains(out, `("a")`) {
		t.Errorf("got %q", out)
	}
	if out := render(t, SQL, []string{`a"b`}, [][]any{{math.NaN()}}, Options{}); out != `INSERT INTO exported ("a""b") VALUES ('NaN');`+"\n" {
		t.Errorf("got %q", out)
	}
}

// unquote parses a single-quoted literal under style's escaping rules and reports
// the decoded value and the rest after the closing quote.
func unquote(t *testing.T, s string, style Literal) (string, string) {
	t.Helper()
	if s == "" || s[0] != '\'' {
		t.Fatalf("not quoted: %q", s)
	}
	backslash := style == LiteralMySQL || style == LiteralClickHouse
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

func TestSQLHostileStrings(t *testing.T) {
	hostile := []string{
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
	for _, lit := range []Literal{LiteralStandard, LiteralPostgres, LiteralMySQL, LiteralClickHouse} {
		for _, h := range hostile {
			for _, v := range []any{h, []byte(h)} {
				out := render(t, SQL, []string{"c"}, [][]any{{v}}, Options{Literal: lit})
				prefix := `INSERT INTO exported ("c") VALUES (`
				if !strings.HasPrefix(out, prefix) {
					t.Fatalf("got %q", out)
				}
				got, rest := unquote(t, strings.TrimPrefix(out, prefix), lit)
				if got != h || rest != ");\n" {
					t.Errorf("literal %d, %q: decoded %q rest %q", lit, h, got, rest)
				}
				if lit == LiteralMySQL && (strings.ContainsRune(out, 0) || strings.Contains(out, "\n)")) {
					t.Errorf("mysql raw control byte in %q", out)
				}
			}
		}
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

package export

import (
	"bytes"
	"database/sql"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

func TestCSVOptions(t *testing.T) {
	out := render(t, CSV, []string{"a", "b"}, [][]any{{"x;y", nil}, {"it's", 1}}, Options{Header: true, Delimiter: ';', QuoteChar: '\'', QuoteAlways: true, NullText: `\N`, BOM: true})
	want := "\ufeff'a';'b'\n'x;y';'\\N'\n'it''s';'1'\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
	plain := render(t, CSV, []string{"a"}, [][]any{{"p;q"}, {"r"}}, Options{Delimiter: ';', QuoteChar: '\''})
	if plain != "'p;q'\nr\n" {
		t.Errorf("minimal quoting %q", plain)
	}
	if _, err := Text(CSV, []string{"a"}, nil, Options{Delimiter: '\'', QuoteChar: '\''}); err == nil {
		t.Error("delimiter equal to quote accepted")
	}
	for _, f := range []Format{TSV, JSON, JSONLines, SQL, Markdown} {
		if s := render(t, f, []string{"a"}, [][]any{{1}}, Options{BOM: true}); !strings.HasPrefix(s, "\ufeff") {
			t.Errorf("%s: no BOM in %q", f, s)
		}
	}
}

func TestSQLRowsPerInsert(t *testing.T) {
	out := render(t, SQL, []string{"a", "b"}, [][]any{{1, "x"}, {2, nil}, {3, "z"}}, Options{Table: "t", RowsPerInsert: 2})
	want := "INSERT INTO t (\"a\", \"b\") VALUES\n  (1, 'x'),\n  (2, NULL);\n" +
		"INSERT INTO t (\"a\", \"b\") VALUES\n  (3, 'z');\n"
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
	if strings.Count(out, "INSERT") != 2 {
		t.Error("want 2 INSERT statements")
	}
}

func TestNewWriterRefusesFileFormats(t *testing.T) {
	for _, f := range []Format{XLSX, Parquet, DuckDBFile} {
		if !NeedsFile(f) {
			t.Errorf("%s: NeedsFile false", f)
		}
		if _, err := NewWriter(&bytes.Buffer{}, f, []string{"a"}, Options{}); err == nil || !strings.Contains(err.Error(), "file") {
			t.Errorf("%s: err %v", f, err)
		}
	}
	for _, f := range []Format{CSV, TSV, JSON, JSONLines, SQL, Markdown} {
		if NeedsFile(f) {
			t.Errorf("%s: NeedsFile true", f)
		}
	}
}

var fileColumns = []Column{{"i", "int4"}, {"d", "NUMERIC(10,2)"}, {"s", "text"}, {"n", "BIGINT"}, {"ts", "timestamp"}, {"b", "bytea"}, {"m", "jsonb"}}

func fileRows() [][]any {
	return [][]any{
		{int32(7), "12.34", "hello", nil, sampleTime, []byte{0xff, 0x01}, map[string]any{"k": 1}},
		{int64(-1), []byte("0.5"), "", int64(9), sampleTime.Add(time.Hour), []byte("ab"), []any{"x"}},
	}
}

func writeFile(t *testing.T, path string, f Format, cols []Column, rows [][]any, opt Options) {
	t.Helper()
	w, err := NewFileWriter(path, f, cols, opt)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func readBack(t *testing.T, dsn, query string) (types []string, rows [][]any) {
	t.Helper()
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rs, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	cts, _ := rs.ColumnTypes()
	for _, c := range cts {
		types = append(types, c.DatabaseTypeName())
	}
	for rs.Next() {
		vals := make([]any, len(cts))
		ptrs := make([]any, len(cts))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, vals)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return types, rows
}

func checkFileRows(t *testing.T, types []string, rows [][]any) {
	t.Helper()
	wantTypes := "BIGINT,DECIMAL(10,2),VARCHAR,BIGINT,TIMESTAMP,BLOB"
	if got := strings.Join(types[:6], ","); got != wantTypes {
		t.Errorf("types %s, want %s", got, wantTypes)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d", len(rows))
	}
	r := rows[0]
	if r[0] != int64(7) || r[2] != "hello" || r[3] != nil {
		t.Errorf("row 0 %#v", r)
	}
	if d, ok := r[1].(interface{ Float64() float64 }); !ok || d.Float64() != 12.34 {
		t.Errorf("decimal %#v", r[1])
	}
	if ts, ok := r[4].(time.Time); !ok || !ts.Equal(sampleTime.Truncate(time.Microsecond)) {
		t.Errorf("timestamp %#v", r[4])
	}
	if b, ok := r[5].([]byte); !ok || !bytes.Equal(b, []byte{0xff, 0x01}) {
		t.Errorf("blob %#v", r[5])
	}
	if s := text(r[6]); !strings.Contains(s, `"k"`) {
		t.Errorf("json %#v", r[6])
	}
	if rows[1][3] != int64(9) || rows[1][2] != "" {
		t.Errorf("row 1 %#v", rows[1])
	}
}

func TestParquetRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out's.parquet")
	writeFile(t, path, Parquet, fileColumns, fileRows(), Options{})
	types, rows := readBack(t, "", "SELECT * FROM read_parquet('"+strings.ReplaceAll(path, "'", "''")+"')")
	checkFileRows(t, types, rows)
}

func TestDuckDBFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.duckdb")
	writeFile(t, path, DuckDBFile, fileColumns, fileRows(), Options{Table: "my table"})
	types, rows := readBack(t, path, `SELECT * FROM "my table"`)
	checkFileRows(t, types, rows)
	if _, err := NewFileWriter(path, DuckDBFile, fileColumns, Options{}); err == nil {
		t.Error("existing DuckDB file accepted")
	}
}

func TestTypeDowngrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.duckdb")
	cols := []Column{{"n", "BIGINT"}, {"big", "numeric"}, {"x", ""}}
	huge, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	writeFile(t, path, DuckDBFile, cols, [][]any{{"abc", 1.5, huge}, {int64(5), 2.0, nil}}, Options{})
	types, rows := readBack(t, path, `SELECT * FROM "export"`)
	if strings.Join(types, ",") != "VARCHAR,DECIMAL(38,9),VARCHAR" {
		t.Errorf("types %v", types)
	}
	if rows[0][0] != "abc" || rows[1][0] != "5" || rows[0][2] != huge.String() {
		t.Errorf("rows %#v", rows)
	}
}

func TestFileWriterRemovesPartialOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.duckdb")
	w, err := NewFileWriter(path, DuckDBFile, []Column{{"n", "BIGINT"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write([]any{int64(1)}); err != nil {
		t.Fatal(err)
	}
	if err := w.Write([]any{"not a number"}); err == nil {
		t.Error("mismatched later value accepted")
	}
	if err := w.Close(); err == nil {
		t.Error("close after failed write succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("partial file left: %v", err)
	}
}

func TestStreamingFileWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "o.csv")
	writeFile(t, path, CSV, []Column{{Name: "a"}}, [][]any{{1}}, Options{Header: true})
	b, _ := os.ReadFile(path)
	if string(b) != "a\n1\n" {
		t.Errorf("%q", b)
	}
}

// Exported rows may be private: every format's file is the user's alone.
func TestExportFilesArePrivate(t *testing.T) {
	for _, f := range Formats() {
		path := filepath.Join(t.TempDir(), "out."+f.Extension())
		w, err := NewFileWriter(path, f, []Column{{Name: "a", DatabaseType: "INTEGER"}}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]any{int64(1)})
		if err := w.Close(); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v %v", f, info.Mode().Perm(), err)
		}
	}
}

func TestDecimalsStayExact(t *testing.T) {
	for in, want := range map[string]duckColumn{
		"NUMERIC":       {kind: kindDecimal, width: 38, scale: 9},
		"decimal":       {kind: kindDecimal, width: 38, scale: 9},
		"decimal(10)":   {kind: kindDecimal, width: 10, scale: 0},
		"numeric(12,2)": {kind: kindDecimal, width: 12, scale: 2},
		"numeric(50,2)": {kind: kindVarchar},
	} {
		if got := duckType(in); got != want {
			t.Errorf("%s: %+v, want %+v", in, got, want)
		}
	}
}

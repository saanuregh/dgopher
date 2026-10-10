package fileimport

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"dgopher/internal/export"
)

func write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// open reads a file, and checks its columns.
func open(t *testing.T, path string, opt Options, want ...Column) *File {
	t.Helper()
	f, err := Open(context.Background(), path, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if !reflect.DeepEqual(f.Columns, want) {
		t.Fatalf("columns %+v, want %+v", f.Columns, want)
	}
	return f
}

func rows(t *testing.T, f *File) [][]any {
	t.Helper()
	var out [][]any
	n, err := f.Read(context.Background(), 2, func(batch [][]any) error {
		out = append(out, batch...)
		return nil
	})
	if err != nil || n != int64(len(out)) {
		t.Fatalf("read %d of %d: %v", len(out), n, err)
	}
	return out
}

func TestCSV(t *testing.T) {
	path := write(t, "people.csv", "id;name;born\n1;Ada;1815-12-10\n2;Grace;1906-12-09\n3;;1912-06-23\n")
	f := open(t, path, Options{}, Column{"id", "BIGINT"}, Column{"name", "VARCHAR"}, Column{"born", "DATE"})
	if f.Delimiter != ';' || !f.HasHeader {
		t.Fatalf("detected %q, header %v", f.Delimiter, f.HasHeader)
	}
	got := rows(t, f)
	if len(got) != 3 || got[0][1] != "Ada" || got[2][1] != nil {
		t.Fatalf("rows %v", got)
	}
	if n, err := f.Count(context.Background()); err != nil || n != 3 {
		t.Fatalf("count %d: %v", n, err)
	}
	// As text, and with the first row as data.
	f = open(t, path, Options{AllText: true, Header: HeaderNone},
		Column{"column0", "VARCHAR"}, Column{"column1", "VARCHAR"}, Column{"column2", "VARCHAR"})
	if got := rows(t, f); len(got) != 4 || got[0][0] != "id" {
		t.Fatalf("as text: %v", got)
	}
}

// DuckDB reads *, ? and [ in a path as a pattern: such a name is read
// as it is.
func TestGlobCharactersInName(t *testing.T) {
	path := write(t, "data[1]*.csv", "a\n1\n")
	f := open(t, path, Options{}, Column{"a", "BIGINT"})
	if got := rows(t, f); len(got) != 1 {
		t.Fatalf("rows %v", got)
	}
	link := f.link
	f.Close()
	if _, err := os.Stat(link); !os.IsNotExist(err) {
		t.Fatalf("the link stays: %v", err)
	}
}

func TestJSON(t *testing.T) {
	path := write(t, "orders.json", `[{"id": 1, "total": 9.5, "tags": ["a", "b"], "customer": {"name": "Ada"}}, {"id": 2, "total": 3, "tags": [], "customer": null}]`)
	f := open(t, path, Options{}, Column{"id", "BIGINT"}, Column{"total", "DOUBLE"}, Column{"tags", "JSON"}, Column{"customer", "JSON"})
	got := rows(t, f)
	if got[0][2] != `["a","b"]` || got[0][3] != `{"name":"Ada"}` || got[1][3] != nil {
		t.Fatalf("rows %v", got)
	}
	lines := write(t, "events.jsonl", "{\"kind\": \"login\"}\n{\"kind\": \"logout\"}\n")
	if got := rows(t, open(t, lines, Options{}, Column{"kind", "VARCHAR"})); len(got) != 2 || got[1][0] != "logout" {
		t.Fatalf("lines %v", got)
	}
}

func TestParquet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "n.parquet")
	d, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec("COPY (SELECT range AS n, range * 1.5 AS x FROM range(5)) TO '" + path + "' (FORMAT PARQUET)"); err != nil {
		t.Fatal(err)
	}
	f := open(t, path, Options{}, Column{"n", "BIGINT"}, Column{"x", "DECIMAL(21,1)"})
	// A decimal is read as its digits, which a float could round.
	if got := rows(t, f); len(got) != 5 || got[4][1] != "6.0" {
		t.Fatalf("rows %v", got)
	}
}

func TestExcel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sheet.xlsx")
	w, err := export.NewFileWriter(path, export.XLSX, []export.Column{{Name: "id", DatabaseType: "int"}, {Name: "price", DatabaseType: "double"},
		{Name: "paid", DatabaseType: "bool"}, {Name: "day", DatabaseType: "date"}, {Name: "at", DatabaseType: "timestamp"}, {Name: "note", DatabaseType: "text"}},
		export.Options{Header: true, Table: "Orders"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2024, 3, 1, 9, 30, 15, 0, time.UTC)
	w.Write([]any{int64(1), 9.75, true, at.Truncate(24 * time.Hour), at, "tab\tand <tag>"})
	w.Write([]any{int64(2), nil, false, at.Truncate(24*time.Hour).AddDate(0, 0, 1), at.Add(time.Hour), nil})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := open(t, path, Options{}, Column{"id", "BIGINT"}, Column{"price", "DOUBLE"}, Column{"paid", "BOOLEAN"},
		Column{"day", "DATE"}, Column{"at", "TIMESTAMP"}, Column{"note", "VARCHAR"})
	if !reflect.DeepEqual(f.Sheets, []string{"Orders"}) || !f.HasHeader {
		t.Fatalf("sheets %v, header %v", f.Sheets, f.HasHeader)
	}
	got := rows(t, f)
	want := [][]any{
		{int64(1), 9.75, true, at.Truncate(24 * time.Hour), at, "tab\tand <tag>"},
		{int64(2), nil, false, at.Truncate(24*time.Hour).AddDate(0, 0, 1), at.Add(time.Hour), nil},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rows\n%v\nwant\n%v", got, want)
	}
	if _, err := Open(context.Background(), path, Options{Sheet: "Missing"}); err == nil {
		t.Fatal("a missing sheet opened")
	}
}

func TestExcelDates(t *testing.T) {
	for _, c := range []struct {
		id   int
		code string
		want bool
	}{{14, "", true}, {2, "", false}, {164, "yyyy-mm-dd", true}, {165, `"day "0`, false}, {166, "[Red]0.00", false}, {167, "h:mm", true}} {
		if got := isDateFormat(c.id, c.code); got != c.want {
			t.Errorf("format %d %q: %v", c.id, c.code, got)
		}
	}
	if got := excelTime(45292.5, false); !got.Equal(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("1900 system: %v", got)
	}
	if got := excelTime(0, true); !got.Equal(time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("1904 system: %v", got)
	}
}

func TestXML(t *testing.T) {
	path := write(t, "feed.xml", `<?xml version="1.0"?>
<shop>
  <meta><title>Feed</title></meta>
  <orders>
    <order id="1"><zip>01234</zip><total>9.5</total><item>a</item><item>b</item><when>2024-01-02</when></order>
    <order id="2"><zip>99999</zip><total>3</total><item>c</item><when>2024-01-03</when><note><b>rush</b> please</note></order>
  </orders>
</shop>`)
	f := open(t, path, Options{}, Column{"id", "BIGINT"}, Column{"zip", "VARCHAR"}, Column{"total", "DOUBLE"},
		Column{"item", "VARCHAR"}, Column{"when", "DATE"}, Column{"note", "VARCHAR"})
	if f.Rows != "orders/order" {
		t.Fatalf("rows at %q", f.Rows)
	}
	got := rows(t, f)
	if got[0][1] != "01234" || got[0][3] != `["a","b"]` || got[1][3] != "c" || got[1][5] != "rush please" || got[0][5] != nil {
		t.Fatalf("rows %v", got)
	}
	f = open(t, path, Options{Rows: "meta"}, Column{"title", "VARCHAR"})
	if got := rows(t, f); len(got) != 1 || got[0][0] != "Feed" {
		t.Fatalf("meta %v", got)
	}
}

// A decimal of more digits than a float keeps arrives as its digits, and
// sniffing never rounds a number to an integer nor cuts a time to a date.
func TestExactValues(t *testing.T) {
	d, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	pq := filepath.Join(t.TempDir(), "exact.parquet")
	if _, err := d.Exec("COPY (SELECT 12345678901234567890.0123456789::DECIMAL(38,10) AS d) TO '" + pq + "' (FORMAT PARQUET)"); err != nil {
		t.Fatal(err)
	}
	f := open(t, pq, Options{}, Column{"d", "DECIMAL(38,10)"})
	if got := rows(t, f); got[0][0] != "12345678901234567890.0123456789" {
		t.Fatalf("decimal %v", got)
	}
	xml := write(t, "t.xml", `<r><x n="9.5" at="2024-01-02 10:00:00" day="2024-01-02"/><x n="3" at="2024-01-03 11:00:00" day="2024-01-03"/></r>`)
	open(t, xml, Options{}, Column{"n", "DOUBLE"}, Column{"at", "TIMESTAMP"}, Column{"day", "DATE"})
}

// Reading an XML file twice, its columns first and its rows after, gives
// the columns, their order and types, and the values reading it once did.
func TestXMLTwoPassSameRows(t *testing.T) {
	path := write(t, "two.xml", `<root>
  <row id="1"><b>x</b><c>1</c></row>
  <row id="2" extra="e"><c>2</c><c>3</c><d><i>deep</i> text</d></row>
  <row><a>late</a><b>y</b><c>4</c></row>
</root>`)
	f := open(t, path, Options{}, Column{"id", "BIGINT"}, Column{"b", "VARCHAR"}, Column{"c", "VARCHAR"},
		Column{"extra", "VARCHAR"}, Column{"d", "VARCHAR"}, Column{"a", "VARCHAR"})
	want := [][]any{
		{int64(1), "x", "1", nil, nil, nil},
		{int64(2), nil, `["2","3"]`, "e", "deep text", nil},
		{nil, "y", "4", nil, nil, "late"},
	}
	if got := rows(t, f); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows %#v, want %#v", got, want)
	}
	f = open(t, path, Options{AllText: true}, Column{"id", "VARCHAR"}, Column{"b", "VARCHAR"}, Column{"c", "VARCHAR"},
		Column{"extra", "VARCHAR"}, Column{"d", "VARCHAR"}, Column{"a", "VARCHAR"})
	if got := rows(t, f); got[0][0] != "1" || got[2][5] != "late" {
		t.Fatalf("all text %v", got)
	}
	if _, err := Open(context.Background(), path, Options{Rows: "nothing"}); err == nil || err.Error() != "no element nothing in the file" {
		t.Fatalf("missing rows: %v", err)
	}
}

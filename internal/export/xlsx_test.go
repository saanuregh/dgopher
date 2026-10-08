package export

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readXLSX returns the parts of a workbook, each checked to be well-formed
// XML.
func readXLSX(t *testing.T, path string) map[string]string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	parts := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		for d := xml.NewDecoder(strings.NewReader(string(b))); ; {
			if _, err := d.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("%s is not well-formed: %v", f.Name, err)
			}
		}
		parts[f.Name] = string(b)
	}
	return parts
}

func TestXLSXWorkbook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.xlsx")
	writeFile(t, path, XLSX, fileColumns, fileRows(), Options{Header: true, Table: "orders: 2024/Q1"})
	parts := readXLSX(t, path)
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/styles.xml", "xl/worksheets/sheet1.xml"} {
		if _, ok := parts[name]; !ok {
			t.Fatalf("no %s in %v", name, parts)
		}
	}
	if !strings.Contains(parts["xl/workbook.xml"], `name="orders_ 2024_Q1"`) {
		t.Errorf("sheet name: %s", parts["xl/workbook.xml"])
	}
	sheet := parts["xl/worksheets/sheet1.xml"]
	for _, want := range []string{
		`state="frozen"`,
		`<c r="A1" s="1" t="inlineStr"><is><t xml:space="preserve">i</t></is></c>`,
		`<c r="A2"><v>7</v></c>`,
		`<c r="B2"><v>12.34</v></c>`, // an exact decimal Excel can hold
		`<c r="C2" t="inlineStr"><is><t xml:space="preserve">hello</t></is></c>`,
		`<c r="E2" s="3"><v>`,
		`<c r="F2" t="inlineStr"><is><t xml:space="preserve">\xff01</t></is></c>`,
		`<c r="G2" t="inlineStr"><is><t xml:space="preserve">{&quot;k&quot;:1}</t></is></c>`,
		`<c r="B3"><v>0.5</v></c>`,
	} {
		if !strings.Contains(sheet, want) {
			t.Errorf("no %s in\n%s", want, sheet)
		}
	}
	if strings.Contains(sheet, `r="D2"`) {
		t.Error("a NULL wrote a cell")
	}
}

func TestXLSXNumbers(t *testing.T) {
	tenth := 0.1 // a variable: constants add exactly
	for _, c := range []struct {
		v    any
		kind duckKind
		want string
		ok   bool
	}{
		{int64(123456789012345), kindBigint, "123456789012345", true},
		{int64(1234567890123456789), kindBigint, "", false}, // past 15 digits: text, not rounded
		{"12345678901234567890.5", kindDecimal, "", false},
		{"0.000123", kindDecimal, "0.000123", true},
		{"12.30", kindDecimal, "12.3", true},
		{"12.30", kindVarchar, "", false}, // text stays text
		{tenth + 0.2, kindDouble, "0.30000000000000004", true},
		{1e300, kindDouble, "1e+300", true},
		{"NaN", kindDouble, "", false},
	} {
		got, ok := xlsxNumber(c.v, c.kind)
		if ok != c.ok || ok && got != c.want {
			t.Errorf("%v (%v): %q %v, want %q %v", c.v, c.kind, got, ok, c.want, c.ok)
		}
	}
}

func TestXLSXTime(t *testing.T) {
	for _, c := range []struct {
		t     time.Time
		kind  duckKind
		want  string
		style int
		ok    bool
	}{
		{time.Date(1900, 3, 1, 0, 0, 0, 0, time.UTC), kindDate, "61", xlsxStyleDate, true},
		{time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC), kindTimestamp, "45292.5", xlsxStyleDateTime, true},
		// The time's own clock, as the database gave it.
		{time.Date(2024, 1, 1, 6, 0, 0, 0, time.FixedZone("x", -5*3600)), kindTimestampTZ, "45292.25", xlsxStyleDateTime, true},
		{time.Date(0, 1, 1, 18, 0, 0, 0, time.UTC), kindTime, "0.75", xlsxStyleTime, true},
		{time.Date(1899, 1, 1, 0, 0, 0, 0, time.UTC), kindDate, "", 0, false},
	} {
		got, style, ok := xlsxTime(c.t, c.kind)
		if ok != c.ok || got != c.want || style != c.style {
			t.Errorf("%v: %q %d %v, want %q %d %v", c.t, got, style, ok, c.want, c.style, c.ok)
		}
	}
}

func TestXLSXEscape(t *testing.T) {
	if got := xlsxEscape("a&b<c>\"d\x01e\tf_x0041_g_xyz"); got != "a&amp;b&lt;c&gt;&quot;d_x0001_e\tf_x005F_x0041_g_xyz" {
		t.Errorf("escaped %q", got)
	}
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 701: "ZZ", 702: "AAA", 16383: "XFD"} {
		if got := xlsxColumn(i); got != want {
			t.Errorf("column %d: %q, want %q", i, got, want)
		}
	}
	if got := xlsxSheetName("'History'"); got != "Sheet1" {
		t.Errorf("sheet name %q", got)
	}
}

// Past Excel's limits the export stops, rather than write a file Excel
// would repair, losing data, and leaves no file.
func TestXLSXLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.xlsx")
	w, err := NewFileWriter(path, XLSX, []Column{{Name: "s", DatabaseType: "text"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write([]any{strings.Repeat("é", xlsxMaxCellText+1)}); err == nil || !strings.Contains(err.Error(), "at most 32767") {
		t.Errorf("a long cell: %v", err)
	}
	if w.Close() == nil {
		t.Error("closed without the error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a partial file stays: %v", err)
	}

	w, err = NewFileWriter(path, XLSX, []Column{{Name: "n", DatabaseType: "int"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	w.(*xlsxWriter).row = xlsxMaxRows - 1
	if err := w.Write([]any{1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Write([]any{2}); err == nil || !strings.Contains(err.Error(), "rows") {
		t.Errorf("a row past the last: %v", err)
	}
	w.Close()
	if _, err := NewFileWriter(path, XLSX, make([]Column, xlsxMaxColumns+1), Options{}); err == nil {
		t.Error("too many columns accepted")
	}
}

package fileimport

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dgopher/internal/export"
)

const (
	testWorkbook = `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S" r:id="rId1"/></sheets></workbook>`
	testRels     = `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`
)

// writeWorkbook writes an .xlsx of the given parts, with a workbook of one
// sheet S unless parts give their own.
func writeWorkbook(t *testing.T, parts map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "book.xlsx")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(out)
	all := map[string]string{"xl/workbook.xml": testWorkbook, "xl/_rels/workbook.xml.rels": testRels}
	for k, v := range parts {
		all[k] = v
	}
	for name, body := range all {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func sheetOf(cells string) string {
	return `<worksheet><sheetData><row>` + cells + `</row></sheetData></worksheet>`
}

func TestExcelColumnBounds(t *testing.T) {
	if c, ok, err := columnIndex("XFD1"); err != nil || !ok || c != export.ExcelMaxColumns-1 {
		t.Fatalf("XFD1: %d %v %v", c, ok, err)
	}
	for _, ref := range []string{"XFE1", "ZZZZ1", strings.Repeat("Z", 15) + "1"} {
		if _, _, err := columnIndex(ref); err == nil {
			t.Fatalf("%s: no error", ref)
		}
	}
	ok := writeWorkbook(t, map[string]string{"xl/worksheets/sheet1.xml": sheetOf(`<c r="XFD1" t="inlineStr"><is><t>x</t></is></c>`)})
	f, err := Open(context.Background(), ok, Options{Header: HeaderNone})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Columns) != export.ExcelMaxColumns {
		t.Fatalf("%d columns", len(f.Columns))
	}
	f.Close()
	for _, ref := range []string{"XFE1", strings.Repeat("Z", 15) + "1", "ZZZZZZZ1"} {
		path := writeWorkbook(t, map[string]string{"xl/worksheets/sheet1.xml": sheetOf(`<c r="` + ref + `"><v>1</v></c>`)})
		if _, err := Open(context.Background(), path, Options{}); err == nil {
			t.Fatalf("%s opened", ref)
		}
	}
	// Cells without a reference count on from the last.
	many := strings.Repeat(`<c><v>1</v></c>`, export.ExcelMaxColumns+1)
	path := writeWorkbook(t, map[string]string{"xl/worksheets/sheet1.xml": sheetOf(many)})
	if _, err := Open(context.Background(), path, Options{}); err == nil {
		t.Fatal("a row past XFD opened")
	}
}

func TestExcelPartSizes(t *testing.T) {
	big := sheetOf(`<c t="inlineStr"><is><t>` + strings.Repeat("a", maxTokenSize+1) + `</t></is></c>`)
	path := writeWorkbook(t, map[string]string{"xl/worksheets/sheet1.xml": big})
	_, err := Open(context.Background(), path, Options{})
	if err == nil || !strings.Contains(err.Error(), "sheet1.xml") {
		t.Fatalf("a cell past the limit: %v", err)
	}
	// A cell's text counts whole, though comments split it into runs.
	run := strings.Repeat("a", maxTokenSize/2) + `<!---->`
	split := sheetOf(`<c t="inlineStr"><is><t>` + strings.Repeat(run, 3) + `</t></is></c>`)
	_, err = Open(context.Background(), writeWorkbook(t, map[string]string{"xl/worksheets/sheet1.xml": split}), Options{})
	if err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Fatalf("a cell split by comments past the limit: %v", err)
	}
	// A sheet longer than the other parts may be is read, a row at a time.
	saved := maxPartSize
	maxPartSize = 1 << 10
	defer func() { maxPartSize = saved }()
	path = writeWorkbook(t, map[string]string{"xl/worksheets/sheet1.xml": sheetOf(strings.Repeat(`<c><v>1</v></c>`, 200))})
	if f, err := Open(context.Background(), path, Options{}); err != nil {
		t.Fatalf("a long sheet: %v", err)
	} else {
		f.Close()
	}
	maxPartSize = saved
	shared := `<sst><si><t>` + strings.Repeat("a", maxSharedStringsSize) + `</t></si></sst>`
	path = writeWorkbook(t, map[string]string{"xl/sharedStrings.xml": shared, "xl/worksheets/sheet1.xml": sheetOf(`<c t="s"><v>0</v></c>`)})
	_, err = Open(context.Background(), path, Options{})
	if err == nil || !strings.Contains(err.Error(), "sharedStrings.xml") {
		t.Fatalf("shared strings past the limit: %v", err)
	}
}

func TestExcelSharedStringsLinear(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<sst><si>`)
	for range 200_000 {
		b.WriteString(`<r><t>abcdefghij</t></r>`)
	}
	b.WriteString(`</si></sst>`)
	path := writeWorkbook(t, map[string]string{"xl/sharedStrings.xml": b.String(), "xl/worksheets/sheet1.xml": sheetOf(`<c t="s"><v>0</v></c>`)})
	start := time.Now()
	f, err := Open(context.Background(), path, Options{Header: HeaderNone})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %v", d)
	}
	got := rows(t, f)
	if s, _ := got[0][0].(string); len(s) != 2_000_000 {
		t.Fatalf("shared string of %d bytes", len(s))
	}
}

// Shared strings cost more memory than their bytes in the part, an
// empty <si/> of 5 bytes most of all: past a count of them, a total of
// text or a depth of elements, the workbook is refused before it takes
// much. A workbook of plain and rich text reads as before.
func TestExcelSharedStringsMemory(t *testing.T) {
	refused := func(what, shared string) {
		t.Helper()
		path := writeWorkbook(t, map[string]string{"xl/sharedStrings.xml": shared, "xl/worksheets/sheet1.xml": sheetOf(`<c t="s"><v>0</v></c>`)})
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		f, err := Open(context.Background(), path, Options{})
		runtime.ReadMemStats(&after)
		if err == nil {
			f.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "sharedStrings.xml") {
			t.Fatalf("%s: %v", what, err)
		}
		if d := after.TotalAlloc - before.TotalAlloc; d > 512<<20 {
			t.Fatalf("%s: allocated %d MB", what, d>>20)
		}
	}
	saved, savedText := maxSharedStrings, maxSharedStringsText
	defer func() { maxSharedStrings, maxSharedStringsText = saved, savedText }()
	maxSharedStrings, maxSharedStringsText = 1<<20, 1<<20
	refused("empty strings", `<sst>`+strings.Repeat(`<si/>`, 2<<20)+`</sst>`)
	refused("text", `<sst>`+strings.Repeat(`<si><t>`+strings.Repeat("a", 1000)+`</t></si>`, 2<<10)+`</sst>`)
	refused("depth", `<sst><si>`+strings.Repeat(`<a>`, maxElementDepth)+strings.Repeat(`</a>`, maxElementDepth)+`</si></sst>`)

	shared := `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="5" uniqueCount="5">` +
		`<si><t>plain &amp; &lt;simple&gt;</t></si>` +
		`<si><t xml:space="preserve">  spaced  </t></si>` +
		`<si><r><rPr><b/><sz val="11"/></rPr><t>bold</t></r><r><t xml:space="preserve"> and </t></r><r><rPr><i/></rPr><t>italic</t></r>` +
		`<rPh sb="0" eb="1"><t>phonetic</t></rPh><phoneticPr fontId="1"/></si>` +
		`<si><t>line_x000D_break_x005F_x000D_</t></si>` +
		`<si><t>a<![CDATA[<b>]]><!-- note -->c</t></si>` +
		`</sst>`
	cells := `<c t="s"><v>0</v></c><c t="s"><v>1</v></c><c t="s"><v>2</v></c><c t="s"><v>3</v></c><c t="s"><v>4</v></c>`
	path := writeWorkbook(t, map[string]string{"xl/sharedStrings.xml": shared, "xl/worksheets/sheet1.xml": sheetOf(cells)})
	f, err := Open(context.Background(), path, Options{Header: HeaderNone})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := rows(t, f)
	want := []any{"plain & <simple>", "  spaced  ", "bold and italic", "line\rbreak_x000D_", "a<b>c"}
	if len(got) != 1 || fmt.Sprintf("%q", got[0]) != fmt.Sprintf("%q", want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Elements nested ever deeper, or declaring ever more name spaces, which
// the XML decoder holds while they are open, are refused in every part of
// a workbook and in an XML file before they take much memory.
func TestNestingLimit(t *testing.T) {
	deep := strings.Repeat(`<a>`, 4<<20)
	refused := func(what, path string, opt Options, want string) {
		t.Helper()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		f, err := Open(context.Background(), path, opt)
		runtime.ReadMemStats(&after)
		if err == nil {
			f.Close()
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", what, err)
		}
		if d := after.TotalAlloc - before.TotalAlloc; d > 512<<20 {
			t.Fatalf("%s: allocated %d MB", what, d>>20)
		}
	}
	refused("a sheet", writeWorkbook(t, map[string]string{"xl/worksheets/sheet1.xml": sheetOf(`<c>` + deep)}), Options{}, "deeper than")
	refused("the styles", writeWorkbook(t, map[string]string{"xl/styles.xml": `<styleSheet>` + deep, "xl/worksheets/sheet1.xml": sheetOf(`<c><v>1</v></c>`)}), Options{}, "deeper than")
	refused("the workbook", writeWorkbook(t, map[string]string{"xl/workbook.xml": `<workbook>` + deep}), Options{}, "deeper than")
	file := write(t, "deep.xml", `<rows><row><a>1</a></row>`+deep)
	refused("an XML file", file, Options{}, "deeper than")
	refused("an XML file's rows", file, Options{Rows: "row"}, "deeper than")

	declaring := strings.Repeat(`<a`+strings.Repeat(` xmlns:p=""`, 5000)+`>`, 3)
	refused("a sheet's name spaces", writeWorkbook(t, map[string]string{"xl/worksheets/sheet1.xml": sheetOf(`<c>` + declaring)}), Options{}, "name spaces")
	refused("the styles' name spaces", writeWorkbook(t, map[string]string{"xl/styles.xml": `<styleSheet>` + declaring, "xl/worksheets/sheet1.xml": sheetOf(`<c><v>1</v></c>`)}), Options{}, "name spaces")
	refused("an XML file's name spaces", write(t, "spaces.xml", `<rows><row><a>1</a></row>`+declaring), Options{}, "name spaces")
}

func TestXMLColumnLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("<rows>")
	for i := range export.ExcelMaxColumns + 1 {
		fmt.Fprintf(&b, `<row c%d="1"/><row c%d="2"/>`, i, i)
	}
	b.WriteString("</rows>")
	path := write(t, "wide.xml", b.String())
	_, err := Open(context.Background(), path, Options{})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprint(export.ExcelMaxColumns)) {
		t.Fatalf("got %v", err)
	}
}

func TestImportRecoversPanic(t *testing.T) {
	path := write(t, "a.csv", "a,b\n1,2\n")
	f := open(t, path, Options{}, Column{"a", "BIGINT"}, Column{"b", "BIGINT"})
	_, err := f.Read(context.Background(), 10, func([][]any) error { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Read: %v", err)
	}
	// A nil context panics where the load first asks it.
	book := writeWorkbook(t, map[string]string{"xl/worksheets/sheet1.xml": sheetOf(`<c><v>1</v></c>`)})
	var ctx context.Context
	if _, err := Open(ctx, book, Options{}); err == nil {
		t.Fatal("Open: no error")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(cancelled, book, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled open: %v", err)
	}
}

// writeRowsXML writes an XML file of at least size bytes of small rows.
func writeRowsXML(t *testing.T, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "big.xml")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(out)
	w.WriteString("<rows>\n")
	n := 0
	for i := 0; n < size; i++ {
		k, _ := fmt.Fprintf(w, "<r><a>%d</a><b>x%d</b><c>%d.5</c><d>t</d></r>\n", i, i, i)
		n += k
	}
	w.WriteString("</rows>\n")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// peakHeap runs fn and returns how far the Go heap in use grew past what
// it was before, at its highest.
func peakHeap(fn func()) uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	base, peak := m.HeapInuse, m.HeapInuse
	done := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		var s runtime.MemStats
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				runtime.ReadMemStats(&s)
				peak = max(peak, s.HeapInuse)
			}
		}
	}()
	fn()
	close(done)
	<-sampled
	runtime.ReadMemStats(&m)
	peak = max(peak, m.HeapInuse)
	return peak - base
}

// An XML file's rows go straight into the table, not first all into
// memory.
func TestXMLImportMemoryBounded(t *testing.T) {
	const size = 20 << 20
	path := writeRowsXML(t, size)
	start := time.Now()
	var f *File
	var err error
	grew := peakHeap(func() { f, err = Open(context.Background(), path, Options{AllText: true}) })
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	t.Logf("a %d MB file: heap grew %d MB in %v", size>>20, grew>>20, time.Since(start))
	if grew > 3*size {
		t.Fatalf("heap grew %d MB for a %d MB file", grew>>20, size>>20)
	}
}

// A tag of too many attributes, or longer than maxTokenSize, which the
// XML decoder would hold whole with every attribute, is refused before it
// is decoded.
func TestXMLLongTagRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<rows><row><a>1</a></row><row`)
	for i := range 1_000_000 {
		fmt.Fprintf(&b, ` xmlns:p%d=""`, i)
	}
	b.WriteString(`><a>2</a></row></rows>`)
	path := write(t, "long.xml", b.String())
	b.Reset()
	for _, opt := range []Options{{}, {Rows: "row"}} {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		f, err := Open(context.Background(), path, opt)
		runtime.ReadMemStats(&after)
		if err == nil {
			f.Close()
		}
		t.Logf("alloc %d MB", (after.TotalAlloc-before.TotalAlloc)>>20)
		if err == nil || !strings.Contains(err.Error(), "attributes") {
			t.Fatalf("%+v: %v", opt, err)
		}
		if d := after.TotalAlloc - before.TotalAlloc; d > 16<<20 {
			t.Fatalf("%+v: allocated %d MB", opt, d>>20)
		}
	}
	long := write(t, "longvalue.xml", `<rows><row a="`+strings.Repeat("v", maxTokenSize)+`"/></rows>`)
	if _, err := Open(context.Background(), long, Options{}); err == nil || !strings.Contains(err.Error(), "tag longer than") {
		t.Fatalf("a long value: %v", err)
	}
	// Markup that is not a tag holds '<' and '>' a tag would end at, and
	// quoted values hold '>'.
	ok := write(t, "ok.xml", `<?xml version="1.0"?><!DOCTYPE rows [<!ENTITY e "v>">]><!-- <r> > --><rows>`+
		`<r a="1>2"><b><![CDATA[<x> ]> > ]]></b></r><?pi a > b?><r a="3"><b>c</b></r></rows>`)
	f, err := Open(context.Background(), ok, Options{Rows: "r"})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := rows(t, f); len(got) != 2 || got[0][0] != "1>2" || got[0][1] != "<x> ]> >" {
		t.Fatalf("rows %q", got)
	}
}

// A child's text past maxTokenSize, though in runs each shorter, is
// refused naming the element.
func TestXMLChildTextCap(t *testing.T) {
	half := strings.Repeat("a", maxTokenSize/2)
	path := write(t, "text.xml", `<rows><row><a>1</a></row><row><long>`+half+`<i>`+half+`</i>`+half+`</long></row></rows>`)
	_, err := Open(context.Background(), path, Options{Rows: "row"})
	if err == nil || !strings.Contains(err.Error(), "element long") || !strings.Contains(err.Error(), "longer than") {
		t.Fatalf("got %v", err)
	}
	fit := write(t, "fit.xml", `<rows><row><long>`+half+`</long></row></rows>`)
	f, err := Open(context.Background(), fit, Options{Rows: "row"})
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// A text run past maxTokenSize is refused before the XML decoder holds
// it, both when the rows are looked for and when they are named.
func TestXMLLongTextRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "longtext.xml")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(out)
	w.WriteString("<rows><row><a>")
	chunk := strings.Repeat("x", 1<<16)
	for range (32 << 20) / len(chunk) {
		w.WriteString(chunk)
	}
	w.WriteString("</a></row></rows>")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	for _, opt := range []Options{{}, {Rows: "row"}} {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		f, err := Open(context.Background(), path, opt)
		runtime.ReadMemStats(&after)
		if err == nil {
			f.Close()
		}
		d := after.TotalAlloc - before.TotalAlloc
		t.Logf("%+v: alloc %d MB", opt, d>>20)
		if err == nil || !strings.Contains(err.Error(), "longtext.xml holds a value longer than") {
			t.Fatalf("%+v: %v", opt, err)
		}
		if d > 16<<20 {
			t.Fatalf("%+v: allocated %d MB", opt, d>>20)
		}
	}
}

// Comments and quoted literals in a DOCTYPE's internal subset may hold
// quotes, '<' and '>', which do not keep the directive open.
func TestXMLDoctypeCommentsImport(t *testing.T) {
	var rowsText strings.Builder
	for i := 0; rowsText.Len() < 2<<20; i++ {
		fmt.Fprintf(&rowsText, "<row><a>%d</a></row>", i)
	}
	for name, doctype := range map[string]string{
		"quote":   `<!DOCTYPE rows [ <!-- rows don't nest --> <!ELEMENT rows (row*)> ]>`,
		"less":    `<!DOCTYPE rows [ <!-- a < b --> <!ELEMENT rows (row*)> ]>`,
		"literal": `<!DOCTYPE rows [ <!ENTITY e "a > b -- <c"> <!ELEMENT rows (row*)> ]>`,
	} {
		path := write(t, name+".xml", doctype+"<rows>"+rowsText.String()+"</rows>")
		for _, opt := range []Options{{}, {Rows: "row"}} {
			f, err := Open(context.Background(), path, opt)
			if err != nil {
				t.Fatalf("%s %+v: %v", name, opt, err)
			}
			f.Close()
		}
	}
}

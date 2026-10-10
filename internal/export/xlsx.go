package export

import (
	"archive/zip"
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// Excel's limits: a worksheet holds at most xlsxMaxRows rows and
// xlsxMaxColumns columns, and a cell at most xlsxMaxCellText UTF-16
// units of text. A file past them is one Excel offers to repair, losing
// data, so the export stops instead.
const (
	xlsxMaxRows     = 1_048_576
	xlsxMaxColumns  = 16_384
	xlsxMaxCellText = 32_767
)

// ExcelMaxColumns is how many columns an Excel sheet holds: A to XFD.
const ExcelMaxColumns = xlsxMaxColumns

// ExcelMaxRows is how many rows an Excel sheet holds below its header.
const ExcelMaxRows = xlsxMaxRows - 1

// The cell formats of styles.xml, by their index in its cellXfs.
const (
	xlsxStyleDefault = iota
	xlsxStyleHeader
	xlsxStyleDate
	xlsxStyleDateTime
	xlsxStyleTime
)

// xlsxWriter writes one worksheet of an Excel workbook, row by row: the
// other parts of the file go first, so that the sheet streams last.
type xlsxWriter struct {
	file  *os.File
	buf   *bufio.Writer
	zip   *zip.Writer
	sheet *bufio.Writer
	path  string
	names []string
	cols  []duckColumn
	refs  []string // the column letters, A, B, … AA, …
	opt   Options
	row   int // the rows written, the header included
	err   error
}

func newXLSXWriter(path string, cols []Column, opt Options) (*xlsxWriter, error) {
	if len(cols) > xlsxMaxColumns {
		return nil, fmt.Errorf("export: Excel holds at most %d columns, and the rows have %d", xlsxMaxColumns, len(cols))
	}
	// Exported rows may be private: the file is the user's alone.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	buf := bufio.NewWriterSize(file, 1<<16)
	w := &xlsxWriter{file: file, buf: buf, zip: zip.NewWriter(buf), path: path, opt: opt}
	for i, c := range cols {
		w.names = append(w.names, c.Name)
		w.cols = append(w.cols, duckType(c.DatabaseType))
		w.refs = append(w.refs, xlsxColumn(i))
	}
	if err := w.start(); err != nil {
		w.zip.Close()
		file.Close()
		os.Remove(path)
		return nil, err
	}
	return w, nil
}

// start writes the workbook's parts, then opens its sheet with the
// header row.
func (w *xlsxWriter) start() error {
	sheetName := xlsxSheetName(w.opt.Table)
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
			`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
			`</Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
			`</Relationships>`},
		{"xl/workbook.xml", `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
			`<sheets><sheet name="` + xlsxEscape(sheetName) + `" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
			`</Relationships>`},
		{"xl/styles.xml", `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
			`<numFmts count="3"><numFmt numFmtId="164" formatCode="yyyy-mm-dd"/><numFmt numFmtId="165" formatCode="yyyy-mm-dd hh:mm:ss"/><numFmt numFmtId="166" formatCode="hh:mm:ss"/></numFmts>` +
			`<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts>` +
			`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
			`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
			`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
			`<cellXfs count="5">` +
			`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
			`<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>` +
			`<xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
			`<xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
			`<xf numFmtId="166" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
			`</cellXfs>` +
			`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
			`</styleSheet>`},
	}
	for _, p := range parts {
		f, err := w.zip.Create(p.name)
		if err == nil {
			_, err = io.WriteString(f, xml.Header+p.body)
		}
		if err != nil {
			return err
		}
	}
	f, err := w.zip.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		return err
	}
	w.sheet = bufio.NewWriterSize(f, 1<<16)
	w.put(xml.Header + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	if w.opt.Header {
		// The header row stays in view as the rows scroll.
		w.put(`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>`)
	}
	w.put(`<sheetData>`)
	if w.opt.Header {
		w.startRow()
		for i, name := range w.names {
			w.inlineString(i, name, xlsxStyleHeader)
		}
		w.put(`</row>`)
	}
	return w.err
}

func (w *xlsxWriter) put(s string) {
	if w.err == nil {
		_, w.err = w.sheet.WriteString(s)
	}
}

func (w *xlsxWriter) startRow() {
	w.row++
	w.put(`<row r="` + strconv.Itoa(w.row) + `">`)
}

func (w *xlsxWriter) Write(row []any) error {
	if w.err != nil {
		return w.err
	}
	if len(row) != len(w.cols) {
		return fmt.Errorf("export: row has %d values, want %d", len(row), len(w.cols))
	}
	if w.row == xlsxMaxRows {
		w.err = fmt.Errorf("export: Excel holds at most %d rows in a sheet: set a row limit, or export to CSV or Parquet", xlsxMaxRows)
		return w.err
	}
	w.startRow()
	for i, v := range row {
		w.cell(i, v)
	}
	w.put(`</row>`)
	return w.err
}

// cell writes a value as Excel keeps it: numbers it can hold exactly as
// numbers, times as dates, the rest as text.
func (w *xlsxWriter) cell(i int, v any) {
	if isNil(v) {
		if w.opt.NullText != "" {
			w.inlineString(i, w.opt.NullText, xlsxStyleDefault)
		}
		return
	}
	v = deref(v)
	switch x := v.(type) {
	case bool:
		b := "0"
		if x {
			b = "1"
		}
		w.put(`<c r="` + w.ref(i) + `" t="b"><v>` + b + `</v></c>`)
		return
	case time.Time:
		if serial, style, ok := xlsxTime(x, w.cols[i].kind); ok {
			w.put(`<c r="` + w.ref(i) + `" s="` + strconv.Itoa(style) + `"><v>` + serial + `</v></c>`)
			return
		}
	}
	if n, ok := xlsxNumber(v, w.cols[i].kind); ok {
		w.put(`<c r="` + w.ref(i) + `"><v>` + n + `</v></c>`)
		return
	}
	s := text(v)
	if n := xlsxTextLen(s); n > xlsxMaxCellText {
		w.err = fmt.Errorf("export: a value of column %q has %d characters, and an Excel cell holds at most %d: export to CSV or Parquet", w.names[i], n, xlsxMaxCellText)
		return
	}
	w.inlineString(i, s, xlsxStyleDefault)
}

func (w *xlsxWriter) ref(i int) string { return w.refs[i] + strconv.Itoa(w.row) }

func (w *xlsxWriter) inlineString(i int, s string, style int) {
	styled := ""
	if style != xlsxStyleDefault {
		styled = ` s="` + strconv.Itoa(style) + `"`
	}
	w.put(`<c r="` + w.ref(i) + `"` + styled + ` t="inlineStr"><is><t xml:space="preserve">` + xlsxEscape(s) + `</t></is></c>`)
}

func (w *xlsxWriter) Close() error {
	w.put(`</sheetData></worksheet>`)
	err := w.err
	if w.sheet != nil {
		if ferr := w.sheet.Flush(); err == nil {
			err = ferr
		}
	}
	if zerr := w.zip.Close(); err == nil {
		err = zerr
	}
	if berr := w.buf.Flush(); err == nil {
		err = berr
	}
	if cerr := w.file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(w.path)
	}
	return err
}

// xlsxColumn names the column of index i as Excel does: A … Z, AA, ….
func xlsxColumn(i int) string {
	var b []byte
	for i++; i > 0; i = (i - 1) / 26 {
		b = append([]byte{byte('A' + (i-1)%26)}, b...)
	}
	return string(b)
}

// xlsxSheetName makes a name Excel accepts for a sheet: at most 31
// characters, none of : \ / ? * [ ], and not starting or ending with '.
func xlsxSheetName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`:\/?*[]`, r) || r < ' ' {
			return '_'
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 31 {
		s = string(r[:31])
	}
	s = strings.Trim(s, "'")
	if s == "" || strings.EqualFold(s, "History") {
		return "Sheet1"
	}
	return s
}

// xlsxNumber writes a number as Excel keeps numbers, in a double. A
// float is one already; an exact value, as an integer or a decimal, is
// a number only within the 15 significant digits a double keeps: a
// longer one, as a large id, stays text, so that it is not rounded.
func xlsxNumber(v any, kind duckKind) (string, bool) {
	var s string
	switch x := v.(type) {
	case float32:
		return xlsxFloat(float64(x), 32)
	case float64:
		return xlsxFloat(x, 64)
	case string, []byte, fmt.Stringer:
		if kind != kindBigint && kind != kindDouble && kind != kindDecimal {
			return "", false
		}
		s = strings.TrimSpace(text(x))
	default:
		t, ok := scalarText(v)
		if !ok {
			return "", false
		}
		s = t
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || significantDigits(s) > 15 {
		return "", false
	}
	return xlsxFloat(f, 64)
}

// xlsxFloat writes a float as Excel does: without an exponent but for
// the very large and the very small; NaN and the infinities, which it
// cannot hold, are not numbers.
func xlsxFloat(f float64, bits int) (string, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", false
	}
	if a := math.Abs(f); a != 0 && (a < 1e-5 || a >= 1e15) {
		return strconv.FormatFloat(f, 'g', -1, bits), true
	}
	return strconv.FormatFloat(f, 'f', -1, bits), true
}

// significantDigits counts the digits of a number's mantissa, without
// the zeros that lead or, after a point, trail.
func significantDigits(s string) int {
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		s = s[:i]
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	return len(strings.TrimLeft(digits, "0"))
}

// xlsxEpoch is day 0 of Excel's dates, as it counts them since 1 March
// 1900, its leap day of 1900 included.
var xlsxEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

// xlsxTime writes a time as Excel's serial number of days, in the time's
// own clock, with the style that shows it: a date, a time of day, or
// both. Before March 1900 Excel's days are off by its leap day of 1900,
// so earlier times stay text.
func xlsxTime(t time.Time, kind duckKind) (serial string, style int, ok bool) {
	wall := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	if kind == kindTime {
		day := wall.Sub(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC))
		return strconv.FormatFloat(day.Hours()/24, 'g', -1, 64), xlsxStyleTime, true
	}
	if wall.Before(time.Date(1900, 3, 1, 0, 0, 0, 0, time.UTC)) || wall.Year() > 9999 {
		return "", 0, false
	}
	days := wall.Sub(xlsxEpoch).Hours() / 24
	if kind == kindDate {
		return strconv.FormatFloat(math.Floor(days), 'g', -1, 64), xlsxStyleDate, true
	}
	return strconv.FormatFloat(days, 'g', -1, 64), xlsxStyleDateTime, true
}

// xlsxTextLen counts text as Excel limits it, in UTF-16 units.
func xlsxTextLen(s string) int {
	if len(s) <= xlsxMaxCellText/2 {
		return utf8.RuneCountInString(s) // short enough whatever its runes
	}
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// xlsxEscape escapes text for an element or attribute of the workbook.
// A character XML cannot hold, as a control character, is written as
// Excel's _xHHHH_, and so is the _ of a text that already looks like one.
func xlsxEscape(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '_' && looksEscaped(s[i:]):
			b.WriteString("_x005F_")
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteRune(r)
		case r < ' ' || r == 0xFFFE || r == 0xFFFF:
			fmt.Fprintf(&b, "_x%04X_", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// looksEscaped reports whether s starts as Excel's _xHHHH_.
func looksEscaped(s string) bool {
	if len(s) < 7 || s[1] != 'x' || s[6] != '_' {
		return false
	}
	for _, c := range s[2:6] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

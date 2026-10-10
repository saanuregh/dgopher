package fileimport

import (
	"archive/zip"
	"context"
	"database/sql/driver"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/duckdb/duckdb-go/v2"

	"dgopher/internal/export"
)

// The most of a workbook's part that is read, as its zip entry declares
// it, which archive/zip holds the entry to: the shared strings, all held
// in memory, may be larger than the other parts. Sheets, read a row at a
// time, are bounded by Excel's rows and columns instead, and their cells
// by maxTokenSize: the longest run between two '<', a text or a tag that
// the XML decoder holds whole. Excel's cells hold at most 32,767
// characters, a few times longer escaped. Variables for tests.
var (
	maxPartSize          = 64 << 20
	maxSharedStringsSize = 256 << 20
	maxTokenSize         = 1 << 20
)

// The most shared strings a workbook may hold, and bytes of text among
// them. Their part's size does not bound their memory: each string costs
// 16 bytes beyond its text, so 5 bytes of <si/> take more than three
// times that, and the garbage collector lets the heap grow to twice what
// is held. These keep the shared strings of any workbook well under 1 GB
// of memory. Variables for tests.
var (
	maxSharedStrings     = 4 << 20
	maxSharedStringsText = 128 << 20
)

// excelMaxSheetRows is how many rows an Excel sheet holds, its header's
// among them.
const excelMaxSheetRows = export.ExcelMaxRows + 1

// workbook is what reading a sheet of an Excel workbook needs from its
// other parts.
type workbook struct {
	zip      *zip.ReadCloser
	sheets   []string          // the sheets' names, in order
	parts    map[string]string // each sheet's part, by name
	strings  []string          // the shared strings
	dates    map[int]bool      // the cell styles that show dates
	date1904 bool
}

func openWorkbook(ctx context.Context, p string) (*workbook, error) {
	z, err := zip.OpenReader(p)
	if err != nil {
		return nil, fmt.Errorf("%s is not an Excel workbook: %w", path.Base(p), err)
	}
	wb := &workbook{zip: z, parts: map[string]string{}, dates: map[int]bool{}}
	if err := wb.readParts(ctx); err != nil {
		z.Close()
		return nil, err
	}
	return wb, nil
}

// open opens a part of the workbook no larger than limit bytes, which
// stops reading once ctx is done.
func (wb *workbook) open(ctx context.Context, name string, limit int) (io.ReadCloser, error) {
	for _, f := range wb.zip.File {
		if f.Name == name {
			if f.UncompressedSize64 > uint64(limit) {
				return nil, fmt.Errorf("the workbook's %s is larger than %d MB", name, limit>>20)
			}
			r, err := f.Open()
			if err != nil {
				return nil, err
			}
			return &partReader{ctx: ctx, ReadCloser: r}, nil
		}
	}
	return nil, missingPart(name)
}

// missingPart is the error of a part the workbook does not have.
type missingPart string

func (m missingPart) Error() string { return "the workbook has no " + string(m) }

// partReader reads a part of the workbook until its context is done. How
// long its text and tags run is bounded where it is decoded (markupLimit).
type partReader struct {
	ctx context.Context
	io.ReadCloser
}

func (p *partReader) Read(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	return p.ReadCloser.Read(b)
}

// decode reads a part of the workbook into v; a part that is not there
// leaves v as it is, when optional.
func (wb *workbook) decode(ctx context.Context, name string, limit int, v any, optional bool) error {
	r, err := wb.open(ctx, name, limit)
	if err != nil {
		if _, missing := err.(missingPart); optional && missing {
			return nil
		}
		return err
	}
	defer r.Close()
	return newDecoder(r, "the workbook's "+name).Decode(v)
}

func (wb *workbook) readParts(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var book struct {
		Pr struct {
			Date1904 string `xml:"date1904,attr"`
		} `xml:"workbookPr"`
		Sheets []struct {
			Name string `xml:"name,attr"`
			ID   string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := wb.decode(ctx, "xl/workbook.xml", maxPartSize, &book, false); err != nil {
		return err
	}
	wb.date1904 = book.Pr.Date1904 == "1" || book.Pr.Date1904 == "true"
	var rels struct {
		Rels []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := wb.decode(ctx, "xl/_rels/workbook.xml.rels", maxPartSize, &rels, false); err != nil {
		return err
	}
	targets := make(map[string][]string, len(rels.Rels))
	for _, r := range rels.Rels {
		targets[r.ID] = append(targets[r.ID], r.Target)
	}
	for _, s := range book.Sheets {
		for _, t := range targets[s.ID] {
			target := strings.TrimPrefix(t, "/")
			if !strings.HasPrefix(target, "xl/") {
				target = path.Join("xl", target)
			}
			wb.sheets = append(wb.sheets, s.Name)
			wb.parts[s.Name] = target
		}
	}
	if len(wb.sheets) == 0 {
		return errors.New("the workbook has no sheet")
	}
	if err := wb.readSharedStrings(ctx); err != nil {
		return err
	}
	var styles struct {
		Formats []struct {
			ID   int    `xml:"numFmtId,attr"`
			Code string `xml:"formatCode,attr"`
		} `xml:"numFmts>numFmt"`
		Cells []struct {
			Format int `xml:"numFmtId,attr"`
		} `xml:"cellXfs>xf"`
	}
	if err := wb.decode(ctx, "xl/styles.xml", maxPartSize, &styles, true); err != nil {
		return err
	}
	custom := map[int]string{}
	for _, f := range styles.Formats {
		custom[f.ID] = f.Code
	}
	for i, c := range styles.Cells {
		wb.dates[i] = isDateFormat(c.Format, custom[c.Format])
	}
	return nil
}

// readSharedStrings reads the workbook's shared strings an element at a
// time. An item's text is its <t>, or its runs' <t> joined; its phonetic
// runs are left out.
func (wb *workbook) readSharedStrings(ctx context.Context) error {
	const name = "xl/sharedStrings.xml"
	r, err := wb.open(ctx, name, maxSharedStringsSize)
	if _, missing := err.(missingPart); missing {
		return nil
	} else if err != nil {
		return err
	}
	defer r.Close()
	d := newTokenReader(r, "the workbook's "+name)
	var (
		item          []byte // the text of the item open
		depth         int    // of the element open: 1 the root, 2 an item
		inItem, inRun bool
		textDepth     int // of the <t> being read, 0 outside one
		text          int // the bytes of text read
	)
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case depth == 2 && t.Name.Local == "si":
				inItem, item = true, item[:0]
			case depth == 3 && inItem && t.Name.Local == "r":
				inRun = true
			case (depth == 3 && inItem || depth == 4 && inRun) && t.Name.Local == "t":
				textDepth = depth
			}
		case xml.EndElement:
			switch {
			case depth == 1:
				return nil
			case depth == textDepth:
				textDepth = 0
			case depth == 3 && inRun:
				inRun = false
			case depth == 2 && inItem:
				inItem = false
				if len(wb.strings) == maxSharedStrings {
					return fmt.Errorf("the workbook's %s holds more than %d shared strings", name, maxSharedStrings)
				}
				wb.strings = append(wb.strings, unescapeExcel(string(item)))
			}
			depth--
		case xml.CharData:
			if textDepth != 0 && depth == textDepth {
				if text += len(t); text > maxSharedStringsText {
					return fmt.Errorf("the workbook's %s holds more than %d MB of text", name, maxSharedStringsText>>20)
				}
				item = append(item, t...)
			}
		}
	}
}

// isDateFormat reports whether a number format shows a date or a time:
// one of Excel's built-in ones, or a format of its own whose codes, out
// of quotes and brackets, hold a day, a month, a year, an hour or a
// second.
func isDateFormat(id int, code string) bool {
	if id >= 14 && id <= 22 || id >= 45 && id <= 47 {
		return true
	}
	if code == "" {
		return false
	}
	quoted, bracket := false, false
	for _, r := range strings.ToLower(code) {
		switch {
		case r == '"':
			quoted = !quoted
		case quoted:
		case r == '[':
			bracket = true
		case r == ']':
			bracket = false
		case bracket:
		case strings.ContainsRune("dmyhs", r):
			return true
		}
	}
	return false
}

// unescapeExcel decodes Excel's _xHHHH_, which writes a character XML
// cannot hold.
func unescapeExcel(s string) string {
	if !strings.Contains(s, "_x") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '_' && i+6 < len(s) && s[i+1] == 'x' && s[i+6] == '_' {
			if n, err := strconv.ParseUint(s[i+2:i+6], 16, 16); err == nil {
				b.WriteRune(rune(n))
				i += 6
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// sheetRows calls each with every row of a sheet, as values by column:
// float64, bool, time.Time or string, nil for an empty cell.
func (wb *workbook) sheetRows(ctx context.Context, sheet string, each func([]any) error) error {
	r, err := wb.open(ctx, wb.parts[sheet], math.MaxInt)
	if err != nil {
		return err
	}
	defer r.Close()
	d := newTokenReader(r, "the workbook's "+wb.parts[sheet])
	var row []any
	rows := 0
	var cell struct {
		col   int
		typ   string
		style int
		value strings.Builder
		in    bool // in the cell's value, <v> or <t>
	}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the sheet %s: %w", sheet, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				if rows++; rows > excelMaxSheetRows {
					return fmt.Errorf("the sheet %s has more than %d rows, Excel's most", sheet, excelMaxSheetRows)
				}
				row = row[:0:0]
			case "c":
				cell.col, cell.typ, cell.style = len(row), "", 0
				cell.value.Reset()
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						c, ok, err := columnIndex(a.Value)
						if err != nil {
							return fmt.Errorf("the sheet %s: %w", sheet, err)
						}
						if ok {
							cell.col = c
						}
					case "t":
						cell.typ = a.Value
					case "s":
						cell.style, _ = strconv.Atoi(a.Value)
					}
				}
			case "v", "t":
				cell.in = true
			}
		case xml.CharData:
			if cell.in {
				// Comments split a text into runs, which markupLimit
				// bounds one at a time.
				if cell.value.Len()+len(t) > maxTokenSize {
					return fmt.Errorf("the workbook's %s holds a value longer than %d KB", wb.parts[sheet], maxTokenSize>>10)
				}
				cell.value.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				cell.in = false
			case "c":
				if cell.col >= export.ExcelMaxColumns {
					return fmt.Errorf("the sheet %s has a cell past column XFD, Excel's last", sheet)
				}
				for len(row) <= cell.col {
					row = append(row, nil)
				}
				row[cell.col] = wb.value(cell.typ, cell.style, cell.value.String())
			case "row":
				if err := each(row); err != nil {
					return err
				}
			}
		}
	}
}

// value is a cell's value by its type and style.
func (wb *workbook) value(typ string, style int, raw string) any {
	switch typ {
	case "s":
		i, err := strconv.Atoi(raw)
		if err != nil || i < 0 || i >= len(wb.strings) {
			return nil
		}
		return wb.strings[i]
	case "inlineStr", "str", "e":
		return unescapeExcel(raw)
	case "b":
		return raw == "1"
	case "d":
		if t, err := time.Parse("2006-01-02T15:04:05.999999999", raw); err == nil {
			return t
		}
		return raw
	}
	if raw == "" {
		return nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	if wb.dates[style] {
		return excelTime(f, wb.date1904)
	}
	return f
}

// excelTime is the time of an Excel serial number: days since 30
// December 1899, or 1 January 1904 in a workbook of 1904's system.
func excelTime(serial float64, date1904 bool) time.Time {
	epoch := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	if date1904 {
		epoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	days := math.Floor(serial)
	// Rounded to the millisecond, as Excel keeps time.
	ms := math.Round((serial - days) * 24 * 60 * 60 * 1000)
	return epoch.AddDate(0, 0, int(days)).Add(time.Duration(ms) * time.Millisecond)
}

// columnIndex is the index of a cell reference's column: A1 is 0, AB7
// 27; ok is false for a reference without a column. A column past XFD,
// Excel's last, is an error.
func columnIndex(ref string) (index int, ok bool, err error) {
	n := 0
	i := 0
	for ; i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z'; i++ {
		if i == 3 {
			return 0, false, fmt.Errorf("the cell %.20s is past column XFD, Excel's last", ref)
		}
		n = n*26 + int(ref[i]-'A'+1)
	}
	if n > export.ExcelMaxColumns {
		return 0, false, fmt.Errorf("the cell %.20s is past column XFD, Excel's last", ref)
	}
	return n - 1, i > 0, nil
}

// kind is what a column's cells hold, as a sheet is read.
type kind int

const (
	kindNone kind = 0
	kindInt  kind = 1 << iota
	kindFloat
	kindBool
	kindDate
	kindTime
	kindText
)

func kindOf(v any) kind {
	switch x := v.(type) {
	case nil:
		return kindNone
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return kindInt
		}
		return kindFloat
	case bool:
		return kindBool
	case time.Time:
		if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
			return kindDate
		}
		return kindTime
	}
	return kindText
}

// duckType is the DuckDB type of a column whose cells hold kinds.
func duckType(kinds kind, allText bool) string {
	switch {
	case allText || kinds&kindText != 0:
		return "VARCHAR"
	case kinds == kindNone:
		return "VARCHAR"
	case kinds == kindInt:
		return "BIGINT"
	case kinds&^(kindInt|kindFloat) == 0:
		return "DOUBLE"
	case kinds == kindBool:
		return "BOOLEAN"
	case kinds == kindDate:
		return "DATE"
	case kinds&^(kindDate|kindTime) == 0:
		return "TIMESTAMP"
	}
	return "VARCHAR" // mixed: numbers beside dates or booleans
}

// cellText is a cell's value as text, for a column of text.
func cellText(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case time.Time:
		if kindOf(x) == kindDate {
			return x.Format(time.DateOnly)
		}
		return x.Format("2006-01-02 15:04:05.999")
	}
	return v
}

// loadExcel reads a sheet into the table src: once to find its columns
// and their types, then to load its rows.
func (f *File) loadExcel(ctx context.Context, opt Options) error {
	wb, err := openWorkbook(ctx, f.Path)
	if err != nil {
		return err
	}
	defer wb.zip.Close()
	f.Sheets, f.Sheet = wb.sheets, wb.sheets[0]
	if opt.Sheet != "" {
		if _, ok := wb.parts[opt.Sheet]; !ok {
			return fmt.Errorf("the workbook has no sheet %q", opt.Sheet)
		}
		f.Sheet = opt.Sheet
	}
	var first []any
	var kinds []kind
	width, rows := 0, 0
	err = wb.sheetRows(ctx, f.Sheet, func(row []any) error {
		if rows == 0 {
			first = append([]any(nil), row...)
		}
		rows++
		width = max(width, len(row))
		for len(kinds) < len(row) {
			kinds = append(kinds, kindNone)
		}
		if rows > 1 || opt.Header == HeaderNone {
			for i, v := range row {
				kinds[i] |= kindOf(v)
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	if width == 0 {
		return fmt.Errorf("the sheet %s is empty", f.Sheet)
	}
	f.HasHeader = opt.Header == HeaderFirstRow || opt.Header == HeaderDetect && namesColumns(first)
	if !f.HasHeader {
		// The first row is data, which the first pass left out.
		for i, v := range first {
			kinds[i] |= kindOf(v)
		}
	}
	for len(kinds) < width {
		kinds = append(kinds, kindNone)
	}
	names := make([]string, width)
	for i := range names {
		if f.HasHeader && i < len(first) && first[i] != nil {
			names[i] = strings.TrimSpace(fmt.Sprint(cellText(first[i])))
		}
	}
	f.Columns = make([]Column, width)
	for i, name := range uniqueNames(names) {
		f.Columns[i] = Column{Name: name, Type: duckType(kinds[i], opt.AllText)}
	}
	return f.createAndAppend(ctx, func(add func([]any) error) error {
		n := 0
		return wb.sheetRows(ctx, f.Sheet, func(row []any) error {
			if n++; n == 1 && f.HasHeader {
				return nil
			}
			vals := make([]any, width)
			for i := range vals {
				if i >= len(row) || row[i] == nil {
					continue
				}
				switch f.Columns[i].Type {
				case "VARCHAR":
					vals[i] = cellText(row[i])
				case "BIGINT":
					vals[i] = int64(row[i].(float64))
				default:
					vals[i] = row[i]
				}
			}
			return add(vals)
		})
	})
}

// namesColumns reports whether a first row reads as the columns' names:
// text in every cell, none twice.
func namesColumns(row []any) bool {
	seen := map[string]bool{}
	for _, v := range row {
		s, ok := v.(string)
		s = strings.TrimSpace(s)
		if !ok || s == "" || seen[s] {
			return false
		}
		seen[s] = true
	}
	return len(row) > 0
}

// uniqueNames names the columns without one column1, column2, …, and
// tells apart names given twice with a number.
func uniqueNames(names []string) []string {
	out := make([]string, len(names))
	seen := map[string]bool{}
	for i, n := range names {
		if n == "" {
			n = fmt.Sprintf("column%d", i+1)
		}
		base := n
		for k := 2; seen[strings.ToLower(n)]; k++ {
			n = fmt.Sprintf("%s_%d", base, k)
		}
		seen[strings.ToLower(n)] = true
		out[i] = n
	}
	return out
}

// createAndAppend creates the table src of the file's columns, and
// appends the rows fill adds.
func (f *File) createAndAppend(ctx context.Context, fill func(add func([]any) error) error) error {
	defs := make([]string, len(f.Columns))
	for i, c := range f.Columns {
		defs[i] = quoteIdent(c.Name) + " " + c.Type
	}
	if _, err := f.db.ExecContext(ctx, "CREATE TABLE src ("+strings.Join(defs, ", ")+")"); err != nil {
		return err
	}
	conn, err := f.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(dc any) error {
		app, err := duckdb.NewAppenderFromConn(dc.(driver.Conn), "", "src")
		if err != nil {
			return err
		}
		err = fill(func(row []any) error {
			vals := make([]driver.Value, len(row))
			for i, v := range row {
				vals[i] = v
			}
			if err := app.AppendRow(vals...); err != nil {
				return err
			}
			return ctx.Err()
		})
		return errors.Join(err, app.Close())
	})
}

// Package export writes query results as CSV, TSV, JSON, JSON Lines, SQL INSERT statements, Markdown,
// an Excel workbook, Parquet, or a DuckDB database file.
package export

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"dgopher/internal/db"
)

type Format string

const (
	CSV       Format = "csv"
	TSV       Format = "tsv"
	JSON      Format = "json"
	JSONLines Format = "jsonl"
	SQL       Format = "sql"
	Markdown  Format = "md"
	// XLSX, Parquet and DuckDBFile need a file path; see NeedsFile. Parquet
	// and DuckDBFile are written through DuckDB.
	XLSX       Format = "xlsx"
	Parquet    Format = "parquet"
	DuckDBFile Format = "duckdb"
)

func Formats() []Format {
	return []Format{CSV, TSV, JSON, JSONLines, SQL, Markdown, XLSX, Parquet, DuckDBFile}
}

// NeedsFile reports whether a format is written only to a file, never as
// text to the clipboard.
func NeedsFile(f Format) bool { return f == XLSX || f == Parquet || f == DuckDBFile }

func (f Format) Extension() string { return string(f) }

func (f Format) Label() string {
	switch f {
	case CSV:
		return "CSV"
	case TSV:
		return "TSV"
	case JSON:
		return "JSON"
	case JSONLines:
		return "JSON Lines"
	case SQL:
		return "SQL INSERT"
	case Markdown:
		return "Markdown"
	case XLSX:
		return "Excel workbook"
	case Parquet:
		return "Parquet"
	case DuckDBFile:
		return "DuckDB database"
	}
	return string(f)
}

// Options' zero value gives the default output of each format.
type Options struct {
	Header   bool
	NullText string
	// Schema and Table are raw names; the SQL writer quotes them with Quote.
	Schema string
	Table  string
	Quote  func(ident string) string
	// Engine selects the literals of Format SQL. PostgreSQL, MySQL,
	// ClickHouse and DuckDB get their own; any other engine, or none, gets
	// standard SQL as SQLite reads it.
	Engine db.Engine
	// Delimiter and QuoteChar apply to CSV only; 0 means ',' and '"'.
	Delimiter   rune
	QuoteChar   rune
	QuoteAlways bool
	BOM         bool
	// FormulaGuard prefixes ' to CSV and TSV text a spreadsheet would read
	// as a formula.
	FormulaGuard bool
	// RowsPerInsert above 1 groups rows into multi-row INSERT statements.
	RowsPerInsert int
}

type RowWriter interface {
	Write(row []any) error
	Close() error
}

func NewWriter(w io.Writer, f Format, columns []string, opt Options) (RowWriter, error) {
	buffered := bufio.NewWriter(w)
	if NeedsFile(f) {
		return nil, fmt.Errorf("export: %s needs a file path", f.Label())
	}
	base := baseWriter{out: buffered, columns: columns, opt: opt}
	if opt.BOM {
		base.put("\ufeff")
	}
	switch f {
	case CSV, TSV:
		cw := &delimitedWriter{baseWriter: base, tsv: f == TSV}
		if f == CSV {
			if err := cw.configureCSV(); err != nil {
				return nil, err
			}
		}
		if opt.Header {
			if err := cw.writeFields(columns); err != nil {
				return nil, err
			}
		}
		return cw, nil
	case JSON, JSONLines:
		return &jsonWriter{baseWriter: base, lines: f == JSONLines, keys: uniqueKeys(columns)}, nil
	case SQL:
		return newSQLWriter(base), nil
	case Markdown:
		mw := &markdownWriter{baseWriter: base}
		mw.writeHeader()
		return mw, nil
	}
	return nil, fmt.Errorf("export: unknown format %q", f)
}

// Text renders rows in format f, for clipboard copies.
func Text(f Format, columns []string, rows [][]any, opt Options) (string, error) {
	var b strings.Builder
	w, err := NewWriter(&b, f, columns, opt)
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		if err := w.Write(row); err != nil {
			return "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

type baseWriter struct {
	out     *bufio.Writer
	columns []string
	opt     Options
	err     error
}

func (b *baseWriter) checkRow(row []any) error {
	if b.err != nil {
		return b.err
	}
	if len(row) != len(b.columns) {
		return fmt.Errorf("export: row has %d values, want %d", len(row), len(b.columns))
	}
	return nil
}

func (b *baseWriter) put(s string) {
	if b.err == nil {
		_, b.err = b.out.WriteString(s)
	}
}

func (b *baseWriter) finish() error {
	if b.err != nil {
		return b.err
	}
	return b.out.Flush()
}

// ---- value conversion ----

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

// deref unwraps pointers so *string, *int64 and similar scan targets print their value.
func deref(v any) any {
	for {
		rv := reflect.ValueOf(v)
		if rv.Kind() != reflect.Pointer || rv.IsNil() {
			return v
		}
		if _, ok := v.(fmt.Stringer); ok {
			return v
		}
		if _, ok := v.(json.Marshaler); ok {
			return v
		}
		v = rv.Elem().Interface()
	}
}

func isCollection(v any) bool {
	switch reflect.ValueOf(v).Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
		return true
	}
	return false
}

func hexText(b []byte) string { return `\x` + hex.EncodeToString(b) }

func floatText(f float64, bits int) string { return strconv.FormatFloat(f, 'g', -1, bits) }

// scalarText converts numbers, bools, and time to text; ok is false for other kinds.
func scalarText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case time.Time:
		return x.Format(time.RFC3339Nano), true
	case float32:
		return floatText(float64(x), 32), true
	case float64:
		return floatText(x, 64), true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if _, ok := v.(fmt.Stringer); !ok {
			return strconv.FormatInt(rv.Int(), 10), true
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if _, ok := v.(fmt.Stringer); !ok {
			return strconv.FormatUint(rv.Uint(), 10), true
		}
	}
	return "", false
}

// text converts a non-nil value to its display text.
func text(v any) string {
	v = deref(v)
	if b, ok := v.([]byte); ok {
		if utf8.Valid(b) {
			return string(b)
		}
		return hexText(b)
	}
	if s, ok := scalarText(v); ok {
		return s
	}
	if s, ok := v.(fmt.Stringer); ok {
		return s.String()
	}
	if m, ok := v.(json.Marshaler); ok {
		if out, err := m.MarshalJSON(); err == nil {
			return string(out)
		}
	}
	if isCollection(v) {
		if out, err := marshalJSON(v); err == nil {
			return string(out)
		}
	}
	return fmt.Sprint(v)
}

func marshalJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

func jsonString(s string) string {
	out, _ := marshalJSON(s)
	return string(out)
}

// jsonValue converts a value to JSON text.
func jsonValue(v any) string {
	if isNil(v) {
		return "null"
	}
	v = deref(v)
	switch x := v.(type) {
	case []byte:
		if utf8.Valid(x) {
			return jsonString(string(x))
		}
		return jsonString(hexText(x))
	case time.Time:
		return jsonString(x.Format(time.RFC3339Nano))
	case bool:
		return strconv.FormatBool(x)
	case float32:
		if f := float64(x); math.IsNaN(f) || math.IsInf(f, 0) {
			return jsonString(floatText(f, 32))
		}
		return floatText(float64(x), 32)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return jsonString(floatText(x, 64))
		}
		return floatText(x, 64)
	case string:
		return jsonString(x)
	case json.Marshaler:
		if out, err := marshalJSON(x); err == nil {
			return string(out)
		}
	}
	if s, ok := scalarText(v); ok {
		return s
	}
	if _, ok := v.(fmt.Stringer); !ok && isCollection(v) {
		if out, err := marshalJSON(v); err == nil {
			return string(out)
		}
	}
	return jsonString(text(v))
}

// ---- CSV / TSV ----

type delimitedWriter struct {
	baseWriter
	csv *csv.Writer
	tsv bool
	// custom CSV quoting, used when encoding/csv cannot express the options
	custom      bool
	delim       string
	quote       string
	quoteAlways bool
}

func (d *delimitedWriter) configureCSV() error {
	delim, quote := d.opt.Delimiter, d.opt.QuoteChar
	if delim == 0 {
		delim = ','
	}
	if quote == 0 {
		quote = '"'
	}
	if delim == quote || delim == '\r' || delim == '\n' || quote == '\r' || quote == '\n' || !utf8.ValidRune(delim) || !utf8.ValidRune(quote) {
		return fmt.Errorf("export: invalid CSV delimiter %q or quote %q", delim, quote)
	}
	if quote == '"' && !d.opt.QuoteAlways {
		d.csv = csv.NewWriter(d.out)
		d.csv.Comma = delim
		return nil
	}
	d.custom, d.delim, d.quote, d.quoteAlways = true, string(delim), string(quote), d.opt.QuoteAlways
	return nil
}

func (d *delimitedWriter) customField(f string) string {
	needs := d.quoteAlways || strings.Contains(f, d.delim) || strings.Contains(f, d.quote) || strings.ContainsAny(f, "\r\n") ||
		(f != "" && (f[0] == ' ' || f[0] == '\t'))
	if !needs {
		return f
	}
	return d.quote + strings.ReplaceAll(f, d.quote, d.quote+d.quote) + d.quote
}

var tsvEscaper = strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)

func (d *delimitedWriter) writeFields(fields []string) error {
	if d.csv != nil {
		return d.csv.Write(fields)
	}
	if d.custom {
		for i, f := range fields {
			if i > 0 {
				d.put(d.delim)
			}
			d.put(d.customField(f))
		}
		d.put("\n")
		return d.err
	}
	for i, f := range fields {
		if i > 0 {
			d.put("\t")
		}
		d.put(tsvEscaper.Replace(f))
	}
	d.put("\n")
	return d.err
}

func (d *delimitedWriter) Write(row []any) error {
	if err := d.checkRow(row); err != nil {
		return err
	}
	fields := make([]string, len(row))
	for i, v := range row {
		if isNil(v) {
			fields[i] = d.opt.NullText
		} else {
			fields[i] = text(v)
			if d.opt.FormulaGuard && isText(v) && startsFormula(fields[i]) {
				fields[i] = "'" + fields[i]
			}
		}
	}
	return d.writeFields(fields)
}

// isText reports whether a value is text, which alone the formula guard
// changes: a negative number stays a number.
func isText(v any) bool {
	switch t := deref(v).(type) {
	case string:
		return true
	case []byte:
		return utf8.Valid(t)
	}
	return false
}

func startsFormula(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return s != "" && strings.ContainsRune("=+-@\t\r\n＝＋－＠", r)
}

func (d *delimitedWriter) Close() error {
	if d.csv != nil {
		d.csv.Flush()
		if err := d.csv.Error(); err != nil {
			return err
		}
	}
	return d.finish()
}

// ---- JSON ----

type jsonWriter struct {
	baseWriter
	lines bool
	keys  []string
	rows  int
}

func uniqueKeys(columns []string) []string {
	seen := make(map[string]bool, len(columns))
	keys := make([]string, len(columns))
	for i, c := range columns {
		key := c
		for n := 2; seen[key]; n++ {
			key = c + "_" + strconv.Itoa(n)
		}
		seen[key] = true
		keys[i] = jsonString(key)
	}
	return keys
}

func (j *jsonWriter) Write(row []any) error {
	if err := j.checkRow(row); err != nil {
		return err
	}
	if !j.lines {
		if j.rows == 0 {
			j.put("[\n")
		} else {
			j.put(",\n")
		}
	}
	j.put("{")
	for i, v := range row {
		if i > 0 {
			j.put(",")
		}
		j.put(j.keys[i])
		j.put(":")
		j.put(jsonValue(v))
	}
	j.put("}")
	if j.lines {
		j.put("\n")
	}
	j.rows++
	return j.err
}

func (j *jsonWriter) Close() error {
	if !j.lines {
		if j.rows == 0 {
			j.put("[]\n")
		} else {
			j.put("\n]\n")
		}
	}
	return j.finish()
}

// ---- SQL ----

type sqlWriter struct {
	baseWriter
	prefix  string
	inBatch int
}

func newSQLWriter(base baseWriter) *sqlWriter {
	quote := base.opt.Quote
	if quote == nil {
		quote = quoteDuckIdent
	}
	name := base.opt.Table
	if name == "" {
		name = "exported"
	}
	table := quote(name)
	if base.opt.Schema != "" {
		table = quote(base.opt.Schema) + "." + table
	}
	cols := make([]string, len(base.columns))
	for i, c := range base.columns {
		cols[i] = quote(c)
	}
	return &sqlWriter{baseWriter: base, prefix: "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES"}
}

func sqlLiteral(v any, e db.Engine) string {
	if isNil(v) {
		return "NULL"
	}
	switch e {
	case db.Postgres, db.MySQL, db.ClickHouse, db.DuckDB:
	default:
		// Standard SQL has no escape for NUL; SQLite, whose literals are
		// otherwise standard, joins it in with char(0).
		e = db.SQLite
	}
	v = deref(v)
	switch x := v.(type) {
	case []byte:
		// The cursor turns text columns into strings, so bytes come from a
		// binary column, or are not UTF-8: as text they would load as other
		// bytes, since PostgreSQL reads '\x41' into a bytea as "A".
		return db.Literal(e, x)
	case bool:
		if x {
			return "TRUE"
		}
		return "FALSE"
	case float32:
		if f := float64(x); math.IsNaN(f) || math.IsInf(f, 0) {
			return db.Literal(e, floatText(f, 32))
		}
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return db.Literal(e, floatText(x, 64))
		}
	case string, time.Time:
		return db.Literal(e, text(x))
	}
	if s, ok := scalarText(v); ok {
		return s
	}
	return db.Literal(e, text(v))
}

func (s *sqlWriter) Write(row []any) error {
	if err := s.checkRow(row); err != nil {
		return err
	}
	multi := s.opt.RowsPerInsert > 1
	switch {
	case !multi:
		s.put(s.prefix + " (")
	case s.inBatch == 0:
		s.put(s.prefix + "\n  (")
	default:
		s.put(",\n  (")
	}
	for i, v := range row {
		if i > 0 {
			s.put(", ")
		}
		s.put(sqlLiteral(v, s.opt.Engine))
	}
	s.put(")")
	s.inBatch++
	if !multi || s.inBatch == s.opt.RowsPerInsert {
		s.put(";\n")
		s.inBatch = 0
	}
	return s.err
}

func (s *sqlWriter) Close() error {
	if s.inBatch > 0 {
		s.put(";\n")
	}
	return s.finish()
}

// ---- Markdown ----

type markdownWriter struct{ baseWriter }

var markdownEscaper = strings.NewReplacer(`\`, `\\`, "&", "&amp;", "<", "&lt;", `|`, `\|`, "\r\n", "<br>", "\n", "<br>", "\r", "<br>")

func (m *markdownWriter) line(cells []string) {
	m.put("|")
	for _, c := range cells {
		m.put(" " + markdownEscaper.Replace(c) + " |")
	}
	m.put("\n")
}

func (m *markdownWriter) writeHeader() {
	m.line(m.columns)
	m.put("|")
	for range m.columns {
		m.put(" --- |")
	}
	m.put("\n")
}

func (m *markdownWriter) Write(row []any) error {
	if err := m.checkRow(row); err != nil {
		return err
	}
	cells := make([]string, len(row))
	for i, v := range row {
		if isNil(v) {
			cells[i] = m.opt.NullText
		} else {
			cells[i] = text(v)
		}
	}
	m.line(cells)
	return m.err
}

func (m *markdownWriter) Close() error { return m.finish() }

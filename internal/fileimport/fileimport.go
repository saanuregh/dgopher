// Package fileimport reads the rows of a data file to import into a
// table: CSV, JSON and Parquet as DuckDB reads them, with the types it
// finds, and Excel workbooks and XML, which it loads into DuckDB first.
package fileimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dgopher/internal/db"
)

// Format is a kind of file an import reads.
type Format string

const (
	CSV     Format = "csv"
	JSON    Format = "json"
	Parquet Format = "parquet"
	Excel   Format = "xlsx"
	XML     Format = "xml"
)

// Extensions are the file extensions of the formats, for a file chooser.
var Extensions = []string{"csv", "tsv", "txt", "json", "jsonl", "ndjson", "parquet", "xlsx", "xml"}

// FormatOf is the format of a file by its extension.
func FormatOf(path string) (Format, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv", ".tsv", ".txt":
		return CSV, true
	case ".json", ".jsonl", ".ndjson":
		return JSON, true
	case ".parquet":
		return Parquet, true
	case ".xlsx":
		return Excel, true
	case ".xml":
		return XML, true
	}
	return "", false
}

func (f Format) Label() string {
	switch f {
	case CSV:
		return "CSV"
	case JSON:
		return "JSON"
	case Parquet:
		return "Parquet"
	case Excel:
		return "Excel workbook"
	case XML:
		return "XML"
	}
	return string(f)
}

// Header says whether the first row of a CSV file or a sheet names the
// columns.
type Header int

const (
	HeaderDetect Header = iota
	HeaderFirstRow
	HeaderNone
)

// Options say how to read a file; their zero value finds out what it can.
type Options struct {
	// Delimiter separates a CSV file's values, 0 to detect it.
	Delimiter rune
	Header    Header
	// AllText reads every column as text, as a file of mixed types needs.
	AllText bool
	// Sheet is the sheet of a workbook to read, "" for the first.
	Sheet string
	// Rows is the element of an XML file each of which is a row, as a
	// path from the root such as "orders/order"; "" for the most repeated.
	Rows string
}

// Column is a column of a file, with the type DuckDB reads it as.
type Column struct {
	Name, Type string
}

// File is a data file open for an import.
type File struct {
	Path    string
	Format  Format
	Columns []Column
	// Delimiter and HasHeader are how a CSV file was read, as given or as
	// detected.
	Delimiter rune
	HasHeader bool
	// Sheets are a workbook's sheets, and Sheet the one read.
	Sheets []string
	Sheet  string
	// Rows is the element of an XML file read as rows.
	Rows string

	db   *sql.DB
	link string // a link to Path without glob characters, to remove
}

// Open reads what a file holds: its columns, and how to read its rows.
func Open(ctx context.Context, path string, opt Options) (*File, error) {
	format, ok := FormatOf(path)
	if !ok {
		return nil, fmt.Errorf("%s: DGopher imports CSV, JSON, Parquet, Excel and XML files", filepath.Base(path))
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	sqldb, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	// One connection, for the source view to stay where it was made.
	sqldb.SetMaxOpenConns(1)
	f := &File{Path: path, Format: format, db: sqldb}
	if err := f.load(ctx, opt); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (f *File) load(ctx context.Context, opt Options) error {
	switch f.Format {
	case Excel:
		return f.loadExcel(ctx, opt)
	case XML:
		return f.loadXML(ctx, opt)
	}
	path, err := f.readablePath()
	if err != nil {
		return err
	}
	lit := quoteString(path)
	var from string
	switch f.Format {
	case CSV:
		from, err = f.csvSource(ctx, lit, opt)
		if err != nil {
			return err
		}
	case JSON:
		from = "read_json(" + lit + ", format = 'auto')"
	case Parquet:
		from = "read_parquet(" + lit + ")"
	}
	cols, err := describe(ctx, f.db, "SELECT * FROM "+from)
	if err != nil {
		return err
	}
	// Nested values, as a JSON object in a column, go as JSON text; exact
	// numbers, which the driver would read as floats, as their digits.
	sel := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = quoteIdent(c.Name)
		switch {
		case nested(c.Type):
			sel[i] = "to_json(" + quoteIdent(c.Name) + ")::VARCHAR AS " + quoteIdent(c.Name)
			cols[i].Type = "JSON"
		case exact(c.Type):
			sel[i] = quoteIdent(c.Name) + "::VARCHAR AS " + quoteIdent(c.Name)
		}
	}
	if _, err := f.db.ExecContext(ctx, "CREATE VIEW src AS SELECT "+strings.Join(sel, ", ")+" FROM "+from); err != nil {
		return err
	}
	f.Columns = cols
	return nil
}

// readablePath is the file's path as DuckDB reads it: DuckDB takes *, ?
// and [ for patterns of many files, so a name holding one is read through
// a link without them.
func (f *File) readablePath() (string, error) {
	if !strings.ContainsAny(f.Path, "*?[") {
		return f.Path, nil
	}
	abs, err := filepath.Abs(f.Path)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "dgopher-import-")
	if err != nil {
		return "", err
	}
	link := filepath.Join(dir, "file"+strings.ToLower(filepath.Ext(f.Path)))
	if err := os.Symlink(abs, link); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	f.link = dir
	return link, nil
}

// csvSource reads a CSV file as given, finding out what is not: its
// delimiter and whether its first row names the columns.
func (f *File) csvSource(ctx context.Context, lit string, opt Options) (string, error) {
	var delim string
	var header bool
	err := f.db.QueryRowContext(ctx, "SELECT Delimiter, HasHeader FROM sniff_csv("+lit+")").Scan(&delim, &header)
	if err != nil {
		return "", fmt.Errorf("reading %s as CSV: %w", filepath.Base(f.Path), err)
	}
	f.Delimiter, f.HasHeader = []rune(delim + ",")[0], header
	if opt.Delimiter != 0 {
		f.Delimiter = opt.Delimiter
	}
	switch opt.Header {
	case HeaderFirstRow:
		f.HasHeader = true
	case HeaderNone:
		f.HasHeader = false
	}
	args := []string{lit, "delim = " + quoteString(string(f.Delimiter)), fmt.Sprintf("header = %v", f.HasHeader)}
	if opt.AllText {
		args = append(args, "all_varchar = true")
	}
	return "read_csv(" + strings.Join(args, ", ") + ")", nil
}

// Close forgets the file.
func (f *File) Close() error {
	if f.link != "" {
		os.RemoveAll(f.link)
	}
	return f.db.Close()
}

// Count counts the rows of the file.
func (f *File) Count(ctx context.Context) (int64, error) {
	var n int64
	err := f.db.QueryRowContext(ctx, "SELECT count(*) FROM src").Scan(&n)
	return n, err
}

// Preview reads the first n rows.
func (f *File) Preview(ctx context.Context, n int) ([][]any, error) {
	var out [][]any
	err := f.query(ctx, fmt.Sprintf("SELECT * FROM src LIMIT %d", n), func(row []any) error {
		out = append(out, row)
		return nil
	})
	return out, err
}

// Read hands every row to each, batch rows at a time, and returns how
// many there were.
func (f *File) Read(ctx context.Context, batch int, each func(rows [][]any) error) (int64, error) {
	var n int64
	rows := make([][]any, 0, batch)
	err := f.query(ctx, "SELECT * FROM src", func(row []any) error {
		rows = append(rows, row)
		if len(rows) < batch {
			return nil
		}
		n += int64(len(rows))
		err := each(rows)
		rows = make([][]any, 0, batch)
		return err
	})
	if err == nil && len(rows) > 0 {
		n += int64(len(rows))
		err = each(rows)
	}
	return n, err
}

func (f *File) query(ctx context.Context, q string, each func([]any) error) error {
	rows, err := f.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := len(f.Columns)
	for rows.Next() {
		vals := make([]any, n)
		ptrs := make([]any, n)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		if err := each(vals); err != nil {
			return err
		}
	}
	return rows.Err()
}

// describe returns the columns of a query, as DuckDB types them.
func describe(ctx context.Context, d *sql.DB, q string) ([]Column, error) {
	rows, err := d.QueryContext(ctx, "DESCRIBE "+q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []Column
	for rows.Next() {
		var c Column
		var null, key, def, extra sql.NullString
		if err := rows.Scan(&c.Name, &c.Type, &null, &key, &def, &extra); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, errors.New("the file has no columns")
	}
	return cols, nil
}

// nested reports whether a DuckDB type holds other values: a struct, a
// map, a list or an array.
func nested(t string) bool {
	return strings.HasPrefix(t, "STRUCT") || strings.HasPrefix(t, "MAP") || strings.HasSuffix(t, "]") || strings.HasPrefix(t, "UNION")
}

// exact reports whether a DuckDB type holds numbers a float64 would
// round: decimals, and integers past 64 bits.
func exact(t string) bool {
	return strings.HasPrefix(t, "DECIMAL") || t == "HUGEINT" || t == "UHUGEINT" || t == "UBIGINT"
}

func quoteString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

var quoteIdent = db.DialectOf(db.DuckDB).Quote

package export

import (
	"bufio"
	"context"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"math/big"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/duckdb/duckdb-go/v2"

	"dgopher/internal/db"
)

// Column describes one result column; DatabaseType picks the Parquet or DuckDB column type.
type Column struct{ Name, DatabaseType string }

// NewFileWriter writes format f to path. On a failed Close the partial file is removed.
func NewFileWriter(path string, f Format, cols []Column, opt Options) (RowWriter, error) {
	switch f {
	case XLSX:
		return newXLSXWriter(path, cols, opt)
	case Parquet, DuckDBFile:
		return newDuckWriter(path, f, cols, opt)
	}
	// Exported rows may be private: the file is the user's alone.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	buf := bufio.NewWriterSize(file, 1<<16)
	w, err := NewWriter(buf, f, names, opt)
	if err != nil {
		file.Close()
		os.Remove(path)
		return nil, err
	}
	return &fileWriter{RowWriter: w, buf: buf, file: file, path: path}, nil
}

type fileWriter struct {
	RowWriter
	buf  *bufio.Writer
	file *os.File
	path string
	err  error
}

func (w *fileWriter) Write(row []any) error {
	if w.err == nil {
		w.err = w.RowWriter.Write(row)
	}
	return w.err
}

func (w *fileWriter) Close() error {
	err := w.err
	if cerr := w.RowWriter.Close(); err == nil {
		err = cerr
	}
	if ferr := w.buf.Flush(); err == nil {
		err = ferr
	}
	if cerr := w.file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(w.path)
	}
	return err
}

// ---- DuckDB-backed formats ----

type duckKind int

const (
	kindVarchar duckKind = iota
	kindBigint
	kindDouble
	kindDecimal
	kindBoolean
	kindDate
	kindTimestamp
	kindTimestampTZ
	kindTime
	kindUUID
	kindBlob
	kindJSON
)

type duckColumn struct {
	kind         duckKind
	width, scale int
}

func (c duckColumn) sqlType() string {
	switch c.kind {
	case kindBigint:
		return "BIGINT"
	case kindDouble:
		return "DOUBLE"
	case kindDecimal:
		return fmt.Sprintf("DECIMAL(%d,%d)", c.width, c.scale)
	case kindBoolean:
		return "BOOLEAN"
	case kindDate:
		return "DATE"
	case kindTimestamp:
		return "TIMESTAMP"
	case kindTimestampTZ:
		return "TIMESTAMPTZ"
	case kindTime:
		return "TIME"
	case kindUUID:
		return "UUID"
	case kindBlob:
		return "BLOB"
	case kindJSON:
		return "JSON"
	}
	return "VARCHAR"
}

var decimalType = regexp.MustCompile(`^(?:decimal|numeric)\s*\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\)$`)

func duckType(databaseType string) duckColumn {
	t := strings.ToLower(strings.TrimSpace(databaseType))
	if m := decimalType.FindStringSubmatch(t); m != nil {
		p, _ := strconv.Atoi(m[1])
		s, _ := strconv.Atoi(m[2])
		switch {
		case p > 38 || p < 1 || s > p:
			return duckColumn{kind: kindVarchar}
		}
		return duckColumn{kind: kindDecimal, width: p, scale: s}
	}
	if strings.HasPrefix(t, "timestamp") && (strings.Contains(t, "with time zone") || strings.HasSuffix(t, "tz")) {
		return duckColumn{kind: kindTimestampTZ}
	}
	if i := strings.IndexAny(t, "( "); i > 0 && !strings.HasPrefix(t, "double precision") {
		t = t[:i]
	}
	switch t {
	case "int", "integer", "int2", "int4", "int8", "smallint", "tinyint", "mediumint", "bigint", "serial", "smallserial",
		"bigserial", "serial4", "serial8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "hugeint", "long":
		return duckColumn{kind: kindBigint}
	case "real", "float", "float4", "float8", "float32", "float64", "double", "double precision":
		return duckColumn{kind: kindDouble}
	case "numeric", "decimal", "money":
		// No precision given: exact up to 29 digits and 9 decimals; a value
		// past that fails the export rather than round.
		return duckColumn{kind: kindDecimal, width: 38, scale: 9}
	case "bool", "boolean":
		return duckColumn{kind: kindBoolean}
	case "date":
		return duckColumn{kind: kindDate}
	case "timestamp", "datetime", "datetime2", "datetime64", "smalldatetime":
		return duckColumn{kind: kindTimestamp}
	case "timestamptz", "datetimeoffset":
		return duckColumn{kind: kindTimestampTZ}
	case "time":
		return duckColumn{kind: kindTime}
	case "uuid", "uniqueidentifier":
		return duckColumn{kind: kindUUID}
	case "blob", "bytea", "binary", "varbinary", "longblob", "mediumblob", "tinyblob", "image":
		return duckColumn{kind: kindBlob}
	case "json", "jsonb":
		return duckColumn{kind: kindJSON}
	}
	return duckColumn{kind: kindVarchar}
}

type duckWriter struct {
	format    Format
	path      string
	table     string
	names     []string
	cols      []duckColumn
	conn      driver.Conn
	connector *duckdb.Connector
	appender  *duckdb.Appender
	created   bool
	err       error
}

var quoteDuckIdent = db.DialectOf(db.DuckDB).Quote

func newDuckWriter(path string, f Format, cols []Column, opt Options) (*duckWriter, error) {
	dsn := ""
	if f == DuckDBFile {
		// duckdb-go reads '?' in a DSN as the start of options.
		if strings.Contains(path, "?") {
			return nil, fmt.Errorf("export: DuckDB file path must not contain '?': %s", path)
		}
		if _, err := os.Stat(path); err == nil {
			return nil, fmt.Errorf("export: %s already exists", path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		dsn = path
	}
	connector, err := duckdb.NewConnector(dsn, nil)
	if err != nil {
		return nil, err
	}
	conn, err := connector.Connect(context.Background())
	if err != nil {
		connector.Close()
		if f == DuckDBFile {
			removeDuckFile(path)
		}
		return nil, err
	}
	table := opt.Table
	if table == "" {
		table = "export"
	}
	w := &duckWriter{format: f, path: path, table: table, conn: conn, connector: connector}
	for _, c := range cols {
		w.names = append(w.names, c.Name)
		w.cols = append(w.cols, duckType(c.DatabaseType))
	}
	return w, nil
}

func removeDuckFile(path string) {
	os.Remove(path)
	os.Remove(path + ".wal")
}

func (w *duckWriter) exec(query string) error {
	_, err := w.conn.(driver.ExecerContext).ExecContext(context.Background(), query, nil)
	return err
}

// create declares the table once the first row shows whether each value fits its column type.
func (w *duckWriter) create(first []any) error {
	for i, v := range first {
		if w.cols[i].kind == kindVarchar || isNil(v) {
			continue
		}
		if _, ok := convertDuck(w.cols[i], v); !ok {
			w.cols[i] = duckColumn{kind: kindVarchar}
		}
	}
	defs := make([]string, len(w.cols))
	for i, c := range w.cols {
		defs[i] = quoteDuckIdent(w.names[i]) + " " + c.sqlType()
	}
	if err := w.exec("CREATE TABLE " + quoteDuckIdent(w.table) + " (" + strings.Join(defs, ", ") + ")"); err != nil {
		return err
	}
	app, err := duckdb.NewAppenderFromConn(w.conn, "", w.table)
	if err != nil {
		return err
	}
	w.appender = app
	w.created = true
	return nil
}

func (w *duckWriter) Write(row []any) error {
	if w.err != nil {
		return w.err
	}
	if len(row) != len(w.cols) {
		return fmt.Errorf("export: row has %d values, want %d", len(row), len(w.cols))
	}
	if !w.created {
		if w.err = w.create(row); w.err != nil {
			return w.err
		}
	}
	vals := make([]driver.Value, len(row))
	for i, v := range row {
		if isNil(v) {
			continue
		}
		out, ok := convertDuck(w.cols[i], v)
		if !ok {
			w.err = fmt.Errorf("export: column %q: value %q does not fit %s", w.names[i], text(v), w.cols[i].sqlType())
			return w.err
		}
		vals[i] = out
	}
	w.err = w.appender.AppendRow(vals...)
	return w.err
}

func (w *duckWriter) Close() error {
	err := w.err
	if err == nil && !w.created {
		err = w.create(make([]any, len(w.cols)))
	}
	if w.appender != nil {
		if cerr := w.appender.Close(); err == nil {
			err = cerr
		}
	}
	if err == nil && w.format == Parquet {
		err = w.exec("COPY " + quoteDuckIdent(w.table) + " TO " + quoteString(w.path, LiteralStandard) + " (FORMAT PARQUET, COMPRESSION ZSTD)")
	}
	if cerr := w.conn.Close(); err == nil {
		err = cerr
	}
	if cerr := w.connector.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(w.path, 0o600) // DuckDB writes files others may read
	}
	if err != nil {
		if w.format == DuckDBFile {
			removeDuckFile(w.path)
		} else {
			os.Remove(w.path)
		}
	}
	return err
}

// ---- value conversion for DuckDB columns ----

var timeLayouts = []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999", "2006-01-02"}

func parseTime(s string, layouts ...string) (time.Time, bool) {
	for _, l := range layouts {
		if t, err := time.Parse(l, strings.TrimSpace(s)); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// rawText returns string and []byte values as text; ok is false for other kinds.
func rawText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case []byte:
		if utf8.Valid(x) {
			return string(x), true
		}
	}
	return "", false
}

// convertDuck returns v in a form the appender accepts for column c; ok is false when it does not fit.
func convertDuck(c duckColumn, v any) (any, bool) {
	v = deref(v)
	switch c.kind {
	case kindBigint:
		return toInt64(v)
	case kindDouble:
		return toFloat64(v)
	case kindDecimal:
		return toDecimal(v, c.width, c.scale)
	case kindBoolean:
		if b, ok := v.(bool); ok {
			return b, true
		}
		if s, ok := rawText(v); ok {
			if b, err := strconv.ParseBool(strings.TrimSpace(s)); err == nil {
				return b, true
			}
		}
		if i, ok := toInt64(v); ok && (i == 0 || i == 1) {
			return i == 1, true
		}
	case kindDate, kindTimestamp, kindTimestampTZ:
		if t, ok := v.(time.Time); ok {
			return t, true
		}
		if s, ok := rawText(v); ok {
			if t, ok := parseTime(s, timeLayouts...); ok {
				return t, true
			}
		}
	case kindTime:
		if t, ok := v.(time.Time); ok {
			return t, true
		}
		if s, ok := rawText(v); ok {
			if t, ok := parseTime(s, "15:04:05.999999999", "15:04:05.999999999Z07:00"); ok {
				return t, true
			}
		}
	case kindUUID:
		return toUUID(v)
	case kindBlob:
		switch x := v.(type) {
		case []byte:
			return x, true
		case string:
			return []byte(x), true
		}
	case kindJSON:
		if s, ok := rawText(v); ok {
			if json.Valid([]byte(s)) {
				return json.RawMessage(s), true
			}
			return nil, false
		}
		return json.RawMessage(jsonValue(v)), true
	default:
		return text(v), true
	}
	return nil, false
}

func toInt64(v any) (any, bool) {
	switch x := v.(type) {
	case bool:
		return nil, false
	case *big.Int:
		if x.IsInt64() {
			return x.Int64(), true
		}
		return nil, false
	case float32:
		return toInt64(float64(x))
	case float64:
		if x == math.Trunc(x) && x >= math.MinInt64 && x < math.MaxInt64 {
			return int64(x), true
		}
		return nil, false
	}
	if s, ok := rawText(v); ok {
		i, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return i, err == nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if u := rv.Uint(); u <= math.MaxInt64 {
			return int64(u), true
		}
	}
	return nil, false
}

func toFloat64(v any) (any, bool) {
	switch x := v.(type) {
	case bool:
		return nil, false
	case float32:
		return float64(x), true
	case float64:
		return x, true
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		return f, true
	}
	if s, ok := rawText(v); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return f, err == nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), true
	}
	return nil, false
}

func toDecimal(v any, width, scale int) (any, bool) {
	var r *big.Rat
	switch x := v.(type) {
	case bool:
		return nil, false
	case *big.Int:
		r = new(big.Rat).SetInt(x)
	case float32, float64:
		f, _ := toFloat64(x)
		if ff := f.(float64); !math.IsNaN(ff) && !math.IsInf(ff, 0) {
			r = new(big.Rat).SetFloat64(ff)
		}
	default:
		s, ok := rawText(v)
		if !ok {
			s, ok = scalarText(v)
		}
		if ok {
			r, _ = new(big.Rat).SetString(strings.TrimSpace(s))
		}
	}
	if r == nil {
		return nil, false
	}
	digits := strings.Replace(r.FloatString(scale), ".", "", 1)
	unscaled, ok := new(big.Int).SetString(digits, 10)
	if !ok || len(strings.TrimPrefix(unscaled.String(), "-")) > width {
		return nil, false
	}
	return duckdb.Decimal{Width: uint8(width), Scale: uint8(scale), Value: unscaled}, true
}

func toUUID(v any) (any, bool) {
	var id duckdb.UUID
	switch x := v.(type) {
	case [16]byte:
		return duckdb.UUID(x), true
	case []byte:
		if len(x) == len(id) {
			copy(id[:], x)
			return id, true
		}
	}
	if s, ok := rawText(v); ok {
		b, err := hex.DecodeString(strings.ReplaceAll(strings.Trim(strings.TrimSpace(s), "{}"), "-", ""))
		if err == nil && len(b) == len(id) {
			copy(id[:], b)
			return id, true
		}
	}
	if s, ok := v.(fmt.Stringer); ok {
		return toUUID(s.String())
	}
	return nil, false
}

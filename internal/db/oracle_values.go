package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	go_ora "github.com/sijms/go-ora/v3"
	"github.com/sijms/go-ora/v3/parameter_coder"
	oratypes "github.com/sijms/go-ora/v3/types"
)

// newOracleDriver is a go-ora driver of a pool's own, which reads
// intervals as Oracle writes them (oracleIntervalCoder): go-ora v3.0.1
// reads a DAY TO SECOND as a YEAR TO MONTH, and either as a time.
func newOracleDriver() *go_ora.OracleDriver {
	drv := go_ora.NewDriver()
	// AddParameterCoder takes the driver from a pool; this one never
	// connects.
	reg := sql.OpenDB(oracleDriverOnly{drv})
	defer reg.Close()
	for _, typ := range []uint16{oratypes.INTERVALYM_DTY, oratypes.INTERVALDS_DTY} {
		// The Go type is one no value the app binds has.
		if err := go_ora.AddParameterCoder(reg, reflect.TypeFor[oracleIntervalCoder](), typ, &oracleIntervalCoder{}); err != nil {
			panic(err) // only when go-ora's driver is not its own type
		}
	}
	return drv
}

// oracleDriverOnly is a connector that only names its driver.
type oracleDriverOnly struct{ drv *go_ora.OracleDriver }

func (oracleDriverOnly) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("not a connection")
}
func (o oracleDriverOnly) Driver() driver.Driver { return o.drv }

// oracleIntervalCoder reads an INTERVAL as Oracle writes it.
type oracleIntervalCoder struct {
	parameter_coder.IntervalParameter
}

func (c *oracleIntervalCoder) Copy() parameter_coder.OracleParameterCoder {
	n := *c
	return &n
}

func (c *oracleIntervalCoder) Decode(parameter_coder.IConnection) (any, error) {
	if len(c.BValue) == 0 {
		return nil, nil
	}
	return oracleIntervalText(c.DataType, c.BValue)
}

// oracleIntervalText writes an interval of type typ, as Oracle sends it,
// as Oracle shows it: +YY-MM, or +DD HH:MI:SS.FFFFFFFFF. Each field is
// offset by 2^31, or by 60; a negative interval's are all negative.
func oracleIntervalText(typ uint16, b []byte) (string, error) {
	field := func(at int) int64 { return int64(binary.BigEndian.Uint32(b[at:])) - 0x80000000 }
	sign := func(negative bool) string {
		if negative {
			return "-"
		}
		return "+"
	}
	abs := func(n int64) int64 { return max(n, -n) }
	switch {
	case typ == oratypes.INTERVALYM_DTY && len(b) >= 5:
		years, months := field(0), int64(b[4])-60
		return fmt.Sprintf("%s%02d-%02d", sign(years < 0 || months < 0), abs(years), abs(months)), nil
	case typ == oratypes.INTERVALDS_DTY && len(b) >= 11:
		days, hours, minutes, seconds, nanos := field(0), int64(b[4])-60, int64(b[5])-60, int64(b[6])-60, field(7)
		negative := days < 0 || hours < 0 || minutes < 0 || seconds < 0 || nanos < 0
		return fmt.Sprintf("%s%02d %02d:%02d:%02d.%09d", sign(negative), abs(days), abs(hours), abs(minutes), abs(seconds), abs(nanos)), nil
	}
	return "", fmt.Errorf("an interval of type %d in %d bytes cannot be read", typ, len(b))
}

// oracleConn is go-ora's connection, whose rows name their columns'
// types (oracleRows), whose times keep their fractions and zones
// (oracleArgs), and whose cancelled statements keep it (oracleCancelled).
type oracleConn struct{ *go_ora.Connection }

func (c oracleConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Connection.QueryContext(ctx, query, oracleArgs(args))
	if ds, ok := rows.(*go_ora.DataSet); ok && err == nil {
		if err := oracleNoLengthColumn(ds); err != nil {
			ds.Close()
			return nil, err
		}
		return oracleRows{ds}, nil
	}
	return rows, c.cancelled(ctx, err)
}

// oracleNoLengthError is a result go-ora v3.0.1 would show no rows of
// though the server has some: one with a column Oracle describes with no
// length, as a NULL or an empty string selected as it is, or a CASE
// always NULL, which go-ora sends no define for, so that the server sends
// no rows, and go-ora reads the empty first batch as the end (and fails
// reading such a column among others, with a TTC error the hint names
// too).
type oracleNoLengthError struct{ column string }

func (e oracleNoLengthError) Error() string {
	what := "a column"
	if e.column != "" {
		what = "its column " + e.column
	}
	return "the result has rows the Oracle driver, go-ora, cannot read, as " + what + " has no type of a length, " +
		"as NULL or '' selected as they are: none were shown"
}

const oracleNoLengthHint = "Give the column a type, as CAST(NULL AS VARCHAR2(1)): the Oracle driver, go-ora, reads no rows of a result " +
	"with a NULL or '' selected as they are, and fails reading one among other columns."

// oracleNoLengthColumn is the error of a result whose first batch go-ora
// read no rows of while the server holds more, which go-ora would end
// there; nil for any other, as one of no rows, or with a LOB or LONG,
// whose rows go-ora reads after. It reads what go-ora keeps unexported
// (v3.0.1), and finds nothing when it cannot.
func oracleNoLengthColumn(ds *go_ora.DataSet) error {
	v := reflect.ValueOf(ds).Elem()
	sets, index := v.FieldByName("resultSets"), v.FieldByName("index")
	if sets.Kind() != reflect.Slice || index.Kind() != reflect.Int || int(index.Int()) >= sets.Len() {
		return nil
	}
	set := sets.Index(int(index.Int()))
	rows, parent := set.FieldByName("rows"), set.FieldByName("parent")
	if rows.Kind() != reflect.Slice || rows.Len() > 0 || parent.Kind() != reflect.Interface || parent.IsNil() {
		return nil
	}
	stmt := parent.Elem()
	if stmt.Kind() == reflect.Pointer {
		stmt = stmt.Elem()
	}
	if stmt.Kind() != reflect.Struct {
		return nil
	}
	more, blob, long := stmt.FieldByName("_hasMoreRows"), stmt.FieldByName("_hasBLOB"), stmt.FieldByName("_hasLONG")
	if more.Kind() != reflect.Bool || blob.Kind() != reflect.Bool || long.Kind() != reflect.Bool || !more.Bool() || blob.Bool() || long.Bool() {
		return nil
	}
	e := oracleNoLengthError{}
	for i, name := range ds.Columns() {
		if col, ok := oracleColumnOf(ds, i); ok && col.maxLen == 0 {
			e.column = name
			break
		}
	}
	return e
}

// oracleTTCError reports go-ora's errors on reading a result it took
// apart wrongly, as one with a column of no length among others.
func oracleTTCError(msg string) bool {
	return strings.HasPrefix(msg, "TTC error: received code") || strings.HasPrefix(msg, "invalid size for GetInt64")
}

func (c oracleConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	res, err := c.Connection.ExecContext(ctx, query, oracleArgs(args))
	return res, c.cancelled(ctx, err)
}

// cancelled is err, an oracleCancelled when ctx ended and the connection
// survived the break go-ora sent.
func (c oracleConn) cancelled(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) && c.IsValid() {
		return oracleCancelled{cause: ctx.Err()}
	}
	return err
}

// oracleCancelled is the error of a statement go-ora stopped as its
// context ended, which keeps the connection and its transaction: the
// context's error, but no network error, which would drop the connection
// (Session.afterError).
type oracleCancelled struct{ cause error }

func (e oracleCancelled) Error() string        { return e.cause.Error() }
func (e oracleCancelled) Is(target error) bool { return target == e.cause }

// oracleArgs are args as Oracle should get them: go-ora sends a time as a
// DATE, without its fraction of a second or its zone, so a time with
// either goes as a TIMESTAMP, or a TIMESTAMP WITH TIME ZONE, instead.
func oracleArgs(args []driver.NamedValue) []driver.NamedValue {
	var out []driver.NamedValue
	for i, a := range args {
		t, ok := a.Value.(time.Time)
		if !ok || t.Location() == time.UTC && t.Nanosecond() == 0 {
			continue
		}
		if out == nil {
			out = slices.Clone(args)
		}
		if t.Location() == time.UTC {
			out[i].Value = *oratypes.NewTimeStamp(t)
		} else {
			out[i].Value = *oratypes.NewTimeStampTZ(t)
		}
	}
	if out == nil {
		return args
	}
	return out
}

// oracleRows are go-ora's rows, which name no column's type.
type oracleRows struct{ *go_ora.DataSet }

func (r oracleRows) ColumnTypeDatabaseTypeName(i int) string {
	col, _ := oracleColumnOf(r.DataSet, i)
	return col.typeName()
}

// oracleColumn is what go-ora keeps of a result's column.
type oracleColumn struct {
	dataType  uint16
	form      int // 2 for the national character set's
	json, xml bool
	maxLen    int64 // as the server described it, in bytes
}

// oracleColumnOf reads the column i of the rows' result, which go-ora
// keeps unexported (go-ora v3.0.1: DataSet.resultSets[index].cols); false
// when it cannot, as after an upgrade renamed them, which
// TestOracleValues catches.
func oracleColumnOf(ds *go_ora.DataSet, i int) (oracleColumn, bool) {
	v := reflect.ValueOf(ds).Elem()
	sets, index := v.FieldByName("resultSets"), v.FieldByName("index")
	if sets.Kind() != reflect.Slice || index.Kind() != reflect.Int || int(index.Int()) >= sets.Len() {
		return oracleColumn{}, false
	}
	cols := sets.Index(int(index.Int())).FieldByName("cols")
	if cols.Kind() != reflect.Pointer || cols.IsNil() || cols.Elem().Kind() != reflect.Slice || i >= cols.Elem().Len() {
		return oracleColumn{}, false
	}
	p := cols.Elem().Index(i)
	dt, form, js, xml, size := p.FieldByName("DataType"), p.FieldByName("CharsetForm"), p.FieldByName("IsJson"), p.FieldByName("IsXmlType"),
		p.FieldByName("MaxLen")
	if dt.Kind() != reflect.Uint16 || form.Kind() != reflect.Int || js.Kind() != reflect.Bool || xml.Kind() != reflect.Bool || size.Kind() != reflect.Int64 {
		return oracleColumn{}, false
	}
	return oracleColumn{dataType: uint16(dt.Uint()), form: int(form.Int()), json: js.Bool(), xml: xml.Bool(), maxLen: size.Int()}, true
}

// typeName is the column's type as Oracle names it, without its sizes; ""
// when unknown.
func (c oracleColumn) typeName() string {
	national := func(name string) string {
		if c.form == 2 {
			return "N" + name
		}
		return name
	}
	switch {
	case c.dataType == oratypes.VECTOR:
		return "VECTOR" // which go-ora marks JSON too
	case c.json:
		return "JSON"
	case c.xml:
		return "XMLTYPE"
	}
	switch c.dataType {
	case oratypes.NCHAR, oratypes.VARCHAR:
		return national("VARCHAR2")
	case oratypes.LongVarChar:
		// go-ora reads a CLOB's text with the row, as a LONG's, and names
		// it so.
		return national("CLOB")
	case oratypes.CHAR:
		return national("CHAR")
	case oratypes.NUMBER:
		return "NUMBER"
	case oratypes.LONG:
		return "LONG"
	case oratypes.RAW:
		return "RAW"
	case oratypes.LongRaw:
		// go-ora reads a BLOB's bytes with the row, as a LONG RAW's, and
		// names it so: the two are told apart no more.
		return "BLOB"
	case oratypes.DATE:
		return "DATE"
	case oratypes.TimeStampDTY, oratypes.TIMESTAMP:
		return "TIMESTAMP"
	case oratypes.TimeStampTZ_DTY, oratypes.TIMESTAMPTZ:
		return "TIMESTAMP WITH TIME ZONE"
	case oratypes.TimeStampLTZ_DTY, oratypes.TimeStampLTZ:
		return "TIMESTAMP WITH LOCAL TIME ZONE"
	case oratypes.INTERVALYM_DTY:
		return "INTERVAL YEAR TO MONTH"
	case oratypes.INTERVALDS_DTY:
		return "INTERVAL DAY TO SECOND"
	case oratypes.IBFLOAT:
		return "BINARY_FLOAT"
	case oratypes.IBDOUBLE:
		return "BINARY_DOUBLE"
	case oratypes.OCIClobLocator:
		return national("CLOB")
	case oratypes.OCIBlobLocator:
		return "BLOB"
	case oratypes.OCIFileLocator:
		return "BFILE"
	case oratypes.ROWID:
		return "ROWID"
	case oratypes.UROWID:
		return "UROWID"
	case oratypes.JSON:
		return "JSON"
	case oratypes.BOOLEAN:
		return "BOOLEAN"
	case oratypes.XMLType, oratypes.OCIXMLType:
		return "XMLTYPE"
	case oratypes.REFCURSOR:
		return "REF CURSOR"
	}
	return ""
}

// oracleValue turns what go-ora gives for a JSON value, and a VECTOR's
// numbers, into their text.
func oracleValue(typ string, v any) any {
	switch x := v.(type) {
	case *oratypes.Json:
		var s string
		if err := x.CopyTo(&s); err != nil {
			return fmt.Sprintf("(a JSON value the driver cannot read: %v)", err)
		}
		return s
	case []float32, []float64, []uint8, []int8:
		if typ == "VECTOR" {
			return vectorText(reflect.ValueOf(x))
		}
	}
	return v
}

// vectorText writes a vector's numbers as Oracle shows them: [1,2.5].
func vectorText(v reflect.Value) string {
	parts := make([]string, v.Len())
	for i := range parts {
		switch e := v.Index(i); e.Kind() {
		case reflect.Float32:
			parts[i] = strconv.FormatFloat(e.Float(), 'g', -1, 32)
		case reflect.Float64:
			parts[i] = strconv.FormatFloat(e.Float(), 'g', -1, 64)
		case reflect.Int8:
			parts[i] = strconv.FormatInt(e.Int(), 10)
		default:
			parts[i] = strconv.FormatUint(e.Uint(), 10)
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// oracleXMLTypeError is go-ora's error for a result with an XMLType
// column, which it cannot read; DescribeError adds oracleXMLTypeHint.
const (
	oracleXMLTypeError = "no parameter coder registered for name XMLTYPE"
	oracleXMLTypeHint  = "The driver cannot read XMLType columns: select them as text, with XMLSERIALIZE(CONTENT column AS CLOB) or column.getClobVal()."
)

// oracleCatalogView finds, in ORA-00942, which 23ai's names the missing
// view in, one of Oracle's V$ or DBA_ views; oracleCatalogHint says what
// reading them needs.
var oracleCatalogView = regexp.MustCompile(`"SYS"\."(?:V_\$|GV_\$|DBA_)`)

const oracleCatalogHint = "Oracle's V$ and DBA_ views, which Explain Analyze, the activity pages and the users tab read, " +
	"need SELECT_CATALOG_ROLE, or SELECT ANY DICTIONARY: ask a DBA to grant one."

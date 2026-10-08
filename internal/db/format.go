package db

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/duckdb/duckdb-go/v2"
)

// Display returns how a value shows in full, as in a value viewer.
func Display(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		return x
	case []byte:
		if utf8.Valid(x) {
			return string(x)
		}
		return `\x` + hex.EncodeToString(x)
	case time.Time:
		return formatTime(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32)
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case *big.Int:
		return x.String()
	case duckdb.Interval:
		return formatInterval(x)
	case duckdb.OrderedMap:
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
	case fmt.Stringer:
		return x.String()
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return "NULL"
		}
		return Display(rv.Elem().Interface())
	case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return fmt.Sprint(v)
}

// Cell returns how a value shows in a grid cell: one line, cut short.
func Cell(v any, max int) string {
	s := Display(v)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i] + " ⏎…"
	}
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max]) + "…"
	}
	return s
}

func formatTime(t time.Time) string {
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0 && t.Location() == time.UTC {
		return t.Format("2006-01-02")
	}
	s := t.Format("2006-01-02 15:04:05.999999")
	if t.Location() != time.UTC {
		s += t.Format(" -07:00")
	}
	return s
}

// formatInterval writes a DuckDB interval as DuckDB does: "1 year
// 2 months 3 days 04:05:06".
func formatInterval(iv duckdb.Interval) string {
	var parts []string
	unit := func(n int64, name string) {
		if n != 0 {
			if n != 1 && n != -1 {
				name += "s"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, name))
		}
	}
	unit(int64(iv.Months/12), "year")
	unit(int64(iv.Months%12), "month")
	unit(int64(iv.Days), "day")
	if iv.Micros != 0 || len(parts) == 0 {
		m, sign := iv.Micros, ""
		if m < 0 {
			m, sign = -m, "-"
		}
		t := fmt.Sprintf("%s%02d:%02d:%02d", sign, m/3600e6, m/60e6%60, m/1e6%60)
		if f := m % 1e6; f != 0 {
			t += strings.TrimRight(fmt.Sprintf(".%06d", f), "0")
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, " ")
}

// IsNumeric reports whether a value is a number, which grids align right.
func IsNumeric(v any) bool {
	switch v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, *big.Int:
		return true
	}
	return false
}

// IsNumericType reports whether a database type name is numeric.
func IsNumericType(t string) bool {
	t = strings.ToUpper(t)
	for _, p := range []string{"INT", "DECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "REAL", "SERIAL", "MONEY", "UINT", "HUGEINT"} {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

// Literal writes a value as a SQL literal of an engine, for SQL the user
// reads and may edit, such as a filter. Statements the app runs itself
// pass values as parameters instead.
func Literal(e Engine, v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if x {
			return "TRUE"
		}
		return "FALSE"
	case []byte:
		return bytesLiteral(e, x)
	}
	if IsNumeric(v) {
		return Display(v)
	}
	s := Display(v)
	switch e {
	case MySQL:
		r := strings.NewReplacer(`\`, `\\`, `'`, `''`, "\x00", `\0`, "\n", `\n`, "\r", `\r`, "\x1a", `\Z`)
		return "'" + r.Replace(s) + "'"
	case ClickHouse:
		r := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
		return "'" + r.Replace(s) + "'"
	}
	// Standard SQL, as PostgreSQL (standard_conforming_strings), SQLite and
	// DuckDB read it: a quote is doubled, a backslash is itself.
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// bytesLiteral writes binary data as each engine reads a binary literal:
// as text it would be stored, or compared, as other bytes.
func bytesLiteral(e Engine, b []byte) string {
	h := hex.EncodeToString(b)
	switch e {
	case Postgres:
		return `'\x` + h + `'::bytea`
	case DuckDB:
		var s strings.Builder
		for _, c := range b {
			fmt.Fprintf(&s, `\x%02X`, c)
		}
		return "'" + s.String() + "'::BLOB"
	case ClickHouse:
		return "unhex('" + h + "')"
	}
	return "X'" + h + "'"
}

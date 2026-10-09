package db

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/duckdb/duckdb-go/v2"
)

// ValueText writes a value of a column of a DuckDB type, as a file read
// by DuckDB, or CanonicalType, gives it, as text a database reads back as
// the same value, for a statement to cast to the column it fills; bytes
// stay bytes.
func ValueText(v any, colType string) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return x
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case time.Time:
		switch {
		case colType == "DATE":
			return x.Format(time.DateOnly)
		case strings.HasPrefix(colType, "TIME") && !strings.HasPrefix(colType, "TIMESTAMP"):
			return x.Format("15:04:05.999999")
		case strings.HasSuffix(colType, "WITH TIME ZONE") || colType == "TIMESTAMPTZ":
			return x.Format("2006-01-02 15:04:05.999999-07:00")
		}
		return x.Format("2006-01-02 15:04:05.999999")
	case duckdb.Decimal:
		return x.String()
	case duckdb.UUID:
		return x.String()
	case *big.Int:
		return x.String()
	case float32, float64:
		return fmt.Sprint(x)
	case map[string]any, []any:
		if out, err := json.Marshal(x); err == nil {
			return string(out)
		}
	}
	return fmt.Sprint(v)
}

// InsertValue is a value of a column of a DuckDB type, as a file read by
// DuckDB or CanonicalType gives it, as an INSERT into an engine gives it: text the
// statement casts to its column, bytes as they are, or nil for NULL.
// A boolean, read as true or as 1 alike, is true or false; MySQL and
// SQLite keep it as 1 or 0, where true would be text.
func InsertValue(e Engine, v any, fileType string) any {
	if at, ok := v.(time.Time); ok && e == ClickHouse && (fileType == "TIMESTAMPTZ" || strings.HasSuffix(fileType, "WITH TIME ZONE")) {
		// ClickHouse reads a time with an offset as NULL, without a word:
		// its column keeps UTC, which the time is written in.
		v, fileType = at.UTC(), "TIMESTAMP"
	}
	t := ValueText(v, fileType)
	switch x := t.(type) {
	case nil:
		return nil
	case []byte:
		return x
	case string:
		if b, ok := booleanText(x); ok && fileType == "BOOLEAN" {
			switch {
			case e != MySQL && e != SQLite:
				return Typed(b)
			case b == "true":
				return Typed("1")
			}
			return Typed("0")
		}
		return Typed(x)
	}
	return t
}

// booleanText reads a boolean as the engines write it, true as true, t
// or 1, as true or false.
func booleanText(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "y", "yes", "on":
		return "true", true
	case "0", "f", "false", "n", "no", "off":
		return "false", true
	}
	return "", false
}

package db

import (
	"regexp"
	"strconv"
	"strings"
)

// NewColumn is a column of a table to create.
type NewColumn struct {
	Name, Type string
}

// CreateTableSQL writes the statement creating a table of columns. On
// ClickHouse every column may hold NULL, as a file's may, and the table
// is a MergeTree, kept in the order the rows came.
func CreateTableSQL(d Dialect, schema, table string, cols []NewColumn) string {
	defs := make([]string, len(cols))
	for i, c := range cols {
		typ := c.Type
		if d.Engine() == ClickHouse && !strings.HasPrefix(typ, "Nullable(") {
			typ = "Nullable(" + typ + ")"
		}
		defs[i] = "  " + d.Quote(c.Name) + " " + typ
	}
	sql := "CREATE TABLE " + QualifiedName(d, schema, table) + " (\n" + strings.Join(defs, ",\n") + "\n)"
	if d.Engine() == ClickHouse {
		sql += " ENGINE = MergeTree ORDER BY tuple()"
	}
	return sql
}

var duckDecimal = regexp.MustCompile(`^DECIMAL\((\d+),\s*(\d+)\)$`)

// ColumnType is the type of an engine's column that holds a DuckDB type's
// values exactly, as a file read by DuckDB gives them: SQLite keeps as
// text what its numbers and dates would round or lose.
func ColumnType(e Engine, duckType string) string {
	t := strings.ToUpper(strings.TrimSpace(duckType))
	pick := func(postgres, mysql, sqlite, clickhouse, duckdb string) string {
		switch e {
		case Postgres:
			return postgres
		case MySQL:
			return mysql
		case SQLite:
			return sqlite
		case ClickHouse:
			return clickhouse
		}
		return duckdb
	}
	if m := duckDecimal.FindStringSubmatch(t); m != nil {
		p, _ := strconv.Atoi(m[1])
		dec := "(" + m[1] + "," + m[2] + ")"
		sqlite := "NUMERIC"
		if p > 15 {
			sqlite = "TEXT" // past a float's digits, which NUMERIC rounds to
		}
		return pick("numeric"+dec, "DECIMAL"+dec, sqlite, "Decimal"+dec, "DECIMAL"+dec)
	}
	switch t {
	case "BOOLEAN":
		return pick("boolean", "BOOLEAN", "INTEGER", "Bool", "BOOLEAN")
	case "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "UTINYINT", "USMALLINT", "UINTEGER":
		return pick("bigint", "BIGINT", "INTEGER", "Int64", "BIGINT")
	case "UBIGINT", "HUGEINT", "UHUGEINT":
		return pick("numeric(39,0)", "DECIMAL(39,0)", "TEXT", "Int128", "HUGEINT")
	case "FLOAT":
		return pick("real", "FLOAT", "REAL", "Float32", "FLOAT")
	case "DOUBLE":
		return pick("double precision", "DOUBLE", "REAL", "Float64", "DOUBLE")
	case "DATE":
		return pick("date", "DATE", "TEXT", "Date32", "DATE")
	case "TIMESTAMP", "TIMESTAMP_S", "TIMESTAMP_MS", "TIMESTAMP_NS":
		return pick("timestamp", "DATETIME(6)", "TEXT", "DateTime64(6)", "TIMESTAMP")
	case "TIMESTAMP WITH TIME ZONE", "TIMESTAMPTZ":
		return pick("timestamptz", "DATETIME(6)", "TEXT", "DateTime64(6, 'UTC')", "TIMESTAMPTZ")
	case "TIME":
		return pick("time", "TIME(6)", "TEXT", "String", "TIME")
	case "UUID":
		return pick("uuid", "CHAR(36)", "TEXT", "UUID", "UUID")
	case "JSON":
		return pick("jsonb", "JSON", "TEXT", "String", "JSON")
	case "BLOB":
		return pick("bytea", "LONGBLOB", "BLOB", "String", "BLOB")
	}
	return pick("text", "LONGTEXT", "TEXT", "String", "VARCHAR")
}

// InsertBatch is how many rows of cols columns a multi-row INSERT holds:
// at most a thousand, within the parameters a statement of the engine may
// have.
func InsertBatch(e Engine, cols int) int {
	params := 65535
	if e == SQLite {
		params = 32766
	}
	return max(1, min(1000, params/max(1, cols)))
}

// InsertRows writes one INSERT of rows into cols, whose values are Typed
// text the statement casts to their columns, or nil for NULL.
func (t *EditTarget) InsertRows(cols []Column, rows [][]any) Statement {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = t.Dialect.Quote(c.Name)
	}
	var args []any
	tuples := make([]string, len(rows))
	vals := make([]string, len(cols))
	for r, row := range rows {
		for i, c := range cols {
			vals[i] = t.value(c, row[i], &args)
		}
		tuples[r] = "(" + strings.Join(vals, ", ") + ")"
	}
	return Statement{SQL: "INSERT INTO " + QualifiedName(t.Dialect, t.Schema, t.Table) + " (" + strings.Join(names, ", ") + ") VALUES " + strings.Join(tuples, ", "),
		Args: args, Want: -1}
}

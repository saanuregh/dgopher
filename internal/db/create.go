package db

import (
	"regexp"
	"strconv"
	"strings"

	"dgopher/internal/lists"
)

// NewColumn is a column of a table to create.
type NewColumn struct {
	Name, Type string
}

// CreateTableSQL writes the statement creating a table of columns. On
// ClickHouse every column may hold NULL, as a file's may, and the table
// is a MergeTree, kept in the order the rows came.
func CreateTableSQL(d Dialect, schema, table string, cols []NewColumn) string {
	r := rulesOfDialect(d)
	defs := make([]string, len(cols))
	for i, c := range cols {
		typ := c.Type
		if r.NullableWrapper() && !strings.HasPrefix(typ, "Nullable(") {
			typ = "Nullable(" + typ + ")"
		}
		defs[i] = "  " + d.Quote(c.Name) + " " + typ
	}
	return "CREATE TABLE " + QualifiedName(d, schema, table) + " (\n" + strings.Join(defs, ",\n") + "\n)" + r.CreateTableSuffix(d, TableDesign{}, nil)
}

var duckDecimal = regexp.MustCompile(`^DECIMAL\((\d+),\s*(\d+)\)$`)

// ColumnType is the type of an engine's column that holds a DuckDB type's
// values exactly, as a file read by DuckDB gives them: SQLite keeps as
// text what its numbers and dates would round or lose. An engine without
// SQL rules gets DuckDB's.
func ColumnType(e Engine, duckType string) string {
	if r, ok := sqlRulesOf(e); ok {
		return r.ColumnTypeFor(duckType)
	}
	return columnTypesFor(duckType).duckdb
}

// engineTypes are a type of each engine.
type engineTypes struct{ postgres, mysql, sqlite, clickhouse, duckdb string }

// columnTypesFor are the types of each engine's column that hold a DuckDB
// type's values exactly.
func columnTypesFor(duckType string) engineTypes {
	t := strings.ToUpper(strings.TrimSpace(duckType))
	if m := duckDecimal.FindStringSubmatch(t); m != nil {
		p, _ := strconv.Atoi(m[1])
		dec := "(" + m[1] + "," + m[2] + ")"
		sqlite := "NUMERIC"
		if p > 15 {
			sqlite = "TEXT" // past a float's digits, which NUMERIC rounds to
		}
		return engineTypes{"numeric" + dec, "DECIMAL" + dec, sqlite, "Decimal" + dec, "DECIMAL" + dec}
	}
	switch t {
	case "BOOLEAN":
		return engineTypes{"boolean", "BOOLEAN", "INTEGER", "Bool", "BOOLEAN"}
	case "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "UTINYINT", "USMALLINT", "UINTEGER":
		return engineTypes{"bigint", "BIGINT", "INTEGER", "Int64", "BIGINT"}
	case "UBIGINT", "HUGEINT", "UHUGEINT":
		return engineTypes{"numeric(39,0)", "DECIMAL(39,0)", "TEXT", "Int128", "HUGEINT"}
	case "FLOAT":
		return engineTypes{"real", "FLOAT", "REAL", "Float32", "FLOAT"}
	case "DOUBLE":
		return engineTypes{"double precision", "DOUBLE", "REAL", "Float64", "DOUBLE"}
	case "DATE":
		return engineTypes{"date", "DATE", "TEXT", "Date32", "DATE"}
	case "TIMESTAMP", "TIMESTAMP_S", "TIMESTAMP_MS", "TIMESTAMP_NS":
		return engineTypes{"timestamp", "DATETIME(6)", "TEXT", "DateTime64(6)", "TIMESTAMP"}
	case "TIMESTAMP WITH TIME ZONE", "TIMESTAMPTZ":
		return engineTypes{"timestamptz", "DATETIME(6)", "TEXT", "DateTime64(6, 'UTC')", "TIMESTAMPTZ"}
	case "TIME":
		return engineTypes{"time", "TIME(6)", "TEXT", "String", "TIME"}
	case "UUID":
		return engineTypes{"uuid", "CHAR(36)", "TEXT", "UUID", "UUID"}
	case "JSON":
		return engineTypes{"jsonb", "JSON", "TEXT", "String", "JSON"}
	case "BLOB":
		return engineTypes{"bytea", "LONGBLOB", "BLOB", "String", "BLOB"}
	}
	return engineTypes{"text", "LONGTEXT", "TEXT", "String", "VARCHAR"}
}

// InsertBatch is how many rows of cols columns a multi-row INSERT holds:
// at most a thousand, within the parameters a statement of the engine may
// have.
func InsertBatch(e Engine, cols int) int {
	params := 65535
	if r, ok := sqlRulesOf(e); ok {
		params = r.MaxParameters()
	}
	return max(1, min(1000, params/max(1, cols)))
}

// InsertRows writes one INSERT of rows into cols, whose values are Typed
// text the statement casts to their columns, or nil for NULL.
func (t *EditTarget) InsertRows(cols []Column, rows [][]any) Statement {
	names := lists.Map(cols, func(c Column) string { return t.Dialect.Quote(c.Name) })
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

// RenameObjectSQL writes the statement renaming a table or a view.
func RenameObjectSQL(d Dialect, obj Object, name string) (string, error) {
	return rulesOfDialect(d).RenameObjectSQL(obj, name)
}

// renameTableSQL renames with RENAME TABLE, which renames views too, and
// may move between databases: the new name stays in the same one.
func renameTableSQL(d Dialect, obj Object, name string) string {
	return "RENAME TABLE " + QualifiedName(d, obj.Schema, obj.Name) + " TO " + QualifiedName(d, obj.Schema, name)
}

// alterRenameSQL renames an object with ALTER, as standard SQL does.
func alterRenameSQL(d Dialect, obj Object, name string) string {
	kind := "TABLE"
	switch obj.Kind {
	case KindView:
		kind = "VIEW"
	case KindMaterializedView:
		kind = "MATERIALIZED VIEW"
	case KindForeignTable:
		kind = "FOREIGN TABLE"
	}
	return "ALTER " + kind + " " + QualifiedName(d, obj.Schema, obj.Name) + " RENAME TO " + d.Quote(name)
}

// RenameColumnSQL writes the statement renaming a column of a table.
func RenameColumnSQL(d Dialect, schema, table, column, name string) string {
	return "ALTER TABLE " + QualifiedName(d, schema, table) + " RENAME COLUMN " + d.Quote(column) + " TO " + d.Quote(name)
}

package db

import (
	"regexp"
	"strings"
)

var (
	sizedDecimal = regexp.MustCompile(`(?i)^(?:numeric|decimal|dec)\s*\(\s*(\d+)\s*,\s*(\d+)\s*\)`)
	typeArgs     = regexp.MustCompile(`\([^)]*\)`)
)

// CanonicalType is an engine's column type as the type of DuckDB that
// holds its values, as ColumnType reads it: the vocabulary a table copied
// to another engine is typed through. What has no such type, as arrays
// and ranges, is text, which keeps the values as they read.
func CanonicalType(e Engine, typ string) string {
	t := strings.TrimSpace(typ)
	if e == ClickHouse {
		return clickhouseCanonical(t)
	}
	if m := sizedDecimal.FindStringSubmatch(t); m != nil {
		return "DECIMAL(" + m[1] + "," + m[2] + ")"
	}
	lower := strings.ToLower(t)
	if e == MySQL && lower == "tinyint(1)" {
		return "BOOLEAN"
	}
	// The type's name without its sizes, as "timestamp with time zone"
	// for timestamp(3) with time zone, or "int unsigned".
	name := strings.Join(strings.Fields(typeArgs.ReplaceAllString(lower, "")), " ")
	if e == SQLite {
		return sqliteCanonical(name)
	}
	first, _, _ := strings.Cut(name, " ")
	switch {
	case name == "bool" || name == "boolean":
		return "BOOLEAN"
	case first == "bigint" && strings.Contains(name, "unsigned"):
		return "UBIGINT"
	case strings.HasPrefix(name, "timestamp") && strings.Contains(name, "with time zone"), name == "timestamptz":
		return "TIMESTAMPTZ"
	case strings.HasPrefix(name, "timestamp"), name == "datetime":
		return "TIMESTAMP"
	case first == "time", name == "timetz":
		return "TIME"
	case name == "double precision":
		return "DOUBLE"
	}
	switch first {
	case "smallint", "int2", "integer", "int", "int4", "bigint", "int8", "mediumint", "tinyint", "smallserial", "serial", "bigserial", "year", "hugeint":
		return "BIGINT"
	case "real", "float4", "float":
		return "FLOAT"
	case "double", "float8":
		return "DOUBLE"
	case "date":
		return "DATE"
	case "uuid":
		return "UUID"
	case "json", "jsonb":
		return "JSON"
	case "bytea", "blob", "tinyblob", "mediumblob", "longblob", "binary", "varbinary":
		return "BLOB"
	}
	return "VARCHAR"
}

// sqliteCanonical finds a SQLite type's affinity in its name, as SQLite's
// documentation does; its dates are text the target reads.
func sqliteCanonical(name string) string {
	switch {
	case strings.Contains(name, "int"):
		return "BIGINT"
	case strings.Contains(name, "char"), strings.Contains(name, "clob"), strings.Contains(name, "text"):
		return "VARCHAR"
	case strings.Contains(name, "blob"), name == "":
		return "BLOB"
	case strings.Contains(name, "real"), strings.Contains(name, "floa"), strings.Contains(name, "doub"):
		return "DOUBLE"
	case name == "bool" || name == "boolean":
		return "BOOLEAN"
	case name == "date":
		return "DATE"
	case strings.Contains(name, "datetime"), strings.Contains(name, "timestamp"):
		return "TIMESTAMP"
	}
	return "VARCHAR"
}

func clickhouseCanonical(t string) string {
	t = clickhouseInnerType(t)
	if m := sizedDecimal.FindStringSubmatch(t); m != nil {
		return "DECIMAL(" + m[1] + "," + m[2] + ")"
	}
	switch {
	case t == "UInt64":
		return "UBIGINT"
	case strings.HasPrefix(t, "Int"), strings.HasPrefix(t, "UInt"):
		return "BIGINT"
	case t == "Float32":
		return "FLOAT"
	case t == "Float64":
		return "DOUBLE"
	case t == "Bool":
		return "BOOLEAN"
	case strings.HasPrefix(t, "DateTime"):
		return "TIMESTAMP"
	case strings.HasPrefix(t, "Date"):
		return "DATE"
	case t == "UUID":
		return "UUID"
	}
	return "VARCHAR"
}

// CopyDesign is the design of a table to copy another's rows into, on
// the same engine or another: the same columns, as nullable, the same
// primary key, typed as the target holds their values exactly; on the
// same engine, as they were.
func CopyDesign(from Engine, cols []Column, to Engine, schema, table string) TableDesign {
	t := TableDesign{Schema: schema, Name: table}
	for _, c := range cols {
		typ := c.Type
		switch {
		case from != to:
			typ = ColumnType(to, CanonicalType(from, c.Type))
		case to == ClickHouse:
			// The design writes Nullable itself, from Nullable.
			if inner, ok := strings.CutPrefix(typ, "Nullable("); ok {
				typ = strings.TrimSuffix(inner, ")")
			}
		}
		t.Columns = append(t.Columns, ColumnDesign{Name: c.Name, Type: typ, Nullable: c.Nullable && !c.PrimaryKey, PrimaryKey: c.PrimaryKey})
	}
	return t
}

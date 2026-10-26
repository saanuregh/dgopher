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
	if r, ok := sqlRulesOf(e); ok {
		return r.CanonicalType(typ)
	}
	return commonCanonical(strings.TrimSpace(typ))
}

// typeName is a type's name in lower case without its sizes, as
// "timestamp with time zone" for timestamp(3) with time zone, or "int
// unsigned".
func typeName(t string) string {
	return strings.Join(strings.Fields(typeArgs.ReplaceAllString(strings.ToLower(t), "")), " ")
}

// commonCanonical reads a type by the names most engines share.
func commonCanonical(t string) string {
	if m := sizedDecimal.FindStringSubmatch(t); m != nil {
		return "DECIMAL(" + m[1] + "," + m[2] + ")"
	}
	name := typeName(t)
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
	r, ok := sqlRulesOf(to)
	wrapsNullable := ok && r.NullableWrapper()
	for _, c := range cols {
		typ := c.Type
		switch {
		case from != to:
			typ = ColumnType(to, CanonicalType(from, c.Type))
		case wrapsNullable:
			// The design writes Nullable itself, from Nullable.
			if inner, ok := strings.CutPrefix(typ, "Nullable("); ok {
				typ = strings.TrimSuffix(inner, ")")
			}
		}
		t.Columns = append(t.Columns, ColumnDesign{Name: c.Name, Type: typ, Nullable: c.Nullable && !c.PrimaryKey, PrimaryKey: c.PrimaryKey})
	}
	return t
}

var (
	typeSize    = regexp.MustCompile(`\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\)`)
	typeWrapper = regexp.MustCompile(`^(?:Nullable|LowCardinality)\((.*)\)$`)
)

// TypeKindOf is the kind of an engine's column type, with its length or
// precision and its scale: "" for a type of no kind. An engine without
// SQL rules reads the names most engines share.
func TypeKindOf(e Engine, typ string) (kind TypeKind, size, scale string) {
	if r, ok := sqlRulesOf(e); ok {
		return r.TypeKindOf(typ)
	}
	return commonTypeKind(typ, false, false)
}

// commonTypeKind reads a type by the names most engines share; MySQL's
// tinyint(1) as a boolean with tinyintBoolean, and every integer as 64
// bits, as SQLite's are, with wideIntegers.
func commonTypeKind(typ string, tinyintBoolean, wideIntegers bool) (kind TypeKind, size, scale string) {
	t := strings.TrimSpace(typ)
	if m := typeSize.FindStringSubmatch(t); m != nil {
		size, scale = m[1], m[2]
	}
	lower := strings.ToLower(t)
	name := strings.Join(strings.Fields(typeSize.ReplaceAllString(lower, " ")), " ")
	first, _, _ := strings.Cut(name, " ")
	unsigned := strings.Contains(name, "unsigned")
	switch {
	case strings.HasSuffix(name, "[]"), strings.Contains(name, "("):
		return "", "", ""
	case tinyintBoolean && first == "tinyint" && size == "1":
		return TypeKindBoolean, "", ""
	case strings.HasPrefix(name, "timestamp") && strings.Contains(name, "with time zone"), name == "timestamptz", name == "timestamp_tz":
		return TypeKindTimestampTZ, "", ""
	case strings.HasPrefix(name, "timestamp"), name == "datetime":
		return TypeKindTimestamp, "", ""
	case strings.HasPrefix(name, "time"):
		return TypeKindTime, "", ""
	case name == "double precision", first == "double", first == "float8":
		return TypeKindDouble, "", ""
	case strings.HasPrefix(name, "character varying"), first == "varchar", first == "nvarchar":
		return TypeKindVarchar, size, ""
	case strings.HasPrefix(name, "character"), first == "char", first == "bpchar", first == "nchar":
		return TypeKindChar, size, ""
	}
	switch first {
	case "smallint", "int2", "tinyint", "year", "utinyint":
		if unsigned && first == "smallint" {
			return TypeKindInteger, "", ""
		}
		return TypeKindSmallint, "", ""
	case "integer", "int", "int4", "mediumint", "serial", "usmallint":
		if wideIntegers {
			return TypeKindBigint, "", ""
		}
		if unsigned && first != "mediumint" {
			return TypeKindBigint, "", ""
		}
		return TypeKindInteger, "", ""
	case "bigint", "int8", "bigserial", "uinteger":
		if unsigned {
			return TypeKindDecimal, "20", "0"
		}
		return TypeKindBigint, "", ""
	case "numeric", "decimal", "dec":
		return TypeKindDecimal, size, scale
	case "real", "float4", "float":
		return TypeKindReal, "", ""
	case "bool", "boolean":
		return TypeKindBoolean, "", ""
	case "text", "tinytext", "mediumtext", "longtext", "string", "clob":
		return TypeKindText, "", ""
	case "date":
		return TypeKindDate, "", ""
	case "uuid":
		return TypeKindUUID, "", ""
	case "json", "jsonb":
		return TypeKindJSON, "", ""
	case "bytea", "blob", "tinyblob", "mediumblob", "longblob", "binary", "varbinary", "bytes":
		return TypeKindBlob, "", ""
	}
	return "", "", ""
}

// clickhouseKind is the kind of a ClickHouse type.
func clickhouseKind(t string) (kind TypeKind, size, scale string) {
	for {
		m := typeWrapper.FindStringSubmatch(t)
		if m == nil {
			break
		}
		t = m[1]
	}
	name, args, _ := strings.Cut(t, "(")
	if m := typeSize.FindStringSubmatch("(" + args); args != "" && m != nil {
		size, scale = m[1], m[2]
	}
	switch name {
	case "Int8", "Int16", "UInt8":
		return TypeKindSmallint, "", ""
	case "Int32", "UInt16":
		return TypeKindInteger, "", ""
	case "Int64", "UInt32":
		return TypeKindBigint, "", ""
	case "UInt64":
		return TypeKindDecimal, "20", "0"
	case "Decimal":
		return TypeKindDecimal, size, scale
	case "Float32":
		return TypeKindReal, "", ""
	case "Float64":
		return TypeKindDouble, "", ""
	case "Bool":
		return TypeKindBoolean, "", ""
	case "String":
		return TypeKindText, "", ""
	case "FixedString":
		return TypeKindChar, size, ""
	case "Date", "Date32":
		return TypeKindDate, "", ""
	case "DateTime", "DateTime64":
		if strings.Contains(args, "'") {
			return TypeKindTimestampTZ, "", ""
		}
		return TypeKindTimestamp, "", ""
	case "UUID":
		return TypeKindUUID, "", ""
	case "JSON":
		return TypeKindJSON, "", ""
	}
	return "", "", ""
}

// TypeOfKind is an engine's column type of a kind, its length,
// precision and scale kept where the type takes them. An engine without
// SQL rules gets DuckDB's.
func TypeOfKind(e Engine, kind TypeKind, size, scale string) string {
	if r, ok := sqlRulesOf(e); ok {
		return r.TypeOfKind(kind, size, scale)
	}
	return typesOfKind(kind, size, scale).duckdb
}

// typesOfKind are each engine's type of a kind. They differ from
// columnTypesFor's, which keep a copy's values exactly: a model's integer
// stays an integer, not a bigint.
func typesOfKind(kind TypeKind, size, scale string) engineTypes {
	sized := func(name, otherwise string) string {
		if size == "" {
			return otherwise
		}
		return name + "(" + size + ")"
	}
	switch kind {
	case TypeKindSmallint:
		return engineTypes{"smallint", "SMALLINT", "INTEGER", "Int16", "SMALLINT"}
	case TypeKindInteger:
		return engineTypes{"integer", "INT", "INTEGER", "Int32", "INTEGER"}
	case TypeKindBigint:
		return engineTypes{"bigint", "BIGINT", "INTEGER", "Int64", "BIGINT"}
	case TypeKindDecimal:
		if size == "" {
			// Unsized, it holds any number: as near as each engine comes.
			return engineTypes{"numeric", "DECIMAL(65,30)", "NUMERIC", "Decimal(38,10)", "DECIMAL(38,10)"}
		}
		ps := size + "," + scale
		if scale == "" {
			ps = size + ",0"
		}
		return engineTypes{"numeric(" + ps + ")", "DECIMAL(" + ps + ")", "NUMERIC", "Decimal(" + ps + ")", "DECIMAL(" + ps + ")"}
	case TypeKindReal:
		return engineTypes{"real", "FLOAT", "REAL", "Float32", "FLOAT"}
	case TypeKindDouble:
		return engineTypes{"double precision", "DOUBLE", "REAL", "Float64", "DOUBLE"}
	case TypeKindBoolean:
		return engineTypes{"boolean", "BOOLEAN", "INTEGER", "Bool", "BOOLEAN"}
	case TypeKindVarchar:
		// MySQL's VARCHAR needs a length, and its TEXT cannot be a key.
		return engineTypes{sized("varchar", "varchar"), sized("VARCHAR", "VARCHAR(255)"), "TEXT", "String", sized("VARCHAR", "VARCHAR")}
	case TypeKindChar:
		return engineTypes{sized("char", "char(1)"), sized("CHAR", "CHAR(1)"), "TEXT", "String", "VARCHAR"}
	case TypeKindText:
		return engineTypes{"text", "LONGTEXT", "TEXT", "String", "VARCHAR"}
	case TypeKindDate:
		return engineTypes{"date", "DATE", "TEXT", "Date32", "DATE"}
	case TypeKindTime:
		return engineTypes{"time", "TIME(6)", "TEXT", "String", "TIME"}
	case TypeKindTimestamp:
		return engineTypes{"timestamp", "DATETIME(6)", "TEXT", "DateTime64(6)", "TIMESTAMP"}
	case TypeKindTimestampTZ:
		return engineTypes{"timestamptz", "DATETIME(6)", "TEXT", "DateTime64(6, 'UTC')", "TIMESTAMPTZ"}
	case TypeKindUUID:
		return engineTypes{"uuid", "CHAR(36)", "TEXT", "UUID", "UUID"}
	case TypeKindJSON:
		return engineTypes{"jsonb", "JSON", "TEXT", "String", "JSON"}
	}
	return engineTypes{"bytea", "LONGBLOB", "BLOB", "String", "BLOB"}
}

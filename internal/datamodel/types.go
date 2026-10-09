package datamodel

import (
	"regexp"
	"strings"

	"dgopher/internal/db"
)

// A type's kind, as a model moved to another engine is typed through: the
// common types of SQL, sizes kept, which every engine has a type for.
const (
	kindSmallint    = "smallint"
	kindInteger     = "integer"
	kindBigint      = "bigint"
	kindDecimal     = "decimal"
	kindReal        = "real"
	kindDouble      = "double"
	kindBoolean     = "boolean"
	kindVarchar     = "varchar"
	kindChar        = "char"
	kindText        = "text"
	kindDate        = "date"
	kindTime        = "time"
	kindTimestamp   = "timestamp"
	kindTimestampTZ = "timestamptz"
	kindUUID        = "uuid"
	kindJSON        = "json"
	kindBlob        = "blob"
)

var (
	typeSize    = regexp.MustCompile(`\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\)`)
	typeWrapper = regexp.MustCompile(`^(?:Nullable|LowCardinality)\((.*)\)$`)
)

// convertType is a column's type of one engine as another's that holds the
// same values, its length, precision and scale kept: varchar(40) stays
// varchar(40), not text. A type no kind has, as an array, takes the type a
// copy of its values would.
func convertType(from, to db.Engine, typ string) string {
	kind, size, scale := typeKind(from, typ)
	if kind == "" {
		return db.ColumnType(to, db.CanonicalType(from, typ))
	}
	pick := func(postgres, mysql, sqlite, clickhouse, duckdb string) string {
		switch to {
		case db.Postgres:
			return postgres
		case db.MySQL:
			return mysql
		case db.SQLite:
			return sqlite
		case db.ClickHouse:
			return clickhouse
		}
		return duckdb
	}
	sized := func(name, otherwise string) string {
		if size == "" {
			return otherwise
		}
		return name + "(" + size + ")"
	}
	switch kind {
	case kindSmallint:
		return pick("smallint", "SMALLINT", "INTEGER", "Int16", "SMALLINT")
	case kindInteger:
		return pick("integer", "INT", "INTEGER", "Int32", "INTEGER")
	case kindBigint:
		return pick("bigint", "BIGINT", "INTEGER", "Int64", "BIGINT")
	case kindDecimal:
		if size == "" {
			// Unsized, it holds any number: as near as each engine comes.
			return pick("numeric", "DECIMAL(65,30)", "NUMERIC", "Decimal(38,10)", "DECIMAL(38,10)")
		}
		ps := size + "," + scale
		if scale == "" {
			ps = size + ",0"
		}
		return pick("numeric("+ps+")", "DECIMAL("+ps+")", "NUMERIC", "Decimal("+ps+")", "DECIMAL("+ps+")")
	case kindReal:
		return pick("real", "FLOAT", "REAL", "Float32", "FLOAT")
	case kindDouble:
		return pick("double precision", "DOUBLE", "REAL", "Float64", "DOUBLE")
	case kindBoolean:
		return pick("boolean", "BOOLEAN", "INTEGER", "Bool", "BOOLEAN")
	case kindVarchar:
		// MySQL's VARCHAR needs a length, and its TEXT cannot be a key.
		return pick(sized("varchar", "varchar"), sized("VARCHAR", "VARCHAR(255)"), "TEXT", "String", sized("VARCHAR", "VARCHAR"))
	case kindChar:
		return pick(sized("char", "char(1)"), sized("CHAR", "CHAR(1)"), "TEXT", "String", "VARCHAR")
	case kindText:
		return pick("text", "LONGTEXT", "TEXT", "String", "VARCHAR")
	case kindDate:
		return pick("date", "DATE", "TEXT", "Date32", "DATE")
	case kindTime:
		return pick("time", "TIME(6)", "TEXT", "String", "TIME")
	case kindTimestamp:
		return pick("timestamp", "DATETIME(6)", "TEXT", "DateTime64(6)", "TIMESTAMP")
	case kindTimestampTZ:
		return pick("timestamptz", "DATETIME(6)", "TEXT", "DateTime64(6, 'UTC')", "TIMESTAMPTZ")
	case kindUUID:
		return pick("uuid", "CHAR(36)", "TEXT", "UUID", "UUID")
	case kindJSON:
		return pick("jsonb", "JSON", "TEXT", "String", "JSON")
	}
	return pick("bytea", "LONGBLOB", "BLOB", "String", "BLOB")
}

// typeKind is the kind of an engine's type, with its length or precision
// and its scale: "" for a type of no kind.
func typeKind(e db.Engine, typ string) (kind, size, scale string) {
	t := strings.TrimSpace(typ)
	if e == db.ClickHouse {
		return clickhouseKind(t)
	}
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
	case e == db.MySQL && first == "tinyint" && size == "1":
		return kindBoolean, "", ""
	case strings.HasPrefix(name, "timestamp") && strings.Contains(name, "with time zone"), name == "timestamptz", name == "timestamp_tz":
		return kindTimestampTZ, "", ""
	case strings.HasPrefix(name, "timestamp"), name == "datetime":
		return kindTimestamp, "", ""
	case strings.HasPrefix(name, "time"):
		return kindTime, "", ""
	case name == "double precision", first == "double", first == "float8":
		return kindDouble, "", ""
	case strings.HasPrefix(name, "character varying"), first == "varchar", first == "nvarchar":
		return kindVarchar, size, ""
	case strings.HasPrefix(name, "character"), first == "char", first == "bpchar", first == "nchar":
		return kindChar, size, ""
	}
	switch first {
	case "smallint", "int2", "tinyint", "year", "utinyint":
		if unsigned && first == "smallint" {
			return kindInteger, "", ""
		}
		return kindSmallint, "", ""
	case "integer", "int", "int4", "mediumint", "serial", "usmallint":
		if e == db.SQLite {
			return kindBigint, "", "" // SQLite's integers are 64 bits
		}
		if unsigned && first != "mediumint" {
			return kindBigint, "", ""
		}
		return kindInteger, "", ""
	case "bigint", "int8", "bigserial", "uinteger":
		if unsigned {
			return kindDecimal, "20", "0"
		}
		return kindBigint, "", ""
	case "numeric", "decimal", "dec":
		return kindDecimal, size, scale
	case "real", "float4", "float":
		return kindReal, "", ""
	case "bool", "boolean":
		return kindBoolean, "", ""
	case "text", "tinytext", "mediumtext", "longtext", "string", "clob":
		return kindText, "", ""
	case "date":
		return kindDate, "", ""
	case "uuid":
		return kindUUID, "", ""
	case "json", "jsonb":
		return kindJSON, "", ""
	case "bytea", "blob", "tinyblob", "mediumblob", "longblob", "binary", "varbinary", "bytes":
		return kindBlob, "", ""
	}
	return "", "", ""
}

// clickhouseKind is the kind of a ClickHouse type.
func clickhouseKind(t string) (kind, size, scale string) {
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
		return kindSmallint, "", ""
	case "Int32", "UInt16":
		return kindInteger, "", ""
	case "Int64", "UInt32":
		return kindBigint, "", ""
	case "UInt64":
		return kindDecimal, "20", "0"
	case "Decimal":
		return kindDecimal, size, scale
	case "Float32":
		return kindReal, "", ""
	case "Float64":
		return kindDouble, "", ""
	case "Bool":
		return kindBoolean, "", ""
	case "String":
		return kindText, "", ""
	case "FixedString":
		return kindChar, size, ""
	case "Date", "Date32":
		return kindDate, "", ""
	case "DateTime", "DateTime64":
		if strings.Contains(args, "'") {
			return kindTimestampTZ, "", ""
		}
		return kindTimestamp, "", ""
	case "UUID":
		return kindUUID, "", ""
	case "JSON":
		return kindJSON, "", ""
	}
	return "", "", ""
}

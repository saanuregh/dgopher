package db

import (
	"fmt"
	"strings"
)

// sqlserverValue turns what the driver gives for a uniqueidentifier, its
// 16 bytes as SQL Server keeps them, into the UUID's text: the first
// three groups are stored little-endian, the last two as written.
func sqlserverValue(typ string, v any) any {
	b, ok := v.([]byte)
	if !ok || typ != "UNIQUEIDENTIFIER" || len(b) != 16 {
		return v
	}
	return sqlserverUUID(b)
}

// sqlserverUUID writes a uniqueidentifier's 16 bytes as its canonical
// text.
func sqlserverUUID(b []byte) string {
	return fmt.Sprintf("%02X%02X%02X%02X-%02X%02X-%02X%02X-%02X%02X-%02X%02X%02X%02X%02X%02X",
		b[3], b[2], b[1], b[0], b[5], b[4], b[7], b[6], b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

// sqlserverCanonical is a SQL Server column type as the DuckDB type that
// holds its values (CanonicalType).
func sqlserverCanonical(typ string) string {
	t := strings.TrimSpace(typ)
	if m := sizedDecimal.FindStringSubmatch(t); m != nil {
		return "DECIMAL(" + m[1] + "," + m[2] + ")"
	}
	switch sqlserverTypeName(t) {
	case "bit":
		return "BOOLEAN"
	case "tinyint", "smallint", "int", "bigint":
		return "BIGINT"
	case "real":
		return "FLOAT"
	case "float":
		return "DOUBLE"
	case "money", "smallmoney":
		return "DECIMAL(19,4)"
	case "date":
		return "DATE"
	case "time":
		return "TIME"
	case "datetime", "datetime2", "smalldatetime":
		return "TIMESTAMP"
	case "datetimeoffset":
		return "TIMESTAMPTZ"
	case "uniqueidentifier":
		return "UUID"
	case "binary", "varbinary", "image", "rowversion", "timestamp":
		return "BLOB"
	}
	return "VARCHAR"
}

// sqlserverColumnType is the type of a SQL Server column that holds a
// DuckDB type's values exactly (ColumnType).
func sqlserverColumnType(duckType string) string {
	t := strings.ToUpper(strings.TrimSpace(duckType))
	if m := duckDecimal.FindStringSubmatch(t); m != nil {
		return "decimal(" + m[1] + "," + m[2] + ")"
	}
	switch t {
	case "BOOLEAN":
		return "bit"
	case "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "UTINYINT", "USMALLINT", "UINTEGER":
		return "bigint"
	case "UBIGINT", "HUGEINT", "UHUGEINT":
		return "decimal(38,0)"
	case "FLOAT":
		return "real"
	case "DOUBLE":
		return "float"
	case "DATE":
		return "date"
	case "TIMESTAMP", "TIMESTAMP_S", "TIMESTAMP_MS", "TIMESTAMP_NS":
		return "datetime2(7)"
	case "TIMESTAMP WITH TIME ZONE", "TIMESTAMPTZ":
		return "datetimeoffset(7)"
	case "TIME":
		return "time(7)"
	case "UUID":
		return "uniqueidentifier"
	case "BLOB":
		return "varbinary(max)"
	}
	return "nvarchar(max)"
}

// sqlserverKind is the kind of a SQL Server type (TypeKindOf).
func sqlserverKind(typ string) (TypeKind, string, string) {
	t := strings.TrimSpace(typ)
	var size, scale string
	if m := typeSize.FindStringSubmatch(t); m != nil {
		size, scale = m[1], m[2]
	}
	switch sqlserverTypeName(t) {
	case "tinyint", "smallint":
		return TypeKindSmallint, "", ""
	case "int":
		return TypeKindInteger, "", ""
	case "bigint":
		return TypeKindBigint, "", ""
	case "decimal", "numeric":
		return TypeKindDecimal, size, scale
	case "money":
		return TypeKindDecimal, "19", "4"
	case "smallmoney":
		return TypeKindDecimal, "10", "4"
	case "real":
		return TypeKindReal, "", ""
	case "float":
		return TypeKindDouble, "", ""
	case "bit":
		return TypeKindBoolean, "", ""
	case "varchar", "nvarchar":
		if strings.Contains(strings.ToLower(t), "(max)") {
			return TypeKindText, "", ""
		}
		return TypeKindVarchar, size, ""
	case "char", "nchar":
		return TypeKindChar, size, ""
	case "text", "ntext":
		return TypeKindText, "", ""
	case "date":
		return TypeKindDate, "", ""
	case "time":
		return TypeKindTime, "", ""
	case "datetime", "datetime2", "smalldatetime":
		return TypeKindTimestamp, "", ""
	case "datetimeoffset":
		return TypeKindTimestampTZ, "", ""
	case "uniqueidentifier":
		return TypeKindUUID, "", ""
	case "binary", "varbinary", "image", "timestamp":
		return TypeKindBlob, "", ""
	}
	return "", "", ""
}

// sqlserverSynonyms are T-SQL's other names of its types.
var sqlserverSynonyms = map[string]string{
	"integer": "int", "dec": "decimal", "double precision": "float", "character": "char", "char varying": "varchar",
	"character varying": "varchar", "national character": "nchar", "national char": "nchar", "national character varying": "nvarchar",
	"national char varying": "nvarchar", "national text": "ntext", "binary varying": "varbinary", "rowversion": "timestamp",
}

// sqlserverTypeName is a type's name, without its sizes, as SQL Server
// names it rather than by a synonym.
func sqlserverTypeName(t string) string {
	name := typeName(t)
	if s, ok := sqlserverSynonyms[name]; ok {
		return s
	}
	return name
}

// sqlserverTypeOfKind is SQL Server's type of a kind (TypeOfKind).
func sqlserverTypeOfKind(kind TypeKind, size, scale string) string {
	switch kind {
	case TypeKindSmallint:
		return "smallint"
	case TypeKindInteger:
		return "int"
	case TypeKindBigint:
		return "bigint"
	case TypeKindDecimal:
		if size == "" {
			return "decimal(38,10)"
		}
		if scale == "" {
			scale = "0"
		}
		return "decimal(" + size + "," + scale + ")"
	case TypeKindReal:
		return "real"
	case TypeKindDouble:
		return "float"
	case TypeKindBoolean:
		return "bit"
	case TypeKindVarchar:
		if size == "" {
			return "nvarchar(max)"
		}
		return "nvarchar(" + size + ")"
	case TypeKindChar:
		if size == "" {
			return "nchar(1)"
		}
		return "nchar(" + size + ")"
	case TypeKindText, TypeKindJSON:
		return "nvarchar(max)"
	case TypeKindDate:
		return "date"
	case TypeKindTime:
		return "time(7)"
	case TypeKindTimestamp:
		return "datetime2(7)"
	case TypeKindTimestampTZ:
		return "datetimeoffset(7)"
	case TypeKindUUID:
		return "uniqueidentifier"
	case TypeKindBlob:
		return "varbinary(max)"
	}
	return "nvarchar(max)"
}

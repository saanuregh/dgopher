package db

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// The table designer's Oracle: its types, and the DDL changing a table,
// whose names are quoted as written and whose comments are COMMENT ON.

func (oracleEngine) CommonTypes() []string {
	return []string{"NUMBER(10)", "NUMBER(19)", "NUMBER(12,2)", "BINARY_DOUBLE", "VARCHAR2(255)", "NVARCHAR2(255)", "CLOB",
		"DATE", "TIMESTAMP", "TIMESTAMP WITH TIME ZONE", "RAW(16)", "BLOB"}
}

func (oracleEngine) FirstColumn() ColumnDesign {
	return ColumnDesign{Name: "id", PrimaryKey: true, Type: "NUMBER(19)", AutoIncrement: true}
}

// ColumnTypeFor is the Oracle type holding a DuckDB type's values: DATE
// keeps a time too, and Oracle before 23ai has no BOOLEAN.
func (oracleEngine) ColumnTypeFor(duckType string) string {
	t := strings.ToUpper(strings.TrimSpace(duckType))
	if m := duckDecimal.FindStringSubmatch(t); m != nil {
		return "NUMBER(" + m[1] + "," + m[2] + ")"
	}
	switch t {
	case "BOOLEAN":
		return "NUMBER(1)"
	case "TINYINT", "SMALLINT", "UTINYINT":
		return "NUMBER(5)"
	case "INTEGER", "USMALLINT":
		return "NUMBER(10)"
	case "BIGINT", "UINTEGER":
		return "NUMBER(19)"
	case "UBIGINT", "HUGEINT", "UHUGEINT":
		return "NUMBER(38)"
	case "FLOAT":
		return "BINARY_FLOAT"
	case "DOUBLE":
		return "BINARY_DOUBLE"
	case "DATE":
		return "DATE"
	case "TIMESTAMP", "TIMESTAMP_S", "TIMESTAMP_MS":
		return "TIMESTAMP(6)"
	case "TIMESTAMP_NS":
		return "TIMESTAMP(9)"
	case "TIMESTAMP WITH TIME ZONE", "TIMESTAMPTZ":
		return "TIMESTAMP(6) WITH TIME ZONE"
	case "UUID":
		return "VARCHAR2(36)"
	case "BLOB":
		return "BLOB"
	}
	return "CLOB"
}

// CanonicalType is the DuckDB type holding an Oracle type's values: a
// DATE has a time; a NUMBER of no scale an integer while it fits, one of
// fewer than four digits a decimal of its own, as NUMBER(1), which holds
// a flag, holds no number past 9.
func (oracleEngine) CanonicalType(typ string) string {
	kind, size, scale := oracleKind(typ)
	switch kind {
	case TypeKindSmallint, TypeKindInteger, TypeKindBigint:
		if m := typeSize.FindStringSubmatch(typ); m != nil && len(m[1]) == 1 && (m[2] == "" || m[2] == "0") && m[1] < "4" {
			return "DECIMAL(" + m[1] + ",0)"
		}
		return "BIGINT"
	case TypeKindDecimal:
		if size == "" {
			return "DOUBLE"
		}
		return "DECIMAL(" + size + "," + scale + ")"
	case TypeKindReal:
		return "FLOAT"
	case TypeKindDouble:
		return "DOUBLE"
	case TypeKindBoolean:
		return "BOOLEAN"
	case TypeKindTimestamp:
		return "TIMESTAMP"
	case TypeKindTimestampTZ:
		return "TIMESTAMPTZ"
	case TypeKindBlob:
		return "BLOB"
	}
	return "VARCHAR"
}

// oracleKind is the kind of an Oracle type (TypeKindOf). A DATE is a
// timestamp: it keeps the time of day.
func oracleKind(typ string) (TypeKind, string, string) {
	t := strings.TrimSpace(typ)
	var size, scale string
	if m := typeSize.FindStringSubmatch(t); m != nil {
		size, scale = m[1], m[2]
	}
	switch name := typeName(t); {
	case name == "number" && strings.Contains(t, "(*"):
		return TypeKindDecimal, "38", "0"
	case name == "number" && size != "" && (scale == "" || scale == "0"):
		// The precisions TypeOfKind writes each integer as, as Oracle's
		// tools map them, though NUMBER(19) holds more than a BIGINT.
		switch p, _ := strconv.Atoi(size); {
		case p <= 5:
			return TypeKindSmallint, "", ""
		case p <= 10:
			return TypeKindInteger, "", ""
		case p <= 19:
			return TypeKindBigint, "", ""
		}
		return TypeKindDecimal, size, "0"
	case name == "number" && size != "":
		return TypeKindDecimal, size, scale
	case name == "number", name == "float", name == "binary_double":
		return TypeKindDouble, "", ""
	case name == "binary_float":
		return TypeKindReal, "", ""
	case name == "boolean":
		return TypeKindBoolean, "", ""
	case name == "varchar2", name == "nvarchar2", name == "varchar":
		return TypeKindVarchar, size, ""
	case name == "char", name == "nchar":
		return TypeKindChar, size, ""
	case name == "clob", name == "nclob", name == "long":
		return TypeKindText, "", ""
	case name == "date", name == "timestamp":
		return TypeKindTimestamp, "", ""
	case name == "timestamp with time zone", name == "timestamp with local time zone":
		return TypeKindTimestampTZ, "", ""
	case name == "json":
		return TypeKindJSON, "", ""
	case name == "raw", name == "blob", name == "long raw":
		return TypeKindBlob, "", ""
	}
	return "", "", ""
}

func (oracleEngine) TypeKindOf(typ string) (TypeKind, string, string) { return oracleKind(typ) }

func (oracleEngine) TypeOfKind(kind TypeKind, size, scale string) string {
	switch kind {
	case TypeKindSmallint:
		return "NUMBER(5)"
	case TypeKindInteger:
		return "NUMBER(10)"
	case TypeKindBigint:
		return "NUMBER(19)"
	case TypeKindDecimal:
		if size == "" {
			return "NUMBER"
		}
		if scale == "" {
			scale = "0"
		}
		return "NUMBER(" + size + "," + scale + ")"
	case TypeKindReal:
		return "BINARY_FLOAT"
	case TypeKindDouble:
		return "BINARY_DOUBLE"
	case TypeKindBoolean:
		return "NUMBER(1)"
	case TypeKindVarchar:
		if size == "" {
			return "VARCHAR2(4000)"
		}
		return "VARCHAR2(" + size + ")"
	case TypeKindChar:
		if size == "" {
			return "CHAR(1)"
		}
		return "CHAR(" + size + ")"
	case TypeKindText, TypeKindJSON:
		return "CLOB"
	case TypeKindDate:
		return "DATE"
	case TypeKindTime:
		return "VARCHAR2(18)"
	case TypeKindTimestamp:
		return "TIMESTAMP(6)"
	case TypeKindTimestampTZ:
		return "TIMESTAMP(6) WITH TIME ZONE"
	case TypeKindUUID:
		return "VARCHAR2(36)"
	case TypeKindBlob:
		return "BLOB"
	}
	return ""
}

// ModifyColumnSQL changes a column with MODIFY, which states its new
// type, default and nullability together, each only when it changed:
// Oracle refuses to make a column NULL, or NOT NULL, that is so already.
// A default is removed as DEFAULT NULL, which Oracle keeps as its text
// (oracleScanColumns reads it as none).
func (oracleEngine) ModifyColumnSQL(d Dialect, t TableDesign, was, now ColumnDesign) ([]string, error) {
	if err := columnChangeError(was, now); err != nil {
		return nil, err
	}
	var parts []string
	if now.Type != was.Type {
		parts = append(parts, now.Type)
	}
	if now.Default != was.Default {
		parts = append(parts, "DEFAULT "+cmp.Or(now.Default, "NULL"))
	}
	if now.Nullable != was.Nullable {
		parts = append(parts, oracleNullability(now.Nullable))
	}
	var out []string
	if len(parts) > 0 {
		out = append(out, "MODIFY ("+d.Quote(now.Name)+" "+strings.Join(parts, " ")+")")
	}
	if now.Comment != was.Comment {
		out = append(out, oracleEngine{}.ColumnCommentSQL(d, t.Schema, t.Name, now.Name, was.Comment, now.Comment))
	}
	return out, nil
}

func oracleNullability(nullable bool) string {
	if nullable {
		return "NULL"
	}
	return "NOT NULL"
}

// RenameObjectSQL renames a table with ALTER TABLE … RENAME TO, which
// takes the new name in the table's schema. Oracle renames a view or a
// sequence only with RENAME, in the user's own schema, which would rename
// another object of the name there.
func (oracleEngine) RenameObjectSQL(obj Object, name string) (string, error) {
	if obj.Kind != KindTable {
		return "", fmt.Errorf("Oracle renames a %s only with RENAME, in the user's own schema: rename %s in SQL", obj.Kind, obj.Name)
	}
	d := oracleDialect{}
	return "ALTER TABLE " + QualifiedName(d, obj.Schema, obj.Name) + " RENAME TO " + d.Quote(name), nil
}

func (oracleEngine) RenameColumnSQL(d Dialect, schema, table, column, name string) string {
	return "ALTER TABLE " + QualifiedName(d, schema, table) + " RENAME COLUMN " + d.Quote(column) + " TO " + d.Quote(name)
}

func (oracleEngine) AddColumnSQL(d Dialect, schema, table, definition string) string {
	return "ALTER TABLE " + QualifiedName(d, schema, table) + " ADD (" + definition + ")"
}

func (oracleEngine) DropColumnSQL(d Dialect, schema, table string, c ColumnDesign) []string {
	return dropColumnSQL(d, schema, table, c)
}

// ColumnCommentSQL sets a column's comment; an empty string is NULL,
// which removes it.
func (oracleEngine) ColumnCommentSQL(d Dialect, schema, table, column, _, comment string) string {
	return "COMMENT ON COLUMN " + QualifiedName(d, schema, table) + "." + d.Quote(column) + " IS " + standardLiteral(comment)
}

func (oracleEngine) ScriptPreamble(bool) string                              { return "" }
func (oracleEngine) NullableWrapper() bool                                   { return false }
func (oracleEngine) CreateTableSuffix(Dialect, TableDesign, []string) string { return "" }
func (oracleEngine) InlineIntegerKey() bool                                  { return false }
func (oracleEngine) PrimaryKeyClause() bool                                  { return true }
func (oracleEngine) RebuildsToAlter() bool                                   { return false }

// DropConstraintSQL drops a constraint of the table, or an index, which
// is the schema's.
func (oracleEngine) DropConstraintSQL(d Dialect, kind ConstraintKind, schema, table, name string) string {
	return dropConstraintSQL(d, kind, schema, table, name)
}

func (oracleEngine) UniqueAsConstraint() bool       { return true }
func (oracleEngine) ReplacesPrimaryKeyAtOnce() bool { return false }

func (oracleEngine) TableCommentSQL(d Dialect, schema, table, comment string) string {
	return "COMMENT ON TABLE " + QualifiedName(d, schema, table) + " IS " + standardLiteral(comment)
}

func (oracleEngine) ColumnCommentInline() bool           { return false }
func (oracleEngine) HasComments() bool                   { return true }
func (oracleEngine) KeepsColumnExtra() bool              { return false }
func (oracleEngine) NotNullLast() bool                   { return true }
func (oracleEngine) AutoIncrementSQL() string            { return "GENERATED BY DEFAULT AS IDENTITY" }
func (oracleEngine) HasAutoNumbering() bool              { return true }
func (oracleEngine) HasForeignKeys() bool                { return true }
func (oracleEngine) HasForeignKeyActions() bool          { return true }
func (oracleEngine) ForeignKeyNamesUniqueInSchema() bool { return true }
func (oracleEngine) HasSecondaryIndexes() bool           { return true }
func (oracleEngine) KeepsKeyNames() bool                 { return true }
func (oracleEngine) NamesConstraintsItself() bool        { return false }
func (oracleEngine) NamesTablesWithinSchema() bool       { return false }
func (oracleEngine) IgnoresVarcharLength() bool          { return false }
func (oracleEngine) KeysOfMadeTable() string             { return "" }
func (oracleEngine) FixedKeysNote() string               { return "" }

// ReadDesignExtras reads the check constraints, but those Oracle keeps
// for the NOT NULL of a column, and which unique indexes are constraints.
func (oracleEngine) ReadDesignExtras(ctx context.Context, d *DB, t *TableDesign) error {
	err := scanRows(ctx, d.Catalog(), `SELECT k.constraint_name, k.search_condition_vc FROM all_constraints k
WHERE k.owner = :1 AND k.table_name = :2 AND k.constraint_type = 'C'
  AND NOT (k.generated = 'GENERATED NAME' AND EXISTS (SELECT 1 FROM all_cons_columns kc
    WHERE kc.owner = k.owner AND kc.constraint_name = k.constraint_name AND k.search_condition_vc = '"' || kc.column_name || '" IS NOT NULL'))
ORDER BY k.constraint_name`, []any{t.Schema, t.Name}, func(scan func(...any) error) error {
		c := CheckDesign{Read: true}
		if err := scan(&c.Name, &c.Expression); err != nil {
			return err
		}
		t.Checks = append(t.Checks, c)
		return nil
	})
	if err != nil {
		return err
	}
	unique, err := queryStrings(ctx, d.Catalog(), `SELECT index_name FROM all_constraints
WHERE owner = :1 AND table_name = :2 AND constraint_type = 'U' AND index_name IS NOT NULL`, t.Schema, t.Name)
	for i := range t.Indexes {
		t.Indexes[i].Constraint = slices.Contains(unique, t.Indexes[i].Name)
	}
	return err
}
func (oracleEngine) NormalizeModelTable(*TableDesign) {}
func (oracleEngine) TruncateToEmpty() bool            { return false }

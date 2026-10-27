package db

import (
	"context"
	"slices"
	"strings"
)

// The table designer's SQL Server: its types, and the DDL changing a
// table, whose comments are MS_Description extended properties and whose
// defaults are constraints of their own.

func (sqlserverEngine) CommonTypes() []string {
	return []string{"int", "bigint", "smallint", "tinyint", "decimal(12,2)", "money", "real", "float", "bit", "nvarchar(255)",
		"nvarchar(max)", "varchar(255)", "date", "datetime2", "datetimeoffset", "time", "uniqueidentifier", "varbinary(max)"}
}

func (sqlserverEngine) FirstColumn() ColumnDesign {
	return ColumnDesign{Name: "id", PrimaryKey: true, Type: "bigint", AutoIncrement: true}
}

func (sqlserverEngine) ColumnTypeFor(duckType string) string { return sqlserverColumnType(duckType) }
func (sqlserverEngine) CanonicalType(typ string) string      { return sqlserverCanonical(typ) }

// ModifyColumnSQL changes a column with ALTER COLUMN, which states the
// type and the nullability together. A default is a constraint, which
// keeps the column's type from changing: it is dropped first, by the
// name SQL Server gave it, and added again after.
func (sqlserverEngine) ModifyColumnSQL(d Dialect, t TableDesign, was, now ColumnDesign) ([]string, error) {
	if err := columnChangeError(was, now); err != nil {
		return nil, err
	}
	name := d.Quote(now.Name)
	retyped := now.Type != was.Type
	moveDefault := now.Default != was.Default || retyped && was.Default != ""
	var out []string
	if moveDefault && was.Default != "" {
		out = append(out, sqlserverDropDefaultSQL(d, t.Schema, t.Name, now.Name))
	}
	if retyped || now.Nullable != was.Nullable {
		out = append(out, "ALTER COLUMN "+name+" "+now.Type+sqlserverNullability(now.Nullable))
	}
	if moveDefault && now.Default != "" {
		out = append(out, "ADD DEFAULT "+now.Default+" FOR "+name)
	}
	if now.Comment != was.Comment {
		out = append(out, sqlserverCommentSQL(d, t.Schema, t.Name, now.Name, was.Comment, now.Comment))
	}
	return out, nil
}

func sqlserverNullability(nullable bool) string {
	if nullable {
		return " NULL"
	}
	return " NOT NULL"
}

// sqlserverDropDefaultSQL drops a column's default constraint, whatever
// SQL Server named it, when it has one.
func sqlserverDropDefaultSQL(d Dialect, schema, table, column string) string {
	name := QualifiedName(d, schema, table)
	return "DECLARE @drop nvarchar(max) = (SELECT N'ALTER TABLE " + strings.ReplaceAll(name, "'", "''") +
		" DROP CONSTRAINT ' + QUOTENAME(d.name) FROM sys.default_constraints d\n  WHERE d.parent_object_id = OBJECT_ID(" +
		sqlserverText(name) + ") AND d.parent_column_id = COLUMNPROPERTY(d.parent_object_id, " + sqlserverText(column) + ", 'ColumnId'));\n" +
		"IF @drop IS NOT NULL EXEC sys.sp_executesql @drop"
}

// sqlserverText is an N'…' literal.
func sqlserverText(s string) string { return sqlserverEngine{}.TextLiteral(s) }

// RenameObjectSQL renames a table or a view with sp_rename, which takes
// the new name as it is, unquoted, in the object's schema.
func (sqlserverEngine) RenameObjectSQL(obj Object, name string) (string, error) {
	return "EXEC sys.sp_rename " + sqlserverText(QualifiedName(sqlserverDialect{}, obj.Schema, obj.Name)) + ", " + sqlserverText(name), nil
}

func (sqlserverEngine) RenameColumnSQL(d Dialect, schema, table, column, name string) string {
	return "EXEC sys.sp_rename " + sqlserverText(QualifiedName(d, schema, table)+"."+d.Quote(column)) + ", " + sqlserverText(name) + ", N'COLUMN'"
}

// AddColumnSQL: T-SQL's ADD takes no COLUMN.
func (sqlserverEngine) AddColumnSQL(d Dialect, schema, table, definition string) string {
	return "ALTER TABLE " + QualifiedName(d, schema, table) + " ADD " + definition
}

// DropColumnSQL drops the column's default first, which would keep the
// column from being dropped.
func (sqlserverEngine) DropColumnSQL(d Dialect, schema, table string, c ColumnDesign) []string {
	drop := dropColumnSQL(d, schema, table, c)
	if c.Default != "" {
		drop = append([]string{sqlserverDropDefaultSQL(d, schema, table, c.Name)}, drop...)
	}
	return drop
}

func (sqlserverEngine) ColumnCommentSQL(d Dialect, schema, table, column, was, comment string) string {
	return sqlserverCommentSQL(d, schema, table, column, was, comment)
}

// sqlserverCommentSQL sets the comment of a table, or of its column
// when column is set, from was: SQL Server keeps it as the
// MS_Description extended property, which is added, updated or dropped
// as was and comment say.
func sqlserverCommentSQL(d Dialect, schema, table, column, was, comment string) string {
	level := "@level0type = N'SCHEMA', @level0name = " + sqlserverText(schema) + ", @level1type = N'TABLE', @level1name = " + sqlserverText(table)
	if column != "" {
		level += ", @level2type = N'COLUMN', @level2name = " + sqlserverText(column)
	}
	switch {
	case comment == "":
		return "EXEC sys.sp_dropextendedproperty @name = N'MS_Description', " + level
	case was == "":
		return "EXEC sys.sp_addextendedproperty @name = N'MS_Description', @value = " + sqlserverText(comment) + ", " + level
	}
	return "EXEC sys.sp_updateextendedproperty @name = N'MS_Description', @value = " + sqlserverText(comment) + ", " + level
}

func (sqlserverEngine) ScriptPreamble(bool) string { return "" }

func (sqlserverEngine) TypeKindOf(typ string) (TypeKind, string, string) { return sqlserverKind(typ) }

func (sqlserverEngine) TypeOfKind(kind TypeKind, size, scale string) string {
	return sqlserverTypeOfKind(kind, size, scale)
}

func (sqlserverEngine) NullableWrapper() bool                                   { return false }
func (sqlserverEngine) CreateTableSuffix(Dialect, TableDesign, []string) string { return "" }
func (sqlserverEngine) InlineIntegerKey() bool                                  { return false }
func (sqlserverEngine) PrimaryKeyClause() bool                                  { return true }
func (sqlserverEngine) RebuildsToAlter() bool                                   { return false }

func (sqlserverEngine) DropConstraintSQL(d Dialect, kind ConstraintKind, schema, table, name string) string {
	if kind == ConstraintIndex {
		return "DROP INDEX " + d.Quote(name) + " ON " + QualifiedName(d, schema, table)
	}
	return "ALTER TABLE " + QualifiedName(d, schema, table) + " DROP CONSTRAINT " + d.Quote(name)
}

func (sqlserverEngine) UniqueAsConstraint() bool { return true }

// ReplacesPrimaryKeyAtOnce is false: a table's key is dropped, which SQL
// Server allows while no foreign key points at it, and added after.
func (sqlserverEngine) ReplacesPrimaryKeyAtOnce() bool { return false }

// TableCommentSQL drops the table's MS_Description when it has one, and
// adds the comment unless it is "".
func (sqlserverEngine) TableCommentSQL(d Dialect, schema, table, comment string) string {
	drop := "IF EXISTS (SELECT 1 FROM sys.extended_properties WHERE class = 1 AND major_id = OBJECT_ID(" +
		sqlserverText(QualifiedName(d, schema, table)) + ") AND minor_id = 0 AND name = N'MS_Description')\n  " +
		sqlserverCommentSQL(d, schema, table, "", "", "")
	if comment == "" {
		return drop
	}
	return drop + ";\n" + sqlserverCommentSQL(d, schema, table, "", "", comment)
}

func (sqlserverEngine) ColumnCommentInline() bool           { return false }
func (sqlserverEngine) HasComments() bool                   { return true }
func (sqlserverEngine) KeepsColumnExtra() bool              { return false }
func (sqlserverEngine) AutoIncrementSQL() string            { return "IDENTITY(1,1)" }
func (sqlserverEngine) HasAutoNumbering() bool              { return true }
func (sqlserverEngine) HasForeignKeys() bool                { return true }
func (sqlserverEngine) HasForeignKeyActions() bool          { return true }
func (sqlserverEngine) ForeignKeyNamesUniqueInSchema() bool { return true }
func (sqlserverEngine) HasSecondaryIndexes() bool           { return true }
func (sqlserverEngine) KeepsKeyNames() bool                 { return true }
func (sqlserverEngine) NamesConstraintsItself() bool        { return false }
func (sqlserverEngine) NamesTablesWithinSchema() bool       { return false }
func (sqlserverEngine) IgnoresVarcharLength() bool          { return false }
func (sqlserverEngine) KeysOfMadeTable() string             { return "" }
func (sqlserverEngine) FixedKeysNote() string               { return "" }

// ReadDesignExtras reads the check constraints, which unique indexes
// are constraints, and which columns are computed.
func (sqlserverEngine) ReadDesignExtras(ctx context.Context, d *DB, t *TableDesign) error {
	name := QualifiedName(d.Dialect, t.Schema, t.Name)
	computed, err := queryStrings(ctx, d.Catalog(), `SELECT name FROM sys.computed_columns WHERE object_id = OBJECT_ID(@p1)`, name)
	if err != nil {
		return err
	}
	for i := range t.Columns {
		t.Columns[i].Generated = slices.Contains(computed, t.Columns[i].Was)
	}
	err = scanRows(ctx, d.Catalog(), `SELECT name, definition FROM sys.check_constraints WHERE parent_object_id = OBJECT_ID(@p1) ORDER BY name`,
		[]any{name}, func(scan func(...any) error) error {
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
	unique, err := queryStrings(ctx, d.Catalog(), `SELECT name FROM sys.key_constraints WHERE parent_object_id = OBJECT_ID(@p1) AND type = 'UQ'`, name)
	for i := range t.Indexes {
		t.Indexes[i].Constraint = slices.Contains(unique, t.Indexes[i].Name)
	}
	return err
}

func (sqlserverEngine) NormalizeModelTable(*TableDesign) {}
func (sqlserverEngine) TruncateToEmpty() bool            { return false }

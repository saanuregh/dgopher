package db

import (
	"context"
	"regexp"
	"slices"
	"strings"
)

// TableDesign is a table as the table form shows and changes it.
type TableDesign struct {
	Schema, Name string
	Comment      string
	Columns      []ColumnDesign
	// PrimaryKeyName names the primary key constraint, where the engine
	// names it; the columns in it are marked PrimaryKey.
	PrimaryKeyName string
	Indexes        []IndexDesign
	ForeignKeys    []ForeignKeyDesign
	Checks         []CheckDesign

	// sqliteUnkept names what of a SQLite table the form cannot write
	// again, "" for nothing: making the table again would lose it.
	sqliteUnkept string
}

// ColumnDesign is a column of a table design. Was is the name it had
// when read, "" for a new column: a column renamed keeps it.
type ColumnDesign struct {
	Was        string
	Name, Type string
	Nullable   bool
	// Default is a SQL expression, "" for none.
	Default       string
	PrimaryKey    bool
	AutoIncrement bool
	Comment       string
	// Extra is what MySQL keeps of the column besides, as its collation
	// and ON UPDATE, written again whenever the column is.
	Extra string
	// Generated columns are computed: their type and nullability change
	// in SQL.
	Generated bool
}

// IndexDesign is an index, or a unique constraint, of a table design.
// One Read from the database is kept as it is, or dropped.
type IndexDesign struct {
	Read    bool
	Name    string
	Columns []string
	Unique  bool
	// Constraint is set for a unique constraint, dropped as one.
	Constraint bool
	// Definition is how the engine writes an index read.
	Definition string
}

// ForeignKeyDesign is a foreign key of a table design: kept or dropped
// when Read, else new.
type ForeignKeyDesign struct {
	Read                bool
	Name                string
	Columns             []string
	RefSchema, RefTable string
	RefColumns          []string
	OnDelete, OnUpdate  string // a referential action, "" for the default
	Definition          string // how the engine writes a key read
}

// CheckDesign is a check constraint of a table design: kept or dropped
// when Read, else new. SQLite's and DuckDB's may have no name.
type CheckDesign struct {
	Read       bool
	Name       string
	Expression string
}

// ReferentialActions are what a foreign key may do when the row it
// points at is deleted or its key changes.
var ReferentialActions = []string{"NO ACTION", "RESTRICT", "CASCADE", "SET NULL", "SET DEFAULT"}

// ReadTableDesign reads a table as the table form changes it.
func ReadTableDesign(ctx context.Context, d *DB, obj Object) (TableDesign, error) {
	t := TableDesign{Schema: obj.Schema, Name: obj.Name, Comment: obj.Comment}
	dl, q := d.Dialect, d.SQL
	cols, err := dl.Columns(ctx, q, obj.Schema, obj.Name)
	if err != nil {
		return t, err
	}
	for _, c := range cols {
		cd := ColumnDesign{Was: c.Name, Name: c.Name, Type: c.Type, Nullable: c.Nullable, PrimaryKey: c.PrimaryKey,
			AutoIncrement: c.AutoIncrement, Comment: c.Comment}
		if c.HasDefault {
			cd.Default = c.Default
		}
		if dl.Engine() == ClickHouse {
			if inner, ok := strings.CutPrefix(c.Type, "Nullable("); ok {
				cd.Type, cd.Nullable = strings.TrimSuffix(inner, ")"), true
			}
		}
		t.Columns = append(t.Columns, cd)
	}
	ixs, err := dl.Indexes(ctx, q, obj.Schema, obj.Name)
	if err != nil {
		return t, err
	}
	for _, ix := range ixs {
		if ix.Primary {
			t.PrimaryKeyName = ix.Name
			continue
		}
		t.Indexes = append(t.Indexes, IndexDesign{Read: true, Name: ix.Name, Columns: ix.Columns, Unique: ix.Unique, Definition: ix.Definition})
	}
	fks, err := dl.ForeignKeys(ctx, q, obj.Schema, obj.Name)
	if err != nil {
		return t, err
	}
	for _, fk := range fks {
		t.ForeignKeys = append(t.ForeignKeys, ForeignKeyDesign{Read: true, Name: fk.Name, Columns: fk.Columns,
			RefSchema: fk.RefSchema, RefTable: fk.RefTable, RefColumns: fk.RefColumns, Definition: fk.Definition})
	}
	switch dl.Engine() {
	case Postgres:
		err = readPostgresDesign(ctx, d, &t)
	case MySQL:
		err = readMySQLDesign(ctx, d, &t)
	case DuckDB:
		err = readDuckDBChecks(ctx, d, &t)
	case SQLite:
		err = readSQLiteDesign(ctx, d, &t)
	}
	return t, err
}

var checkClause = regexp.MustCompile(`(?is)^CHECK\s*\((.*)\)(\s+NOT VALID)?$`)

// readPostgresDesign reads the check constraints, and which unique
// indexes are constraints.
func readPostgresDesign(ctx context.Context, d *DB, t *TableDesign) error {
	name := QualifiedName(d.Dialect, t.Schema, t.Name)
	err := scanRows(ctx, d.SQL, `SELECT conname, pg_get_constraintdef(oid) FROM pg_constraint
WHERE conrelid = $1::regclass AND contype = 'c' ORDER BY conname`, []any{name}, func(scan func(...any) error) error {
		var c CheckDesign
		var def string
		if err := scan(&c.Name, &def); err != nil {
			return err
		}
		c.Read, c.Expression = true, def
		if m := checkClause.FindStringSubmatch(def); m != nil {
			c.Expression = m[1]
		}
		t.Checks = append(t.Checks, c)
		return nil
	})
	if err != nil {
		return err
	}
	unique, err := queryStrings(ctx, d.SQL, `SELECT conname FROM pg_constraint WHERE conrelid = $1::regclass AND contype = 'u'`, name)
	for i := range t.Indexes {
		t.Indexes[i].Constraint = slices.Contains(unique, t.Indexes[i].Name)
	}
	return err
}

// readMySQLDesign reads the check constraints, and each column's default
// as an expression and what else MySQL keeps of it.
func readMySQLDesign(ctx context.Context, d *DB, t *TableDesign) error {
	err := scanRows(ctx, d.SQL, `SELECT COLUMN_NAME, DATA_TYPE, COLUMN_DEFAULT, EXTRA, COALESCE(CHARACTER_SET_NAME, ''), COALESCE(COLLATION_NAME, ''),
  COALESCE(GENERATION_EXPRESSION, '')
FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, []any{t.Schema, t.Name},
		func(scan func(...any) error) error {
			var name, dataType, extra, charset, collation, generation string
			var def *string
			if err := scan(&name, &dataType, &def, &extra, &charset, &collation, &generation); err != nil {
				return err
			}
			for i := range t.Columns {
				if t.Columns[i].Was != name {
					continue
				}
				c := &t.Columns[i]
				c.Default = mysqlDefault(def, dataType, extra)
				c.Generated = generation != ""
				var more []string
				if charset != "" {
					more = append(more, "CHARACTER SET "+charset+" COLLATE "+collation)
				}
				if m := mysqlOnUpdate.FindString(extra); m != "" {
					more = append(more, m)
				}
				if strings.Contains(strings.ToUpper(extra), "INVISIBLE") {
					more = append(more, "INVISIBLE")
				}
				c.Extra = strings.Join(more, " ")
			}
			return nil
		})
	if err != nil {
		return err
	}
	return scanRows(ctx, d.SQL, `SELECT cc.CONSTRAINT_NAME, cc.CHECK_CLAUSE
FROM information_schema.TABLE_CONSTRAINTS tc
JOIN information_schema.CHECK_CONSTRAINTS cc ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
WHERE tc.TABLE_SCHEMA = ? AND tc.TABLE_NAME = ? AND tc.CONSTRAINT_TYPE = 'CHECK' ORDER BY cc.CONSTRAINT_NAME`, []any{t.Schema, t.Name},
		func(scan func(...any) error) error {
			var c CheckDesign
			if err := scan(&c.Name, &c.Expression); err != nil {
				return err
			}
			c.Read = true
			t.Checks = append(t.Checks, c)
			return nil
		})
}

var (
	mysqlOnUpdate  = regexp.MustCompile(`(?i)on update current_timestamp(\(\d\))?`)
	mysqlTimestamp = regexp.MustCompile(`(?i)^current_timestamp(\(\d?\))?$`)
	mysqlNumber    = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][-+]?\d+)?$`)
	mysqlNumeric   = map[string]bool{"tinyint": true, "smallint": true, "mediumint": true, "int": true, "bigint": true,
		"decimal": true, "float": true, "double": true, "year": true}
)

// mysqlDefault writes a default as information_schema gives it as an
// expression: a literal is quoted there only when MySQL computes it.
func mysqlDefault(def *string, dataType, extra string) string {
	switch {
	case def == nil:
		return ""
	case strings.Contains(strings.ToUpper(extra), "DEFAULT_GENERATED") && !mysqlTimestamp.MatchString(*def):
		return "(" + *def + ")"
	case mysqlTimestamp.MatchString(*def), dataType == "bit" && strings.HasPrefix(*def, "b'"):
		return *def
	case mysqlNumeric[dataType] && mysqlNumber.MatchString(*def):
		return *def
	}
	return Literal(MySQL, *def)
}

func readDuckDBChecks(ctx context.Context, d *DB, t *TableDesign) error {
	return scanRows(ctx, d.SQL, `SELECT COALESCE(constraint_name, ''), expression FROM duckdb_constraints()
WHERE database_name = current_database() AND schema_name = ? AND table_name = ? AND constraint_type = 'CHECK'`, []any{t.Schema, t.Name},
		func(scan func(...any) error) error {
			var c CheckDesign
			if err := scan(&c.Name, &c.Expression); err != nil {
				return err
			}
			c.Read = true
			t.Checks = append(t.Checks, c)
			return nil
		})
}

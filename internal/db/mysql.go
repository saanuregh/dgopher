package db

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

type mysqlDialect struct{}

func (mysqlDialect) Engine() Engine           { return MySQL }
func (mysqlDialect) Quote(s string) string    { return quoteBacktick(s) }
func (mysqlDialect) Placeholder(int) string   { return "?" }
func (mysqlDialect) Editable() (bool, string) { return true, "" }

func (mysqlDialect) Databases(context.Context, Querier) ([]string, error) { return nil, nil }

func (mysqlDialect) CurrentSchema(ctx context.Context, q Querier) (string, error) {
	return queryString(ctx, q, `SELECT COALESCE(DATABASE(), '')`)
}

func (mysqlDialect) Schemas(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA
ORDER BY SCHEMA_NAME IN ('information_schema','mysql','performance_schema','sys'), SCHEMA_NAME`)
}

func (mysqlDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	var out []Object
	err := scanRows(ctx, q, `SELECT TABLE_NAME, TABLE_TYPE, COALESCE(TABLE_ROWS, -1),
  COALESCE(DATA_LENGTH + INDEX_LENGTH, -1), COALESCE(ENGINE, ''), COALESCE(TABLE_COMMENT, '')
FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, []any{schema}, func(scan func(...any) error) error {
		o := Object{Schema: schema}
		var typ string
		if err := scan(&o.Name, &typ, &o.Rows, &o.Bytes, &o.Engine, &o.Comment); err != nil {
			return err
		}
		o.Kind = KindTable
		if strings.Contains(typ, "VIEW") {
			o.Kind, o.Rows, o.Bytes = KindView, -1, -1
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

func (mysqlDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	var out []Column
	err := scanRows(ctx, q, `SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE = 'YES', COALESCE(COLUMN_DEFAULT, ''),
  COLUMN_DEFAULT IS NOT NULL, COLUMN_KEY = 'PRI', EXTRA LIKE '%auto_increment%', COALESCE(COLUMN_COMMENT, '')
FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
ORDER BY ORDINAL_POSITION`, []any{schema, table}, func(scan func(...any) error) error {
		var c Column
		if err := scan(&c.Name, &c.Type, &c.Nullable, &c.Default, &c.HasDefault, &c.PrimaryKey, &c.AutoIncrement, &c.Comment); err != nil {
			return err
		}
		out = append(out, c)
		return nil
	})
	return out, err
}

func (d mysqlDialect) Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error) {
	var out []Index
	err := scanRows(ctx, q, `SELECT INDEX_NAME, MIN(NON_UNIQUE) = 0,
  GROUP_CONCAT(COALESCE(COLUMN_NAME, EXPRESSION) ORDER BY SEQ_IN_INDEX SEPARATOR 0x1f), MIN(INDEX_TYPE)
FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
GROUP BY INDEX_NAME ORDER BY INDEX_NAME = 'PRIMARY' DESC, INDEX_NAME`, []any{schema, table}, func(scan func(...any) error) error {
		var ix Index
		var cols, typ string
		if err := scan(&ix.Name, &ix.Unique, &cols, &typ); err != nil {
			return err
		}
		ix.Columns = splitList(cols)
		ix.Primary = ix.Name == "PRIMARY"
		quoted := make([]string, len(ix.Columns))
		for i, c := range ix.Columns {
			quoted[i] = d.Quote(c)
		}
		kind := "INDEX"
		if ix.Primary {
			kind = "PRIMARY KEY"
		} else if ix.Unique {
			kind = "UNIQUE INDEX " + d.Quote(ix.Name)
		} else {
			kind += " " + d.Quote(ix.Name)
		}
		ix.Definition = fmt.Sprintf("%s (%s) USING %s", kind, strings.Join(quoted, ", "), typ)
		out = append(out, ix)
		return nil
	})
	return out, err
}

func (mysqlDialect) ForeignKeys(ctx context.Context, q Querier, schema, table string) ([]ForeignKey, error) {
	var out []ForeignKey
	err := scanRows(ctx, q, `SELECT k.CONSTRAINT_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, rc.DELETE_RULE, rc.UPDATE_RULE,
  GROUP_CONCAT(k.COLUMN_NAME ORDER BY k.ORDINAL_POSITION SEPARATOR 0x1f),
  GROUP_CONCAT(k.REFERENCED_COLUMN_NAME ORDER BY k.ORDINAL_POSITION SEPARATOR 0x1f)
FROM information_schema.KEY_COLUMN_USAGE k
JOIN information_schema.REFERENTIAL_CONSTRAINTS rc
  ON rc.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND rc.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND rc.TABLE_NAME = k.TABLE_NAME
WHERE k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
GROUP BY k.CONSTRAINT_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, rc.DELETE_RULE, rc.UPDATE_RULE
ORDER BY k.CONSTRAINT_NAME`, []any{schema, table}, func(scan func(...any) error) error {
		var fk ForeignKey
		var cols, refCols, onDelete, onUpdate string
		if err := scan(&fk.Name, &fk.RefSchema, &fk.RefTable, &onDelete, &onUpdate, &cols, &refCols); err != nil {
			return err
		}
		fk.Columns, fk.RefColumns = splitList(cols), splitList(refCols)
		fk.OnDelete, fk.OnUpdate = action(onDelete), action(onUpdate)
		fk.Definition = fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s.%s (%s)",
			strings.Join(fk.Columns, ", "), fk.RefSchema, fk.RefTable, strings.Join(fk.RefColumns, ", ")) + actionsSQL(fk.OnDelete, fk.OnUpdate)
		out = append(out, fk)
		return nil
	})
	return out, err
}

func (d mysqlDialect) DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error) {
	if obj.Kind == KindView {
		return showCreate(ctx, q, "SHOW CREATE VIEW "+QualifiedName(d, schema, obj.Name), "Create View")
	}
	return showCreate(ctx, q, "SHOW CREATE TABLE "+QualifiedName(d, schema, obj.Name), "Create Table")
}

// showCreate runs a SHOW CREATE statement and returns the definition in
// its column of that name.
func showCreate(ctx context.Context, q Querier, stmt, column string) (string, error) {
	rows, err := q.QueryContext(ctx, stmt)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	at := slices.Index(cols, column)
	if at < 0 {
		return "", fmt.Errorf("%s gave no %s", stmt, column)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("%s gave no definition", stmt)
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", err
	}
	if vals[at] == nil {
		return "", fmt.Errorf("%s gave no definition: the user may lack the privilege to read it", stmt)
	}
	return Display(vals[at]) + ";\n", nil
}

// Items lists routines, triggers and events.
func (mysqlDialect) Items(ctx context.Context, q Querier, schema string) ([]Item, error) {
	return scanItems(ctx, q, schema, `SELECT LOWER(ROUTINE_TYPE) AS kind, ROUTINE_NAME AS name, '' AS tbl, '' AS detail, '' AS id
FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?
UNION ALL
SELECT 'trigger', TRIGGER_NAME, EVENT_OBJECT_TABLE, CONCAT(ACTION_TIMING, ' ', EVENT_MANIPULATION), ''
FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ?
UNION ALL
SELECT 'event', EVENT_NAME, '', STATUS, '' FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ?
ORDER BY kind, name`, schema, schema, schema)
}

// ItemDDL writes an item's definition as SHOW CREATE gives it.
func (d mysqlDialect) ItemDDL(ctx context.Context, q Querier, it Item) (string, error) {
	name := QualifiedName(d, it.Schema, it.Name)
	switch it.Kind {
	case ItemFunction:
		return showCreate(ctx, q, "SHOW CREATE FUNCTION "+name, "Create Function")
	case ItemProcedure:
		return showCreate(ctx, q, "SHOW CREATE PROCEDURE "+name, "Create Procedure")
	case ItemTrigger:
		return showCreate(ctx, q, "SHOW CREATE TRIGGER "+name, "SQL Original Statement")
	case ItemEvent:
		return showCreate(ctx, q, "SHOW CREATE EVENT "+name, "Create Event")
	}
	return "", errNoDefinition(it)
}

func (mysqlDialect) ReferencedBy(ctx context.Context, q Querier, schema, table string) ([]Reference, error) {
	var out []Reference
	err := scanRows(ctx, q, `SELECT CONSTRAINT_NAME, TABLE_SCHEMA, TABLE_NAME,
  GROUP_CONCAT(COLUMN_NAME ORDER BY ORDINAL_POSITION SEPARATOR 0x1f),
  GROUP_CONCAT(REFERENCED_COLUMN_NAME ORDER BY ORDINAL_POSITION SEPARATOR 0x1f)
FROM information_schema.KEY_COLUMN_USAGE
WHERE REFERENCED_TABLE_SCHEMA = ? AND REFERENCED_TABLE_NAME = ?
GROUP BY CONSTRAINT_NAME, TABLE_SCHEMA, TABLE_NAME
ORDER BY TABLE_SCHEMA, TABLE_NAME, CONSTRAINT_NAME`, []any{schema, table}, func(scan func(...any) error) error {
		var r Reference
		var cols, refCols string
		if err := scan(&r.Name, &r.Schema, &r.Table, &cols, &refCols); err != nil {
			return err
		}
		r.Columns, r.RefColumns = splitList(cols), splitList(refCols)
		out = append(out, r)
		return nil
	})
	return out, err
}

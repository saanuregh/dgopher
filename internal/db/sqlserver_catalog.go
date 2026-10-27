package db

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"dgopher/internal/sqltext"
)

// Databases lists the databases the login may use, the connection's own
// among them, but SQL Server's own (master, tempdb, model, msdb).
func (sqlserverDialect) Databases(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT name FROM sys.databases
WHERE (database_id > 4 OR name = DB_NAME()) AND state = 0 AND HAS_DBACCESS(name) = 1
ORDER BY name`)
}

func (sqlserverDialect) CurrentSchema(ctx context.Context, q Querier) (string, error) {
	return queryString(ctx, q, `SELECT COALESCE(SCHEMA_NAME(), 'dbo')`)
}

// Schemas lists a database's schemas but SQL Server's own and those of
// its fixed roles, dbo first.
func (sqlserverDialect) Schemas(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT name FROM sys.schemas
WHERE schema_id < 16384 AND name NOT IN ('sys', 'INFORMATION_SCHEMA', 'guest')
ORDER BY CASE WHEN name = 'dbo' THEN 0 ELSE 1 END, name`)
}

// Objects reads a schema's tables and views, with each table's rows and
// size from sys.partitions and sys.allocation_units, which, unlike
// sys.dm_db_partition_stats, need no VIEW DATABASE STATE.
func (sqlserverDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return sqlserverObjects(ctx, q, schema, true)
}

func (sqlserverDialect) ObjectNames(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return sqlserverObjects(ctx, q, schema, false)
}

func sqlserverObjects(ctx context.Context, q Querier, schema string, sizes bool) ([]Object, error) {
	rows, size := "CAST(-1 AS bigint)", "CAST(-1 AS bigint)"
	if sizes {
		rows = `COALESCE((SELECT SUM(p.rows) FROM sys.partitions p WHERE p.object_id = o.object_id AND p.index_id IN (0, 1)), 0)`
		size = `COALESCE((SELECT SUM(a.total_pages) FROM sys.partitions p JOIN sys.allocation_units a ON a.container_id = p.partition_id
    WHERE p.object_id = o.object_id), 0) * 8192`
	}
	return scanAll(ctx, q, `SELECT o.name, o.type, CASE WHEN o.type = 'U' THEN `+rows+` ELSE -1 END, CASE WHEN o.type = 'U' THEN `+size+` ELSE -1 END,
  `+sqlserverDescription("o.object_id", "0")+`
FROM sys.objects o
WHERE o.schema_id = SCHEMA_ID(@p1) AND o.type IN ('U', 'V') AND o.is_ms_shipped = 0
ORDER BY o.name`, []any{schema}, func(scan func(...any) error) (Object, error) {
		o := Object{Schema: schema}
		var kind string
		if err := scan(&o.Name, &kind, &o.Rows, &o.Bytes, &o.Comment); err != nil {
			return o, err
		}
		o.Kind = KindTable
		if strings.TrimSpace(kind) == "V" {
			o.Kind = KindView
		}
		return o, nil
	})
}

// sqlserverDescription reads the comment of an object, or of its column
// when minor is the column's ID: its MS_Description extended property.
func sqlserverDescription(major, minor string) string {
	return `COALESCE((SELECT CAST(ep.value AS nvarchar(max)) FROM sys.extended_properties ep
    WHERE ep.class = 1 AND ep.major_id = ` + major + ` AND ep.minor_id = ` + minor + ` AND ep.name = 'MS_Description'), '')`
}

// sqlserverTypeSQL writes the type of a column, c joined with sys.types
// as t, as a CREATE TABLE names it: with its length, precision or scale.
const sqlserverTypeSQL = `CASE
  WHEN t.is_user_defined = 1 THEN QUOTENAME(SCHEMA_NAME(t.schema_id)) + '.' + QUOTENAME(t.name)
  WHEN t.name IN ('varchar', 'char', 'varbinary', 'binary') THEN t.name + '(' + CASE WHEN c.max_length = -1 THEN 'max' ELSE CAST(c.max_length AS varchar(10)) END + ')'
  WHEN t.name IN ('nvarchar', 'nchar') THEN t.name + '(' + CASE WHEN c.max_length = -1 THEN 'max' ELSE CAST(c.max_length / 2 AS varchar(10)) END + ')'
  WHEN t.name IN ('decimal', 'numeric') THEN t.name + '(' + CAST(c.precision AS varchar(10)) + ',' + CAST(c.scale AS varchar(10)) + ')'
  WHEN t.name IN ('datetime2', 'datetimeoffset', 'time') THEN t.name + '(' + CAST(c.scale AS varchar(10)) + ')'
  ELSE t.name END`

// sqlserverColumnsSQL reads columns as sqlserverScanColumns takes them,
// of the objects of a schema the WHERE that follows keeps: a computed
// column and a rowversion, which the server writes, are generated, and
// have a default.
var sqlserverColumnsSQL = `SELECT o.name, c.name, ` + sqlserverTypeSQL + `, c.is_nullable,
  COALESCE(OBJECT_DEFINITION(c.default_object_id), ''),
  CAST(CASE WHEN c.default_object_id <> 0 OR c.is_computed = 1 OR t.name = 'timestamp' THEN 1 ELSE 0 END AS bit),
  CAST(CASE WHEN EXISTS (SELECT 1 FROM sys.indexes i JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
    WHERE i.object_id = c.object_id AND i.is_primary_key = 1 AND ic.column_id = c.column_id) THEN 1 ELSE 0 END AS bit),
  c.is_identity, ` + sqlserverDescription("c.object_id", "c.column_id") + `,
  CAST(CASE WHEN c.is_computed = 1 OR t.name = 'timestamp' THEN 1 ELSE 0 END AS bit)
FROM sys.columns c
JOIN sys.objects o ON o.object_id = c.object_id
JOIN sys.types t ON t.user_type_id = c.user_type_id
WHERE o.schema_id = SCHEMA_ID(@p1) AND o.type IN ('U', 'V') AND o.is_ms_shipped = 0`

func (sqlserverDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	cols, err := sqlserverScanColumns(ctx, q, sqlserverColumnsSQL+` AND o.name = @p2 ORDER BY c.column_id`, []any{schema, table})
	return cols[table], err
}

func (sqlserverDialect) SchemaColumns(ctx context.Context, q Querier, schema string) (map[string][]Column, error) {
	return sqlserverScanColumns(ctx, q, sqlserverColumnsSQL+` ORDER BY o.name, c.column_id`, []any{schema})
}

// sqlserverScanColumns reads the columns of sqlserverColumnsSQL by their
// table, as scanColumns does, and which the server writes.
func sqlserverScanColumns(ctx context.Context, q Querier, query string, args []any) (map[string][]Column, error) {
	out := map[string][]Column{}
	err := scanRows(ctx, q, query, args, func(scan func(...any) error) error {
		var t string
		var c Column
		if err := scan(&t, &c.Name, &c.Type, &c.Nullable, &c.Default, &c.HasDefault, &c.PrimaryKey, &c.AutoIncrement, &c.Comment, &c.Generated); err != nil {
			return err
		}
		out[t] = append(out[t], c)
		return nil
	})
	return out, err
}

// sqlserverIndex is an index as sys.indexes and sys.index_columns keep it.
type sqlserverIndex struct {
	name, kind               string
	unique, primary, uniqueC bool
	keys, included, filter   string
}

// definition writes the index as SQL Server makes it: a key or a unique
// constraint as the clause of a CREATE TABLE, any other index as its own
// statement on table.
func (ix sqlserverIndex) definition(table string) string {
	cols := "(" + strings.Join(splitList(ix.keys), ", ") + ")"
	kind := strings.ToUpper(ix.kind)
	switch {
	case ix.primary:
		return "CONSTRAINT " + sqlserverDialect{}.Quote(ix.name) + " PRIMARY KEY " + kind + " " + cols
	case ix.uniqueC:
		return "CONSTRAINT " + sqlserverDialect{}.Quote(ix.name) + " UNIQUE " + kind + " " + cols
	}
	def := "CREATE "
	if ix.unique {
		def += "UNIQUE "
	}
	def += kind + " INDEX " + sqlserverDialect{}.Quote(ix.name) + " ON " + table + " " + cols
	if ix.included != "" {
		def += " INCLUDE (" + strings.Join(splitList(ix.included), ", ") + ")"
	}
	if ix.filter != "" {
		def += " WHERE " + ix.filter
	}
	return def
}

func sqlserverIndexes(ctx context.Context, q Querier, schema, table string) ([]sqlserverIndex, error) {
	return scanAll(ctx, q, `SELECT i.name, i.type_desc, i.is_unique, i.is_primary_key, i.is_unique_constraint,
  COALESCE((SELECT STRING_AGG(QUOTENAME(c.name) + CASE WHEN ic.is_descending_key = 1 THEN ' DESC' ELSE '' END, CHAR(31))
    WITHIN GROUP (ORDER BY ic.key_ordinal)
    FROM sys.index_columns ic JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
    WHERE ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.key_ordinal > 0), ''),
  COALESCE((SELECT STRING_AGG(QUOTENAME(c.name), CHAR(31)) WITHIN GROUP (ORDER BY ic.index_column_id)
    FROM sys.index_columns ic JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
    WHERE ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.is_included_column = 1), ''),
  COALESCE(i.filter_definition, '')
FROM sys.indexes i
WHERE i.object_id = OBJECT_ID(QUOTENAME(@p1) + '.' + QUOTENAME(@p2)) AND i.index_id > 0 AND i.is_hypothetical = 0
ORDER BY i.is_primary_key DESC, i.name`, []any{schema, table}, func(scan func(...any) error) (sqlserverIndex, error) {
		var ix sqlserverIndex
		err := scan(&ix.name, &ix.kind, &ix.unique, &ix.primary, &ix.uniqueC, &ix.keys, &ix.included, &ix.filter)
		return ix, err
	})
}

func (d sqlserverDialect) Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error) {
	ixs, err := sqlserverIndexes(ctx, q, schema, table)
	out := make([]Index, 0, len(ixs))
	for _, ix := range ixs {
		var cols []string
		for _, k := range splitList(ix.keys) {
			cols = append(cols, sqltext.DecodeQuoted(strings.TrimSuffix(k, " DESC"), sqltext.TSQL))
		}
		out = append(out, Index{Name: ix.name, Columns: cols, Unique: ix.unique, Primary: ix.primary,
			Definition: ix.definition(QualifiedName(d, schema, table))})
	}
	return out, err
}

// sqlserverAction is a foreign key's action as sys.foreign_keys describes
// it: NO ACTION, the default, is "".
func sqlserverAction(desc string) string {
	if desc == "NO_ACTION" {
		return ""
	}
	return strings.ReplaceAll(desc, "_", " ")
}

// sqlserverKeyColumns lists a foreign key's columns, of side parent or
// referenced, in order, separated by unitSep.
func sqlserverKeyColumns(side string) string {
	return `COALESCE((SELECT STRING_AGG(c.name, CHAR(31)) WITHIN GROUP (ORDER BY fkc.constraint_column_id)
    FROM sys.foreign_key_columns fkc JOIN sys.columns c ON c.object_id = fkc.` + side + `_object_id AND c.column_id = fkc.` + side + `_column_id
    WHERE fkc.constraint_object_id = fk.object_id), '')`
}

func (d sqlserverDialect) ForeignKeys(ctx context.Context, q Querier, schema, table string) ([]ForeignKey, error) {
	return scanAll(ctx, q, `SELECT fk.name, OBJECT_SCHEMA_NAME(fk.referenced_object_id), OBJECT_NAME(fk.referenced_object_id),
  fk.delete_referential_action_desc, fk.update_referential_action_desc, `+sqlserverKeyColumns("parent")+`, `+sqlserverKeyColumns("referenced")+`
FROM sys.foreign_keys fk
WHERE fk.parent_object_id = OBJECT_ID(QUOTENAME(@p1) + '.' + QUOTENAME(@p2))
ORDER BY fk.name`, []any{schema, table}, func(scan func(...any) error) (ForeignKey, error) {
		var fk ForeignKey
		var onDelete, onUpdate, cols, refCols string
		if err := scan(&fk.Name, &fk.RefSchema, &fk.RefTable, &onDelete, &onUpdate, &cols, &refCols); err != nil {
			return fk, err
		}
		fk.Columns, fk.RefColumns = splitList(cols), splitList(refCols)
		fk.OnDelete, fk.OnUpdate = sqlserverAction(onDelete), sqlserverAction(onUpdate)
		fk.Definition = "FOREIGN KEY (" + QuoteNames(d, fk.Columns) + ") REFERENCES " + QualifiedName(d, fk.RefSchema, fk.RefTable) +
			" (" + QuoteNames(d, fk.RefColumns) + ")"
		if fk.OnDelete != "" {
			fk.Definition += " ON DELETE " + fk.OnDelete
		}
		if fk.OnUpdate != "" {
			fk.Definition += " ON UPDATE " + fk.OnUpdate
		}
		return fk, nil
	})
}

func (sqlserverDialect) ReferencedBy(ctx context.Context, q Querier, schema, table string) ([]Reference, error) {
	return scanAll(ctx, q, `SELECT fk.name, OBJECT_SCHEMA_NAME(fk.parent_object_id), OBJECT_NAME(fk.parent_object_id),
  `+sqlserverKeyColumns("parent")+`, `+sqlserverKeyColumns("referenced")+`
FROM sys.foreign_keys fk
WHERE fk.referenced_object_id = OBJECT_ID(QUOTENAME(@p1) + '.' + QUOTENAME(@p2))
ORDER BY 2, 3, fk.name`, []any{schema, table}, func(scan func(...any) error) (Reference, error) {
		var r Reference
		var cols, refCols string
		if err := scan(&r.Name, &r.Schema, &r.Table, &cols, &refCols); err != nil {
			return r, err
		}
		r.Columns, r.RefColumns = splitList(cols), splitList(refCols)
		return r, nil
	})
}

// DDL writes a view's definition as it was made, and a table's CREATE
// TABLE from the catalog: its columns, with identity and computed ones,
// keys, unique, check and foreign key constraints, then its other indexes.
func (d sqlserverDialect) DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error) {
	name := QualifiedName(d, schema, obj.Name)
	if obj.Kind == KindView {
		def, err := queryString(ctx, q, `SELECT OBJECT_DEFINITION(OBJECT_ID(@p1))`, name)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(def) + "\n", nil
	}
	lines, err := scanAll(ctx, q, `SELECT '  ' + QUOTENAME(c.name) + ' ' +
  CASE WHEN c.is_computed = 1 THEN 'AS ' + cc.definition + CASE WHEN cc.is_persisted = 1 THEN ' PERSISTED' ELSE '' END
  ELSE `+sqlserverTypeSQL+`
    + CASE WHEN c.is_identity = 1 THEN ' IDENTITY(' + CAST(ic.seed_value AS varchar(40)) + ',' + CAST(ic.increment_value AS varchar(40)) + ')' ELSE '' END
    + CASE WHEN c.is_nullable = 1 THEN ' NULL' ELSE ' NOT NULL' END
    + COALESCE(' DEFAULT ' + OBJECT_DEFINITION(c.default_object_id), '') END
FROM sys.columns c
JOIN sys.types t ON t.user_type_id = c.user_type_id
LEFT JOIN sys.computed_columns cc ON cc.object_id = c.object_id AND cc.column_id = c.column_id
LEFT JOIN sys.identity_columns ic ON ic.object_id = c.object_id AND ic.column_id = c.column_id
WHERE c.object_id = OBJECT_ID(@p1)
ORDER BY c.column_id`, []any{name}, func(scan func(...any) error) (string, error) {
		var line string
		return line, scan(&line)
	})
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("%s has no columns that %s can read", name, SQLServer.Label())
	}
	ixs, err := sqlserverIndexes(ctx, q, schema, obj.Name)
	if err != nil {
		return "", err
	}
	var after []string
	for _, ix := range ixs {
		if ix.primary || ix.uniqueC {
			lines = append(lines, "  "+ix.definition(name))
		} else {
			after = append(after, ix.definition(name)+";")
		}
	}
	checks, err := scanAll(ctx, q, `SELECT '  CONSTRAINT ' + QUOTENAME(name) + ' CHECK ' + definition FROM sys.check_constraints
WHERE parent_object_id = OBJECT_ID(@p1) ORDER BY name`, []any{name}, func(scan func(...any) error) (string, error) {
		var line string
		return line, scan(&line)
	})
	if err != nil {
		return "", err
	}
	lines = append(lines, checks...)
	fks, err := d.ForeignKeys(ctx, q, schema, obj.Name)
	if err != nil {
		return "", err
	}
	for _, fk := range fks {
		lines = append(lines, "  CONSTRAINT "+d.Quote(fk.Name)+" "+fk.Definition)
	}
	out := "CREATE TABLE " + name + " (\n" + strings.Join(lines, ",\n") + "\n);\n"
	for _, a := range after {
		out += "\n" + a
	}
	return strings.TrimRight(out, "\n") + "\n", nil
}

// sqlserverItemsSQL reads a schema's items as scanItems takes them.
const sqlserverItemsSQL = `SELECT CASE WHEN o.type IN ('P', 'PC') THEN 'procedure' WHEN o.type = 'TR' THEN 'trigger' WHEN o.type = 'SN' THEN 'synonym'
    WHEN o.type = 'SO' THEN 'sequence' ELSE 'function' END,
  o.name, COALESCE(OBJECT_NAME(o.parent_object_id), ''),
  CASE WHEN o.type IN ('P', 'PC', 'FN', 'IF', 'TF', 'FS', 'FT') THEN COALESCE((SELECT STRING_AGG(p.name + ' ' + TYPE_NAME(p.user_type_id), ', ')
    WITHIN GROUP (ORDER BY p.parameter_id) FROM sys.parameters p WHERE p.object_id = o.object_id AND p.parameter_id > 0), '')
    WHEN o.type = 'SN' THEN (SELECT s.base_object_name FROM sys.synonyms s WHERE s.object_id = o.object_id) ELSE '' END,
  CAST(o.object_id AS varchar(20))
FROM sys.objects o
WHERE o.schema_id = SCHEMA_ID(@p1) AND o.is_ms_shipped = 0 AND o.type IN ('P', 'PC', 'FN', 'IF', 'TF', 'FS', 'FT', 'TR', 'SN', 'SO')
UNION ALL
SELECT 'type', t.name, '', CASE WHEN t.is_table_type = 1 THEN 'table' ELSE TYPE_NAME(t.system_type_id) END, 'type:' + CAST(t.user_type_id AS varchar(20))
FROM sys.types t WHERE t.is_user_defined = 1 AND t.schema_id = SCHEMA_ID(@p1)
ORDER BY 1, 2`

// Items lists a schema's procedures, functions, triggers of its tables,
// sequences, synonyms and types.
func (sqlserverDialect) Items(ctx context.Context, q Querier, schema string) ([]Item, error) {
	return scanItems(ctx, q, schema, sqlserverItemsSQL, schema)
}

// ItemDDL writes a routine's or a trigger's definition as it was made; a
// sequence's, a synonym's and a type's from the catalog.
func (d sqlserverDialect) ItemDDL(ctx context.Context, q Querier, it Item) (string, error) {
	var query string
	id := it.ID
	switch it.Kind {
	case ItemFunction, ItemProcedure, ItemTrigger:
		query = `SELECT OBJECT_DEFINITION(CAST(@p1 AS int))`
	case ItemSequence:
		query = `SELECT 'CREATE SEQUENCE ' + QUOTENAME(OBJECT_SCHEMA_NAME(s.object_id)) + '.' + QUOTENAME(s.name) + ' AS ' + TYPE_NAME(s.user_type_id)
  + ' START WITH ' + CAST(s.start_value AS varchar(40)) + ' INCREMENT BY ' + CAST(s.increment AS varchar(40))
  + ' MINVALUE ' + CAST(s.minimum_value AS varchar(40)) + ' MAXVALUE ' + CAST(s.maximum_value AS varchar(40))
  + CASE WHEN s.is_cycling = 1 THEN ' CYCLE' ELSE ' NO CYCLE' END
  + CASE WHEN s.is_cached = 0 THEN ' NO CACHE' WHEN s.cache_size IS NULL THEN ' CACHE' ELSE ' CACHE ' + CAST(s.cache_size AS varchar(20)) END + ';'
FROM sys.sequences s WHERE s.object_id = CAST(@p1 AS int)`
	case ItemSynonym:
		query = `SELECT 'CREATE SYNONYM ' + QUOTENAME(OBJECT_SCHEMA_NAME(s.object_id)) + '.' + QUOTENAME(s.name) + ' FOR ' + s.base_object_name + ';'
FROM sys.synonyms s WHERE s.object_id = CAST(@p1 AS int)`
	case ItemType:
		id = strings.TrimPrefix(id, "type:")
		if _, err := strconv.Atoi(id); err != nil {
			return "", errNoDefinition(it)
		}
		query = `SELECT 'CREATE TYPE ' + QUOTENAME(SCHEMA_NAME(t.schema_id)) + '.' + QUOTENAME(t.name) + CASE WHEN t.is_table_type = 1 THEN
    ' AS TABLE (' + CHAR(10) + (SELECT STRING_AGG('  ' + QUOTENAME(c.name) + ' ' + CASE
      WHEN ct.name IN ('varchar', 'char', 'varbinary', 'binary') THEN ct.name + '(' + CASE WHEN c.max_length = -1 THEN 'max' ELSE CAST(c.max_length AS varchar(10)) END + ')'
      WHEN ct.name IN ('nvarchar', 'nchar') THEN ct.name + '(' + CASE WHEN c.max_length = -1 THEN 'max' ELSE CAST(c.max_length / 2 AS varchar(10)) END + ')'
      WHEN ct.name IN ('decimal', 'numeric') THEN ct.name + '(' + CAST(c.precision AS varchar(10)) + ',' + CAST(c.scale AS varchar(10)) + ')'
      ELSE ct.name END + CASE WHEN c.is_nullable = 1 THEN ' NULL' ELSE ' NOT NULL' END, ',' + CHAR(10)) WITHIN GROUP (ORDER BY c.column_id)
      FROM sys.table_types tt JOIN sys.columns c ON c.object_id = tt.type_table_object_id JOIN sys.types ct ON ct.user_type_id = c.user_type_id
      WHERE tt.user_type_id = t.user_type_id) + CHAR(10) + ');'
  ELSE ' FROM ' + CASE
      WHEN b.name IN ('varchar', 'char', 'varbinary', 'binary') THEN b.name + '(' + CASE WHEN t.max_length = -1 THEN 'max' ELSE CAST(t.max_length AS varchar(10)) END + ')'
      WHEN b.name IN ('nvarchar', 'nchar') THEN b.name + '(' + CASE WHEN t.max_length = -1 THEN 'max' ELSE CAST(t.max_length / 2 AS varchar(10)) END + ')'
      WHEN b.name IN ('decimal', 'numeric') THEN b.name + '(' + CAST(t.precision AS varchar(10)) + ',' + CAST(t.scale AS varchar(10)) + ')'
      ELSE b.name END + CASE WHEN t.is_nullable = 1 THEN ' NULL' ELSE ' NOT NULL' END + ';' END
FROM sys.types t LEFT JOIN sys.types b ON b.user_type_id = t.system_type_id WHERE t.user_type_id = CAST(@p1 AS int)`
	default:
		return "", errNoDefinition(it)
	}
	def, err := queryString(ctx, q, query, id)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(def) == "" {
		// OBJECT_DEFINITION is NULL for an encrypted routine, or one the
		// login may not see the text of.
		return "", fmt.Errorf("SQL Server shows no definition of the %s %s: it is encrypted, or the login lacks VIEW DEFINITION", it.Kind, it.Name)
	}
	return strings.TrimSpace(def) + "\n", nil
}

// sqlserverSearchSQL finds, as searchWith's do, a database's tables,
// views, columns, routines, triggers, sequences, synonyms and types, @p1
// the lower-case text, @p2 whether definitions are searched.
const sqlserverSearchSQL = `SELECT TOP (@p3) s, kind, name, tbl, tkind, detail, id, CASE WHEN @p2 = 1 THEN def ELSE '' END FROM (
SELECT SCHEMA_NAME(o.schema_id) AS s,
  CASE o.type WHEN 'U' THEN 'table' WHEN 'V' THEN 'view' WHEN 'P' THEN 'procedure' WHEN 'PC' THEN 'procedure' WHEN 'TR' THEN 'trigger'
    WHEN 'SN' THEN 'synonym' WHEN 'SO' THEN 'sequence' ELSE 'function' END AS kind,
  o.name AS name, COALESCE(OBJECT_NAME(o.parent_object_id), '') AS tbl, '' AS tkind, '' AS detail,
  CAST(o.object_id AS varchar(20)) AS id, COALESCE(m.definition, '') AS def
FROM sys.objects o LEFT JOIN sys.sql_modules m ON m.object_id = o.object_id
WHERE o.is_ms_shipped = 0 AND o.type IN ('U', 'V', 'P', 'PC', 'FN', 'IF', 'TF', 'FS', 'FT', 'TR', 'SN', 'SO')
  AND (CHARINDEX(@p1, LOWER(o.name)) > 0 OR @p2 = 1 AND CHARINDEX(@p1, LOWER(m.definition)) > 0)
UNION ALL
SELECT SCHEMA_NAME(o.schema_id), 'column', c.name, o.name, CASE o.type WHEN 'V' THEN 'view' ELSE 'table' END, TYPE_NAME(c.user_type_id), '', ''
FROM sys.columns c JOIN sys.objects o ON o.object_id = c.object_id
WHERE o.is_ms_shipped = 0 AND o.type IN ('U', 'V') AND CHARINDEX(@p1, LOWER(c.name)) > 0
UNION ALL
SELECT SCHEMA_NAME(t.schema_id), 'type', t.name, '', '', '', 'type:' + CAST(t.user_type_id AS varchar(20)), ''
FROM sys.types t WHERE t.is_user_defined = 1 AND CHARINDEX(@p1, LOWER(t.name)) > 0
) hits ORDER BY 1, 3, 4`

// sqlserverSearch is Search on SQL Server, whose T-SQL keeps to TOP.
func sqlserverSearch(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	return scanHits(ctx, d, text, sqlserverSearchSQL, []any{text, definitions, SearchLimit + 1})
}

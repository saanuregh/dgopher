package db

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"
)

// KindColumn is the kind of a SearchHit that is a column of a table or a
// view.
const KindColumn = "column"

// SearchLimit is the most hits a search gives.
const SearchLimit = 1000

// SearchHit is an object of a database whose name, or definition, holds
// the text searched for.
type SearchHit struct {
	Schema string
	// Kind is an ObjectKind, an ItemKind or KindColumn.
	Kind string
	Name string
	// Table is the table a column, a trigger, a partition or a projection
	// is of, and TableKind a column's table's kind.
	Table     string
	TableKind ObjectKind
	// Detail and ID are an item's, a column's type its Detail.
	Detail, ID string
	// Excerpt is the line of the definition holding the text, "" when the
	// name holds it.
	Excerpt string
}

// IsObject says whether the hit is a table or a view.
func (h SearchHit) IsObject() bool {
	switch ObjectKind(h.Kind) {
	case KindTable, KindView, KindMaterializedView, KindForeignTable, KindDictionary:
		return true
	}
	return false
}

// Object is the table or view the hit is, or a column's.
func (h SearchHit) Object() Object {
	if h.Kind == KindColumn {
		return Object{Schema: h.Schema, Name: h.Table, Kind: h.TableKind, Rows: -1, Bytes: -1}
	}
	return Object{Schema: h.Schema, Name: h.Name, Kind: ObjectKind(h.Kind), Rows: -1, Bytes: -1}
}

// Item is the item the hit is, unless it is a table, a view or a column.
func (h SearchHit) Item() Item {
	return Item{Schema: h.Schema, Name: h.Name, Kind: ItemKind(h.Kind), Table: h.Table, Detail: h.Detail, ID: h.ID}
}

// Search finds the tables, views, columns, routines, triggers and other
// objects of every schema of a database whose name holds text, ignoring
// case, and with definitions those whose definition holds it too: a
// view's query, a routine's body, a trigger's. It gives at most
// SearchLimit hits, and says whether there were more.
func Search(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	text = strings.ToLower(text)
	var query string
	var args []any
	switch d.Dialect.Engine() {
	case Postgres:
		query, args = postgresSearch, []any{text, definitions}
	case MySQL:
		query, args = mysqlSearch, []any{text, definitions, text}
	case ClickHouse:
		query, args = clickhouseSearch, []any{text, definitions, text}
	case DuckDB:
		query, args = duckdbSearch, []any{text, definitions}
	case SQLite:
		schemas, err := d.Dialect.Schemas(ctx, d.Catalog())
		if err != nil {
			return nil, false, err
		}
		parts := make([]string, len(schemas))
		for i, s := range schemas {
			parts[i] = strings.ReplaceAll(sqliteSearch, "SCHEMA", d.Dialect.Quote(s))
			args = append(args, s, s, s)
		}
		query = `SELECT * FROM (` + strings.Join(parts, "\nUNION ALL\n") + `)
WHERE instr(lower(name), ?) > 0 OR (? AND instr(lower(def), ?) > 0)`
		args = append(args, text, definitions, text)
	default:
		return nil, false, nil
	}
	query += " ORDER BY 1, 3, 4 LIMIT " + strconv.Itoa(SearchLimit+1)
	var hits []SearchHit
	err := scanRows(ctx, d.Catalog(), query, args, func(scan func(...any) error) error {
		var h SearchHit
		var tableKind, def string
		if err := scan(&h.Schema, &h.Kind, &h.Name, &h.Table, &tableKind, &h.Detail, &h.ID, &def); err != nil {
			return err
		}
		h.TableKind = ObjectKind(tableKind)
		if !strings.Contains(strings.ToLower(h.Name), text) {
			h.Excerpt = excerpt(def, text)
		}
		hits = append(hits, h)
		return nil
	})
	if len(hits) > SearchLimit {
		return hits[:SearchLimit], true, err
	}
	return hits, false, err
}

// excerpt is the first line of def holding text, which is in lower
// case, trimmed and cut to the part around it.
func excerpt(def, text string) string {
	for line := range strings.Lines(def) {
		lower := strings.ToLower(line)
		at := strings.Index(lower, text)
		if at < 0 {
			continue
		}
		if len(lower) != len(line) {
			at = 0 // lowering changed the length: positions do not carry over
		}
		at -= len(line) - len(strings.TrimLeft(line, " \t"))
		line = strings.TrimSpace(line)
		const width = 120
		if len(line) <= width {
			return line
		}
		from := min(max(0, at+len(text)/2-width/2), len(line)-width)
		to := from + width
		for from > 0 && !utf8.RuneStart(line[from]) {
			from--
		}
		for to < len(line) && !utf8.RuneStart(line[to]) {
			to--
		}
		out := line[from:to]
		if from > 0 {
			out = "…" + out
		}
		if to < len(line) {
			out += "…"
		}
		return out
	}
	return ""
}

// The searches of each engine give, in this order: schema, kind, name,
// table, the table's kind, detail, ID and definition, filtered by the
// lower-case text and whether definitions are searched.

const postgresSearch = `SELECT s, kind, name, tbl, tkind, detail, id, CASE WHEN $2 THEN def ELSE '' END FROM (
SELECT n.nspname AS s, CASE WHEN c.relispartition THEN 'partition' ELSE CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view'
    WHEN 'f' THEN 'foreign table' ELSE 'table' END END AS kind, c.relname AS name,
  COALESCE((SELECT p.relname FROM pg_inherits i JOIN pg_class p ON p.oid = i.inhparent WHERE c.relispartition AND i.inhrelid = c.oid), '') AS tbl,
  '' AS tkind, '' AS detail, c.oid::text AS id,
  CASE WHEN c.relkind IN ('v', 'm') THEN pg_get_viewdef(c.oid) ELSE '' END AS def
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
UNION ALL
SELECT n.nspname, 'column', a.attname, c.relname,
  CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view' WHEN 'f' THEN 'foreign table' ELSE 'table' END,
  format_type(a.atttypid, a.atttypmod), '', ''
FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f') AND NOT c.relispartition AND a.attnum > 0 AND NOT a.attisdropped
UNION ALL
SELECT n.nspname, CASE p.prokind WHEN 'p' THEN 'procedure' ELSE 'function' END, p.proname, '', '',
  pg_get_function_identity_arguments(p.oid), p.oid::text, COALESCE(p.prosrc, '')
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
UNION ALL
SELECT n.nspname, 'trigger', t.tgname, c.relname, '', '', t.oid::text, pg_get_triggerdef(t.oid)
FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE NOT t.tgisinternal
UNION ALL
SELECT n.nspname, 'sequence', c.relname, '', '', '', c.oid::text, ''
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'S'
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'i')
UNION ALL
SELECT n.nspname, 'type', t.typname, '', '', CASE t.typtype WHEN 'e' THEN 'enum' WHEN 'd' THEN 'domain' WHEN 'c' THEN 'composite' ELSE 'range' END,
  t.oid::text, ''
FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
WHERE t.typtype IN ('e', 'd', 'c', 'r')
  AND (t.typtype <> 'c' OR (SELECT c.relkind FROM pg_class c WHERE c.oid = t.typrelid) = 'c')
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_type'::regclass AND d.objid = t.oid AND d.deptype = 'e')
) found
WHERE s NOT LIKE 'pg\_%' AND s <> 'information_schema'
  AND (strpos(lower(name), $1) > 0 OR ($2 AND strpos(lower(def), $1) > 0))`

// MySQL 8.4, pushing the filter into the UNION of information_schema
// views, loses the views whose definition matches: the hint keeps it out.
const mysqlSearch = `SELECT /*+ NO_DERIVED_CONDITION_PUSHDOWN(found) */ * FROM (
SELECT t.TABLE_SCHEMA AS s, IF(t.TABLE_TYPE LIKE '%VIEW', 'view', 'table') AS kind, t.TABLE_NAME AS name, '' AS tbl, '' AS tkind,
  '' AS detail, '' AS id, COALESCE(v.VIEW_DEFINITION, '') AS def
FROM information_schema.TABLES t
LEFT JOIN information_schema.VIEWS v ON v.TABLE_SCHEMA = t.TABLE_SCHEMA AND v.TABLE_NAME = t.TABLE_NAME
UNION ALL
SELECT c.TABLE_SCHEMA, 'column', c.COLUMN_NAME, c.TABLE_NAME, IF(t.TABLE_TYPE LIKE '%VIEW', 'view', 'table'), c.COLUMN_TYPE, '', ''
FROM information_schema.COLUMNS c
JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME
UNION ALL
SELECT ROUTINE_SCHEMA, LOWER(ROUTINE_TYPE), ROUTINE_NAME, '', '', '', '', COALESCE(ROUTINE_DEFINITION, '')
FROM information_schema.ROUTINES
UNION ALL
SELECT TRIGGER_SCHEMA, 'trigger', TRIGGER_NAME, EVENT_OBJECT_TABLE, '', CONCAT(ACTION_TIMING, ' ', EVENT_MANIPULATION), '', ACTION_STATEMENT
FROM information_schema.TRIGGERS
UNION ALL
SELECT EVENT_SCHEMA, 'event', EVENT_NAME, '', '', STATUS, '', EVENT_DEFINITION
FROM information_schema.EVENTS
) found
WHERE s NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
  AND (LOCATE(?, LOWER(name)) > 0 OR (? AND LOCATE(?, LOWER(def)) > 0))`

const clickhouseSearch = `SELECT * FROM (
SELECT database AS s, multiIf(engine IN ('View', 'LiveView', 'WindowView'), 'view', engine = 'MaterializedView', 'materialized view',
    engine = 'Dictionary', 'dictionary', 'table') AS kind, name, '' AS tbl, '' AS tkind, '' AS detail, '' AS id, as_select AS def
FROM system.tables WHERE NOT is_temporary
UNION ALL
SELECT c.database, 'column', c.name, c.table, multiIf(t.engine IN ('View', 'LiveView', 'WindowView'), 'view',
    t.engine = 'MaterializedView', 'materialized view', t.engine = 'Dictionary', 'dictionary', 'table'), c.type, '', ''
FROM system.columns c JOIN system.tables t ON t.database = c.database AND t.name = c.table
) found
WHERE s NOT IN ('system', 'INFORMATION_SCHEMA', 'information_schema')
  AND (positionCaseInsensitiveUTF8(name, ?) > 0 OR (? AND positionCaseInsensitiveUTF8(def, ?) > 0))`

const duckdbSearch = `SELECT * FROM (
SELECT schema_name AS s, 'table' AS kind, table_name AS name, '' AS tbl, '' AS tkind, '' AS detail, '' AS id, '' AS def
FROM duckdb_tables() WHERE database_name = current_database() AND NOT internal
UNION ALL
SELECT schema_name, 'view', view_name, '', '', '', '', COALESCE(sql, '')
FROM duckdb_views() WHERE database_name = current_database() AND NOT internal
UNION ALL
SELECT c.schema_name, 'column', c.column_name, c.table_name, CASE WHEN v.view_name IS NULL THEN 'table' ELSE 'view' END, c.data_type, '', ''
FROM duckdb_columns() c
LEFT JOIN duckdb_views() v ON v.database_name = c.database_name AND v.schema_name = c.schema_name AND v.view_name = c.table_name
WHERE c.database_name = current_database() AND NOT c.internal
UNION ALL
SELECT schema_name, 'function', function_name, '', '', array_to_string(parameters, ', '), function_type, COALESCE(macro_definition, '')
FROM duckdb_functions() WHERE database_name = current_database() AND NOT internal AND function_type IN ('macro', 'table_macro')
UNION ALL
SELECT schema_name, 'sequence', sequence_name, '', '', '', '', ''
FROM duckdb_sequences() WHERE database_name = current_database() AND NOT temporary
UNION ALL
SELECT schema_name, 'type', type_name, '', '', lower(logical_type), '', ''
FROM duckdb_types() WHERE database_name = current_database() AND NOT internal
) found
WHERE s NOT IN ('information_schema', 'pg_catalog')
  AND (contains(lower(name), $1) OR ($2 AND contains(lower(def), $1)))`

// sqliteSearch is the search of one schema, named SCHEMA, given its name
// three times.
const sqliteSearch = `SELECT ? AS s, type AS kind, name, CASE type WHEN 'trigger' THEN tbl_name ELSE '' END AS tbl, '' AS tkind,
  '' AS detail, '' AS id, CASE type WHEN 'table' THEN '' ELSE COALESCE(sql, '') END AS def
FROM SCHEMA.sqlite_master WHERE type IN ('table', 'view', 'trigger') AND name NOT LIKE 'sqlite\_%' ESCAPE '\'
UNION ALL
SELECT ?, 'column', p.name, m.name, m.type, p.type, '', ''
FROM SCHEMA.sqlite_master m, pragma_table_info(m.name, ?) p
WHERE m.type IN ('table', 'view') AND m.name NOT LIKE 'sqlite\_%' ESCAPE '\'`

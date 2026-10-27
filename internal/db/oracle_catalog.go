package db

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// Oracle's catalog is read from its ALL_ views, which show what the user
// may reach without a DBA's privileges; a schema is a user.

// Databases are none: each pluggable database is a connection of its own.
func (oracleDialect) Databases(context.Context, Querier) ([]string, error) { return nil, nil }

func (oracleDialect) CurrentSchema(ctx context.Context, q Querier) (string, error) {
	return queryString(ctx, q, `SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual`)
}

// Schemas lists the users that own objects the user may see, but those
// Oracle maintains, as SYS; the user's own, and its current schema,
// always, first.
func (oracleDialect) Schemas(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT u.username FROM all_users u
WHERE u.username = USER OR u.username = SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')
  OR u.oracle_maintained = 'N' AND EXISTS (SELECT 1 FROM all_objects o WHERE o.owner = u.username)
ORDER BY CASE WHEN u.username = USER THEN 0 ELSE 1 END, u.username`)
}

// oracleObjectsSQL reads a schema's tables, views and materialized views,
// with each table's estimate of its rows, from its statistics, and its
// comment; not those in the recycle bin, nor the tables Oracle makes for
// materialized views, nested tables and IOT overflows.
const oracleObjectsSQL = `SELECT o.object_name, o.object_type, NVL(t.num_rows, -1), NVL(c.comments, m.comments)
FROM all_objects o
LEFT JOIN all_tables t ON t.owner = o.owner AND t.table_name = o.object_name
LEFT JOIN all_tab_comments c ON c.owner = o.owner AND c.table_name = o.object_name AND o.object_type <> 'MATERIALIZED VIEW'
LEFT JOIN all_mview_comments m ON m.owner = o.owner AND m.mview_name = o.object_name AND o.object_type = 'MATERIALIZED VIEW'
WHERE o.owner = :1 AND o.object_type IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW') AND o.object_name NOT LIKE 'BIN$%'
  AND NOT (o.object_type = 'TABLE' AND EXISTS (SELECT 1 FROM all_mviews v WHERE v.owner = o.owner AND v.container_name = o.object_name))
  AND (t.nested IS NULL OR t.nested = 'NO') AND (t.iot_type IS NULL OR t.iot_type = 'IOT')
ORDER BY o.object_name`

func (oracleDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	objs, err := oracleObjects(ctx, q, schema)
	if err != nil {
		return nil, err
	}
	sizes, err := oracleSizes(ctx, q, schema)
	if err != nil {
		// DBA_SEGMENTS needs a privilege for another user's schema: its
		// tables' sizes are unknown.
		return objs, nil
	}
	for i := range objs {
		if b, ok := sizes[objs[i].Name]; ok && objs[i].Kind != KindView {
			objs[i].Bytes = b
		}
	}
	return objs, nil
}

func (oracleDialect) ObjectNames(ctx context.Context, q Querier, schema string) ([]Object, error) {
	objs, err := oracleObjects(ctx, q, schema)
	for i := range objs {
		objs[i].Rows = -1
	}
	return objs, err
}

func oracleObjects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return scanAll(ctx, q, oracleObjectsSQL, []any{schema}, func(scan func(...any) error) (Object, error) {
		o := Object{Schema: schema, Bytes: -1}
		var kind string
		var comment sql.NullString
		if err := scan(&o.Name, &kind, &o.Rows, &comment); err != nil {
			return o, err
		}
		o.Comment = comment.String
		switch kind {
		case "VIEW":
			o.Kind, o.Rows = KindView, -1
		case "MATERIALIZED VIEW":
			o.Kind = KindMaterializedView
		default:
			o.Kind = KindTable
		}
		return o, nil
	})
}

// oracleSizes reads, in one query, the bytes of each table of a schema:
// its segments', its partitions', its indexes' and its LOBs'. The user's
// own come from USER_SEGMENTS; another's need DBA_SEGMENTS.
func oracleSizes(ctx context.Context, q Querier, schema string) (map[string]int64, error) {
	current, err := queryString(ctx, q, `SELECT USER FROM dual`)
	if err != nil {
		return nil, err
	}
	var args []any
	// Binds are positions, numbered as they come.
	bind := func() string {
		args = append(args, schema)
		return ":" + strconv.Itoa(len(args))
	}
	segments := func() string {
		if current == schema {
			return "user_segments"
		}
		return "(SELECT segment_name, segment_type, bytes FROM dba_segments WHERE owner = " + bind() + ")"
	}
	query := `SELECT t, SUM(bytes) FROM (
  SELECT s.segment_name AS t, s.bytes FROM ` + segments() + ` s WHERE s.segment_type LIKE 'TABLE%'
  UNION ALL SELECT i.table_name, s.bytes FROM all_indexes i JOIN ` + segments() + ` s ON s.segment_name = i.index_name AND s.segment_type LIKE 'INDEX%'
    WHERE i.owner = ` + bind() + `
  UNION ALL SELECT l.table_name, s.bytes FROM all_lobs l JOIN ` + segments() + ` s ON s.segment_name IN (l.segment_name, l.index_name)
    WHERE l.owner = ` + bind() + `
) GROUP BY t`
	out := map[string]int64{}
	err = scanRows(ctx, q, query, args, func(scan func(...any) error) error {
		var name string
		var bytes int64
		if err := scan(&name, &bytes); err != nil {
			return err
		}
		out[name] = bytes
		return nil
	})
	return out, err
}

// oracleTypeSQL writes the type of a column of ALL_TAB_COLS c as a CREATE
// TABLE names it: with its length, in characters where the column counts
// them, precision and scale; an object type with its owner.
const oracleTypeSQL = `CASE
    WHEN c.data_type IN ('VARCHAR2', 'CHAR') THEN c.data_type || '(' || c.char_length || CASE c.char_used WHEN 'C' THEN ' CHAR' END || ')'
    WHEN c.data_type IN ('NVARCHAR2', 'NCHAR') THEN c.data_type || '(' || c.char_length || ')'
    WHEN c.data_type IN ('RAW', 'UROWID') THEN c.data_type || '(' || c.data_length || ')'
    WHEN c.data_type = 'NUMBER' AND c.data_precision IS NULL AND c.data_scale = 0 THEN 'NUMBER(*,0)'
    WHEN c.data_type = 'NUMBER' AND c.data_precision IS NOT NULL AND c.data_scale = 0 THEN 'NUMBER(' || c.data_precision || ')'
    WHEN c.data_type = 'NUMBER' AND c.data_precision IS NOT NULL THEN 'NUMBER(' || c.data_precision || ',' || c.data_scale || ')'
    WHEN c.data_type = 'FLOAT' AND c.data_precision IS NOT NULL THEN 'FLOAT(' || c.data_precision || ')'
    WHEN c.data_type_owner IS NOT NULL AND c.data_type_owner NOT IN ('SYS', 'PUBLIC') THEN '"' || c.data_type_owner || '"."' || c.data_type || '"'
    ELSE c.data_type END`

// oracleColumnsSQL reads the columns of a schema's tables and views, in
// the order oracleScanColumns takes them: DATA_DEFAULT is a LONG, which
// SQL cannot trim or compare, read as it is.
const oracleColumnsSQL = `SELECT c.table_name, c.column_name, ` + oracleTypeSQL + `, CASE c.nullable WHEN 'Y' THEN 1 ELSE 0 END, c.data_default,
  CASE WHEN EXISTS (SELECT 1 FROM all_constraints k JOIN all_cons_columns kc ON kc.owner = k.owner AND kc.constraint_name = k.constraint_name
    WHERE k.owner = c.owner AND k.table_name = c.table_name AND k.constraint_type = 'P' AND kc.column_name = c.column_name) THEN 1 ELSE 0 END,
  CASE c.identity_column WHEN 'YES' THEN 1 ELSE 0 END, cc.comments, CASE c.virtual_column WHEN 'YES' THEN 1 ELSE 0 END
FROM all_tab_cols c
LEFT JOIN all_col_comments cc ON cc.owner = c.owner AND cc.table_name = c.table_name AND cc.column_name = c.column_name
WHERE c.owner = :1 AND c.hidden_column = 'NO'`

func (oracleDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	cols, err := oracleScanColumns(ctx, q, oracleColumnsSQL+` AND c.table_name = :2 ORDER BY c.column_id`, []any{schema, table})
	return cols[table], err
}

func (oracleDialect) SchemaColumns(ctx context.Context, q Querier, schema string) (map[string][]Column, error) {
	return oracleScanColumns(ctx, q, oracleColumnsSQL+` ORDER BY c.table_name, c.column_id`, []any{schema})
}

// oracleScanColumns reads the columns of oracleColumnsSQL by their table:
// an identity column numbers its rows, its default the sequence it draws
// on, which is not shown; a virtual column is generated, its default its
// expression.
func oracleScanColumns(ctx context.Context, q Querier, query string, args []any) (map[string][]Column, error) {
	out := map[string][]Column{}
	err := scanRows(ctx, q, query, args, func(scan func(...any) error) error {
		var t string
		var c Column
		var def, comment sql.NullString
		if err := scan(&t, &c.Name, &c.Type, &c.Nullable, &def, &c.PrimaryKey, &c.AutoIncrement, &comment, &c.Generated); err != nil {
			return err
		}
		c.Comment = comment.String
		// DEFAULT NULL, which removes a default, is kept as its text.
		if text := strings.TrimSpace(def.String); text != "" && !strings.EqualFold(text, "NULL") {
			if !c.AutoIncrement {
				c.Default = text
			}
			c.HasDefault = true
		}
		out[t] = append(out[t], c)
		return nil
	})
	return out, err
}

// oracleIndexesSQL reads a table's indexes, the primary key's first, with
// their columns in order, an expression's by its hidden column's name.
const oracleIndexesSQL = `SELECT i.index_name, CASE i.uniqueness WHEN 'UNIQUE' THEN 1 ELSE 0 END,
  CASE WHEN EXISTS (SELECT 1 FROM all_constraints k WHERE k.owner = i.table_owner AND k.table_name = i.table_name
    AND k.constraint_type = 'P' AND k.index_name = i.index_name) THEN 1 ELSE 0 END,
  (SELECT LISTAGG(ic.column_name || CASE ic.descend WHEN 'DESC' THEN ' DESC' END, CHR(31)) WITHIN GROUP (ORDER BY ic.column_position)
    FROM all_ind_columns ic WHERE ic.index_owner = i.owner AND ic.index_name = i.index_name)
FROM all_indexes i
WHERE i.table_owner = :1 AND i.table_name = :2 AND i.index_type <> 'LOB'
ORDER BY 3 DESC, i.index_name`

func (d oracleDialect) Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error) {
	return scanAll(ctx, q, oracleIndexesSQL, []any{schema, table}, func(scan func(...any) error) (Index, error) {
		var ix Index
		var cols sql.NullString
		if err := scan(&ix.Name, &ix.Unique, &ix.Primary, &cols); err != nil {
			return ix, err
		}
		var quoted []string
		for _, c := range splitList(cols.String) {
			name, desc := strings.CutSuffix(c, " DESC")
			ix.Columns = append(ix.Columns, name)
			q := d.Quote(name)
			if desc {
				q += " DESC"
			}
			quoted = append(quoted, q)
		}
		unique := ""
		if ix.Unique {
			unique = "UNIQUE "
		}
		ix.Definition = "CREATE " + unique + "INDEX " + QualifiedName(d, schema, ix.Name) + " ON " + QualifiedName(d, schema, table) +
			" (" + strings.Join(quoted, ", ") + ")"
		return ix, nil
	})
}

// oracleKeyColumns lists a constraint's columns, of ALL_CONSTRAINTS k, in
// order, separated by unitSep.
func oracleKeyColumns(k string) string {
	return `(SELECT LISTAGG(kc.column_name, CHR(31)) WITHIN GROUP (ORDER BY kc.position) FROM all_cons_columns kc
    WHERE kc.owner = ` + k + `.owner AND kc.constraint_name = ` + k + `.constraint_name)`
}

func (d oracleDialect) ForeignKeys(ctx context.Context, q Querier, schema, table string) ([]ForeignKey, error) {
	return scanAll(ctx, q, `SELECT k.constraint_name, r.owner, r.table_name, k.delete_rule, `+oracleKeyColumns("k")+`, `+oracleKeyColumns("r")+`
FROM all_constraints k JOIN all_constraints r ON r.owner = k.r_owner AND r.constraint_name = k.r_constraint_name
WHERE k.constraint_type = 'R' AND k.owner = :1 AND k.table_name = :2
ORDER BY k.constraint_name`, []any{schema, table}, func(scan func(...any) error) (ForeignKey, error) {
		var fk ForeignKey
		var rule string
		var cols, refCols sql.NullString
		if err := scan(&fk.Name, &fk.RefSchema, &fk.RefTable, &rule, &cols, &refCols); err != nil {
			return fk, err
		}
		fk.Columns, fk.RefColumns = splitList(cols.String), splitList(refCols.String)
		if rule != "NO ACTION" {
			fk.OnDelete = rule
		}
		fk.Definition = "FOREIGN KEY (" + QuoteNames(d, fk.Columns) + ") REFERENCES " + QualifiedName(d, fk.RefSchema, fk.RefTable) +
			" (" + QuoteNames(d, fk.RefColumns) + ")"
		if fk.OnDelete != "" {
			fk.Definition += " ON DELETE " + fk.OnDelete
		}
		return fk, nil
	})
}

func (oracleDialect) ReferencedBy(ctx context.Context, q Querier, schema, table string) ([]Reference, error) {
	return scanAll(ctx, q, `SELECT k.constraint_name, k.owner, k.table_name, `+oracleKeyColumns("k")+`, `+oracleKeyColumns("r")+`
FROM all_constraints r JOIN all_constraints k ON k.r_owner = r.owner AND k.r_constraint_name = r.constraint_name AND k.constraint_type = 'R'
WHERE r.owner = :1 AND r.table_name = :2 AND r.constraint_type IN ('P', 'U')
ORDER BY 2, 3, 1`, []any{schema, table}, func(scan func(...any) error) (Reference, error) {
		var r Reference
		var cols, refCols sql.NullString
		if err := scan(&r.Name, &r.Schema, &r.Table, &cols, &refCols); err != nil {
			return r, err
		}
		r.Columns, r.RefColumns = splitList(cols.String), splitList(refCols.String)
		return r, nil
	})
}

// oracleMetadataSQL writes an object's DDL with DBMS_METADATA.GET_DDL, its
// statements ended by ; and its PL/SQL units by a / line, as SQL*Plus and
// the editor run them, without its storage clauses. In a function of the
// query: a PL/SQL block setting the session's transform would be
// committed by the driver, and could run on another connection.
const oracleMetadataSQL = `WITH FUNCTION dgopher_ddl(kind VARCHAR2, name VARCHAR2, owner VARCHAR2) RETURN CLOB IS
BEGIN
  DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SQLTERMINATOR', TRUE);
  DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SEGMENT_ATTRIBUTES', FALSE);
  RETURN DBMS_METADATA.GET_DDL(kind, name, owner);
END;
SELECT dgopher_ddl(:1, :2, :3) FROM dual`

// oracleMetadata is the DDL of an object of a metadata kind, as TABLE or
// PACKAGE.
func oracleMetadata(ctx context.Context, q Querier, kind, schema, name string) (string, error) {
	ddl, err := queryString(ctx, q, oracleMetadataSQL, kind, name, schema)
	if oracleErrorIs(err, 31603) {
		return "", fmt.Errorf("%w: DBMS_METADATA writes the DDL of another user's objects only for a user with SELECT_CATALOG_ROLE", err)
	}
	return strings.TrimSpace(ddl) + "\n", err
}

// DDL writes a table's, a view's or a materialized view's DDL, a table's
// with its indexes but those of its keys.
func (d oracleDialect) DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error) {
	kind := "TABLE"
	switch obj.Kind {
	case KindView:
		kind = "VIEW"
	case KindMaterializedView:
		kind = "MATERIALIZED_VIEW"
	}
	ddl, err := oracleMetadata(ctx, q, kind, schema, obj.Name)
	if err != nil || kind != "TABLE" {
		return ddl, err
	}
	indexes, err := queryStrings(ctx, q, `SELECT i.index_name FROM all_indexes i
WHERE i.table_owner = :1 AND i.table_name = :2 AND i.index_type <> 'LOB'
  AND NOT EXISTS (SELECT 1 FROM all_constraints k WHERE k.owner = i.table_owner AND k.table_name = i.table_name AND k.index_name = i.index_name)
ORDER BY i.index_name`, schema, obj.Name)
	if err != nil {
		return "", err
	}
	for _, ix := range indexes {
		def, err := oracleMetadata(ctx, q, "INDEX", schema, ix)
		if err != nil {
			return "", err
		}
		ddl += "\n" + def
	}
	return ddl, nil
}

// oracleItemsSQL reads a schema's items as scanItems takes them, the
// object type their ID: procedures and functions with their arguments,
// packages, triggers with their tables, sequences but those of identity
// columns, synonyms with what they stand for, and types.
const oracleItemsSQL = `SELECT LOWER(o.object_type), o.object_name,
  (SELECT tr.table_name FROM all_triggers tr WHERE tr.owner = o.owner AND tr.trigger_name = o.object_name),
  CASE WHEN o.object_type IN ('PROCEDURE', 'FUNCTION') THEN (SELECT LISTAGG(a.argument_name || ' ' || a.data_type, ', ') WITHIN GROUP (ORDER BY a.position)
      FROM all_arguments a WHERE a.owner = o.owner AND a.object_name = o.object_name AND a.package_name IS NULL AND a.data_level = 0
        AND a.argument_name IS NOT NULL)
    WHEN o.object_type = 'SYNONYM' THEN (SELECT s.table_owner || '.' || s.table_name FROM all_synonyms s WHERE s.owner = o.owner AND s.synonym_name = o.object_name)
    WHEN o.object_type = 'TYPE' THEN (SELECT ty.typecode FROM all_types ty WHERE ty.owner = o.owner AND ty.type_name = o.object_name) END,
  o.object_type
FROM all_objects o
WHERE o.owner = :1 AND o.object_type IN ('PROCEDURE', 'FUNCTION', 'PACKAGE', 'TRIGGER', 'SEQUENCE', 'SYNONYM', 'TYPE')
  AND o.object_name NOT LIKE 'BIN$%' AND o.object_name NOT LIKE 'ISEQ$$%'
ORDER BY 1, 2`

// Items lists a schema's procedures, functions, packages, triggers,
// sequences, synonyms and types.
func (oracleDialect) Items(ctx context.Context, q Querier, schema string) ([]Item, error) {
	return scanItems(ctx, q, schema, oracleItemsSQL, schema)
}

// ItemDDL writes an item's DDL with DBMS_METADATA, a package's spec and
// body, a trigger's ALTER TRIGGER … ENABLE, each a statement of its own.
func (d oracleDialect) ItemDDL(ctx context.Context, q Querier, it Item) (string, error) {
	switch it.ID {
	case "PROCEDURE", "FUNCTION", "PACKAGE", "TRIGGER", "SEQUENCE", "SYNONYM", "TYPE":
		return oracleMetadata(ctx, q, it.ID, it.Schema, it.Name)
	}
	return "", errNoDefinition(it)
}

// oracleSearchSQL finds, as searchWith's do, the tables, views, columns,
// routines, packages, triggers, sequences, synonyms and types of the
// schemas Schemas lists whose name holds the text, and with definitions
// the views whose query, and the code whose lines, hold it. Its binds are
// numbered in the order they come: the text, whether definitions are
// searched and the limit.
const oracleSearchSQL = `SELECT s, kind, name, tbl, tkind, detail, id, def FROM (
SELECT o.owner AS s, LOWER(o.object_type) AS kind, o.object_name AS name,
  (SELECT tr.table_name FROM all_triggers tr WHERE tr.owner = o.owner AND tr.trigger_name = o.object_name) AS tbl,
  CAST(NULL AS VARCHAR2(30)) AS tkind, CAST(NULL AS VARCHAR2(30)) AS detail,
  CASE WHEN o.object_type NOT IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW') THEN o.object_type END AS id,
  CASE WHEN :1 = 1 THEN NVL((SELECT v.text_vc FROM all_views v WHERE v.owner = o.owner AND v.view_name = o.object_name AND o.object_type = 'VIEW'),
    (SELECT MIN(src.text) KEEP (DENSE_RANK FIRST ORDER BY src.line) FROM all_source src
      WHERE src.owner = o.owner AND src.name = o.object_name AND src.type = o.object_type AND INSTR(LOWER(src.text), :2) > 0)) END AS def
FROM all_objects o JOIN all_users u ON u.username = o.owner
WHERE (u.oracle_maintained = 'N' OR u.username = USER)
  AND o.object_type IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW', 'PROCEDURE', 'FUNCTION', 'PACKAGE', 'TRIGGER', 'SEQUENCE', 'SYNONYM', 'TYPE')
  AND o.object_name NOT LIKE 'BIN$%' AND o.object_name NOT LIKE 'ISEQ$$%'
  AND (INSTR(LOWER(o.object_name), :3) > 0 OR :4 = 1 AND (
    EXISTS (SELECT 1 FROM all_views v WHERE v.owner = o.owner AND v.view_name = o.object_name AND o.object_type = 'VIEW' AND INSTR(LOWER(v.text_vc), :5) > 0)
    OR EXISTS (SELECT 1 FROM all_source src WHERE src.owner = o.owner AND src.name = o.object_name AND src.type = o.object_type AND INSTR(LOWER(src.text), :6) > 0)))
UNION ALL
SELECT c.owner, 'column', c.column_name, c.table_name, LOWER(o.object_type), c.data_type, NULL, NULL
FROM all_tab_cols c JOIN all_objects o ON o.owner = c.owner AND o.object_name = c.table_name AND o.object_type IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW')
  JOIN all_users u ON u.username = c.owner
WHERE (u.oracle_maintained = 'N' OR u.username = USER) AND c.hidden_column = 'NO' AND c.table_name NOT LIKE 'BIN$%' AND INSTR(LOWER(c.column_name), :7) > 0
) ORDER BY 1, 3, 4 FETCH FIRST :8 ROWS ONLY`

// oracleSearch is Search on Oracle, whose binds are positions.
func oracleSearch(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	defs := 0
	if definitions {
		defs = 1
	}
	return scanHits(ctx, d, text, oracleSearchSQL, []any{defs, text, text, defs, text, text, text, SearchLimit + 1})
}

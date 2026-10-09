package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// execExtended runs a statement without arguments through the extended
// protocol, whose one Parse message the server refuses for text holding
// more than one statement. pgx sends such a statement through the simple
// protocol, which would run every statement of the text. Errors map to
// driver.ErrBadConn as stdlib.Conn.ExecContext maps them.
func execExtended(ctx context.Context, conn *sql.Conn, query string) (int64, error) {
	var tag pgconn.CommandTag
	err := conn.Raw(func(dc any) error {
		c, ok := dc.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("unexpected PostgreSQL driver connection %T", dc)
		}
		pc := c.Conn().PgConn()
		if pc.IsClosed() {
			return driver.ErrBadConn
		}
		var err error
		tag, err = pc.ExecParams(ctx, query, nil, nil, nil, nil).Close()
		if err != nil && pgconn.SafeToRetry(err) {
			return driver.ErrBadConn
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CopyFrom runs a PostgreSQL COPY … FROM STDIN, sending data, the rows
// as the COPY's format writes them, as a dump's lines do; it reports how
// many rows it wrote.
func (s *Session) CopyFrom(ctx context.Context, query string, data io.Reader) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return 0, err
	}
	if s.db.Config.Engine != Postgres || s.conn == nil {
		return 0, errors.New("COPY FROM STDIN is PostgreSQL's")
	}
	wasTx := s.txState()
	var tag pgconn.CommandTag
	err := s.conn.Raw(func(dc any) error {
		c, ok := dc.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("unexpected PostgreSQL driver connection %T", dc)
		}
		var err error
		tag, err = c.Conn().PgConn().CopyFrom(ctx, data, query)
		return err
	})
	if err != nil {
		return 0, s.afterError(err, wasTx)
	}
	return tag.RowsAffected(), nil
}

type postgresDialect struct{}

func (postgresDialect) Engine() Engine           { return Postgres }
func (postgresDialect) Quote(s string) string    { return quoteDouble(s) }
func (postgresDialect) Placeholder(n int) string { return fmt.Sprintf("$%d", n) }
func (postgresDialect) Editable() (bool, string) { return true, "" }

func (postgresDialect) Databases(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT datname FROM pg_database WHERE NOT datistemplate AND datallowconn ORDER BY datname`)
}

func (postgresDialect) CurrentSchema(ctx context.Context, q Querier) (string, error) {
	return queryString(ctx, q, `SELECT COALESCE(current_schema(), 'public')`)
}

func (postgresDialect) Schemas(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT nspname FROM pg_namespace
WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'
ORDER BY nspname = 'public' DESC, nspname`)
}

func (postgresDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return postgresObjects(ctx, q, schema, true)
}

func (postgresDialect) ObjectNames(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return postgresObjects(ctx, q, schema, false)
}

// postgresObjects reads a schema's tables and views; their sizes and
// comments only when asked, which read every table's files and a
// catalog more.
func postgresObjects(ctx context.Context, q Querier, schema string, sized bool) ([]Object, error) {
	size, comment := "-1", "''"
	if sized {
		size = "CASE WHEN c.relkind IN ('r','m','p') THEN pg_total_relation_size(c.oid) ELSE -1 END"
		comment = "COALESCE(obj_description(c.oid, 'pg_class'), '')"
	}
	var out []Object
	err := scanRows(ctx, q, `SELECT c.relname, c.relkind::text,
  GREATEST(c.reltuples, -1)::bigint,
  `+size+`,
  `+comment+`,
  CASE WHEN c.relkind = 'p' THEN pg_get_partkeydef(c.oid) ELSE '' END
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p','v','m','f') AND NOT c.relispartition
ORDER BY c.relname`, []any{schema}, func(scan func(...any) error) error {
		o := Object{Schema: schema}
		var kind string
		if err := scan(&o.Name, &kind, &o.Rows, &o.Bytes, &o.Comment, &o.Partitioning); err != nil {
			return err
		}
		switch kind {
		case "v":
			o.Kind = KindView
		case "m":
			o.Kind = KindMaterializedView
		case "f":
			o.Kind = KindForeignTable
		default:
			o.Kind = KindTable
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

func (postgresDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	cols, err := postgresColumns(ctx, q, schema, table)
	return cols[table], err
}

func (postgresDialect) SchemaColumns(ctx context.Context, q Querier, schema string) (map[string][]Column, error) {
	return postgresColumns(ctx, q, schema, "")
}

// postgresColumns reads the columns of a table of a schema, or of every
// table and view of it for "", by their table.
func postgresColumns(ctx context.Context, q Querier, schema, table string) (map[string][]Column, error) {
	which, args := "c.relkind IN ('r','p','v','m','f') AND NOT c.relispartition", []any{schema}
	if table != "" {
		which, args = "c.relname = $2", append(args, table)
	}
	out := map[string][]Column{}
	err := scanRows(ctx, q, `SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
  COALESCE(pg_get_expr(d.adbin, d.adrelid), ''), d.adbin IS NOT NULL,
  EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = c.oid AND i.indisprimary AND a.attnum = ANY(i.indkey)),
  a.attidentity <> '' OR COALESCE(pg_get_expr(d.adbin, d.adrelid), '') LIKE 'nextval(%',
  COALESCE(col_description(c.oid, a.attnum), '')
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE n.nspname = $1 AND `+which+` AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY c.relname, a.attnum`, args, func(scan func(...any) error) error {
		var t string
		var c Column
		if err := scan(&t, &c.Name, &c.Type, &c.Nullable, &c.Default, &c.HasDefault, &c.PrimaryKey, &c.AutoIncrement, &c.Comment); err != nil {
			return err
		}
		out[t] = append(out[t], c)
		return nil
	})
	return out, err
}

func (postgresDialect) Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error) {
	var out []Index
	err := scanRows(ctx, q, `SELECT i.relname, ix.indisunique, ix.indisprimary, pg_get_indexdef(ix.indexrelid),
  array_to_string(ARRAY(SELECT pg_get_indexdef(ix.indexrelid, k + 1, true)
    FROM generate_subscripts(ix.indkey, 1) k ORDER BY k), chr(31))
FROM pg_index ix
JOIN pg_class i ON i.oid = ix.indexrelid
JOIN pg_class t ON t.oid = ix.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = $1 AND t.relname = $2
ORDER BY ix.indisprimary DESC, i.relname`, []any{schema, table}, func(scan func(...any) error) error {
		var ix Index
		var cols string
		if err := scan(&ix.Name, &ix.Unique, &ix.Primary, &ix.Definition, &cols); err != nil {
			return err
		}
		ix.Columns = splitList(cols)
		out = append(out, ix)
		return nil
	})
	return out, err
}

// postgresAction is a foreign key's action by pg_constraint's letter for
// it: NO ACTION, the default, is "".
var postgresAction = map[string]string{"r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}

func (postgresDialect) ForeignKeys(ctx context.Context, q Querier, schema, table string) ([]ForeignKey, error) {
	var out []ForeignKey
	err := scanRows(ctx, q, `SELECT con.conname, pg_get_constraintdef(con.oid), nf.nspname, cf.relname, con.confdeltype, con.confupdtype,
  array_to_string(ARRAY(SELECT a.attname FROM unnest(con.conkey) WITH ORDINALITY k(n, o)
    JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.n ORDER BY k.o), chr(31)),
  array_to_string(ARRAY(SELECT a.attname FROM unnest(con.confkey) WITH ORDINALITY k(n, o)
    JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.n ORDER BY k.o), chr(31))
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_class cf ON cf.oid = con.confrelid
JOIN pg_namespace nf ON nf.oid = cf.relnamespace
WHERE con.contype = 'f' AND n.nspname = $1 AND c.relname = $2
ORDER BY con.conname`, []any{schema, table}, func(scan func(...any) error) error {
		var fk ForeignKey
		var cols, refCols, onDelete, onUpdate string
		if err := scan(&fk.Name, &fk.Definition, &fk.RefSchema, &fk.RefTable, &onDelete, &onUpdate, &cols, &refCols); err != nil {
			return err
		}
		fk.Columns, fk.RefColumns = splitList(cols), splitList(refCols)
		fk.OnDelete, fk.OnUpdate = postgresAction[onDelete], postgresAction[onUpdate]
		out = append(out, fk)
		return nil
	})
	return out, err
}

func (d postgresDialect) DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error) {
	name := QualifiedName(d, schema, obj.Name)
	switch obj.Kind {
	case KindView, KindMaterializedView:
		def, err := queryString(ctx, q, `SELECT pg_get_viewdef($1::regclass, true)`, name)
		if err != nil {
			return "", err
		}
		kw := "VIEW"
		if obj.Kind == KindMaterializedView {
			kw = "MATERIALIZED VIEW"
		}
		return fmt.Sprintf("CREATE %s %s AS\n%s", kw, name, strings.TrimRight(def, "; \n")) + ";\n", nil
	}
	cols, err := d.Columns(ctx, q, schema, obj.Name)
	if err != nil {
		return "", err
	}
	lines := columnsDDL(d, cols)
	constraintIndexes := map[string]bool{}
	err = scanRows(ctx, q, `SELECT conname, pg_get_constraintdef(oid) FROM pg_constraint
WHERE conrelid = $1::regclass ORDER BY contype = 'p' DESC, contype, conname`, []any{name}, func(scan func(...any) error) error {
		var cname, def string
		if err := scan(&cname, &def); err != nil {
			return err
		}
		constraintIndexes[cname] = true
		lines = append(lines, "  CONSTRAINT "+d.Quote(cname)+" "+def)
		return nil
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n%s\n);\n", name, strings.Join(lines, ",\n"))
	ixs, err := d.Indexes(ctx, q, schema, obj.Name)
	if err != nil {
		return "", err
	}
	for _, ix := range ixs {
		if !constraintIndexes[ix.Name] {
			b.WriteString("\n" + ix.Definition + ";")
		}
	}
	if obj.Comment != "" {
		fmt.Fprintf(&b, "\n\nCOMMENT ON TABLE %s IS %s;", name, quoteString(obj.Comment))
	}
	for _, c := range cols {
		if c.Comment != "" {
			fmt.Fprintf(&b, "\nCOMMENT ON COLUMN %s.%s IS %s;", name, d.Quote(c.Name), quoteString(c.Comment))
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

func quoteString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

var _ = sql.ErrNoRows

func (postgresDialect) ReferencedBy(ctx context.Context, q Querier, schema, table string) ([]Reference, error) {
	var out []Reference
	err := scanRows(ctx, q, `SELECT con.conname, n.nspname, c.relname,
  array_to_string(ARRAY(SELECT a.attname FROM unnest(con.conkey) WITH ORDINALITY k(n, o)
    JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.n ORDER BY k.o), chr(31)),
  array_to_string(ARRAY(SELECT a.attname FROM unnest(con.confkey) WITH ORDINALITY k(n, o)
    JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.n ORDER BY k.o), chr(31))
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_class cf ON cf.oid = con.confrelid
JOIN pg_namespace nf ON nf.oid = cf.relnamespace
WHERE con.contype = 'f' AND nf.nspname = $1 AND cf.relname = $2
ORDER BY n.nspname, c.relname, con.conname`, []any{schema, table}, func(scan func(...any) error) error {
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

// Items lists routines (but those of extensions), triggers, sequences
// (but identity columns'), types, extensions and partitions.
func (postgresDialect) Items(ctx context.Context, q Querier, schema string) ([]Item, error) {
	return scanItems(ctx, q, schema, `SELECT CASE p.prokind WHEN 'p' THEN 'procedure' ELSE 'function' END, p.proname, '',
  pg_get_function_identity_arguments(p.oid), p.oid::text
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
UNION ALL
SELECT 'trigger', t.tgname, c.relname, '', t.oid::text
FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND NOT t.tgisinternal
UNION ALL
SELECT 'sequence', c.relname, '', '', c.oid::text
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind = 'S'
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'i')
UNION ALL
SELECT 'type', t.typname, '', CASE t.typtype WHEN 'e' THEN 'enum' WHEN 'd' THEN 'domain' WHEN 'c' THEN 'composite' ELSE 'range' END, t.oid::text
FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
WHERE n.nspname = $1 AND t.typtype IN ('e', 'd', 'c', 'r')
  AND (t.typtype <> 'c' OR (SELECT c.relkind FROM pg_class c WHERE c.oid = t.typrelid) = 'c')
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_type'::regclass AND d.objid = t.oid AND d.deptype = 'e')
UNION ALL
SELECT 'extension', e.extname, '', e.extversion, e.oid::text
FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE n.nspname = $1
UNION ALL
SELECT 'partition', c.relname, parent.relname, pg_get_expr(c.relpartbound, c.oid), c.oid::text
FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid JOIN pg_class parent ON parent.oid = i.inhparent
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relispartition
ORDER BY 1, 2, 4`, schema)
}

// ItemDDL writes an item's definition as PostgreSQL writes it, or, where
// it writes none, from its catalog.
func (postgresDialect) ItemDDL(ctx context.Context, q Querier, it Item) (string, error) {
	var query string
	switch it.Kind {
	case ItemFunction, ItemProcedure:
		query = `SELECT CASE WHEN p.prokind IN ('a', 'w') THEN '-- An aggregate: ' || p.oid::regprocedure::text ELSE pg_get_functiondef(p.oid) END
FROM pg_proc p WHERE p.oid = $1::oid`
	case ItemTrigger:
		query = `SELECT pg_get_triggerdef($1::oid, true) || ';'`
	case ItemSequence:
		query = `SELECT format('CREATE SEQUENCE %I.%I AS %s INCREMENT BY %s MINVALUE %s MAXVALUE %s START WITH %s CACHE %s%s;',
  schemaname, sequencename, data_type, increment_by, min_value, max_value, start_value, cache_size, CASE WHEN cycle THEN ' CYCLE' ELSE '' END)
FROM pg_sequences s JOIN pg_class c ON c.relname = s.sequencename JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = s.schemaname
WHERE c.oid = $1::oid`
	case ItemType:
		query = `SELECT format('CREATE TYPE %I.%I AS ', n.nspname, t.typname) || CASE t.typtype
  WHEN 'e' THEN 'ENUM (' || (SELECT string_agg(quote_literal(e.enumlabel), ', ' ORDER BY e.enumsortorder) FROM pg_enum e WHERE e.enumtypid = t.oid) || ');'
  WHEN 'c' THEN '(' || (SELECT string_agg(quote_ident(a.attname) || ' ' || format_type(a.atttypid, a.atttypmod), ', ' ORDER BY a.attnum)
    FROM pg_attribute a WHERE a.attrelid = t.typrelid AND a.attnum > 0 AND NOT a.attisdropped) || ');'
  WHEN 'r' THEN 'RANGE (SUBTYPE = ' || (SELECT format_type(r.rngsubtype, NULL) FROM pg_range r WHERE r.rngtypid = t.oid) || ');'
  END
FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace WHERE t.oid = $1::oid AND t.typtype <> 'd'
UNION ALL
SELECT format('CREATE DOMAIN %I.%I AS %s', n.nspname, t.typname, format_type(t.typbasetype, t.typtypmod))
  || CASE WHEN t.typnotnull THEN ' NOT NULL' ELSE '' END || coalesce(' DEFAULT ' || t.typdefault, '')
  -- Since PostgreSQL 17 NOT NULL is a constraint too, which the line above writes.
  || coalesce((SELECT string_agg(' CONSTRAINT ' || quote_ident(c.conname) || ' ' || pg_get_constraintdef(c.oid), '' ORDER BY c.conname)
    FROM pg_constraint c WHERE c.contypid = t.oid AND c.contype <> 'n'), '') || ';'
FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace WHERE t.oid = $1::oid AND t.typtype = 'd'`
	case ItemExtension:
		query = `SELECT format('CREATE EXTENSION %I WITH SCHEMA %I VERSION %L;', e.extname, n.nspname, e.extversion)
FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.oid = $1::oid`
	case ItemPartition:
		query = `SELECT format('CREATE TABLE %I.%I PARTITION OF %I.%I %s;', n.nspname, c.relname, pn.nspname, parent.relname, pg_get_expr(c.relpartbound, c.oid))
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace JOIN pg_inherits i ON i.inhrelid = c.oid
JOIN pg_class parent ON parent.oid = i.inhparent JOIN pg_namespace pn ON pn.oid = parent.relnamespace WHERE c.oid = $1::oid`
	default:
		return "", errNoDefinition(it)
	}
	return queryString(ctx, q, query, it.ID)
}

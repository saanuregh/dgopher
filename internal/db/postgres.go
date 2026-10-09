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
	var out []Object
	err := scanRows(ctx, q, `SELECT c.relname, c.relkind::text,
  GREATEST(c.reltuples, -1)::bigint,
  CASE WHEN c.relkind IN ('r','m','p') THEN pg_total_relation_size(c.oid) ELSE -1 END,
  COALESCE(obj_description(c.oid, 'pg_class'), '')
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p','v','m','f') AND NOT c.relispartition
ORDER BY c.relname`, []any{schema}, func(scan func(...any) error) error {
		o := Object{Schema: schema}
		var kind string
		if err := scan(&o.Name, &kind, &o.Rows, &o.Bytes, &o.Comment); err != nil {
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
	var out []Column
	err := scanRows(ctx, q, `SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
  COALESCE(pg_get_expr(d.adbin, d.adrelid), ''), d.adbin IS NOT NULL,
  EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = c.oid AND i.indisprimary AND a.attnum = ANY(i.indkey)),
  a.attidentity <> '' OR COALESCE(pg_get_expr(d.adbin, d.adrelid), '') LIKE 'nextval(%',
  COALESCE(col_description(c.oid, a.attnum), '')
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`, []any{schema, table}, func(scan func(...any) error) error {
		var c Column
		if err := scan(&c.Name, &c.Type, &c.Nullable, &c.Default, &c.HasDefault, &c.PrimaryKey, &c.AutoIncrement, &c.Comment); err != nil {
			return err
		}
		out = append(out, c)
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

func (postgresDialect) ForeignKeys(ctx context.Context, q Querier, schema, table string) ([]ForeignKey, error) {
	var out []ForeignKey
	err := scanRows(ctx, q, `SELECT con.conname, pg_get_constraintdef(con.oid), nf.nspname, cf.relname,
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
		var cols, refCols string
		if err := scan(&fk.Name, &fk.Definition, &fk.RefSchema, &fk.RefTable, &cols, &refCols); err != nil {
			return err
		}
		fk.Columns, fk.RefColumns = splitList(cols), splitList(refCols)
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

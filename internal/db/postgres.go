package db

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/sqltext"

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
		c, ok := driverConn(dc).(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("unexpected PostgreSQL driver connection %T", dc)
		}
		pc := c.Conn().PgConn()
		if pc.IsClosed() {
			return driver.ErrBadConn
		}
		var err error
		start := time.Now()
		tag, err = pc.ExecParams(ctx, query, nil, nil, nil, nil).Close()
		recordOn(dc, query, start, err)
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
	if r, ok := SQL(s.db.Config.Engine); !ok || !r.CopyFromStdin() || s.conn == nil {
		return 0, errors.New("COPY FROM STDIN is PostgreSQL's")
	}
	wasTx := s.txState()
	var tag pgconn.CommandTag
	err := s.conn.Raw(func(dc any) error {
		c, ok := driverConn(dc).(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("unexpected PostgreSQL driver connection %T", dc)
		}
		var err error
		start := time.Now()
		tag, err = c.Conn().PgConn().CopyFrom(ctx, data, query)
		recordOn(dc, query, start, err)
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
	return postgresObjects(ctx, q, schema, postgresTableSize)
}

func (postgresDialect) ObjectNames(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return postgresObjects(ctx, q, schema, "")
}

// postgresTableSize is the size of table c: its files, its indexes' and
// TOAST's included.
const postgresTableSize = "pg_total_relation_size(c.oid)"

// postgresObjects reads a schema's tables and views; their sizes, by
// tableSize, and comments only when tableSize is not "", as they read
// every table's files and a catalog more.
func postgresObjects(ctx context.Context, q Querier, schema, tableSize string) ([]Object, error) {
	size, comment := "-1", "''"
	if tableSize != "" {
		size = "CASE WHEN c.relkind IN ('r','m','p') THEN " + tableSize + " ELSE -1 END"
		comment = "COALESCE(obj_description(c.oid, 'pg_class'), '')"
	}
	return scanAll(ctx, q, `SELECT c.relname, c.relkind::text,
  GREATEST(c.reltuples, -1)::bigint,
  `+size+`,
  `+comment+`,
  CASE WHEN c.relkind = 'p' THEN pg_get_partkeydef(c.oid) ELSE '' END
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p','v','m','f') AND NOT c.relispartition
ORDER BY c.relname`, []any{schema}, func(scan func(...any) error) (Object, error) {
		o := Object{Schema: schema}
		var kind string
		if err := scan(&o.Name, &kind, &o.Rows, &o.Bytes, &o.Comment, &o.Partitioning); err != nil {
			return o, err
		}
		o.Kind = postgresObjectKind(kind)
		return o, nil
	})
}

// postgresObjectKind is the kind of a pg_class relkind.
func postgresObjectKind(relkind string) ObjectKind {
	switch relkind {
	case "v":
		return KindView
	case "m":
		return KindMaterializedView
	case "f":
		return KindForeignTable
	}
	return KindTable
}

func (postgresDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	cols, err := postgresColumns(ctx, q, schema, table, postgresColumnsFrom)
	return cols[table], err
}

func (postgresDialect) SchemaColumns(ctx context.Context, q Querier, schema string) (map[string][]Column, error) {
	return postgresColumns(ctx, q, schema, "", postgresColumnsFrom)
}

// columnsFrom is how an engine of PostgreSQL's family finds the columns
// postgresColumns reads: the tables and views whose columns a schema
// lists, and a join leaving out columns the engine hides.
type columnsFrom struct {
	relations, hidden string
}

var postgresColumnsFrom = columnsFrom{relations: "c.relkind IN ('r','p','v','m','f') AND NOT c.relispartition"}

// postgresColumns reads the columns of a table of a schema, or of every
// table and view of it for "", by their table.
func postgresColumns(ctx context.Context, q Querier, schema, table string, from columnsFrom) (map[string][]Column, error) {
	which, args := from.relations, []any{schema}
	if table != "" {
		which, args = "c.relname = $2", append(args, table)
	}
	return scanColumns(ctx, q, `SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
  COALESCE(pg_get_expr(d.adbin, d.adrelid), ''), d.adbin IS NOT NULL,
  EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = c.oid AND i.indisprimary AND a.attnum = ANY(i.indkey)),
  a.attidentity <> '' OR COALESCE(pg_get_expr(d.adbin, d.adrelid), '') LIKE 'nextval(%',
  COALESCE(col_description(c.oid, a.attnum), '')
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
`+from.hidden+`LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE n.nspname = $1 AND `+which+` AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY c.relname, a.attnum`, args)
}

func (postgresDialect) Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error) {
	return scanAll(ctx, q, `SELECT i.relname, ix.indisunique, ix.indisprimary, pg_get_indexdef(ix.indexrelid),
  array_to_string(ARRAY(SELECT pg_get_indexdef(ix.indexrelid, k + 1, true)
    FROM generate_subscripts(ix.indkey, 1) k ORDER BY k), chr(31))
FROM pg_index ix
JOIN pg_class i ON i.oid = ix.indexrelid
JOIN pg_class t ON t.oid = ix.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = $1 AND t.relname = $2
ORDER BY ix.indisprimary DESC, i.relname`, []any{schema, table}, func(scan func(...any) error) (Index, error) {
		var ix Index
		var cols string
		if err := scan(&ix.Name, &ix.Unique, &ix.Primary, &ix.Definition, &cols); err != nil {
			return ix, err
		}
		ix.Columns = splitList(cols)
		return ix, nil
	})
}

// postgresAction is a foreign key's action by pg_constraint's letter for
// it: NO ACTION, the default, is "".
var postgresAction = map[string]string{"r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}

func (postgresDialect) ForeignKeys(ctx context.Context, q Querier, schema, table string) ([]ForeignKey, error) {
	return scanAll(ctx, q, `SELECT con.conname, pg_get_constraintdef(con.oid), nf.nspname, cf.relname, con.confdeltype, con.confupdtype,
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
ORDER BY con.conname`, []any{schema, table}, func(scan func(...any) error) (ForeignKey, error) {
		var fk ForeignKey
		var cols, refCols, onDelete, onUpdate string
		if err := scan(&fk.Name, &fk.Definition, &fk.RefSchema, &fk.RefTable, &onDelete, &onUpdate, &cols, &refCols); err != nil {
			return fk, err
		}
		fk.Columns, fk.RefColumns = splitList(cols), splitList(refCols)
		fk.OnDelete, fk.OnUpdate = postgresAction[onDelete], postgresAction[onUpdate]
		return fk, nil
	})
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

func quoteString(s string) string { return Literal(Postgres, s) }

func (postgresDialect) ReferencedBy(ctx context.Context, q Querier, schema, table string) ([]Reference, error) {
	return scanAll(ctx, q, `SELECT con.conname, n.nspname, c.relname,
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
ORDER BY n.nspname, c.relname, con.conname`, []any{schema, table}, func(scan func(...any) error) (Reference, error) {
		var r Reference
		var cols, refCols string
		if err := scan(&r.Name, &r.Schema, &r.Table, &cols, &refCols); err != nil {
			return r, err
		}
		r.Columns, r.RefColumns = splitList(cols), splitList(refCols)
		return r, nil
	})
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

const (
	postgresStopQuery  = "SELECT pg_cancel_backend(%s)"
	postgresEndSession = "SELECT pg_terminate_backend(%s)"
)

// postgresFileReaders are PostgreSQL's functions that read the server's
// files or fetch URLs; callers only read them.
var postgresFileReaders = strings.Fields("pg_read_file pg_read_binary_file pg_stat_file pg_ls_dir pg_ls_logdir pg_ls_waldir pg_ls_tmpdir " +
	"pg_ls_archive_statusdir pg_ls_logicalmapdir pg_ls_logicalsnapdir pg_ls_replslotdir lo_get loread dblink dblink_open dblink_fetch")

type postgresEngine struct{ postgresDialect }

var _ SQLRules = postgresEngine{}

func (postgresEngine) Label() string                   { return "PostgreSQL" }
func (postgresEngine) Base() Engine                    { return Postgres }
func (postgresEngine) ColorHex() string                { return "#336791" }
func (postgresEngine) DefaultPort() int                { return 5432 }
func (postgresEngine) DefaultUser() string             { return "postgres" }
func (postgresEngine) Kind() EngineKind                { return KindSQL }
func (postgresEngine) Transactions() bool              { return true }
func (postgresEngine) Savepoints() bool                { return true }
func (postgresEngine) TransactionalDDL() bool          { return true }
func (postgresEngine) DatabaseField() (string, string) { return "Database", "postgres" }
func (postgresEngine) URLSchemes() []string            { return []string{"postgres", "postgresql"} }

func (postgresEngine) URLTLSParameters() map[string]map[string]TLSMode {
	return postgresURLTLS
}

func (postgresEngine) CloudIdentity() bool              { return true }
func (postgresEngine) ClearTextPassword() bool          { return false }
func (postgresEngine) ServerEnforcesReadOnly() bool     { return true }
func (postgresEngine) DatabasesNeedOwnConnection() bool { return true }
func (postgresEngine) DefaultSchemaName() string        { return "public" }
func (postgresEngine) FileExtensions() []string         { return nil }

// postgresSessionsQuery lists the client sessions but the app's own.
const postgresSessionsQuery = `SELECT pid, usename AS "user", datname AS database, application_name AS application, client_addr::text AS client,
  state, date_trunc('second', now() - query_start)::text AS running_for, wait_event_type AS waiting_on, left(query, 400) AS query
FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND backend_type = 'client backend'
ORDER BY state = 'active' DESC, query_start NULLS LAST`

func (postgresEngine) ActivityPages() []ActivityPage {
	return []ActivityPage{{
		Name: "Sessions", Live: true, Stop: sessionStatement(postgresStopQuery), Kill: sessionStatement(postgresEndSession),
		Query: postgresSessionsQuery,
	}, {
		Name: "Locks", Live: true, Stop: sessionStatement(postgresStopQuery), Kill: sessionStatement(postgresEndSession),
		Query: `SELECT l.pid, a.usename AS "user", l.locktype AS lock_type,
  coalesce(l.relation::regclass::text, l.transactionid::text, l.virtualxid) AS object, l.mode, l.granted,
  array_to_string(pg_blocking_pids(l.pid), ', ') AS waits_for, date_trunc('second', now() - a.xact_start)::text AS transaction_for,
  left(a.query, 300) AS query
FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
WHERE l.pid <> pg_backend_pid() AND a.backend_type = 'client backend' AND l.locktype <> 'virtualxid'
ORDER BY l.granted, a.xact_start`,
	}, {
		Name: "Statistics",
		Query: `SELECT left(query, 400) AS query, calls, round(total_exec_time::numeric, 1) AS total_ms, round(mean_exec_time::numeric, 2) AS mean_ms,
  round(max_exec_time::numeric, 1) AS max_ms, rows, shared_blks_hit + shared_blks_read AS blocks
FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 500`,
		Hint: "These come from the pg_stat_statements extension: add it to shared_preload_libraries, restart the server, and run CREATE EXTENSION pg_stat_statements.",
	}}
}

func (postgresEngine) dialect() Dialect { return postgresDialect{} }

func (postgresEngine) open(_ context.Context, cfg Config, ep endpoint, tc *tls.Config, w wire) (openedPool, error) {
	sqldb, err := openPostgres(cfg, ep, tc, w)
	return openedPool{sql: sqldb, tls: tc}, err
}

func (postgresEngine) tlsState(ctx context.Context, sqldb *sql.DB, _ Config, _ *tls.Config) string {
	return negotiatedPostgresTLS(ctx, sqldb)
}

func (postgresEngine) pingStatement() string { return "-- ping" }

func (postgresEngine) beginStatements(opts driver.TxOptions) []string {
	text := "begin"
	switch sql.IsolationLevel(opts.Isolation) {
	case sql.LevelReadUncommitted:
		text += " isolation level read uncommitted"
	case sql.LevelReadCommitted:
		text += " isolation level read committed"
	case sql.LevelRepeatableRead, sql.LevelSnapshot:
		text += " isolation level repeatable read"
	case sql.LevelSerializable:
		text += " isolation level serializable"
	}
	if opts.ReadOnly {
		text += " read only"
	}
	return []string{text}
}

func (postgresEngine) endStatement(commit bool) string          { return lowerEndStatement(commit) }
func (postgresEngine) pingsOnReset() bool                       { return true }
func (postgresEngine) execUnprepared() bool                     { return true }
func (postgresEngine) bytesAreBinary() bool                     { return false }
func (postgresEngine) cancelQuerySQL(int64) string              { return "" }
func (postgresEngine) pingAfterError() bool                     { return false }
func (postgresEngine) takesServerParameters() bool              { return false }
func (postgresEngine) CopyFromStdin() bool                      { return true }
func (postgresEngine) CreateFile(context.Context, string) error { return noFileError(Postgres) }
func (postgresEngine) VersionQuery() string                     { return "SHOW server_version" }
func (postgresEngine) LexDialect() sqltext.Dialect              { return sqltext.Postgres }
func (postgresEngine) BeginStatement() string                   { return "BEGIN" }
func (postgresEngine) ReadOnlyBeginSQL() string                 { return "START TRANSACTION READ ONLY" }
func (postgresEngine) followsTransactionByVerbs() bool          { return false }

func (postgresEngine) identify(ctx context.Context, q *sql.DB) (Engine, string, error) {
	return identifyPostgresFamily(ctx, q)
}

func (postgresEngine) afterFailedStatement(v txByVerbs, _ error, _ bool, _ func() bool) txByVerbs {
	return v
}

func (postgresEngine) autocommitMayBeOff() bool                              { return false }
func (postgresEngine) ExecutableComments() bool                              { return false }
func (postgresEngine) commitsImplicitlyWords([]string, func() []string) bool { return false }

func (postgresEngine) setsAutocommitWords([]string, func() []string) (on, ok bool) {
	return false, false
}

func (postgresEngine) beginIsCompound([]string) bool    { return false }
func (postgresEngine) SavepointBeginsTransaction() bool { return false }

// PostgreSQL's VACUUM, CREATE and DROP of a DATABASE or a TABLESPACE,
// ALTER SYSTEM and the CONCURRENTLY forms of index commands.
func (postgresEngine) OutsideTransaction(sql string) bool {
	words := nameWords(sql, sqltext.Postgres)
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "VACUUM":
		return true
	case "ALTER":
		return len(words) > 1 && words[1] == "SYSTEM"
	case "REINDEX":
		return slices.Contains(words, "CONCURRENTLY")
	case "CREATE", "DROP":
		if len(words) > 1 && (words[1] == "DATABASE" || words[1] == "TABLESPACE") {
			return true
		}
		// CREATE [UNIQUE] INDEX CONCURRENTLY, DROP INDEX CONCURRENTLY:
		// the word only there, never a table of that name.
		i := slices.Index(words[:min(3, len(words))], "INDEX")
		return i > 0 && i+1 < len(words) && words[i+1] == "CONCURRENTLY"
	}
	return false
}

func (postgresEngine) RollbackMayKeepChanges() bool { return false }
func (postgresEngine) RowCountWord() string         { return "changed" }

func (postgresEngine) Explain(statement string, analyze bool) (ExplainRun, bool) {
	if analyze {
		return explainWithPrefix("EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON) ", statement), true
	}
	return explainWithPrefix("EXPLAIN (VERBOSE, FORMAT JSON) ", statement), true
}

func (postgresEngine) ParsePlan(_ []string, rows [][]any) (*Plan, bool) {
	return parsePostgresPlan(planText(rows))
}

func (postgresEngine) AdvisePlanStep(n *PlanNode, add func(n *PlanNode, format string, args ...any)) {
	switch {
	case strings.HasPrefix(n.Op, "Seq Scan"):
		// Only a plan that ran tells how few rows the filter keeps.
		removed, _ := strconv.ParseFloat(n.Prop("Rows Removed by Filter"), 64)
		if filter := n.Prop("Filter"); filter != "" && removed >= adviseRows && removed > 10*rowsOf(n) {
			add(n, "Seq Scan reads every row of %s to keep the few of %s: an index on (%s) may let it read only those.", n.Target, filter, columnsIn(filter))
		}
	case n.Op == "Sort" && n.Prop("Sort Space Type") == "Disk":
		add(n, "The sort spills to disk (%s kB): more work_mem, or an index in the sort's order, avoids it.", n.Prop("Sort Space Used"))
	case n.Op == "Hash":
		if batches, _ := strconv.Atoi(n.Prop("Hash Batches")); batches > 1 {
			add(n, "The hash is built in %d batches on disk: more work_mem would keep it in memory.", batches)
		}
	case strings.HasPrefix(n.Op, "Nested Loop") && len(n.Children) == 2:
		inner := n.Children[1]
		if strings.HasPrefix(inner.Op, "Seq Scan") && inner.Loops >= adviseRows {
			add(inner, "The nested loop reads all of %s %s times: an index on its join columns may let each loop find its rows.", inner.Target, count(inner.Loops))
		}
	}
}

func (postgresEngine) UseSchemaSQL(schema string) string {
	return "SET search_path TO " + postgresDialect{}.Quote(schema)
}

func (postgresEngine) EditorSchemaPreamble(schema string) string {
	return "SET search_path TO " + postgresDialect{}.Quote(schema) + ";\n\n"
}

func (postgresEngine) NameFolding() NameFolding { return NamesFoldToLower }

func (postgresEngine) Search(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	text = strings.ToLower(text)
	return searchWith(ctx, d, text, postgresSearch, []any{text, definitions})
}

func (postgresEngine) EnumValues(ctx context.Context, d *DB, typ string) ([]string, error) {
	return queryStrings(ctx, d.Catalog(), `SELECT enumlabel FROM pg_enum WHERE enumtypid = to_regtype($1) ORDER BY enumsortorder`, strings.TrimSpace(typ))
}

func (postgresEngine) ParameterAsText() bool { return true }

func (postgresEngine) TextLiteral(s string) string {
	return textLiteral(s, "chr(0)", escapeStringLiteral)
}

func (postgresEngine) BytesLiteral(b []byte) string {
	return `E'\\x` + hex.EncodeToString(b) + `'::bytea`
}

func (postgresEngine) BooleansAreNumbers() bool { return false }
func (postgresEngine) OffsetTimesAsUTC() bool   { return false }

func (postgresEngine) CastParameter(placeholder, columnType string) string {
	return placeholder + "::text::" + columnType
}

func (postgresEngine) InsertDefaultRowSQL(table string) string {
	return "INSERT INTO " + table + " DEFAULT VALUES"
}

func (postgresEngine) MaxParameters() int       { return 65535 }
func (postgresEngine) ProfilesInOneQuery() bool { return false }

func (postgresEngine) Comparable(columnType string) bool {
	return !slices.Contains([]string{"json", "xml", "point", "line", "lseg", "box", "path", "polygon", "circle"}, strings.ToLower(columnType))
}

func (postgresEngine) StableOrderByKey() bool           { return true }
func (postgresEngine) UpdateSetsDefault() bool          { return true }
func (postgresEngine) InterruptAbortsTransaction() bool { return false }
func (postgresEngine) FileReadingFunctions() []string   { return postgresFileReaders }
func (postgresEngine) FileNamesInStrings() bool         { return false }

func (postgresEngine) LikeFilterSQL(column, pattern string, not bool) string {
	return "CAST(" + column + " AS TEXT)" + likeOperator(true, not) + pattern
}

func (postgresEngine) JSONPathSQL(column string, path []any, leaf bool) string {
	var b strings.Builder
	b.WriteString(column)
	for i, p := range path {
		op := "->"
		if leaf && i == len(path)-1 {
			op = "->>"
		}
		b.WriteString(op)
		switch x := p.(type) {
		case int:
			b.WriteString(strconv.Itoa(x))
		case string:
			b.WriteString(standardLiteral(x))
		}
	}
	return b.String()
}

func (postgresEngine) UpsertSQL(insert string, keys, updatedColumns []string) string {
	return upsertOnConflictSQL(insert, keys, updatedColumns)
}

func (postgresEngine) CommonTypes() []string {
	return []string{"integer", "bigint", "smallint", "numeric(12,2)", "real", "double precision", "boolean", "text", "varchar(255)",
		"date", "timestamp", "timestamptz", "time", "interval", "uuid", "jsonb", "bytea"}
}

func (postgresEngine) FirstColumn() ColumnDesign {
	return ColumnDesign{Name: "id", PrimaryKey: true, Type: "bigint", AutoIncrement: true}
}

func (postgresEngine) ColumnTypeFor(duckType string) string { return columnTypesFor(duckType).postgres }

func (postgresEngine) CanonicalType(typ string) string {
	return commonCanonical(strings.TrimSpace(typ))
}

func (postgresEngine) ModifyColumnSQL(d Dialect, t TableDesign, was, now ColumnDesign) ([]string, error) {
	return alterColumnSQL(d, t, was, now, castNeeded(was.Type, now.Type))
}

func (postgresEngine) RenameObjectSQL(obj Object, name string) (string, error) {
	return alterRenameSQL(postgresDialect{}, obj, name), nil
}

func (postgresEngine) ScriptPreamble(functions bool) string {
	if functions {
		// As pg_dump does: a function's body may name a table created
		// after it.
		return "SET check_function_bodies = false;\n\n"
	}
	return ""
}

func (postgresEngine) TypeKindOf(typ string) (TypeKind, string, string) {
	return commonTypeKind(typ, false, false)
}

func (postgresEngine) TypeOfKind(kind TypeKind, size, scale string) string {
	return typesOfKind(kind, size, scale).postgres
}

func (postgresEngine) NullableWrapper() bool                                   { return false }
func (postgresEngine) CreateTableSuffix(Dialect, TableDesign, []string) string { return "" }
func (postgresEngine) InlineIntegerKey() bool                                  { return false }
func (postgresEngine) PrimaryKeyClause() bool                                  { return true }
func (postgresEngine) RebuildsToAlter() bool                                   { return false }

func (postgresEngine) DropConstraintSQL(d Dialect, kind ConstraintKind, schema, table, name string) string {
	return dropConstraintSQL(d, kind, schema, table, name)
}

func (postgresEngine) UniqueAsConstraint() bool       { return true }
func (postgresEngine) ReplacesPrimaryKeyAtOnce() bool { return false }

func (postgresEngine) TableCommentSQL(d Dialect, schema, table, comment string) string {
	return commentOnTableSQL(d, schema, table, comment)
}

func (postgresEngine) ColumnCommentInline() bool           { return false }
func (postgresEngine) HasComments() bool                   { return true }
func (postgresEngine) KeepsColumnExtra() bool              { return false }
func (postgresEngine) AutoIncrementSQL() string            { return "GENERATED BY DEFAULT AS IDENTITY" }
func (postgresEngine) HasAutoNumbering() bool              { return true }
func (postgresEngine) HasForeignKeys() bool                { return true }
func (postgresEngine) HasForeignKeyActions() bool          { return true }
func (postgresEngine) ForeignKeyNamesUniqueInSchema() bool { return false }
func (postgresEngine) HasSecondaryIndexes() bool           { return true }
func (postgresEngine) KeepsKeyNames() bool                 { return true }
func (postgresEngine) NamesConstraintsItself() bool        { return false }
func (postgresEngine) NamesTablesWithinSchema() bool       { return false }
func (postgresEngine) IgnoresVarcharLength() bool          { return false }
func (postgresEngine) KeysOfMadeTable() string             { return "" }
func (postgresEngine) FixedKeysNote() string               { return "" }

func (postgresEngine) ReadDesignExtras(ctx context.Context, d *DB, t *TableDesign) error {
	return readPostgresDesign(ctx, d, t)
}

func (postgresEngine) NormalizeModelTable(*TableDesign) {}
func (postgresEngine) TruncateToEmpty() bool            { return false }
func (postgresEngine) ManagesAccounts() bool            { return true }

func (postgresEngine) ListAccounts(ctx context.Context, q Querier) ([]Account, error) {
	return listPostgresAccounts(ctx, q, "r.rolname !~ '^pg_'")
}

// listPostgresAccounts lists the roles of pg_roles that which keeps.
func listPostgresAccounts(ctx context.Context, q Querier, which string) ([]Account, error) {
	return scanAll(ctx, q, `SELECT r.rolname, r.rolcanlogin, r.rolsuper, r.rolcreatedb, r.rolcreaterole, r.rolreplication, r.rolbypassrls,
  r.rolconnlimit, coalesce(r.rolvaliduntil::text, ''),
  coalesce((SELECT string_agg(b.rolname, ',' ORDER BY b.rolname) FROM pg_auth_members m JOIN pg_roles b ON b.oid = m.roleid WHERE m.member = r.oid), '')
FROM pg_roles r WHERE `+which+` ORDER BY r.rolname`, nil, func(scan func(...any) error) (Account, error) {
		var a Account
		var login, super, createDB, createRole, replication, bypass bool
		var limit int
		var until, member string
		if err := scan(&a.Name, &login, &super, &createDB, &createRole, &replication, &bypass, &limit, &until, &member); err != nil {
			return a, err
		}
		a.Role = !login
		for _, f := range []struct {
			on   bool
			what string
		}{{login, "can log in"}, {super, "superuser"}, {createDB, "creates databases"}, {createRole, "creates roles"},
			{replication, "replication"}, {bypass, "bypasses row security"}} {
			if f.on {
				a.Attributes = append(a.Attributes, f.what)
			}
		}
		if limit >= 0 {
			a.Attributes = append(a.Attributes, fmt.Sprintf("at most %d connections", limit))
		}
		if until != "" {
			a.Attributes = append(a.Attributes, "password valid until "+until)
		}
		a.MemberOf = splitList(member)
		return a, nil
	})
}

func (postgresEngine) AccountPrivileges(ctx context.Context, q Querier, a Account) ([]Privilege, error) {
	d := postgresDialect{}
	account := d.Quote(a.Name)
	out, err := scanAll(ctx, q, `SELECT kind, name, string_agg(privilege_type, ', ' ORDER BY privilege_type) FROM (
  SELECT 'TABLE' AS kind, quote_ident(n.nspname) || '.' || quote_ident(c.relname) AS name, x.privilege_type, x.grantee
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace CROSS JOIN LATERAL aclexplode(c.relacl) x
  WHERE c.relkind IN ('r', 'v', 'm', 'p', 'f')
  UNION ALL
  SELECT 'SCHEMA', quote_ident(n.nspname), x.privilege_type, x.grantee FROM pg_namespace n CROSS JOIN LATERAL aclexplode(n.nspacl) x
  UNION ALL
  SELECT 'DATABASE', quote_ident(d.datname), x.privilege_type, x.grantee FROM pg_database d CROSS JOIN LATERAL aclexplode(d.datacl) x
) p JOIN pg_roles r ON r.oid = p.grantee WHERE r.rolname = $1
GROUP BY kind, name ORDER BY kind = 'TABLE', kind, name`, []any{a.Name}, func(scan func(...any) error) (Privilege, error) {
		var kind, name, privs string
		if err := scan(&kind, &name, &privs); err != nil {
			return Privilege{}, err
		}
		return Privilege{Text: privs + " on " + strings.ToLower(kind) + " " + name,
			Revoke: "REVOKE " + privs + " ON " + kind + " " + name + " FROM " + account}, nil
	})
	if err != nil {
		return nil, err
	}
	return append(out, roleMemberships(d, a)...), nil
}

// roleMemberships are the roles an account of PostgreSQL's family is a
// member of, as privileges revoked by REVOKE role FROM account.
func roleMemberships(d Dialect, a Account) []Privilege {
	var out []Privilege
	for _, role := range a.MemberOf {
		out = append(out, Privilege{Text: "member of " + role, Revoke: "REVOKE " + d.Quote(role) + " FROM " + d.Quote(a.Name)})
	}
	return out
}

func (postgresEngine) AccountName(a Account) string { return postgresDialect{}.Quote(a.Name) }

func (postgresEngine) CreateAccountSQL(n NewAccount) (string, error) {
	name := postgresDialect{}.Quote(n.Name)
	if n.Role {
		return createRoleSQL(name), nil
	}
	sql := "CREATE ROLE " + name + " WITH LOGIN"
	if n.CreateDB {
		sql += " CREATEDB"
	}
	if n.CreateRole {
		sql += " CREATEROLE"
	}
	if n.Password == "" {
		return sql, nil
	}
	pw, err := postgresPassword(n.Password)
	return sql + " PASSWORD " + pw, err
}

func (postgresEngine) PasswordSQL(a Account, password string) (string, error) {
	pw, err := postgresPassword(password)
	return "ALTER ROLE " + postgresDialect{}.Quote(a.Name) + " WITH PASSWORD " + pw, err
}

// A PostgreSQL role cannot be dropped while it owns objects or holds
// privileges: what it owns in the database passes to the user dropping
// it, never dropped, then its privileges are revoked. Those of other
// databases stay, and the DROP says where.
func (postgresEngine) DropAccountSQL(a Account) []string {
	name := postgresDialect{}.Quote(a.Name)
	return []string{"REASSIGN OWNED BY " + name + " TO CURRENT_USER", "DROP OWNED BY " + name, "DROP ROLE " + name}
}

// On PostgreSQL a schema's tables are reached only through the schema,
// whose USAGE is granted too.
func (postgresEngine) GrantSQL(privileges []string, schema, table string, a Account) ([]string, error) {
	if err := grantError(privileges, schema); err != nil {
		return nil, err
	}
	d := postgresDialect{}
	privs, name := strings.Join(privileges, ", "), d.Quote(a.Name)
	on := "ALL TABLES IN SCHEMA " + d.Quote(schema)
	if table != "" {
		on = "TABLE " + QualifiedName(d, schema, table)
	}
	return []string{"GRANT USAGE ON SCHEMA " + d.Quote(schema) + " TO " + name, "GRANT " + privs + " ON " + on + " TO " + name}, nil
}

func (postgresEngine) Privileges() []string { return standardPrivileges() }

func (postgresEngine) AccountForm() AccountFormFields {
	return AccountFormFields{CreateOptions: true, GrantTargetLabel: "Schema",
		PrivilegesNote: "Those on the database's own tables and schemas, and on its databases."}
}

func (postgresEngine) BackupFormats() []BackupFormat {
	return []BackupFormat{BackupArchive, BackupSQL}
}

func (postgresEngine) PlanBackup(ctx context.Context, d *DB, o BackupOptions) (ToolRun, error) {
	return planPgDump(ctx, d, o)
}

// planPgDump is how pg_dump backs up a database of PostgreSQL's family.
func planPgDump(ctx context.Context, d *DB, o BackupOptions) (ToolRun, error) {
	return planPostgresDump(ctx, d, o, "pg_dump")
}

// planPostgresDump is how tool, pg_dump or one taking its options, backs
// up a database of PostgreSQL's family.
func planPostgresDump(ctx context.Context, d *DB, o BackupOptions, tool string) (ToolRun, error) {
	if err := backupOptionsError(d.Config.Engine, o); err != nil {
		return ToolRun{}, err
	}
	run, err := postgresTool(ctx, d, tool, o.Database)
	if err != nil {
		return run, err
	}
	format := "custom"
	if o.Format == BackupSQL {
		format = "plain"
	}
	run.Argv = append(run.Argv, "--format", format, "--file", o.Path, "--verbose")
	run.Argv = append(run.Argv, contentFlag(o.Content, "--schema-only", "--data-only")...)
	return run, nil
}

// PlanRestore: YugabyteDB's differs, without --single-transaction.
func (postgresEngine) PlanRestore(ctx context.Context, d *DB, database, path string, clean bool) (ToolRun, error) {
	run, err := postgresTool(ctx, d, "pg_restore", database)
	if err != nil {
		return run, err
	}
	run.Argv = append(run.Argv, "--verbose", "--single-transaction", "--exit-on-error")
	if clean {
		run.Argv = append(run.Argv, "--clean", "--if-exists")
	}
	// After --, an archive named as an option is the archive.
	run.Argv = append(run.Argv, "--", path)
	return run, nil
}

func (postgresEngine) BackupNote(restore bool) string {
	return backupNoteFor(restore,
		"A .dump of pg_dump, or a SQL file.",
		"pg_dump writes it, reaching the server as the connection does; it must be installed, of the server's version or later.")
}

func (postgresEngine) BackupChoices() BackupChoices {
	return BackupChoices{Content: true, Clean: true}
}

func (postgresEngine) BackupScope() BackupScope        { return BackupScopeDatabase }
func (postgresEngine) BackupToolChecksCAOnly(*DB) bool { return false }

func (postgresEngine) MaintenanceCommands(quotedTable string) []MaintenanceCommand {
	return []MaintenanceCommand{
		{"Vacuum", "VACUUM " + quotedTable},
		{"Vacuum and Analyze", "VACUUM ANALYZE " + quotedTable},
		{"Analyze", "ANALYZE " + quotedTable},
		{"Reindex (locks writes)", "REINDEX TABLE " + quotedTable},
		{"Vacuum Full (locks reads and writes)", "VACUUM FULL " + quotedTable},
	}
}

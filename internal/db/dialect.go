package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Querier runs the queries that read a schema: a pool or a session's
// connection.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ObjectKind is what a schema object is.
type ObjectKind string

const (
	KindTable            ObjectKind = "table"
	KindView             ObjectKind = "view"
	KindMaterializedView ObjectKind = "materialized view"
	KindForeignTable     ObjectKind = "foreign table"
	KindDictionary       ObjectKind = "dictionary"
)

// Object is a table or a view.
type Object struct {
	Schema  string
	Name    string
	Kind    ObjectKind
	Rows    int64 // the server's estimate, -1 when unknown
	Bytes   int64 // on disk, -1 when unknown
	Engine  string
	Comment string
	// Partitioning is a PostgreSQL partitioned table's key, as RANGE
	// (placed_at), "" for a table of its own rows.
	Partitioning string
}

// Column is a column of a table or view.
type Column struct {
	Name          string
	Type          string
	Nullable      bool
	Default       string
	HasDefault    bool
	PrimaryKey    bool
	AutoIncrement bool
	Comment       string
}

// Index is an index of a table.
type Index struct {
	Name       string
	Columns    []string
	Unique     bool
	Primary    bool
	Definition string
}

// ForeignKey is a reference from columns of a table to another's.
// Reference is a foreign key of Schema.Table whose Columns point at
// RefColumns of the table asked about.
type Reference struct {
	Name          string
	Schema, Table string
	Columns       []string
	RefColumns    []string
}

type ForeignKey struct {
	Name       string
	Columns    []string
	RefSchema  string
	RefTable   string
	RefColumns []string
	Definition string
}

// Dialect is what differs between SQL engines: quoting, placeholders and
// reading the schema.
type Dialect interface {
	Engine() Engine
	// Quote quotes an identifier.
	Quote(ident string) string
	// Placeholder is the n-th (from 1) parameter of a statement.
	Placeholder(n int) string
	// Databases lists the databases of the server that need a connection
	// of their own (PostgreSQL), nil for engines whose databases are
	// schemas.
	Databases(ctx context.Context, q Querier) ([]string, error)
	// CurrentSchema is the schema unqualified names resolve in.
	CurrentSchema(ctx context.Context, q Querier) (string, error)
	Schemas(ctx context.Context, q Querier) ([]string, error)
	Objects(ctx context.Context, q Querier, schema string) ([]Object, error)
	Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error)
	Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error)
	ForeignKeys(ctx context.Context, q Querier, schema, table string) ([]ForeignKey, error)
	// ReferencedBy lists the foreign keys of other tables that point at
	// this one.
	ReferencedBy(ctx context.Context, q Querier, schema, table string) ([]Reference, error)
	DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error)
	// Items lists a schema's objects besides its tables and views:
	// routines, triggers, sequences, types and the like.
	Items(ctx context.Context, q Querier, schema string) ([]Item, error)
	// ItemDDL writes the statement creating an item.
	ItemDDL(ctx context.Context, q Querier, it Item) (string, error)
	// Editable reports whether the app may change a table's rows from
	// the grid, and why not.
	Editable() (bool, string)
}

// DialectOf returns the dialect of an engine.
func DialectOf(e Engine) Dialect {
	switch e {
	case Postgres:
		return postgresDialect{}
	case MySQL:
		return mysqlDialect{}
	case ClickHouse:
		return clickhouseDialect{}
	case SQLite:
		return sqliteDialect{}
	case DuckDB:
		return duckdbDialect{}
	}
	return nil
}

// QualifiedName quotes a table name with its schema.
func QualifiedName(d Dialect, schema, table string) string {
	if schema == "" {
		return d.Quote(table)
	}
	return d.Quote(schema) + "." + d.Quote(table)
}

func quoteDouble(s string) string   { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func quoteBacktick(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// unitSep separates the items of lists that queries return as one string.
const unitSep = "\x1f"

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, unitSep)
}

// queryStrings returns the first column of every row.
func queryStrings(ctx context.Context, q Querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s.String)
	}
	return out, rows.Err()
}

func queryString(ctx context.Context, q Querier, query string, args ...any) (string, error) {
	out, err := queryStrings(ctx, q, query, args...)
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "", fmt.Errorf("no result")
	}
	return out[0], nil
}

// scanRows calls fn for every row, with the row's scanner.
func scanRows(ctx context.Context, q Querier, query string, args []any, fn func(scan func(...any) error) error) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

// columnsDDL writes the column lines of a CREATE TABLE.
func columnsDDL(d Dialect, cols []Column) []string {
	var lines []string
	for _, c := range cols {
		line := "  " + d.Quote(c.Name) + " " + c.Type
		if !c.Nullable {
			line += " NOT NULL"
		}
		if c.HasDefault {
			line += " DEFAULT " + c.Default
		}
		lines = append(lines, line)
	}
	return lines
}

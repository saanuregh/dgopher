package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// ServerParam is an argument the server binds itself, as a ClickHouse
// {name:Type} parameter; Value is its text, \N for NULL.
type ServerParam struct {
	Name, Value string
}

// withServerParams moves the ServerParams out of args and, on ClickHouse,
// into ctx.
func withServerParams(ctx context.Context, e Engine, args []any) (context.Context, []any, error) {
	params := clickhouse.Parameters{}
	var rest []any
	for _, a := range args {
		if p, ok := a.(ServerParam); ok {
			params[p.Name] = p.Value
		} else {
			rest = append(rest, a)
		}
	}
	if len(params) == 0 {
		return ctx, args, nil
	}
	if e != ClickHouse {
		return ctx, nil, fmt.Errorf("%s does not take server parameters", e)
	}
	return clickhouse.Context(ctx, clickhouse.WithParameters(params)), rest, nil
}

type clickhouseDialect struct{}

func (clickhouseDialect) Engine() Engine         { return ClickHouse }
func (clickhouseDialect) Quote(s string) string  { return quoteBacktick(s) }
func (clickhouseDialect) Placeholder(int) string { return "?" }
func (clickhouseDialect) Editable() (bool, string) {
	return false, "ClickHouse changes rows with asynchronous mutations (ALTER TABLE … UPDATE), not row edits: use the SQL editor."
}

func (clickhouseDialect) Databases(context.Context, Querier) ([]string, error) { return nil, nil }

func (clickhouseDialect) CurrentSchema(ctx context.Context, q Querier) (string, error) {
	return queryString(ctx, q, `SELECT currentDatabase()`)
}

func (clickhouseDialect) Schemas(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT name FROM system.databases
ORDER BY name IN ('system', 'INFORMATION_SCHEMA', 'information_schema'), name`)
}

func (clickhouseDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	var out []Object
	err := scanRows(ctx, q, `SELECT name, engine, ifNull(toInt64(total_rows), toInt64(-1)), ifNull(toInt64(total_bytes), toInt64(-1)), comment
FROM system.tables WHERE database = ? AND NOT is_temporary ORDER BY name`, []any{schema}, func(scan func(...any) error) error {
		o := Object{Schema: schema}
		if err := scan(&o.Name, &o.Engine, &o.Rows, &o.Bytes, &o.Comment); err != nil {
			return err
		}
		switch o.Engine {
		case "View", "LiveView", "WindowView":
			o.Kind = KindView
		case "MaterializedView":
			o.Kind = KindMaterializedView
		case "Dictionary":
			o.Kind = KindDictionary
		default:
			o.Kind = KindTable
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

func (clickhouseDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	var out []Column
	err := scanRows(ctx, q, `SELECT name, type, default_kind, default_expression, toBool(is_in_primary_key), comment
FROM system.columns WHERE database = ? AND table = ? ORDER BY position`, []any{schema, table}, func(scan func(...any) error) error {
		var c Column
		var kind string
		if err := scan(&c.Name, &c.Type, &kind, &c.Default, &c.PrimaryKey, &c.Comment); err != nil {
			return err
		}
		c.Nullable = strings.HasPrefix(c.Type, "Nullable(")
		c.HasDefault = kind != ""
		if kind != "" && kind != "DEFAULT" {
			c.Default = kind + " " + c.Default
		}
		out = append(out, c)
		return nil
	})
	return out, err
}

func (clickhouseDialect) Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error) {
	var out []Index
	var sortingKey, primaryKey string
	err := scanRows(ctx, q, `SELECT sorting_key, primary_key FROM system.tables WHERE database = ? AND name = ?`,
		[]any{schema, table}, func(scan func(...any) error) error { return scan(&sortingKey, &primaryKey) })
	if err != nil {
		return nil, err
	}
	if primaryKey != "" {
		out = append(out, Index{Name: "PRIMARY KEY", Primary: true, Columns: strings.Split(primaryKey, ", "), Definition: "PRIMARY KEY (" + primaryKey + ")"})
	}
	if sortingKey != "" && sortingKey != primaryKey {
		out = append(out, Index{Name: "ORDER BY", Columns: strings.Split(sortingKey, ", "), Definition: "ORDER BY (" + sortingKey + ")"})
	}
	err = scanRows(ctx, q, `SELECT name, type_full, expr, granularity FROM system.data_skipping_indices
WHERE database = ? AND table = ? ORDER BY name`, []any{schema, table}, func(scan func(...any) error) error {
		var ix Index
		var typ, expr string
		var gran uint64
		if err := scan(&ix.Name, &typ, &expr, &gran); err != nil {
			return err
		}
		ix.Columns = []string{expr}
		ix.Definition = "INDEX " + ix.Name + " " + expr + " TYPE " + typ
		out = append(out, ix)
		return nil
	})
	return out, err
}

func (clickhouseDialect) ForeignKeys(context.Context, Querier, string, string) ([]ForeignKey, error) {
	return nil, nil
}

func (d clickhouseDialect) DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error) {
	def, err := queryString(ctx, q, `SELECT create_table_query FROM system.tables WHERE database = ? AND name = ?`, schema, obj.Name)
	if err != nil {
		return "", err
	}
	return def + ";\n", nil
}

func (clickhouseDialect) ReferencedBy(context.Context, Querier, string, string) ([]Reference, error) {
	return nil, nil // ClickHouse has no foreign keys
}

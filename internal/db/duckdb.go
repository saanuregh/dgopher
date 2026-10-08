package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type duckdbDialect struct{}

func (duckdbDialect) Engine() Engine           { return DuckDB }
func (duckdbDialect) Quote(s string) string    { return quoteDouble(s) }
func (duckdbDialect) Placeholder(int) string   { return "?" }
func (duckdbDialect) Editable() (bool, string) { return true, "" }

func (duckdbDialect) Databases(context.Context, Querier) ([]string, error) { return nil, nil }

func (duckdbDialect) CurrentSchema(ctx context.Context, q Querier) (string, error) {
	return queryString(ctx, q, `SELECT current_schema()`)
}

func (duckdbDialect) Schemas(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT schema_name FROM duckdb_schemas()
WHERE database_name = current_database() AND schema_name NOT IN ('information_schema', 'pg_catalog')
ORDER BY schema_name <> 'main', schema_name`)
}

func (duckdbDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	var out []Object
	err := scanRows(ctx, q, `SELECT table_name, 'table', estimated_size, COALESCE(comment, '') FROM duckdb_tables()
WHERE database_name = current_database() AND schema_name = ? AND NOT internal
UNION ALL
SELECT view_name, 'view', -1, COALESCE(comment, '') FROM duckdb_views()
WHERE database_name = current_database() AND schema_name = ? AND NOT internal
ORDER BY 1`, []any{schema, schema}, func(scan func(...any) error) error {
		o := Object{Schema: schema, Bytes: -1}
		var kind string
		var rows sql.NullInt64
		if err := scan(&o.Name, &kind, &rows, &o.Comment); err != nil {
			return err
		}
		o.Rows = -1
		if rows.Valid {
			o.Rows = rows.Int64
		}
		o.Kind = KindTable
		if kind == "view" {
			o.Kind = KindView
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

// primaryKey returns the columns of a DuckDB table's primary key.
func (duckdbDialect) primaryKey(ctx context.Context, q Querier, schema, table string) (map[string]bool, error) {
	pk := map[string]bool{}
	cols, err := queryStrings(ctx, q, `SELECT array_to_string(constraint_column_names, chr(31)) FROM duckdb_constraints()
WHERE database_name = current_database() AND schema_name = ? AND table_name = ? AND constraint_type = 'PRIMARY KEY'`, schema, table)
	if err != nil {
		return nil, err
	}
	for _, c := range cols {
		for _, name := range splitList(c) {
			pk[name] = true
		}
	}
	return pk, nil
}

func (d duckdbDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	pk, err := d.primaryKey(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	var out []Column
	err = scanRows(ctx, q, `SELECT column_name, data_type, is_nullable, column_default, COALESCE(comment, '')
FROM duckdb_columns() WHERE database_name = current_database() AND schema_name = ? AND table_name = ?
ORDER BY column_index`, []any{schema, table}, func(scan func(...any) error) error {
		var c Column
		var def sql.NullString
		if err := scan(&c.Name, &c.Type, &c.Nullable, &def, &c.Comment); err != nil {
			return err
		}
		c.Default, c.HasDefault = def.String, def.Valid
		c.PrimaryKey = pk[c.Name]
		c.AutoIncrement = strings.HasPrefix(c.Default, "nextval(")
		out = append(out, c)
		return nil
	})
	return out, err
}

func (d duckdbDialect) Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error) {
	var out []Index
	pk, err := d.primaryKey(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	if len(pk) > 0 {
		cols, _ := d.Columns(ctx, q, schema, table)
		ix := Index{Name: "PRIMARY KEY", Primary: true, Unique: true}
		for _, c := range cols {
			if c.PrimaryKey {
				ix.Columns = append(ix.Columns, c.Name)
			}
		}
		ix.Definition = "PRIMARY KEY (" + strings.Join(ix.Columns, ", ") + ")"
		out = append(out, ix)
	}
	err = scanRows(ctx, q, `SELECT index_name, is_unique, COALESCE(sql, ''), COALESCE(expressions, '') FROM duckdb_indexes()
WHERE database_name = current_database() AND schema_name = ? AND table_name = ? ORDER BY index_name`,
		[]any{schema, table}, func(scan func(...any) error) error {
			var ix Index
			var exprs string
			if err := scan(&ix.Name, &ix.Unique, &ix.Definition, &exprs); err != nil {
				return err
			}
			ix.Columns = []string{strings.Trim(exprs, "[]")}
			out = append(out, ix)
			return nil
		})
	return out, err
}

func (duckdbDialect) ForeignKeys(ctx context.Context, q Querier, schema, table string) ([]ForeignKey, error) {
	var out []ForeignKey
	err := scanRows(ctx, q, `SELECT constraint_text, array_to_string(constraint_column_names, chr(31)) FROM duckdb_constraints()
WHERE database_name = current_database() AND schema_name = ? AND table_name = ? AND constraint_type = 'FOREIGN KEY'`,
		[]any{schema, table}, func(scan func(...any) error) error {
			var text, cols string
			if err := scan(&text, &cols); err != nil {
				return err
			}
			fk := ForeignKey{Definition: text, Columns: splitList(cols), RefSchema: schema}
			fk.Name = "fk_" + strings.Join(fk.Columns, "_")
			// FOREIGN KEY (a) REFERENCES other(b)
			if _, after, ok := strings.Cut(text, "REFERENCES "); ok {
				name, rest, _ := strings.Cut(after, "(")
				fk.RefTable = strings.Trim(strings.TrimSpace(name), `"`)
				fk.RefColumns = strings.Split(strings.TrimSuffix(strings.TrimSpace(rest), ")"), ", ")
			}
			out = append(out, fk)
			return nil
		})
	return out, err
}

func (duckdbDialect) DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error) {
	fn, col := "duckdb_tables()", "table_name"
	if obj.Kind == KindView {
		fn, col = "duckdb_views()", "view_name"
	}
	def, err := queryString(ctx, q, fmt.Sprintf(`SELECT sql FROM %s WHERE database_name = current_database() AND schema_name = ? AND %s = ?`, fn, col), schema, obj.Name)
	if err != nil {
		return "", err
	}
	ixs, _ := queryStrings(ctx, q, `SELECT sql FROM duckdb_indexes() WHERE database_name = current_database() AND schema_name = ? AND table_name = ? AND sql IS NOT NULL`, schema, obj.Name)
	return strings.Join(append([]string{strings.TrimRight(def, ";")}, ixs...), ";\n\n") + ";\n", nil
}

func (duckdbDialect) ReferencedBy(ctx context.Context, q Querier, schema, table string) ([]Reference, error) {
	var out []Reference
	err := scanRows(ctx, q, `SELECT schema_name, table_name, array_to_string(constraint_column_names, chr(31)),
  array_to_string(referenced_column_names, chr(31))
FROM duckdb_constraints()
WHERE database_name = current_database() AND constraint_type = 'FOREIGN KEY' AND schema_name = ? AND referenced_table = ?
ORDER BY table_name`, []any{schema, table}, func(scan func(...any) error) error {
		var r Reference
		var cols, refCols string
		if err := scan(&r.Schema, &r.Table, &cols, &refCols); err != nil {
			return err
		}
		r.Columns, r.RefColumns = splitList(cols), splitList(refCols)
		r.Name = "fk_" + strings.Join(r.Columns, "_")
		out = append(out, r)
		return nil
	})
	return out, err
}

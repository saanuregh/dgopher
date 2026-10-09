package db

import (
	"context"
	"database/sql"
	"strings"
)

type sqliteDialect struct{}

func (sqliteDialect) Engine() Engine           { return SQLite }
func (sqliteDialect) Quote(s string) string    { return quoteDouble(s) }
func (sqliteDialect) Placeholder(int) string   { return "?" }
func (sqliteDialect) Editable() (bool, string) { return true, "" }

func (sqliteDialect) Databases(context.Context, Querier) ([]string, error) { return nil, nil }

func (sqliteDialect) CurrentSchema(context.Context, Querier) (string, error) { return "main", nil }

func (sqliteDialect) Schemas(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT name FROM pragma_database_list ORDER BY seq`)
}

func (d sqliteDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	var out []Object
	err := scanRows(ctx, q, `SELECT name, type FROM `+d.Quote(schema)+`.sqlite_master
WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`, nil, func(scan func(...any) error) error {
		o := Object{Schema: schema, Rows: -1, Bytes: -1, Kind: KindTable}
		var typ string
		if err := scan(&o.Name, &typ); err != nil {
			return err
		}
		if typ == "view" {
			o.Kind = KindView
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

func (sqliteDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	var out []Column
	var pks int
	err := scanRows(ctx, q, `SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?, ?) ORDER BY cid`,
		[]any{table, schema}, func(scan func(...any) error) error {
			var c Column
			var notNull, pk int64
			var def sql.NullString
			if err := scan(&c.Name, &c.Type, &notNull, &def, &pk); err != nil {
				return err
			}
			c.Nullable = notNull == 0
			c.Default, c.HasDefault = def.String, def.Valid
			c.PrimaryKey = pk > 0
			if c.PrimaryKey {
				pks++
			}
			out = append(out, c)
			return nil
		})
	if pks == 1 {
		for i := range out {
			// The one INTEGER PRIMARY KEY is the rowid, numbered on its own.
			if out[i].PrimaryKey && strings.EqualFold(out[i].Type, "INTEGER") {
				out[i].AutoIncrement = true
			}
		}
	}
	return out, err
}

func (d sqliteDialect) Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error) {
	var out []Index
	type row struct {
		name   string
		unique bool
		origin string
	}
	var list []row
	err := scanRows(ctx, q, `SELECT name, "unique", origin FROM pragma_index_list(?, ?) ORDER BY name`, []any{table, schema},
		func(scan func(...any) error) error {
			var r row
			var unique int64
			if err := scan(&r.name, &unique, &r.origin); err != nil {
				return err
			}
			r.unique = unique != 0
			list = append(list, r)
			return nil
		})
	if err != nil {
		return nil, err
	}
	for _, r := range list {
		cols, err := queryStrings(ctx, q, `SELECT COALESCE(name, '<expression>') FROM pragma_index_info(?, ?) ORDER BY seqno`, r.name, schema)
		if err != nil {
			return nil, err
		}
		def, _ := queryString(ctx, q, `SELECT COALESCE(sql, '') FROM `+d.Quote(schema)+`.sqlite_master WHERE type = 'index' AND name = ?`, r.name)
		out = append(out, Index{Name: r.name, Columns: cols, Unique: r.unique, Primary: r.origin == "pk", Definition: def})
	}
	return out, nil
}

func (sqliteDialect) ForeignKeys(ctx context.Context, q Querier, schema, table string) ([]ForeignKey, error) {
	var out []ForeignKey
	byID := map[int64]int{}
	err := scanRows(ctx, q, `SELECT id, "table", "from", COALESCE("to", '') FROM pragma_foreign_key_list(?, ?) ORDER BY id, seq`,
		[]any{table, schema}, func(scan func(...any) error) error {
			var id int64
			var ref, from, to string
			if err := scan(&id, &ref, &from, &to); err != nil {
				return err
			}
			i, ok := byID[id]
			if !ok {
				i = len(out)
				byID[id] = i
				out = append(out, ForeignKey{RefSchema: schema, RefTable: ref})
			}
			out[i].Columns = append(out[i].Columns, from)
			out[i].RefColumns = append(out[i].RefColumns, to)
			return nil
		})
	for i := range out {
		fk := &out[i]
		fk.Name = "fk_" + strings.Join(fk.Columns, "_")
		fk.Definition = "FOREIGN KEY (" + strings.Join(fk.Columns, ", ") + ") REFERENCES " + fk.RefTable + " (" + strings.Join(fk.RefColumns, ", ") + ")"
	}
	return out, err
}

// DDL writes a table's or view's statement and its indexes'; its
// triggers are items of their own, as on the other engines.
func (d sqliteDialect) DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error) {
	defs, err := queryStrings(ctx, q, `SELECT sql FROM `+d.Quote(schema)+`.sqlite_master
WHERE tbl_name = ? AND type <> 'trigger' AND sql IS NOT NULL ORDER BY type <> 'table' AND type <> 'view', name`, obj.Name)
	if err != nil {
		return "", err
	}
	return strings.Join(defs, ";\n\n") + ";\n", nil
}

func (d sqliteDialect) ReferencedBy(ctx context.Context, q Querier, schema, table string) ([]Reference, error) {
	var out []Reference
	type key struct {
		table string
		id    int64
	}
	byKey := map[key]int{}
	err := scanRows(ctx, q, `SELECT m.name, f.id, f."from", COALESCE(f."to", '')
FROM `+d.Quote(schema)+`.sqlite_master m, pragma_foreign_key_list(m.name, ?) f
WHERE m.type = 'table' AND lower(f."table") = lower(?)
ORDER BY m.name, f.id, f.seq`, []any{schema, table}, func(scan func(...any) error) error {
		var name, from, to string
		var id int64
		if err := scan(&name, &id, &from, &to); err != nil {
			return err
		}
		k := key{name, id}
		i, ok := byKey[k]
		if !ok {
			i = len(out)
			byKey[k] = i
			out = append(out, Reference{Schema: schema, Table: name})
		}
		out[i].Columns = append(out[i].Columns, from)
		out[i].RefColumns = append(out[i].RefColumns, to)
		return nil
	})
	for i := range out {
		out[i].Name = "fk_" + strings.Join(out[i].Columns, "_")
	}
	return out, err
}

// Items lists the schema's triggers, SQLite's only objects besides tables,
// views and indexes.
func (d sqliteDialect) Items(ctx context.Context, q Querier, schema string) ([]Item, error) {
	return scanItems(ctx, q, schema, `SELECT 'trigger', name, tbl_name, '', '' FROM `+d.Quote(schema)+`.sqlite_master
WHERE type = 'trigger' ORDER BY name`)
}

// ItemDDL writes a trigger's definition as SQLite keeps it.
func (d sqliteDialect) ItemDDL(ctx context.Context, q Querier, it Item) (string, error) {
	if it.Kind != ItemTrigger {
		return "", errNoDefinition(it)
	}
	def, err := queryString(ctx, q, `SELECT sql FROM `+d.Quote(it.Schema)+`.sqlite_master WHERE type = 'trigger' AND name = ?`, it.Name)
	return def + ";\n", err
}

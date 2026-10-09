package datamodel

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"dgopher/internal/db"
)

// Script writes a model's tables as the statements making them on an
// engine, in a schema, "" for none named: each table after those its keys
// point at, and a key closing a cycle added once both tables are made.
// Notes say what of the model the engine cannot have, or the statements
// leave out.
func Script(m *Model, to db.Engine, schema string) (db.SchemaChange, []string) {
	d := db.DialectOf(to)
	n := notes{}
	tables := make([]db.TableDesign, len(m.Tables))
	for i, t := range m.Tables {
		tables[i] = convert(m.Engine, to, fresh(to, moved(t, schema), &n), t.Schema == schema, &n)
	}
	if m.Engine != to {
		apart(to, tables, &n)
	}
	ch := db.SchemaChange{Atomic: to.TransactionalDDL()}
	// SQLite takes a key to a table not made yet, and adds none later.
	ordered, closing := createOrder(tables, to != db.SQLite)
	for _, t := range ordered {
		made, err := db.NewTableChange(d, t)
		if err != nil {
			n.add(fmt.Sprintf("%s is not written: %v.", t.Name, err))
			continue
		}
		ch.Steps = append(ch.Steps, made.Steps...)
	}
	for _, k := range closing {
		if to == db.DuckDB {
			n.add(fmt.Sprintf("DuckDB adds no key to a table made, and %s's to %s closes a cycle of keys: it is left out.", k.table.Name, k.key.RefTable))
			continue
		}
		ch.Steps = append(ch.Steps, db.Step{SQL: db.AddForeignKeySQL(d, k.table, k.key)})
	}
	return ch, n.list
}

// ScriptText is a script with a header naming its model, its engine, and
// what it leaves out.
func ScriptText(m *Model, to db.Engine, ch db.SchemaChange, notes []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "-- The data model %q, for %s.\n", m.Name, to.Label())
	b.WriteString("-- A model keeps tables: its types, sequences, views and routines are not in it.\n")
	for _, n := range notes {
		b.WriteString("-- " + n + "\n")
	}
	b.WriteString("\n" + ch.Text() + "\n")
	return b.String()
}

// notes are what a script or a migration says it leaves out, each once.
type notes struct{ list []string }

func (n *notes) add(s string) {
	if !slices.Contains(n.list, s) {
		n.list = append(n.list, s)
	}
}

// moved is a table in another schema, its keys to its own schema's
// tables pointing at the other's.
func moved(t db.TableDesign, schema string) db.TableDesign {
	t = clone(t)
	for i := range t.ForeignKeys {
		if t.ForeignKeys[i].RefSchema == t.Schema {
			t.ForeignKeys[i].RefSchema = schema
		}
	}
	t.Schema = schema
	return t
}

// clone copies a design, so that a change to the copy's lists leaves the
// model's.
func clone(t db.TableDesign) db.TableDesign {
	t.Columns = slices.Clone(t.Columns)
	t.Indexes = slices.Clone(t.Indexes)
	for i := range t.Indexes {
		t.Indexes[i].Columns = slices.Clone(t.Indexes[i].Columns)
	}
	t.ForeignKeys = slices.Clone(t.ForeignKeys)
	for i := range t.ForeignKeys {
		fk := &t.ForeignKeys[i]
		fk.Columns, fk.RefColumns = slices.Clone(fk.Columns), slices.Clone(fk.RefColumns)
	}
	t.Checks = slices.Clone(t.Checks)
	return t
}

var (
	// literalDefault is a default that is a value, a string's or a
	// number's, with the casts PostgreSQL writes after it.
	literalDefault = regexp.MustCompile(`^\(*('(?:[^']|'')*'|-?\d+(?:\.\d+)?)\)*(?:::[a-z ]+(?:\(\d+(?:,\d+)?\))?)*$`)
	// portableDefault is a default every engine writes alike.
	portableDefault = regexp.MustCompile(`(?i)^(NULL|TRUE|FALSE|CURRENT_TIMESTAMP|CURRENT_DATE|CURRENT_TIME)$`)
)

// fresh is a table as a new database makes it, which has none of the
// model's sequences: a serial column numbers its rows itself instead,
// where the engine can. A computed column, whose expression the model
// does not keep, is a plain one.
func fresh(to db.Engine, t db.TableDesign, n *notes) db.TableDesign {
	for i := range t.Columns {
		c := &t.Columns[i]
		if c.Generated {
			n.add(fmt.Sprintf("%s.%s is computed, and the model keeps no expression: it is written as a plain column.", t.Name, c.Name))
			c.Generated = false
		}
		if strings.HasPrefix(strings.ToLower(c.Default), "nextval(") {
			c.Default, c.AutoIncrement = "", true
			n.add("Serial columns are written as identity columns: the model keeps no sequences.")
		}
		if c.AutoIncrement && (to == db.DuckDB || to == db.ClickHouse) {
			c.AutoIncrement = false
			n.add(fmt.Sprintf("%s numbers no rows itself: %s.%s is written as a plain column.", to.Label(), t.Name, c.Name))
		}
	}
	return t
}

// apart makes the names of indexes, and on MySQL of foreign keys, that
// two tables share unique in the schema, as the other engines' names are
// not: MySQL's indexes are named within their table, SQLite's keys are
// named by their columns. A name taken gets its table's before it.
func apart(to db.Engine, tables []db.TableDesign, n *notes) {
	taken := map[string]bool{}
	name := func(t db.TableDesign, name string) string {
		if !taken[name] {
			taken[name] = true
			return name
		}
		n.add("Names two tables share, of indexes or keys, are made apart with their tables' names.")
		for i, next := 2, t.Name+"_"+name; ; i++ {
			if !taken[next] {
				taken[next] = true
				return next
			}
			next = fmt.Sprintf("%s_%s_%d", t.Name, name, i)
		}
	}
	for _, t := range tables {
		for i := range t.Indexes {
			t.Indexes[i].Name = name(t, t.Indexes[i].Name)
		}
		for i := range t.ForeignKeys {
			if to == db.MySQL && t.ForeignKeys[i].Name != "" {
				t.ForeignKeys[i].Name = name(t, t.ForeignKeys[i].Name)
			}
		}
	}
}

// convert is a table of a model as an engine takes it: what the engine
// cannot have is left out, and noted. inPlace is set when the table stays
// in its own schema, where on its own engine its indexes are written as
// the engine wrote them.
func convert(from, to db.Engine, t db.TableDesign, inPlace bool, n *notes) db.TableDesign {
	same := from == to
	keyed := keyedColumns(t)
	for i := range t.Columns {
		c := &t.Columns[i]
		if !same {
			kind, _, _ := typeKind(from, c.Type)
			was := c.Type
			c.Type = convertType(from, to, c.Type)
			switch {
			case kind == "":
				n.add(fmt.Sprintf("%s.%s is of %s's %s, which %s has no like of: it is %s.", t.Name, c.Name, from.Label(), was, to.Label(), c.Type))
			case kind == kindTimestampTZ && (to == db.MySQL || to == db.SQLite):
				n.add(fmt.Sprintf("%s keeps no time zone with a time: %s.%s is %s.", to.Label(), t.Name, c.Name, c.Type))
			}
			if to == db.MySQL && c.Type == "LONGTEXT" && keyed[c.Name] {
				c.Type = "VARCHAR(255)"
				n.add(fmt.Sprintf("MySQL's text cannot be a key: %s.%s is VARCHAR(255).", t.Name, c.Name))
			}
			if def := portable(c.Default, c.Type, to); def != c.Default {
				if def == "" {
					n.add(fmt.Sprintf("The default of %s.%s, %s, is %s's SQL: it is left out.", t.Name, c.Name, c.Default, from.Label()))
				}
				c.Default = def
			}
			if c.Extra != "" {
				c.Extra = ""
				n.add("MySQL's character sets and ON UPDATE of columns are left out.")
			}
		}
		if c.Comment != "" && to == db.SQLite {
			c.Comment = ""
			n.add("SQLite keeps no comments: they are left out.")
		}
	}
	if t.Comment != "" && to == db.SQLite {
		t.Comment = ""
		n.add("SQLite keeps no comments: they are left out.")
	}
	if !same {
		t.PrimaryKeyName = ""
		if from == db.SQLite {
			// Named by SQLite from their columns, which another engine's
			// names would clash by.
			for i := range t.ForeignKeys {
				t.ForeignKeys[i].Name = ""
			}
		}
		if len(t.Checks) > 0 {
			names := make([]string, len(t.Checks))
			for i, ch := range t.Checks {
				names[i] = checkName(ch)
			}
			n.add(fmt.Sprintf("%s's checks are written in %s's SQL, and are left out: %s.", t.Name, from.Label(), strings.Join(names, ", ")))
			t.Checks = nil
		}
	}
	if to == db.ClickHouse && len(t.Indexes) > 0 {
		n.add(fmt.Sprintf("ClickHouse's indexes need a type: %s's are left out.", t.Name))
		t.Indexes = nil
	}
	if !same || !inPlace {
		// As the engine wrote it, an index names the table where it was,
		// in its engine's SQL: it is written from its columns instead.
		t.Indexes = slices.DeleteFunc(t.Indexes, func(ix db.IndexDesign) bool {
			for _, col := range ix.Columns {
				if !slices.ContainsFunc(t.Columns, func(c db.ColumnDesign) bool { return c.Name == col }) {
					n.add(fmt.Sprintf("The index %s of %s is on an expression, written in %s's SQL: it is left out.", ix.Name, t.Name, from.Label()))
					return true
				}
			}
			return false
		})
		for i := range t.Indexes {
			t.Indexes[i].Definition = ""
		}
	}
	if to == db.ClickHouse && len(t.ForeignKeys) > 0 {
		n.add("ClickHouse has no foreign keys: they are left out.")
		t.ForeignKeys = nil
	}
	for i := range t.ForeignKeys {
		fk := &t.ForeignKeys[i]
		if to == db.DuckDB && (fk.OnDelete != "" || fk.OnUpdate != "") {
			fk.OnDelete, fk.OnUpdate = "", ""
			n.add("DuckDB's foreign keys take no actions: ON DELETE and ON UPDATE are left out.")
		}
	}
	return t
}

// keyedColumns are the columns of a table in its primary key, an index or
// a foreign key.
func keyedColumns(t db.TableDesign) map[string]bool {
	out := map[string]bool{}
	for _, c := range t.Columns {
		out[c.Name] = out[c.Name] || c.PrimaryKey
	}
	for _, ix := range t.Indexes {
		for _, c := range ix.Columns {
			out[c] = true
		}
	}
	for _, fk := range t.ForeignKeys {
		for _, c := range fk.Columns {
			out[c] = true
		}
	}
	return out
}

// checkName is a check by its name, or its expression when it has none.
func checkName(ch db.CheckDesign) string {
	if ch.Name != "" {
		return ch.Name
	}
	return ch.Expression
}

// portable is a default as another engine writes it: a value, the casts
// PostgreSQL writes after it taken off, or what every engine writes
// alike; any other expression is the source engine's SQL, and is dropped.
func portable(def, typ string, to db.Engine) string {
	if def == "" || portableDefault.MatchString(def) {
		return def
	}
	m := literalDefault.FindStringSubmatch(def)
	if m == nil {
		return ""
	}
	value := m[1]
	if kind, _, _ := typeKind(to, typ); kind == kindBoolean && to != db.SQLite {
		switch strings.Trim(value, "'") {
		case "0", "f", "false":
			return "FALSE"
		case "1", "t", "true":
			return "TRUE"
		}
	}
	return value
}

// closingKey is a foreign key closing a cycle of tables, added once its
// table and the one it points at are made.
type closingKey struct {
	table db.TableDesign
	key   db.ForeignKeyDesign
}

// createOrder orders tables so that each comes after those its keys point
// at, in their order otherwise. A cycle is broken at its first table: by
// its keys to tables not made yet, which close after, when breaking;
// else the table comes first, its keys as they are.
func createOrder(tables []db.TableDesign, breaking bool) ([]db.TableDesign, []closingKey) {
	tables = slices.Clone(tables)
	index := map[string]int{}
	for i, t := range tables {
		index[t.Schema+"."+t.Name] = i
	}
	made := make([]bool, len(tables))
	// pending reports whether a key of tables[i] points at another table
	// not made yet.
	pending := func(i int, fk db.ForeignKeyDesign) bool {
		schema := cmp.Or(fk.RefSchema, tables[i].Schema)
		j, ok := index[schema+"."+fk.RefTable]
		return ok && j != i && !made[j]
	}
	var out []db.TableDesign
	var closing []closingKey
	for len(out) < len(tables) {
		next := -1
		for i, t := range tables {
			if !made[i] && !slices.ContainsFunc(t.ForeignKeys, func(fk db.ForeignKeyDesign) bool { return pending(i, fk) }) {
				next = i
				break
			}
		}
		if next < 0 {
			next = slices.Index(made, false)
			if breaking {
				t := clone(tables[next])
				var kept []db.ForeignKeyDesign
				for _, fk := range t.ForeignKeys {
					if pending(next, fk) {
						closing = append(closing, closingKey{t, fk})
					} else {
						kept = append(kept, fk)
					}
				}
				t.ForeignKeys = kept
				tables[next] = t
			}
		}
		made[next] = true
		out = append(out, tables[next])
	}
	return out, closing
}

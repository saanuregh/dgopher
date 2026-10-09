package db

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ChangeKind is what a pending change of a grid does.
type ChangeKind int

const (
	ChangeUpdate ChangeKind = iota
	ChangeInsert
	ChangeDelete
)

// Change is one row's pending change.
type Change struct {
	Kind ChangeKind
	// Key holds the values of the key columns as read from the database,
	// for updates and deletes.
	Key []any
	// Values maps columns to their new values: Typed values are given
	// as text the user typed, which the database converts; nil is NULL.
	Values map[string]any
}

// Typed is a value the user typed, which the statement casts to the
// column's type.
type Typed string

// defaultValue sets a column to its default, written as DEFAULT.
type defaultValue struct{}

// Default is the value of a change that sets a column to its default.
var Default = defaultValue{}

// Statement is a statement with its parameters.
type Statement struct {
	SQL  string
	Args []any
	// Want is the number of rows it must change, -1 for any. Updates and
	// deletes by key must change exactly one: more means the key is not
	// unique, and the change is rolled back.
	Want int64
}

// EditTarget is the table a grid edits.
type EditTarget struct {
	Dialect Dialect
	Schema  string
	Table   string
	Columns []Column // all the table's columns, in order
	Key     []string // the columns identifying a row
}

// KeyColumns returns the primary key of a table, or an error saying why
// its rows cannot be edited.
func KeyColumns(cols []Column) ([]string, error) {
	var key []string
	for _, c := range cols {
		if c.PrimaryKey {
			key = append(key, c.Name)
		}
	}
	if len(key) == 0 {
		return nil, errors.New("the table has no primary key, so a row cannot be told apart from its duplicates: edit it with SQL")
	}
	return key, nil
}

func (t *EditTarget) column(name string) (Column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}

// value writes the placeholder of a value, cast to its column's type when
// the user typed it.
func (t *EditTarget) value(col Column, v any, args *[]any) string {
	if v == nil {
		return "NULL"
	}
	if v == Default {
		return "DEFAULT"
	}
	*args = append(*args, v)
	ph := t.Dialect.Placeholder(len(*args))
	typed, ok := v.(Typed)
	if !ok {
		return ph
	}
	(*args)[len(*args)-1] = string(typed)
	if isTextType(col.Type) {
		return ph
	}
	switch t.Dialect.Engine() {
	case Postgres:
		return ph + "::text::" + col.Type
	case DuckDB:
		return "CAST(" + ph + " AS " + col.Type + ")"
	}
	return ph
}

func (t *EditTarget) where(key []any, args *[]any) (string, error) {
	if len(key) != len(t.Key) {
		return "", fmt.Errorf("a row's key has %d values, the table's %d columns", len(key), len(t.Key))
	}
	var parts []string
	for i, name := range t.Key {
		if key[i] == nil {
			return "", fmt.Errorf("the key column %s is NULL", name)
		}
		*args = append(*args, key[i])
		parts = append(parts, t.Dialect.Quote(name)+" = "+t.Dialect.Placeholder(len(*args)))
	}
	return strings.Join(parts, " AND "), nil
}

// Statements turns changes into statements, in the order given.
func (t *EditTarget) Statements(changes []Change) ([]Statement, error) {
	table := QualifiedName(t.Dialect, t.Schema, t.Table)
	var out []Statement
	for _, ch := range changes {
		var args []any
		var cols []Column
		for _, c := range t.Columns {
			if _, ok := ch.Values[c.Name]; ok {
				cols = append(cols, c)
			}
		}
		if len(cols) != len(ch.Values) {
			for name := range ch.Values {
				if _, ok := t.column(name); !ok {
					return nil, fmt.Errorf("no column %q in %s", name, t.Table)
				}
			}
		}
		switch ch.Kind {
		case ChangeUpdate:
			if len(cols) == 0 {
				continue
			}
			var sets []string
			for _, c := range cols {
				sets = append(sets, t.Dialect.Quote(c.Name)+" = "+t.value(c, ch.Values[c.Name], &args))
			}
			where, err := t.where(ch.Key, &args)
			if err != nil {
				return nil, err
			}
			out = append(out, Statement{SQL: "UPDATE " + table + " SET " + strings.Join(sets, ", ") + " WHERE " + where, Args: args, Want: 1})
		case ChangeInsert:
			if len(cols) == 0 {
				sql := "INSERT INTO " + table + " DEFAULT VALUES"
				if t.Dialect.Engine() == MySQL {
					sql = "INSERT INTO " + table + " () VALUES ()"
				}
				out = append(out, Statement{SQL: sql, Want: 1})
				continue
			}
			var names, vals []string
			for _, c := range cols {
				names = append(names, t.Dialect.Quote(c.Name))
				vals = append(vals, t.value(c, ch.Values[c.Name], &args))
			}
			out = append(out, Statement{SQL: "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(vals, ", ") + ")", Args: args, Want: 1})
		case ChangeDelete:
			where, err := t.where(ch.Key, &args)
			if err != nil {
				return nil, err
			}
			out = append(out, Statement{SQL: "DELETE FROM " + table + " WHERE " + where, Args: args, Want: 1})
		}
	}
	return out, nil
}

// ExistingRowChanges counts the statements, as Statements writes them,
// that change or delete a row already there, rather than add one.
func ExistingRowChanges(stmts []Statement) int {
	n := 0
	for _, s := range stmts {
		if strings.HasPrefix(s.SQL, "UPDATE ") || strings.HasPrefix(s.SQL, "DELETE ") {
			n++
		}
	}
	return n
}

// Preview writes a statement with its parameters inlined, for the user to
// read before applying it. It is never executed.
func (s Statement) Preview() string { return s.inline(previewLiteral) }

// Script writes a statement with its parameters as literals of an engine,
// for the user to read, edit and run as SQL.
func (s Statement) Script(e Engine) string {
	return s.inline(func(v any) string {
		if t, ok := v.(Typed); ok {
			v = string(t)
		}
		return Literal(e, v)
	})
}

// inline writes the statement with each of its placeholders, $n or ?,
// written by lit; those inside quotes are names, left as they are.
func (s Statement) inline(lit func(any) string) string {
	var b strings.Builder
	var quote rune
	next := 0
	for i := 0; i < len(s.SQL); i++ {
		c := rune(s.SQL[i])
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '`' || c == '\'':
			quote = c
		case c == '?' && next < len(s.Args):
			b.WriteString(lit(s.Args[next]))
			next++
			continue
		case c == '$':
			j := i + 1
			for j < len(s.SQL) && s.SQL[j] >= '0' && s.SQL[j] <= '9' {
				j++
			}
			if n, err := strconv.Atoi(s.SQL[i+1 : j]); err == nil && n >= 1 && n <= len(s.Args) {
				b.WriteString(lit(s.Args[n-1]))
				i = j - 1
				continue
			}
		}
		b.WriteByte(s.SQL[i])
	}
	return b.String() + ";"
}

func previewLiteral(v any) string {
	if IsNumeric(v) {
		return Display(v)
	}
	if v == nil {
		return "NULL"
	}
	return "'" + strings.ReplaceAll(Display(v), "'", "''") + "'"
}

// isTextType reports whether a column holds text, which a text parameter
// fills without a cast.
func isTextType(t string) bool {
	t = strings.ToLower(t)
	for _, p := range []string{"text", "character varying", "varchar", "character(", "char(", "name", "citext"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return t == "character"
}

package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/sqltext"
)

// readSQLiteDesign reads what pragmas do not tell of a SQLite table, from
// its CREATE TABLE: its checks, which column is AUTOINCREMENT, and what a
// rebuild could not keep.
func readSQLiteDesign(ctx context.Context, d *DB, t *TableDesign) error {
	create, err := queryString(ctx, d.Catalog(), `SELECT sql FROM `+d.Dialect.Quote(t.Schema)+`.sqlite_master WHERE type = 'table' AND name = ?`, t.Name)
	if err != nil {
		return err
	}
	parsed := parseSQLiteTable(create)
	t.Checks = parsed.checks
	t.sqliteUnkept = parsed.unkept
	for i := range t.Columns {
		// The rowid's alias numbers itself with or without AUTOINCREMENT,
		// which only keeps numbers from coming back.
		t.Columns[i].AutoIncrement = slices.Contains(parsed.autoIncrement, strings.ToLower(t.Columns[i].Name))
	}
	for i := range t.Indexes {
		// An index SQLite made for a UNIQUE constraint has no statement.
		t.Indexes[i].Constraint = t.Indexes[i].Definition == ""
	}
	return nil
}

// sqliteTable is what a SQLite CREATE TABLE says that pragmas do not.
type sqliteTable struct {
	checks        []CheckDesign
	autoIncrement []string // the AUTOINCREMENT column, in lower case
	// unkept names what the table form cannot write again, "" for
	// nothing: a rebuild would lose it.
	unkept string
}

// parseSQLiteTable reads a CREATE TABLE: its checks, of the table and of
// its columns, and what the table form could not write again.
func parseSQLiteTable(create string) sqliteTable {
	var out sqliteTable
	var toks []sqltext.Token
	for _, tok := range sqltext.Tokenize(create, sqltext.SQLite) {
		if tok.Kind != sqltext.Whitespace && tok.Kind != sqltext.Comment {
			toks = append(toks, tok)
		}
	}
	word := func(tok sqltext.Token) string {
		if tok.Kind == sqltext.Keyword || tok.Kind == sqltext.Identifier {
			return strings.ToUpper(tok.Text)
		}
		return ""
	}
	open := slices.IndexFunc(toks, func(t sqltext.Token) bool { return t.Text == "(" })
	if open < 0 {
		out.unkept = "a table made by AS SELECT"
		return out
	}
	// Split the body into its items at the commas between them.
	var items [][]sqltext.Token
	depth, start, end := 0, open+1, len(toks)
	for i := open; i < len(toks); i++ {
		switch toks[i].Text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				items, end = append(items, toks[start:i]), i
				i = len(toks)
			}
		case ",":
			if depth == 1 {
				items, start = append(items, toks[start:i]), i+1
			}
		}
	}
	for _, tok := range toks[min(end+1, len(toks)):] {
		if w := word(tok); w == "WITHOUT" || w == "STRICT" {
			out.unkept = "WITHOUT ROWID or STRICT"
		}
	}
	unkept := map[string]string{"COLLATE": "a collation", "GENERATED": "a computed column", "AS": "a computed column",
		"CONFLICT": "an ON CONFLICT clause", "DEFERRABLE": "a deferred foreign key"}
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		name := ""
		column := !slices.Contains([]string{"CONSTRAINT", "PRIMARY", "UNIQUE", "CHECK", "FOREIGN"}, word(item[0]))
		if column {
			name = strings.ToLower(sqltext.DecodeQuoted(item[0].Text, sqltext.SQLite))
		}
		depth := 0 // within the item: what is in parentheses is an expression
		for i := 0; i < len(item); i++ {
			switch item[i].Text {
			case "(":
				depth++
			case ")":
				depth--
			}
			w := word(item[i])
			if why, ok := unkept[w]; ok && depth == 0 && (i > 0 || !column) && out.unkept == "" {
				out.unkept = why
			}
			switch w {
			case "AUTOINCREMENT":
				if column {
					out.autoIncrement = append(out.autoIncrement, name)
				}
			case "CHECK":
				if i+1 < len(item) && item[i+1].Text == "(" {
					check := CheckDesign{Read: true}
					if i >= 2 && word(item[i-2]) == "CONSTRAINT" {
						check.Name = sqltext.DecodeQuoted(item[i-1].Text, sqltext.SQLite)
					}
					close := matchingParen(item, i+1)
					check.Expression = create[runeOffset(create, item[i+1].End):runeOffset(create, item[close].Start)]
					out.checks = append(out.checks, check)
					i = close
				}
			}
		}
	}
	return out
}

// matchingParen is the index of the ")" closing the "(" at open, or the
// last token.
func matchingParen(toks []sqltext.Token, open int) int {
	depth := 0
	for i := open; i < len(toks); i++ {
		switch toks[i].Text {
		case "(":
			depth++
		case ")":
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return len(toks) - 1
}

// runeOffset is the byte offset of the rune at index n of s.
func runeOffset(s string, n int) int {
	i := 0
	for at := range s {
		if i == n {
			return at
		}
		i++
	}
	return len(s)
}

// sqliteAlterChange changes a SQLite table: with ALTER TABLE what it can
// change in place, else by making the table again.
func sqliteAlterChange(d Dialect, was, now TableDesign) (SchemaChange, error) {
	diff := diffDesigns(was, now)
	table := QualifiedName(d, was.Schema, was.Name)
	dropped := func(col string) bool {
		return slices.ContainsFunc(diff.droppedColumns, func(c ColumnDesign) bool { return c.Name == col })
	}
	rebuild := len(diff.changedColumns) > 0 || diff.primaryKeyChanged ||
		len(diff.droppedKeys)+len(diff.addedKeys)+len(diff.droppedChecks)+len(diff.addedChecks) > 0
	// A unique constraint is the table's own: SQLite writes it there.
	for _, ix := range slices.Concat(diff.droppedIndexes, diff.addedIndexes) {
		rebuild = rebuild || ix.Constraint
	}
	for _, c := range diff.addedColumns {
		// ADD COLUMN takes no key, and NOT NULL only with a default.
		rebuild = rebuild || c.PrimaryKey || !c.Nullable && c.Default == ""
	}
	for _, c := range diff.droppedColumns {
		used := c.PrimaryKey || slices.ContainsFunc(now.Indexes, func(ix IndexDesign) bool { return slices.Contains(ix.Columns, c.Name) }) ||
			slices.ContainsFunc(was.ForeignKeys, func(fk ForeignKeyDesign) bool { return slices.Contains(fk.Columns, c.Name) })
		rebuild = rebuild || used
	}
	for _, ix := range now.Indexes {
		for _, c := range ix.Columns {
			if ix.Read && dropped(c) {
				return SchemaChange{}, fmt.Errorf("the index %s uses %s: drop it too", ix.Name, c)
			}
		}
	}
	if rebuild && was.sqliteUnkept != "" {
		return SchemaChange{}, fmt.Errorf("SQLite makes the table again for this change, and the table has %s, which the form cannot keep: change it in SQL", was.sqliteUnkept)
	}

	var steps []Step
	for _, c := range now.Columns {
		if c.Was != "" && c.Was != c.Name {
			steps = append(steps, Step{SQL: "ALTER TABLE " + table + " RENAME COLUMN " + d.Quote(c.Was) + " TO " + d.Quote(c.Name)})
		}
	}
	for _, ix := range diff.droppedIndexes {
		if !ix.Constraint {
			steps = append(steps, Step{SQL: "DROP INDEX " + QualifiedName(d, was.Schema, ix.Name)})
		}
	}
	if rebuild {
		r, err := sqliteRebuild(d, was, now)
		if err != nil {
			return SchemaChange{}, err
		}
		steps = append(steps, Step{Rebuild: r})
	} else {
		for _, c := range diff.droppedColumns {
			steps = append(steps, Step{SQL: "ALTER TABLE " + table + " DROP COLUMN " + d.Quote(c.Name)})
		}
		for _, c := range diff.addedColumns {
			steps = append(steps, Step{SQL: "ALTER TABLE " + table + " ADD COLUMN " + columnSQL(d, c, false)})
		}
	}
	for _, ix := range diff.addedIndexes {
		if !ix.Constraint {
			steps = append(steps, Step{SQL: createIndexSQL(d, was, ix)})
		}
	}
	if now.Name != was.Name {
		steps = append(steps, Step{SQL: "ALTER TABLE " + table + " RENAME TO " + d.Quote(now.Name)})
	}
	return SchemaChange{Steps: steps, Atomic: true}, nil
}

// sqliteRebuild makes a table again as now designs it, under its old
// name, the rows of its kept columns copied; its columns renamed before.
func sqliteRebuild(d Dialect, was, now TableDesign) (*Rebuild, error) {
	temp := "dgopher_new_" + was.Name
	next := now
	next.Name = temp
	next.Schema = was.Schema
	// The unique constraints are written in the new table.
	var unique []string
	for _, ix := range now.Indexes {
		if ix.Constraint {
			unique = append(unique, "UNIQUE ("+quoteNames(d, ix.Columns)+")")
		}
	}
	var kept []string
	for _, c := range now.Columns {
		if c.Was != "" {
			kept = append(kept, d.Quote(c.Name))
		}
	}
	if len(kept) == 0 {
		return nil, errors.New("the table keeps none of its columns: drop it and make another")
	}
	columns := strings.Join(kept, ", ")
	return &Rebuild{Schema: was.Schema, Table: was.Name, Temp: temp,
		Create: createTableSQL(d, next, unique),
		Copy: "INSERT INTO " + QualifiedName(d, was.Schema, temp) + " (" + columns + ") SELECT " + columns +
			" FROM " + QualifiedName(d, was.Schema, was.Name),
	}, nil
}

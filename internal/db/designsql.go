package db

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// NewTableChange writes the statements creating a designed table, its
// indexes and comments.
func NewTableChange(d Dialect, t TableDesign) (SchemaChange, error) {
	if err := validateDesign(d, t); err != nil {
		return SchemaChange{}, err
	}
	e := d.Engine()
	// A unique constraint, as a data model keeps one read, is written in
	// the table; SQLite's are named by SQLite.
	var unique []string
	for _, ix := range t.Indexes {
		if ix.Constraint {
			s := "UNIQUE (" + quoteNames(d, ix.Columns) + ")"
			if e != SQLite && ix.Name != "" {
				s = "CONSTRAINT " + d.Quote(ix.Name) + " " + s
			}
			unique = append(unique, s)
		}
	}
	ch := SchemaChange{Steps: []Step{{SQL: createTableSQL(d, t, unique)}}, Atomic: e.TransactionalDDL()}
	for _, ix := range t.Indexes {
		if !ix.Constraint {
			ch.Steps = append(ch.Steps, Step{SQL: createIndexSQL(d, t, ix)})
		}
	}
	if commentsApart(e) {
		if t.Comment != "" {
			ch.Steps = append(ch.Steps, Step{SQL: "COMMENT ON TABLE " + QualifiedName(d, t.Schema, t.Name) + " IS " + Literal(e, t.Comment)})
		}
		for _, c := range t.Columns {
			if c.Comment != "" {
				ch.Steps = append(ch.Steps, Step{SQL: columnCommentSQL(d, t, c)})
			}
		}
	}
	return ch, nil
}

// createTableSQL writes the CREATE TABLE of a design, with more lines,
// as constraints, after its own.
func createTableSQL(d Dialect, t TableDesign, more []string) string {
	e := d.Engine()
	pk := primaryKey(t)
	// SQLite numbers an INTEGER PRIMARY KEY AUTOINCREMENT only when the
	// key is written in its column.
	inlinePK := e == SQLite && len(pk) == 1 && slices.ContainsFunc(t.Columns, func(c ColumnDesign) bool { return c.PrimaryKey && c.AutoIncrement })
	var lines []string
	for _, c := range t.Columns {
		lines = append(lines, "  "+columnSQL(d, c, inlinePK && c.PrimaryKey))
	}
	if len(pk) > 0 && !inlinePK && e != ClickHouse {
		lines = append(lines, "  PRIMARY KEY ("+quoteNames(d, pk)+")")
	}
	for _, fk := range t.ForeignKeys {
		lines = append(lines, "  "+foreignKeySQL(d, fk))
	}
	for _, ch := range t.Checks {
		lines = append(lines, "  "+checkSQL(d, ch))
	}
	for _, l := range more {
		lines = append(lines, "  "+l)
	}
	create := "CREATE TABLE " + QualifiedName(d, t.Schema, t.Name) + " (\n" + strings.Join(lines, ",\n") + "\n)"
	switch e {
	case MySQL:
		if t.Comment != "" {
			create += " COMMENT = " + Literal(e, t.Comment)
		}
	case ClickHouse:
		order := "tuple()"
		if len(pk) > 0 {
			order = "(" + quoteNames(d, pk) + ")"
		}
		create += " ENGINE = MergeTree ORDER BY " + order
		if t.Comment != "" {
			create += " COMMENT " + Literal(e, t.Comment)
		}
	}
	return create
}

// AlterTableChange writes the statements changing a table from what was
// read to what the form made of it: drops first, so that what is added
// may take a dropped object's name, the table's rename last.
func AlterTableChange(d Dialect, was, now TableDesign) (SchemaChange, error) {
	if err := validateDesign(d, now); err != nil {
		return SchemaChange{}, err
	}
	e := d.Engine()
	if e == SQLite {
		return sqliteAlterChange(d, was, now)
	}
	table := QualifiedName(d, was.Schema, was.Name)
	alter := func(what string) Step { return Step{SQL: "ALTER TABLE " + table + " " + what} }
	diff := diffDesigns(was, now)
	if err := diff.supported(e); err != nil {
		return SchemaChange{}, err
	}
	var steps []Step
	for _, fk := range diff.droppedKeys {
		if e == MySQL {
			steps = append(steps, alter("DROP FOREIGN KEY "+d.Quote(fk.Name)))
		} else {
			steps = append(steps, alter("DROP CONSTRAINT "+d.Quote(fk.Name)))
		}
	}
	for _, ch := range diff.droppedChecks {
		if e == MySQL {
			steps = append(steps, alter("DROP CHECK "+d.Quote(ch.Name)))
		} else {
			steps = append(steps, alter("DROP CONSTRAINT "+d.Quote(ch.Name)))
		}
	}
	for _, ix := range diff.droppedIndexes {
		switch {
		case ix.Constraint:
			steps = append(steps, alter("DROP CONSTRAINT "+d.Quote(ix.Name)))
		case e == MySQL:
			steps = append(steps, Step{SQL: "DROP INDEX " + d.Quote(ix.Name) + " ON " + table})
		default:
			steps = append(steps, Step{SQL: "DROP INDEX " + QualifiedName(d, was.Schema, ix.Name)})
		}
	}
	if diff.primaryKeyChanged && len(primaryKey(was)) > 0 {
		if e == MySQL {
			steps = append(steps, alter("DROP PRIMARY KEY"))
		} else {
			steps = append(steps, alter("DROP CONSTRAINT "+d.Quote(was.PrimaryKeyName)))
		}
	}
	for _, c := range now.Columns {
		if c.Was != "" && c.Was != c.Name {
			steps = append(steps, alter("RENAME COLUMN "+d.Quote(c.Was)+" TO "+d.Quote(c.Name)))
		}
	}
	for _, c := range diff.droppedColumns {
		steps = append(steps, alter("DROP COLUMN "+d.Quote(c.Name)))
	}
	for _, m := range diff.changedColumns {
		changes, err := modifyColumnSQL(d, now, m.was, m.now)
		if err != nil {
			return SchemaChange{}, err
		}
		for _, s := range changes {
			if strings.HasPrefix(s, "COMMENT ON ") {
				steps = append(steps, Step{SQL: s})
			} else {
				steps = append(steps, alter(s))
			}
		}
	}
	for _, c := range diff.addedColumns {
		steps = append(steps, alter("ADD COLUMN "+columnSQL(d, c, false)))
		if commentsApart(e) && c.Comment != "" {
			steps = append(steps, Step{SQL: columnCommentSQL(d, now, c)})
		}
	}
	if pk := primaryKey(now); diff.primaryKeyChanged && len(pk) > 0 {
		steps = append(steps, alter("ADD PRIMARY KEY ("+quoteNames(d, pk)+")"))
	}
	for _, ix := range diff.addedIndexes {
		if ix.Constraint && (e == Postgres || e == MySQL) {
			steps = append(steps, alter("ADD CONSTRAINT "+d.Quote(ix.Name)+" UNIQUE ("+quoteNames(d, ix.Columns)+")"))
		} else {
			steps = append(steps, Step{SQL: createIndexSQL(d, was, ix)})
		}
	}
	for _, fk := range diff.addedKeys {
		steps = append(steps, alter("ADD "+foreignKeySQL(d, fk)))
	}
	for _, ch := range diff.addedChecks {
		steps = append(steps, alter("ADD "+checkSQL(d, ch)))
	}
	if now.Comment != was.Comment {
		switch e {
		case MySQL:
			steps = append(steps, alter("COMMENT = "+Literal(e, now.Comment)))
		case ClickHouse:
			steps = append(steps, alter("MODIFY COMMENT "+Literal(e, now.Comment)))
		default:
			steps = append(steps, Step{SQL: "COMMENT ON TABLE " + table + " IS " + commentLiteral(e, now.Comment)})
		}
	}
	if now.Name != was.Name {
		rename, err := RenameObjectSQL(d, Object{Schema: was.Schema, Name: was.Name, Kind: KindTable}, now.Name)
		if err != nil {
			return SchemaChange{}, err
		}
		steps = append(steps, Step{SQL: rename})
	}
	return SchemaChange{Steps: steps, Atomic: e.TransactionalDDL()}, nil
}

// designDiff is what changed between two designs of a table.
type designDiff struct {
	droppedColumns, addedColumns []ColumnDesign
	changedColumns               []columnChange
	primaryKeyChanged            bool
	droppedIndexes, addedIndexes []IndexDesign
	droppedKeys, addedKeys       []ForeignKeyDesign
	droppedChecks, addedChecks   []CheckDesign
}

type columnChange struct{ was, now ColumnDesign }

func diffDesigns(was, now TableDesign) designDiff {
	var diff designDiff
	byWas := map[string]ColumnDesign{}
	for _, c := range now.Columns {
		if c.Was == "" {
			diff.addedColumns = append(diff.addedColumns, c)
		} else {
			byWas[c.Was] = c
		}
	}
	for _, w := range was.Columns {
		c, kept := byWas[w.Name]
		switch {
		case !kept:
			diff.droppedColumns = append(diff.droppedColumns, w)
		case c.Type != w.Type || c.Nullable != w.Nullable || c.Default != w.Default || c.Comment != w.Comment || c.AutoIncrement != w.AutoIncrement:
			diff.changedColumns = append(diff.changedColumns, columnChange{w, c})
		}
	}
	// The key is the same when the same columns, renamed or not, make it
	// in the same order: a column is known by the name it was read with.
	var wasKey, nowKey []string
	for _, c := range was.Columns {
		if c.PrimaryKey {
			wasKey = append(wasKey, c.Name)
		}
	}
	for _, c := range now.Columns {
		if c.PrimaryKey {
			id := c.Was
			if id == "" {
				id = "\x00" + c.Name // a new column, which no name read can match
			}
			nowKey = append(nowKey, id)
		}
	}
	diff.primaryKeyChanged = !slices.Equal(wasKey, nowKey)

	for _, ix := range was.Indexes {
		if !slices.ContainsFunc(now.Indexes, func(n IndexDesign) bool { return n.Read && n.Name == ix.Name }) {
			diff.droppedIndexes = append(diff.droppedIndexes, ix)
		}
	}
	for _, ix := range now.Indexes {
		if !ix.Read {
			diff.addedIndexes = append(diff.addedIndexes, ix)
		}
	}
	for _, fk := range was.ForeignKeys {
		if !slices.ContainsFunc(now.ForeignKeys, func(n ForeignKeyDesign) bool { return n.Read && n.Name == fk.Name }) {
			diff.droppedKeys = append(diff.droppedKeys, fk)
		}
	}
	for _, fk := range now.ForeignKeys {
		if !fk.Read {
			diff.addedKeys = append(diff.addedKeys, fk)
		}
	}
	for _, ch := range was.Checks {
		if !slices.ContainsFunc(now.Checks, func(n CheckDesign) bool { return n.Read && n.Name == ch.Name && n.Expression == ch.Expression }) {
			diff.droppedChecks = append(diff.droppedChecks, ch)
		}
	}
	for _, ch := range now.Checks {
		if !ch.Read {
			diff.addedChecks = append(diff.addedChecks, ch)
		}
	}
	return diff
}

// supported says what an engine cannot change of a table it made.
func (diff designDiff) supported(e Engine) error {
	keys := diff.primaryKeyChanged || len(diff.droppedKeys)+len(diff.addedKeys)+len(diff.droppedChecks)+len(diff.addedChecks) > 0
	switch {
	case e == ClickHouse && (keys || len(diff.droppedIndexes)+len(diff.addedIndexes) > 0):
		return errors.New("ClickHouse cannot change a table's key, indexes or constraints here: its sorting key is fixed when the table is made")
	case e == DuckDB && keys:
		return errors.New("DuckDB cannot change the keys or checks of a table it made: make the table again")
	}
	for _, ch := range diff.droppedChecks {
		if ch.Name == "" {
			return fmt.Errorf("the check %s has no name to drop it by", ch.Expression)
		}
	}
	return nil
}

// modifyColumnSQL writes the ALTER TABLE clauses, and on PostgreSQL and
// DuckDB the COMMENT statement, changing a column: the column has its new
// name already.
func modifyColumnSQL(d Dialect, t TableDesign, was, now ColumnDesign) ([]string, error) {
	e := d.Engine()
	name := d.Quote(now.Name)
	if now.AutoIncrement != was.AutoIncrement {
		return nil, fmt.Errorf("%s: whether a column numbers its rows changes in SQL", now.Name)
	}
	if now.Generated && (now.Type != was.Type || now.Nullable != was.Nullable) {
		return nil, fmt.Errorf("%s is computed: change its type in SQL", now.Name)
	}
	var out []string
	switch e {
	case MySQL:
		if now.Type == was.Type && now.Nullable == was.Nullable && now.Comment == was.Comment {
			if now.Default == "" {
				return []string{"ALTER COLUMN " + name + " DROP DEFAULT"}, nil
			}
			return []string{"ALTER COLUMN " + name + " SET DEFAULT " + now.Default}, nil
		}
		// MODIFY writes the whole column again, what MySQL keeps besides
		// included.
		return []string{"MODIFY COLUMN " + columnSQL(d, now, false)}, nil
	case ClickHouse:
		defaulted := now.Default == was.Default
		if now.Type != was.Type || now.Nullable != was.Nullable {
			typ := now.Type
			if now.Nullable {
				typ = "Nullable(" + typ + ")"
			}
			modify := "MODIFY COLUMN " + name + " " + typ
			if !defaulted && now.Default != "" {
				// The rows' NULLs take the default, when the column stops
				// holding them.
				modify, defaulted = modify+" DEFAULT "+now.Default, true
			}
			out = append(out, modify)
		}
		if !defaulted {
			if now.Default == "" {
				out = append(out, "MODIFY COLUMN "+name+" REMOVE DEFAULT")
			} else {
				out = append(out, "MODIFY COLUMN "+name+" DEFAULT "+now.Default)
			}
		}
		if now.Comment != was.Comment {
			out = append(out, "COMMENT COLUMN "+name+" "+Literal(e, now.Comment))
		}
		return out, nil
	}
	if now.Type != was.Type {
		out = append(out, "ALTER COLUMN "+name+" TYPE "+now.Type+" USING "+name+"::"+now.Type)
	}
	if now.Default != was.Default {
		if now.Default == "" {
			out = append(out, "ALTER COLUMN "+name+" DROP DEFAULT")
		} else {
			out = append(out, "ALTER COLUMN "+name+" SET DEFAULT "+now.Default)
		}
	}
	if now.Nullable != was.Nullable {
		if now.Nullable {
			out = append(out, "ALTER COLUMN "+name+" DROP NOT NULL")
		} else {
			out = append(out, "ALTER COLUMN "+name+" SET NOT NULL")
		}
	}
	if now.Comment != was.Comment {
		out = append(out, columnCommentSQL(d, t, now))
	}
	return out, nil
}

// columnSQL writes a column's definition, as CREATE TABLE and ADD COLUMN
// take it; inlinePK makes it SQLite's INTEGER PRIMARY KEY.
func columnSQL(d Dialect, c ColumnDesign, inlinePK bool) string {
	e := d.Engine()
	typ := c.Type
	if e == ClickHouse && c.Nullable {
		typ = "Nullable(" + typ + ")"
	}
	parts := []string{d.Quote(c.Name), typ}
	if e == MySQL && c.Extra != "" {
		parts = append(parts, c.Extra)
	}
	if !c.Nullable && e != ClickHouse {
		parts = append(parts, "NOT NULL")
	}
	if c.Default != "" {
		parts = append(parts, "DEFAULT "+c.Default)
	}
	switch {
	case inlinePK:
		parts = append(parts, "PRIMARY KEY")
		if c.AutoIncrement {
			parts = append(parts, "AUTOINCREMENT")
		}
	case c.AutoIncrement && e == Postgres:
		parts = append(parts, "GENERATED BY DEFAULT AS IDENTITY")
	case c.AutoIncrement && e == MySQL:
		parts = append(parts, "AUTO_INCREMENT")
	}
	if c.Comment != "" && (e == MySQL || e == ClickHouse) {
		parts = append(parts, "COMMENT "+Literal(e, c.Comment))
	}
	return strings.Join(parts, " ")
}

func foreignKeySQL(d Dialect, fk ForeignKeyDesign) string {
	s := ""
	if fk.Name != "" {
		s = "CONSTRAINT " + d.Quote(fk.Name) + " "
	}
	ref := QualifiedName(d, fk.RefSchema, fk.RefTable)
	if d.Engine() == SQLite {
		ref = d.Quote(fk.RefTable) // SQLite's keys point within their schema
	}
	return s + "FOREIGN KEY (" + quoteNames(d, fk.Columns) + ") REFERENCES " + ref + " (" + quoteNames(d, fk.RefColumns) + ")" +
		actionsSQL(fk.OnDelete, fk.OnUpdate)
}

func checkSQL(d Dialect, ch CheckDesign) string {
	s := ""
	if ch.Name != "" {
		s = "CONSTRAINT " + d.Quote(ch.Name) + " "
	}
	return s + "CHECK (" + ch.Expression + ")"
}

// AddForeignKeySQL writes the statement adding a foreign key to a table
// made.
func AddForeignKeySQL(d Dialect, t TableDesign, fk ForeignKeyDesign) string {
	return "ALTER TABLE " + QualifiedName(d, t.Schema, t.Name) + " ADD " + foreignKeySQL(d, fk)
}

// createIndexSQL writes the statement making an index: as its definition,
// when that is a statement, as a data model keeps an index read with its
// order, method and WHERE; else from its columns.
func createIndexSQL(d Dialect, t TableDesign, ix IndexDesign) string {
	if def := strings.TrimSpace(ix.Definition); len(def) > 7 && strings.EqualFold(def[:7], "CREATE ") {
		return def
	}
	s := "CREATE INDEX "
	if ix.Unique {
		s = "CREATE UNIQUE INDEX "
	}
	name := d.Quote(ix.Name)
	if d.Engine() == SQLite {
		name = QualifiedName(d, t.Schema, ix.Name) // SQLite names the schema on the index
	}
	table := QualifiedName(d, t.Schema, t.Name)
	if d.Engine() == SQLite {
		table = d.Quote(t.Name)
	}
	return s + name + " ON " + table + " (" + quoteNames(d, ix.Columns) + ")"
}

// commentsApart reports whether an engine comments on tables and columns
// with statements of their own.
func commentsApart(e Engine) bool { return e == Postgres || e == DuckDB }

func columnCommentSQL(d Dialect, t TableDesign, c ColumnDesign) string {
	return "COMMENT ON COLUMN " + QualifiedName(d, t.Schema, t.Name) + "." + d.Quote(c.Name) + " IS " + commentLiteral(d.Engine(), c.Comment)
}

// commentLiteral is a comment as COMMENT ON takes it: NULL removes it.
func commentLiteral(e Engine, comment string) string {
	if comment == "" {
		return "NULL"
	}
	return Literal(e, comment)
}

func primaryKey(t TableDesign) []string {
	var out []string
	for _, c := range t.Columns {
		if c.PrimaryKey {
			out = append(out, c.Name)
		}
	}
	return out
}

func quoteNames(d Dialect, names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = d.Quote(n)
	}
	return strings.Join(quoted, ", ")
}

// validateDesign says what in a design no engine would take, or the
// engine cannot keep.
func validateDesign(d Dialect, t TableDesign) error {
	e := d.Engine()
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("the table needs a name")
	}
	if len(t.Columns) == 0 {
		return errors.New("the table needs a column")
	}
	names := map[string]bool{}
	for _, c := range t.Columns {
		key := strings.ToLower(c.Name)
		switch {
		case strings.TrimSpace(c.Name) == "":
			return errors.New("every column needs a name")
		case names[key]:
			return fmt.Errorf("two columns are named %s", c.Name)
		case strings.TrimSpace(c.Type) == "":
			return fmt.Errorf("%s needs a type", c.Name)
		case c.PrimaryKey && c.Nullable:
			return fmt.Errorf("%s is in the primary key, which holds no NULL", c.Name)
		case c.Comment != "" && e == SQLite:
			return errors.New("SQLite keeps no comments")
		case c.AutoIncrement && c.Was == "" && (e == DuckDB || e == ClickHouse):
			return fmt.Errorf("%s numbers no rows of its own: use a sequence", e.Label())
		}
		names[key] = true
	}
	if t.Comment != "" && e == SQLite {
		return errors.New("SQLite keeps no comments")
	}
	has := func(col string) bool { return names[strings.ToLower(col)] }
	for _, ix := range t.Indexes {
		if ix.Read {
			continue
		}
		if strings.TrimSpace(ix.Name) == "" || len(ix.Columns) == 0 {
			return errors.New("a new index needs a name and a column")
		}
		for _, c := range ix.Columns {
			if !has(c) {
				return fmt.Errorf("the index %s names %s, which is no column", ix.Name, c)
			}
		}
	}
	for _, fk := range t.ForeignKeys {
		if fk.Read {
			continue
		}
		if len(fk.Columns) == 0 || len(fk.Columns) != len(fk.RefColumns) || fk.RefTable == "" {
			return errors.New("a new foreign key needs columns, and as many in the table it points at")
		}
		for _, c := range fk.Columns {
			if !has(c) {
				return fmt.Errorf("a foreign key names %s, which is no column", c)
			}
		}
		for _, action := range []string{fk.OnDelete, fk.OnUpdate} {
			if action != "" && !slices.Contains(ReferentialActions, action) {
				return fmt.Errorf("%s is no action of a foreign key", action)
			}
		}
	}
	for _, ch := range t.Checks {
		if !ch.Read && strings.TrimSpace(ch.Expression) == "" {
			return errors.New("a new check needs an expression")
		}
	}
	return nil
}

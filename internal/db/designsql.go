package db

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/lists"
)

// NewTableChange writes the statements creating a designed table, its
// indexes and comments.
func NewTableChange(d Dialect, t TableDesign) (SchemaChange, error) {
	if err := validateDesign(d, t); err != nil {
		return SchemaChange{}, err
	}
	e, r := d.Engine(), rulesOfDialect(d)
	// A unique constraint, as a data model keeps one read, is written in
	// the table; SQLite's are named by SQLite.
	var unique []string
	for _, ix := range t.Indexes {
		if ix.Constraint {
			s := "UNIQUE (" + QuoteNames(d, ix.Columns) + ")"
			if !r.NamesConstraintsItself() && ix.Name != "" {
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
	if commentsApart(r) {
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
	r := rulesOfDialect(d)
	pk := primaryKey(t)
	// SQLite numbers an INTEGER PRIMARY KEY AUTOINCREMENT only when the
	// key is written in its column.
	inlinePK := r.InlineIntegerKey() && len(pk) == 1 && slices.ContainsFunc(t.Columns, func(c ColumnDesign) bool { return c.PrimaryKey && c.AutoIncrement })
	var lines []string
	for _, c := range t.Columns {
		lines = append(lines, "  "+columnSQL(d, c, inlinePK && c.PrimaryKey))
	}
	if len(pk) > 0 && !inlinePK && r.PrimaryKeyClause() {
		lines = append(lines, "  PRIMARY KEY ("+QuoteNames(d, pk)+")")
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
	return "CREATE TABLE " + QualifiedName(d, t.Schema, t.Name) + " (\n" + strings.Join(lines, ",\n") + "\n)" + r.CreateTableSuffix(d, t, pk)
}

// AlterTableChange writes the statements changing a table from what was
// read to what the form made of it: drops first, so that what is added
// may take a dropped object's name, the table's rename last. A primary
// key replaced at once is replaced before columns are dropped.
func AlterTableChange(d Dialect, was, now TableDesign) (SchemaChange, error) {
	if err := validateDesign(d, now); err != nil {
		return SchemaChange{}, err
	}
	e, r := d.Engine(), rulesOfDialect(d)
	if r.RebuildsToAlter() {
		return sqliteAlterChange(d, was, now)
	}
	table := QualifiedName(d, was.Schema, was.Name)
	alter := func(what string) Step { return Step{SQL: "ALTER TABLE " + table + " " + what} }
	drop := func(kind ConstraintKind, name string) Step {
		return Step{SQL: r.DropConstraintSQL(d, kind, was.Schema, was.Name, name)}
	}
	diff := diffDesigns(was, now)
	if err := diff.supported(r); err != nil {
		return SchemaChange{}, err
	}
	var steps []Step
	for _, fk := range diff.droppedKeys {
		steps = append(steps, drop(ConstraintForeignKey, fk.Name))
	}
	for _, ch := range diff.droppedChecks {
		steps = append(steps, drop(ConstraintCheck, ch.Name))
	}
	for _, ix := range diff.droppedIndexes {
		if ix.Constraint {
			steps = append(steps, alter("DROP CONSTRAINT "+d.Quote(ix.Name)))
		} else {
			steps = append(steps, drop(ConstraintIndex, ix.Name))
		}
	}
	pk, hadPK := primaryKey(now), len(primaryKey(was)) > 0
	// An engine that replaces the key at once keeps one at all times: the
	// new key's columns are made and changed first, the old key's dropped
	// after it.
	atOnce := diff.primaryKeyChanged && hadPK && r.ReplacesPrimaryKeyAtOnce()
	if diff.primaryKeyChanged && hadPK && !atOnce {
		steps = append(steps, drop(ConstraintPrimaryKey, was.PrimaryKeyName))
	}
	for _, c := range now.Columns {
		if c.Was != "" && c.Was != c.Name {
			steps = append(steps, alter("RENAME COLUMN "+d.Quote(c.Was)+" TO "+d.Quote(c.Name)))
		}
	}
	changeColumns := func() error {
		for _, m := range diff.changedColumns {
			changes, err := r.ModifyColumnSQL(d, now, m.was, m.now)
			if err != nil {
				return err
			}
			for _, s := range changes {
				if strings.HasPrefix(s, "COMMENT ON ") {
					steps = append(steps, Step{SQL: s})
				} else {
					steps = append(steps, alter(s))
				}
			}
		}
		return nil
	}
	addColumn := func(c ColumnDesign) {
		steps = append(steps, alter("ADD COLUMN "+columnSQL(d, c, false)))
		if commentsApart(r) && c.Comment != "" {
			steps = append(steps, Step{SQL: columnCommentSQL(d, now, c)})
		}
	}
	added := diff.addedColumns
	if atOnce {
		if err := changeColumns(); err != nil {
			return SchemaChange{}, err
		}
		added = nil
		for _, c := range diff.addedColumns {
			if c.PrimaryKey {
				addColumn(c)
			} else {
				added = append(added, c)
			}
		}
		steps = append(steps, alter("DROP CONSTRAINT "+d.Quote(was.PrimaryKeyName)+", ADD PRIMARY KEY ("+QuoteNames(d, pk)+")"))
	}
	for _, c := range diff.droppedColumns {
		steps = append(steps, alter("DROP COLUMN "+d.Quote(c.Name)))
	}
	if !atOnce {
		if err := changeColumns(); err != nil {
			return SchemaChange{}, err
		}
	}
	for _, c := range added {
		addColumn(c)
	}
	if diff.primaryKeyChanged && !atOnce && len(pk) > 0 {
		steps = append(steps, alter("ADD PRIMARY KEY ("+QuoteNames(d, pk)+")"))
	}
	for _, ix := range diff.addedIndexes {
		if ix.Constraint && r.UniqueAsConstraint() {
			steps = append(steps, alter("ADD CONSTRAINT "+d.Quote(ix.Name)+" UNIQUE ("+QuoteNames(d, ix.Columns)+")"))
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
		steps = append(steps, Step{SQL: r.TableCommentSQL(d, was.Schema, was.Name, now.Comment)})
	}
	if now.Name != was.Name {
		rename, err := r.RenameObjectSQL(Object{Schema: was.Schema, Name: was.Name, Kind: KindTable}, now.Name)
		if err != nil {
			return SchemaChange{}, err
		}
		steps = append(steps, Step{SQL: rename})
	}
	return SchemaChange{Steps: steps, Atomic: e.TransactionalDDL()}, nil
}

// dropConstraintSQL drops a constraint by its name, as standard SQL does,
// and an index of the table's schema.
func dropConstraintSQL(d Dialect, kind ConstraintKind, schema, table, name string) string {
	if kind == ConstraintIndex {
		return "DROP INDEX " + QualifiedName(d, schema, name)
	}
	return "ALTER TABLE " + QualifiedName(d, schema, table) + " DROP CONSTRAINT " + d.Quote(name)
}

func commentOnTableSQL(d Dialect, schema, table, comment string) string {
	return "COMMENT ON TABLE " + QualifiedName(d, schema, table) + " IS " + commentLiteral(d.Engine(), comment)
}

// designDiff is what changed between two designs of a table.
type designDiff struct {
	droppedColumns, addedColumns []ColumnDesign
	changedColumns               []columnChange
	primaryKeyChanged            bool
	primaryKeyRemoved            bool
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
	wasKey := primaryKey(was)
	var nowKey []string
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
	diff.primaryKeyRemoved = len(wasKey) > 0 && len(nowKey) == 0

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
func (diff designDiff) supported(r SQLRules) error {
	keys := diff.primaryKeyChanged || len(diff.droppedKeys)+len(diff.addedKeys)+len(diff.droppedChecks)+len(diff.addedChecks) > 0
	indexes := len(diff.droppedIndexes)+len(diff.addedIndexes) > 0
	// Where a table holds no indexes of its own, none of them changes.
	if why := r.KeysOfMadeTable(); why != "" && (keys || indexes && !r.HasSecondaryIndexes()) {
		return errors.New(why)
	}
	if diff.primaryKeyRemoved && r.ReplacesPrimaryKeyAtOnce() {
		return fmt.Errorf("%s keeps a primary key on every table: mark the columns of a new key instead of removing it", r.Label())
	}
	for _, ch := range diff.droppedChecks {
		if ch.Name == "" {
			return fmt.Errorf("the check %s has no name to drop it by", ch.Expression)
		}
	}
	return nil
}

// castNeeded reports whether a PostgreSQL column of type was needs USING
// to change to type now. To a character type, and to a bit type from one,
// it does not: PostgreSQL then converts as it does when assigning, which
// fails on a value that does not fit, where the explicit cast would cut the
// value, or pad a bit string, without an error.
func castNeeded(was, now string) bool {
	base := func(t string) string {
		t = strings.ToLower(strings.Join(strings.Fields(t), " "))
		if i := strings.IndexByte(t, '('); i >= 0 && strings.HasSuffix(t, ")") {
			t = strings.TrimSpace(t[:i])
		}
		return strings.TrimPrefix(t, "pg_catalog.")
	}
	switch base(now) {
	case "varchar", "character varying", "char", "character", "bpchar":
		return false
	case "bit", "bit varying", "varbit":
		switch base(was) {
		case "bit", "bit varying", "varbit":
			return false
		}
	}
	return true
}

// columnChangeError says what change of a column only SQL makes.
func columnChangeError(was, now ColumnDesign) error {
	if now.AutoIncrement != was.AutoIncrement {
		return fmt.Errorf("%s: whether a column numbers its rows changes in SQL", now.Name)
	}
	if now.Generated && (now.Type != was.Type || now.Nullable != was.Nullable) {
		return fmt.Errorf("%s is computed: change its type in SQL", now.Name)
	}
	return nil
}

// alterColumnSQL changes a column with ALTER COLUMN, as standard SQL
// does; a new type with USING a cast when cast is set.
func alterColumnSQL(d Dialect, t TableDesign, was, now ColumnDesign, cast bool) ([]string, error) {
	if err := columnChangeError(was, now); err != nil {
		return nil, err
	}
	name := d.Quote(now.Name)
	var out []string
	if now.Type != was.Type {
		step := "ALTER COLUMN " + name + " TYPE " + now.Type
		if cast {
			step += " USING " + name + "::" + now.Type
		}
		out = append(out, step)
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
	e, r := d.Engine(), rulesOfDialect(d)
	typ := c.Type
	if r.NullableWrapper() && c.Nullable {
		typ = "Nullable(" + typ + ")"
	}
	parts := []string{d.Quote(c.Name), typ}
	if r.KeepsColumnExtra() && c.Extra != "" {
		parts = append(parts, c.Extra)
	}
	if !c.Nullable && !r.NullableWrapper() {
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
	case c.AutoIncrement && r.AutoIncrementSQL() != "":
		parts = append(parts, r.AutoIncrementSQL())
	}
	if c.Comment != "" && r.ColumnCommentInline() {
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
	if rulesOfDialect(d).NamesTablesWithinSchema() {
		ref = d.Quote(fk.RefTable) // SQLite's keys point within their schema
	}
	return s + "FOREIGN KEY (" + QuoteNames(d, fk.Columns) + ") REFERENCES " + ref + " (" + QuoteNames(d, fk.RefColumns) + ")" +
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
	name, table := d.Quote(ix.Name), QualifiedName(d, t.Schema, t.Name)
	if rulesOfDialect(d).NamesTablesWithinSchema() {
		name = QualifiedName(d, t.Schema, ix.Name) // SQLite names the schema on the index
		table = d.Quote(t.Name)
	}
	return s + name + " ON " + table + " (" + QuoteNames(d, ix.Columns) + ")"
}

// commentsApart reports whether an engine comments on tables and columns
// with statements of their own.
func commentsApart(r SQLRules) bool { return r.HasComments() && !r.ColumnCommentInline() }

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

// QuoteNames quotes each of names in d, joined with ", ".
func QuoteNames(d Dialect, names []string) string {
	return strings.Join(lists.Map(names, func(n string) string { return d.Quote(n) }), ", ")
}

// validateDesign says what in a design no engine would take, or the
// engine cannot keep.
func validateDesign(d Dialect, t TableDesign) error {
	e, r := d.Engine(), rulesOfDialect(d)
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
		case c.Comment != "" && !r.HasComments():
			return fmt.Errorf("%s keeps no comments", e.Label())
		case c.AutoIncrement && c.Was == "" && !r.HasAutoNumbering():
			return fmt.Errorf("%s numbers no rows of its own: use a sequence", e.Label())
		}
		names[key] = true
	}
	if t.Comment != "" && !r.HasComments() {
		return fmt.Errorf("%s keeps no comments", e.Label())
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

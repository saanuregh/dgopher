package datamodel

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/sqltext"
)

// State is how a table of one model stands in another.
type State int

const (
	Same    State = iota
	OnlyInA       // in A, not in B: a migration makes it
	OnlyInB       // in B, not in A: a migration drops it only when asked
	Changed
)

// TableDiff is how a table differs between models A and B.
type TableDiff struct {
	Schema, Name string // A's, else B's
	State        State
	Changes      []Change
}

// Change is a part of a table that differs: each side as written, ""
// where that side has none.
type Change struct {
	What, Name string
	A, B       string
}

// Compare compares two models of one engine, table by table, in A's
// order, then B's tables A lacks. Tables match by name, and by schema too
// when either model has tables of several.
func Compare(a, b *Model) ([]TableDiff, error) {
	if a.Engine != b.Engine {
		return nil, fmt.Errorf("the models are of %s and %s: generate one's DDL for the other's engine, and compare that", a.Engine.Label(), b.Engine.Label())
	}
	var out []TableDiff
	pairs, onlyB := match(a, b)
	for _, p := range pairs {
		d := TableDiff{Schema: p.a.Schema, Name: p.a.Name, State: OnlyInA}
		if p.b != nil {
			d.Changes = diffTable(a.Engine, p.a, *p.b)
			if d.State = Same; len(d.Changes) > 0 {
				d.State = Changed
			}
		}
		out = append(out, d)
	}
	for _, t := range onlyB {
		out = append(out, TableDiff{Schema: t.Schema, Name: t.Name, State: OnlyInB})
	}
	return out, nil
}

// pair is a table of A with B's of its name, nil when B has none.
type pair struct {
	a db.TableDesign
	b *db.TableDesign
}

// match pairs A's tables with B's, and lists B's that A lacks.
func match(a, b *Model) ([]pair, []db.TableDesign) {
	byName := oneSchema(a) && oneSchema(b)
	key := func(t db.TableDesign) string {
		if byName {
			return t.Name
		}
		return t.Schema + "." + t.Name
	}
	inB := map[string]int{}
	for i, t := range b.Tables {
		inB[key(t)] = i
	}
	var pairs []pair
	matched := make([]bool, len(b.Tables))
	for _, t := range a.Tables {
		p := pair{a: t}
		if i, ok := inB[key(t)]; ok {
			p.b, matched[i] = &b.Tables[i], true
		}
		pairs = append(pairs, p)
	}
	var onlyB []db.TableDesign
	for i, t := range b.Tables {
		if !matched[i] {
			onlyB = append(onlyB, t)
		}
	}
	return pairs, onlyB
}

// oneSchema reports whether a model's tables are all of one schema.
func oneSchema(m *Model) bool {
	return !slices.ContainsFunc(m.Tables, func(t db.TableDesign) bool { return t.Schema != m.Tables[0].Schema })
}

// diffTable lists what differs between two designs of a table of an
// engine.
func diffTable(e db.Engine, a, b db.TableDesign) []Change {
	var out []Change
	add := func(what, name, x, y string) { out = append(out, Change{What: what, Name: name, A: x, B: y}) }
	for _, c := range a.Columns {
		i := slices.IndexFunc(b.Columns, func(o db.ColumnDesign) bool { return o.Name == c.Name })
		switch {
		case i < 0:
			add("column", c.Name, columnText(c), "")
		case !sameColumn(e, c, b.Columns[i]):
			add("column", c.Name, columnText(c), columnText(b.Columns[i]))
		}
	}
	for _, c := range b.Columns {
		if !slices.ContainsFunc(a.Columns, func(o db.ColumnDesign) bool { return o.Name == c.Name }) {
			add("column", c.Name, "", columnText(c))
		}
	}
	if x, y := keyText(a), keyText(b); x != y {
		add("primary key", "", x, y)
	}
	sameSchema := a.Schema == b.Schema
	diffParts(a.Indexes, b.Indexes, indexID(e),
		func(x, y db.IndexDesign) bool { return sameIndex(x, y, sameSchema) }, indexText,
		func(name, x, y string) { add("index", name, x, y) })
	diffParts(a.ForeignKeys, b.ForeignKeys, keyID(e),
		func(x, y db.ForeignKeyDesign) bool { return sameKey(a, b, x, y) }, foreignKeyText,
		func(name, x, y string) { add("foreign key", name, x, y) })
	diffParts(a.Checks, b.Checks, checkName, sameCheck, func(ch db.CheckDesign) string { return "CHECK (" + ch.Expression + ")" },
		func(name, x, y string) { add("check", name, x, y) })
	if a.Comment != b.Comment {
		add("comment", "", a.Comment, b.Comment)
	}
	return out
}

// diffParts lists the parts, as indexes, of A and B by their names: those
// in one only, and those of a name in both that differ.
func diffParts[T any](a, b []T, name func(T) string, same func(T, T) bool, text func(T) string, add func(name, x, y string)) {
	for _, x := range a {
		i := slices.IndexFunc(b, func(y T) bool { return name(y) == name(x) })
		switch {
		case i < 0:
			add(name(x), text(x), "")
		case !same(x, b[i]):
			add(name(x), text(x), text(b[i]))
		}
	}
	for _, y := range b {
		if !slices.ContainsFunc(a, func(x T) bool { return name(x) == name(y) }) {
			add(name(y), "", text(y))
		}
	}
}

// normal is SQL text as two engines' readings of it compare: in lower
// case, spaced alike.
func normal(s string) string {
	s = strings.Join(strings.Fields(strings.ToLower(s)), " ")
	for _, p := range []string{"(", ")", ","} {
		s = strings.ReplaceAll(strings.ReplaceAll(s, " "+p, p), p+" ", p)
	}
	return s
}

func sameColumn(e db.Engine, a, b db.ColumnDesign) bool {
	return sameType(e, a.Type, b.Type) && a.Nullable == b.Nullable && ownDefault(a) == ownDefault(b) &&
		a.PrimaryKey == b.PrimaryKey && a.AutoIncrement == b.AutoIncrement && a.Comment == b.Comment &&
		sameExtra(a.Extra, b.Extra) && a.Generated == b.Generated
}

// ownDefault is a column's default as compared: a numbered column's
// sequence, as a serial's, numbers it as an identity does.
func ownDefault(c db.ColumnDesign) string {
	def := strings.TrimSpace(c.Default)
	if c.AutoIncrement && strings.HasPrefix(strings.ToLower(def), "nextval(") {
		return ""
	}
	return def
}

var (
	typeNumbers = regexp.MustCompile(`\d+`)
	// charset is MySQL's character set of a column, as Extra writes it.
	charset = regexp.MustCompile(`(?i)^CHARACTER SET \S+ COLLATE \S+$`)
)

// sameType reports whether two types of an engine are one, as varchar(20)
// and character varying(20) are on PostgreSQL: of one kind, of the same
// sizes, signed alike.
func sameType(e db.Engine, a, b string) bool {
	if normal(a) == normal(b) {
		return true
	}
	ka, _, _ := typeKind(e, a)
	kb, _, _ := typeKind(e, b)
	switch {
	case ka == "" || ka != kb:
		return false
	case ka == kindBoolean, e == db.DuckDB && ka == kindVarchar:
		// MySQL's boolean is tinyint(1); DuckDB keeps no length.
		return true
	}
	unsigned := func(s string) bool { return strings.Contains(strings.ToLower(s), "unsigned") }
	return slices.Equal(typeNumbers.FindAllString(a, -1), typeNumbers.FindAllString(b, -1)) && unsigned(a) == unsigned(b)
}

// sameExtra compares what MySQL keeps of columns besides: a character
// set on one side only is the table's, which the other leaves unsaid.
func sameExtra(a, b string) bool {
	if a == "" || b == "" {
		return charset.MatchString(a+b) || a == b
	}
	return normal(a) == normal(b)
}

// sameIndex compares indexes; by their definitions only in one schema,
// which a definition names.
func sameIndex(a, b db.IndexDesign, sameSchema bool) bool {
	if a.Unique != b.Unique || a.Constraint != b.Constraint || !slices.Equal(a.Columns, b.Columns) {
		return false
	}
	return !sameSchema || a.Definition == "" || b.Definition == "" || normal(a.Definition) == normal(b.Definition)
}

// sameKey compares the foreign keys of two tables, a key to its own
// table's schema alike in both.
func sameKey(at, bt db.TableDesign, a, b db.ForeignKeyDesign) bool {
	return slices.Equal(a.Columns, b.Columns) && a.RefTable == b.RefTable && slices.Equal(a.RefColumns, b.RefColumns) &&
		a.OnDelete == b.OnDelete && a.OnUpdate == b.OnUpdate && refSchema(at, a) == refSchema(bt, b)
}

// refSchema is the schema a key points into, "" for its table's own.
func refSchema(t db.TableDesign, fk db.ForeignKeyDesign) string {
	if fk.RefSchema == t.Schema {
		return ""
	}
	return fk.RefSchema
}

func sameCheck(a, b db.CheckDesign) bool { return normal(a.Expression) == normal(b.Expression) }

// keyID is how an engine's foreign keys are known apart: by their names,
// or by what they point at where the engine names keys itself, and for a
// key that has no name.
func keyID(e db.Engine) func(db.ForeignKeyDesign) string {
	return func(fk db.ForeignKeyDesign) string {
		if fk.Name != "" && e != db.SQLite && e != db.DuckDB {
			return fk.Name
		}
		return "(" + strings.Join(fk.Columns, ", ") + ") → " + fk.RefTable + " (" + strings.Join(fk.RefColumns, ", ") + ")"
	}
}

func columnText(c db.ColumnDesign) string {
	parts := []string{c.Type}
	if c.Extra != "" {
		parts = append(parts, c.Extra)
	}
	if !c.Nullable {
		parts = append(parts, "NOT NULL")
	}
	if c.Default != "" {
		parts = append(parts, "DEFAULT "+c.Default)
	}
	if c.AutoIncrement {
		parts = append(parts, "numbered")
	}
	if c.Generated {
		parts = append(parts, "computed")
	}
	if c.PrimaryKey {
		parts = append(parts, "PRIMARY KEY")
	}
	if c.Comment != "" {
		parts = append(parts, sqltext.LineComment(c.Comment))
	}
	return strings.Join(parts, " ")
}

func keyText(t db.TableDesign) string {
	var cols []string
	for _, c := range t.Columns {
		if c.PrimaryKey {
			cols = append(cols, c.Name)
		}
	}
	if len(cols) == 0 {
		return ""
	}
	return "(" + strings.Join(cols, ", ") + ")"
}

func indexText(ix db.IndexDesign) string {
	if ix.Definition != "" {
		return ix.Definition
	}
	s := "(" + strings.Join(ix.Columns, ", ") + ")"
	if ix.Unique {
		s = "UNIQUE " + s
	}
	return s
}

func foreignKeyText(fk db.ForeignKeyDesign) string {
	ref := fk.RefTable
	if fk.RefSchema != "" {
		ref = fk.RefSchema + "." + ref
	}
	s := "(" + strings.Join(fk.Columns, ", ") + ") → " + ref + " (" + strings.Join(fk.RefColumns, ", ") + ")"
	if fk.OnDelete != "" {
		s += " ON DELETE " + fk.OnDelete
	}
	if fk.OnUpdate != "" {
		s += " ON UPDATE " + fk.OnUpdate
	}
	return s
}

// Migration writes the statements making B, a database's schema read as a
// model, like A: A's tables B lacks made in schema, B's tables changed,
// and what B has that A lacks dropped, tables and their parts, only when
// drop is set; else kept. On PostgreSQL and MySQL the statements go in
// an order every one of them can run in, whatever the keys between the
// tables: keys dropped first, then tables, then the tables changed, the
// new ones made, and keys added last. A table whose change the engine
// cannot make is noted, and left as it is.
func Migration(a, b *Model, schema string, drop bool) (db.SchemaChange, []string, error) {
	if a.Engine != b.Engine {
		return db.SchemaChange{}, nil, errors.New("a migration is between models of one engine")
	}
	e := b.Engine
	d := db.DialectOf(e)
	n := notes{}
	pairs, onlyB := match(a, b)
	var made []db.TableDesign
	type change struct{ was, now db.TableDesign }
	var changed []change
	for _, p := range pairs {
		if p.b == nil {
			made = append(made, convert(e, e, fresh(e, moved(p.a, schema), &n), p.a.Schema == schema, &n))
		} else if len(diffTable(e, p.a, *p.b)) > 0 {
			now := target(e, convert(e, e, moved(p.a, p.b.Schema), p.a.Schema == p.b.Schema, &n), *p.b, drop)
			// A table the engine cannot change is left whole as it is:
			// its keys neither dropped nor added.
			if _, err := db.AlterTableChange(d, *p.b, now); err != nil {
				n.add(fmt.Sprintf("%s is not changed: %v.", p.b.Name, err))
				continue
			}
			changed = append(changed, change{*p.b, now})
		}
	}
	var dropped []db.TableDesign
	if drop {
		// Those pointing at others first.
		dropped, _ = createOrder(onlyB, false)
		slices.Reverse(dropped)
	}
	ch := db.SchemaChange{Atomic: e.TransactionalDDL()}
	alter := func(was, now db.TableDesign) {
		steps, err := db.AlterTableChange(d, was, now)
		switch {
		case err != nil:
			n.add(fmt.Sprintf("%s is not changed: %v.", was.Name, err))
		case len(steps.Steps) == 0 && len(diffTable(e, now, was)) > 0:
			n.add(fmt.Sprintf("%s differs in what a migration does not change, as a column's character set or computation: change it in SQL.", was.Name))
		default:
			ch.Steps = append(ch.Steps, steps.Steps...)
		}
	}
	create := func(t db.TableDesign) {
		created, err := db.NewTableChange(d, t)
		if err != nil {
			n.add(fmt.Sprintf("%s is not made: %v.", t.Name, err))
			return
		}
		ch.Steps = append(ch.Steps, created.Steps...)
	}
	dropTables := func() {
		for _, t := range dropped {
			ch.Steps = append(ch.Steps, db.Step{SQL: "DROP TABLE " + db.QualifiedName(d, t.Schema, t.Name)})
		}
	}
	if e != db.Postgres && e != db.MySQL {
		// SQLite makes a table again for a change of its keys, and takes a
		// key to a table not made yet; DuckDB changes no keys of a table
		// made: the tables dropped first, which a table changed may
		// depend on, then each table changes whole, the new ones made in
		// order.
		dropTables()
		ordered, closing := createOrder(made, e != db.SQLite)
		for _, t := range ordered {
			create(t)
		}
		for _, c := range changed {
			alter(c.was, c.now)
		}
		for _, k := range closing {
			n.add(fmt.Sprintf("%s adds no key to a table made, and %s's to %s closes a cycle of keys: it is left out.", e.Label(), k.table.Name, k.key.RefTable))
		}
		return ch, n.list, nil
	}

	// The keys dropped, of the tables changed and of those dropped.
	for i, c := range changed {
		kept := withKeys(c.was, func(fk db.ForeignKeyDesign) bool {
			return slices.ContainsFunc(c.now.ForeignKeys, func(k db.ForeignKeyDesign) bool { return k.Read && k.Name == fk.Name })
		})
		alter(c.was, kept)
		changed[i].was = kept
	}
	for _, t := range dropped {
		alter(t, withKeys(t, func(db.ForeignKeyDesign) bool { return false }))
	}
	dropTables()
	// The tables changed, but for the keys they gain.
	var adding []closingKey
	for _, c := range changed {
		now := clone(c.now)
		now.ForeignKeys = slices.DeleteFunc(now.ForeignKeys, func(fk db.ForeignKeyDesign) bool {
			if !fk.Read {
				adding = append(adding, closingKey{c.now, fk})
			}
			return !fk.Read
		})
		alter(c.was, now)
	}
	// The new tables, their keys after.
	for _, t := range made {
		for _, fk := range t.ForeignKeys {
			adding = append(adding, closingKey{t, fk})
		}
		t.ForeignKeys = nil
		create(t)
	}
	for _, k := range adding {
		ch.Steps = append(ch.Steps, db.Step{SQL: db.AddForeignKeySQL(d, k.table, k.key)})
	}
	return ch, n.list, nil
}

// withKeys is a table read, as AlterTableChange takes the change of it:
// every part of it kept, and of its foreign keys those keep takes.
func withKeys(t db.TableDesign, keep func(db.ForeignKeyDesign) bool) db.TableDesign {
	t = clone(t)
	for i := range t.Columns {
		t.Columns[i].Was = t.Columns[i].Name
	}
	for i := range t.Indexes {
		t.Indexes[i].Read = true
	}
	for i := range t.Checks {
		t.Checks[i].Read = true
	}
	t.ForeignKeys = slices.DeleteFunc(t.ForeignKeys, func(fk db.ForeignKeyDesign) bool { return !keep(fk) })
	for i := range t.ForeignKeys {
		t.ForeignKeys[i].Read = true
	}
	return t
}

// indexID is how an engine's indexes are known apart: by their names, or
// SQLite's unique constraints by their columns, which SQLite names by
// their place in the table.
func indexID(e db.Engine) func(db.IndexDesign) string {
	return func(ix db.IndexDesign) string {
		if e == db.SQLite && ix.Constraint {
			return "UNIQUE (" + strings.Join(ix.Columns, ", ") + ")"
		}
		return ix.Name
	}
}

// target is B's table made like A's, as AlterTableChange takes it: B's
// columns and parts that A has kept, A's others added; and B's that A
// lacks kept unless drop is set, its primary key too when A has none.
func target(e db.Engine, a, b db.TableDesign, drop bool) db.TableDesign {
	keyName, indexName := keyID(e), indexID(e)
	now := clone(a)
	now.Schema, now.Name, now.PrimaryKeyName = b.Schema, b.Name, b.PrimaryKeyName
	keyless := !slices.ContainsFunc(now.Columns, func(c db.ColumnDesign) bool { return c.PrimaryKey })
	for i, c := range now.Columns {
		j := slices.IndexFunc(b.Columns, func(o db.ColumnDesign) bool { return o.Name == c.Name })
		if j < 0 {
			continue
		}
		now.Columns[i].Was = c.Name
		if ownDefault(c) == ownDefault(b.Columns[j]) {
			// A sequence numbering both, which may be named otherwise, as
			// in another schema: B's stays.
			now.Columns[i].Default = b.Columns[j].Default
		}
		if keyless && !drop {
			now.Columns[i].PrimaryKey = b.Columns[j].PrimaryKey
		}
	}
	// A part kept takes B's name and text, by which the change knows it
	// from one dropped: a key or a SQLite constraint matched by what it
	// is may be named otherwise.
	for i, ix := range now.Indexes {
		j := slices.IndexFunc(b.Indexes, func(o db.IndexDesign) bool { return indexName(o) == indexName(ix) && sameIndex(ix, o, true) })
		if j >= 0 {
			now.Indexes[i] = b.Indexes[j]
			now.Indexes[i].Read = true
		}
	}
	for i, fk := range now.ForeignKeys {
		j := slices.IndexFunc(b.ForeignKeys, func(o db.ForeignKeyDesign) bool { return keyName(o) == keyName(fk) && sameKey(now, b, fk, o) })
		if j >= 0 {
			now.ForeignKeys[i] = b.ForeignKeys[j]
			now.ForeignKeys[i].Read = true
		}
	}
	for i, c := range now.Checks {
		j := slices.IndexFunc(b.Checks, func(o db.CheckDesign) bool { return checkName(o) == checkName(c) && sameCheck(c, o) })
		if j >= 0 {
			now.Checks[i] = b.Checks[j]
			now.Checks[i].Read = true
		}
	}
	if drop {
		return now
	}
	for _, c := range b.Columns {
		if !slices.ContainsFunc(now.Columns, func(o db.ColumnDesign) bool { return o.Name == c.Name }) {
			// Kept as it is; in the key only when A has none.
			c.Was, c.PrimaryKey = c.Name, c.PrimaryKey && keyless
			now.Columns = append(now.Columns, c)
		}
	}
	for _, ix := range b.Indexes {
		if !slices.ContainsFunc(now.Indexes, func(o db.IndexDesign) bool { return indexName(o) == indexName(ix) }) {
			ix.Read = true
			now.Indexes = append(now.Indexes, ix)
		}
	}
	for _, fk := range b.ForeignKeys {
		if !slices.ContainsFunc(now.ForeignKeys, func(o db.ForeignKeyDesign) bool { return keyName(o) == keyName(fk) }) {
			fk.Read = true
			now.ForeignKeys = append(now.ForeignKeys, fk)
		}
	}
	for _, c := range b.Checks {
		if !slices.ContainsFunc(now.Checks, func(o db.CheckDesign) bool { return checkName(o) == checkName(c) }) {
			c.Read = true
			now.Checks = append(now.Checks, c)
		}
	}
	return now
}

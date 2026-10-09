package dataview

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Sections of the table form.
const (
	designColumns = iota
	designIndexes
	designKeys
	designChecks
)

// designer is the table form: it changes a design of a table, a new
// one's or one read, and applies it once the user has reviewed its SQL;
// or, for a data model, saves it to the model.
type designer struct {
	a        Host
	conn     *connection.Conn // nil for a model's table
	database string
	engine   db.Engine
	// tables are the tables a foreign key may point at, and columns a
	// table's columns.
	tables  func() []string
	columns func(table string) []string
	// save keeps the design in a model, nil when it is applied to a
	// database.
	save func(db.TableDesign) error
	// was is the design read, nil for a new table; first is what the
	// form started from.
	was   *db.TableDesign
	first db.TableDesign
	now   db.TableDesign

	section                  int
	cols, indexes, fks, chks rowKeys
	// actions are the labels the action selects of new foreign keys show,
	// by row key: a select writes its choice where it was given.
	actions  map[int]*[2]string
	applying bool
	cancel   func()
	// applied hears the design made, on the main thread.
	applied func(db.TableDesign)
}

// rowKeys keeps keys for the rows of a list, which stay with their row
// as rows above it come and go, and so does what a row's inputs hold.
type rowKeys struct {
	keys []int
	next int
}

func newRowKeys(n int) rowKeys {
	var k rowKeys
	for range n {
		k.add()
	}
	return k
}

func (k *rowKeys) add() {
	k.next++
	k.keys = append(k.keys, k.next)
}

func (k *rowKeys) remove(i int) { k.keys = slices.Delete(k.keys, i, i+1) }

func (k *rowKeys) key(prefix string, i int) string { return fmt.Sprint(prefix, k.keys[i]) }

func newDesigner(a Host, cn *connection.Conn, database string, was *db.TableDesign, start db.TableDesign, applied func(db.TableDesign)) *designer {
	d := &designer{a: a, conn: cn, database: database, engine: cn.Config.Engine, was: was, first: cloneDesign(start), applied: applied}
	d.tables = func() []string {
		var out []string
		for _, o := range cn.Objects[connection.SchemaKey{Database: database, Schema: d.now.Schema}] {
			if o.Kind == db.KindTable {
				out = append(out, o.Name)
			}
		}
		return out
	}
	d.columns = func(table string) []string {
		var out []string
		for _, col := range cn.Columns[connection.ObjectKey{Database: database, Schema: d.now.Schema, Name: table}] {
			out = append(out, col.Name)
		}
		return out
	}
	d.discard()
	return d
}

// TableForm is the table form for a table of a data model.
type TableForm struct{ d *designer }

// NewModelTableForm opens the table form on a table of a data model of
// an engine: tables and columns are what its keys may point at, and save
// keeps the design in the model. An index the engine wrote, which the
// model keeps as written, is kept or dropped, not changed.
func NewModelTableForm(a Host, e db.Engine, t db.TableDesign, tables func() []string, columns func(table string) []string, save func(db.TableDesign) error) *TableForm {
	t = cloneDesign(t)
	for i := range t.Indexes {
		t.Indexes[i].Read = t.Indexes[i].Definition != ""
	}
	d := &designer{a: a, engine: e, tables: tables, columns: columns, save: save, first: t}
	d.discard()
	return &TableForm{d}
}

// Changed reports whether the form holds what the model does not.
func (f *TableForm) Changed() bool { return f.d.changed() }

func (f *TableForm) View(c *ui.Context) { f.d.View(c) }

// discard puts the form back as it started.
func (d *designer) discard() {
	start := d.first
	d.now = cloneDesign(start)
	d.actions = map[int]*[2]string{}
	d.cols, d.indexes = newRowKeys(len(start.Columns)), newRowKeys(len(start.Indexes))
	d.fks, d.chks = newRowKeys(len(start.ForeignKeys)), newRowKeys(len(start.Checks))
}

// cloneDesign copies a design, so that changing one leaves the other.
func cloneDesign(t db.TableDesign) db.TableDesign {
	t.Columns = slices.Clone(t.Columns)
	t.Indexes = slices.Clone(t.Indexes)
	t.ForeignKeys = slices.Clone(t.ForeignKeys)
	t.Checks = slices.Clone(t.Checks)
	for i := range t.Indexes {
		t.Indexes[i].Columns = slices.Clone(t.Indexes[i].Columns)
	}
	for i := range t.ForeignKeys {
		t.ForeignKeys[i].Columns = slices.Clone(t.ForeignKeys[i].Columns)
		t.ForeignKeys[i].RefColumns = slices.Clone(t.ForeignKeys[i].RefColumns)
	}
	return t
}

// changed reports whether the form holds what it did not start from.
func (d *designer) changed() bool { return !reflect.DeepEqual(d.first, d.now) }

// change is the statements making the design.
func (d *designer) change() (db.SchemaChange, error) {
	dialect := db.DialectOf(d.engine)
	if d.was == nil {
		return db.NewTableChange(dialect, d.now)
	}
	return db.AlterTableChange(dialect, *d.was, d.now)
}

// fixed says why an engine cannot change keys or checks of a table it
// made, "" when it can.
func (d *designer) fixed() string {
	if d.was == nil {
		return ""
	}
	switch d.engine {
	case db.DuckDB:
		return "DuckDB cannot change the keys or checks of a table it made."
	case db.ClickHouse:
		return "A ClickHouse table's sorting key is fixed when it is made."
	}
	return ""
}

func (d *designer) apply() {
	ch, err := d.change()
	if err != nil || len(ch.Steps) == 0 {
		return
	}
	if d.save != nil {
		if err := d.save(d.now); err != nil {
			d.a.ShowError("Could not save the model", err.Error())
			return
		}
		d.first = cloneDesign(d.now)
		return
	}
	verb, title := "change", "Change table "+d.now.Name+"?"
	if d.was == nil {
		verb, title = "create", "Create table "+d.now.Name+"?"
	}
	ApplyChange(d.a, d.conn, d.database, title, ch, "Review the statements before they run.",
		func(cancel func()) { d.applying, d.cancel = true, cancel },
		func(err error) {
			d.applying, d.cancel = false, nil
			if err != nil {
				d.a.ShowError("Could not "+verb+" "+d.now.Name, err.Error())
				return
			}
			d.conn.ForgetCatalog()
			d.applied(d.now)
		})
}

func (d *designer) View(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	e := d.engine
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(10, 16).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			name := ui.TextInput(c, &d.now.Name).Placeholder("table name").Label("Table name").Font(widgets.MonoFont).Width(240)
			if d.was == nil {
				name.AutoFocus()
			}
			if e != db.SQLite {
				ui.TextInput(c, &d.now.Comment).Placeholder("comment").Label("Table comment").Grow(1)
			} else {
				ui.Spacer(c)
			}
			if e != db.ClickHouse {
				ui.Segmented(c, &d.section, "Columns", "Indexes", "Foreign Keys", "Checks").Label("Section")
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(12, 16).Gap(8).Children(func() {
				switch d.section {
				case designColumns:
					d.columnsView(c)
				case designIndexes:
					d.indexesView(c)
				case designKeys:
					d.keysView(c)
				case designChecks:
					d.checksView(c)
				}
			})
		})
		ch, err := d.change()
		ui.Row(c).Padding(8, 16).Gap(10).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
			switch {
			case d.applying:
				ui.Spinner(c).Size(14, 14)
				ui.Text(c, "Applying…").TextColor(pal.Muted)
				c.After(200 * time.Millisecond)
			case err != nil && d.changed():
				ui.Icon(c, widgets.IconAlert).TextColor(th.Danger).FontSize(13)
				ui.Text(c, widgets.Sentence(err.Error())).TextColor(th.Danger).FontSize(12.5).Shrink(1)
			case len(ch.Steps) > 0 && d.save == nil:
				ui.Text(c, fmt.Sprintf("%d statement%s to run", len(ch.Statements()), widgets.Plural(len(ch.Statements())))).
					FontSize(12.5).TextColor(pal.Muted)
			}
			ui.Spacer(c)
			if d.applying {
				if ui.Button(c, "Cancel").Clicked() && d.cancel != nil {
					d.cancel()
				}
				return
			}
			if (d.was != nil || d.save != nil) && ui.Button(c, "Discard Changes").Disabled(!d.changed()).Clicked() {
				d.discard()
			}
			if d.save != nil {
				if ui.PrimaryButton(c, "Save to Model").Disabled(err != nil || !d.changed()).Clicked() {
					d.apply()
				}
				return
			}
			if ui.PrimaryButton(c, "Review SQL…").Disabled(err != nil || len(ch.Steps) == 0).Tooltip("See the statements, then apply them").Clicked() {
				d.apply()
			}
		})
	})
}

func (d *designer) columnsView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	e := d.engine
	keyFixed := d.fixed() != ""
	autoAllowed := e == db.Postgres || e == db.MySQL || e == db.SQLite
	header := func(text string, width float32) {
		t := ui.Text(c, text).FontSize(11.5).Bold().TextColor(pal.Muted)
		if width > 0 {
			t.Width(width)
		} else {
			t.Grow(1)
		}
	}
	ui.Row(c).Gap(8).PaddingX(4).Children(func() {
		header("Name", 170)
		header("Type", 190)
		header("Null", 44)
		header("Default", 160)
		header("Key", 40)
		if autoAllowed {
			header("Auto", 44)
		}
		if e != db.SQLite {
			header("Comment", 0)
		} else {
			ui.Spacer(c)
		}
		ui.Box(c).Width(26)
	})
	types := db.CommonTypes(e)
	remove := -1
	for i := range d.now.Columns {
		col := &d.now.Columns[i]
		ui.Row(c.Key(d.cols.key("column-", i))).Gap(8).PaddingX(4).AlignItems(ui.Center).Children(func() {
			name := ui.TextInput(c, &col.Name).Font(widgets.MonoFont).Width(170).Label("Column name")
			if col.Was != "" && col.Was != col.Name {
				name.Tooltip("Renamed from " + col.Was)
			}
			ui.Autocomplete(c, &col.Type, types).Font(widgets.MonoFont).Width(190).Label("Type of " + col.Name).Disabled(col.Generated)
			ui.Box(c).Width(44).Children(func() {
				ui.Checkbox(c, &col.Nullable, "").Label("Null").Disabled(col.PrimaryKey || col.Generated)
			})
			ui.TextInput(c, &col.Default).Placeholder("none").Font(widgets.MonoFont).Width(160).Label("Default of " + col.Name)
			ui.Box(c).Width(40).Children(func() {
				if ui.Checkbox(c, &col.PrimaryKey, "").Label("Primary key").Disabled(keyFixed).Changed() && col.PrimaryKey {
					col.Nullable = false // a key holds no NULL
				}
			})
			if autoAllowed {
				ui.Box(c).Width(44).Children(func() {
					// Whether an existing column numbers its rows changes in SQL.
					ui.Checkbox(c, &col.AutoIncrement, "").Label("Numbers its rows").Disabled(col.Was != "")
				})
			}
			if e != db.SQLite {
				ui.TextInput(c, &col.Comment).Placeholder("comment").Grow(1).Label("Comment of " + col.Name)
			} else {
				ui.Spacer(c)
			}
			if widgets.IconButton(c, widgets.IconTrash, "Drop "+col.Name).Clicked() {
				remove = i
			}
		})
	}
	if remove >= 0 {
		d.now.Columns = slices.Delete(d.now.Columns, remove, remove+1)
		d.cols.remove(remove)
	}
	ui.Row(c).Gap(8).PaddingY(4).Children(func() {
		if widgets.ToolButton(c, widgets.IconPlus, "Add Column", "Add a column at the end").Clicked() {
			typ := ""
			if len(types) > 0 {
				typ = types[0]
			}
			d.now.Columns = append(d.now.Columns, db.ColumnDesign{Name: fmt.Sprint("column", len(d.now.Columns)+1), Type: typ, Nullable: true})
			d.cols.add()
		}
	})
	if d.was != nil {
		var dropped []db.ColumnDesign
		for _, w := range d.was.Columns {
			if !slices.ContainsFunc(d.now.Columns, func(c db.ColumnDesign) bool { return c.Was == w.Name }) {
				dropped = append(dropped, w)
			}
		}
		droppedRow(c, "Columns dropped", len(dropped), func(i int) string { return dropped[i].Name }, func(i int) {
			d.now.Columns = append(d.now.Columns, dropped[i])
			d.cols.add()
		})
	}
}

// droppedRow lists what the form dropped of what was read, each with a
// button bringing it back.
func droppedRow(c *ui.Context, title string, n int, name func(int) string, restore func(int)) {
	if n == 0 {
		return
	}
	pal := widgets.PaletteOf(c)
	ui.Row(c).Gap(8).PaddingY(6).Wrap().Children(func() {
		ui.Text(c, title+":").FontSize(12).TextColor(pal.Muted)
		for i := range n {
			if ui.Link(c, "Restore "+name(i), "").Clicked() {
				restore(i)
			}
		}
	})
}

func (d *designer) columnNames() []string {
	names := make([]string, len(d.now.Columns))
	for i, c := range d.now.Columns {
		names[i] = c.Name
	}
	return names
}

func (d *designer) indexesView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	if d.engine == db.ClickHouse {
		return
	}
	remove := -1
	for i := range d.now.Indexes {
		ix := &d.now.Indexes[i]
		ui.Row(c.Key(d.indexes.key("index-", i))).Gap(8).AlignItems(ui.Center).Children(func() {
			if ix.Read {
				ui.Text(c, ix.Name).Bold().Font(widgets.MonoFont).SingleLine().Shrink(1)
				ui.Text(c, "("+strings.Join(ix.Columns, ", ")+")").TextColor(pal.Muted).SingleLine().Grow(1).Shrink(1)
				switch {
				case ix.Constraint:
					ui.Badge(c, "UNIQUE CONSTRAINT")
				case ix.Unique:
					ui.Badge(c, "UNIQUE")
				}
			} else {
				ui.TextInput(c, &ix.Name).Font(widgets.MonoFont).Width(200).Label("Index name")
				ui.TokenField(c, &ix.Columns, d.columnNames()).Grow(1).Label("Columns of " + ix.Name)
				ui.Checkbox(c, &ix.Unique, "Unique")
			}
			if widgets.IconButton(c, widgets.IconTrash, "Drop "+ix.Name).Clicked() {
				remove = i
			}
		})
	}
	if remove >= 0 {
		d.now.Indexes = slices.Delete(d.now.Indexes, remove, remove+1)
		d.indexes.remove(remove)
	}
	if widgets.ToolButton(c, widgets.IconPlus, "Add Index", "Add an index of columns").Clicked() {
		d.now.Indexes = append(d.now.Indexes, db.IndexDesign{Name: d.freeName("idx", func(n string) bool {
			return slices.ContainsFunc(d.now.Indexes, func(ix db.IndexDesign) bool { return ix.Name == n })
		})})
		d.indexes.add()
	}
	if d.was != nil {
		var dropped []db.IndexDesign
		for _, ix := range d.was.Indexes {
			if !slices.ContainsFunc(d.now.Indexes, func(n db.IndexDesign) bool { return n.Read && n.Name == ix.Name }) {
				dropped = append(dropped, ix)
			}
		}
		droppedRow(c, "Indexes dropped", len(dropped), func(i int) string { return dropped[i].Name }, func(i int) {
			d.now.Indexes = append(d.now.Indexes, dropped[i])
			d.indexes.add()
		})
	}
}

// freeName is a name for a new object of the table, ending with suffix,
// that taken does not refuse.
func (d *designer) freeName(suffix string, taken func(string) bool) string {
	base := d.now.Name + "_" + suffix
	name := base
	for n := 2; taken(name); n++ {
		name = fmt.Sprint(base, n)
	}
	return name
}

// actionLabels name the referential actions, the default first.
var actionLabels = []string{"Default", "Restrict", "Cascade", "Set null", "Set default"}

func actionOf(label string) string {
	switch label {
	case "Restrict":
		return "RESTRICT"
	case "Cascade":
		return "CASCADE"
	case "Set null":
		return "SET NULL"
	case "Set default":
		return "SET DEFAULT"
	}
	return ""
}

func actionLabel(action string) string {
	for _, l := range actionLabels {
		if actionOf(l) == action {
			return l
		}
	}
	return actionLabels[0]
}

func (d *designer) keysView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	fixed := d.fixed()
	if fixed != "" {
		ui.Text(c, fixed).FontSize(12).TextColor(pal.Muted)
	}
	tables := d.tables()
	remove := -1
	for i := range d.now.ForeignKeys {
		fk := &d.now.ForeignKeys[i]
		ui.Row(c.Key(d.fks.key("fk-", i))).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
			if fk.Read {
				ui.Text(c, fk.Name).Bold().Font(widgets.MonoFont).SingleLine()
				ui.Text(c, fk.Definition).Font(widgets.MonoFont).FontSize(12).TextColor(pal.Muted).SingleLine().Grow(1).Shrink(1)
			} else {
				ui.TextInput(c, &fk.Name).Placeholder("name").Font(widgets.MonoFont).Width(170).Label("Foreign key name")
				ui.TokenField(c, &fk.Columns, d.columnNames()).Width(180).Label("Columns")
				ui.Text(c, "→").TextColor(pal.Muted)
				ui.Autocomplete(c, &fk.RefTable, tables).Placeholder("table").Font(widgets.MonoFont).Width(160).Label("Table it points at")
				ui.TokenField(c, &fk.RefColumns, d.columns(fk.RefTable)).Width(160).Label("Columns it points at")
				labels := d.actions[d.fks.keys[i]]
				if labels == nil {
					labels = &[2]string{actionLabel(fk.OnDelete), actionLabel(fk.OnUpdate)}
					d.actions[d.fks.keys[i]] = labels
				}
				ui.Text(c, "on delete").FontSize(12).TextColor(pal.Muted)
				ui.Select(c, &labels[0], actionLabels).Label("On delete")
				ui.Text(c, "on update").FontSize(12).TextColor(pal.Muted)
				ui.Select(c, &labels[1], actionLabels).Label("On update")
				fk.OnDelete, fk.OnUpdate = actionOf(labels[0]), actionOf(labels[1])
			}
			if widgets.IconButton(c, widgets.IconTrash, "Drop "+fk.Name).Disabled(fixed != "" && fk.Read).Clicked() {
				remove = i
			}
		})
	}
	if remove >= 0 {
		d.now.ForeignKeys = slices.Delete(d.now.ForeignKeys, remove, remove+1)
		d.fks.remove(remove)
	}
	if fixed == "" && widgets.ToolButton(c, widgets.IconPlus, "Add Foreign Key", "Point columns at another table's").Clicked() {
		d.now.ForeignKeys = append(d.now.ForeignKeys, db.ForeignKeyDesign{RefSchema: d.now.Schema, Name: d.freeName("fkey", func(n string) bool {
			return slices.ContainsFunc(d.now.ForeignKeys, func(fk db.ForeignKeyDesign) bool { return fk.Name == n })
		})})
		d.fks.add()
	}
	if d.was != nil {
		var dropped []db.ForeignKeyDesign
		for _, fk := range d.was.ForeignKeys {
			if !slices.ContainsFunc(d.now.ForeignKeys, func(n db.ForeignKeyDesign) bool { return n.Read && n.Name == fk.Name }) {
				dropped = append(dropped, fk)
			}
		}
		droppedRow(c, "Foreign keys dropped", len(dropped), func(i int) string { return dropped[i].Name }, func(i int) {
			d.now.ForeignKeys = append(d.now.ForeignKeys, dropped[i])
			d.fks.add()
		})
	}
}

func (d *designer) checksView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	fixed := d.fixed()
	if fixed != "" {
		ui.Text(c, fixed).FontSize(12).TextColor(pal.Muted)
	}
	remove := -1
	for i := range d.now.Checks {
		ch := &d.now.Checks[i]
		ui.Row(c.Key(d.chks.key("check-", i))).Gap(8).AlignItems(ui.Center).Children(func() {
			if ch.Read {
				name := ch.Name
				if name == "" {
					name = "unnamed"
				}
				ui.Text(c, name).Bold().Font(widgets.MonoFont).SingleLine()
				ui.Text(c, ch.Expression).Font(widgets.MonoFont).FontSize(12).TextColor(pal.Muted).SingleLine().Grow(1).Shrink(1)
			} else {
				ui.TextInput(c, &ch.Name).Placeholder("name").Font(widgets.MonoFont).Width(200).Label("Check name")
				ui.TextInput(c, &ch.Expression).Placeholder("price > 0").Font(widgets.MonoFont).Grow(1).Label("Check expression")
			}
			if widgets.IconButton(c, widgets.IconTrash, "Drop the check").Disabled(fixed != "" && ch.Read).Clicked() {
				remove = i
			}
		})
	}
	if remove >= 0 {
		d.now.Checks = slices.Delete(d.now.Checks, remove, remove+1)
		d.chks.remove(remove)
	}
	if fixed == "" && widgets.ToolButton(c, widgets.IconPlus, "Add Check", "Add a condition every row must meet").Clicked() {
		d.now.Checks = append(d.now.Checks, db.CheckDesign{Name: d.freeName("check", func(n string) bool {
			return slices.ContainsFunc(d.now.Checks, func(ch db.CheckDesign) bool { return ch.Name == n })
		})})
		d.chks.add()
	}
	if d.was != nil {
		var dropped []db.CheckDesign
		for _, ch := range d.was.Checks {
			if !slices.ContainsFunc(d.now.Checks, func(n db.CheckDesign) bool { return n.Read && n.Name == ch.Name && n.Expression == ch.Expression }) {
				dropped = append(dropped, ch)
			}
		}
		droppedRow(c, "Checks dropped", len(dropped), func(i int) string { return dropped[i].Expression }, func(i int) {
			d.now.Checks = append(d.now.Checks, dropped[i])
			d.chks.add()
		})
	}
}

// TableDesignTab designs a table to create in a schema; once made, the
// table's own tab takes its place.
type TableDesignTab struct {
	a        Host
	Conn     *connection.Conn
	Database string
	Schema   string
	design   *designer
}

// OpenNewTable opens the form making a table in a schema.
func OpenNewTable(a Host, cn *connection.Conn, database, schema string) {
	a.Connect(cn, func() {
		t := &TableDesignTab{a: a, Conn: cn, Database: database, Schema: schema}
		start := db.TableDesign{Schema: schema, Columns: []db.ColumnDesign{FirstColumn(cn.Config.Engine)}}
		t.design = newDesigner(a, cn, database, nil, start, func(made db.TableDesign) {
			obj := db.Object{Schema: made.Schema, Name: made.Name, Kind: db.KindTable, Rows: -1, Bytes: -1, Comment: made.Comment}
			a.ReplaceTab(t, NewTableTab(a, cn, database, obj, PageStructure))
		})
		a.AddTab(t)
	})
}

// FirstColumn is the key column a new table starts with: one numbering
// its rows where the engine does.
func FirstColumn(e db.Engine) db.ColumnDesign {
	c := db.ColumnDesign{Name: "id", PrimaryKey: true}
	switch e {
	case db.Postgres, db.MySQL:
		c.Type, c.AutoIncrement = "bigint", true
	case db.SQLite:
		c.Type = "INTEGER" // the rowid, numbered on its own
	case db.DuckDB:
		c.Type = "BIGINT"
	case db.ClickHouse:
		c.Type = "UInt64"
	}
	return c
}

func (t *TableDesignTab) Title() string {
	if t.design.now.Name != "" {
		return t.design.now.Name + " (new)"
	}
	return "New table"
}

func (t *TableDesignTab) Connection() *connection.Conn { return t.Conn }

func (t *TableDesignTab) CloseReason() string {
	if t.design.changed() {
		return "The table has not been created. Closing discards its design."
	}
	return ""
}

func (t *TableDesignTab) Close() {
	if t.design.cancel != nil {
		t.design.cancel()
	}
}

func (t *TableDesignTab) View(c *ui.Context) { t.design.View(c) }

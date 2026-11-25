package dataview

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/lists"
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

// appendRow adds a row at the end of a list, with a key of its own.
func appendRow[T any](list *[]T, keys *rowKeys, row T) {
	*list = append(*list, row)
	keys.add()
}

// deleteRow takes the row at i out of a list, with its key.
func deleteRow[T any](list *[]T, keys *rowKeys, i int) {
	*list = slices.Delete(*list, i, i+1)
	keys.remove(i)
}

// deleteRowsFunc takes out the rows del reports, from the last, with
// their keys.
func deleteRowsFunc[T any](list *[]T, keys *rowKeys, del func(T) bool) {
	for i := len(*list) - 1; i >= 0; i-- {
		if del((*list)[i]) {
			deleteRow(list, keys, i)
		}
	}
}

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
	if r, ok := db.SQL(d.engine); ok {
		return r.FixedKeysNote()
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
			d.a.ShowReport(widgets.ErrorReportOf("Could not save the model", err))
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
				d.a.ShowReport(widgets.ErrorReportOf("Could not "+verb+" "+d.now.Name, err))
				return
			}
			d.conn.ForgetCatalog()
			d.applied(d.now)
		})
}

func (d *designer) View(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	r, ok := db.SQL(d.engine)
	keepsComments, hasIndexes := !ok || r.HasComments(), !ok || r.HasSecondaryIndexes()
	ui.Column(c).Grow(1).Children(func() {
		widgets.Bar(c, "Table design", func() {
			name := ui.TextInput(c, &d.now.Name).Placeholder("table name").Label("Table name").Font(widgets.MonoFont).Width(240)
			if d.was == nil {
				name.AutoFocus()
			}
			if keepsComments {
				ui.TextInput(c, &d.now.Comment).Placeholder("comment").Label("Table comment").Grow(1)
			} else {
				ui.Spacer(c)
			}
			if hasIndexes {
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
		ui.Row(c).Padding(widgets.PadSectionY, widgets.PadSectionX).Gap(10).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
			switch {
			case d.applying:
				widgets.Appear(ui.Text(c.Key("applying"), "Applying…").TextColor(pal.Muted))
			case err != nil && d.changed():
				ui.Icon(c, widgets.IconAlert).TextColor(th.Danger).FontSize(widgets.IconMedium)
				ui.Text(c, widgets.Sentence(err.Error())).TextColor(th.Danger).FontSize(widgets.TextBody).Shrink(1)
			case len(ch.Steps) > 0 && d.save == nil:
				ui.Text(c, fmt.Sprintf("%d statement%s to run", len(ch.Statements()), widgets.Plural(len(ch.Statements())))).
					FontSize(widgets.TextBody).TextColor(pal.Muted)
			}
			ui.Spacer(c)
			if d.applying {
				if widgets.Appear(ui.Button(c.Key("cancel"), "Cancel")).Clicked() && d.cancel != nil {
					d.cancel()
				}
				return
			}
			if (d.was != nil || d.save != nil) && widgets.Clicked(widgets.Appear(ui.Button(c.Key("discard"), "Discard Changes")), widgets.When(!d.changed(), "Nothing has changed")) {
				d.discard()
			}
			if d.save != nil {
				if widgets.Clicked(widgets.Appear(ui.PrimaryButton(c.Key("main"), "Save to Model")), cmp.Or(widgets.WhyNot(err), widgets.When(!d.changed(), "Nothing has changed"))) {
					d.apply()
				}
				return
			}
			if widgets.Clicked(widgets.Appear(ui.PrimaryButton(c.Key("main"), "Review SQL…")).Tooltip("See the statements, then apply them"), cmp.Or(widgets.WhyNot(err), widgets.When(len(ch.Steps) == 0, "Nothing has changed"))) {
				d.apply()
			}
		})
	})
}

// The widths of the grid's narrow columns, of a box to tick or a button;
// those of text share the rest of the window by their weights.
const (
	designTickWidth = 52
	designDropWidth = 30
)

// columnsView is the table's columns as a grid, a row each, dense as the
// data grid's: a cell is typed in where it is, and the cells of text take
// the window's width between them.
func (d *designer) columnsView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	th := c.Theme()
	keyFixed := d.fixed() != ""
	r, ok := db.SQL(d.engine)
	autoAllowed, keepsComments := ok && r.HasAutoNumbering(), !ok || r.HasComments()
	rowH := RowHeight(d.a.Settings().GridFontSize())
	// A cell of the grid: of text, sharing the width by weight, or of a
	// fixed width; a line at its right, as the data grid's cells have.
	cell := func(weight, width float32, fn func()) {
		box := ui.Row(c).Height(rowH).AlignItems(ui.Center).BorderWidth(0, 1, 0, 0).BorderColor(pal.GridLine)
		if width > 0 {
			box.Width(width).Justify(ui.Center).Shrink(0)
		} else {
			box.Grow(weight).Shrink(1).Basis(0).MinWidth(60)
		}
		box.Children(fn)
	}
	header := func(text string, weight, width float32) {
		cell(weight, width, func() {
			ui.Text(c, text).FontSize(widgets.TextSmall).FontWeight(600).TextColor(pal.Muted).SingleLine().Padding(0, 8)
		})
	}
	// Text typed in a cell: the grid's own look, no box of its own; the
	// cell it is in shows the focus.
	text := func(value *string, placeholder, label string, mono bool) ui.Element {
		in := ui.TextInputBase(c, value).Placeholder(placeholder).FontSize(widgets.TextBody).FillWidth().Padding(2, 8).Label(label)
		if mono {
			in.Font(widgets.MonoFont)
		}
		if in.Focused() {
			in.Border(1, th.Focus).Radius(3)
		} else {
			in.Border(1, ui.Transparent)
		}
		return in
	}
	ui.Column(c).Border(1, th.Border).Radius(widgets.RadiusMedium).Clip().Children(func() {
		ui.Row(c).Background(pal.GridHeader).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			header("Name", 3, 0)
			header("Type", 3, 0)
			header("Null", 0, designTickWidth)
			header("Default", 3, 0)
			header("Key", 0, designTickWidth)
			if autoAllowed {
				header("Auto", 0, designTickWidth)
			}
			if keepsComments {
				header("Comment", 4, 0)
			}
			ui.Box(c).Width(designDropWidth)
		})
		var types []string
		if ok {
			types = r.CommonTypes()
		}
		remove := -1
		for i := range d.now.Columns {
			col := &d.now.Columns[i]
			row := ui.Row(c.Key(d.cols.key("column-", i))).BorderWidth(0, 0, 1, 0).BorderColor(pal.GridLine)
			if row.Hovered() {
				row.Background(pal.Hover)
			}
			row.Children(func() {
				cell(3, 0, func() {
					name := text(&col.Name, "name", "Column name", true)
					if col.Was != "" && col.Was != col.Name {
						name.Tooltip("Renamed from " + col.Was)
					}
				})
				cell(3, 0, func() {
					typ := ui.Autocomplete(c, &col.Type, types).Font(widgets.MonoFont).FontSize(widgets.TextBody).FillWidth().Padding(2, 8).
						Background(ui.Transparent).Border(1, ui.Transparent).Radius(3).Label("Type of " + col.Name).Disabled(col.Generated)
					if typ.Focused() {
						typ.Border(1, th.Focus)
					}
				})
				cell(0, designTickWidth, func() {
					ui.Checkbox(c, &col.Nullable, "").Label("Null").Disabled(col.PrimaryKey || col.Generated)
				})
				cell(3, 0, func() { text(&col.Default, "none", "Default of "+col.Name, true) })
				cell(0, designTickWidth, func() {
					if ui.Checkbox(c, &col.PrimaryKey, "").Label("Primary key").Disabled(keyFixed).Changed() && col.PrimaryKey {
						col.Nullable = false // a key holds no NULL
					}
				})
				if autoAllowed {
					cell(0, designTickWidth, func() {
						// Whether an existing column numbers its rows changes in SQL.
						ui.Checkbox(c, &col.AutoIncrement, "").Label("Numbers its rows").Disabled(col.Was != "")
					})
				}
				if keepsComments {
					cell(4, 0, func() { text(&col.Comment, "comment", "Comment of "+col.Name, false) })
				}
				ui.Row(c).Width(designDropWidth).Height(rowH).Center().Children(func() {
					if widgets.SmallIconButton(c, widgets.IconTrash, "Drop "+col.Name).Clicked() {
						remove = i
					}
				})
			})
		}
		if remove >= 0 {
			deleteRow(&d.now.Columns, &d.cols, remove)
		}
	})
	ui.Row(c).Gap(8).PaddingY(4).Children(func() {
		if widgets.ToolButton(c, widgets.IconPlus, "Add Column", "Add a column at the end").Clicked() {
			typ := ""
			if r, ok := db.SQL(d.engine); ok {
				if types := r.CommonTypes(); len(types) > 0 {
					typ = types[0]
				}
			}
			appendRow(&d.now.Columns, &d.cols, db.ColumnDesign{Name: fmt.Sprint("column", len(d.now.Columns)+1), Type: typ, Nullable: true})
		}
	})
	if d.was != nil {
		droppedRow(c, "Columns dropped", d.was.Columns, &d.now.Columns, &d.cols, func(w db.ColumnDesign) bool {
			return slices.ContainsFunc(d.now.Columns, func(c db.ColumnDesign) bool { return c.Was == w.Name })
		}, func(w db.ColumnDesign) string { return w.Name })
	}
}

// droppedRow lists what the form dropped of what was read, each with a
// button bringing it back: the rows of was that kept does not report.
func droppedRow[T any](c *ui.Context, title string, was []T, list *[]T, keys *rowKeys, kept func(T) bool, name func(T) string) {
	dropped := slices.DeleteFunc(slices.Clone(was), kept)
	if len(dropped) == 0 {
		return
	}
	pal := widgets.PaletteOf(c)
	ui.Row(c).Gap(8).PaddingY(6).Wrap().Children(func() {
		ui.Text(c, title+":").FontSize(widgets.TextSmall).TextColor(pal.Muted)
		for _, row := range dropped {
			if ui.Link(c, "Restore "+name(row), "").Clicked() {
				appendRow(list, keys, row)
			}
		}
	})
}

func (d *designer) columnNames() []string {
	return lists.Map(d.now.Columns, func(c db.ColumnDesign) string { return c.Name })
}

func (d *designer) indexesView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	if r, ok := db.SQL(d.engine); ok && !r.HasSecondaryIndexes() {
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
		deleteRow(&d.now.Indexes, &d.indexes, remove)
	}
	if widgets.ToolButton(c, widgets.IconPlus, "Add Index", "Add an index of columns").Clicked() {
		appendRow(&d.now.Indexes, &d.indexes, db.IndexDesign{Name: d.freeName("idx", func(n string) bool {
			return slices.ContainsFunc(d.now.Indexes, func(ix db.IndexDesign) bool { return ix.Name == n })
		})})
	}
	if d.was != nil {
		droppedRow(c, "Indexes dropped", d.was.Indexes, &d.now.Indexes, &d.indexes, func(ix db.IndexDesign) bool {
			return slices.ContainsFunc(d.now.Indexes, func(n db.IndexDesign) bool { return n.Read && n.Name == ix.Name })
		}, func(ix db.IndexDesign) string { return ix.Name })
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

// actionClauses are the actions of actionLabels as SQL, in their order.
var actionClauses = []string{"", "RESTRICT", "CASCADE", "SET NULL", "SET DEFAULT"}

func actionOf(label string) string {
	if i := slices.Index(actionLabels, label); i >= 0 {
		return actionClauses[i]
	}
	return ""
}

func actionLabel(action string) string {
	if i := slices.Index(actionClauses, action); i >= 0 {
		return actionLabels[i]
	}
	return actionLabels[0]
}

func (d *designer) keysView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	fixed := d.fixed()
	if fixed != "" {
		ui.Text(c, fixed).FontSize(widgets.TextSmall).TextColor(pal.Muted)
	}
	tables := d.tables()
	remove := -1
	for i := range d.now.ForeignKeys {
		fk := &d.now.ForeignKeys[i]
		ui.Row(c.Key(d.fks.key("fk-", i))).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
			if fk.Read {
				ui.Text(c, fk.Name).Bold().Font(widgets.MonoFont).SingleLine()
				ui.Text(c, fk.Definition).Font(widgets.MonoFont).FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine().Grow(1).Shrink(1)
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
				ui.Text(c, "on delete").FontSize(widgets.TextSmall).TextColor(pal.Muted)
				ui.Select(c, &labels[0], actionLabels).Label("On delete")
				ui.Text(c, "on update").FontSize(widgets.TextSmall).TextColor(pal.Muted)
				ui.Select(c, &labels[1], actionLabels).Label("On update")
				fk.OnDelete, fk.OnUpdate = actionOf(labels[0]), actionOf(labels[1])
			}
			if widgets.Clicked(widgets.IconButton(c, widgets.IconTrash, "Drop "+fk.Name), widgets.When(fk.Read, fixed)) {
				remove = i
			}
		})
	}
	if remove >= 0 {
		deleteRow(&d.now.ForeignKeys, &d.fks, remove)
	}
	if fixed == "" && widgets.ToolButton(c, widgets.IconPlus, "Add Foreign Key", "Point columns at another table's").Clicked() {
		appendRow(&d.now.ForeignKeys, &d.fks, db.ForeignKeyDesign{RefSchema: d.now.Schema, Name: d.freeName("fkey", func(n string) bool {
			return slices.ContainsFunc(d.now.ForeignKeys, func(fk db.ForeignKeyDesign) bool { return fk.Name == n })
		})})
	}
	if d.was != nil {
		droppedRow(c, "Foreign keys dropped", d.was.ForeignKeys, &d.now.ForeignKeys, &d.fks, func(fk db.ForeignKeyDesign) bool {
			return slices.ContainsFunc(d.now.ForeignKeys, func(n db.ForeignKeyDesign) bool { return n.Read && n.Name == fk.Name })
		}, func(fk db.ForeignKeyDesign) string { return fk.Name })
	}
}

func (d *designer) checksView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	fixed := d.fixed()
	if fixed != "" {
		ui.Text(c, fixed).FontSize(widgets.TextSmall).TextColor(pal.Muted)
	}
	remove := -1
	for i := range d.now.Checks {
		ch := &d.now.Checks[i]
		ui.Row(c.Key(d.chks.key("check-", i))).Gap(8).AlignItems(ui.Center).Children(func() {
			if ch.Read {
				name := cmp.Or(ch.Name, "unnamed")
				ui.Text(c, name).Bold().Font(widgets.MonoFont).SingleLine()
				ui.Text(c, ch.Expression).Font(widgets.MonoFont).FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine().Grow(1).Shrink(1)
			} else {
				ui.TextInput(c, &ch.Name).Placeholder("name").Font(widgets.MonoFont).Width(200).Label("Check name")
				ui.TextInput(c, &ch.Expression).Placeholder("price > 0").Font(widgets.MonoFont).Grow(1).Label("Check expression")
			}
			if widgets.Clicked(widgets.IconButton(c, widgets.IconTrash, "Drop the check"), widgets.When(ch.Read, fixed)) {
				remove = i
			}
		})
	}
	if remove >= 0 {
		deleteRow(&d.now.Checks, &d.chks, remove)
	}
	if fixed == "" && widgets.ToolButton(c, widgets.IconPlus, "Add Check", "Add a condition every row must meet").Clicked() {
		appendRow(&d.now.Checks, &d.chks, db.CheckDesign{Name: d.freeName("check", func(n string) bool {
			return slices.ContainsFunc(d.now.Checks, func(ch db.CheckDesign) bool { return ch.Name == n })
		})})
	}
	if d.was != nil {
		droppedRow(c, "Checks dropped", d.was.Checks, &d.now.Checks, &d.chks, func(ch db.CheckDesign) bool {
			return slices.ContainsFunc(d.now.Checks, func(n db.CheckDesign) bool { return n.Read && n.Name == ch.Name && n.Expression == ch.Expression })
		}, func(ch db.CheckDesign) string { return ch.Expression })
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
	if r, ok := db.SQL(e); ok {
		return r.FirstColumn()
	}
	return db.ColumnDesign{Name: "id", PrimaryKey: true}
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

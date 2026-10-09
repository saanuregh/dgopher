package dataview

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"dgopher/internal/db"

	"github.com/egoist/mygo/ui"
)

// erLink is a column being dragged onto a table, to refer to it by a
// foreign key: where the drag started, and where the pointer is.
type erLink struct {
	from   *erTable
	column string
	x0, y0 float32
	x, y   float32
	moved  bool
}

// editable says why the diagram cannot change the schema, "" when it can.
func (e *ERTab) editable() string {
	switch {
	case e.conn.Config.ReadOnly:
		return e.conn.Config.Name + " is read-only."
	case e.conn.Config.Engine == db.ClickHouse:
		return "ClickHouse has no foreign keys."
	}
	return ""
}

// dragColumn follows a column dragged out of its table; dropped on another
// table, it offers to refer to it.
func (e *ERTab) dragColumn(t *erTable, column string, row ui.Element) {
	if e.editable() != "" {
		return
	}
	if _, _, ok := row.Dragged(); ok {
		// The pointer where it is on the diagram, from where it is on the
		// row: the row's top is half a row above its middle.
		px, py, _ := row.PointerPosition()
		x, y := t.x+px, t.rowY(column)-erRowH/2+py
		if e.linking == nil {
			e.linking = &erLink{from: t, column: column, x0: t.x + erBoxW, y0: t.rowY(column)}
		}
		e.linking.x, e.linking.y, e.linking.moved = x, y, true
		return
	}
	if l := e.linking; l != nil && l.from == t && l.column == column && !row.Pressed() {
		e.linking = nil
		if target := e.tableAt(l.x, l.y); target != nil && target != t && l.moved {
			e.proposeForeignKey(t, column, target)
		}
	}
}

// tableAt is the table whose box holds a point of the diagram, nil for none.
func (e *ERTab) tableAt(x, y float32) *erTable {
	for _, t := range e.tables {
		if x >= t.x && x <= t.x+erBoxW && y >= t.y && y <= t.y+t.height() {
			return t
		}
	}
	return nil
}

// drawLinking draws the column being dragged to its target, dashed.
func (e *ERTab) drawLinking(p *ui.Painter, r ui.Rect, c *ui.Context) {
	l := e.linking
	if l == nil || !l.moved {
		return
	}
	col := c.Theme().Accent
	// Dashes of 6 along the line.
	dx, dy := l.x-l.x0, l.y-l.y0
	n := int(max(1, (abs32(dx)+abs32(dy))/6))
	for i := 0; i < n; i += 2 {
		a, b := float32(i)/float32(n), float32(min(i+1, n))/float32(n)
		p.Line(r.X+l.x0+dx*a, r.Y+l.y0+dy*a, r.X+l.x0+dx*b, r.Y+l.y0+dy*b, 2, col)
	}
	if target := e.tableAt(l.x, l.y); target != nil && target != l.from {
		p.Stroke(ui.Rect{X: r.X + target.x - 2, Y: r.Y + target.y - 2, W: erBoxW + 4, H: target.height() + 4}, col, 10, 2)
	}
}

func abs32(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}

// proposeForeignKey offers to make a column of a table refer to another
// table's primary key.
func (e *ERTab) proposeForeignKey(from *erTable, column string, to *erTable) {
	var key []string
	for _, c := range to.cols {
		if c.PrimaryKey {
			key = append(key, c.Name)
		}
	}
	if len(key) != 1 {
		e.a.ShowError("No foreign key added", fmt.Sprintf("%s has no primary key of one column for %s.%s to refer to.", to.obj.Name, from.obj.Name, column))
		return
	}
	fk := db.ForeignKeyDesign{Columns: []string{column}, RefSchema: to.schema, RefTable: to.obj.Name, RefColumns: key}
	why := fmt.Sprintf("%s.%s will refer to %s.%s: a value with no row there is refused, and the rows there already must all have one.", from.obj.Name, column, to.obj.Name, key[0])
	e.changeTable(from, "Add a foreign key to "+from.obj.Name+"?", why, func(t *db.TableDesign) {
		t.ForeignKeys = append(t.ForeignKeys, fk)
	})
}

// dropForeignKey offers to drop a foreign key of a table.
func (e *ERTab) dropForeignKey(t *erTable, fk db.ForeignKey) {
	why := fmt.Sprintf("%s.%s will no longer have to refer to rows of %s.", t.obj.Name, strings.Join(fk.Columns, ", "), fk.RefTable)
	e.changeTable(t, "Drop the foreign key of "+t.obj.Name+"?", why, func(d *db.TableDesign) {
		d.ForeignKeys = slices.DeleteFunc(d.ForeignKeys, func(k db.ForeignKeyDesign) bool {
			return k.Name == fk.Name && slices.Equal(k.Columns, fk.Columns) && k.RefTable == fk.RefTable
		})
	})
}

// changeTable reads a table's design, changes it, and applies the change
// the engine writes for it, once agreed; then reads the diagram again.
func (e *ERTab) changeTable(t *erTable, title, why string, change func(*db.TableDesign)) {
	poolOf, obj := e.conn.PoolFor(e.database), t.obj
	obj.Schema = t.schema
	e.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var ch db.SchemaChange
		if err == nil {
			var was db.TableDesign
			if was, err = db.ReadTableDesign(ctx, d, obj); err == nil {
				now := cloneDesign(was)
				change(&now)
				ch, err = db.AlterTableChange(d.Dialect, was, now)
			}
		}
		return func() {
			if err != nil {
				e.a.ShowError("The schema was not changed", err.Error())
				return
			}
			e.apply(title, why, ch)
		}
	})
}

// dropTable offers to drop a table, with its rows.
func (e *ERTab) dropTable(t *erTable) {
	if e.conn.DB == nil {
		e.a.ShowError("The table was not dropped", e.conn.Config.Name+" is not connected.")
		return
	}
	ch := db.StatementsChange([]string{"DROP TABLE " + db.QualifiedName(e.conn.DB.Dialect, t.schema, t.obj.Name)})
	e.apply("Drop "+t.obj.Name+"?", "The table and its rows go.", ch)
}

// apply runs a change of the schema, always asked first, then reads the
// diagram again, its tables where they stood.
func (e *ERTab) apply(title, why string, ch db.SchemaChange) {
	ApplyChange(e.a, e.conn, e.database, title, ch, why, nil, func(err error) {
		if err != nil {
			e.a.ShowError("The schema was not changed", err.Error())
		}
		e.conn.ForgetCatalog()
		e.reload()
	})
}

// reload reads the diagram again, keeping its tables where they stand;
// those new find a free place.
func (e *ERTab) reload() {
	e.kept = map[string][2]float32{}
	for _, t := range e.tables {
		e.kept[erKey(t.schema, t.obj.Name)] = [2]float32{t.x, t.y}
	}
	e.loading, e.err = true, ""
	e.load()
}

// place puts the tables where they stood before a reload; those new to
// the right of the others, where they hide none.
func (e *ERTab) place() {
	right := float32(24)
	for _, t := range e.tables {
		if at, ok := e.kept[erKey(t.schema, t.obj.Name)]; ok {
			t.x, t.y = at[0], at[1]
			right = max(right, t.x+erBoxW+erGapX)
		}
	}
	for _, t := range e.tables {
		if _, ok := e.kept[erKey(t.schema, t.obj.Name)]; !ok {
			t.x, t.y = right, 24
			e.settle(t)
		}
	}
	e.kept = nil
}

// tableMenu is a table's menu in the diagram: open it, change it.
func (e *ERTab) tableMenu(m *ui.Menu, t *erTable) {
	obj := t.obj
	obj.Schema = t.schema
	if m.Item("Open Data").Chosen() {
		e.a.OpenTable(e.conn, e.database, obj, PageData)
	}
	if m.Item("Open Structure").Chosen() {
		e.a.OpenTable(e.conn, e.database, obj, PageStructure)
	}
	why := e.editable()
	if why != "" || obj.Kind != db.KindTable {
		return
	}
	m.Separator()
	if len(t.fks) > 0 {
		m.Submenu("Drop Foreign Key", func(m *ui.Menu) {
			for _, fk := range t.fks {
				label := strings.Join(fk.Columns, ", ") + " → " + fk.RefTable
				if fk.Name != "" {
					label = fk.Name + ": " + label
				}
				if m.Item(label).Chosen() {
					e.dropForeignKey(t, fk)
				}
			}
		})
	}
	if m.Item("Drop Table…").Chosen() {
		e.dropTable(t)
	}
}

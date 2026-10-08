package dataview

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// erTable is a table drawn in an ER diagram.
type erTable struct {
	schema string
	obj    db.Object
	cols   []db.Column
	fks    []db.ForeignKey
	x, y   float32
}

// ERTab draws the tables of a schema and the foreign keys between them.
type ERTab struct {
	a        Host
	conn     *connection.Conn
	database string
	schema   string

	tables []*erTable
	byName map[string]*erTable // by erKey
	// focus, when set, draws one table and the tables next to it by
	// their foreign keys, as a table's Diagram page does.
	focus   *db.Object
	loading bool
	err     string
	scroll  ui.ScrollState
	more    int // tables left out of a large schema
}

const (
	erBoxW      = 230
	erHeaderH   = 30
	erRowH      = 21
	erMaxCols   = 14
	erMaxTables = 120
	erGapX      = 90
	erGapY      = 36
)

// erKey names a table across schemas.
func erKey(schema, name string) string { return schema + "\x00" + name }

// newTableDiagram draws a table and its neighbours by foreign key.
func newTableDiagram(a Host, cn *connection.Conn, database string, obj db.Object) *ERTab {
	e := &ERTab{a: a, conn: cn, database: database, schema: obj.Schema, focus: &obj, loading: true}
	e.load()
	return e
}

func newERTab(a Host, cn *connection.Conn, database, schema string) *ERTab {
	e := &ERTab{a: a, conn: cn, database: database, schema: schema, loading: true}
	e.load()
	return e
}

func (e *ERTab) Title() string { return e.schema + " · ER diagram" }

func (e *ERTab) Connection() *connection.Conn { return e.conn }

func (e *ERTab) CloseReason() string { return "" }

func (e *ERTab) Close() {}

func (e *ERTab) load() {
	cn, database, schema, focus := e.conn, e.database, e.schema, e.focus
	poolOf := cn.PoolFor(database) // read on the main thread
	e.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		d, err := poolOf(ctx)
		var tables []*erTable
		more := 0
		if err == nil && focus != nil {
			tables, err = loadNeighbours(ctx, d, *focus)
		} else if err == nil {
			var objs []db.Object
			objs, err = d.Dialect.Objects(ctx, d.SQL, schema)
			objs = connection.SortedObjects(objs, false)
			if len(objs) > erMaxTables {
				more = len(objs) - erMaxTables
				objs = objs[:erMaxTables]
			}
			for _, o := range objs {
				if err != nil {
					break
				}
				t := &erTable{schema: schema, obj: o}
				t.cols, err = d.Dialect.Columns(ctx, d.SQL, schema, o.Name)
				if err == nil {
					t.fks, _ = d.Dialect.ForeignKeys(ctx, d.SQL, schema, o.Name)
				}
				tables = append(tables, t)
			}
		}
		return func() {
			e.loading = false
			if err != nil {
				e.err = err.Error()
				return
			}
			e.tables, e.more = tables, more
			e.byName = map[string]*erTable{}
			for _, t := range tables {
				e.byName[erKey(t.schema, t.obj.Name)] = t
			}
			if e.focus != nil {
				e.layoutFocus()
			} else {
				e.layout()
			}
		}
	})
}

func (t *erTable) height() float32 {
	n := min(len(t.cols), erMaxCols)
	h := float32(erHeaderH + n*erRowH + 8)
	if len(t.cols) > erMaxCols {
		h += erRowH
	}
	return h
}

// layout places the tables in columns by their references: a table
// stands to the right of the tables it refers to.
func (e *ERTab) layout() {
	depth := map[*erTable]int{}
	var visit func(t *erTable, seen map[*erTable]bool) int
	visit = func(t *erTable, seen map[*erTable]bool) int {
		if d, ok := depth[t]; ok {
			return d
		}
		if seen[t] {
			return 0 // a cycle of references
		}
		seen[t] = true
		d := 0
		for _, fk := range t.fks {
			if ref := e.refOf(t, fk); ref != nil && ref != t {
				d = max(d, visit(ref, seen)+1)
			}
		}
		depth[t] = d
		return d
	}
	maxDepth := 0
	for _, t := range e.tables {
		maxDepth = max(maxDepth, visit(t, map[*erTable]bool{}))
	}
	columns := make([][]*erTable, maxDepth+1)
	for _, t := range e.tables {
		columns[depth[t]] = append(columns[depth[t]], t)
	}
	// Wide layers wrap into more columns, for a diagram that fits a screen.
	limit := max(4, int(math.Ceil(math.Sqrt(float64(len(e.tables))))))
	x := float32(24)
	for _, col := range columns {
		sort.SliceStable(col, func(i, j int) bool { return len(col[i].fks) > len(col[j].fks) })
		for start := 0; start < len(col); start += limit {
			y := float32(24)
			for _, t := range col[start:min(start+limit, len(col))] {
				t.x, t.y = x, y
				y += t.height() + erGapY
			}
			x += erBoxW + erGapX
		}
	}
}

// rowY is the middle of a column's row in a table's box, nil column for
// the header.
func (t *erTable) rowY(column string) float32 {
	for i, c := range t.cols {
		if c.Name == column && i < erMaxCols {
			return t.y + erHeaderH + float32(i)*erRowH + erRowH/2 + 4
		}
	}
	return t.y + erHeaderH/2
}

func (e *ERTab) View(c *ui.Context) {
	a := e.a
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(6, 10).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Icon(c, widgets.IconSchema).TextColor(pal.Muted).FontSize(14)
			if e.focus != nil {
				ui.Text(c, "Tables linked to "+e.focus.Name+" by foreign keys").Bold()
			} else {
				ui.Text(c, e.schema).Bold()
			}
			ui.Text(c, fmt.Sprintf("%d tables", len(e.tables))).FontSize(12).TextColor(pal.Muted)
			if e.more > 0 {
				ui.Text(c, fmt.Sprintf("(%d more not drawn)", e.more)).FontSize(12).TextColor(th.Warning)
			}
			ui.Spacer(c)
			ui.Text(c, "Drag a table by its name; double-click it to open its data.").FontSize(12).TextColor(pal.Muted)
			if widgets.ToolButton(c, widgets.IconRefresh, "Arrange", "Lay the tables out again").Clicked() {
				if e.focus != nil {
					e.layoutFocus()
				} else {
					e.layout()
				}
			}
		})
		switch {
		case e.loading:
			ui.Row(c).Grow(1).Center().Gap(8).Children(func() {
				ui.Spinner(c)
				ui.Text(c, "Reading the schema…").TextColor(pal.Muted)
			})
			return
		case e.err != "":
			ui.Text(c, e.err).TextColor(th.Danger).Padding(12).Selectable()
			return
		case len(e.tables) == 0:
			ui.Text(c, "No tables in this schema.").TextColor(pal.Muted).Padding(12)
			return
		}
		var w, h float32
		for _, t := range e.tables {
			w, h = max(w, t.x+erBoxW+40), max(h, t.y+t.height()+40)
		}
		ui.ScrollBoth(c).TrackScroll(&e.scroll).Grow(1).Background(pal.EditorBg).Children(func() {
			canvas := ui.Box(c).Size(w, h)
			canvas.Draw(func(p *ui.Painter, r ui.Rect) { e.drawLinks(p, r, c) })
			canvas.Children(func() {
				for _, t := range e.tables {
					e.box(c, a, t)
				}
			})
		})
	})
}

// drawLinks draws a connector for each foreign key, from the referencing
// column to the referenced one.
func (e *ERTab) drawLinks(p *ui.Painter, r ui.Rect, c *ui.Context) {
	th := c.Theme()
	col := th.Accent.Alpha(0.75)
	for _, t := range e.tables {
		for _, fk := range t.fks {
			ref := e.refOf(t, fk)
			if ref == nil || len(fk.Columns) == 0 {
				continue
			}
			refCol := ""
			if len(fk.RefColumns) > 0 {
				refCol = fk.RefColumns[0]
			}
			y0 := r.Y + t.rowY(fk.Columns[0])
			y1 := r.Y + ref.rowY(refCol)
			if ref == t {
				// A reference to itself: a loop on the right.
				x := r.X + t.x + erBoxW
				p.Line(x, y0, x+18, y0, 1.5, col)
				p.Line(x+18, y0, x+18, y1, 1.5, col)
				p.Line(x+18, y1, x, y1, 1.5, col)
				continue
			}
			// Leave from the side facing the other table.
			x0, x1 := r.X+t.x, r.X+ref.x+erBoxW
			if ref.x > t.x {
				x0, x1 = r.X+t.x+erBoxW, r.X+ref.x
			}
			mid := (x0 + x1) / 2
			p.Line(x0, y0, mid, y0, 1.5, col)
			p.Line(mid, y0, mid, y1, 1.5, col)
			p.Line(mid, y1, x1, y1, 1.5, col)
			// The referenced end: a bar, as crow's foot notation draws "one".
			dir := float32(1)
			if x1 < mid {
				dir = -1
			}
			p.Line(x1-dir*6, y1-5, x1-dir*6, y1+5, 1.5, col)
			p.Fill(ui.Rect{X: x0 - 3, Y: y0 - 3, W: 6, H: 6}, col, 3)
		}
	}
}

func (e *ERTab) box(c *ui.Context, a Host, t *erTable) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	box := ui.Column(c.Key("er-"+t.schema+"."+t.obj.Name)).Absolute().Left(t.x).Top(t.y).Width(erBoxW).
		Radius(8).Background(th.Background).Border(1, th.Border).Clip().
		Shadow(0, 2, 8, 0, ui.RGBA(0, 0, 0, 0.08))
	box.Children(func() {
		header := ui.Row(c).Height(erHeaderH).Padding(0, 10).Gap(6).Background(pal.GridHeader).
			BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Cursor(ui.CursorMove)
		focused := e.focus != nil && t.schema == e.focus.Schema && t.obj.Name == e.focus.Name
		if focused {
			header.Background(th.Accent.Alpha(0.18))
		}
		header.Children(func() {
			ui.Icon(c, widgets.IconTable).FontSize(12).TextColor(widgets.EngineColor(e.conn.Config.Engine))
			name := t.obj.Name
			if t.schema != e.schema {
				name = t.schema + "." + name
			}
			ui.Text(c, name).Bold().FontSize(12.5).SingleLine().Grow(1).Shrink(1)
		})
		if dx, dy, ok := header.Dragged(); ok {
			t.x, t.y = max(0, t.x+dx), max(0, t.y+dy)
		}
		if header.DoubleClicked() && !focused {
			a.OpenTable(e.conn, e.database, t.obj, PageData)
		}
		ui.Column(c).PaddingY(4).Children(func() {
			for i, col := range t.cols {
				if i == erMaxCols {
					ui.Text(c, fmt.Sprintf("+ %d more", len(t.cols)-erMaxCols)).FontSize(11).TextColor(pal.Muted).Height(erRowH).Padding(3, 10)
					break
				}
				ui.Row(c).Height(erRowH).Padding(0, 10).Gap(6).Children(func() {
					switch {
					case col.PrimaryKey:
						ui.Icon(c, widgets.IconKey).FontSize(10).TextColor(ui.Hex("#d97706")).Width(12)
					case isFKColumn(t, col.Name):
						ui.Text(c, "→").FontSize(10).TextColor(th.Accent).Width(12)
					default:
						ui.Box(c).Width(12)
					}
					name := ui.Text(c, col.Name).FontSize(12).SingleLine().Grow(1).Shrink(1)
					if !col.Nullable {
						name.FontWeight(600)
					}
					ui.Text(c, col.Type).FontSize(10.5).TextColor(pal.Muted).SingleLine().MaxWidth(100)
				})
			}
		})
	})
}

func isFKColumn(t *erTable, name string) bool {
	for _, fk := range t.fks {
		for _, c := range fk.Columns {
			if c == name {
				return true
			}
		}
	}
	return false
}

// OpenER opens the ER diagram of a schema, or brings it forward.
func OpenER(a Host, cn *connection.Conn, database, schema string) {
	if a.ActivateTab(func(t widgets.Tab) bool {
		et, ok := t.(*ERTab)
		return ok && et.conn == cn && et.database == database && et.schema == schema
	}) {
		return
	}
	a.Connect(cn, func() { a.AddTab(newERTab(a, cn, database, schema)) })
}

// refOf is the table a foreign key points at, among those drawn.
func (e *ERTab) refOf(t *erTable, fk db.ForeignKey) *erTable {
	schema := fk.RefSchema
	if schema == "" {
		schema = t.schema
	}
	return e.byName[erKey(schema, fk.RefTable)]
}

// loadNeighbours reads a table, the tables its foreign keys point at, and
// those whose foreign keys point at it, with their columns and keys.
func loadNeighbours(ctx context.Context, d *db.DB, focus db.Object) ([]*erTable, error) {
	var tables []*erTable
	seen := map[string]bool{}
	add := func(schema string, obj db.Object) error {
		if seen[erKey(schema, obj.Name)] {
			return nil
		}
		seen[erKey(schema, obj.Name)] = true
		t := &erTable{schema: schema, obj: obj}
		var err error
		if t.cols, err = d.Dialect.Columns(ctx, d.SQL, schema, obj.Name); err != nil {
			return err
		}
		t.fks, _ = d.Dialect.ForeignKeys(ctx, d.SQL, schema, obj.Name)
		tables = append(tables, t)
		return nil
	}
	if err := add(focus.Schema, focus); err != nil {
		return nil, err
	}
	for _, fk := range tables[0].fks {
		schema := fk.RefSchema
		if schema == "" {
			schema = focus.Schema
		}
		if err := add(schema, db.Object{Schema: schema, Name: fk.RefTable, Kind: db.KindTable, Rows: -1}); err != nil {
			return nil, err
		}
	}
	refs, err := d.Dialect.ReferencedBy(ctx, d.SQL, focus.Schema, focus.Name)
	if err != nil {
		return nil, err
	}
	for _, r := range refs {
		if err := add(r.Schema, db.Object{Schema: r.Schema, Name: r.Table, Kind: db.KindTable, Rows: -1}); err != nil {
			return nil, err
		}
	}
	return tables, nil
}

// layoutFocus puts the tables that point at the focus on its left, those
// it points at on its right, each side stacked.
func (e *ERTab) layoutFocus() {
	if len(e.tables) == 0 {
		return
	}
	focus := e.tables[0]
	var left, right []*erTable
	for _, t := range e.tables[1:] {
		pointsAtFocus := false
		for _, fk := range t.fks {
			if e.refOf(t, fk) == focus {
				pointsAtFocus = true
			}
		}
		if pointsAtFocus {
			left = append(left, t)
		} else {
			right = append(right, t)
		}
	}
	stack := func(ts []*erTable, x float32) float32 {
		y := float32(24)
		for _, t := range ts {
			t.x, t.y = x, y
			y += t.height() + erGapY
		}
		return y
	}
	x := float32(24)
	if len(left) > 0 {
		stack(left, x)
		x += erBoxW + erGapX
	}
	focus.x, focus.y = x, 24
	stack(right, x+erBoxW+erGapX)
}

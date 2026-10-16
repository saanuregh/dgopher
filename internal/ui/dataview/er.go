package dataview

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
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
	// dragged is the table being dragged by its name, which settles where
	// it hides no other once dropped.
	dragged *erTable
	// linking is a column being dragged onto a table to refer to it.
	linking *erLink
	// kept are where the tables stood before a reload, which they keep.
	kept map[string][2]float32
	// linkOn is the line the pointer is on, -1 for none. cardLine is the
	// line whose card shows or waits, counted from 1 so that a new tab has
	// none, opened at cardAt.
	linkOn, cardLine int
	cardAt           ui.Point
}

const (
	erBoxW      = 230
	erHeaderH   = 30
	erRowH      = 21
	erMaxCols   = 14
	erMaxTables = 120
	erGapX      = 90
	erGapY      = 36
	// erClear is the least room between two tables a drop leaves.
	erClear = 16
	// erStep is how far an arrow key moves a table.
	erStep = 20
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

func (e *ERTab) Icon() *ui.SVG { return widgets.IconSchema }

func (e *ERTab) Places() []connection.Place { return []connection.Place{e.conn.PlaceOf(e.database)} }

func (e *ERTab) CloseReason() string { return "" }

func (e *ERTab) Close() {}

func (e *ERTab) load() {
	cn, database, schema, focus := e.conn, e.database, e.schema, e.focus
	poolOf := cn.PoolFor(database) // read on the main thread
	BackgroundResetOnPanic(e.a, func() { e.loading = false }, func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		d, err := poolOf(ctx)
		var tables []*erTable
		more := 0
		if err == nil && focus != nil {
			tables, err = loadNeighbours(ctx, d, *focus)
		} else if err == nil {
			var objs []db.Object
			objs, err = d.Dialect.Objects(ctx, d.Catalog(), schema)
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
				t.cols, err = d.Dialect.Columns(ctx, d.Catalog(), schema, o.Name)
				if err == nil {
					t.fks, _ = d.Dialect.ForeignKeys(ctx, d.Catalog(), schema, o.Name)
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
			switch {
			case e.kept != nil:
				e.place()
			case e.focus != nil:
				e.layoutFocus()
			default:
				e.layout()
			}
		}
	})
}

// height is the height of a table's box as drawn: its header, its rows
// with their padding, and its border.
func (t *erTable) height() float32 {
	n := min(len(t.cols), erMaxCols)
	h := float32(erHeaderH + n*erRowH + 8 + 2)
	if len(t.cols) > erMaxCols {
		h += erRowH
	}
	return h
}

// inTheWay is a table that a table placed at x, y would come within
// erClear of, nil when there is none.
func (e *ERTab) inTheWay(t *erTable, x, y float32) *erTable {
	for _, o := range e.tables {
		if o != t && x < o.x+erBoxW+erClear && o.x < x+erBoxW+erClear && y < o.y+o.height()+erClear && o.y < y+t.height()+erClear {
			return o
		}
	}
	return nil
}

// settle moves a table dropped over others to the free place nearest
// where it was dropped, so that no table hides another. A free place
// nearest the drop lies against the edges of other tables, along each
// axis, so those edges are where it is looked for; down the same column,
// below every table, there is always one.
func (e *ERTab) settle(t *erTable) {
	if e.inTheWay(t, t.x, t.y) == nil {
		return
	}
	xs, ys := []float32{t.x}, []float32{t.y}
	for _, o := range e.tables {
		if o != t {
			xs = append(xs, o.x-erBoxW-erClear, o.x+erBoxW+erClear)
			ys = append(ys, o.y-t.height()-erClear, o.y+o.height()+erClear)
		}
	}
	bestX, bestY, best := t.x, t.y, float32(math.Inf(1))
	for _, x := range xs {
		for _, y := range ys {
			d := (x-t.x)*(x-t.x) + (y-t.y)*(y-t.y)
			if x >= 0 && y >= 0 && d < best && e.inTheWay(t, x, y) == nil {
				bestX, bestY, best = x, y, d
			}
		}
	}
	if math.IsInf(float64(best), 1) {
		// Below whatever is in the way: each step goes lower, and below the
		// lowest table nothing is.
		for o := e.inTheWay(t, t.x, bestY); o != nil; o = e.inTheWay(t, t.x, bestY) {
			bestY = o.y + o.height() + erClear
		}
	}
	t.x, t.y = bestX, bestY
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
			stackTables(col[start:min(start+limit, len(col))], x)
			x += erBoxW + erGapX
		}
	}
}

// stackTables places tables one under another in a column at x.
func stackTables(ts []*erTable, x float32) {
	y := float32(24)
	for _, t := range ts {
		t.x, t.y = x, y
		y += t.height() + erGapY
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
		widgets.Bar(c, "Diagram", func() {
			widgets.BarTitle(c, func() {
				ui.Icon(c, widgets.IconSchema).TextColor(pal.Muted).FontSize(widgets.ToolIconSize)
				if e.focus != nil {
					ui.Text(c, "Tables linked to "+e.focus.Name+" by foreign keys").Bold()
				} else {
					ui.Text(c, e.schema).Bold()
				}
				ui.Text(c, widgets.Count(len(e.tables), "table")).FontSize(12).TextColor(pal.Muted)
				if e.more > 0 {
					ui.Text(c, fmt.Sprintf("(%d more not drawn)", e.more)).FontSize(12).TextColor(th.Warning)
				}
			})
			ui.Spacer(c)
			e.tools(c, a)
		})
		switch {
		case e.loading:
			ui.Row(c).Grow(1).Center().Gap(8).Children(func() {
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
		dots := ui.RGBA(255, 255, 255, 0.05)
		if !th.Dark {
			dots = ui.RGBA(0, 0, 0, 0.07)
		}
		ui.ScrollBoth(c).TrackScroll(&e.scroll).Grow(1).Background(pal.EditorBg).Draw(func(p *ui.Painter, r ui.Rect) {
			// Dots under the tables, moving with them: a surface they are
			// placed on, and moved about. Where the window draws on the
			// CPU, as tests do, a shader costs seconds: none there.
			if p.Accelerated() {
				dotGrid.Draw(p, r, shader.Options{Uniforms: map[string]any{"Color": dots, "Offset": []float32{e.scroll.X, e.scroll.Y}}})
			}
		}).Children(func() {
			e.canvas(c, a)
		})
		// How the canvas works, in its corner rather than in the bar.
		ui.Text(c, "Drag a table by its name; double-click it to open its data.").FontSize(11.5).TextColor(pal.Muted).
			Absolute().Left(14).Bottom(10)
	})
}

// dotGrid draws a dot every 20 DIPs, a DIP across, of Color, the grid
// moved by Offset, the scroll's.
var dotGrid = shader.MustCompile(`package main

var Color vec4
var Offset vec2

func Fragment(dst vec4, pos vec2) vec4 {
	q := mod(pos+Offset, 20) - 10
	a := Color.a * (1 - smoothstep(0.5, 1.1, length(q)))
	return vec4(Color.rgb*a, a)
}
`)

// tools are the diagram's buttons: save it, a new table, read it again,
// arrange it.
func (e *ERTab) tools(c *ui.Context, a Host) {
	widgets.MenuOn(c, widgets.ToolButton(c, widgets.IconDownload, "Save Image", "Save the diagram as a PNG or SVG image").Disabled(len(e.tables) == 0), func(m *widgets.Menu) {
		if m.Item("Save as PNG…").Chosen() {
			e.save(exportPNG)
		}
		if m.Item("Save as SVG…").Chosen() {
			e.save(exportSVG)
		}
	})
	if e.editable() == "" && widgets.ToolButton(c, widgets.IconPlus, "New Table", "Design a new table in "+e.schema).Clicked() {
		OpenNewTable(a, e.conn, e.database, e.schema)
	}
	if widgets.ToolButton(c, widgets.IconRefresh, "Refresh", "Read the schema again, the tables staying where they are").Clicked() {
		e.reload()
	}
	if widgets.ToolButton(c, widgets.IconLayers, "Arrange", "Lay the tables out again").Clicked() {
		if e.focus != nil {
			e.layoutFocus()
		} else {
			e.layout()
		}
	}
}

// extent is the size of the diagram: its tables, with a margin.
func (e *ERTab) extent() (w, h float32) {
	for _, t := range e.tables {
		w, h = max(w, t.x+erBoxW+40), max(h, t.y+t.height()+40)
	}
	return w, h
}

// canvas draws the tables where they stand, and their links.
func (e *ERTab) canvas(c *ui.Context, a Host) {
	w, h := e.extent()
	canvas := ui.Box(c).Size(w, h).Label("ER diagram of " + e.schema)
	// The lines, in the canvas: drawn, and found under the pointer.
	conns := e.connectors(ui.Rect{})
	cardKey := ""
	if i := e.cardLine - 1; i >= 0 && i < len(conns) {
		cardKey = e.linkKey(conns[i])
	}
	px, py, over := canvas.PointerPosition()
	on := -1
	switch {
	case cardKey != "" && widgets.PointerInCard(c) && widgets.HoverCardOpen(c) == cardKey:
		// In the card, the line it is of stays the one pointed at, not
		// one running under the card.
		on = e.cardLine - 1
	case over && e.dragged == nil && e.linking == nil:
		on = linkAt(conns, px, py)
	}
	if on >= 0 && on+1 != e.cardLine {
		e.cardLine, e.cardAt = on+1, ui.Point{X: px, Y: py}
		cardKey = e.linkKey(conns[on])
	}
	// Alt+click on a line shows its card at once.
	if on >= 0 && canvas.PressedInside() && c.Modifiers()&ui.Alt != 0 {
		widgets.ShowCard(c, cardKey)
	}
	e.linkOn = on
	canvas.Draw(func(p *ui.Painter, r ui.Rect) {
		e.drawLinks(p, r, c, conns)
		e.drawLinking(p, r, c)
	})
	canvas.Children(func() {
		for _, t := range e.tables {
			e.box(c, a, t)
		}
		if i := e.cardLine - 1; i >= 0 && i < len(conns) {
			e.linkCardView(c, a, conns[i], cardKey, on == i)
		}
	})
}

// linkKey is the key of a line's card.
func (e *ERTab) linkKey(cn erConnector) string {
	return fmt.Sprintf("er:%p:%s\x00%s", e, cn.from.obj.Name, strings.Join(cn.fk.Columns, ","))
}

// linkCardView gives a foreign key's line its card, at the point the
// pointer came on it.
func (e *ERTab) linkCardView(c *ui.Context, a Host, cn erConnector, key string, hovered bool) {
	at := e.cardAt
	anchor := ui.Box(c.Key("link card")).Absolute().Left(at.X).Top(at.Y).Size(1, 1).PassThrough()
	widgets.HoverCard(c, anchor, widgets.CardDiagram, key, widgets.CardOptions{Place: widgets.PlaceBelow, Hovered: &hovered}, func(c *ui.Context, card *widgets.Card) {
		fk := cn.fk
		name := fk.Name
		if name == "" {
			name = cn.from.obj.Name + " → " + cn.to.obj.Name
		}
		card.Header(c, widgets.IconKey, widgets.ToneTable, name, "foreign key")
		qualify := func(table string, cols []string) string {
			return table + "." + strings.Join(cols, ", "+table+".")
		}
		card.Rows(c, [][2]string{
			{"From", qualify(cn.from.obj.Name, fk.Columns)},
			{"To", qualify(cn.to.obj.Name, fk.RefColumns)},
			{"On delete", cmp.Or(fk.OnDelete, "NO ACTION")},
			{"On update", cmp.Or(fk.OnUpdate, "NO ACTION")},
		})
		card.Actions(c, func(ac *widgets.CardActions) {
			if ac.Button(c, "Open "+cn.from.obj.Name, "") {
				a.OpenTable(e.conn, e.database, cn.from.obj, PageData)
			}
			if ac.Button(c, "Open "+cn.to.obj.Name, "") {
				a.OpenTable(e.conn, e.database, cn.to.obj, PageData)
			}
		})
	})
}

// erConnector is a foreign key's line: from the referencing column of
// from to the referenced one of to, along pts.
type erConnector struct {
	from, to *erTable
	fk       db.ForeignKey
	pts      []ui.Point
	self     bool
}

// connectors are the lines of the foreign keys, in a canvas at r.
func (e *ERTab) connectors(r ui.Rect) []erConnector {
	var out []erConnector
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
				out = append(out, erConnector{from: t, to: ref, fk: fk, self: true,
					pts: []ui.Point{{X: x, Y: y0}, {X: x + 18, Y: y0}, {X: x + 18, Y: y1}, {X: x, Y: y1}}})
				continue
			}
			// Leave from the side facing the other table.
			x0, x1 := r.X+t.x, r.X+ref.x+erBoxW
			if ref.x > t.x {
				x0, x1 = r.X+t.x+erBoxW, r.X+ref.x
			}
			mid := e.channel(r, x0, x1, y0, y1)
			out = append(out, erConnector{from: t, to: ref, fk: fk,
				pts: []ui.Point{{X: x0, Y: y0}, {X: mid, Y: y0}, {X: mid, Y: y1}, {X: x1, Y: y1}}})
		}
	}
	return out
}

// linkHit is how near a line the pointer is on it, in DIPs.
const linkHit = 4

// linkAt is the line the point (x, y) is on, -1 for none.
func linkAt(conns []erConnector, x, y float32) int {
	for i, cn := range conns {
		for j := 1; j < len(cn.pts); j++ {
			if segmentDistance(ui.Point{X: x, Y: y}, cn.pts[j-1], cn.pts[j]) <= linkHit {
				return i
			}
		}
	}
	return -1
}

// segmentDistance is how far p is from the segment from a to b.
func segmentDistance(p, a, b ui.Point) float32 {
	dx, dy := b.X-a.X, b.Y-a.Y
	t := float32(0)
	if l := dx*dx + dy*dy; l > 0 {
		t = max(0, min(1, ((p.X-a.X)*dx+(p.Y-a.Y)*dy)/l))
	}
	cx, cy := a.X+t*dx-p.X, a.Y+t*dy-p.Y
	return float32(math.Sqrt(float64(cx*cx + cy*cy)))
}

// drawLinks draws a connector for each foreign key, from the referencing
// column to the referenced one, as connectors found them at the canvas's
// origin; the one the pointer is on stands out.
func (e *ERTab) drawLinks(p *ui.Painter, r ui.Rect, c *ui.Context, conns []erConnector) {
	th := c.Theme()
	for i, cn := range conns {
		col, width := th.Accent.Alpha(0.75), float32(1.5)
		if i == e.linkOn && e.linkOn >= 0 {
			col, width = th.Accent, 2.5
		}
		pts := cn.pts
		if cn.self {
			for j := 1; j < len(pts); j++ {
				p.Line(r.X+pts[j-1].X, r.Y+pts[j-1].Y, r.X+pts[j].X, r.Y+pts[j].Y, width, col)
			}
			continue
		}
		x0, y0, mid, x1, y1 := r.X+pts[0].X, r.Y+pts[0].Y, r.X+pts[1].X, r.X+pts[3].X, r.Y+pts[3].Y
		p.StrokePath(elbow(x0, y0, mid, x1, y1), width, col)
		// The referenced end: a bar, as crow's foot notation draws "one".
		dir := float32(1)
		if x1 < mid {
			dir = -1
		}
		p.Line(x1-dir*6, y1-5, x1-dir*6, y1+5, width, col)
		p.Fill(ui.Rect{X: x0 - 3, Y: y0 - 3, W: 6, H: 6}, col, 3)
	}
}

// channel is the x where a connector from (x0, y0) to (x1, y1) turns:
// halfway, unless a table stands there, then by the end it goes to or the
// one it leaves, so that no line runs under a table and seems to join it.
func (e *ERTab) channel(r ui.Rect, x0, x1, y0, y1 float32) float32 {
	dir := float32(1)
	if x1 < x0 {
		dir = -1
	}
	const by = 24
	for _, x := range []float32{(x0 + x1) / 2, x1 - dir*by, x0 + dir*by} {
		if e.clear(r, x, x0, x1, y0, y1) {
			return x
		}
	}
	return (x0 + x1) / 2
}

// clear reports whether a connector turning at x crosses no table: its
// way along at y0, then down to y1, then along to x1. The ends sit on
// their tables' edges, which is not crossing them.
func (e *ERTab) clear(r ui.Rect, x, x0, x1, y0, y1 float32) bool {
	// across reports whether a segment from a to b at c, along or down,
	// passes through the box from lo to hi there, between top and bottom.
	across := func(a, b, c, lo, hi, top, bottom float32) bool {
		return c > top && c < bottom && max(min(a, b), lo) < min(max(a, b), hi)
	}
	for _, t := range e.tables {
		bx, by := r.X+t.x, r.Y+t.y
		bw, bh := float32(erBoxW), t.height()
		if across(x0, x, y0, bx, bx+bw, by, by+bh) || across(y0, y1, x, by, by+bh, bx, bx+bw) || across(x, x1, y1, bx, bx+bw, by, by+bh) {
			return false
		}
	}
	return true
}

// elbow is a connector from (x0, y0) along to x, down to y1 and along to
// (x1, y1), its corners rounded.
func elbow(x0, y0, x, x1, y1 float32) *ui.Path {
	sign := func(v float32) float32 {
		if v < 0 {
			return -1
		}
		return 1
	}
	abs := func(v float32) float32 { return v * sign(v) }
	rr := min(6, abs(y1-y0)/2, abs(x-x0), abs(x1-x))
	path := (&ui.Path{}).MoveTo(x0, y0)
	if rr <= 0 {
		return path.LineTo(x, y0).LineTo(x, y1).LineTo(x1, y1)
	}
	a, b, v := sign(x-x0), sign(x1-x), sign(y1-y0)
	return path.LineTo(x-a*rr, y0).QuadTo(x, y0, x, y0+v*rr).
		LineTo(x, y1-v*rr).QuadTo(x, y1, x+b*rr, y1).LineTo(x1, y1)
}

// erMoves are how the arrow keys move a table.
var erMoves = []struct {
	key    ui.Key
	dx, dy float32
}{{ui.KeyLeft, -erStep, 0}, {ui.KeyRight, erStep, 0}, {ui.KeyUp, 0, -erStep}, {ui.KeyDown, 0, erStep}}

func (e *ERTab) box(c *ui.Context, a Host, t *erTable) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	box := ui.Column(c.Key("er-"+t.schema+"."+t.obj.Name)).Absolute().Left(t.x).Top(t.y).Width(erBoxW).Label("Table "+t.obj.Name).
		Radius(8).Background(th.Background).Border(1, th.Border).Clip().
		Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.2)).Shadow(0, 4, 12, 0, ui.RGBA(0, 0, 0, 0.12))
	box.ContextMenu(func(m *ui.Menu) { e.tableMenu(m, t) })
	box.Children(func() {
		// The header takes the keys: ↵ opens the data, the arrows move
		// the table, and the menu key opens its menu.
		header := ui.Row(c).Height(erHeaderH).Padding(0, 10).Gap(6).Background(pal.GridHeader).
			BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Cursor(ui.CursorMove).
			Focusable().Label("Table " + t.obj.Name + ": ↵ opens its data, the arrows move it")
		focused := e.focus != nil && t.schema == e.focus.Schema && t.obj.Name == e.focus.Name
		if focused || header.Focused() {
			header.Background(th.Accent.Alpha(0.18))
		}
		if header.Shortcut(0, ui.KeyEnter) {
			a.OpenTable(e.conn, e.database, t.obj, PageData)
		}
		for _, m := range erMoves {
			if header.Shortcut(0, m.key) {
				t.x, t.y = max(0, t.x+m.dx), max(0, t.y+m.dy)
				e.settle(t)
			}
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
			e.dragged = t
		} else if e.dragged == t && !header.Pressed() {
			// Dropped: a frame of the drag may not report the press.
			e.dragged = nil
			e.settle(t)
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
				row := ui.Row(c).Height(erRowH).Padding(0, 10).Gap(6)
				// Every other row tinted, so that a name reads across to
				// its type.
				if i%2 == 1 {
					row.Background(th.Surface)
				}
				if e.editable() == "" {
					row.Cursor(ui.CursorPointer).Tooltip("Drag onto another table to refer to it")
				}
				e.dragColumn(t, col.Name, row)
				row.Children(func() {
					switch {
					case col.PrimaryKey:
						ui.Icon(c, widgets.IconKey).FontSize(10).TextColor(c.Theme().Warning).Width(12)
					case isFKColumn(t, col.Name):
						ui.Text(c, "→").FontSize(10).TextColor(th.Accent).Width(12)
					default:
						ui.Box(c).Width(12)
					}
					ui.Text(c, col.Name).FontSize(12).SingleLine().Grow(1).Shrink(1)
					ui.Text(c, erType(col.Type)).Font(widgets.MonoFont).FontSize(10.5).TextColor(pal.Muted).SingleLine().MaxWidth(130)
					// The exception named, not the rule, after the type as
					// DDL writes it: most columns hold no NULL, which a
					// weight on each would only repeat.
					if col.Nullable && !col.PrimaryKey {
						ui.Text(c, "NULL").FontSize(9).TextColor(pal.Muted.Alpha(0.7)).SingleLine().Shrink(0)
					}
				})
			}
		})
	})
}

// erType is a column's type as the diagram shows it: the SQL standard's
// long names by the short ones PostgreSQL also takes, as varchar for
// character varying, which fit the box.
func erType(typ string) string {
	for _, a := range erTypeAliases {
		if rest, ok := strings.CutPrefix(typ, a.long); ok && (rest == "" || rest[0] == '(' || rest[0] == '[') {
			typ = a.short + rest
			break
		}
	}
	// A precision before the zone, as timestamp(3) with time zone.
	for _, z := range []struct{ suffix, base, short string }{
		{" with time zone", "timestamp", "timestamptz"}, {" with time zone", "time", "timetz"},
		{" without time zone", "timestamp", "timestamp"}, {" without time zone", "time", "time"},
	} {
		if head, ok := strings.CutSuffix(typ, z.suffix); ok {
			if rest, ok := strings.CutPrefix(head, z.base); ok && (rest == "" || rest[0] == '(') {
				return z.short + rest
			}
		}
	}
	return typ
}

var erTypeAliases = []struct{ long, short string }{
	{"character varying", "varchar"}, {"character", "char"}, {"bit varying", "varbit"},
}

func isFKColumn(t *erTable, name string) bool {
	return slices.ContainsFunc(t.fks, func(fk db.ForeignKey) bool { return slices.Contains(fk.Columns, name) })
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

// Where is the database and schema the diagram draws.
func (e *ERTab) Where() (database, schema string) { return e.database, e.schema }

// refOf is the table a foreign key points at, among those drawn.
func (e *ERTab) refOf(t *erTable, fk db.ForeignKey) *erTable {
	schema := cmp.Or(fk.RefSchema, t.schema)
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
		if t.cols, err = d.Dialect.Columns(ctx, d.Catalog(), schema, obj.Name); err != nil {
			return err
		}
		t.fks, _ = d.Dialect.ForeignKeys(ctx, d.Catalog(), schema, obj.Name)
		tables = append(tables, t)
		return nil
	}
	if err := add(focus.Schema, focus); err != nil {
		return nil, err
	}
	for _, fk := range tables[0].fks {
		schema := cmp.Or(fk.RefSchema, focus.Schema)
		if err := add(schema, db.Object{Schema: schema, Name: fk.RefTable, Kind: db.KindTable, Rows: -1}); err != nil {
			return nil, err
		}
	}
	refs, err := d.Dialect.ReferencedBy(ctx, d.Catalog(), focus.Schema, focus.Name)
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
		if slices.ContainsFunc(t.fks, func(fk db.ForeignKey) bool { return e.refOf(t, fk) == focus }) {
			left = append(left, t)
		} else {
			right = append(right, t)
		}
	}
	x := float32(24)
	if len(left) > 0 {
		stackTables(left, x)
		x += erBoxW + erGapX
	}
	focus.x, focus.y = x, 24
	stackTables(right, x+erBoxW+erGapX)
}

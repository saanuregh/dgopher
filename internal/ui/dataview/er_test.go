package dataview

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// erSchema draws n tables of a few to many columns, each referring to
// some of those before it, the first to itself and the last two to each
// other, in a cycle.
func erSchema(t *testing.T, n int, seed uint64) (*FakeHost, *ui.Tester, *ERTab) {
	t.Helper()
	a := NewFakeHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	r := rand.New(rand.NewPCG(seed, seed))
	e := &ERTab{a: a, conn: cn, schema: "main", byName: map[string]*erTable{}}
	for i := range n {
		tb := &erTable{schema: "main", obj: db.Object{Schema: "main", Name: fmt.Sprintf("t%d", i), Kind: db.KindTable}}
		for c := range r.IntN(20) {
			tb.cols = append(tb.cols, db.Column{Name: fmt.Sprintf("c%d", c), Type: "int"})
		}
		for j := range i {
			if r.IntN(4) == 0 {
				tb.fks = append(tb.fks, db.ForeignKey{Columns: []string{"c0"}, RefTable: fmt.Sprintf("t%d", j), RefColumns: []string{"c0"}})
			}
		}
		e.tables = append(e.tables, tb)
		e.byName[erKey("main", tb.obj.Name)] = tb
	}
	e.tables[0].fks = append(e.tables[0].fks, db.ForeignKey{Columns: []string{"c0"}, RefTable: "t0"})
	e.tables[n-2].fks = append(e.tables[n-2].fks, db.ForeignKey{Columns: []string{"c0"}, RefTable: fmt.Sprintf("t%d", n-1)})
	e.tables[n-1].fks = append(e.tables[n-1].fks, db.ForeignKey{Columns: []string{"c0"}, RefTable: fmt.Sprintf("t%d", n-2)})
	e.layout()
	a.AddTab(e)
	tt := ui.NewTester(a.View, 1400, 900)
	tt.Frame()
	return a, tt, e
}

// boxes are the tables' boxes, by name, in the canvas. Those the window
// shows in full are checked to be drawn as large as the layout takes
// them to be, which the rest are then taken to be too.
func boxes(t *testing.T, tt *ui.Tester, e *ERTab) map[string]ui.Rect {
	t.Helper()
	canvas, _ := tt.Find("Table " + e.tables[0].obj.Name)
	canvas.X, canvas.Y = canvas.X-e.tables[0].x+e.scroll.X, canvas.Y-e.tables[0].y+e.scroll.Y
	out := map[string]ui.Rect{}
	checked := 0
	for _, tb := range e.tables {
		r := ui.Rect{X: tb.x, Y: tb.y, W: erBoxW, H: tb.height()}
		if drawn, ok := tt.Find("Table " + tb.obj.Name); ok && drawn.Y+r.H < 850 && drawn.X+r.W < 1350 {
			checked++
			if drawn.H != r.H || drawn.X-canvas.X != r.X || drawn.Y-canvas.Y != r.Y {
				t.Fatalf("%s is drawn at %v, laid out at %v", tb.obj.Name, drawn, r)
			}
		}
		out[tb.obj.Name] = r
	}
	if checked == 0 {
		t.Fatal("no table is drawn in full")
	}
	return out
}

func noOverlap(t *testing.T, boxes map[string]ui.Rect) {
	t.Helper()
	for a, ra := range boxes {
		for b, rb := range boxes {
			if a < b && ra.X < rb.X+rb.W && rb.X < ra.X+ra.W && ra.Y < rb.Y+rb.H && rb.Y < ra.Y+ra.H {
				t.Fatalf("%s %v overlaps %s %v", a, ra, b, rb)
			}
		}
	}
}

func TestERLayoutNeverOverlaps(t *testing.T) {
	for _, n := range []int{2, 7, 30, 64} {
		for seed := range uint64(5) {
			_, tt, e := erSchema(t, n, seed)
			noOverlap(t, boxes(t, tt, e))
		}
	}
}

// A table dropped on another settles beside it, near the drop.
func TestERDropSettles(t *testing.T) {
	_, tt, e := erSchema(t, 12, 1)
	before := boxes(t, tt, e)
	from, _ := tt.Find("Table t5")
	onto, _ := tt.Find("Table t0")
	tt.Press(from.X+20, from.Y+10)
	tt.Frame()
	tt.Move(from.X+30, from.Y+20)
	tt.Frame()
	tt.Move(onto.X+20, onto.Y+15)
	tt.Frame()
	if e.dragged == nil {
		t.Fatal("the drag did not start")
	}
	tt.Release(onto.X+20, onto.Y+15)
	tt.Frame()
	tt.Frame()
	after := boxes(t, tt, e)
	noOverlap(t, after)
	moved, target := after["t5"], before["t0"]
	if moved == before["t5"] {
		t.Fatal("t5 did not move")
	}
	if dx, dy := moved.X-target.X, moved.Y-target.Y; dx*dx+dy*dy > 600*600 {
		t.Fatalf("t5 settled at %v, far from the drop on t0 %v", moved, target)
	}
	testutil.Snapshot(t, tt, "er-drop")
}

// The diagram shows the SQL standard's long type names by their short
// ones, a length and a precision kept; others as they are.
func TestERType(t *testing.T) {
	for in, want := range map[string]string{
		"character varying(256)":      "varchar(256)",
		"character varying":           "varchar",
		"character(2)":                "char(2)",
		"character varying(32)[]":     "varchar(32)[]",
		"timestamp with time zone":    "timestamptz",
		"timestamp(3) with time zone": "timestamptz(3)",
		"timestamp without time zone": "timestamp",
		"time with time zone":         "timetz",
		"bit varying(8)":              "varbit(8)",
		"bigint":                      "bigint",
		"characteristic":              "characteristic",
		"double precision":            "double precision",
	} {
		if got := erType(in); got != want {
			t.Errorf("erType(%q) = %q, want %q", in, got, want)
		}
	}
}

// linkSchema shows orders, whose customer_id points at customers.
func linkSchema(t *testing.T) (*FakeHost, *ui.Tester, *ERTab) {
	t.Helper()
	a := NewFakeHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	e := &ERTab{a: a, conn: cn, schema: "main", byName: map[string]*erTable{}}
	customers := &erTable{schema: "main", obj: db.Object{Schema: "main", Name: "customers", Kind: db.KindTable},
		cols: []db.Column{{Name: "id", Type: "int", PrimaryKey: true}, {Name: "name", Type: "text"}}}
	orders := &erTable{schema: "main", obj: db.Object{Schema: "main", Name: "orders", Kind: db.KindTable},
		cols: []db.Column{{Name: "id", Type: "int", PrimaryKey: true}, {Name: "customer_id", Type: "int"}},
		fks:  []db.ForeignKey{{Name: "orders_customer_fk", Columns: []string{"customer_id"}, RefTable: "customers", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}}
	for _, tb := range []*erTable{customers, orders} {
		e.tables = append(e.tables, tb)
		e.byName[erKey("main", tb.obj.Name)] = tb
	}
	e.layout()
	a.AddTab(e)
	tt := ui.NewTester(a.View, 1400, 900)
	tt.Frame()
	return a, tt, e
}

// The pointer near a line is on it, within a few DIPs; far, on none.
func TestERLinkHit(t *testing.T) {
	_, _, e := linkSchema(t)
	conns := e.connectors(ui.Rect{})
	if len(conns) != 1 {
		t.Fatalf("%d connectors", len(conns))
	}
	a, b := conns[0].pts[0], conns[0].pts[1]
	mid := ui.Point{X: (a.X + b.X) / 2, Y: (a.Y + b.Y) / 2}
	if i := linkAt(conns, mid.X, mid.Y+3); i != 0 {
		t.Fatalf("near the line: %d", i)
	}
	if i := linkAt(conns, mid.X, mid.Y+30); i != -1 {
		t.Fatalf("far from the line: %d", i)
	}
}

// Resting on a line shows its foreign key's card.
func TestERLinkCard(t *testing.T) {
	_, tt, e := linkSchema(t)
	canvas, ok := tt.Find("ER diagram of main")
	if !ok {
		t.Fatalf("no canvas: %q", tt.Texts())
	}
	conns := e.connectors(ui.Rect{})
	a, b := conns[0].pts[0], conns[0].pts[1]
	tt.Move(canvas.X+(a.X+b.X)/2, canvas.Y+(a.Y+b.Y)/2)
	tt.Frame()
	tt.Advance(widgets.DefaultHoverDelay)
	tt.Frame()
	tt.Frame()
	for _, s := range []string{"orders_customer_fk", "orders.customer_id", "customers.id", "CASCADE"} {
		if !tt.HasText(s) {
			t.Errorf("no %q in %q", s, tt.Texts())
		}
	}
}

// Alt+click on a line shows its card at once.
func TestERLinkAltClick(t *testing.T) {
	_, tt, e := linkSchema(t)
	canvas, _ := tt.Find("ER diagram of main")
	conns := e.connectors(ui.Rect{})
	a, b := conns[0].pts[0], conns[0].pts[1]
	tt.ClickAtWith(ui.Alt, canvas.X+(a.X+b.X)/2, canvas.Y+(a.Y+b.Y)/2)
	tt.Frame()
	tt.Frame()
	if !tt.HasText("orders_customer_fk") {
		t.Fatalf("no card: %q", tt.Texts())
	}
}

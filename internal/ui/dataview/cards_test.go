package dataview

import (
	"slices"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// cardConn is a connection whose catalog holds orders, with its columns
// and relations already read.
func cardConn(h *FakeHost) (*connection.Conn, db.Object) {
	cn := h.AddConn(db.Config{ID: "c", Name: "shop", Engine: db.SQLite})
	cn.Status = connection.StatusConnected
	obj := db.Object{Schema: "main", Name: "orders", Kind: db.KindTable, Rows: 12400, Bytes: 3 << 20, Comment: "One row per checkout."}
	key := connection.ObjectKey{Schema: "main", Name: "orders"}
	cn.Objects[connection.SchemaKey{Schema: "main"}] = []db.Object{obj}
	cn.Columns[key] = []db.Column{
		{Name: "status", Type: "varchar(16)"},
		{Name: "customer_id", Type: "bigint"},
		{Name: "id", Type: "bigint", PrimaryKey: true},
		{Name: "note", Type: "text", Nullable: true},
	}
	cn.Relations[key] = connection.Relations{
		FKs:  []db.ForeignKey{{Columns: []string{"customer_id"}, RefSchema: "main", RefTable: "customers", RefColumns: []string{"id"}}},
		Refs: []db.Reference{{Schema: "main", Table: "refunds", Columns: []string{"order_id"}, RefColumns: []string{"id"}}},
	}
	return cn, obj
}

// hoverTester shows "orders" with the card build fills.
func hoverTester(t *testing.T, build func(c *ui.Context, card *widgets.Card)) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Padding(20).AlignItems(ui.Start).Children(func() {
			name := ui.Text(c, "orders").Padding(4)
			widgets.HoverCard(c, name, widgets.CardSidebar, "orders", widgets.CardOptions{Place: widgets.PlaceBelow}, build)
		})
		widgets.FinishCards(c)
	}, 900, 700)
	r, _ := tt.Find("orders")
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	tt.Advance(widgets.DefaultHoverDelay)
	tt.Frame()
	return tt
}

// A table's card says what it is, how big, what it means and what it
// holds, keys first, and how it joins.
func TestTableCardShowsFacts(t *testing.T) {
	h := NewFakeHost(t)
	cn, obj := cardConn(h)
	tt := hoverTester(t, func(c *ui.Context, card *widgets.Card) { TableCard(c, card, h, cn, "", obj, TableCardOptions{}) })
	for _, s := range []string{"Table", "≈ 12k rows (estimate)", "3.0 MB", "One row per checkout.", "id", "customer_id", "→ customers", "refunds", "Open Data", "Structure"} {
		if !tt.HasText(s) && !hasPart(tt, s) {
			t.Errorf("no %q in %q", s, tt.Texts())
		}
	}
	id, _ := tt.Find("id")
	status, _ := tt.Find("status")
	if id.Y > status.Y {
		t.Errorf("the key is not first: id at %v, status at %v", id.Y, status.Y)
	}
}

// hasPart reports whether a text shown holds s.
func hasPart(tt *ui.Tester, s string) bool {
	return slices.ContainsFunc(tt.Texts(), func(x string) bool { return strings.Contains(x, s) })
}

// A click on a column inserts it, where the card offers that.
func TestTableCardInsertColumn(t *testing.T) {
	h := NewFakeHost(t)
	cn, obj := cardConn(h)
	var inserted []string
	tt := hoverTester(t, func(c *ui.Context, card *widgets.Card) {
		TableCard(c, card, h, cn, "", obj, TableCardOptions{Insert: func(col string) { inserted = append(inserted, col) }})
	})
	if err := tt.Click("status"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(inserted) != 1 || inserted[0] != "status" {
		t.Fatalf("inserted %q", inserted)
	}
}

// New Query opens an editor on the table.
func TestTableCardNewQuery(t *testing.T) {
	h := NewFakeHost(t)
	cn, obj := cardConn(h)
	tt := hoverTester(t, func(c *ui.Context, card *widgets.Card) {
		TableCard(c, card, h, cn, "", obj, TableCardOptions{NewQuery: true})
	})
	if err := tt.Click("New Query"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(h.Queries) != 1 || !strings.Contains(h.Queries[0], "orders") {
		t.Fatalf("queries %q", h.Queries)
	}
}

// A column's card shows its type and constraints as pills.
func TestColumnCardPills(t *testing.T) {
	h := NewFakeHost(t)
	cn, _ := cardConn(h)
	col := db.Column{Name: "customer_id", Type: "bigint", Comment: "The buyer."}
	tt := hoverTester(t, func(c *ui.Context, card *widgets.Card) {
		ColumnCard(c, card, h, cn, "", "main", "orders", col, ColumnCardOptions{})
	})
	for _, s := range []string{"customer_id", "bigint", "NOT NULL", "→ customers.id", "The buyer."} {
		if !tt.HasText(s) {
			t.Errorf("no %q in %q", s, tt.Texts())
		}
	}
}

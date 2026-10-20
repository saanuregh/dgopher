package app

import (
	"testing"
	"time"

	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Resting on a table of the sidebar opens its card at the sidebar's
// right; Open Data opens the table.
func TestSidebarTableCard(t *testing.T) {
	a := newBareApp(t)
	p, _ := a.addProject(newProjectDir(t, "acme"))
	tt := ui.NewTester(a.view, 1200, 760)
	a.openSampleIn(p)
	testutil.WaitFor(t, tt, "the sample", func() bool { return a.tabs().Len() == 1 })
	tt.Advance(time.Second)
	tt.Frame()
	r, ok := tt.Find("customers")
	if !ok {
		t.Fatalf("no customers: %q", tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	tt.Advance(widgets.DefaultHoverDelay)
	tt.Frame()
	testutil.WaitFor(t, tt, "the card", func() bool { return tt.HasText("Open Data") })
	card, _ := tt.Find("Open Data")
	if card.X < a.layout.SidebarWidth {
		t.Errorf("the card covers the sidebar: at %v, sidebar %v wide", card.X, a.layout.SidebarWidth)
	}
	if err := tt.Click("Open Data"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tab, ok := a.ActiveTab().(*dataview.TableTab); !ok || tab.Object.Name != "customers" {
		t.Fatalf("opened %T", a.ActiveTab())
	}
}

// A table's card opens from its menu's Show Card, and with Alt+click,
// which still chooses the row.
func TestSidebarShowCard(t *testing.T) {
	a := newBareApp(t)
	p, _ := a.addProject(newProjectDir(t, "acme"))
	tt := ui.NewTester(a.view, 1200, 760)
	a.openSampleIn(p)
	testutil.WaitFor(t, tt, "the sample", func() bool { return a.tabs().Len() == 1 })
	tt.Advance(time.Second)
	tt.Frame()
	if err := tt.RightClick("customers"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Show Card"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	tt.Move(1100, 700)
	tt.Advance(2 * time.Second)
	tt.Frame()
	if !tt.HasText("Open Data") {
		t.Fatalf("no card from the menu: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	r, _ := tt.Find("orders")
	tt.ClickAtWith(ui.Alt, r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	tt.Move(1100, 700)
	tt.Advance(2 * time.Second)
	tt.Frame()
	if !tt.HasText("Open Data") {
		t.Fatalf("no card from Alt+click: %q", tt.Texts())
	}
	if n := a.nav.tree.Item(a.nav.row); n.name != "orders" || a.tabs().Len() != 1 {
		t.Fatalf("Alt+click chose %q, %d tabs", n.name, a.tabs().Len())
	}
}

// A click on a table of the sidebar chooses it, its card notwithstanding:
// a thing with a card does not take the clicks meant for it.
func TestSidebarClickWithCards(t *testing.T) {
	a := newBareApp(t)
	p, _ := a.addProject(newProjectDir(t, "acme"))
	tt := ui.NewTester(a.view, 1200, 760)
	a.openSampleIn(p)
	testutil.WaitFor(t, tt, "the sample", func() bool { return a.tabs().Len() == 1 })
	tt.Advance(time.Second)
	tt.Frame()
	r, _ := tt.Find("orders")
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if n := a.nav.tree.Item(a.nav.row); n.name != "orders" {
		t.Fatalf("the click chose %q", n.name)
	}
}

// Alt+click anywhere in a grid's cell, its padding too, shows its card;
// a date the database keeps as text has one.
func TestGridCellCardAltClick(t *testing.T) {
	a := newBareApp(t)
	p, _ := a.addProject(newProjectDir(t, "acme"))
	tt := ui.NewTester(a.view, 1300, 800)
	a.openSampleIn(p)
	testutil.WaitFor(t, tt, "the sample", func() bool { return a.tabs().Len() == 1 })
	tt.Advance(time.Second)
	tt.Frame()
	if err := tt.RightClick("orders"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Open Data"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the rows", func() bool {
		tb, ok := a.ActiveTab().(*dataview.TableTab)
		return ok && tb.Object.Name == "orders" && tt.HasText("ordered_at")
	})
	tt.Advance(time.Second)
	tt.Frame()
	for _, c := range []struct{ header, card string }{{"customer_id", "Open Row"}, {"ordered_at", "Relative"}} {
		h, _ := tt.Find(c.header)
		// Near the cell's left edge, in its padding.
		tt.ClickAtWith(ui.Alt, h.X+3, h.Y+h.H+15)
		tt.Frame()
		tt.Frame()
		if !tt.HasText(c.card) {
			t.Fatalf("no card on Alt+click in %s: %q", c.header, tt.Texts())
		}
		tt.Key(0, ui.KeyEscape)
		tt.Frame()
		tt.Advance(time.Second)
	}
}

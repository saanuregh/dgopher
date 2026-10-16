package widgets_test

import (
	"testing"
	"time"

	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// cardTester shows rows "alpha" and "beta", each with a card naming it,
// a Copy button and an Open button; opened counts Open's clicks.
func cardTester(t *testing.T, opened *int) *ui.Tester {
	t.Helper()
	return ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Padding(40).Gap(4).AlignItems(ui.Start).Children(func() {
			for _, name := range []string{"alpha", "beta"} {
				row := ui.Text(c.Key(name), name).Padding(4)
				widgets.HoverCard(c, row, widgets.CardSidebar, "row:"+name, widgets.CardOptions{Place: widgets.PlaceRight}, func(c *ui.Context, card *widgets.Card) {
					card.Header(c, widgets.IconTable, widgets.ToneTable, "card of "+name, "")
					card.Columns(c, []widgets.CardColumn{{Name: "id", Type: "bigint", Key: true}, {Name: "note", Type: "text"}}, widgets.ColumnsOptions{})
					card.Actions(c, func(a *widgets.CardActions) {
						a.Copy(c, "Copy Name", name)
						if a.Button(c, "Open", "") {
							*opened++
						}
					})
				})
			}
			widgets.FinishCards(c)
		})
	}, 800, 500)
}

func moveTo(t *testing.T, tt *ui.Tester, s string) {
	t.Helper()
	r, ok := tt.Find(s)
	if !ok {
		t.Fatalf("no %q: %q", s, tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
}

// The card opens once the pointer rests on its thing, not before.
func TestHoverCardOpensAfterRest(t *testing.T) {
	var opened int
	tt := cardTester(t, &opened)
	moveTo(t, tt, "alpha")
	if tt.HasText("card of alpha") {
		t.Fatal("the card opened at once")
	}
	tt.Advance(widgets.DefaultHoverDelay)
	tt.Frame()
	if !tt.HasText("card of alpha") {
		t.Fatalf("no card after the rest: %q", tt.Texts())
	}
}

// Once a card shows, the next thing's opens at once.
func TestHoverCardWarm(t *testing.T) {
	var opened int
	tt := cardTester(t, &opened)
	moveTo(t, tt, "alpha")
	tt.Advance(widgets.DefaultHoverDelay)
	tt.Frame()
	moveTo(t, tt, "beta")
	tt.Frame()
	if !tt.HasText("card of beta") || tt.HasText("card of alpha") {
		t.Fatalf("beta's card not at once: %q", tt.Texts())
	}
}

// The pointer can go into the card and click its buttons; Copy says so.
func TestHoverCardStaysInside(t *testing.T) {
	var opened int
	tt := cardTester(t, &opened)
	moveTo(t, tt, "alpha")
	tt.Advance(widgets.DefaultHoverDelay)
	tt.Frame()
	moveTo(t, tt, "Copy Name")
	tt.Advance(time.Second)
	tt.Frame()
	if !tt.HasText("card of alpha") {
		t.Fatal("the card closed with the pointer in it")
	}
	if err := tt.Click("Copy Name"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.Clipboard() != "alpha" || !tt.HasText("Copied") {
		t.Fatalf("clipboard %q, texts %q", tt.Clipboard(), tt.Texts())
	}
	if err := tt.Click("Open"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if opened != 1 || tt.HasText("card of alpha") {
		t.Fatalf("opened %d, card still shows %v", opened, tt.HasText("card of alpha"))
	}
}

// The card closes a moment after the pointer left, and with Escape.
func TestHoverCardCloses(t *testing.T) {
	var opened int
	tt := cardTester(t, &opened)
	moveTo(t, tt, "alpha")
	tt.Advance(widgets.DefaultHoverDelay)
	tt.Frame()
	tt.Move(5, 480)
	tt.Frame()
	if !tt.HasText("card of alpha") {
		t.Fatal("closed with no grace")
	}
	tt.Advance(time.Second)
	tt.Frame()
	if tt.HasText("card of alpha") {
		t.Fatal("still open after the pointer left")
	}
	moveTo(t, tt, "alpha")
	tt.Advance(widgets.DefaultHoverDelay)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if tt.HasText("card of alpha") {
		t.Fatal("Escape left it open")
	}
}

// A pinned card stays with the pointer gone, until closed.
func TestHoverCardPin(t *testing.T) {
	var opened int
	tt := cardTester(t, &opened)
	moveTo(t, tt, "alpha")
	tt.Advance(widgets.DefaultHoverDelay)
	tt.Frame()
	// The pin is the header's last button, at the card's right end: the
	// card starts 40 DIPs before its name, past the icon.
	r, _ := tt.Find("card of alpha")
	pin := func() { tt.ClickAt(r.X-40+widgets.CardWidth-22, r.Y+r.H/2); tt.Frame() }
	pin()
	tt.Move(5, 480)
	tt.Advance(2 * time.Second)
	tt.Frame()
	if !tt.HasText("card of alpha") {
		t.Fatalf("the pinned card closed: %q", tt.Texts())
	}
	r, _ = tt.Find("card of alpha")
	pin()
	if tt.HasText("card of alpha") {
		t.Fatal("close left the pinned card")
	}
}

// A card asked for, as by a menu's Show Card, opens at once and stays
// with the pointer away, until Escape or a press outside it.
func TestShowCard(t *testing.T) {
	var opened int
	var ask bool
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Padding(40).Gap(4).AlignItems(ui.Start).Children(func() {
			row := ui.Text(c, "alpha").Padding(4)
			if ask {
				widgets.ShowCard(c, "row:alpha")
				ask = false
			}
			widgets.HoverCard(c, row, widgets.CardSidebar, "row:alpha", widgets.CardOptions{Place: widgets.PlaceRight}, func(c *ui.Context, card *widgets.Card) {
				card.Header(c, widgets.IconTable, widgets.ToneTable, "card of alpha", "")
			})
			ui.Text(c, "elsewhere").Padding(4)
		})
		widgets.FinishCards(c)
	}, 800, 500)
	_ = opened
	tt.Move(5, 480)
	ask = true
	tt.Frame()
	tt.Frame()
	if !tt.HasText("card of alpha") {
		t.Fatalf("not shown at once: %q", tt.Texts())
	}
	tt.Advance(2 * time.Second)
	tt.Frame()
	if !tt.HasText("card of alpha") {
		t.Fatal("an asked-for card closed with the pointer away")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if tt.HasText("card of alpha") {
		t.Fatal("Escape left it open")
	}
	ask = true
	tt.Frame()
	tt.Frame()
	if err := tt.Click("elsewhere"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("card of alpha") {
		t.Fatal("a press outside left it open")
	}
}

package widgets_test

import (
	"testing"

	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// menuTester shows a button with a menu: Open, a disabled Locked, a
// checked Wrap, and a submenu Recent of a.sql and b.sql; chosen records
// what was chosen.
func menuTester(t *testing.T, chosen *[]string) *ui.Tester {
	t.Helper()
	wrap := true
	return ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Padding(10).Children(func() {
			widgets.MenuOn(c, ui.Button(c, "Menu"), func(m *widgets.Menu) {
				if m.Item("Open").Shortcut(ui.Cmd, ui.KeyO).Chosen() {
					*chosen = append(*chosen, "Open")
				}
				if m.Item("Locked").Disabled(true).Chosen() {
					*chosen = append(*chosen, "Locked")
				}
				m.Separator()
				if m.Item("Wrap").Checked(wrap).Chosen() {
					wrap = !wrap
					*chosen = append(*chosen, "Wrap")
				}
				m.Submenu("Recent", func(m *widgets.Menu) {
					for _, f := range []string{"a.sql", "b.sql"} {
						if m.Item(f).Chosen() {
							*chosen = append(*chosen, f)
						}
					}
				})
			})
		})
	}, 600, 400)
}

// A click on the button opens the menu, one on an item chooses it, once,
// and closes the menu; a disabled item does neither.
func TestMenuClick(t *testing.T) {
	var chosen []string
	tt := menuTester(t, &chosen)
	if err := tt.Click("Menu"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, s := range []string{"Open", "Locked", "Wrap", "Recent"} {
		if !tt.HasText(s) {
			t.Fatalf("no %s in the menu: %q", s, tt.Texts())
		}
	}
	tt.Click("Locked")
	tt.Frame()
	if len(chosen) != 0 || !tt.HasText("Open") {
		t.Fatalf("a disabled item: chosen %q, open %v", chosen, tt.HasText("Open"))
	}
	tt.Click("Open")
	tt.Frame()
	if len(chosen) != 1 || chosen[0] != "Open" || tt.HasText("Wrap") {
		t.Fatalf("chosen %q, still open %v", chosen, tt.HasText("Wrap"))
	}
}

// A submenu opens as the pointer goes onto its item, and a click on its
// item chooses it.
func TestMenuSubmenu(t *testing.T) {
	var chosen []string
	tt := menuTester(t, &chosen)
	tt.Click("Menu")
	tt.Frame()
	r, _ := tt.Find("Recent")
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if err := tt.Click("b.sql"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	if len(chosen) != 1 || chosen[0] != "b.sql" || tt.HasText("Recent") {
		t.Fatalf("chosen %q, still open %v", chosen, tt.HasText("Recent"))
	}
}

// The arrows move past the disabled item and the separator, Right opens a
// submenu, Enter chooses, and Escape closes the menu.
func TestMenuKeys(t *testing.T) {
	var chosen []string
	tt := menuTester(t, &chosen)
	tt.Click("Menu")
	tt.Frame()
	tt.Key(0, ui.KeyDown) // Open
	tt.Key(0, ui.KeyDown) // past Locked and the line: Wrap
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(chosen) != 1 || chosen[0] != "Wrap" {
		t.Fatalf("chosen %q", chosen)
	}
	tt.Click("Menu")
	tt.Frame()
	tt.Key(0, ui.KeyEnd) // Recent
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	tt.Key(0, ui.KeyDown) // b.sql
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(chosen) != 2 || chosen[1] != "b.sql" {
		t.Fatalf("chosen %q", chosen)
	}
	tt.Click("Menu")
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if tt.HasText("Open") {
		t.Fatal("Escape left the menu open")
	}
}

// Moving down the menu, the item left loses its highlight in the frame
// the pointer moved in: drawn before the one now under the pointer, it
// learns of it all the same.
func TestMenuHoverMovesDown(t *testing.T) {
	var chosen []string
	tt := menuTester(t, &chosen)
	tt.Click("Menu")
	tt.Frame()
	open, _ := tt.Find("Open")
	wrap, _ := tt.Find("Wrap")
	recent, _ := tt.Find("Recent")
	tt.Move(open.X+2, open.Y+open.H/2)
	tt.Move(wrap.X+2, wrap.Y+wrap.H/2)
	img := tt.Image()
	scale := float32(img.Bounds().Dx()) / 600
	// The highlight is the accent, a blue; the rest a gray.
	lit := func(r ui.Rect) bool {
		cr, _, cb, _ := img.At(int((r.X+4)*scale), int((r.Y+r.H/2)*scale)).RGBA()
		return cb > cr+0x2000
	}
	if lit(open) || lit(recent) || !lit(wrap) {
		t.Fatalf("lit: Open %v (left behind), Recent %v, Wrap %v (under the pointer)", lit(open), lit(recent), lit(wrap))
	}
}

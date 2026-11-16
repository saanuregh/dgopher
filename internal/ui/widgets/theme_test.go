package widgets_test

import (
	"testing"

	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// A chosen row stays a light tint in a window that is not in front, gray
// there rather than the accent's, as macOS's lists show it: not the dark
// gray MyGo fades the accent to by default.
func TestChosenRowInInactiveWindow(t *testing.T) {
	for _, dark := range []bool{false, true} {
		chosen := 0
		list := ui.ListState{Selected: &chosen}
		tt := ui.NewTester(func(c *ui.Context) {
			th := ui.LightTheme()
			if dark {
				th = ui.DarkTheme()
			}
			c.SetTheme(th)
			ui.Box(c).Grow(1).Background(th.Background).Children(func() {
				widgets.ChosenRows(c, func() {
					ui.List(c, &list, 3, func(i int) {
						ui.Row(c).Padding(8, 16).Children(func() { ui.Text(c, "row") })
					}).Grow(1)
				})
			})
		}, 200, 120)
		tt.Frame()
		row, ok := tt.Find("row")
		if !ok {
			t.Fatal("no row")
		}
		at := func() (r, g, b int) {
			p := tt.Image().RGBAAt(int(row.X+row.W+20), int(row.Y+row.H/2))
			return int(p.R), int(p.G), int(p.B)
		}
		ar, ag, ab := at()
		tt.SetWindowActive(false)
		tt.Frame()
		ir, ig, ib := at()
		if max(ir, ig, ib)-min(ir, ig, ib) > 6 {
			t.Errorf("dark %v: the inactive row is not gray: %d %d %d", dark, ir, ig, ib)
		}
		if d := (ar + ag + ab - ir - ig - ib) / 3; d > 40 || d < -40 {
			t.Errorf("dark %v: the inactive row %d %d %d is far from the active tint %d %d %d", dark, ir, ig, ib, ar, ag, ab)
		}
	}
}

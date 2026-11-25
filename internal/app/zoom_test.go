package app

import (
	"math"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/keymap"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// ⌘= and ⌘- zoom the whole window, the sidebar with the rest, a step at a
// time, and ⌘K ⌘0 puts it back; the zoom is kept in the layout.
func TestWindowZoom(t *testing.T) {
	a, tt, _ := filterApp(t)
	height := func() float32 {
		t.Helper()
		tt.Frame()
		tt.Frame()
		r, ok := tt.Find("Filter the navigator")
		if !ok {
			t.Fatalf("the sidebar's filter does not show: %q", tt.Texts())
		}
		return r.H
	}
	actual := height()
	tt.Key(ui.Cmd, ui.KeyEqual)
	tt.Key(ui.Cmd, ui.KeyEqual)
	if got := height(); a.layout.Zoom != 1.25 || math.Abs(float64(got/actual-1.25)) > 0.02 {
		t.Fatalf("after ⌘= twice the zoom is %v and the sidebar's filter %v high, was %v: want 1.25 of it", a.layout.Zoom, got, actual)
	}
	tt.Key(ui.Cmd, ui.KeyMinus)
	tt.Key(ui.Cmd, ui.KeyMinus)
	tt.Key(ui.Cmd, ui.KeyMinus)
	if got := height(); a.layout.Zoom != 0.9 || got >= actual {
		t.Fatalf("after ⌘- three times the zoom is %v and the filter %v high, was %v", a.layout.Zoom, got, actual)
	}
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Key(ui.Cmd, ui.Key0)
	if got := height(); a.layout.Zoom != 1 || got != actual {
		t.Fatalf("after ⌘K ⌘0 the zoom is %v and the filter %v high, was %v", a.layout.Zoom, got, actual)
	}
	// The steps end, and a zoom between two goes to the next one.
	for range 40 {
		a.menuAction(keymap.ZoomIn)()
	}
	if a.layout.Zoom != 3 {
		t.Errorf("zoomed in as far as it goes, the zoom is %v, want 3", a.layout.Zoom)
	}
	a.layout.Zoom = 1.2
	a.menuAction(keymap.ZoomIn)()
	if a.layout.Zoom != 1.25 {
		t.Errorf("a step in from 1.2 is %v, want 1.25", a.layout.Zoom)
	}
	a.layout.Zoom = 1.2
	a.menuAction(keymap.ZoomOut)()
	if a.layout.Zoom != 1.1 {
		t.Errorf("a step out from 1.2 is %v, want 1.1", a.layout.Zoom)
	}
}

// The keys zoom the window from a grid too, which had them for its text,
// and a pinch zooms it over the sidebar as over an editor.
func TestWindowZoomFromAGridAndByAPinch(t *testing.T) {
	a, tt, cn := filterApp(t)
	for _, o := range cn.Objects[connection.SchemaKey{Schema: cn.DefaultSchema}] {
		if o.Name == "orders" {
			a.OpenTable(cn, "", o, dataview.PageData)
		}
	}
	testutil.WaitFor(t, tt, "the table's grid", func() bool { _, ok := tt.Find("Filter rows"); return ok })
	grid, _ := tt.Access("Results")
	tt.ClickAt(grid.Bounds.X+grid.Bounds.W/2, grid.Bounds.Y+grid.Bounds.H/2)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyEqual)
	tt.Frame()
	if a.layout.Zoom != 1.1 {
		t.Fatalf("⌘= with the grid in focus left the zoom %v, want 1.1", a.layout.Zoom)
	}
	a.layout.Zoom = 1
	tt.Frame()
	side, _ := tt.Access("Navigator")
	x, y := side.Bounds.X+side.Bounds.W/2, side.Bounds.Y+side.Bounds.H/2
	tt.Move(x, y)
	tt.Pinch(x, y, 1.3)
	tt.Frame()
	if a.layout.Zoom != 1.1 {
		t.Errorf("a pinch out over the sidebar left the zoom %v, want 1.1", a.layout.Zoom)
	}
	grid, _ = tt.Access("Results")
	x, y = grid.Bounds.X+grid.Bounds.W/2, grid.Bounds.Y+grid.Bounds.H/2
	tt.Move(x, y)
	tt.Pinch(x, y, 0.7)
	tt.Frame()
	if a.layout.Zoom != 1 {
		t.Errorf("a pinch in over the grid left the zoom %v, want 1", a.layout.Zoom)
	}
}

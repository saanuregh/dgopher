package dataview

import (
	"testing"

	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// A tint set from outside colours the cells it names, and an edit's fill
// still wins over it.
func TestViewerTint(t *testing.T) {
	_, _, tb, _ := openNumbers(t, 3, 100)
	v := tb.view
	pal := &widgets.Palette{Modified: ui.Hex("#111111"), Deleted: ui.Hex("#222222"), Inserted: ui.Hex("#333333")}
	if got := v.grid.cellFill(&v.src, pal, 0, 1, false); got != (ui.Color{}) {
		t.Fatalf("an untinted cell is filled %v", got)
	}
	v.SetTint(func(row, col int, p *widgets.Palette) (ui.Color, bool) { return p.Deleted, col == 1 })
	if got := v.grid.cellFill(&v.src, pal, 0, 1, false); got != pal.Deleted {
		t.Errorf("a tinted cell is filled %v, want %v", got, pal.Deleted)
	}
	if got := v.grid.cellFill(&v.src, pal, 0, 0, false); got != (ui.Color{}) {
		t.Errorf("a cell the tint leaves is filled %v", got)
	}
	if got := v.grid.cellFill(&v.src, pal, 0, 1, true); got != pal.Modified {
		t.Errorf("an edited cell is filled %v, want the edit's %v", got, pal.Modified)
	}
}

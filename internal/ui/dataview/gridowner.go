package dataview

import (
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// What an owner outside the package, which keeps edits of its own, sets
// and reads of a grid it shows read only.

// SetTint colours the cells tint names, by data row and column, as the
// owner's pending edits mark them.
func (g *Grid) SetTint(tint func(row, col int, pal *widgets.Palette) (ui.Color, bool)) {
	g.tint = tint
}

// SelectedRows are the data rows chosen, in the order shown: the cursor's
// row when none is.
func (g *Grid) SelectedRows(src *Source) []int { return g.selectedRows(src) }

// Reset forgets the choice, order and widths of the rows shown, for rows
// of another result.
func (g *Grid) Reset() { g.reset() }

package dataview

import (
	"dgopher/internal/keymap"

	"github.com/egoist/mygo/ui"
)

// multiCell reports whether more than one cell is chosen.
func (g *Grid) multiCell(src *Source) bool {
	return g.selection.Len() > 1 || len(g.selectedCols(src)) > 1
}

// selectRows chooses the whole rows of the rows chosen.
func (g *Grid) selectRows() {
	c := g.List.Cells
	g.List.Cells = ui.ColumnRange{Cursor: c.Cursor, Anchor: c.Cursor, All: true}
}

// selectColumns chooses every row, in the columns chosen.
func (g *Grid) selectColumns() {
	c := g.List.Cells
	if c.All {
		c.Anchor = c.Cursor
	}
	g.List.SelectColumns(c.Anchor, c.Cursor)
}

// selectAll chooses every row, in every column.
func (g *Grid) selectAll(order []int) {
	g.selGen++
	for _, r := range order {
		g.selection.Add(r)
	}
	g.selectRows()
}

// selectWhere chooses the rows shown that keep, alone, and keeps the
// cursor on one of them.
func (g *Grid) selectWhere(order []int, keep func(row int) bool) {
	g.selGen++
	g.selection.Clear()
	first := -1
	for i, r := range order {
		if keep(r) {
			g.selection.Add(r)
			if first < 0 || i == g.SelRow {
				first = i
			}
		}
	}
	if first >= 0 {
		g.SelRow = first
	}
}

// selectMatching chooses the rows whose value in col is that of row's.
func (g *Grid) selectMatching(src *Source, order []int, row, col int) {
	v := g.plainValue(src, row, col)
	g.selectWhere(order, func(r int) bool {
		return sameValue(g.plainValue(src, r, col), v)
	})
	g.setCursorCol(col)
}

// selectChanged chooses the rows with pending changes.
func (g *Grid) selectChanged(src *Source, order []int) {
	g.selectWhere(order, func(r int) bool {
		return g.edits != nil && (r >= len(src.Rows) || g.edits.deleted[r] || len(g.edits.updates[r]) > 0)
	})
}

// invertSelection chooses the rows shown that were not chosen, alone.
func (g *Grid) invertSelection(order []int) {
	was := map[int]bool{}
	for r := range g.selection.All() {
		was[r] = true
	}
	g.selectWhere(order, func(r int) bool { return !was[r] })
}

// collapse leaves the cursor's cell chosen, alone.
func (g *Grid) collapse(order []int) {
	g.selGen++
	g.selection.Clear()
	if g.SelRow >= 0 && g.SelRow < len(order) {
		g.selection.Add(order[g.SelRow])
	}
	g.setCursorCol(g.selCol)
}

// selectMenu is the Select submenu of a cell.
func (g *Grid) selectMenu(m *ui.Menu, src *Source, row, col int) {
	order := g.ViewOrder(src)
	m.Submenu("Select", func(m *ui.Menu) {
		if keymap.Item(m.Item("Select Row"), keymap.SelectRow).Chosen() {
			g.selectRows()
		}
		if keymap.Item(m.Item("Select Column"), keymap.SelectColumn).Chosen() {
			g.selectColumns()
		}
		if m.Item("Select All").Shortcut(ui.Cmd, ui.KeyA).Chosen() {
			g.selectAll(order)
		}
		m.Separator()
		if col != wholeRow && keymap.Item(m.Item("Select Matching Rows"), keymap.SelectMatching).Chosen() {
			g.selectMatching(src, order, row, col)
		}
		if keymap.Item(m.Item("Select Changed Rows"), keymap.SelectChanged).Disabled(g.edits == nil || g.edits.count() == 0).Chosen() {
			g.selectChanged(src, order)
		}
		if keymap.Item(m.Item("Invert Selection"), keymap.InvertSelection).Chosen() {
			g.invertSelection(order)
		}
	})
}

// selectKeys are the keys that choose cells, after the table is built. It
// reports whether Escape left one cell chosen, which it then takes.
func (g *Grid) selectKeys(c *ui.Context, src *Source, order []int) (escaped bool) {
	pressed := func(id string) bool { return keymap.ListPressed(c, &g.List, id) }
	row := -1
	if g.SelRow >= 0 && g.SelRow < len(order) {
		row = order[g.SelRow]
	}
	switch {
	case pressed(keymap.SelectRow):
		g.selectRows()
	case pressed(keymap.SelectColumn):
		g.selectColumns()
	case len(order) > 0 && pressed(keymap.ExtendToFirstRow):
		g.List.ExtendTo(0)
	case len(order) > 0 && pressed(keymap.ExtendToLastRow):
		g.List.ExtendTo(len(order) - 1)
	case row >= 0 && g.selCol < len(src.Cols) && pressed(keymap.SelectMatching):
		g.selectMatching(src, order, row, g.selCol)
	case pressed(keymap.SelectChanged):
		g.selectChanged(src, order)
	case pressed(keymap.InvertSelection):
		g.invertSelection(order)
	case g.editing == nil && g.multiCell(src) && g.List.Shortcut(c, 0, ui.KeyEscape):
		g.collapse(order)
		return true
	}
	return false
}

package dataview

import (
	"fmt"
	"strings"

	"dgopher/internal/db"

	"github.com/egoist/mygo/ui"
)

// duplicateRow adds a new row with the values of a row, its key columns
// left to their defaults so that the copy does not collide.
func (g *Grid) duplicateRow(src *Source, row int, keys map[int]bool) {
	g.checkpoint()
	copyRow := make([]any, len(src.Cols))
	for col := range src.Cols {
		v, _ := g.value(src, row, col)
		if keys[col] {
			v = unset
		}
		copyRow[col] = v
	}
	g.edits.inserted = append(g.edits.inserted, copyRow)
	g.orderKey = ""
	order := g.ViewOrder(src)
	g.SelRow = len(order) - 1
	g.selection.Clear()
	g.List.ScrollToEnd()
}

// setDefault sets a cell to its column's default.
func (g *Grid) setDefault(src *Source, row, col int) {
	g.checkpoint()
	if row >= len(src.Rows) {
		g.edits.inserted[row-len(src.Rows)][col] = unset
		return
	}
	g.setValue(src, row, col, db.Default)
}

// revertCell forgets the change of a cell.
func (g *Grid) revertCell(src *Source, row, col int) {
	g.checkpoint()
	if row >= len(src.Rows) {
		g.edits.inserted[row-len(src.Rows)][col] = unset
		return
	}
	delete(g.edits.updates[row], col)
}

// revertRow forgets the changes of a row: its edits, its deletion, or the
// row itself when it was added.
func (g *Grid) revertRow(src *Source, row int) {
	g.checkpoint()
	if row >= len(src.Rows) {
		i := row - len(src.Rows)
		g.edits.inserted = append(g.edits.inserted[:i], g.edits.inserted[i+1:]...)
	} else {
		delete(g.edits.updates, row)
		delete(g.edits.deleted, row)
	}
	g.orderKey = ""
}

// editMenu is the Edit submenu of a cell of an editable grid.
func (g *Grid) editMenu(m *ui.Menu, src *Source, row, col int) {
	m.Submenu("Edit", func(m *ui.Menu) {
		if m.Item("Edit Cell").Shortcut(0, ui.KeyF2).Chosen() {
			g.startEdit(src, row, col)
		}
		m.Separator()
		if m.Item("Add Row").Shortcut(ui.Alt, ui.KeyInsert).Chosen() {
			g.addRow(src)
		}
		if m.Item("Duplicate Row").Shortcut(ui.Cmd|ui.Alt, ui.KeyInsert).Chosen() {
			g.duplicateRow(src, row, g.keyCols)
		}
		if m.Item("Delete Row").Shortcut(0, ui.KeyDelete).Chosen() {
			g.deleteSelected(src)
		}
		m.Separator()
		if m.Item("Set to NULL").Chosen() {
			g.checkpoint()
			g.setValue(src, row, col, nil)
		}
		defaultOK := row >= len(src.Rows) || !g.noDefaultUpdate
		// ⌘⌫ deletes rows here, as on a Mac keyboard without Delete: DEFAULT
		// has no key of its own.
		if m.Item("Set to DEFAULT").Disabled(!defaultOK).Chosen() {
			g.setDefault(src, row, col)
		}
		m.Separator()
		if m.Item("Revert Cell").Chosen() {
			g.revertCell(src, row, col)
		}
		if m.Item("Revert Row").Chosen() {
			g.revertRow(src, row)
		}
	})
}

// headerMenu is the context menu of a column's header.
func (g *Grid) headerMenu(m *ui.Menu, a Host, src *Source, col int) {
	name := src.Cols[col].Name
	if m.Item("Copy Column Name").Chosen() {
		a.WriteClipboard(name)
	}
	if m.Item("Copy All Column Names").Chosen() {
		var names []string
		for i, c := range src.Cols {
			if !g.hidden[i] {
				names = append(names, c.Name)
			}
		}
		a.WriteClipboard(strings.Join(names, ", "))
	}
	m.Separator()
	g.orderMenu(m, col)
	m.Separator()
	pin := "Pin Column"
	if g.pinned[col] {
		pin = "Unpin Column"
	}
	if m.Item(pin).Chosen() {
		if g.pinned == nil {
			g.pinned = map[int]bool{}
		}
		g.pinned[col] = !g.pinned[col]
	}
	if m.Item("Fit Width to Values").Chosen() {
		g.setWidth(col, g.fitWidth(a, src, col))
	}
	if m.Item("Fit All Columns to Screen").Chosen() {
		g.fitToScreen(src)
	}
	m.Separator()
	if m.Item("Hide Column").Chosen() {
		g.hide(col)
	}
	if m.Item("Hide Columns with No Data").Chosen() {
		for i := range src.Cols {
			if g.columnEmpty(src, i) {
				g.hide(i)
			}
		}
	}
	g.showAllMenu(m)
}

// showAllMenu offers to show the hidden columns again.
func (g *Grid) showAllMenu(m *ui.Menu) {
	n := 0
	for _, h := range g.hidden {
		if h {
			n++
		}
	}
	if m.Item(fmt.Sprintf("Show All Columns (%d hidden)", n)).Disabled(n == 0).Chosen() {
		clear(g.hidden)
	}
}

func (g *Grid) hide(col int) {
	if g.hidden == nil {
		g.hidden = map[int]bool{}
	}
	g.hidden[col] = true
	if g.selCol == col {
		g.selCol = 0
	}
}

// columnEmpty reports whether a column is NULL in every row read.
func (g *Grid) columnEmpty(src *Source, col int) bool {
	for _, r := range src.Rows {
		if r[col] != nil {
			return false
		}
	}
	return len(src.Rows) > 0
}

// fitWidth is the width that shows a column's values read, up to a limit.
func (g *Grid) fitWidth(a Host, src *Source, col int) float32 {
	n := len([]rune(src.Cols[col].Name)) + 2
	for r := 0; r < len(src.Rows) && r < 2000; r++ {
		n = max(n, len([]rune(cellText(a.Settings().ViewFormat.Format(src.Rows[r][col]), 200))))
	}
	return float32(min(max(n*7+28, 60), 900))
}

func (g *Grid) setWidth(col int, w float32) {
	if g.List.Columns.Widths == nil {
		g.List.Columns.Widths = map[string]float32{}
	}
	g.List.Columns.Widths[colID(col)] = w
}

// fitToScreen shares the table's width among its visible columns, as
// they ask for room.
func (g *Grid) fitToScreen(src *Source) {
	widths := g.columnWidths(src)
	var want float32
	var cols []int
	for i := range src.Cols {
		if !g.hidden[i] {
			cols = append(cols, i)
			want += widths[i]
		}
	}
	room := g.viewW - 60 // the row numbers
	if room <= 0 || want <= 0 {
		return
	}
	for _, i := range cols {
		g.setWidth(i, max(40, widths[i]*room/want))
	}
}

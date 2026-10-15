package dataview

import (
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/keymap"

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

func (g *Grid) setCellDefault(src *Source, row, col int) {
	if row >= len(src.Rows) {
		g.edits.inserted[row-len(src.Rows)][col] = unset
		return
	}
	g.setValue(src, row, col, db.Default)
}

// defaultAllowed says whether a cell of row can be set to DEFAULT: added
// rows always, read rows only where the database allows it in an UPDATE.
func (g *Grid) defaultAllowed(src *Source, row int) bool {
	return row >= len(src.Rows) || !g.noDefaultUpdate
}

// revertCell forgets the change of a cell.
func (g *Grid) revertCell(src *Source, row, col int) {
	g.checkpoint()
	g.forgetCell(src, row, col)
}

func (g *Grid) forgetCell(src *Source, row, col int) {
	if row >= len(src.Rows) {
		g.edits.inserted[row-len(src.Rows)][col] = unset
		return
	}
	delete(g.edits.updates[row], col)
}

func (g *Grid) forgetRow(src *Source, row int) {
	if row >= len(src.Rows) {
		i := row - len(src.Rows)
		g.edits.inserted = append(g.edits.inserted[:i], g.edits.inserted[i+1:]...)
	} else {
		delete(g.edits.updates, row)
		delete(g.edits.deleted, row)
	}
	g.orderKey = ""
}

// editableSelection calls fn on every chosen cell that can be edited,
// skipping read-only columns and rows marked deleted, as paste does.
func (g *Grid) editableSelection(src *Source, fn func(row, col int)) {
	cols := g.selectedCols(src)
	for _, row := range g.selectedRows(src) {
		if row < len(src.Rows) && g.edits.deleted[row] {
			continue
		}
		for _, col := range cols {
			if g.readOnlyCols[col] == "" {
				fn(row, col)
			}
		}
	}
}

// setSelectedNull sets every editable chosen cell to NULL, as one undo step.
func (g *Grid) setSelectedNull(src *Source) {
	g.checkpoint()
	g.editableSelection(src, func(row, col int) { g.setValue(src, row, col, nil) })
}

// setSelectedDefault sets every chosen cell that allows it to its default,
// as one undo step.
func (g *Grid) setSelectedDefault(src *Source) {
	g.checkpoint()
	g.editableSelection(src, func(row, col int) {
		if g.defaultAllowed(src, row) {
			g.setCellDefault(src, row, col)
		}
	})
}

// revertSelectedCells forgets the changes of every chosen cell, as one
// undo step.
func (g *Grid) revertSelectedCells(src *Source) {
	g.checkpoint()
	g.editableSelection(src, func(row, col int) { g.forgetCell(src, row, col) })
}

// revertSelectedRows forgets the changes of every chosen row, as one undo
// step.
func (g *Grid) revertSelectedRows(src *Source) {
	g.checkpoint()
	rows := g.selectedRows(src)
	// From the last: an added row taken out moves those after it.
	slices.SortFunc(rows, func(x, y int) int { return y - x })
	for _, row := range rows {
		g.forgetRow(src, row)
	}
}

// editMenu is the Edit submenu of a cell of an editable grid. Its value
// actions act on the whole selection, which always holds the clicked cell.
func (g *Grid) editMenu(m *ui.Menu, src *Source, row, col int) {
	m.Submenu("Edit", func(m *ui.Menu) {
		if col != wholeRow && m.Item("Edit Cell").Shortcut(0, ui.KeyF2).Chosen() {
			g.startEdit(src, row, col)
		}
		m.Separator()
		if keymap.Item(m.Item("Add Row"), keymap.AddRow).Chosen() {
			g.addRow(src)
		}
		if keymap.Item(m.Item("Duplicate Row"), keymap.DuplicateRow).Chosen() {
			g.duplicateRow(src, row, g.keyCols)
		}
		if m.Item("Delete Row").Shortcut(0, ui.KeyDelete).Chosen() {
			g.deleteSelected(src)
		}
		m.Separator()
		rows := g.selectedRows(src)
		cells := len(rows) * len(g.selectedCols(src))
		cellLabel := func(single, format string) string {
			if cells <= 1 {
				return single
			}
			return fmt.Sprintf(format, cells)
		}
		if m.Item(cellLabel("Set to NULL", "Set %d Cells to NULL")).Chosen() {
			g.setSelectedNull(src)
		}
		defaultOK := slices.ContainsFunc(rows, func(r int) bool { return g.defaultAllowed(src, r) })
		// ⌘⌫ deletes rows here, as on a Mac keyboard without Delete: DEFAULT
		// has no key of its own.
		if m.Item(cellLabel("Set to DEFAULT", "Set %d Cells to DEFAULT")).Disabled(!defaultOK).Chosen() {
			g.setSelectedDefault(src)
		}
		m.Separator()
		if m.Item(cellLabel("Revert Cell", "Revert %d Cells")).Chosen() {
			g.revertSelectedCells(src)
		}
		rowLabel := "Revert Row"
		if len(rows) > 1 {
			rowLabel = fmt.Sprintf("Revert %d Rows", len(rows))
		}
		if m.Item(rowLabel).Chosen() {
			g.revertSelectedRows(src)
		}
	})
}

// headerMenu is the context menu of a column's header.
func (g *Grid) headerMenu(m *ui.Menu, a Host, src *Source, col int) {
	name := src.Cols[col].Name
	if keymap.Item(m.Item("Select Column"), keymap.SelectColumn).Chosen() {
		g.List.SelectColumns(colID(col), colID(col))
	}
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
	if g.valuesMenu != nil {
		g.valuesMenu(m, col)
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
		g.setCursorCol(0)
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

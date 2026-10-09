package dataview

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// clone copies pending edits, for undo.
func (p *pendingEdits) clone() *pendingEdits {
	c := newPendingEdits()
	for r, cols := range p.updates {
		c.updates[r] = maps.Clone(cols)
	}
	maps.Copy(c.deleted, p.deleted)
	for _, row := range p.inserted {
		c.inserted = append(c.inserted, slices.Clone(row))
	}
	return c
}

// checkpoint keeps the pending edits as they are, for undo, before a
// change; a change drops what could be redone.
func (g *Grid) checkpoint() {
	if g.edits == nil {
		return
	}
	g.undoStack = append(g.undoStack, g.edits.clone())
	if len(g.undoStack) > 200 {
		g.undoStack = g.undoStack[1:]
	}
	g.redoStack = nil
}

func (g *Grid) undo() {
	if g.edits == nil || len(g.undoStack) == 0 {
		return
	}
	g.redoStack = append(g.redoStack, g.edits.clone())
	*g.edits = *g.undoStack[len(g.undoStack)-1]
	g.undoStack = g.undoStack[:len(g.undoStack)-1]
	g.editing, g.orderKey = nil, ""
}

func (g *Grid) redo() {
	if g.edits == nil || len(g.redoStack) == 0 {
		return
	}
	g.undoStack = append(g.undoStack, g.edits.clone())
	*g.edits = *g.redoStack[len(g.redoStack)-1]
	g.redoStack = g.redoStack[:len(g.redoStack)-1]
	g.editing, g.orderKey = nil, ""
}

// copyFromNeighbour sets the chosen cell to the value of the row above
// (-1) or below (1), in the order shown.
func (g *Grid) copyFromNeighbour(src *Source, step int) {
	order := g.ViewOrder(src)
	from := g.SelRow + step
	if g.SelRow < 0 || g.SelRow >= len(order) || from < 0 || from >= len(order) {
		return
	}
	v, _ := g.value(src, order[from], g.selCol)
	g.checkpoint()
	g.setValue(src, order[g.SelRow], g.selCol, v)
}

// editKeys are DBeaver's keys of an editable grid.
func (g *Grid) editKeys(c *ui.Context, src *Source, order []int) {
	key := func(mods ui.Modifiers, k ui.Key) bool { return g.List.Shortcut(c, mods, k) }
	row := -1
	if g.SelRow >= 0 && g.SelRow < len(order) {
		row = order[g.SelRow]
	}
	switch {
	case key(ui.Cmd, ui.KeyZ):
		g.undo()
	case key(ui.Cmd|ui.Shift, ui.KeyZ):
		g.redo()
	case key(ui.Alt, ui.KeyInsert):
		g.addRow(src)
	case row >= 0 && key(ui.Cmd|ui.Alt, ui.KeyInsert):
		g.duplicateRow(src, row, g.keyCols)
	case key(ui.Alt, ui.KeyDelete):
		g.deleteSelected(src)
	case key(ui.Cmd, ui.KeyD):
		g.copyFromNeighbour(src, -1)
	case key(ui.Cmd|ui.Alt, ui.KeyD):
		g.copyFromNeighbour(src, 1)
	case row >= 0 && g.editing == nil && g.changed(src, row, g.selCol) && key(0, ui.KeyEscape):
		// Only a changed cell takes Escape, which otherwise closes what
		// is open over the grid.
		g.revertCell(src, row, g.selCol)
	}
}

// changed reports whether a cell holds a value not applied yet.
func (g *Grid) changed(src *Source, row, col int) bool {
	_, changed := g.value(src, row, col)
	return changed
}

// moveKeys are DBeaver's keys of every grid: the value editor, going to a
// row or a column, the first and last rows, the column's order.
func (g *Grid) moveKeys(c *ui.Context, a Host, src *Source, order []int) {
	key := func(mods ui.Modifiers, k ui.Key) bool { return g.List.Shortcut(c, mods, k) }
	switch {
	case key(ui.Shift, ui.KeyEnter) && g.SelRow >= 0 && g.SelRow < len(order):
		openValueEditor(a, g, src, order[g.SelRow], g.selCol)
	case key(ui.Cmd, ui.KeyG):
		openGoTo(a, g, src, false)
	case key(ui.Cmd|ui.Shift, ui.KeyG):
		openGoTo(a, g, src, true)
	case key(ui.Cmd, ui.KeyUp) && len(order) > 0:
		g.SelRow = 0
		g.List.ScrollIntoView(0)
	case key(ui.Cmd, ui.KeyDown) && len(order) > 0:
		g.SelRow = len(order) - 1
		g.List.ScrollToEnd()
	case key(ui.Cmd, ui.KeyF11) && g.selCol < len(src.Cols):
		apply, _ := g.filterFuncs()
		openDistinct(a, g, src, g.selCol, apply)
	case key(ui.Cmd, ui.Key2):
		// Ascending, descending, none: the chosen column's order in turn.
		id := colID(g.selCol)
		switch {
		case g.sort.Column != id:
			g.sort = ui.SortOrder{Column: id}
		case !g.sort.Descending:
			g.sort.Descending = true
		default:
			g.sort = ui.SortOrder{}
		}
	}
}

// goToForm asks for a row number, or a column name.
type goToForm struct {
	open   bool
	g      *Grid
	src    *Source
	column bool
	text   string
}

func openGoTo(a Host, g *Grid, src *Source, column bool) {
	a.Dialogs().goTo = &goToForm{open: true, g: g, src: src, column: column}
}

func goToView(a Host, c *ui.Context) {
	f := a.Dialogs().goTo
	title, hint := "Go to Row", "Row number"
	if f.column {
		title, hint = "Go to Column", "Column name, or its start"
	}
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(360).Gap(10).Children(func() {
			ui.Text(c, title).FontSize(15).Bold()
			submit := ui.TextInput(c, &f.text).Placeholder(hint).AutoFocus().Label(hint).Submitted()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if ui.PrimaryButton(c, "Go").Clicked() || submit {
					f.open = false
					f.g.goTo(f.src, f.text, f.column)
				}
			})
		})
	})
	if !f.open && a.Dialogs().goTo == f {
		a.Dialogs().goTo = nil
	}
}

// goTo chooses the row of a number, or the first column whose name starts
// with text.
func (g *Grid) goTo(src *Source, text string, column bool) {
	text = strings.TrimSpace(text)
	if column {
		for i, c := range src.Cols {
			if !g.hidden[i] && strings.HasPrefix(strings.ToLower(c.Name), strings.ToLower(text)) {
				g.selCol = i
				return
			}
		}
		return
	}
	n, err := strconv.Atoi(text)
	order := g.ViewOrder(src)
	if err != nil || len(order) == 0 {
		return
	}
	n = min(max(n, 1), len(order))
	g.SelRow = n - 1
	g.List.ScrollIntoView(n - 1)
}

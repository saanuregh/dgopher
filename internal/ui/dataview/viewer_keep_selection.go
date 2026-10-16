package dataview

import (
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/lists"
)

// keptSelection is the grid's choice before the rows are read again, by
// key and by place, with its columns by name.
type keptSelection struct {
	rows      []int
	keys      []string
	cursorRow int
	cursorKey string
	byKey     bool
	cursor    string
	anchor    string
	allCols   bool
}

// rowKey names a row by its key columns' values. The type stays in it:
// the same statement reads a key back with the same Go type, and keeping
// it never matches 1 with "1".
func rowKey(row []any, cols []string, keyCols map[int]bool) string {
	var parts []string
	for i := range keyCols {
		if i < len(row) && i < len(cols) {
			parts = append(parts, fmt.Sprintf("%s\x1f%T\x1f%v", cols[i], row[i], row[i]))
		}
	}
	slices.Sort(parts)
	return strings.Join(parts, "\x1e")
}

func (v *Viewer) columnNames() []string {
	return lists.Map(v.src.Cols, func(c db.ColumnInfo) string { return c.Name })
}

func (v *Viewer) keepSelection() keptSelection {
	g := v.grid
	names := v.columnNames()
	k := keptSelection{cursorRow: -1, byKey: len(g.keyCols) > 0, allCols: g.List.Cells.All}
	colName := func(id string) string {
		if i, ok := colIndex(id, len(names)); ok {
			return names[i]
		}
		return ""
	}
	k.cursor, k.anchor = colName(g.List.Cells.Cursor), colName(g.List.Cells.Anchor)
	for i := range g.selection.All() {
		if i < len(v.src.Rows) {
			k.rows = append(k.rows, i)
		}
	}
	slices.Sort(k.rows)
	if order := g.ViewOrder(&v.src); g.SelRow >= 0 && g.SelRow < len(order) && order[g.SelRow] < len(v.src.Rows) {
		k.cursorRow = order[g.SelRow]
	}
	if k.byKey {
		for _, i := range k.rows {
			k.keys = append(k.keys, rowKey(v.src.Rows[i], names, g.keyCols))
		}
		if k.cursorRow >= 0 {
			k.cursorKey = rowKey(v.src.Rows[k.cursorRow], names, g.keyCols)
		}
	}
	return k
}

// restoreSelection chooses again what k kept among the rows just read;
// what is gone stays unchosen.
func (v *Viewer) restoreSelection(k keptSelection) {
	g := v.grid
	names := v.columnNames()
	cursorRow := -1
	if k.byKey && len(g.keyCols) > 0 {
		at := map[string]int{}
		for i, row := range v.src.Rows {
			at[rowKey(row, names, g.keyCols)] = i
		}
		for _, key := range k.keys {
			if i, ok := at[key]; ok {
				g.selection.Add(i)
			}
		}
		if i, ok := at[k.cursorKey]; ok && k.cursorRow >= 0 {
			cursorRow = i
		}
	} else {
		for _, i := range k.rows {
			if i < len(v.src.Rows) {
				g.selection.Add(i)
			}
		}
		if k.cursorRow < len(v.src.Rows) {
			cursorRow = k.cursorRow
		}
	}
	if cursorRow >= 0 {
		g.SelRow = slices.Index(g.ViewOrder(&v.src), cursorRow)
	}
	cursor, anchor := slices.Index(names, k.cursor), slices.Index(names, k.anchor)
	if cursor >= 0 {
		g.setCursorCol(cursor)
		if anchor >= 0 {
			g.List.Cells.Anchor = colID(anchor)
		}
	}
	g.List.Cells.All = k.allCols
}

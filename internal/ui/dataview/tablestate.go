package dataview

import (
	"slices"
	"strconv"

	"github.com/egoist/mygo/ui"
)

// SortKey is a column a table's rows sort by, by name.
type SortKey struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc,omitempty"`
}

// TableState is how a table tab shows its rows: the filter applied, the
// sort and the chosen cell, its row in the order shown and its column by
// name; Column is "" for none.
type TableState struct {
	Filter string
	Sort   []SortKey
	Row    int
	Column string
}

// State is how the tab shows its rows now; until its first rows are read,
// the sort and cell it opened with.
func (t *TableTab) State() TableState {
	v := t.view
	if r := v.restoring; r != nil {
		st := *r
		st.Filter = v.where
		return st
	}
	st := TableState{Filter: v.where}
	g := v.grid
	for _, k := range sortKeys(g.sort, len(v.src.Cols)) {
		i, _ := strconv.Atoi(k.Column)
		st.Sort = append(st.Sort, SortKey{Column: v.src.Cols[i].Name, Desc: k.Descending})
	}
	if g.SelRow >= 0 && g.selCol >= 0 && g.selCol < len(v.src.Cols) {
		st.Row, st.Column = g.SelRow, v.src.Cols[g.selCol].Name
	}
	return st
}

// restore opens the rows as st shows them: the filter as typed, in the
// WHERE box, and the sort and cell once the first rows are read.
func (v *Viewer) restore(st TableState) {
	v.where, v.typedWhere, v.whereIn = st.Filter, st.Filter, st.Filter
	if len(st.Sort) > 0 || st.Row > 0 || st.Column != "" {
		st.Sort = slices.Clone(st.Sort)
		v.restoring = &st
	}
}

// restoredSort is the sort restored, waiting for the first rows, by the
// names of the table's columns; a column the table no longer has is left
// out.
func (v *Viewer) restoredSort() []SortKey {
	if v.restoring == nil {
		return nil
	}
	var keys []SortKey
	for _, k := range v.restoring.Sort {
		if name := v.tableColumn(k.Column); name != "" {
			keys = append(keys, SortKey{Column: name, Desc: k.Desc})
		}
	}
	return keys
}

// applyRestored shows the first rows read as the restored state says: the
// sort they were read in, and the cell chosen, in view.
func (v *Viewer) applyRestored() {
	r := v.restoring
	if r == nil {
		return
	}
	sorted := v.restoredSort()
	v.restoring = nil
	g := v.grid
	names := v.columnNames()
	g.sort = nil
	for _, k := range sorted {
		if i := slices.Index(names, k.Column); i >= 0 {
			g.sort = append(g.sort, ui.SortOrder{Column: colID(i), Descending: k.Desc})
		}
	}
	v.sortRead = g.sort
	order := g.ViewOrder(&v.src)
	if len(order) == 0 || r.Row <= 0 && r.Column == "" {
		return
	}
	row := max(0, min(r.Row, len(order)-1))
	g.List.Choose(row)
	if col := slices.Index(names, r.Column); col >= 0 {
		g.setCursorCol(col)
	}
	g.List.ScrollIntoView(row)
}

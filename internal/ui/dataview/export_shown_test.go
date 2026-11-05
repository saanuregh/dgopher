package dataview

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Exporting the rows read follows the grid's sort and quick filter.
func TestExportRowsReadAsShown(t *testing.T) {
	_, _, tb, _ := openNumbers(t, 5, 100)
	v := tb.view
	v.grid.sort = []ui.SortOrder{{Column: colID(0), Descending: true}}
	v.grid.Filter = "row"
	v.grid.ViewOrder(&v.src)
	src := v.exportSource()
	ids := func() []any {
		var out []any
		for _, r := range src.RowsRead() {
			out = append(out, r[0])
		}
		return out
	}
	if got := ids(); !slices.Equal(got, []any{int64(5), int64(4), int64(3), int64(2), int64(1)}) {
		t.Fatalf("sorted: %v", got)
	}
	v.grid.Filter = "row 2"
	if got := ids(); !slices.Equal(got, []any{int64(2)}) {
		t.Fatalf("filtered: %v", got)
	}
}

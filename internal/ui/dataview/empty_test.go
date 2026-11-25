package dataview

import (
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// A table without rows says so under its header, where a blank grid would
// read as rows not read yet; there is no filter to clear.
func TestNoRowsSaysSo(t *testing.T) {
	_, tt, _, _ := openNumbers(t, 0, 100)
	tt.Frame()
	said, ok := tt.Find("No rows")
	header, _ := tt.Find("label")
	if !ok || said.Y < header.Y+header.H {
		t.Fatalf("No rows at %v (found %v), not under the header at %v", said, ok, header)
	}
	if tt.HasText("Clear Filter") {
		t.Error("Clear Filter shows with no filter on")
	}
	testutil.Snapshot(t, tt, "grid-no-rows")
}

// Rows that a filter hides say that it is the filter, with the button
// that clears it: the quick filter first, then the one the server ran.
func TestNoRowsMatchingOffersToClearTheFilter(t *testing.T) {
	_, tt, tb, _ := openNumbers(t, 5, 100)
	v := tb.view
	v.grid.Filter = "no such row"
	tt.Frame()
	tt.Frame()
	if !tt.HasText("No rows match the filter") {
		t.Fatalf("a quick filter matching nothing shows %q", tt.Texts())
	}
	testutil.Snapshot(t, tt, "grid-no-rows-match")
	if err := tt.Click("Clear Filter"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if v.grid.Filter != "" || !tt.HasText("row 2") {
		t.Fatalf("Clear Filter left the quick filter %q", v.grid.Filter)
	}
	// On the server.
	v.addFilter(rowCond{col: 1, op: "=", vals: []any{db.Typed("nothing")}})
	testutil.WaitFor(t, tt, "the filtered rows", func() bool { return !v.loading && len(v.src.Rows) == 0 })
	tt.Frame()
	if !tt.HasText("No rows match the filter") {
		t.Fatalf("a filter matching nothing shows %q", tt.Texts())
	}
	if err := tt.Click("Clear Filter"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "every row again", func() bool { return !v.loading && len(v.src.Rows) == 5 })
	if v.where != "" {
		t.Errorf("the filter is still %q", v.where)
	}
}

// A column the filter is on carries a mark in its header, which says how
// to clear it; the others carry none.
func TestFilteredColumnIsMarked(t *testing.T) {
	_, tt, tb, _ := openNumbers(t, 5, 100)
	v := tb.view
	tt.Frame()
	if _, ok := tt.Access("label is filtered"); ok {
		t.Fatal("a column is marked with no filter on")
	}
	v.addFilter(rowCond{col: 1, op: "=", vals: []any{db.Typed("row 2")}})
	testutil.WaitFor(t, tt, "the filtered rows", func() bool { return !v.loading && len(v.src.Rows) == 1 })
	tt.Frame()
	if _, ok := tt.Access("label is filtered"); !ok {
		t.Error("the filtered column carries no mark")
	}
	if _, ok := tt.Access("id is filtered"); ok {
		t.Error("a column without a filter is marked")
	}
	testutil.Settled(t, tt, "row 2")
	testutil.Snapshot(t, tt, "grid-filtered-column")
	// A filter typed marks the columns it names.
	v.clearFilters(-1)
	testutil.WaitFor(t, tt, "every row again", func() bool { return !v.loading && len(v.src.Rows) == 5 })
	v.setFilter(func() { v.typedWhere = "ID > 3"; v.joinFilters() })
	testutil.WaitFor(t, tt, "the typed filter", func() bool { return !v.loading && len(v.src.Rows) == 2 })
	tt.Frame()
	if _, ok := tt.Access("id is filtered"); !ok {
		t.Error("the column a typed filter names carries no mark")
	}
	// On the rows read, as a result's.
	v.clearFilters(-1)
	testutil.WaitFor(t, tt, "every row again", func() bool { return !v.loading && len(v.src.Rows) == 5 })
	v.grid.addCond(rowCond{col: 1, op: "=", vals: []any{db.Typed("row 2")}})
	tt.Frame()
	tt.Frame()
	if _, ok := tt.Access("label is filtered"); !ok {
		t.Error("a column filtered on the rows read carries no mark")
	}
}

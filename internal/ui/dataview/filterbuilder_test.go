package dataview

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// The filter builder writes the conditions chosen as the rows' filter.
func TestFilterBuilder(t *testing.T) {
	a, tt, cn := designHost(t)
	cn.DB.SQL.Exec(`INSERT INTO items (label, price) VALUES ('100%_pure', '9'), (NULL, '2')`)
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "items", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	v := tb.view
	testutil.WaitFor(t, tt, "the rows", func() bool { return len(v.src.Rows) == 4 })
	if err := tt.Click("Build a filter from conditions"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	b := v.builder
	b.conds[0] = builderCond{column: "price", op: "is between", value: "2", to: "9"}
	if err := tt.Click("Condition"); err != nil {
		t.Fatal(err)
	}
	b.conds[1] = builderCond{column: "label", op: "contains", value: "%_"}
	tt.Frame()
	testutil.Snapshot(t, tt, "filter-builder")
	tt.SetDark(true)
	tt.Frame()
	testutil.Snapshot(t, tt, "filter-builder-dark")
	tt.SetDark(false)
	tt.Frame()
	if err := tt.Click("Apply"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the filtered rows", func() bool { return v.where != "" && len(v.src.Rows) == 1 })
	if !strings.Contains(v.whereIn, `"price" BETWEEN 2 AND 9 AND CAST("label" AS TEXT) LIKE '%\%\_%' ESCAPE '\'`) {
		t.Fatalf("filter %q", v.whereIn)
	}
	b.any = 1
	b.conds = []builderCond{{column: "label", op: "is NULL"}, {column: "label", op: "is one of", value: "pen, ink"}}
	where, err := b.where(v)
	if err != nil || where != `("label" IS NULL) OR ("label" IN ('pen', 'ink'))` {
		t.Fatalf("%q %v", where, err)
	}
	b.conds = []builderCond{{column: "label", op: "="}}
	if _, err := b.where(v); err == nil {
		t.Fatal("a condition without its value")
	}
}

// Opened, the builder shows the filter of the rows: the conditions it
// applied, a filter typed as its SQL, and the Filter menu's conditions,
// sent again as they were while unchanged.
func TestFilterBuilderShowsTheFilter(t *testing.T) {
	a, tt, cn := designHost(t)
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "items", Kind: db.KindTable, Rows: -1}, PageData)
	v := a.Tabs[0].(*TableTab).view
	testutil.WaitFor(t, tt, "the rows", func() bool { return len(v.src.Rows) == 2 })
	toggle := func() {
		t.Helper()
		if err := tt.Click("Build a filter from conditions"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}

	// The builder's own conditions come back as they were.
	toggle()
	v.builder.conds[0] = builderCond{column: "label", op: "is one of", value: "pen, ink"}
	if err := tt.Click("Apply"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the filter applied", func() bool { return v.where != "" && !v.loading })
	toggle()
	toggle()
	if got := v.builder.conds; len(got) != 1 || got[0].column != "label" || got[0].op != "is one of" || got[0].value != "pen, ink" {
		t.Fatalf("builder's conditions not shown: %+v", got)
	}

	// A filter typed shows as its SQL, applied as typed.
	toggle()
	v.whereIn = "price > 2"
	v.setFilter(func() { v.where, v.typedWhere, v.menuFilters = v.whereIn, v.whereIn, nil })
	testutil.WaitFor(t, tt, "the typed filter", func() bool { return v.where == "price > 2" && !v.loading })
	toggle()
	if got := v.builder.conds; len(got) != 1 || got[0].column != "" || got[0].sql != "price > 2" {
		t.Fatalf("typed filter not shown: %+v", got)
	}
	if where, err := v.builder.where(v); err != nil || where != "price > 2" {
		t.Fatalf("typed filter written %q %v", where, err)
	}

	// A condition of the Filter menu, added while the builder shows, shows
	// in it; unchanged, its SQL is sent as the menu wrote it.
	v.addFilter(rowCond{col: 1, op: "=", vals: []any{"pen"}})
	testutil.WaitFor(t, tt, "the menu's filter", func() bool { return len(v.menuFilters) == 1 && !v.loading })
	tt.Frame()
	got := v.builder.conds
	if len(got) != 2 || got[1].column != "label" || got[1].op != "=" || got[1].value != "pen" || !got[1].applied() {
		t.Fatalf("menu's filter not shown: %+v", got)
	}
	want := "(price > 2) AND (" + v.menuFilters[0].sql + ")"
	if where, err := v.builder.where(v); err != nil || where != want {
		t.Fatalf("menu's filter written %q %v, want %q", where, err, want)
	}
	// Changed, it is written from what it shows.
	v.builder.conds[1].value = "ink"
	if where, _ := v.builder.where(v); !strings.Contains(where, `"label" = 'ink'`) {
		t.Fatalf("changed condition written %q", where)
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "filter-builder-prefilled")
}

// The clear-filter button is there once a filter is set, and goes once it
// is cleared.
func TestFilterClearAppears(t *testing.T) {
	a, tt, cn := designHost(t)
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "items", Kind: db.KindTable, Rows: -1}, PageData)
	v := a.Tabs[0].(*TableTab).view
	testutil.WaitFor(t, tt, "the rows", func() bool { return len(v.src.Rows) == 2 })
	const clear = "Clear the filter"
	if _, ok := tt.Find(clear); ok {
		t.Fatal("Clear the filter shown with no filter")
	}
	v.whereIn = "price > 2"
	v.setFilter(func() { v.where, v.typedWhere, v.menuFilters = v.whereIn, v.whereIn, nil })
	testutil.WaitFor(t, tt, "the filtered rows", func() bool { return !v.loading && len(v.src.Rows) == 1 })
	testutil.WaitFor(t, tt, "the button", func() bool { _, ok := tt.Find(clear); return ok })
	v.clearFilters(-1)
	testutil.WaitFor(t, tt, "the button gone", func() bool { _, ok := tt.Find(clear); return !ok })
}

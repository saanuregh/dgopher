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

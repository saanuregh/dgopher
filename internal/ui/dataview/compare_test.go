package dataview

import (
	"slices"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

func TestCompareRows(t *testing.T) {
	a := &rowSnapshot{cols: []string{"id", "name", "gone"}, rows: [][]any{{int64(1), "pen", "x"}, {int64(2), "ink", "y"}, {int64(3), nil, "z"}}}
	b := &rowSnapshot{cols: []string{"id", "name"}, rows: [][]any{{int64(3), nil}, {int64(1), "Pen"}, {int64(4), "cap"}}}
	cols, diffs := compareRows(a, b, []string{"id"})
	if len(cols) != 3 {
		t.Fatal(cols)
	}
	kinds := []int{}
	for _, d := range diffs {
		kinds = append(kinds, d.kind)
	}
	// 1 changed (name, and gone missing from B), 2 only in A, 3 changed
	// (gone only), 4 only in B.
	if !slices.Equal(kinds, []int{rowChanged, rowOnlyA, rowChanged, rowOnlyB}) {
		t.Fatalf("kinds %v", kinds)
	}
	if !diffs[0].diff[1] || diffs[2].diff[1] || !diffs[2].diff[2] {
		t.Fatalf("cells %+v", diffs)
	}
	_, byPosition := compareRows(a, &rowSnapshot{cols: a.cols, rows: a.rows[:2]}, nil)
	if len(byPosition) != 3 || byPosition[0].kind != rowSame || byPosition[2].kind != rowOnlyA {
		t.Fatalf("by position %+v", byPosition)
	}
}

// Rows pinned compare with the rows read again, matched by the table's
// key, and their differences shown.
func TestCompareResults(t *testing.T) {
	a, tt, cn := designHost(t)
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "items", Kind: db.KindTable, Rows: -1}, PageData)
	v := a.Tabs[0].(*TableTab).view
	testutil.WaitFor(t, tt, "the rows", func() bool { return len(v.src.Rows) == 2 && v.columns != nil })
	a.Dialogs().pinned = v.snapshot()
	cn.DB.SQL.Exec(`UPDATE items SET price = '9' WHERE label = 'ink'`)
	cn.DB.SQL.Exec(`INSERT INTO items (label, price) VALUES ('cap', '1')`)
	v.RequestReload()
	testutil.WaitFor(t, tt, "the rows again", func() bool { return len(v.src.Rows) == 3 })
	ct := newCompareTab(a.Dialogs().pinned, v.snapshot())
	if ct.matchBy != "key" {
		t.Fatalf("matched by %q", ct.matchBy)
	}
	a.AddTab(ct)
	tt.Frame()
	testutil.Snapshot(t, tt, "compare-results")
	if !tt.HasText("1 the same · 1 changed · 0 only in A · 1 only in B") || len(ct.lines) != 3 {
		t.Fatalf("texts %q, lines %d", tt.Texts(), len(ct.lines))
	}
	ct.shown = 1
	ct.lay()
	if len(ct.lines) != 4 {
		t.Fatalf("all rows: %d lines", len(ct.lines))
	}
}

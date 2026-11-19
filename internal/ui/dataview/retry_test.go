package dataview

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/widgets"
)

// reportAction finds the action labelled label of a report, failing without one.
func reportAction(t *testing.T, r widgets.ErrorReport, label string) func() {
	t.Helper()
	for _, a := range r.Actions {
		if a.Label == label {
			return a.Run
		}
	}
	t.Fatalf("report %q has no %s action: %+v", r.Title, label, r.Actions)
	return nil
}

// A failed count offers Retry, which counts again.
func TestCountFailureOffersRetry(t *testing.T) {
	a, tt, tb, _ := openNumbers(t, 3, 0)
	v := tb.view
	sess := tb.view.source.Session()
	if _, err := sess.Exec(t.Context(), "ALTER TABLE n RENAME TO gone"); err != nil {
		t.Fatal(err)
	}
	v.countRows()
	testutil.WaitFor(t, tt, "the failed count", func() bool { return !v.counting && len(a.Reports) > 0 })
	r := a.Reports[len(a.Reports)-1]
	if r.Title != "Could not count the rows" {
		t.Fatalf("reports %+v", a.Reports)
	}
	retry := reportAction(t, r, "Retry")
	if _, err := sess.Exec(t.Context(), "ALTER TABLE gone RENAME TO n"); err != nil {
		t.Fatal(err)
	}
	retry()
	testutil.WaitFor(t, tt, "the count", func() bool { return !v.counting && v.count == 3 })
}

// A failed apply offers Retry, which applies the same pending changes.
func TestApplyFailureOffersRetry(t *testing.T) {
	a, tt, tb, file := openNumbers(t, 3, 100)
	v := tb.view
	applyAt(t, func(stage string, _ int) {
		if stage == "finish" {
			v.applyCancel()
		}
	})
	editLabels(t, v, "edited")
	testutil.WaitFor(t, tt, "the apply", func() bool { return !v.applying })
	if len(a.Reports) != 1 || !strings.Contains(a.Reports[0].Title, "not applied") {
		t.Fatalf("reports %+v", a.Reports)
	}
	if v.grid.edits.count() != 1 {
		t.Fatalf("pending edits %d after the failure", v.grid.edits.count())
	}
	retry := reportAction(t, a.Reports[0], "Retry")
	applyStep = nil
	retry()
	if a.Confirm != nil && a.Confirm.OnConfirm != nil {
		a.Confirm.OnConfirm()
	}
	testutil.WaitFor(t, tt, "the retried apply", func() bool { return !v.applying && !v.loading && v.grid.edits.count() == 0 })
	if got := labelOf(t, file, 1); got != "edited" {
		t.Fatalf("label %q, reports %+v", got, a.Reports)
	}
}

// A first read failing under a filter shows a banner offering to edit or
// clear the filter; Clear Filter reads the rows.
func TestFailedFilterOffersEdit(t *testing.T) {
	a, tt, _, _ := openNumbers(t, 3, 0)
	cn := a.Conns[0]
	tb := NewTableTab(a, cn, "", db.Object{Schema: "main", Name: "n", Kind: db.KindTable, Rows: -1}, PageData)
	v := tb.view
	v.where, v.typedWhere, v.whereIn = "missing = 1", "missing = 1", "missing = 1"
	a.AddTab(tb)
	testutil.WaitFor(t, tt, "the failed read", func() bool { return v.err != "" && !v.loading })
	tt.Frame()
	if v.src.Cols != nil {
		t.Fatalf("columns read despite the failure: %v", v.src.Cols)
	}
	if !tt.HasText("Could not read the rows: ") {
		t.Fatalf("no banner: %q", tt.Texts())
	}
	if err := tt.Click("Edit Filter"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	testutil.WaitFor(t, tt, "the WHERE box focused", func() bool { return *a.FocusWant() == "" })
	if err := tt.Click("Clear Filter"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	testutil.WaitFor(t, tt, "the rows", func() bool { return len(v.src.Rows) == 3 && !v.loading })
	if v.where != "" || v.typedWhere != "" || v.menuFilters != nil {
		t.Fatalf("filter left: %q %q %v", v.where, v.typedWhere, v.menuFilters)
	}
}

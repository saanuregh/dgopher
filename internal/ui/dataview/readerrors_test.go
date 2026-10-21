package dataview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// failingIndexes is a dialect whose index read fails.
type failingIndexes struct{ db.Dialect }

func (failingIndexes) Indexes(ctx context.Context, q db.Querier, schema, table string) ([]db.Index, error) {
	return nil, errors.New("no permission to read the indexes")
}

// A Structure view whose index read failed says so, rather than showing
// a table without indexes.
func TestStructureShowsIndexReadError(t *testing.T) {
	a, tt, cn := designHost(t)
	cn.DB.Dialect = failingIndexes{cn.DB.Dialect}
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "items", Kind: db.KindTable, Rows: -1}, PageStructure)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "the structure", func() bool { return tb.columns != nil })
	tt.Frame()
	if !tt.HasText("Couldn't read indexes") {
		t.Fatalf("no note of the failed index read: %q", tt.Texts())
	}
	if tt.HasText("Couldn't read foreign keys") {
		t.Errorf("the foreign keys were read, yet a note says otherwise")
	}
}

// The banner of a failed rows read opens the full error on Details.
func TestRowsErrorBannerDetails(t *testing.T) {
	a, tt, cn := designHost(t)
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "items", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "the rows", func() bool { return tb.view.src.Rows != nil && !tb.view.loading })
	if _, err := cn.DB.SQL.Exec(`DROP TABLE items`); err != nil {
		t.Fatal(err)
	}
	tb.view.reload()
	testutil.WaitFor(t, tt, "the failed read", func() bool { return tb.view.err != "" && !tb.view.loading })
	if err := tt.Click("Details"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	if len(a.Reports) != 1 || a.Reports[0].Title != "Could not read the rows" || !strings.Contains(a.Reports[0].Detail, "items") {
		t.Fatalf("reports %+v", a.Reports)
	}
}

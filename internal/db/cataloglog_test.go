package db

import (
	"context"
	"strings"
	"testing"
)

// Catalog reads are kept, the newest first, at most catalogLogSize.
func TestCatalogLog(t *testing.T) {
	ctx := context.Background()
	d := newSQLiteDB(t)
	mustExec(t, d, `CREATE TABLE t (a INT)`)
	if _, err := d.Dialect.Objects(ctx, d.Catalog(), "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Dialect.Columns(ctx, d.Catalog(), "main", "t"); err != nil {
		t.Fatal(err)
	}
	got := d.CatalogQueries()
	if len(got) != 2 || !strings.Contains(got[0].SQL, "pragma_table_info") || !strings.Contains(got[1].SQL, "sqlite_master") {
		t.Fatalf("%+v", got)
	}
	for range catalogLogSize + 10 {
		rows, err := d.Catalog().QueryContext(ctx, "SELECT 1")
		if err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	if n := len(d.CatalogQueries()); n != catalogLogSize {
		t.Fatalf("kept %d", n)
	}
}

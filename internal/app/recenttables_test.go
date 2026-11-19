package app

import (
	"fmt"
	"slices"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/lists"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

func tableTitles(items []paletteItem) []string {
	var titles []string
	for _, it := range items {
		if it.group == "Table" {
			titles = append(titles, it.title)
		}
	}
	return titles
}

// ⌘P lists the tables opened last first, the latest at the top, then the
// rest by name whatever their case; typing keeps that order among equal
// matches.
func TestPaletteRanksRecentTablesFirst(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	other := addConn(a, db.Config{ID: "other", Name: "other", Engine: db.SQLite, Database: ":memory:"})
	obj := func(name string) db.Object { return db.Object{Schema: "main", Name: name, Kind: db.KindTable} }
	other.Objects[connection.SchemaKey{Database: "", Schema: "main"}] = []db.Object{obj("delta")}
	tt := ui.NewTester(a.view, 1000, 700)

	a.OpenTable(cn, "", obj("zeta"), dataview.PageData)
	a.OpenTable(cn, "", obj("beta"), dataview.PageData)
	testutil.WaitFor(t, tt, "both tabs", func() bool { return a.tabs().Len() == 2 })
	// What the catalog read is replaced by the names the test needs.
	cn.Objects = map[connection.SchemaKey][]db.Object{{Schema: "main"}: {obj("alpha"), obj("beta"), obj("Gamma"), obj("zeta")}}

	a.openPalette(true)
	if got, want := tableTitles(a.palette.items), []string{"beta", "zeta", "alpha", "delta", "Gamma"}; !slices.Equal(got, want) {
		t.Fatalf("the tables %q, want %q", got, want)
	}
	a.palette.query = "eta"
	tt.Frame()
	if got := tableTitles(a.palette.shown); len(got) < 2 || got[0] != "beta" || got[1] != "zeta" {
		t.Fatalf("typed, the tables %q, want beta and zeta first", got)
	}
	a.palette = nil
}

// A connection's new ID keeps the recency of its tables.
func TestRecentTablesFollowRename(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	obj := func(name string) db.Object { return db.Object{Schema: "main", Name: name, Kind: db.KindTable} }
	cn.Objects[connection.SchemaKey{Schema: "main"}] = []db.Object{obj("alpha"), obj("zeta")}
	a.recordRecentTable(cn, "", obj("zeta"))

	if err := a.renameConn(cn, "lite2"); err != nil {
		t.Fatal(err)
	}
	keys := lists.Map(loadRecentTables(cn.Project), func(r recentTable) string { return r.Key })
	if !slices.Equal(keys, []string{"lite2//main.zeta"}) {
		t.Fatalf("the recent keys %q", keys)
	}
	if got := tableTitles(a.paletteItems(true)); !slices.Equal(got, []string{"zeta", "alpha"}) {
		t.Fatalf("the tables %q, want zeta first", got)
	}
}

// The list keeps the latest 50 tables, and a table opened again moves to
// the front rather than appearing twice.
func TestRecentTablesCap(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	for i := range 51 {
		a.recordRecentTable(cn, "", db.Object{Schema: "main", Name: fmt.Sprint("t", i)})
	}
	a.recordRecentTable(cn, "", db.Object{Schema: "main", Name: "t10"})
	kept := loadRecentTables(cn.Project)
	if len(kept) != 50 || kept[0].Key != "lite//main.t10" || kept[1].Key != "lite//main.t50" || kept[49].Key != "lite//main.t1" {
		t.Fatalf("kept %d, first %+v, last %+v", len(kept), kept[0], kept[len(kept)-1])
	}
}

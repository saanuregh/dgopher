package mongo

import (
	"slices"
	"testing"

	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// A statement runs on the console's database, which use changes: the
// question of dropDatabase names it. No server is needed: the statement
// is only asked about.
func TestConsoleDropDatabaseNamesTheDatabase(t *testing.T) {
	h := testHost{dataview.NewFakeHost(t)}
	cfg := offlineConfig(t)
	con := NewConsole(h, h.AddConn(cfg), "shop")
	con.Database = "other" // as use other leaves it
	con.running = true
	con.runGen++
	con.step(con.runGen, statementsOf("db.dropDatabase()", 0), 0)
	want := "dropDatabase removes the database other and all of its collections"
	if h.Confirm == nil || !slices.Contains(h.Confirm.Reasons, want) {
		t.Fatalf("asked %+v, want the reason %q", h.Confirm, want)
	}
}

// The ▶ before each statement follows the text, which is split again only
// when it changes, not on every frame.
func TestConsoleRunLinesFollowTheText(t *testing.T) {
	h := testHost{dataview.NewFakeHost(t)}
	con := NewConsole(h, h.AddConn(offlineConfig(t)), "shop")
	h.AddTab(con)
	tt := ui.NewTester(h.View, 1200, 760)
	con.Editor.SetText("db.a.find()\n\ndb.b.find()")
	tt.Frame()
	if !slices.Equal(con.Editor.RunLines, []int{0, 2}) {
		t.Fatalf("run lines %v", con.Editor.RunLines)
	}
	first := &con.stmts[0]
	tt.Frame()
	if &con.stmts[0] != first {
		t.Error("the text was split again without a change")
	}
	con.Editor.SetText("db.a.find()\ndb.b.find()\ndb.c.find()")
	tt.Frame()
	if !slices.Equal(con.Editor.RunLines, []int{0, 1, 2}) {
		t.Errorf("run lines after a change %v", con.Editor.RunLines)
	}
}

// A MongoDB tab opens on the database asked for, else the connection's,
// else test, as mongosh does; the console the same.
func TestDatabaseFor(t *testing.T) {
	h := testHost{dataview.NewFakeHost(t)}
	cn := h.AddConn(offlineConfig(t))
	if got := DatabaseFor(cn, ""); got != "test" {
		t.Errorf("no database: %s", got)
	}
	if got := NewConsole(h, cn, "").Database; got != "test" {
		t.Errorf("the console: %s", got)
	}
	cn.Config.Database = "shop"
	if got, other := DatabaseFor(cn, ""), DatabaseFor(cn, "other"); got != "shop" || other != "other" {
		t.Errorf("the connection's: %s, asked: %s", got, other)
	}
}

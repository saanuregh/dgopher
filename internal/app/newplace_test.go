package app

import (
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// New SQL Editor asks where, the place at hand first and chosen: ↵ alone
// opens another editor on the database the tab in front works in, not on
// its connection's own.
func TestNewEditorAsksWhereWithThePlaceAtHandFirst(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "db.invalid", Port: 5432, Database: "postgres"})
	addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 760)
	a.NewQueryTab(cn, "reports", "")
	// The editor tries its server, which is not there: done with first.
	testutil.WaitFor(t, tt, "the connection to fail", func() bool { return cn.Status == connection.StatusFailed })
	if err := tt.Click("OK"); err != nil { // the dialog that says so
		t.Fatal(err)
	}
	tt.Frame()
	before := a.tabs().Len()
	a.menuAction(keymap.NewEditor)()
	tt.Frame()
	tt.Frame()
	if a.palette == nil || !a.palette.places || a.tabs().Len() != before {
		t.Fatalf("with two connections, New SQL Editor opened %d tab(s) and no picker", a.tabs().Len()-before)
	}
	if first := a.palette.shown[0]; first.conn != cn || first.detail != "reports · not connected" || first.group != "where you are" {
		t.Errorf("the picker's first row is %q (%s, %s), not the place at hand", first.title, first.detail, first.group)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	q, ok := a.ActiveTab().(*query.Tab)
	if !ok || a.tabs().Len() != before+1 || a.palette != nil {
		t.Fatalf("↵ in the picker opened %d tab(s), in front %T, the picker still open: %v", a.tabs().Len()-before, a.ActiveTab(), a.palette != nil)
	}
	if got := q.Places()[0]; got.Conn != cn || got.Database != "reports" {
		t.Errorf("↵ opened the editor on %q of %s, want reports of pg", got.Database, got.Conn.Config.Name)
	}
}

// Typing in the picker finds another place by its name, and one
// connection alone is not asked about.
func TestNewEditorPicksAnotherPlaceByName(t *testing.T) {
	a, tt, shop := filterApp(t)
	archive := a.connByID(a.projects[0].Prefix + "archive")
	a.NewQueryTab(shop, "", "")
	tt.Frame()
	a.menuAction(keymap.NewEditor)()
	tt.Frame()
	tt.Type("arch")
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if q, ok := a.ActiveTab().(*query.Tab); !ok || q.Places()[0].Conn != archive {
		t.Errorf("\"arch\" and ↵ in the picker left %T in front, not an editor on archive", a.ActiveTab())
	}

	one := newTestApp(t)
	only := addConn(one, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	ot := ui.NewTester(one.view, 1200, 760)
	one.menuAction(keymap.NewEditor)()
	ot.Frame()
	if q, ok := one.ActiveTab().(*query.Tab); one.palette != nil || !ok || q.Places()[0].Conn != only {
		t.Errorf("with one connection, New SQL Editor left the picker open (%v) and %T in front", one.palette != nil, one.ActiveTab())
	}
}

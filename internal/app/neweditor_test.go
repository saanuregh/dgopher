package app

import (
	"path/filepath"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// A new editor opens on a connection that cannot connect, which it then
// offers to retry, as a restored editor does; it does not wait for the
// connection.
func TestNewEditorWhileConnectionFails(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: filepath.Join(t.TempDir(), "no", "x.sqlite")})
	tt := ui.NewTester(a.view, 1200, 760)
	tt.Frame()
	a.NewQueryTab(cn, "", "")
	testutil.WaitFor(t, tt, "the failure", func() bool { return cn.Status == connection.StatusFailed })
	if _, ok := a.ActiveTab().(*query.Tab); !ok {
		t.Fatalf("no editor for a connection that failed: front is %T", a.ActiveTab())
	}
}

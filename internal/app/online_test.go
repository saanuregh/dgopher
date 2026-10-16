package app

import (
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// While the system says the computer is offline, an editor on a network
// engine's connection says so, and stops saying it once online.
func TestEditorShowsOffline(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1})
	a.AddTab(query.New(a, cn, "", "query", "select 1"))
	tt := ui.NewTester(a.view, 1200, 800)
	a.setOnline(false)
	tt.Frame()
	if _, ok := tt.Find("offline"); !ok || !tt.HasText("Connect") {
		t.Fatalf("offline: %q", tt.Texts())
	}
	a.setOnline(true)
	tt.Frame()
	if _, ok := tt.Find("offline"); ok {
		t.Fatalf("online: %q", tt.Texts())
	}
}

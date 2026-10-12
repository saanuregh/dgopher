package query

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/params"

	"github.com/egoist/mygo/ui"
)

// The parameters asked close at Escape as they opened, and ask once a
// value was typed.
func TestParamsKeepEdits(t *testing.T) {
	a := newFakeQueryHost(t)
	file := filepath.Join(t.TempDir(), "p.sqlite")
	os.WriteFile(file, nil, 0o600)
	setup, _ := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	setup.SQL.Exec("CREATE TABLE n (i INTEGER)")
	setup.Close()
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1100, 760)
	q := newEditor(t, a, tt, cn, "-- connection: lite\n\nSELECT * FROM n WHERE i <= :last")
	ask := func() {
		q.withParams([]string{q.Editor.Text}, func(map[string]params.Input) {})
		tt.Frame()
		if a.dialogs.params == nil {
			t.Fatal("no parameters asked")
		}
	}
	ask()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.dialogs.params != nil {
		t.Fatalf("untouched parameters ask: %q", tt.Texts())
	}
	ask()
	a.dialogs.params.fields[0].value = "10"
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.dialogs.params == nil || !tt.HasText("Discard your changes?") {
		t.Fatalf("typed parameters closed: %q", tt.Texts())
	}
}

package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// The palette offers the tab's own commands first, each with its key,
// and runs them as the key would.
func TestPaletteTabCommands(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := query.New(a, cn, "", "q", "SELECT 1 AS one;")
	a.AddTab(q)
	tt.Frame()

	a.openPalette(false)
	tt.Frame()
	if first := a.palette.shown[0]; first.title != "Run Statement" || first.key != keymap.Run {
		t.Fatalf("the first command is %+v", first)
	}
	if !tt.HasText(keymap.First(keymap.Run)) {
		t.Fatalf("the palette shows no key of Run: %q", tt.Texts())
	}
	titles := func() []string {
		var out []string
		for _, it := range a.palette.items {
			out = append(out, it.title)
		}
		return out
	}
	if slices.Contains(titles(), "Commit") || slices.Contains(titles(), "Refresh Rows") {
		t.Fatalf("commands of a transaction or rows not there: %q", titles())
	}
	tt.Key(0, ui.KeyEnter)
	testutil.WaitFor(t, tt, "the result", func() bool { return resultRows(tt, q, 1) })

	// The result's commands follow.
	a.openPalette(false)
	tt.Frame()
	if !slices.Contains(titles(), "Refresh Rows") || !slices.Contains(titles(), "Show Chart") {
		t.Fatalf("no commands of the result: %q", titles())
	}
	a.palette.query = "show chart"
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	a.openPalette(false)
	tt.Frame()
	if !slices.Contains(titles(), "Hide Chart") {
		t.Fatalf("Show Chart did not show it: %q", titles())
	}
	a.palette = nil
}

// ⌘P offers the projects' query files, those added since the palette
// opened too, and opens one in its editor.
func TestPaletteQueryFiles(t *testing.T) {
	a := newTestApp(t)
	addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	p := a.projects[0]
	tt := ui.NewTester(a.view, 1000, 700)
	if err := os.MkdirAll(p.Queries, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Queries, "daily.sql"), []byte("-- connection: lite\nSELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.Scanned = time.Time{} // as long after the last scan as a rescan needs

	a.openPalette(true)
	a.palette.query = "daily"
	testutil.WaitFor(t, tt, "the file offered", func() bool {
		return len(a.palette.shown) > 0 && a.palette.shown[0].title == "daily.sql"
	})
	tt.Key(0, ui.KeyEnter)
	testutil.WaitFor(t, tt, "its editor", func() bool {
		q, ok := a.ActiveTab().(*query.Tab)
		return ok && q.Path == filepath.Join(p.Queries, "daily.sql")
	})
}

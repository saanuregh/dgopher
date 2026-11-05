package dataview

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dgopher/internal/db"

	"github.com/egoist/mygo/ui"
)

// Open in Spreadsheet App writes the rows as the grid shows them: in its
// order, through its quick filter, without hidden columns or rows marked
// for deletion, with pending changes, and formulas guarded.
func TestSpreadsheetFileIsTheGridShown(t *testing.T) {
	spreadsheetDir = t.TempDir()
	g := NewGrid()
	g.edits = newPendingEdits()
	src := &Source{
		Cols: []db.ColumnInfo{{Name: "id", Type: "INTEGER"}, {Name: "name", Type: "TEXT"}, {Name: "secret", Type: "TEXT"}},
		Rows: [][]any{{int64(1), "=1+1", "s"}, {int64(2), "bob", "s"}, {int64(3), "carl", "s"}, {int64(4), "zed", "s"}},
	}
	g.hidden = map[int]bool{2: true}
	g.sort = []ui.SortOrder{{Column: colID(0), Descending: true}}
	g.edits.deleted[1] = true
	g.setValue(src, 2, 1, db.Typed("carla"))
	g.Filter = "carl" // the quick filter matches the rows read
	path, n, err := g.spreadsheetFile(src)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if want := "id,name\n3,carla\n"; string(data) != want || n != 1 {
		t.Fatalf("%d rows: %q, want %q", n, data, want)
	}
	g.Filter = ""
	path, _, err = g.spreadsheetFile(src)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if want := "id,name\n4,zed\n3,carla\n1,'=1+1\n"; string(data) != want {
		t.Fatalf("%q, want %q", data, want)
	}
}

// A spreadsheet file left from before is removed once it is a day old,
// the next time rows are opened in the spreadsheet app.
func TestOldSpreadsheetFilesRemoved(t *testing.T) {
	spreadsheetDir = t.TempDir()
	old := filepath.Join(spreadsheetDir, spreadsheetPrefix+"old.csv")
	recent := filepath.Join(spreadsheetDir, spreadsheetPrefix+"recent.csv")
	other := filepath.Join(spreadsheetDir, "other.csv")
	for _, p := range []string{old, recent, other} {
		os.WriteFile(p, nil, 0o600)
	}
	day := time.Now().Add(-25 * time.Hour)
	os.Chtimes(old, day, day)
	os.Chtimes(other, day, day)
	g, src := editableGrid()
	if _, _, err := g.spreadsheetFile(src); err != nil {
		t.Fatal(err)
	}
	for p, kept := range map[string]bool{old: false, recent: true, other: true} {
		if _, err := os.Stat(p); (err == nil) != kept {
			t.Errorf("%s kept %v", filepath.Base(p), err == nil)
		}
	}
}

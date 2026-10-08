package app

import (
	"testing"

	"dgopher/internal/testutil"
	"dgopher/internal/ui/editor"

	"github.com/egoist/mygo/ui"
)

func TestEditorSnapshot(t *testing.T) {
	e := &editor.Editor{Text: "-- top customers\nSELECT c.id, c.name, count(*) AS orders\nFROM customers c\nJOIN orders o ON o.customer_id = c.id\nWHERE c.email LIKE '%@example.com' AND o.total > 100.5\nGROUP BY 1, 2\nORDER BY orders DESC\nLIMIT 50;"}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { e.View(c, 13) })
	}, 700, 300)
	testutil.Snapshot(t, tt, "editor")
	if !tt.HasText("SQL editor") && len(tt.Texts()) == 0 {
		t.Fatal("nothing built")
	}
}

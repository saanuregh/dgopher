package query

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// A result's tab says what ran: its table, else the statement's first
// words, numbered when repeated, with a pin when pinned; the statement
// shown below is marked in the gutter, where it is now.
func TestResultTabsSayWhatRan(t *testing.T) {
	script := "CREATE TABLE orders (id int);\nINSERT INTO orders VALUES (1);\n\nSELECT * FROM orders;\n\nSELECT 42 AS answer;\n\nSELECT id FROM orders"
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1200, 800, script)
	q.Run(RunScript)
	testutil.WaitFor(t, tt, "the results", func() bool { return !q.Running && len(q.results) == 5 })
	tt.Frame()
	for _, want := range []string{"orders", "SELECT 42 AS answer", "orders 2"} {
		if _, ok := tt.Find(want); !ok {
			t.Errorf("no tab %q: %q", want, tt.Texts())
		}
	}
	if tt.HasText("Result 1") {
		t.Error("a tab is still named by its number")
	}

	// The last result shows: its statement starts on line 7.
	if !q.Editor.HasShown || q.Editor.ShownLine != 7 {
		t.Fatalf("the shown statement: %v on line %d, want line 7", q.Editor.HasShown, q.Editor.ShownLine)
	}
	q.Editor.SetText("-- moved\n" + script)
	tt.Frame()
	if !q.Editor.HasShown || q.Editor.ShownLine != 8 {
		t.Errorf("after a line above: %v on line %d, want line 8", q.Editor.HasShown, q.Editor.ShownLine)
	}
	q.Editor.SetText(strings.Replace(script, "SELECT id FROM orders", "SELECT id, id FROM orders", 1))
	tt.Frame()
	if q.Editor.HasShown {
		t.Errorf("its statement changed, yet line %d is marked", q.Editor.ShownLine)
	}

	r := q.results[len(q.results)-1]
	r.pinned = true
	tt.Frame()
	if _, ok := tt.Find("orders 2, pinned"); !ok || tt.HasText("orders 2 (pinned)") {
		t.Errorf("pinned: %q", tt.Texts())
	}
	if tip := resultTip(r); tip != "SELECT id FROM orders" {
		t.Errorf("the tip %q", tip)
	}
}

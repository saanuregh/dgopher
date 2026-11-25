package query

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// A statement that returns no rows shows what it did as the Output's
// headline, in the size of a title, over the log of what ran before.
func TestOutputLeadsWithTheLastOutcome(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700,
		"create table t (id integer);\ninsert into t values (1), (2), (3);")
	q.Run(RunScript)
	testutil.WaitFor(t, tt, "the script", func() bool { return !q.Running && len(q.messages) == 2 })
	tt.Frame()
	head, ok := tt.Find("3 rows affected")
	if !ok {
		t.Fatal("no headline says 3 rows affected")
	}
	// The statement before stays in the log, under the headline and in
	// smaller text.
	var earlier bool
	for _, n := range tt.AccessAll() {
		if strings.HasPrefix(n.Name, "CREATE") && n.Bounds.Y > head.Y {
			earlier = true
			if n.Bounds.H >= head.H {
				t.Errorf("the headline is %v high, no taller than the log's line at %v", head.H, n.Bounds.H)
			}
		}
	}
	if !earlier {
		t.Error("the CREATE before it is not in the log under the headline")
	}
}

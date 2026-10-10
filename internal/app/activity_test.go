package app

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
)

// Cancel and End session pass the safety policy, as a connection that
// only reads, being exempt, may still stop another user's work.
func TestActivityGoesThroughPolicy(t *testing.T) {
	ask := func(cfg db.Config, terminate bool) (title string, reasons []string, typeName bool, preview string) {
		t.Helper()
		a := newTestApp(t)
		cn := addConn(a, cfg)
		at := &activityTab{a: a, conn: cn, grid: dataview.NewGrid(), pages: activityPages(cfg.Engine)}
		row := []any{int64(42), "ada"}
		at.src = dataview.Source{Cols: []db.ColumnInfo{{Name: "pid"}, {Name: "user"}}, Rows: [][]any{row}}
		at.act(row, terminate)
		if a.confirm == nil {
			t.Fatalf("%s: no confirmation asked (alert %v)", cfg.Name, a.alert)
		}
		return a.confirm.Title, a.confirm.Reasons, a.confirm.TypeName, a.confirm.Preview
	}
	_, reasons, _, preview := ask(db.Config{ID: "ro", Name: "ro", Engine: db.Postgres, ReadOnly: true}, false)
	if preview != "SELECT pg_cancel_backend(42)" || !strings.Contains(strings.Join(reasons, "\n"), "read-only") ||
		!strings.Contains(reasons[0], "acts on another user's work") {
		t.Fatalf("read-only cancel: %q %q", preview, reasons)
	}
	if _, _, typeName, _ := ask(db.Config{ID: "prod", Name: "prod", Engine: db.Postgres, Env: db.Production}, true); !typeName {
		t.Fatal("End session on production does not ask for the name")
	}
	if _, _, typeName, _ := ask(db.Config{ID: "my", Name: "my", Engine: db.MySQL, Env: db.Development}, false); typeName {
		t.Fatal("Cancel on development asks for the name")
	}
	_, reasons, typeName, preview := ask(db.Config{ID: "rd", Name: "rd", Engine: db.Redis, Env: db.Production, ReadOnly: true}, true)
	if preview != "CLIENT KILL ID 42" || !typeName || len(reasons) < 3 {
		t.Fatalf("Redis: %q %q %v", preview, reasons, typeName)
	}
}

package query

import (
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/sqltext"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

func TestOutline(t *testing.T) {
	text := "-- connection: lite\n\n-- monthly totals\nSELECT month,\n  sum(total)\nFROM orders;\n\nUPDATE x SET y = 1;"
	got := outline(text, sqltext.SQLite, sqltext.SplitOptions{})
	if len(got) != 2 || got[0].line != 4 || got[0].text != "SELECT month, sum(total) FROM orders" || got[0].comment != "monthly totals" ||
		got[1].line != 8 || got[1].comment != "" {
		t.Fatalf("%+v", got)
	}
}

// ⌘⇧O lists the statements; one chosen puts the caret at its start.
func TestGoToStatement(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "SELECT 1;\n\nSELECT 2;\n\n-- the third\nSELECT 3;")
	tt.Key(ui.Cmd|ui.Shift, ui.KeyO)
	tt.Frame()
	d := a.QueryDialogs().outline
	if d == nil || len(d.entries) != 3 || d.sel != 2 {
		t.Fatalf("outline %+v", d)
	}
	testutil.Snapshot(t, tt, "outline")
	tt.Type("2")
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	if a.QueryDialogs().outline != nil || q.Editor.SelEnd != len("SELECT 1;\n\n") {
		t.Fatalf("caret %d", q.Editor.SelEnd)
	}
}

package query

import (
	"slices"
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

func TestExpandSnippet(t *testing.T) {
	text, fields := expandSnippet(`SELECT ${2:*} FROM ${1:table} WHERE ${1} > \$1 AND x = $3;$0`)
	if text != `SELECT * FROM table WHERE table > $1 AND x = ;` {
		t.Fatalf("text %q", text)
	}
	third := strings.Index(text, " = ") + 3
	want := []snippetField{{14, 19}, {7, 8}, {third, third}, {len(text), len(text)}} // 1, 2, 3, then 0
	if !slices.Equal(fields, want) {
		t.Fatalf("fields %+v, want %+v", fields, want)
	}
	if text, fields := expandSnippet("no fields, $ alone"); text != "no fields, $ alone" || len(fields) != 0 {
		t.Fatalf("%q %+v", text, fields)
	}
}

// A snippet's keyword completes to it, and Tab walks its fields as they
// are filled in; a function completes with its parentheses.
func TestSnippetAndFunctionCompletion(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	cn.Project.Snippets = []project.Snippet{{Name: "Recent", Keyword: "recent", SQL: "SELECT * FROM ${1:t} ORDER BY ${2:id} DESC LIMIT ${3:10};"}}
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "")
	tt.Type("recen")
	testutil.WaitFor(t, tt, "the snippet", func() bool {
		return q.ac.open && len(q.ac.items) > 0 && q.ac.items[0].kind == "snippet"
	})
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if q.Editor.Text != "SELECT * FROM t ORDER BY id DESC LIMIT 10;" || q.snippet == nil {
		t.Fatalf("text %q, session %v", q.Editor.Text, q.snippet)
	}
	tt.Frame()
	if q.Editor.Selection() != "t" {
		t.Fatalf("selection %q", q.Editor.Selection())
	}
	tt.Type("orders")
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	tt.Frame()
	if q.Editor.Text != "SELECT * FROM orders ORDER BY id DESC LIMIT 10;" || q.Editor.Selection() != "id" {
		t.Fatalf("text %q, selection %q", q.Editor.Text, q.Editor.Selection())
	}
	tt.Type("placed_at")
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	tt.Frame()
	if q.Editor.Selection() != "10" {
		t.Fatalf("third field: %q in %q", q.Editor.Selection(), q.Editor.Text)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if q.snippet != nil {
		t.Fatal("Escape kept the fields")
	}

	q.Editor.Text, q.ac.lastText = "", ""
	q.Editor.PendingSel = &[2]int{0, 0}
	tt.Frame()
	tt.Type("SELECT coales")
	testutil.WaitFor(t, tt, "the function", func() bool {
		return q.ac.open && len(q.ac.items) > 0 && q.ac.items[0].label == "coalesce"
	})
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	if q.Editor.Text != "SELECT coalesce()" || q.Editor.SelEnd != len("SELECT coalesce(") {
		t.Fatalf("text %q caret %d", q.Editor.Text, q.Editor.SelEnd)
	}
}

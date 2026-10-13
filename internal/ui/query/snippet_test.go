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
	// A snippet kept from a file with \r\n line endings has its fields
	// where the editor's \n text has them.
	text, fields = expandSnippet("SELECT ${2:*}\r\nFROM ${1:table}")
	if text != "SELECT *\nFROM table" || !slices.Equal(fields, []snippetField{{14, 19}, {7, 8}}) {
		t.Fatalf("\\r\\n: %q %+v", text, fields)
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
		return offers(q, "recent")
	})
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if q.Editor.Text() != "SELECT * FROM t ORDER BY id DESC LIMIT 10;" || q.snippet == nil {
		t.Fatalf("text %q, session %v", q.Editor.Text(), q.snippet)
	}
	tt.Frame()
	if q.Editor.SelectedText() != "t" {
		t.Fatalf("selection %q", q.Editor.SelectedText())
	}
	tt.Type("orders")
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	tt.Frame()
	if q.Editor.Text() != "SELECT * FROM orders ORDER BY id DESC LIMIT 10;" || q.Editor.SelectedText() != "id" {
		t.Fatalf("text %q, selection %q", q.Editor.Text(), q.Editor.SelectedText())
	}
	tt.Type("placed_at")
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	tt.Frame()
	if q.Editor.SelectedText() != "10" {
		t.Fatalf("third field: %q in %q", q.Editor.SelectedText(), q.Editor.Text())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if q.snippet != nil {
		t.Fatal("Escape kept the fields")
	}

	q.Editor.SetText("")
	q.Editor.Select(0, 0)
	tt.Frame()
	tt.Type("SELECT coales")
	testutil.WaitFor(t, tt, "the function", func() bool {
		return offers(q, "coalesce")
	})
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	if q.Editor.Text() != "SELECT coalesce()" || q.Editor.Caret() != len("SELECT coalesce(") {
		t.Fatalf("text %q caret %d", q.Editor.Text(), q.Editor.Caret())
	}
}

// A snippet's fields, past characters of several bytes, are selected
// whole as Tab goes through them.
func TestSnippetFieldsWithMultibyte(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "-- 日本\n")
	q.InsertSnippet("SELECT '${1:é}' AS ${2:名前}, '🎉' FROM ${3:t};$0")
	tt.Frame()
	tt.Frame()
	if q.Editor.SelectedText() != "é" {
		t.Fatalf("first field: %q in %q", q.Editor.SelectedText(), q.Editor.Text())
	}
	tt.Type("ü")
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if q.Editor.SelectedText() != "名前" {
		t.Fatalf("second field: %q in %q", q.Editor.SelectedText(), q.Editor.Text())
	}
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if q.Editor.SelectedText() != "t" {
		t.Fatalf("third field: %q in %q", q.Editor.SelectedText(), q.Editor.Text())
	}
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if text := q.Editor.Text(); text != "-- 日本\nSELECT 'ü' AS 名前, '🎉' FROM t;" || q.Editor.Caret() != len(text) || q.snippet != nil {
		t.Fatalf("text %q, caret %d, session %v", text, q.Editor.Caret(), q.snippet != nil)
	}
}

package query

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/sqltext"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

func TestOccurrencesAndRename(t *testing.T) {
	d := sqltext.Postgres
	text := `SELECT o.total, "Total" FROM orders o WHERE total > 1 ORDER BY o.total -- total`
	id, ok := identifierAt(text, strings.Index(text, "total")+2, d)
	if !ok || id.name != "total" || id.qualifier != "o" || id.keyword {
		t.Fatalf("%+v", id)
	}
	// Quoted or not, any case; not in the comment.
	if n := len(occurrences(text, id, d)); n != 4 {
		t.Fatalf("%d occurrences", n)
	}
	got := renameInText(text, id, "Amount", d, db.DialectOf(db.Postgres).Quote)
	if got != `SELECT o."Amount", "Amount" FROM orders o WHERE "Amount" > 1 ORDER BY o."Amount" -- total` {
		t.Fatalf("%s", got)
	}
	// A keyword is a name only when the one renamed is spelled as one.
	order, _ := identifierAt(text, strings.Index(text, "orders")+1, d)
	if n := len(occurrences("SELECT * FROM orders ORDER BY 1", order, d)); n != 1 {
		t.Fatalf("orders: %d", n)
	}
}

// F12 opens what the name at the caret names: a table, through its alias
// or one of its columns too; Shift+F12 lists its usages in the project's
// files; F2 renames it in the file.
func TestNavigateNames(t *testing.T) {
	a := newFakeQueryHost(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1000, 700)
	other := filepath.Join(cn.Project.Queries, "report.sql")
	os.MkdirAll(cn.Project.Queries, 0o755)
	os.WriteFile(other, []byte("-- connection: lite\n\nSELECT customer\nFROM invoices;\n"), 0o644)
	cn.Project.Files = []string{"report.sql"}
	q := newEditor(t, a, tt, cn, "SELECT i.customer FROM invoices i WHERE customer <> ''")
	cn.DB.SQL.Exec(`CREATE TABLE invoices (id INTEGER PRIMARY KEY, customer TEXT)`)

	q.GoTo(strings.Index(q.Editor.Text, "i.customer"))
	tt.Frame()
	q.goToDefinition() // the alias i, before the schema is read
	testutil.WaitFor(t, tt, "the schema", func() bool { _, ok := cn.Objects[connection.SchemaKey{Schema: "main"}]; return ok })
	q.goToDefinition()
	tt.Frame()
	tab, ok := a.ActiveTab().(*dataview.TableTab)
	if !ok || tab.Object.Name != "invoices" || tab.Page != dataview.PageStructure {
		t.Fatalf("opened %+v", a.ActiveTab())
	}

	a.ActivateTab(func(tb widgets.Tab) bool { return tb == q })
	q.GoTo(strings.LastIndex(q.Editor.Text, "customer") + 2)
	tt.Frame()
	q.findUsages()
	d := a.QueryDialogs().usages
	if d == nil || len(d.usages) != 3 || d.usages[2].file != "report.sql" || d.usages[2].line != 3 {
		t.Fatalf("usages %+v", d)
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "usages")
	d.sel = 2
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(a.opened) != 1 || !strings.HasPrefix(a.opened[0], other+"@") {
		t.Fatalf("opened %q", a.opened)
	}

	q.askRename()
	f := a.QueryDialogs().rename
	f.name = "client"
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if q.Editor.Text != "SELECT i.client FROM invoices i WHERE client <> ''" {
		t.Fatalf("renamed %q", q.Editor.Text)
	}
}

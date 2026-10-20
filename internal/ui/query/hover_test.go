package query

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	code "github.com/egoist/mygo/ui/editor"
)

// hoverEditor opens an editor on text over a SQLite file holding
// customers, orders pointing at them and refunds pointing at orders,
// once the catalog is read.
func hoverEditor(t *testing.T, text string) (*fakeQueryHost, *ui.Tester, *connection.Conn, *Tab) {
	t.Helper()
	a := newFakeQueryHost(t)
	file := filepath.Join(t.TempDir(), "shop.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := a.AddConn(db.Config{ID: "shop", Name: "shop", Engine: db.SQLite, Database: file})
	setup, err := db.Open(context.Background(), cn.Config, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers(id), status TEXT, total REAL)`,
		`CREATE TABLE refunds (id INTEGER PRIMARY KEY, order_id INTEGER REFERENCES orders(id))`,
	} {
		if _, err := setup.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	setup.Close()
	tt := ui.NewTester(a.view, 1100, 760)
	q := newEditor(t, a, tt, cn, text)
	connection.ObjectsOf(a, cn, "", "main")
	testutil.WaitFor(t, tt, "the schema", func() bool { _, ok := cn.Objects[connection.SchemaKey{Schema: "main"}]; return ok })
	for _, name := range []string{"customers", "orders"} {
		connection.LoadColumns(a, cn, "", "main", name, nil)
	}
	testutil.WaitFor(t, tt, "the columns", func() bool {
		return len(cn.Columns[connection.ObjectKey{Schema: "main", Name: "orders"}]) > 0 &&
			len(cn.Columns[connection.ObjectKey{Schema: "main", Name: "customers"}]) > 0
	})
	return a, tt, cn, q
}

const hoverText = `WITH recent AS (SELECT id, total AS amount FROM orders WHERE total > :min)
SELECT o.status, c.name, round(o.total, 2), customer_id, * FROM recent r JOIN orders o ON o.id = r.id JOIN customers c ON c.id = o.customer_id`

// at is the offset of the n-th (from 1) s in text.
func at(text, s string, n int) int {
	i := -1
	for range n {
		i += 1 + strings.Index(text[i+1:], s)
	}
	return i
}

// The name under the pointer resolves to what it names, in the statement
// and the catalog.
func TestHoverTargets(t *testing.T) {
	_, _, _, q := hoverEditor(t, hoverText)
	cases := []struct {
		at   int
		want string
	}{
		{at(hoverText, "orders o", 1), "table orders"},
		{at(hoverText, "o.status", 1), "table orders as o"},
		{at(hoverText, "status", 1) + 1, "column orders.status"},
		{at(hoverText, "name", 1) + 1, "column customers.name"},
		{at(hoverText, "customer_id", 1) + 1, "column orders.customer_id"},
		{at(hoverText, "round", 1) + 1, "function round"},
		{at(hoverText, ":min", 1) + 1, "parameter :min"},
		{at(hoverText, "* FROM", 1), "star"},
		{at(hoverText, "recent r", 1) + 1, "derived recent: id, amount"},
		{at(hoverText, "r JOIN", 1), "derived recent: id, amount"}, // its alias
		{at(hoverText, "SELECT o", 1) + 1, "none"},
	}
	for _, tc := range cases {
		target, ok := q.hoverTarget(tc.at)
		got := "none"
		if ok {
			got = target.String()
		}
		if got != tc.want {
			t.Errorf("at %d (%q): %s, want %s", tc.at, hoverText[tc.at:min(tc.at+8, len(hoverText))], got, tc.want)
		}
	}
}

// showHoverAt shows the card of what is at off, as the resting pointer
// asks.
func showHoverAt(tt *ui.Tester, q *Tab, off int) {
	q.hover(code.HoverRequest{Offset: off})
	tt.Frame()
	tt.Frame()
}

// A table's card shows in the editor, and a click on a column inserts
// it, qualified by the alias.
func TestHoverInsertColumn(t *testing.T) {
	text := "SELECT o.id FROM orders o WHERE "
	_, tt, _, q := hoverEditor(t, text)
	q.GoTo(len(text))
	showHoverAt(tt, q, at(text, "orders", 1)+1)
	if !tt.HasText("Open Data") || !tt.HasText("status") {
		t.Fatalf("no card: %q", tt.Texts())
	}
	if err := tt.Click("status"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := q.Editor.Text(); got != text+"o.status" {
		t.Fatalf("text %q", got)
	}
}

// A related table's pill writes the join after the FROM clause.
func TestHoverJoin(t *testing.T) {
	text := "SELECT *\nFROM orders o\nWHERE o.total > 10"
	_, tt, _, q := hoverEditor(t, text)
	showHoverAt(tt, q, at(text, "orders", 1)+1)
	testutil.WaitFor(t, tt, "the relations", func() bool { return tt.HasText("Joins") })
	// The pill after Joins, not the column's own "→ customers".
	r, _ := tt.Find("Joins")
	tt.ClickAt(r.X+r.W+30, r.Y+r.H/2)
	tt.Frame()
	want := "SELECT *\nFROM orders o\nJOIN customers ON customers.id = o.customer_id\nWHERE o.total > 10"
	if got := q.Editor.Text(); got != want {
		t.Fatalf("text\n%s\nwant\n%s", got, want)
	}
}

// * expands to the columns of the statement's tables.
func TestHoverStarExpand(t *testing.T) {
	text := "SELECT * FROM customers"
	_, tt, _, q := hoverEditor(t, text)
	showHoverAt(tt, q, at(text, "*", 1))
	if err := tt.Click("Expand"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	if got := q.Editor.Text(); got != "SELECT id, name FROM customers" {
		t.Fatalf("text %q", got)
	}
}

// A function's card shows its signature from the dialect's table.
func TestHoverFunction(t *testing.T) {
	text := "SELECT round(2.5, 1)"
	_, tt, _, q := hoverEditor(t, text)
	showHoverAt(tt, q, 8)
	testutil.WaitFor(t, tt, "the signature", func() bool { return hasTextPart(tt, "round(") })
}

// A parameter's card says it is asked for when run, without a value.
func TestHoverParam(t *testing.T) {
	text := "SELECT * FROM orders WHERE id = :id"
	_, tt, _, q := hoverEditor(t, text)
	showHoverAt(tt, q, len(text)-1)
	if !hasTextPart(tt, "Asked when run") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

// A keyword has no card.
func TestHoverKeywordNone(t *testing.T) {
	text := "SELECT 1"
	_, tt, _, q := hoverEditor(t, text)
	showHoverAt(tt, q, 2)
	if tt.HasText("Copy Name") {
		t.Fatalf("a card for SELECT: %q", tt.Texts())
	}
}

// The keymap's key shows the card of the name at the caret.
func TestShowHoverKey(t *testing.T) {
	text := "SELECT * FROM orders"
	_, tt, _, q := hoverEditor(t, text)
	q.GoTo(len(text) - 2)
	tt.Frame()
	ch := keymap.Bindings(keymap.ShowHover)[0].Keys.First
	tt.Key(ch.Mods, ch.Key)
	tt.Frame()
	tt.Frame()
	if !tt.HasText("Open Data") {
		t.Fatalf("no card: %q", tt.Texts())
	}
}

func hasTextPart(tt *ui.Tester, s string) bool {
	for _, x := range tt.Texts() {
		if strings.Contains(x, s) {
			return true
		}
	}
	return false
}

// Only names the catalog or the statement knows are links; a click on
// one opens it.
func TestLinkAtKnownNamesOnly(t *testing.T) {
	text := "WITH r AS (SELECT 1 AS x) SELECT nothing FROM orders, r"
	a, tt, _, q := hoverEditor(t, text)
	if s, e, ok := q.linkAt(at(text, "orders", 1) + 2); !ok || text[s:e] != "orders" {
		t.Fatalf("orders: %d %d %v", s, e, ok)
	}
	for _, word := range []string{"SELECT", "nothing"} {
		if _, _, ok := q.linkAt(at(text, word, 1) + 1); ok {
			t.Errorf("%s is a link", word)
		}
	}
	q.Editor.State.OnLink(at(text, "orders", 1) + 2)
	tt.Frame()
	if tab, ok := a.ActiveTab().(*dataview.TableTab); !ok || tab.Object.Name != "orders" {
		t.Fatalf("opened %+v", a.ActiveTab())
	}
}

// * expands to the tables of its own SELECT, not those of a subquery,
// and an expression column without a name leaves nothing to expand.
func TestHoverStarScope(t *testing.T) {
	text := "SELECT * FROM orders WHERE customer_id IN (SELECT id FROM customers)"
	_, tt, _, q := hoverEditor(t, text)
	showHoverAt(tt, q, at(text, "*", 1))
	if err := tt.Click("Expand"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	if got := q.Editor.Text(); got != "SELECT id, customer_id, status, total FROM orders WHERE customer_id IN (SELECT id FROM customers)" {
		t.Fatalf("text %q", got)
	}
	cte := "WITH x AS (SELECT count(*) FROM orders) SELECT * FROM x"
	q.replaceText(0, len(q.Editor.Text()), cte)
	tt.Frame()
	showHoverAt(tt, q, at(cte, "*", 2))
	if tt.HasText("Expand") {
		t.Fatalf("Expand offered with an unnamed column: %q", tt.Texts())
	}
}

// A table joined to itself gets an alias of its own.
func TestHoverJoinSelf(t *testing.T) {
	text := "SELECT *\nFROM refunds"
	_, tt, cn, q := hoverEditor(t, text)
	_ = cn
	h, ok := q.hoverTarget(at(text, "refunds", 1) + 1)
	if !ok {
		t.Fatal("no target")
	}
	q.insertJoin(h, dataview.Relation{Table: "refunds", Columns: []string{"parent_id"}, RefColumns: []string{"id"}}, h.start)
	tt.Frame()
	want := "SELECT *\nFROM refunds\nJOIN refunds refunds_2 ON refunds_2.id = refunds.parent_id"
	if got := q.Editor.Text(); got != want {
		t.Fatalf("text\n%s\nwant\n%s", got, want)
	}
}

// With the editor's cards off, the resting pointer asks for none, as the
// editor is told (NoPointerHover, tested in MyGo); the key still shows one.
func TestHoverEditorOff(t *testing.T) {
	text := "SELECT * FROM orders"
	_, tt, _, q := hoverEditor(t, text)
	widgets.HoverCards = widgets.HoverSettings{Delay: widgets.DefaultHoverDelay, Hidden: map[string]bool{widgets.CardEditor: true}}
	t.Cleanup(func() { widgets.HoverCards = widgets.HoverSettings{Delay: widgets.DefaultHoverDelay} })
	tt.Frame()
	if !q.Editor.State.NoPointerHover {
		t.Fatal("the editor still shows cards on a rest")
	}
	q.GoTo(len(text) - 2)
	tt.Frame()
	ch := keymap.Bindings(keymap.ShowHover)[0].Keys.First
	tt.Key(ch.Mods, ch.Key)
	tt.Frame()
	tt.Frame()
	if !tt.HasText("Open Data") {
		t.Fatalf("the key shows no card: %q", tt.Texts())
	}
}

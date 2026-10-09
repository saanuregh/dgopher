package query

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/sqltext"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// fakeCatalog is a schema of two tables, and their columns.
type fakeCatalog struct{}

func (fakeCatalog) objects(schema string) ([]db.Object, bool) {
	if schema != "public" {
		return nil, false
	}
	return []db.Object{{Name: "orders"}, {Name: "customers"}, {Name: "Order Lines"}}, true
}

func (fakeCatalog) columns(schema, table string) ([]db.Column, bool) {
	if table != "orders" {
		return nil, false
	}
	return []db.Column{{Name: "id"}, {Name: "total"}, {Name: "customer_id"}}, true
}

func (fakeCatalog) schemas() []string        { return []string{"public"} }
func (fakeCatalog) defaultSchema() string    { return "public" }
func (fakeCatalog) quote(name string) string { return `"` + name + `"` }

func messages(text string, cat catalog) []string {
	var out []string
	for _, p := range checkText(text, sqltext.Postgres, sqltext.SplitOptions{}, cat) {
		out = append(out, string([]rune(text)[p.Start:p.End])+": "+p.Message+" fix="+p.Fix)
	}
	return out
}

func TestCheckText(t *testing.T) {
	for _, c := range []struct {
		text string
		want []string
	}{
		{"SELECT o.totl FROM ordrs o", []string{"ordrs: No table or view ordrs in public. Did you mean orders? fix=orders"}},
		{"SELECT o.totl, o.id FROM orders o JOIN customers c ON c.id = o.customer_id", []string{"totl: No column totl in orders. Did you mean total? fix=total"}},
		{"WITH recent AS (SELECT * FROM orders) SELECT * FROM recent, order_lines", []string{"order_lines: No table or view order_lines in public. Did you mean Order Lines? fix=\"Order Lines\""}},
		{"SELECT count(* FROM orders", []string{"(: This parenthesis is not closed. fix="}},
		{"SELECT 'open", []string{"': This string is not closed: ' ends it. fix="}},
		{"SELECT 'it''s' FROM orders /* note", []string{"/: This comment is not closed: */ ends it. fix="}},
		{"SELECT * FROM generate_series(1, 3) g, other.t, xyz", []string{"xyz: No table or view xyz in public. fix="}},
		{"CREATE TABLE brand_new (id int)", nil},
		{"SELECT $$ unfinished", []string{"$: This string is not closed: $$ ends it. fix="}},
		{"INSERT INTO ordrs (id) VALUES (1)", []string{"ordrs: No table or view ordrs in public. Did you mean orders? fix=orders"}},
		{"SELECT * INTO new_orders FROM orders", nil},
	} {
		if got := messages(c.text, fakeCatalog{}); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got %q\nwant %q", c.text, got, c.want)
		}
	}
	if got := messages("SELECT * FROM nowhere", nil); len(got) != 0 {
		t.Fatalf("without a catalog: %q", got)
	}
}

// The editor marks a table it does not know, says why at the caret, and
// Alt+Enter mends it.
func TestProblemsInEditor(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "")
	cn.DB.SQL.Exec(`CREATE TABLE invoices (id INTEGER)`)
	q.Editor.Text = "SELECT * FROM invoces"
	q.GoTo(len(q.Editor.Text))
	testutil.WaitFor(t, tt, "the mark", func() bool { return len(q.Editor.Problems) == 1 })
	if !tt.HasText("No table or view invoces in main. Did you mean invoices?") {
		t.Fatalf("texts %q", tt.Texts())
	}
	testutil.Snapshot(t, tt, "problem")
	tt.Key(ui.Alt, ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	if q.Editor.Text != "SELECT * FROM invoices" || len(q.Editor.Problems) != 0 {
		t.Fatalf("fixed %q, problems %+v", q.Editor.Text, q.Editor.Problems)
	}
}

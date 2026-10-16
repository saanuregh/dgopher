package dataview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/export"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// A key's path in a JSON column is written as each engine reads it.
func TestJSONPathPerEngine(t *testing.T) {
	path := []any{"items", 0, "sku"}
	for _, tc := range []struct {
		engine db.Engine
		leaf   bool
		want   string
	}{
		{db.Postgres, true, `payload->'items'->0->>'sku'`},
		{db.Postgres, false, `payload->'items'->0->'sku'`},
		{db.MySQL, true, `payload->>'$.items[0].sku'`},
		{db.SQLite, true, `payload->>'$.items[0].sku'`},
		{db.DuckDB, true, `payload->>'$.items[0].sku'`},
		{db.ClickHouse, true, `JSONExtractString(payload, 'items', 1, 'sku')`},
		{db.ClickHouse, false, `JSONExtractRaw(payload, 'items', 1, 'sku')`},
	} {
		if got := JSONPath(tc.engine, "payload", path, tc.leaf); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.engine, got, tc.want)
		}
	}
	if got := JSONPath(db.MySQL, "doc", []any{"a key", "it's"}, true); got != `doc->>'$."a key"."it''s"'` {
		t.Errorf("quoted keys: %s", got)
	}
	// A key's " and \ are escaped in the path; MySQL reads a backslash
	// in a string as an escape too, so it is doubled once more there.
	if got := JSONPath(db.MySQL, "doc", []any{`a"b\c`}, true); got != `doc->>'$."a\\"b\\\\c"'` {
		t.Errorf("MySQL escapes: %s", got)
	}
	if got := JSONPath(db.SQLite, "doc", []any{`a"b\c`}, true); got != `doc->>'$."a\"b\\c"'` {
		t.Errorf("SQLite escapes: %s", got)
	}
	if got := JSONPath(db.Postgres, "Doc", []any{"it's"}, true); got != `"Doc"->>'it''s'` {
		t.Errorf("quoted column: %s", got)
	}
}

// Only a cell with more to show than it shows gets a card: a JSON
// document, a time, a long or cut text; a plain value none.
func TestCellCardKinds(t *testing.T) {
	at := time.Date(2026, 3, 2, 9, 14, 0, 0, time.UTC)
	for _, tc := range []struct {
		v       any
		name    string
		typ     string
		clipped bool
		want    cellCardKind
	}{
		{"paid", "status", "text", false, cardNone},
		{int64(42), "id", "bigint", false, cardNone},
		{`{"a": 1}`, "payload", "jsonb", false, cardJSON},
		{`[1, 2]`, "tags", "text", false, cardJSON},
		{`{not json`, "payload", "text", false, cardNone},
		{at, "placed_at", "timestamptz", false, cardTime},
		{"2026-03-02 09:14:00", "placed_at", "timestamp", false, cardTime},
		{int64(1772442840), "created_at", "bigint", false, cardTime},
		{int64(1772442840), "views", "bigint", false, cardNone},
		{"2026-03-02 09:14:00", "ordered_at", "TEXT", false, cardTime},
		{"2026-03-02 09:14:00", "note", "TEXT", false, cardNone},
		{strings.Repeat("x", 300), "note", "text", false, cardText},
		{"two\nlines", "note", "text", false, cardText},
		{"fits", "note", "text", true, cardText},
	} {
		if got := cellCardOf(tc.v, tc.name, tc.typ, tc.clipped); got != tc.want {
			t.Errorf("%v in %s %s: %v, want %v", tc.v, tc.name, tc.typ, got, tc.want)
		}
	}
}

// A JSON document keeps its keys' order.
func TestParseJSONOrdered(t *testing.T) {
	n, err := parseJSON(`{"z": 1, "a": [true, null, {"k": "v"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.children) != 2 || n.children[0].key != "z" || n.children[1].key != "a" || len(n.children[1].children) != 3 {
		t.Fatalf("%+v", n)
	}
	if leaf := n.children[1].children[2].children[0]; leaf.key != "k" || leaf.scalar != `"v"` {
		t.Fatalf("leaf %+v", leaf)
	}
}

// Resting on a JSON cell shows it indented; a click on a key copies its
// path, and Copy Pretty the indented document.
func TestJSONCardCopy(t *testing.T) {
	h := NewFakeHost(t)
	src := &Source{
		Cols: []db.ColumnInfo{{Name: "id", Type: "INTEGER"}, {Name: "payload", Type: "TEXT"}},
		Rows: [][]any{{int64(1), `{"items":[{"sku":"A-1","qty":2}],"gift":true}`}},
	}
	g := NewGrid()
	g.SQLOpts = export.Options{Engine: db.SQLite}
	var copied string
	tt := ui.NewTester(func(c *ui.Context) {
		g.View(c, h, src)
	}, 900, 600)
	tt.Frame()
	askCard(t, tt, `{"items":[{"sku":"A-1","qty":2}],"gift":true}`)
	if !tt.HasText(`"sku"`) {
		t.Fatalf("no card: %q", tt.Texts())
	}
	testutil.Snapshot(t, tt, "json-card")
	if err := tt.Click(`"sku"`); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	copied = tt.Clipboard()
	if copied != `payload->>'$.items[0].sku'` {
		t.Fatalf("copied %q", copied)
	}
	if err := tt.Click("Copy Pretty"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !strings.Contains(tt.Clipboard(), "\n  \"items\": [") {
		t.Fatalf("pretty %q", tt.Clipboard())
	}
}

// refTable opens orders, whose customer_id points at customers, on a
// SQLite file, in env; it returns the host, the tester and the tab once
// its rows and keys are read.
func refTable(t *testing.T, env db.Environment) (*FakeHost, *ui.Tester, *TableTab) {
	t.Helper()
	h := NewFakeHost(t)
	file := filepath.Join(t.TempDir(), "shop.sqlite")
	os.WriteFile(file, nil, 0o600)
	cfg := db.Config{ID: "shop", Name: "shop", Engine: db.SQLite, Database: file, Env: env}
	setup, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT, password TEXT)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers(id))`,
		`INSERT INTO customers VALUES (42, 'Ada Lovelace', 'hunter2')`,
		`INSERT INTO orders VALUES (1, 42)`,
	} {
		if _, err := setup.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	setup.Close()
	cn := h.AddConn(cfg)
	tt := ui.NewTester(h.View, 1100, 700)
	h.OpenTable(cn, "", db.Object{Schema: "main", Name: "orders", Kind: db.KindTable, Rows: -1}, PageData)
	testutil.WaitFor(t, tt, "the rows", func() bool {
		tb, ok := h.ActiveTab().(*TableTab)
		return ok && len(tb.view.src.Rows) == 1 && len(tb.view.fks) == 1
	})
	return h, tt, h.ActiveTab().(*TableTab)
}

// askCard asks for the card of the cell or header showing s, with
// Alt+click: the grid shows none on hover.
func askCard(t *testing.T, tt *ui.Tester, s string) {
	t.Helper()
	r, ok := tt.Find(s)
	if !ok {
		t.Fatalf("no %q: %q", s, tt.Texts())
	}
	tt.ClickAtWith(ui.Alt, r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	tt.Frame()
}

// A foreign key's cell shows the row it points at, read once, its
// sensitive values masked.
func TestFKPreviewFetchesOnce(t *testing.T) {
	h, tt, _ := refTable(t, db.Development)
	askCard(t, tt, "42")
	testutil.WaitFor(t, tt, "the row", func() bool { return tt.HasText("Ada Lovelace") })
	if tt.HasText("hunter2") {
		t.Fatal("the password shows")
	}
	reads := len(h.Events)
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Advance(time.Second) // two clicks this close would be a double click, which edits
	askCard(t, tt, "42")
	tt.Frame()
	if !tt.HasText("Ada Lovelace") || len(h.Events) != reads {
		t.Fatalf("read again: %d events, then %d", reads, len(h.Events))
	}
}

// On production, the row is read only once asked.
func TestFKPreviewProductionAsks(t *testing.T) {
	_, tt, _ := refTable(t, db.Production)
	askCard(t, tt, "42")
	if !tt.HasText("Show Row") || tt.HasText("Ada Lovelace") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if err := tt.Click("Show Row"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the row", func() bool { return tt.HasText("Ada Lovelace") })
}

// A table's column header shows its column's card; a result's column
// that is no table's shows its name and type.
func TestHeaderCard(t *testing.T) {
	_, tt, _ := refTable(t, db.Development)
	askCard(t, tt, "customer_id")
	testutil.WaitFor(t, tt, "the card", func() bool { return tt.HasText("→ customers.id") })

	h := NewFakeHost(t)
	src := &Source{Cols: []db.ColumnInfo{{Name: "total", Type: "numeric"}}, Rows: [][]any{{"1.5"}}}
	g := NewGrid()
	tt = ui.NewTester(func(c *ui.Context) { g.View(c, h, src) }, 600, 300)
	tt.Frame()
	askCard(t, tt, "total")
	if !tt.HasText("numeric") || !hasPart(tt, "not a column of one table") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

// A JSON card follows its cell: rows read again with another value at
// the same place show the new document.
func TestJSONCardFollowsValue(t *testing.T) {
	h := NewFakeHost(t)
	src := &Source{Cols: []db.ColumnInfo{{Name: "doc", Type: "TEXT"}}, Rows: [][]any{{`{"oldkey":1}`}}}
	g := NewGrid()
	tt := ui.NewTester(func(c *ui.Context) { g.View(c, h, src) }, 700, 400)
	tt.Frame()
	askCard(t, tt, `{"oldkey":1}`)
	if !tt.HasText(`"oldkey"`) {
		t.Fatalf("no card: %q", tt.Texts())
	}
	src.Rows[0][0] = `{"newkey":2}`
	tt.Frame()
	tt.Frame()
	if tt.HasText(`"oldkey"`) || !tt.HasText(`"newkey"`) {
		t.Fatalf("the card kept the old document: %q", tt.Texts())
	}
}

// Each node of a document has an id of its own, the same keys under
// different parents included, and the path to it.
func TestJSONNodeIDs(t *testing.T) {
	n, err := parseJSON(`{"items":[{"sku":"A"},{"sku":"B"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	a, b := n.children[0].children[0].children[0], n.children[0].children[1].children[0]
	if a.id == b.id {
		t.Fatalf("both sku nodes have the id %q", a.id)
	}
	if got := JSONPath(db.SQLite, "doc", b.path(), true); got != `doc->>'$.items[1].sku'` {
		t.Fatalf("path %s", got)
	}
}

// Resting on a cell or a header shows no card; its menu's Show Card does.
func TestGridCardsOnlyWhenAsked(t *testing.T) {
	h := NewFakeHost(t)
	src := &Source{Cols: []db.ColumnInfo{{Name: "doc", Type: "TEXT"}}, Rows: [][]any{{`{"a":1}`}}}
	g := NewGrid()
	tt := ui.NewTester(func(c *ui.Context) { g.View(c, h, src); widgets.FinishCards(c) }, 700, 400)
	tt.Frame()
	for _, s := range []string{`{"a":1}`, "doc"} {
		r, _ := tt.Find(s)
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
		tt.Advance(3 * widgets.DefaultHoverDelay)
		tt.Frame()
		if hasPart(tt, "Click a key") || hasPart(tt, "its name and type only") {
			t.Fatalf("a card on resting on %s: %q", s, tt.Texts())
		}
	}
	if err := tt.RightClick(`{"a":1}`); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Show Card"); err != nil {
		t.Fatalf("%v: %q", err, tt.Menu())
	}
	tt.Frame()
	tt.Frame()
	if !hasPart(tt, "Click a key") {
		t.Fatalf("no card from the menu: %q", tt.Texts())
	}
}

// Alt+click on a header shows its card, without sorting by it.
func TestHeaderAltClickNoSort(t *testing.T) {
	h := NewFakeHost(t)
	src := &Source{Cols: []db.ColumnInfo{{Name: "total", Type: "numeric"}}, Rows: [][]any{{"2"}, {"1"}}}
	g := NewGrid()
	tt := ui.NewTester(func(c *ui.Context) { g.View(c, h, src); widgets.FinishCards(c) }, 600, 300)
	tt.Frame()
	askCard(t, tt, "total")
	if !hasPart(tt, "its name and type only") {
		t.Fatalf("no card: %q", tt.Texts())
	}
	if len(g.sort) != 0 {
		t.Fatalf("Alt+click sorted: %+v", g.sort)
	}
}

// Every cell offers its card: a plain value's shows its column and the
// value, with Copy.
func TestPlainCellCard(t *testing.T) {
	h := NewFakeHost(t)
	src := &Source{Cols: []db.ColumnInfo{{Name: "status", Type: "TEXT"}}, Rows: [][]any{{"paid"}, {nil}}}
	g := NewGrid()
	tt := ui.NewTester(func(c *ui.Context) { g.View(c, h, src); widgets.FinishCards(c) }, 700, 400)
	tt.Frame()
	if err := tt.RightClick("paid"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Show Card"); err != nil {
		t.Fatalf("%v: %q", err, tt.Menu())
	}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("status") || !tt.HasText("Copy") {
		t.Fatalf("no card: %q", tt.Texts())
	}
	tt.Click("Copy")
	tt.Frame()
	if tt.Clipboard() != "paid" {
		t.Fatalf("copied %q", tt.Clipboard())
	}
}

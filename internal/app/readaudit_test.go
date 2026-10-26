package app

import (
	"strings"
	"testing"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// findEvent returns the first event of the app's audit log that match
// accepts, or fails the test.
func findEvent(t *testing.T, a *App, what string, match func(audit.Event) bool) audit.Event {
	t.Helper()
	events := auditEvents(t, a)
	for _, e := range events {
		if match(e) {
			return e
		}
	}
	t.Fatalf("no audit entry for %s among %d: %+v", what, len(events), events)
	return audit.Event{}
}

// A copy's read of the source table is audited on the source's connection,
// with the text sent and the rows read.
func TestCopySourceReadAudited(t *testing.T) {
	a := newTestApp(t)
	lite := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	duck := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 800)
	for _, cn := range []*connection.Conn{lite, duck} {
		connectNow(t, a, tt, cn)
	}
	mustExec(t, lite.DB(), `CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO items VALUES (1, 'pen'), (2, 'ink')`)
	if x := copyTables(t, a, tt, lite, "main", []string{"items"}, duck, "main", copyCreate); x.err != "" {
		t.Fatal(x.err)
	}
	e := findEvent(t, a, "the source read", func(e audit.Event) bool {
		return e.Kind == audit.KindStatement && strings.HasPrefix(e.Statement, "SELECT ") && e.ConnectionID == lite.Config.ID
	})
	if !strings.Contains(e.Statement, "items") || e.Rows != 2 || e.Origin != audit.OriginApp || e.Error != "" {
		t.Fatalf("the source read: %+v", e)
	}
}

// A row compare's reads of both tables are audited, one entry for each
// connection.
func TestCompareRowsReadsAudited(t *testing.T) {
	a := newTestApp(t)
	lite := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	duck := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 800)
	for _, cn := range []*connection.Conn{lite, duck} {
		connectNow(t, a, tt, cn)
	}
	mustExec(t, lite.DB(), `CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO items VALUES (1, 'pen'), (2, 'ink'), (3, 'pad')`)
	mustExec(t, duck.DB(), `CREATE TABLE items (id INTEGER PRIMARY KEY, name VARCHAR)`, `INSERT INTO items VALUES (1, 'pen'), (4, 'nib')`)
	a.openRowCompare(lite, "", db.Object{Schema: "main", Name: "items", Kind: db.KindTable})
	x := a.rowCompare
	x.target.label = targetLabel(duck)
	testutil.WaitFor(t, tt, "the table chosen", func() bool { return x.table == "items" })
	a.startCompare(x)
	testutil.WaitFor(t, tt, "the comparison", func() bool { return !x.running })
	if x.err != "" {
		t.Fatal(x.err)
	}
	for _, c := range []struct {
		cn   *connection.Conn
		rows int64
	}{{lite, 3}, {duck, 2}} {
		e := findEvent(t, a, "the read on "+c.cn.Config.Name, func(e audit.Event) bool {
			return e.ConnectionID == c.cn.Config.ID && strings.Contains(e.Detail, "to compare")
		})
		if e.Kind != audit.KindStatement || e.Rows != c.rows || e.Origin != audit.OriginApp || !strings.Contains(e.Detail, "items") {
			t.Fatalf("the read on %s: %+v", c.cn.Config.Name, e)
		}
	}
}

// A fill's reads of another table's values and of a key's highest are
// audited.
func TestFillSamplesAudited(t *testing.T) {
	a := newTestApp(t)
	lite := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	tt := ui.NewTester(a.view, 1200, 800)
	connectNow(t, a, tt, lite)
	mustExec(t, lite.DB(),
		`CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO customers VALUES (1, 'a'), (2, 'b')`,
		`CREATE TABLE orders (n BIGINT NOT NULL UNIQUE, customer_id INTEGER NOT NULL REFERENCES customers (id))`,
	)
	if x := fillTable(t, a, tt, lite, "main", "orders", "5", nil); x.err != "" {
		t.Fatal(x.err)
	}
	values := findEvent(t, a, "the reference's values", func(e audit.Event) bool { return strings.Contains(e.Detail, "customers.id") })
	if values.Kind != audit.KindStatement || values.Rows != 2 || values.Origin != audit.OriginApp {
		t.Fatalf("the values read: %+v", values)
	}
	findEvent(t, a, "the key's highest", func(e audit.Event) bool { return strings.Contains(e.Detail, "orders.n") })
}

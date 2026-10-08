package query

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

func TestAutocomplete(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := newFakeQueryHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "")

	tt.Frame()
	tt.Type("SELECT * FROM shop.cu")
	tt.Frame()
	t.Logf("text %q focus %v open %v items %d", q.Editor.Text, q.Editor.HasFocus, q.ac.open, len(q.ac.items))
	testutil.WaitFor(t, tt, "table suggestions", func() bool { return q.ac.open && len(q.ac.items) > 0 })
	if q.ac.items[0].label != "customers" {
		t.Fatalf("suggestions %+v", q.ac.items)
	}
	testutil.Snapshot(t, tt, "autocomplete-table")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !strings.HasSuffix(q.Editor.Text, "shop.customers") || q.ac.open {
		t.Fatalf("after accepting: %q open %v", q.Editor.Text, q.ac.open)
	}
	tt.Type(" c WHERE c.em")
	testutil.WaitFor(t, tt, "column suggestions", func() bool { return q.ac.open && len(q.ac.items) > 0 && q.ac.items[0].label == "email" })
	testutil.Snapshot(t, tt, "autocomplete-column")
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if !strings.HasSuffix(q.Editor.Text, "c.email") {
		t.Fatalf("after accepting a column: %q", q.Editor.Text)
	}
}

func TestChart(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := newFakeQueryHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.view, 1360, 820)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	// The kinds a chart picks are TestChartDetect's; these are its looks.
	for _, c := range []struct{ name, sql string }{
		{"chart-bar", "SELECT country, count(*) AS customers, sum(o.total) / 100 AS revenue_hundreds FROM shop.customers c JOIN shop.orders o ON o.customer_id = c.id GROUP BY 1 ORDER BY 1"},
		{"chart-line", "SELECT date_trunc('day', placed_at) AS day, count(*) AS orders, avg(total) AS avg_total FROM shop.orders GROUP BY 1 ORDER BY 1"},
		{"chart-pie", "SELECT status, sum(total) AS revenue FROM shop.orders GROUP BY 1"},
	} {
		q := newEditor(t, a, tt, cn, c.sql)
		testutil.SetCaret(tt, &q.Editor, 3)
		q.Run(RunStatement)
		testutil.WaitFor(t, tt, "result", func() bool { return !q.Running && len(q.results) == 1 })
		r := q.results[0]
		if r.err != "" {
			t.Fatal(r.err)
		}
		q.editorH = 120
		tt.Frame()
		if err := tt.Click("Chart"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if !tt.HasText("Series") {
			t.Fatalf("%s: no chart", c.name)
		}
		if c.name == "chart-pie" {
			if err := tt.Click("Pie"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			testutil.Snapshot(t, tt, c.name)
			continue
		}
		tt.Move(900, 560)
		tt.Frame()
		testutil.Snapshot(t, tt, c.name)
	}
}

// A transaction outlives the results of its session: running a statement
// while an earlier result still has rows to read must not lose it.
func TestTransactionSurvivesNewRun(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	for _, cfg := range []db.Config{
		testutil.PGConfig(),
		{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop", Env: db.Production},
	} {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			cfg.Env = db.Production // manual commit
			ctx := context.Background()
			d, err := db.Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			table := db.QualifiedName(d.Dialect, "shop", "tx_probe")
			for _, q := range []string{"DROP TABLE IF EXISTS " + table, "CREATE TABLE " + table + " (id INT PRIMARY KEY, v INT)"} {
				if _, err := d.SQL.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			for i := range 3000 {
				d.SQL.Exec(fmt.Sprintf("INSERT INTO %s VALUES (%d, 0)", table, i))
			}
			d.Close()
			a := newFakeQueryHost(t)
			cn := a.AddConn(cfg)
			tt := ui.NewTester(a.view, 1200, 760)
			q := newEditor(t, a, tt, cn, "")
			run := func(sql string) {
				q.execute(safety.Analyze(&cn.Config, []string{sql}), !strings.HasPrefix(sql, "SELECT"))
				testutil.WaitFor(t, tt, sql, func() bool { return !q.Running })
			}
			run("UPDATE " + table + " SET v = 1 WHERE id = 7")
			if q.Tx != db.TxOpen {
				t.Fatalf("tx %v after the update", q.Tx)
			}
			run("SELECT * FROM " + table) // more rows than a page: the cursor stays open
			if r := q.results[0]; r.view.Done() {
				t.Fatal("the result was read whole; the test needs an open cursor")
			}
			run("SELECT v FROM " + table + " WHERE id = 7")
			if q.Tx != db.TxOpen || db.Display(q.results[0].view.Rows()[0][0]) != "1" {
				t.Fatalf("after a new run: tx %v, v %v, messages %+v", q.Tx, q.results[0].view.Rows(), q.messages)
			}
			q.endTx(false)
			testutil.WaitFor(t, tt, "rollback", func() bool { return !q.Running && q.Tx == db.TxNone })
		})
	}
}

func TestFindInEditor(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := New(a, cn, "", "q", "SELECT éa FROM t;\nselect b FROM u;\n-- Select me")
	a.AddTab(q)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Frame()
	tt.Type("select")
	tt.Frame()
	tt.Frame()
	if len(q.find.Matches) != 3 || q.find.Matches[1] != 18 {
		t.Fatalf("matches %v", q.find.Matches)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	if q.find.Current != 1 || q.Editor.SelStart != 18 || q.Editor.SelEnd != 24 {
		t.Fatalf("current %d, selection %d-%d", q.find.Current, q.Editor.SelStart, q.Editor.SelEnd)
	}
	testutil.Snapshot(t, tt, "find")
}

func TestReplaceInEditor(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := New(a, cn, "", "q", "select a FROM t;\nselect b FROM u;\nselect c")
	a.AddTab(q)
	tt.Frame()
	tt.Key(ui.Cmd|ui.Alt, ui.KeyF)
	tt.Frame()
	tt.Type("select")
	tt.Frame()
	if !q.find.Replacing || len(q.find.Matches) != 3 {
		t.Fatalf("replacing %v, matches %v", q.find.Replacing, q.find.Matches)
	}
	q.find.Replacement = "SELECT DISTINCT"
	tt.Frame()
	// A replacement holding the query is not found again: the next match
	// is the second statement's.
	if err := tt.Click("Replace"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if q.Editor.Text != "SELECT DISTINCT a FROM t;\nselect b FROM u;\nselect c" || q.find.Current != 1 || q.Editor.SelStart != 26 {
		t.Fatalf("after Replace: %q, current %d, selection at %d", q.Editor.Text, q.find.Current, q.Editor.SelStart)
	}
	testutil.Snapshot(t, tt, "replace")
	before := q.Editor.Text
	if err := tt.Click("Replace All"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// Finding ignores case, so the statement replaced already matches too.
	if q.Editor.Text != "SELECT DISTINCT DISTINCT a FROM t;\nSELECT DISTINCT b FROM u;\nSELECT DISTINCT c" || a.Toasts[len(a.Toasts)-1] != "Replaced 3 matches" {
		t.Fatalf("after Replace All: %q, toasts %v", q.Editor.Text, a.Toasts)
	}
	a.ToastRun()
	tt.Frame()
	if q.Editor.Text != before {
		t.Fatalf("undo left %q", q.Editor.Text)
	}
}

func TestExplain(t *testing.T) {
	testutil.Integration(t)
	file := t.TempDir() + "/e.sqlite"
	os.WriteFile(file, nil, 0o600)
	for _, cfg := range []db.Config{
		testutil.PGConfig(),
		{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"},
		{ID: "ch", Name: "ch", Engine: db.ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher"},
		{ID: "sq", Name: "sq", Engine: db.SQLite, Database: file},
	} {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			a := newFakeQueryHost(t)
			cn := a.AddConn(cfg)
			tt := ui.NewTester(a.view, 1000, 700)
			q := newEditor(t, a, tt, cn, "SELECT 1 + 1 AS two;")
			testutil.SetCaret(tt, &q.Editor, 3)
			q.Run(RunExplain)
			testutil.WaitFor(t, tt, "plan", func() bool { return !q.Running && len(q.results) == 1 })
			if r := q.results[0]; r.err != "" || len(r.view.Rows()) == 0 {
				t.Fatalf("explain: %q %v", r.err, r.view.Rows())
			}
		})
	}
}

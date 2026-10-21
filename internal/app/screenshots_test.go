package app

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/saanuregh/agent-cli-connector"
	"github.com/saanuregh/agent-cli-connector/agenttest"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/datamodel"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/chat"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/panes"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// screenshotter returns the function that writes the image of a frame to
// the folder DGOPHER_SCREENSHOTS names, and skips the test without one.
func screenshotter(t *testing.T) func(tt *ui.Tester, name string) {
	t.Helper()
	out := os.Getenv("DGOPHER_SCREENSHOTS")
	if out == "" {
		t.Skip("DGOPHER_SCREENSHOTS names no folder for the docs' images")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	return func(tt *ui.Tester, name string) {
		t.Helper()
		// What comes in, as a tab opened or a dialog, done coming.
		tt.Frame()
		tt.Advance(time.Second)
		f, err := os.Create(filepath.Join(out, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestScreenshots renders the images of the README and the docs, dark,
// from the sample shop and connections made up around it, into the folder
// DGOPHER_SCREENSHOTS names: docs/development.md says how.
func TestScreenshots(t *testing.T) {
	shot := screenshotter(t)

	a := newBareApp(t)
	a.settings.Theme = "dark"
	p, err := a.addProject(newProjectDir(t, "acme-analytics"))
	if err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.view, 1440, 900)
	tt.SetDark(true)
	tt.SetScale(2)
	a.openSampleIn(p)
	testutil.WaitFor(t, tt, "the sample", func() bool { return a.tabs().Len() == 1 })
	a.closeTab(0)
	shop := a.projectConns(p)[0]
	shop.Config.Name = "shop"
	for _, cfg := range []db.Config{
		{ID: "billing", Name: "billing", Engine: db.Postgres, Host: "billing.db.acme.internal", Port: 5432, User: "analyst", Database: "billing", Env: db.Production, ReadOnly: true},
		{ID: "inventory", Name: "inventory", Engine: db.MySQL, Host: "inventory.staging.acme.internal", Port: 3306, User: "app", Database: "inventory", Env: db.Staging},
		{ID: "events", Name: "events", Engine: db.ClickHouse, Host: "clickhouse.acme.internal", Port: 9000, User: "default", Database: "events", Env: db.Development},
		{ID: "sessions", Name: "sessions", Engine: db.Redis, Host: "cache.acme.internal", Port: 6379, Env: db.Development},
	} {
		addConn(a, cfg)
	}

	// Query files, a dashboard and a data model, as a team keeps them.
	header := connection.HeaderLine(shop)
	files := map[string]string{
		"revenue-by-country.sql": `-- Revenue by country, cancelled orders left out.
SELECT c.country,
       count(DISTINCT o.id)                      AS orders,
       count(DISTINCT c.id)                      AS customers,
       round(sum(i.quantity * i.unit_price), 2)  AS revenue,
       round(sum(i.quantity * i.unit_price)
             / count(DISTINCT o.id), 2)          AS avg_order
FROM orders o
JOIN customers c   ON c.id = o.customer_id
JOIN order_items i ON i.order_id = o.id
WHERE o.status <> 'cancelled'
GROUP BY c.country
ORDER BY revenue DESC;

-- Who bought the most.
SELECT c.name, c.email, count(*) AS orders
FROM customers c JOIN orders o ON o.customer_id = c.id
GROUP BY c.id
ORDER BY orders DESC
LIMIT 10;
`,
		"monthly-revenue.sql":      "SELECT * FROM revenue_by_month;\n",
		"cleanup/stale-orders.sql": "SELECT * FROM orders WHERE status = 'new' AND ordered_at < date('now', '-30 days');\n",
	}
	for name, text := range files {
		path := filepath.Join(p.Queries, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(header+text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a.ScanQueries(p, true)
	id := strings.TrimPrefix(shop.Config.ID, p.Prefix)
	sales := &dashboard.Dashboard{Name: "Sales overview", Panels: []dashboard.Panel{
		{Title: "Revenue", Connection: id, View: dashboard.ViewValue, Width: 1,
			SQL: `SELECT round(sum(i.quantity * i.unit_price), 2) AS "net of cancellations" FROM order_items i JOIN orders o ON o.id = i.order_id WHERE o.status <> 'cancelled'`},
		{Title: "Orders", Connection: id, View: dashboard.ViewValue, Width: 1, SQL: `SELECT count(*) AS "placed" FROM orders WHERE status <> 'cancelled'`},
		{Title: "Customers", Connection: id, View: dashboard.ViewValue, Width: 1, SQL: `SELECT count(*) AS "registered" FROM customers`},
		{Title: "Products", Connection: id, View: dashboard.ViewValue, Width: 1, SQL: `SELECT count(*) AS "in the catalog" FROM products`},
		{Title: "Revenue by month", Connection: id, View: dashboard.ViewChart, Width: 2, SQL: "SELECT month, revenue FROM revenue_by_month",
			Chart: &dataview.ChartSettings{Kind: "Area", X: "month", Series: []string{"revenue"}}},
		{Title: "Customers by country", Connection: id, View: dashboard.ViewChart, Width: 2,
			SQL:   "SELECT country, count(*) AS customers FROM customers GROUP BY 1 ORDER BY 2 DESC",
			Chart: &dataview.ChartSettings{Kind: "Bar", X: "country", Series: []string{"customers"}}},
		{Title: "Best sellers", Connection: id, View: dashboard.ViewTable, Width: 4,
			SQL: "SELECT p.name AS product, p.category, sum(i.quantity) AS units, round(sum(i.quantity * i.unit_price), 2) AS revenue FROM order_items i JOIN products p ON p.id = i.product_id GROUP BY p.id ORDER BY revenue DESC LIMIT 6"},
	}}
	dashPath := dashboard.File(p, sales.Name)
	if _, err := sales.Save(dashPath, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (&datamodel.Model{Name: "Shop schema", Engine: db.SQLite}).Save(datamodel.File(p, "Shop schema"), nil); err != nil {
		t.Fatal(err)
	}

	// The main window: the shop's tables, a table, a dashboard, and an
	// editor with its result.
	key := connection.SchemaKey{Schema: "main"}
	testutil.WaitFor(t, tt, "the shop's tables", func() bool { return shop.Objects[key] != nil })
	var orders db.Object
	for _, o := range shop.Objects[key] {
		if o.Name == "orders" {
			orders = o
			a.OpenTable(shop, "", o, dataview.PageData)
		}
	}
	a.openDashboard(p, dashPath)
	a.openQueryFile(p, "monthly-revenue.sql", nil)
	var q *query.Tab
	a.openQueryFile(p, "revenue-by-country.sql", func(t *query.Tab) { q = t })
	testutil.WaitFor(t, tt, "the editor", func() bool { return q != nil })
	a.nav.expand(navNode{kind: nodeFolder, conn: shop.Config.ID, schema: "main", folder: folderTables})
	a.nav.expand(navNode{kind: nodeQueries, projectDir: p.Dir})
	a.nav.expand(navNode{kind: nodeDashboards, projectDir: p.Dir})
	a.nav.expand(navNode{kind: nodeModels, projectDir: p.Dir})
	testutil.SetCaret(tt, q.Editor, len(header)+60)
	q.Run(query.RunStatement)
	testutil.WaitFor(t, tt, "the result", func() bool { return resultRows(tt, q, -1) })
	tt.Frame()
	shot(tt, "main")

	// The same window, light.
	tt.SetDark(false)
	tt.Frame()
	shot(tt, "main-light")
	tt.SetDark(true)
	tt.Frame()

	// The dashboard, its panels read.
	a.openDashboard(p, dashPath)
	testutil.WaitFor(t, tt, "the panels", func() bool { return tt.HasText("Best sellers") && testutil.HasTextContaining(tt, "Hardware") })
	shot(tt, "dashboard")

	// The shop's ER diagram.
	dataview.OpenER(a, shop, "", "main")
	testutil.WaitFor(t, tt, "the diagram", func() bool { return tt.HasText("order_items") && tt.HasText("unit_price") })
	shot(tt, "er-diagram")

	// What a DELETE without WHERE asks on production.
	prodCfg := shop.Config
	prodCfg.ID, prodCfg.Name, prodCfg.Env = "shop-prod", "shop-prod", db.Production
	prod := addConn(a, prodCfg)
	pq := newAppEditor(t, a, tt, prod, "DELETE FROM orders;\n")
	testutil.WaitFor(t, tt, "connect", func() bool { return prod.Status == connection.StatusConnected })
	pq.Run(query.RunStatement)
	testutil.WaitFor(t, tt, "the question", func() bool {
		return testutil.HasTextContaining(tt, "shop-prod") && testutil.HasTextContaining(tt, "Type")
	})
	shot(tt, "production-confirm")

	// The question left unanswered covers the screens below.
	a.confirm = nil
	a.closeTab(a.tabs().Len() - 1)

	// The changes of a grid edit, as SQL to review before they are applied.
	a.OpenTable(shop, "", orders, dataview.PageData)
	testutil.WaitFor(t, tt, "the orders", func() bool { return tt.HasText("shipped") })
	if err := tt.Click("shipped"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("delivered")
	tt.Key(0, ui.KeyEnter)
	tt.Key(ui.Cmd, ui.KeyS)
	testutil.WaitFor(t, tt, "the review", func() bool { return a.confirm != nil })
	shot(tt, "edit-review")
	a.confirm.OnConfirm()
	a.confirm = nil
	testutil.WaitFor(t, tt, "the edit audited", func() bool {
		for _, e := range auditEvents(t, a) {
			if e.Kind == audit.KindEdit {
				return true
			}
		}
		return false
	})

	// The audit log of the project.
	a.openAuditOf(p)
	testutil.WaitFor(t, tt, "the log", func() bool { return a.history.audit != nil && a.history.audit.read })
	shot(tt, "audit-log")
	a.history = nil

	notebookShots(t, a, tt, p, shop, q, shot)
}

// notebookShots renders a notebook, two groups side by side, and an
// editor's AI chat.
func notebookShots(t *testing.T, a *App, tt *ui.Tester, p *project.Project, shop *connection.Conn, q *query.Tab, shot func(*ui.Tester, string)) {
	// A notebook: Markdown, and cells each with its results.
	nbText := connection.HeaderLine(shop) + `-- view: notebook

-- %% [markdown]
-- # Orders this quarter
--
-- Where the orders stand, then what they hold.

-- %%
SELECT status, count(*) AS orders
FROM orders
GROUP BY status
ORDER BY orders DESC;

-- %%
SELECT p.category, sum(i.quantity) AS units
FROM order_items i JOIN products p ON p.id = i.product_id
GROUP BY p.category
ORDER BY units DESC;
`
	if err := os.WriteFile(filepath.Join(p.Queries, "orders-notebook.sql"), []byte(nbText), 0o644); err != nil {
		t.Fatal(err)
	}
	var nb *query.Tab
	a.openQueryFile(p, "orders-notebook.sql", func(t *query.Tab) { nb = t })
	testutil.WaitFor(t, tt, "the notebook", func() bool { return nb != nil && nb.Notebook() })
	if err := tt.Click("Run All"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the cells' results", func() bool {
		return !nb.Running && tt.HasText("[2]") && testutil.HasTextContaining(tt, "Hardware")
	})
	shot(tt, "notebook")

	// Two groups: the editor and its result, the dashboard beside.
	a.closeTabOf(nb)
	a.window.tabs().Select(q)
	a.openDashboard(p, dashboard.File(p, "Sales overview"))
	a.splitTab(a.main, a.main.panes.Focused(), panes.Right)
	a.main.panes.Groups()[0].Tabs.Select(q)
	testutil.WaitFor(t, tt, "the groups", func() bool {
		return a.main.panes.Len() == 2 && tt.HasText("Best sellers") && resultRows(tt, q, -1)
	})
	shot(tt, "groups")
	a.joinGroups(a.main)

	// The editor's AI chat: a question about its statement, the schema
	// read, and SQL to insert.
	saved := chat.NewProvider
	defer func() { chat.NewProvider, widgets.ShadersWithoutGPU = saved, false }()
	widgets.ShadersWithoutGPU = true
	chat.NewProvider = func(string) agent.Provider {
		return agenttest.New(func(turn *agenttest.Turn) error {
			turn.Reasoning("They want revenue per order by country; the statement already works it out.")
			turn.ToolCall(agent.ToolHost, "mcp__host__describe_table", `{"table":"orders"}`, `{"columns":["id","customer_id","status","ordered_at"]}`)
			turn.Text("Your statement already has it as **avg_order**. To rank by it instead of total revenue, order by that column:\n\n" +
				"```sql\nSELECT c.country,\n       round(sum(i.quantity * i.unit_price)\n             / count(DISTINCT o.id), 2) AS avg_order\nFROM orders o\nJOIN customers c   ON c.id = o.customer_id\nJOIN order_items i ON i.order_id = o.id\nWHERE o.status <> 'cancelled'\nGROUP BY c.country\nORDER BY avg_order DESC;\n```\n\n" +
				"Countries with few orders can lead this list: add `HAVING count(DISTINCT o.id) >= 5` to keep them out.")
			return nil
		})
	}
	a.window.tabs().Select(q)
	q.ToggleChat()
	tt.Frame()
	q.Chat().Send("Which countries spend the most per order?")
	testutil.WaitFor(t, tt, "the reply", func() bool { return testutil.HasTextContaining(tt, "HAVING") && !q.Chat().Busy() })
	shot(tt, "ai-chat")
}

// TestScreenshotsServers renders the images that need a server, which the
// tests of the integration servers use too: it runs only with
// DGOPHER_IT. docs/development.md says how. Each image has an app of its
// own, as the status bar keeps what the last connection said.
func TestScreenshotsServers(t *testing.T) {
	testutil.Integration(t)
	shot := screenshotter(t)
	testutil.SeedPostgres(t)

	start := func(t *testing.T) (*App, *ui.Tester) {
		a := newBareApp(t)
		a.settings.Theme = "dark"
		if _, err := a.addProject(newProjectDir(t, "acme-analytics")); err != nil {
			t.Fatal(err)
		}
		tt := ui.NewTester(a.view, 1440, 900)
		tt.SetDark(true)
		tt.SetScale(2)
		return a, tt
	}
	warehouse := testutil.PGConfig()
	warehouse.ID, warehouse.Name = "warehouse", "warehouse"

	// The plan of a join as Postgres ran it: the steps, the rows found
	// against the rows estimated, and the share of the time of each.
	t.Run("query-plan", func(t *testing.T) {
		a, tt := start(t)
		pg := addConn(a, warehouse)
		plan := newAppEditor(t, a, tt, pg, `SELECT c.country,
       count(*)                 AS orders,
       round(sum(o.total), 2)   AS revenue
FROM shop.orders o
JOIN shop.customers c ON c.id = o.customer_id
WHERE o.status <> 'new'
GROUP BY c.country
ORDER BY revenue DESC;
`)
		testutil.WaitFor(t, tt, "connect", func() bool { return pg.Status == connection.StatusConnected })
		testutil.SetCaret(tt, plan.Editor, 3)
		plan.Run(query.RunExplainAnalyze)
		testutil.WaitFor(t, tt, "the plan", func() bool { return !plan.Running && testutil.HasTextContaining(tt, "Scan") })
		tt.Frame()
		shot(tt, "query-plan")
	})

	// What a server is doing: a statement that runs for a while.
	t.Run("server-activity", func(t *testing.T) {
		a, tt := start(t)
		pg := addConn(a, warehouse)
		ctx, stop := context.WithCancel(context.Background())
		t.Cleanup(stop)
		busy, err := db.Open(ctx, warehouse, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { busy.Close() })
		go busy.SQL.ExecContext(ctx, "SELECT pg_sleep(300) /* nightly revenue rollup */")
		a.openActivity(pg)
		testutil.WaitFor(t, tt, "the running statement", func() bool { return testutil.HasTextContaining(tt, "nightly revenue rollup") })
		tt.Frame()
		shot(tt, "server-activity")
	})

	// A Redis database: its keys, and a hash opened.
	t.Run("redis", func(t *testing.T) {
		a, tt := start(t)
		cfg := db.Config{ID: "cache", Name: "cache", Engine: db.Redis, Host: "127.0.0.1", Port: 16379, Database: "3", Env: db.Development}
		ctx := context.Background()
		kv, err := db.OpenRedis(ctx, cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		kv.Do(ctx, []string{"FLUSHDB"})
		for i := range 12 {
			kv.Do(ctx, []string{"SET", fmt.Sprintf("session:%04x", 0x3a10+i*97), `{"user":42,"roles":["admin"]}`, "EX", "3600"})
		}
		kv.Do(ctx, []string{"HSET", "user:42", "name", "Ada Lovelace", "email", "ada@example.com", "plan", "pro", "country", "GB"})
		kv.Do(ctx, []string{"RPUSH", "queue:emails", "welcome:42", "digest:7", "receipt:1031"})
		kv.Close()
		a.openRedis(addConn(a, cfg))
		testutil.WaitFor(t, tt, "the keys", func() bool { return tt.HasText("user") })
		if err := tt.Click("user"); err != nil {
			t.Fatal(err)
		}
		tt.Key(0, ui.KeyRight)
		testutil.WaitFor(t, tt, "the key", func() bool { return tt.HasText("42") })
		if err := tt.Click("42"); err != nil {
			t.Fatal(err)
		}
		testutil.WaitFor(t, tt, "the hash", func() bool { return tt.HasText("Ada Lovelace") })
		tt.Frame()
		shot(tt, "redis")
	})
}

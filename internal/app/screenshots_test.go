package app

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/datamodel"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// TestScreenshots renders the images of the README and the docs, dark,
// from the sample shop and connections made up around it, into the folder
// DGOPHER_SCREENSHOTS names: docs/development.md says how.
func TestScreenshots(t *testing.T) {
	out := os.Getenv("DGOPHER_SCREENSHOTS")
	if out == "" {
		t.Skip("DGOPHER_SCREENSHOTS names no folder for the docs' images")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	shot := func(tt *ui.Tester, name string) {
		t.Helper()
		f, err := os.Create(filepath.Join(out, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}

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
	testutil.WaitFor(t, tt, "the sample", func() bool { return len(a.tabs) == 1 })
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
	for _, o := range shop.Objects[key] {
		if o.Name == "orders" {
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
	testutil.SetCaret(tt, &q.Editor, len(header)+60)
	q.Run(query.RunStatement)
	testutil.WaitFor(t, tt, "the result", func() bool { return resultRows(tt, q, -1) })
	tt.Frame()
	shot(tt, "main")

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
}

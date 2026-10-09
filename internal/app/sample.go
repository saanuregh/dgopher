package app

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/project"
)

// sampleQuery is the editor the sample database opens with, naming the
// keys as they are set.
func sampleQuery() string {
	return strings.NewReplacer("{run}", keymap.Hint("Running", keymap.Run), "{apply}", keymap.Hint("applied", keymap.Apply),
		"{palette}", keymap.Hint("The palette", keymap.Palette)).Replace(sampleText)
}

// The sample database: a small shop in SQLite, made on this computer, to
// try the app without a server.
const sampleText = `-- A sample shop, to try DGopher. {run} runs the statement at the caret.
SELECT c.country,
       count(DISTINCT o.id) AS orders,
       round(sum(i.quantity * i.unit_price), 2) AS revenue
FROM orders o
JOIN customers c ON c.id = o.customer_id
JOIN order_items i ON i.order_id = o.id
WHERE o.status <> 'cancelled'
GROUP BY c.country
ORDER BY revenue DESC;

-- Then: open a table from the navigator, double-click a cell to edit it,
-- and review the SQL before it is {apply}. {palette} lists every command.
SELECT * FROM revenue_by_month;
`

func sampleSchema() []string {
	return []string{
		`CREATE TABLE customers (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  email TEXT UNIQUE,
  country TEXT NOT NULL,
  created_at TEXT NOT NULL
)`,
		`CREATE TABLE products (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  category TEXT NOT NULL,
  price REAL NOT NULL CHECK (price >= 0)
)`,
		`CREATE TABLE orders (
  id INTEGER PRIMARY KEY,
  customer_id INTEGER NOT NULL REFERENCES customers(id),
  ordered_at TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'new'
)`,
		`CREATE TABLE order_items (
  order_id INTEGER NOT NULL REFERENCES orders(id),
  product_id INTEGER NOT NULL REFERENCES products(id),
  quantity INTEGER NOT NULL,
  unit_price REAL NOT NULL,
  PRIMARY KEY (order_id, product_id)
)`,
		`CREATE INDEX orders_customer ON orders (customer_id)`,
		`CREATE VIEW revenue_by_month AS
SELECT substr(o.ordered_at, 1, 7) AS month, count(DISTINCT o.id) AS orders,
       round(sum(i.quantity * i.unit_price), 2) AS revenue
FROM orders o JOIN order_items i ON i.order_id = o.id
WHERE o.status <> 'cancelled'
GROUP BY 1 ORDER BY 1`,
	}
}

// makeSample writes the sample database at path.
func makeSample(ctx context.Context, path string) error {
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return err
	}
	d, err := db.Open(ctx, db.Config{Name: "sample", Engine: db.SQLite, Database: path}, nil)
	if err != nil {
		return err
	}
	defer d.Close()
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range sampleSchema() {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	r := rand.New(rand.NewPCG(7, 42)) // the same shop every time
	first := []string{"Ada", "Grace", "Linus", "Margaret", "Ken", "Barbara", "Dennis", "Frances", "Edsger", "Radia", "Tim", "Hedy", "Alan", "Katherine", "John", "Sophie"}
	last := []string{"Lovelace", "Hopper", "Torvalds", "Hamilton", "Thompson", "Liskov", "Ritchie", "Allen", "Dijkstra", "Perlman", "Berners-Lee", "Lamarr", "Turing", "Johnson", "Backus", "Wilson"}
	countries := []string{"DE", "IN", "US", "JP", "BR", "FR", "GB", "NG"}
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 400; i++ {
		name := first[r.IntN(len(first))] + " " + last[r.IntN(len(last))]
		email := fmt.Sprintf("%s.%d@example.com", strings.ToLower(strings.ReplaceAll(name, " ", ".")), i)
		created := start.Add(time.Duration(r.IntN(365*24)) * time.Hour)
		if _, err := tx.ExecContext(ctx, `INSERT INTO customers VALUES (?, ?, ?, ?, ?)`, i, name, email, countries[r.IntN(len(countries))], created.Format(time.DateTime)); err != nil {
			return err
		}
	}
	products := []struct {
		name, category string
		price          float64
	}{
		{"Mechanical keyboard", "Hardware", 129}, {"Ergonomic mouse", "Hardware", 59}, {"4K monitor", "Hardware", 399},
		{"USB-C dock", "Hardware", 189}, {"Standing desk", "Furniture", 549}, {"Desk lamp", "Furniture", 45},
		{"Noise-cancelling headphones", "Audio", 299}, {"Podcast microphone", "Audio", 149}, {"Webcam", "Video", 89},
		{"Laptop stand", "Furniture", 39}, {"Cable kit", "Accessories", 19}, {"Notebook (dotted)", "Accessories", 12},
		{"Fountain pen", "Accessories", 35}, {"Desk mat", "Accessories", 29}, {"Monitor arm", "Furniture", 119},
	}
	for i, p := range products {
		if _, err := tx.ExecContext(ctx, `INSERT INTO products VALUES (?, ?, ?, ?)`, i+1, p.name, p.category, p.price); err != nil {
			return err
		}
	}
	statuses := []string{"new", "paid", "paid", "shipped", "shipped", "shipped", "cancelled"}
	for o := 1; o <= 2000; o++ {
		at := start.Add(time.Duration(r.IntN(640*24*60)) * time.Minute)
		if _, err := tx.ExecContext(ctx, `INSERT INTO orders VALUES (?, ?, ?, ?)`, o, 1+r.IntN(400), at.Format(time.DateTime), statuses[r.IntN(len(statuses))]); err != nil {
			return err
		}
		for _, p := range r.Perm(len(products))[:1+r.IntN(3)] {
			if _, err := tx.ExecContext(ctx, `INSERT INTO order_items VALUES (?, ?, ?, ?)`, o, p+1, 1+r.IntN(3), products[p].price); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// openSample makes the sample database of the current project if needed,
// adds its connection and opens an editor on it. The file is in the
// project's .dgopher folder, which git ignores.
func (a *App) openSample() {
	a.withProject(a.openSampleIn)
}

func (a *App) openSampleIn(p *project.Project) {
	path := filepath.Join(p.Local.Dir(), "sample.sqlite")
	for _, cn := range a.projectConns(p) {
		if cn.Config.Engine == db.SQLite && cn.Config.Database == path {
			a.Connect(cn, func() {
				a.expandDefaults(cn)
				a.NewQueryTab(cn, "", sampleQuery())
			})
			return
		}
	}
	a.Background(func() func() {
		var err error
		if _, statErr := os.Stat(path); statErr != nil {
			err = makeSample(context.Background(), path)
		}
		return func() {
			if err != nil {
				os.Remove(path)
				a.ShowError("Could not make the sample database", err.Error())
				return
			}
			if !slices.Contains(a.projects, p) {
				return // removed meanwhile
			}
			cn := a.addConn(p, db.Config{Name: "Sample shop", Engine: db.SQLite, Database: path, Env: db.Development})
			if cn == nil {
				return
			}
			a.Connect(cn, func() {
				a.expandDefaults(cn)
				a.NewQueryTab(cn, "", sampleQuery())
			})
		}
	})
}

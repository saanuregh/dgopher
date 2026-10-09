// Package testutil holds what the packages' tests share: waiting for
// background work, snapshots, and the integration test servers.
package testutil

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/testutil/serverlock"
	"dgopher/internal/ui/editor"

	"github.com/egoist/mygo/ui"
)

// WaitFor draws frames until cond holds.
func WaitFor(t *testing.T, tt *ui.Tester, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; texts: %q", what, tt.Texts())
		}
		time.Sleep(20 * time.Millisecond)
		tt.Frame()
	}
	tt.Frame()
}

func Integration(t *testing.T) {
	t.Helper()
	if os.Getenv("DGOPHER_IT") == "" {
		t.Skip("set DGOPHER_IT=1 to run against the test servers")
	}
	// The packages' tests run in parallel processes, and seed and change
	// the same schemas: one test at a time uses the servers.
	serverlock.Lock(t)
}

func PGConfig() db.Config {
	return db.Config{ID: "pg", Name: "Shop (local)", Engine: db.Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dgopher", Database: "postgres", Env: db.Development}
}

func SeedPostgres(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, PGConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{
		`DROP SCHEMA IF EXISTS shop CASCADE`,
		`CREATE SCHEMA shop`,
		`CREATE TABLE shop.customers (id serial PRIMARY KEY, name text NOT NULL, email text UNIQUE, country text, created_at timestamptz NOT NULL DEFAULT now(), profile jsonb)`,
		`CREATE TABLE shop.orders (id bigserial PRIMARY KEY, customer_id int NOT NULL REFERENCES shop.customers(id), total numeric(10,2) NOT NULL, status text NOT NULL DEFAULT 'new', placed_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE INDEX orders_customer ON shop.orders (customer_id)`,
		`CREATE VIEW shop.revenue AS SELECT c.country, sum(o.total) AS revenue FROM shop.orders o JOIN shop.customers c ON c.id = o.customer_id GROUP BY 1`,
		`INSERT INTO shop.customers (name, email, country, created_at, profile)
		 SELECT 'Customer ' || i, 'customer' || i || '@example.com', (ARRAY['DE','IN','US','JP','BR'])[1 + i % 5],
		        timestamp '2026-01-01' + i * interval '37 minutes', jsonb_build_object('tier', CASE WHEN i % 7 = 0 THEN 'gold' ELSE 'basic' END, 'tags', jsonb_build_array('a', 'b'))
		 FROM generate_series(1, 2500) i`,
		`UPDATE shop.customers SET email = NULL WHERE id % 11 = 0`,
		`INSERT INTO shop.orders (customer_id, total, status, placed_at)
		 SELECT 1 + i % 2500, round((random() * 500)::numeric, 2), (ARRAY['new','paid','shipped'])[1 + i % 3], timestamp '2026-02-01' + i * interval '5 minutes'
		 FROM generate_series(1, 10000) i`,
		`ANALYZE shop.customers`, `ANALYZE shop.orders`,
	} {
		if _, err := d.SQL.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func HasTextContaining(tt *ui.Tester, part string) bool {
	for _, s := range tt.Texts() {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

// Snapshot writes the tester's last frame to $DGOPHER_SNAPSHOTS/name.png,
// to look at the interface without opening a window.
func Snapshot(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := os.Getenv("DGOPHER_SNAPSHOTS")
	if dir == "" {
		return
	}
	os.MkdirAll(dir, 0o755)
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

// SetCaret puts the editor's caret at a rune offset, as a click would.
func SetCaret(tt *ui.Tester, e *editor.Editor, at int) {
	e.PendingSel = &[2]int{at, at}
	tt.Frame()
	tt.Frame()
}

package db

import (
	"context"
	"testing"
	"time"
)

// A pool knows when it was last used: as it opened, then by a statement,
// rows read, or a catalog read.
func TestLastUsed(t *testing.T) {
	ctx := context.Background()
	d := newSQLiteDB(t)
	mustExec(t, d, `CREATE TABLE t (a INT)`)
	mustExec(t, d, `INSERT INTO t VALUES (1), (2)`)
	moved := func(what string, use func()) {
		t.Helper()
		before := d.LastUsed()
		if before.IsZero() {
			t.Fatal("never used")
		}
		time.Sleep(30 * time.Millisecond) // past a coarse clock's step
		use()
		if !d.LastUsed().After(before) {
			t.Fatalf("%s did not count as use", what)
		}
	}
	var s *Session
	moved("a session", func() {
		var err error
		if s, err = d.Session(ctx); err != nil {
			t.Fatal(err)
		}
	})
	defer s.Close()
	var c *Cursor
	moved("a statement", func() {
		var err error
		if c, err = s.Query(ctx, "SELECT a FROM t"); err != nil {
			t.Fatal(err)
		}
	})
	moved("rows read", func() {
		if _, err := c.Fetch(1); err != nil {
			t.Fatal(err)
		}
	})
	moved("a catalog read", func() {
		rows, err := d.Catalog().QueryContext(ctx, "SELECT 1")
		if err != nil {
			t.Fatal(err)
		}
		rows.Close()
	})
	moved("another database's pool", func() {
		if _, err := d.Database(ctx, "other"); err != nil {
			t.Fatal(err)
		}
	})
}

// A statement on another database of a PostgreSQL server uses the pool
// it came from.
func TestLastUsedOtherDatabase(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, err := Open(ctx, Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dgopher", Database: "postgres"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	other, err := d.Database(ctx, "template1")
	if err != nil {
		t.Fatal(err)
	}
	before := d.LastUsed()
	time.Sleep(30 * time.Millisecond)
	s, err := other.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !d.LastUsed().After(before) {
		t.Fatal("another database's session did not count as use")
	}
}

package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func openDuckMemory(t *testing.T) *Session {
	t.Helper()
	ctx := context.Background()
	d, err := Open(ctx, Config{Name: "duck", Engine: DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestDuckDBCancel(t *testing.T) {
	s := openDuckMemory(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	c, err := s.Query(ctx, "SELECT count(*) FROM range(10000000000) a, range(1000) b")
	if err == nil {
		_, err = c.Fetch(10)
	}
	if err == nil {
		t.Fatal("a cancelled query succeeded")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("cancel took %v", el)
	}
	c, err = s.Query(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatalf("after cancel: %v", err)
	}
	if rows, err := c.Fetch(1); err != nil || len(rows) != 1 {
		t.Fatalf("after cancel: %v %v", rows, err)
	}
}

func TestDuckDBExecWithParams(t *testing.T) {
	s := openDuckMemory(t)
	ctx := context.Background()
	if _, err := s.Exec(ctx, "CREATE TABLE t (a INTEGER, b VARCHAR)"); err != nil {
		t.Fatal(err)
	}
	n, err := s.Exec(ctx, "INSERT INTO t VALUES (?, ?)", 7, "seven")
	if err != nil || n != 1 {
		t.Fatalf("insert: %d rows, %v", n, err)
	}
	c, err := s.Query(ctx, "SELECT b FROM t WHERE a = ?", 7)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Fetch(10)
	if err != nil || len(rows) != 1 || rows[0][0] != "seven" {
		t.Fatalf("rows %v %v", rows, err)
	}
}

// The driver returns its own types for these; each shows as DuckDB's CLI
// shows it.
func TestDuckDBValuesDisplay(t *testing.T) {
	s := openDuckMemory(t)
	c, err := s.Query(context.Background(), `SELECT
		'7cfa2755-df1a-4ef9-9a03-bd6042b20544'::UUID,
		TIME '12:34:56.5',
		INTERVAL 14 MONTH + INTERVAL 1 DAY + INTERVAL 3 HOUR,
		INTERVAL 2 DAY,
		-INTERVAL 90 SECOND,
		MAP {'a': 1, 'b': 2},
		1.25::DECIMAL(10,2)`)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Fetch(1)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"7cfa2755-df1a-4ef9-9a03-bd6042b20544",
		"12:34:56.5",
		"1 year 2 months 1 day 03:00:00",
		"2 days",
		"-00:01:30",
		`{"a":1,"b":2}`,
		"1.25",
	}
	for i, w := range want {
		if got := Display(rows[0][i]); got != w {
			t.Errorf("column %d (%T): %q, want %q", i, rows[0][i], got, w)
		}
	}
}

// On a database of one connection the rows are read ahead up to MaxRows,
// and the cursor says when more were left.
func TestCursorSaysWhenTruncated(t *testing.T) {
	d, err := Open(context.Background(), Config{Name: "d", Engine: DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, err := d.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for n, want := range map[int]bool{MaxRows: false, MaxRows + 1: true} {
		c, err := s.Query(context.Background(), fmt.Sprintf("SELECT * FROM range(%d)", n))
		if err != nil {
			t.Fatal(err)
		}
		rows, _ := c.Fetch(MaxRows + 10)
		if len(rows) != MaxRows || c.Truncated() != want {
			t.Fatalf("%d rows: read %d, truncated %v", n, len(rows), c.Truncated())
		}
		c.Close()
	}
}

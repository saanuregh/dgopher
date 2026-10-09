package db

import (
	"context"
	"testing"
)

func TestNextNumber(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, sqliteFile(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, c := range []struct {
		rows string
		want int64
	}{
		{"", 1},
		{"(7), (2)", 8},
		{"(2.5)", 3},
		{"(-3.5)", -3},
		{"(-4)", -3},
		{"(9007199254740993)", 9007199254740994},
	} {
		if _, err := d.SQL.ExecContext(ctx, `DROP TABLE IF EXISTS t; CREATE TABLE t (n NUMERIC)`); err != nil {
			t.Fatal(err)
		}
		if c.rows != "" {
			if _, err := d.SQL.ExecContext(ctx, `INSERT INTO t VALUES `+c.rows); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := NextNumber(ctx, d, "main", "t", "n"); err != nil || got != c.want {
			t.Errorf("after %q: %d, %v; want %d", c.rows, got, err, c.want)
		}
	}
}

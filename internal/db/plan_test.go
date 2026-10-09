package db

import "testing"

func TestParseMySQLTree(t *testing.T) {
	text := `-> Sort: n DESC  (actual time=2.1..2.2 rows=10 loops=1)
    -> Table scan on <temporary>  (actual time=1.9..1.95 rows=10 loops=1)
        -> Aggregate using temporary table  (actual time=1.9..1.9 rows=10 loops=1)
            -> Table scan on orders  (cost=101 rows=1000) (actual time=0.05..0.8 rows=1000 loops=1)
    -> Index lookup on lines using order_id (order_id=orders.id)  (cost=0.25 rows=2) (actual time=0.002..0.003 rows=2 loops=1000)`
	p, ok := parseMySQLTree(text)
	if !ok || !p.Analyzed || p.Root.Op != "Sort" || p.Root.Detail != "n DESC" || len(p.Root.Children) != 2 {
		t.Fatalf("%+v", p.Root)
	}
	scan := p.Root.Children[0].Children[0].Children[0]
	if scan.Target != "orders" || scan.Cost != 101 || scan.Rows != 1000 || scan.ActualRows != 1000 {
		t.Fatalf("scan %+v", scan)
	}
	lookup := p.Root.Children[1]
	if lookup.Target != "lines using order_id" || lookup.Loops != 1000 || lookup.ActualRows != 2000 || lookup.Time != 3 {
		t.Fatalf("lookup %+v", lookup)
	}
	if _, ok := parseMySQLTree("id\tselect_type"); ok {
		t.Fatal("a table of EXPLAIN read as a tree")
	}
}

func TestParsePostgresPlan(t *testing.T) {
	p, ok := parsePostgresPlan(`[{"Plan": {"Node Type": "Nested Loop", "Join Type": "Left", "Total Cost": 10, "Plan Rows": 5,
		"Actual Total Time": 2, "Actual Rows": 5, "Actual Loops": 1, "Plans": [
		{"Node Type": "Index Scan", "Relation Name": "lines", "Index Name": "lines_pkey", "Index Cond": "(id = o.id)",
		 "Actual Total Time": 0.01, "Actual Rows": 2, "Actual Loops": 100}]}}]`)
	if !ok || !p.Analyzed || p.Root.Op != "Nested Loop (Left)" {
		t.Fatalf("%+v", p.Root)
	}
	scan := p.Root.Children[0]
	if scan.Target != "lines using lines_pkey" || scan.Detail != "Index Cond: (id = o.id)" || scan.ActualRows != 200 || scan.Time != 1 {
		t.Fatalf("scan %+v", scan)
	}
}

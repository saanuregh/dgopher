package db

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// Plans as TiDB 8.5 printed them, for queries on it_plan (3,000 rows) and
// it_p2 (its rows, by id, with an index by_g on g).
const (
	// EXPLAIN ANALYZE FORMAT='tidb_json' SELECT p.g, COUNT(*) FROM it_plan p JOIN it_p2 q ON q.g = p.g WHERE q.id > 10
	// GROUP BY p.g ORDER BY 2 DESC LIMIT 3, the times of the scans' cop tasks cut short
	tidbAnalyzedJoin = `[
    {
        "id": "Projection_13",
        "estRows": "3.00",
        "actRows": "3",
        "taskType": "root",
        "executeInfo": "time:3.27ms, loops:2, RU:5.901245, Concurrency:OFF",
        "operatorInfo": "shop.it_plan.g, Column#6",
        "memoryInfo": "760 Bytes",
        "diskInfo": "N/A",
        "subOperators": [
            {
                "id": "TopN_16",
                "estRows": "3.00",
                "actRows": "3",
                "taskType": "root",
                "executeInfo": "time:3.27ms, loops:2",
                "operatorInfo": "Column#6:desc, offset:0, count:3",
                "memoryInfo": "936 Bytes",
                "diskInfo": "0 Bytes",
                "subOperators": [
                    {
                        "id": "HashAgg_21",
                        "estRows": "100.00",
                        "actRows": "100",
                        "taskType": "root",
                        "executeInfo": "time:3.25ms, loops:6, partial_worker:{wall_time:3.227858ms, concurrency:5, task_num:88, tot_wait:11.459946ms, tot_exec:3.92798ms, tot_time:15.907455ms, max:3.182663ms, p95:3.182663ms}, final_worker:{wall_time:3.253426ms, concurrency:5, task_num:25, tot_wait:3.797µs, tot_exec:22.55µs, tot_time:16.027471ms, max:3.209153ms, p95:3.209153ms}",
                        "operatorInfo": "group by:shop.it_plan.g, funcs:count(1)->Column#6, funcs:firstrow(shop.it_plan.g)->shop.it_plan.g",
                        "memoryInfo": "383.3 KB",
                        "diskInfo": "0 Bytes",
                        "subOperators": [
                            {
                                "id": "HashJoin_34",
                                "estRows": "89670.00",
                                "actRows": "89670",
                                "taskType": "root",
                                "executeInfo": "time:3.11ms, loops:89, build_hash_table:{total:556.7µs, fetch:427.3µs, build:129.4µs}, probe:{concurrency:5, total:9.41ms, max:2.95ms, probe:5.53ms, fetch and wait:3.89ms}",
                                "operatorInfo": "inner join, equal:[eq(shop.it_p2.g, shop.it_plan.g)]",
                                "memoryInfo": "87.9 KB",
                                "diskInfo": "0 Bytes",
                                "subOperators": [
                                    {
                                        "id": "TableReader_38(Build)",
                                        "estRows": "3000.00",
                                        "actRows": "3000",
                                        "taskType": "root",
                                        "executeInfo": "time:465µs, loops:4, cop_task: {num: 5, max: 154.1µs, min: 52.4µs, avg: 97.7µs, p95: 154.1µs, copr_cache_hit_ratio: 0.00, build_task_duration: 1.69µs, max_distsql_concurrency: 1}, rpc_info:{Cop:{num_rpc:5, total_time:468.4µs}}",
                                        "operatorInfo": "data:Selection_37",
                                        "memoryInfo": "16.8 KB",
                                        "diskInfo": "N/A",
                                        "subOperators": [
                                            {
                                                "id": "Selection_37",
                                                "estRows": "3000.00",
                                                "actRows": "3000",
                                                "taskType": "cop[tikv]",
                                                "executeInfo": "tikv_task:{proc max:143.8µs, min:45.5µs, avg: 88.1µs, p80:143.8µs, p95:143.8µs, iters:0, tasks:5}",
                                                "operatorInfo": "not(isnull(shop.it_plan.g))",
                                                "memoryInfo": "N/A",
                                                "diskInfo": "N/A",
                                                "subOperators": [
                                                    {
                                                        "id": "TableFullScan_36",
                                                        "estRows": "3000.00",
                                                        "actRows": "3000",
                                                        "taskType": "cop[tikv]",
                                                        "accessObject": "table:p",
                                                        "executeInfo": "tikv_task:{proc max:143.8µs, min:45.5µs, avg: 88.1µs, p80:143.8µs, p95:143.8µs, iters:0, tasks:5}",
                                                        "operatorInfo": "keep order:false",
                                                        "memoryInfo": "N/A",
                                                        "diskInfo": "N/A"
                                                    }
                                                ]
                                            }
                                        ]
                                    },
                                    {
                                        "id": "TableReader_41(Probe)",
                                        "estRows": "2989.00",
                                        "actRows": "2989",
                                        "taskType": "root",
                                        "executeInfo": "time:539.3µs, loops:4, cop_task: {num: 5, max: 166.7µs, min: 73.9µs, avg: 116µs, p95: 166.7µs, copr_cache_hit_ratio: 0.00, build_task_duration: 4.14µs, max_distsql_concurrency: 1}, rpc_info:{Cop:{num_rpc:5, total_time:557.6µs}}",
                                        "operatorInfo": "data:Selection_40",
                                        "memoryInfo": "32.8 KB",
                                        "diskInfo": "N/A",
                                        "subOperators": [
                                            {
                                                "id": "Selection_40",
                                                "estRows": "2989.00",
                                                "actRows": "2989",
                                                "taskType": "cop[tikv]",
                                                "executeInfo": "tikv_task:{proc max:156.2µs, min:62.8µs, avg: 104.7µs, p80:156.2µs, p95:156.2µs, iters:0, tasks:5}",
                                                "operatorInfo": "not(isnull(shop.it_p2.g))",
                                                "memoryInfo": "N/A",
                                                "diskInfo": "N/A",
                                                "subOperators": [
                                                    {
                                                        "id": "TableRangeScan_39",
                                                        "estRows": "2990.00",
                                                        "actRows": "2989",
                                                        "taskType": "cop[tikv]",
                                                        "accessObject": "table:q",
                                                        "executeInfo": "tikv_task:{proc max:156.2µs, min:62.8µs, avg: 104.7µs, p80:156.2µs, p95:156.2µs, iters:0, tasks:5}",
                                                        "operatorInfo": "range:(10,+inf], keep order:false",
                                                        "memoryInfo": "N/A",
                                                        "diskInfo": "N/A"
                                                    }
                                                ]
                                            }
                                        ]
                                    }
                                ]
                            }
                        ]
                    }
                ]
            }
        ]
    }
]`
	// EXPLAIN FORMAT='tidb_json' SELECT * FROM it_p2 WHERE g = 5 AND id < 100 ORDER BY id
	tidbExplainedIndex = `[{"id":"IndexReader_12","estRows":"29.97","taskType":"root","operatorInfo":"index:IndexRangeScan_11",` +
		`"subOperators":[{"id":"IndexRangeScan_11","estRows":"29.97","taskType":"cop[tikv]","accessObject":"table:it_p2, index:by_g(g)",` +
		`"operatorInfo":"range:[5 -inf,5 100), keep order:true"}]}]`
	// EXPLAIN ANALYZE FORMAT='tidb_json' UPDATE it_p2 SET g = g WHERE id = 1
	tidbAnalyzedUpdate = `[{"id":"Update_3","estRows":"N/A","actRows":"0","taskType":"root","executeInfo":"time:55.3µs, loops:1, RU:0.475000",` +
		`"operatorInfo":"N/A","memoryInfo":"0 Bytes","diskInfo":"N/A","subOperators":[{"id":"Point_Get_1","estRows":"1.00","actRows":"0",` +
		`"taskType":"root","accessObject":"table:it_p2","executeInfo":"time:48.9µs, loops:1, Get:{num_rpc:1, total_time:24.3µs}",` +
		`"operatorInfo":"handle:1","memoryInfo":"N/A","diskInfo":"N/A"}]}]`
	// EXPLAIN FORMAT='tidb_json' SELECT * FROM it_plan WHERE g = 5
	tidbExplainedScan = `[{"id":"TableReader_7","estRows":"10.00","taskType":"root","operatorInfo":"data:Selection_6","subOperators":[` +
		`{"id":"Selection_6","estRows":"10.00","taskType":"cop[tikv]","operatorInfo":"eq(shop.it_plan.g, 5)","subOperators":[` +
		`{"id":"TableFullScan_5","estRows":"10000.00","taskType":"cop[tikv]","accessObject":"table:it_plan","operatorInfo":"keep order:false, stats:pseudo"}]}]}]`
)

func TestTiDBParsePlan(t *testing.T) {
	r, _ := SQL(TiDB)
	parse := func(text string) *Plan {
		t.Helper()
		p, ok := r.ParsePlan([]string{"TiDB_JSON"}, [][]any{{text}})
		if !ok || p.Root == nil {
			t.Fatalf("not read as a plan: %.60s", text)
		}
		return p
	}

	p := parse(tidbAnalyzedJoin)
	root := p.Root
	if !p.Analyzed || root.Op != "Projection" || root.Detail != "shop.it_plan.g, Column#6" || root.Rows != 3 || root.ActualRows != 3 ||
		!near(root.Time, 3.27) || root.Loops != 2 || len(root.Children) != 1 {
		t.Fatalf("root %+v", root)
	}
	if root.Prop("taskType") != "root" || root.Prop("memoryInfo") != "760 Bytes" || root.Prop("id") != "Projection_13" {
		t.Fatalf("props %q", root.Props)
	}
	agg := root.Children[0].Children[0]
	if agg.Op != "HashAgg" || !near(agg.Time, 3.25) || agg.Loops != 6 {
		t.Fatalf("aggregate %+v", agg)
	}
	join := agg.Children[0]
	if join.Op != "HashJoin" || len(join.Children) != 2 {
		t.Fatalf("join %+v", join)
	}
	build, probe := join.Children[0], join.Children[1]
	if build.Op != "TableReader (Build)" || probe.Op != "TableReader (Probe)" || !near(build.Time, 0.465) {
		t.Fatalf("build %+v, probe %+v", build, probe)
	}
	// A cop task's step tells no time of its own: its reader's holds it.
	scan := build.Children[0].Children[0]
	if scan.Op != "TableFullScan" || scan.Target != "p" || scan.Detail != "keep order:false" || scan.Rows != 3000 ||
		scan.ActualRows != 3000 || scan.Time != -1 || scan.Loops != -1 {
		t.Fatalf("scan %+v", scan)
	}
	if rng := probe.Children[0].Children[0]; rng.Op != "TableRangeScan" || rng.Target != "q" || rng.Detail != "range:(10,+inf], keep order:false" {
		t.Fatalf("range %+v", rng)
	}

	p = parse(tidbExplainedIndex)
	if p.Analyzed || p.Root.Op != "IndexReader" || p.Root.ActualRows != -1 || p.Root.Time != -1 || len(p.Root.Children) != 1 {
		t.Fatalf("explained %+v", p.Root)
	}
	if ix := p.Root.Children[0]; ix.Op != "IndexRangeScan" || ix.Target != "it_p2 using by_g(g)" || !near(ix.Rows, 29.97) {
		t.Fatalf("index scan %+v", ix)
	}

	p = parse(tidbAnalyzedUpdate)
	if !p.Analyzed || p.Root.Op != "Update" || p.Root.Rows != -1 || p.Root.ActualRows != 0 || !near(p.Root.Time, 0.0553) {
		t.Fatalf("update %+v", p.Root)
	}
	if get := p.Root.Children[0]; get.Op != "Point_Get" || get.Target != "it_p2" || get.Detail != "handle:1" {
		t.Fatalf("point get %+v", get)
	}

	for _, text := range []string{"-> Table scan on t  (cost=1 rows=1)", "", "[]", `{"query_block": {}}`, `[{"estRows": "1"}]`} {
		if _, ok := r.ParsePlan([]string{"TiDB_JSON"}, [][]any{{text}}); ok {
			t.Errorf("%q read as a plan", text)
		}
	}
}

// A step's time is a Go duration, of more than one unit past a minute.
func TestTiDBPlanTimeUnits(t *testing.T) {
	r, _ := SQL(TiDB)
	for info, want := range map[string]float64{
		"time:1m5.2s, loops:2": 65200, "time:1h2m3s, loops:1": 3723000, "time:3.27ms, loops:2": 3.27, "time:850µs, loops:1": 0.85,
	} {
		p, ok := r.ParsePlan(nil, [][]any{{`[{"id": "Projection_1", "estRows": "1", "actRows": "1", "taskType": "root", "executeInfo": "` + info + `"}]`}})
		if !ok || !near(p.Root.Time, want) {
			t.Errorf("%s: %+v %v", info, p.Root, ok)
		}
	}
}

func TestTiDBExplain(t *testing.T) {
	for analyze, prefix := range map[bool]string{false: "EXPLAIN FORMAT='tidb_json' ", true: "EXPLAIN ANALYZE FORMAT='tidb_json' "} {
		run, ok := Explain(TiDB, "SELECT 1", analyze)
		if !ok || run.Statement != prefix+"SELECT 1" || run.Offset != len(prefix) {
			t.Errorf("analyze %v: %+v %v", analyze, run, ok)
		}
	}
}

func TestTiDBAdvice(t *testing.T) {
	r, _ := SQL(TiDB)
	texts := func(text string) string {
		p, _ := r.ParsePlan(nil, [][]any{{text}})
		var out []string
		for _, a := range Advise(TiDB, p) {
			out = append(out, a.Text)
		}
		return strings.Join(out, "\n")
	}
	if got := texts(tidbExplainedScan); !strings.Contains(got, "table scan reads every row of it_plan") || strings.Contains(got, "MySQL") {
		t.Errorf("scan: %q", got)
	}
	if got := texts(tidbExplainedIndex); got != "" {
		t.Errorf("index range scan: %q", got)
	}
	// A step of a join is advised as one alone.
	for _, op := range []string{"TableFullScan (Build)", "Sort (Probe)", "TopN (Build)"} {
		n := newNode()
		n.Op, n.Rows, n.Target = op, adviseRows, "t"
		advised := false
		r.AdvisePlanStep(n, func(*PlanNode, string, ...any) { advised = true })
		if !advised {
			t.Errorf("%s: no advice", op)
		}
	}
}

// TiDB has no read-only session (its transaction_read_only and READ ONLY
// transactions are no-ops, refused unless tidb_enable_noop_functions is
// on): a read-only connection is the app's check alone, and opens.
func TestTiDBReadOnlyIsTheAppsCheck(t *testing.T) {
	r, _ := SQL(TiDB)
	p, err := r.open(context.Background(), Config{Engine: TiDB, User: "u", ReadOnly: true}, endpoint{host: "127.0.0.1", port: 1}, nil, wire{engine: TiDB})
	if err != nil {
		t.Fatal(err)
	}
	p.sql.Close()
	if c, ok := p.connector.(*wireConnector); !ok || len(c.onConnect) != 0 {
		t.Errorf("sends %+v on connect", p.connector)
	}
	if got := r.ReadOnlyBeginSQL(); got != "" {
		t.Errorf("read-only transactions begin with %q", got)
	}
}

func TestTiDBActivityPages(t *testing.T) {
	r, _ := SQL(TiDB)
	from := map[string]string{"Sessions": "CLUSTER_PROCESSLIST", "Locks": "DATA_LOCK_WAITS", "Statistics": "CLUSTER_STATEMENTS_SUMMARY"}
	var names []string
	for _, p := range r.ActivityPages() {
		names = append(names, p.Name)
		if !strings.Contains(p.Query, "information_schema."+from[p.Name]) || strings.Contains(p.Query, "performance_schema") {
			t.Errorf("%s reads %s", p.Name, p.Query)
		}
		if p.Hint == "" {
			t.Errorf("%s has no hint", p.Name)
		}
		if p.Kill != nil && (p.Kill([]any{int64(7)}) != "KILL 7" || p.Stop([]any{int64(7)}) != "KILL QUERY 7" || !strings.Contains(p.Hint, "enable-global-kill")) {
			t.Errorf("%s ends sessions by %q, hint %q", p.Name, p.Kill([]any{int64(7)}), p.Hint)
		}
	}
	if !slices.Equal(names, []string{"Sessions", "Locks", "Statistics"}) {
		t.Errorf("pages %q", names)
	}
}

func TestTiDBCreateFileNamesTiDB(t *testing.T) {
	r, _ := SQL(TiDB)
	if err := r.CreateFile(context.Background(), "x"); err == nil || !strings.HasPrefix(err.Error(), "TiDB ") {
		t.Fatalf("%v", err)
	}
}

// TiDB's TABLE_CONSTRAINTS lists no checks, nor, before 8.x,
// CHECK_CONSTRAINTS their table: they are read from SHOW CREATE TABLE, as
// TiDB 7.5 and 8.5 print it.
func TestTiDBChecksOf(t *testing.T) {
	ddl := "CREATE TABLE `cq` (\n" +
		"  `id` int NOT NULL,\n" +
		"  `note` varchar(20) DEFAULT 'CONSTRAINT `x` CHECK ((1))',\n" +
		"  PRIMARY KEY (`id`) /*T![clustered_index] CLUSTERED */,\n" +
		"  CONSTRAINT `cq_qty` CHECK ((`qty` > 0)),\n" +
		"CONSTRAINT `odd ``name` CHECK ((`qty` < 100 and `qty` != 7)) /*!80016 NOT ENFORCED */\n" +
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;\n"
	got := tidbChecksOf(ddl)
	want := []CheckDesign{{Read: true, Name: "cq_qty", Expression: "(`qty` > 0)"}, {Read: true, Name: "odd `name", Expression: "(`qty` < 100 and `qty` != 7)"}}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
}

// The catalog lists a sequence as an item, not a table, and finds it as a
// sequence.
func TestIntegrationTiDBItems(t *testing.T) {
	integration(t)
	skipUnlessRunning(t, tidbConfig)
	ctx := context.Background()
	d, err := Open(ctx, tidbConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	drop := []string{"DROP SEQUENCE IF EXISTS it_tidb_seq"}
	mustExec(t, d, drop...)
	defer mustExec(t, d, drop...)
	mustExec(t, d, "CREATE SEQUENCE it_tidb_seq START WITH 10 INCREMENT BY 5")
	objs, err := d.Dialect.Objects(ctx, d.SQL, "shop")
	if err != nil || slices.ContainsFunc(objs, func(o Object) bool { return o.Name == "it_tidb_seq" }) {
		t.Errorf("objects %+v %v", objs, err)
	}
	if names, err := d.Dialect.ObjectNames(ctx, d.SQL, "shop"); err != nil || slices.ContainsFunc(names, func(o Object) bool { return o.Name == "it_tidb_seq" }) {
		t.Errorf("names %+v %v", names, err)
	}
	items, err := d.Dialect.Items(ctx, d.SQL, "shop")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(items, func(it Item) bool { return it.Name == "it_tidb_seq" })
	if i < 0 || items[i].Kind != ItemSequence {
		t.Fatalf("items %+v", items)
	}
	if ddl, err := d.Dialect.ItemDDL(ctx, d.SQL, items[i]); err != nil || !strings.Contains(ddl, "CREATE SEQUENCE `it_tidb_seq` start with 10") {
		t.Errorf("sequence ddl %q %v", ddl, err)
	}
	hits, _, err := Search(ctx, d, "it_tidb_seq", false)
	if err != nil || len(hits) != 1 || hits[0].Kind != string(ItemSequence) {
		t.Errorf("search for the sequence %+v %v", hits, err)
	}
}

// Each activity page reads on the server; Locks shows a session waiting
// for a row another holds, and whom it waits for.
func TestIntegrationTiDBActivity(t *testing.T) {
	integration(t)
	d := open(t, fixtureOf(t, TiDB))
	ctx := context.Background()
	r, _ := SQL(TiDB)
	holder, _ := d.Session(ctx)
	defer holder.Close()
	waiter, _ := d.Session(ctx)
	defer waiter.Close()
	for _, st := range []struct {
		s   *Session
		sql string
	}{{holder, "START TRANSACTION"}, {holder, "UPDATE customers SET name = 'held' WHERE id = 1"},
		{waiter, "SET SESSION innodb_lock_wait_timeout = 5"}, {waiter, "START TRANSACTION"}} {
		if _, err := st.s.Exec(ctx, st.sql); err != nil {
			t.Fatalf("%s: %v", st.sql, err)
		}
	}
	waited := make(chan error, 1)
	go func() {
		_, err := waiter.Exec(ctx, "UPDATE customers SET name = 'waits' WHERE id = 1")
		waited <- err
	}()
	// read lists a page's rows as status, object and whom it waits for.
	read := func(page ActivityPage) []string {
		rows, err := d.SQL.QueryContext(ctx, page.Query)
		if err != nil {
			t.Fatalf("%s: %v", page.Name, err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var got []string
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			if i := slices.Index(cols, "status"); i >= 0 {
				got = append(got, Display(vals[i])+" "+Display(vals[slices.Index(cols, "object")])+" "+Display(vals[slices.Index(cols, "waits_for")]))
			}
		}
		return got
	}
	for _, page := range r.ActivityPages() {
		got := read(page)
		// TiDB lists a wait a moment after it begins.
		want := "WAITING shop.customers " + Display(holder.mysqlID)
		for deadline := time.Now().Add(4 * time.Second); page.Name == "Locks" && !slices.Contains(got, want) && time.Now().Before(deadline); {
			time.Sleep(100 * time.Millisecond)
			got = read(page)
		}
		if page.Name == "Locks" && !slices.Contains(got, want) {
			t.Errorf("locks %q, holder %d", got, holder.mysqlID)
		}
	}
	holder.Rollback(ctx)
	if err := <-waited; err != nil {
		t.Fatal(err)
	}
	waiter.Rollback(ctx)
}

// An optimistic transaction that clashes with another fails at COMMIT
// with 9007, shown with the hint that it can be run again.
func TestIntegrationTiDBWriteConflictHint(t *testing.T) {
	integration(t)
	d := open(t, fixtureOf(t, TiDB))
	ctx := context.Background()
	first, _ := d.Session(ctx)
	defer first.Close()
	second, _ := d.Session(ctx)
	defer second.Close()
	for _, s := range []*Session{first, second} {
		for _, q := range []string{"SET SESSION tidb_txn_mode = 'optimistic'", "START TRANSACTION", "UPDATE customers SET name = 'clash' WHERE id = 1"} {
			if _, err := s.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err := second.Commit(ctx)
	if err == nil {
		t.Fatal("the second commit of the same row did not clash")
	}
	if info := DescribeError(err); !strings.HasPrefix(info.Code, "9007") || info.Hint != conflictHint(err) || info.Hint == "" {
		t.Fatalf("%+v", info)
	}
	if second.Tx() != TxNone {
		t.Fatalf("after the clash: %v", second.Tx())
	}
}

// A TiDB backup keeps the schema, without the snapshot TiDB cannot hold,
// and offers no choice of content.
func TestTiDBBackupIsSchemaOnly(t *testing.T) {
	d := &DB{Config: Config{Name: "t", Engine: TiDB, Host: "127.0.0.1", Port: 4000, User: "root", Database: "shop"}}
	r, _ := SQL(TiDB)
	run, err := r.PlanBackup(context.Background(), d, BackupOptions{Format: BackupSQL, Content: BackupAll, Path: "/tmp/x.sql"})
	if err != nil {
		t.Fatal(err)
	}
	// Run against TiDB 8.5 by mysqldump 8.4, it writes the schema and exits 0.
	want := []string{"mysqldump", "--host=127.0.0.1", "--port=4000", "--protocol=TCP", "--user=root", "--ssl-mode=DISABLED",
		"--routines", "--triggers", "--events", "--verbose", "--result-file=/tmp/x.sql", "--no-data", "--databases", "--", "shop"}
	if !slices.Equal(run.Argv, want) {
		t.Fatalf("argv %q", run.Argv)
	}
	if r.BackupChoices().Content {
		t.Error("TiDB offers a choice of content")
	}
}

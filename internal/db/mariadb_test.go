package db

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// Plans as MariaDB 11.4 printed them, for queries on pl_c (2,000 rows)
// and pl_o (6,000 rows, an index on c).
const (
	// ANALYZE FORMAT=JSON SELECT city, SUM(total) FROM pl_c JOIN pl_o ON pl_o.c = pl_c.id GROUP BY city
	mariadbAnalyzedJoin = `{"query_optimization":{"r_total_time_ms":0.091671275},"query_block":{"select_id":1,"cost":10.340906,"r_loops":1,"r_total_time_ms":6.400055906,"filesort":{"sort_key":"pl_c.city","r_loops":1,"r_total_time_ms":0.015868115,"r_used_priority_queue":false,"r_output_rows":10,"r_buffer_size":"6KiB","r_sort_mode":"sort_key,rowid","temporary_table":{"nested_loop":[{"table":{"table_name":"pl_o","access_type":"ALL","possible_keys":["c"],"loops":1,"r_loops":1,"rows":6000,"r_rows":6000,"cost":0.9999672,"r_table_time_ms":0.539485756,"r_other_time_ms":0.312669194,"r_engine_stats":{"pages_accessed":14},"filtered":100,"r_filtered":100,"attached_condition":"pl_o.c is not null"}},{"table":{"table_name":"pl_c","access_type":"eq_ref","possible_keys":["PRIMARY"],"key":"PRIMARY","key_length":"4","used_key_parts":["id"],"ref":["shop.pl_o.c"],"loops":6000,"r_loops":6000,"rows":1,"r_rows":1,"cost":5.3723744,"r_table_time_ms":2.873425167,"r_other_time_ms":2.640960802,"r_engine_stats":{"pages_accessed":12030},"filtered":100,"r_filtered":100}}]}}}}`
	// ANALYZE FORMAT=JSON SELECT * FROM pl_c a JOIN pl_c b ON a.city = b.city WHERE a.id < 50
	mariadbAnalyzedBlockJoin = `{"query_optimization":{"r_total_time_ms":0.093761565},"query_block":{"select_id":1,"cost":9.458527504,"r_loops":1,"r_total_time_ms":5.032634425,"nested_loop":[{"table":{"table_name":"a","access_type":"range","possible_keys":["PRIMARY"],"key":"PRIMARY","key_length":"4","used_key_parts":["id"],"loops":1,"r_loops":1,"rows":49,"r_rows":49,"cost":0.01269347,"r_table_time_ms":0.013345698,"r_other_time_ms":0.007094927,"r_engine_stats":{"pages_accessed":4},"filtered":100,"r_filtered":100,"attached_condition":"a.` + "`id`" + ` < 50"}},{"block-nl-join":{"table":{"table_name":"b","access_type":"ALL","loops":49,"r_loops":1,"rows":2000,"r_rows":2000,"cost":9.445834034,"r_table_time_ms":0.154540766,"r_other_time_ms":0.031529951,"r_engine_stats":{"pages_accessed":7},"filtered":100,"r_filtered":100},"buffer_type":"flat","buffer_size":"14KiB","join_type":"BNL","attached_condition":"b.city = a.city","r_loops":49,"r_filtered":10,"r_unpack_time_ms":1.622502445,"r_other_time_ms":3.198073331,"r_effective_rows":2000}}]}}`
	// EXPLAIN FORMAT=JSON SELECT * FROM pl_o o JOIN pl_c c ON c.id = o.c WHERE c.city = 'city3'
	mariadbExplainedJoin = `{
  "query_block": {
    "select_id": 1,
    "cost": 6.3723416,
    "nested_loop": [
      {
        "table": {
          "table_name": "o",
          "access_type": "ALL",
          "possible_keys": ["c"],
          "loops": 1,
          "rows": 6000,
          "cost": 0.9999672,
          "filtered": 100,
          "attached_condition": "o.c is not null"
        }
      },
      {
        "table": {
          "table_name": "c",
          "access_type": "eq_ref",
          "possible_keys": ["PRIMARY"],
          "key": "PRIMARY",
          "key_length": "4",
          "used_key_parts": ["id"],
          "ref": ["shop.o.c"],
          "loops": 6000,
          "rows": 1,
          "cost": 5.3723744,
          "filtered": 100,
          "attached_condition": "c.city = 'city3'"
        }
      }
    ]
  }
}`
	// ANALYZE FORMAT=JSON SELECT name, (SELECT MAX(total) FROM pl_o WHERE pl_o.c = pl_c.id) FROM pl_c WHERE id < 10
	mariadbAnalyzedSubquery = `{"query_optimization":{"r_total_time_ms":0.096816604},"query_block":{"select_id":1,"cost":0.00416387,"r_loops":1,"r_total_time_ms":0.079551613,"nested_loop":[{"table":{"table_name":"pl_c","access_type":"range","possible_keys":["PRIMARY"],"key":"PRIMARY","key_length":"4","used_key_parts":["id"],"loops":1,"r_loops":1,"rows":9,"r_rows":9,"cost":0.00416387,"r_table_time_ms":0.007728043,"r_other_time_ms":0.029475099,"r_engine_stats":{"pages_accessed":4},"filtered":100,"r_filtered":100,"attached_condition":"pl_c.` + "`id`" + ` < 10"}}],"subqueries":[{"subquery_cache":{"r_loops":9,"r_hit_ratio":0,"query_block":{"select_id":2,"cost":0.00708384,"r_loops":9,"r_total_time_ms":0.032932117,"nested_loop":[{"table":{"table_name":"pl_o","access_type":"ref","possible_keys":["c"],"key":"c","key_length":"5","used_key_parts":["c"],"ref":["shop.pl_c.id"],"loops":1,"r_loops":9,"rows":3,"r_rows":3,"cost":0.00708384,"r_table_time_ms":0.025194024,"r_other_time_ms":0.00456246,"r_engine_stats":{"pages_accessed":72},"filtered":100,"r_filtered":100}}]}}}]}}`
	// ANALYZE FORMAT=JSON SELECT id FROM pl_c UNION SELECT id FROM pl_o
	mariadbAnalyzedUnion = `{"query_optimization":{"r_total_time_ms":0.070265902},"query_block":{"union_result":{"table_name":"<union1,2>","access_type":"ALL","r_loops":1,"r_rows":6000,"query_specifications":[{"query_block":{"select_id":1,"cost":0.3419836,"r_loops":1,"r_total_time_ms":0.298589885,"nested_loop":[{"table":{"table_name":"pl_c","access_type":"ALL","loops":1,"r_loops":1,"rows":2000,"r_rows":2000,"cost":0.3419836,"r_table_time_ms":0.11256439,"r_other_time_ms":0.181111303,"r_engine_stats":{"pages_accessed":7},"filtered":100,"r_filtered":100}}]}},{"query_block":{"select_id":2,"operation":"UNION","cost":0.89017512,"r_loops":1,"r_total_time_ms":0.90440215,"nested_loop":[{"table":{"table_name":"pl_o","access_type":"index","key":"c","key_length":"5","used_key_parts":["c"],"loops":1,"r_loops":1,"rows":6000,"r_rows":6000,"cost":0.89017512,"r_table_time_ms":0.330145224,"r_other_time_ms":0.572930396,"r_engine_stats":{"pages_accessed":8},"filtered":100,"r_filtered":100,"using_index":true}}]}}]}}}`
	// EXPLAIN FORMAT=JSON SELECT 1
	mariadbExplainedNoTable = `{"query_block":{"select_id":1,"table":{"message":"No tables used"}}}`
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestMariaDBParsePlan(t *testing.T) {
	r, _ := SQL(MariaDB)
	parse := func(text string) *Plan {
		t.Helper()
		p, ok := r.ParsePlan([]string{"ANALYZE"}, [][]any{{text}})
		if !ok || p.Root == nil {
			t.Fatalf("not read as a plan: %s", text[:60])
		}
		return p
	}

	p := parse(mariadbAnalyzedJoin)
	if !p.Analyzed || p.Root.Op != "Query block #1" || !near(p.Root.Time, 6.400055906) || len(p.Root.Children) != 1 {
		t.Fatalf("root %+v", p.Root)
	}
	sort := p.Root.Children[0]
	if sort.Op != "Sort" || sort.Detail != "Sort key: pl_c.city" || sort.ActualRows != 10 || len(sort.Children) != 1 {
		t.Fatalf("sort %+v", sort)
	}
	temp := sort.Children[0]
	if temp.Op != "Temporary table" || len(temp.Children) != 2 {
		t.Fatalf("temporary table %+v", temp)
	}
	scan, lookup := temp.Children[0], temp.Children[1]
	if scan.Op != "Table scan" || scan.Target != "pl_o" || scan.Detail != "Condition: pl_o.c is not null" ||
		scan.Rows != 6000 || scan.ActualRows != 6000 || scan.Loops != 1 || !near(scan.Time, 0.539485756+0.312669194) {
		t.Fatalf("scan %+v", scan)
	}
	// rows and r_rows are per loop.
	if lookup.Op != "Unique index lookup" || lookup.Target != "pl_c using PRIMARY" || lookup.Detail != "Ref: shop.pl_o.c" ||
		lookup.Rows != 6000 || lookup.ActualRows != 6000 || lookup.Loops != 6000 {
		t.Fatalf("lookup %+v", lookup)
	}
	// A step's time holds its steps'.
	if !near(temp.Time, scan.Time+lookup.Time) || !near(sort.Time, 0.015868115+temp.Time) {
		t.Fatalf("times: sort %v, temporary table %v", sort.Time, temp.Time)
	}
	if lookup.Prop("key_length") != "4" || lookup.Prop("r_engine_stats") != `{"pages_accessed":12030}` {
		t.Fatalf("props %q", lookup.Props)
	}

	p = parse(mariadbAnalyzedBlockJoin)
	if len(p.Root.Children) != 2 {
		t.Fatalf("root %+v", p.Root)
	}
	if rng := p.Root.Children[0]; rng.Op != "Index range scan" || rng.Target != "a using PRIMARY" {
		t.Fatalf("range %+v", rng)
	}
	join := p.Root.Children[1]
	if join.Op != "Block nested-loop join" || join.Detail != "Condition: b.city = a.city" || len(join.Children) != 1 ||
		join.Children[0].Target != "b" || join.Children[0].Op != "Table scan" {
		t.Fatalf("block join %+v", join)
	}

	p = parse(mariadbExplainedJoin)
	if p.Analyzed || len(p.Root.Children) != 2 || p.Root.Cost != 6.3723416 {
		t.Fatalf("explained %+v", p.Root)
	}
	if c := p.Root.Children[1]; c.Rows != 6000 || c.ActualRows != -1 || c.Time != -1 || c.Detail != "Condition: c.city = 'city3'" {
		t.Fatalf("explained lookup %+v", c)
	}

	p = parse(mariadbAnalyzedSubquery)
	if len(p.Root.Children) != 2 {
		t.Fatalf("root %+v", p.Root)
	}
	cache := p.Root.Children[1]
	if cache.Op != "Subquery cache" || len(cache.Children) != 1 || cache.Children[0].Op != "Query block #2" ||
		cache.Children[0].Children[0].Op != "Index lookup" || cache.Children[0].Children[0].ActualRows != 27 {
		t.Fatalf("subquery %+v", cache)
	}

	p = parse(mariadbAnalyzedUnion)
	union := p.Root.Children[0]
	if !p.Analyzed || union.Op != "Union result" || union.Target != "<union1,2>" || len(union.Children) != 2 ||
		union.Children[1].Op != "Query block #2" || union.Children[1].Children[0].Op != "Index scan" {
		t.Fatalf("union %+v", union)
	}

	p = parse(mariadbExplainedNoTable)
	if len(p.Root.Children) != 1 || p.Root.Children[0].Op != "No tables used" {
		t.Fatalf("no table %+v", p.Root)
	}

	for _, text := range []string{"-> Table scan on t  (cost=1 rows=1)", "", `{"query_block": 1}`} {
		if _, ok := r.ParsePlan([]string{"EXPLAIN"}, [][]any{{text}}); ok {
			t.Errorf("%q read as a plan", text)
		}
	}
}

func TestMariaDBExplain(t *testing.T) {
	for analyze, prefix := range map[bool]string{false: "EXPLAIN FORMAT=JSON ", true: "ANALYZE FORMAT=JSON "} {
		run, ok := Explain(MariaDB, "SELECT 1", analyze)
		if !ok || run.Statement != prefix+"SELECT 1" || run.Offset != len(prefix) {
			t.Errorf("analyze %v: %+v %v", analyze, run, ok)
		}
	}
}

func TestMariaDBAdvice(t *testing.T) {
	r, _ := SQL(MariaDB)
	texts := func(text string) string {
		p, _ := r.ParsePlan(nil, [][]any{{text}})
		var out []string
		for _, a := range Advise(MariaDB, p) {
			out = append(out, a.Text)
		}
		return strings.Join(out, "\n")
	}
	if got := texts(mariadbAnalyzedJoin); !strings.Contains(got, "A table scan reads every row of pl_o") {
		t.Errorf("join: %q", got)
	}
	if got := texts(mariadbAnalyzedBlockJoin); !strings.Contains(got, "block nested-loop join") || !strings.Contains(got, "of b") || strings.Contains(got, "MySQL") {
		t.Errorf("block join: %q", got)
	}
}

// A role of MariaDB is named without a host, which its statements refuse.
func TestMariaDBAccountSQL(t *testing.T) {
	d := DialectOf(MariaDB)
	ada, reader := Account{Name: "ada", Host: "%"}, Account{Name: "reader", Role: true}
	if got := accountName(d, reader); got != "'reader'" {
		t.Errorf("role name %s", got)
	}
	if got, _ := CreateAccountSQL(d, NewAccount{Account: Account{Name: "reader", Host: "%", Role: true}}); got != "CREATE ROLE 'reader'" {
		t.Errorf("create role: %s", got)
	}
	if got, _ := CreateAccountSQL(d, NewAccount{Account: ada, Password: `a'b\c`}); got != `CREATE USER 'ada'@'%' IDENTIFIED BY 'a''b\\c'` {
		t.Errorf("create user: %s", got)
	}
	if got, _ := PasswordSQL(d, ada, "x"); got != `ALTER USER 'ada'@'%' IDENTIFIED BY 'x'` {
		t.Errorf("password: %s", got)
	}
	if got := DropAccountSQL(d, reader); !slices.Equal(got, []string{"DROP ROLE 'reader'"}) {
		t.Errorf("drop role: %q", got)
	}
	if got, _ := GrantSQL(d, []string{"SELECT"}, "shop", "", reader); !slices.Equal(got, []string{"GRANT SELECT ON `shop`.* TO 'reader'"}) {
		t.Errorf("grant: %q", got)
	}
	if got := GrantRoleSQL(d, reader, ada); got != "GRANT 'reader' TO 'ada'@'%'" {
		t.Errorf("grant role: %s", got)
	}
}

// SHOW GRANTS of MariaDB writes an account's password hash, or its
// plugin's string, into the grant of USAGE or of global privileges: it is
// left out of what is shown and of the REVOKE.
func TestMariaDBGrantText(t *testing.T) {
	for grant, want := range map[string]Privilege{
		"GRANT USAGE ON *.* TO `ada`@`%` IDENTIFIED BY PASSWORD '*B69027D44F6E5EDC07F1AEAD1477967B16F28227'": {Text: "GRANT USAGE ON *.* TO `ada`@`%`"},
		"GRANT ALL PRIVILEGES ON *.* TO `root`@`localhost` IDENTIFIED BY PASSWORD '*428B32FED9B6436DF5E206FB98B45ECA6D0FA091' WITH GRANT OPTION": {
			Text: "GRANT ALL PRIVILEGES ON *.* TO `root`@`localhost` WITH GRANT OPTION", Revoke: "REVOKE ALL PRIVILEGES ON *.* FROM `root`@`localhost`"},
		"GRANT USAGE ON *.* TO `ed`@`localhost` IDENTIFIED VIA ed25519 USING 'Gv2JI8Env2fd5YGonZ082tX3UAW+yJYldOlj4R1bfK4' REQUIRE SSL WITH MAX_QUERIES_PER_HOUR 5": {
			Text: "GRANT USAGE ON *.* TO `ed`@`localhost` REQUIRE SSL WITH MAX_QUERIES_PER_HOUR 5"},
		"GRANT `reader` TO `ada`@`%`":          {Text: "GRANT `reader` TO `ada`@`%`", Revoke: "REVOKE `reader` FROM `ada`@`%`"},
		"GRANT SELECT ON `shop`.* TO `reader`": {Text: "GRANT SELECT ON `shop`.* TO `reader`", Revoke: "REVOKE SELECT ON `shop`.* FROM `reader`"},
		// IDENTIFIED inside a quoted name is the name's.
		"GRANT SELECT ON `a IDENTIFIED b`.* TO `ada`@`%`": {Text: "GRANT SELECT ON `a IDENTIFIED b`.* TO `ada`@`%`", Revoke: "REVOKE SELECT ON `a IDENTIFIED b`.* FROM `ada`@`%`"},
		"GRANT USAGE ON *.* TO `a IDENTIFIED b`@`%` IDENTIFIED BY PASSWORD '*B69027D44F6E5EDC07F1AEAD1477967B16F28227'": {
			Text: "GRANT USAGE ON *.* TO `a IDENTIFIED b`@`%`"},
	} {
		if got := mariadbPrivilege(grant); got != want {
			t.Errorf("%s:\n%+v, want\n%+v", grant, got, want)
		}
	}
}

// fakeDumpTools puts the named tools on PATH, each printing version for
// --version and its arguments otherwise, to standard error.
func fakeDumpTools(t *testing.T, version string, tools ...string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tools are shell scripts")
	}
	dir := t.TempDir()
	for _, tool := range tools {
		script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '" + version + "'; exit 0; fi\necho \"$@\" >&2\n"
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// MariaDB backs up with mariadb-dump, else mysqldump: MariaDB's, which
// takes the same options, or MySQL's, which takes MySQL's and must not
// read the column statistics MariaDB lacks.
func TestMariaDBBackupTool(t *testing.T) {
	ctx := context.Background()
	cfg := Config{Engine: MariaDB, Host: "db.example.com", Port: 3306, User: "root", Password: `p"w`, Database: "shop"}
	plan := func(mode TLSMode) ToolRun {
		t.Helper()
		c := cfg
		c.TLS = mode
		if mode == TLSVerifyFull {
			c.CAFile = "/ca.pem"
		}
		run, err := PlanBackup(ctx, &DB{Config: c}, BackupOptions{Format: BackupSQL, Path: "/tmp/x.sql"})
		if err != nil {
			t.Fatal(err)
		}
		if run.MySQLDefaults != "[client]\npassword=\"p\\\"w\"\n" || strings.Contains(run.Shown(), `p"w`) || run.Argv[len(run.Argv)-1] != "shop" {
			t.Fatalf("%+v", run)
		}
		return run
	}
	has := func(run ToolRun, want ...string) {
		t.Helper()
		for _, w := range want {
			if !slices.Contains(run.Argv, w) {
				t.Errorf("%q lacks %s", run.Argv, w)
			}
		}
		if slices.ContainsFunc(run.Argv, func(a string) bool { return strings.HasPrefix(a, "--ssl-mode") }) && run.Argv[0] != "mysqldump" {
			t.Errorf("%q: a MySQL option to a MariaDB tool", run.Argv)
		}
	}

	fakeDumpTools(t, "mariadb-dump from 11.4.13-MariaDB, client 10.19 for debian-linux-gnu (x86_64)", "mariadb-dump", "mysqldump")
	for mode, want := range map[TLSMode][]string{
		TLSDisable:    {"--skip-ssl"},
		TLSPrefer:     {"--skip-ssl-verify-server-cert"},
		TLSRequire:    {"--ssl", "--skip-ssl-verify-server-cert"},
		TLSVerifyFull: {"--ssl", "--ssl-verify-server-cert", "--ssl-ca=/ca.pem"},
	} {
		run := plan(mode)
		if run.Argv[0] != "mariadb-dump" {
			t.Errorf("%s: %q", mode, run.Argv)
		}
		has(run, append(want, "--single-transaction", "--routines", "--events")...)
	}

	fakeDumpTools(t, "mysqldump from 10.4.34-MariaDB, client 10.17 for Linux (x86_64)", "mysqldump")
	if run := plan(TLSRequire); run.Argv[0] != "mysqldump" {
		t.Errorf("MariaDB's mysqldump: %q", run.Argv)
	} else {
		has(run, "--ssl", "--skip-ssl-verify-server-cert")
	}

	fakeDumpTools(t, "mysqldump  Ver 8.4.11 for Linux on x86_64 (MySQL Community Server - GPL)", "mysqldump")
	if run := plan(TLSRequire); run.Argv[0] != "mysqldump" {
		t.Errorf("MySQL's mysqldump: %q", run.Argv)
	} else {
		has(run, "--ssl-mode=REQUIRED", "--column-statistics=0")
	}

	fakeDumpTools(t, "", "pg_dump")
	if run := plan(TLSDisable); run.Argv[0] != "mariadb-dump" {
		t.Errorf("neither installed: %q", run.Argv)
	}

	r, _ := SQL(MariaDB)
	if note := r.BackupNote(false); !strings.Contains(note, "mariadb-dump") {
		t.Errorf("backup note %q", note)
	}
	if note := r.BackupNote(true); strings.Contains(note, "mysqldump") {
		t.Errorf("restore note %q", note)
	}
}

// mariadb-dump without a password to give reads no option file of the
// user's, whose password is for some other server.
func TestMariaDBDumpReadsNoOptionFiles(t *testing.T) {
	fakeDumpTools(t, "", "mariadb-dump")
	var lines []string
	if err := RunTool(context.Background(), ToolRun{Argv: []string{"mariadb-dump", "--databases", "--", "x"}}, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "--no-defaults ") {
		t.Fatalf("%q", lines)
	}
}

func TestMariaDBActivityPages(t *testing.T) {
	r, _ := SQL(MariaDB)
	var names []string
	for _, p := range r.ActivityPages() {
		names = append(names, p.Name)
		if p.Name == "Locks" && (strings.Contains(p.Query, "performance_schema") || !strings.Contains(p.Query, "INNODB_LOCKS")) {
			t.Errorf("locks read from %s", p.Query)
		}
		if p.Name == "Statistics" && !strings.Contains(p.Hint, "performance_schema=ON") {
			t.Errorf("statistics hint %q", p.Hint)
		}
	}
	if !slices.Equal(names, []string{"Sessions", "Locks", "Statistics"}) {
		t.Errorf("pages %q", names)
	}
}

func TestMariaDBCreateFileNamesMariaDB(t *testing.T) {
	r, _ := SQL(MariaDB)
	if err := r.CreateFile(context.Background(), "x"); err == nil || !strings.HasPrefix(err.Error(), "MariaDB ") {
		t.Fatalf("%v", err)
	}
}

// The catalog lists a sequence as an item, not a table; a system-versioned
// table as a table that says so; a JSON column, which MariaDB keeps as
// LONGTEXT checked by JSON_VALID, as json.
func TestIntegrationMariaDBItems(t *testing.T) {
	integration(t)
	skipUnlessRunning(t, mariadbConfig)
	ctx := context.Background()
	d, err := Open(ctx, mariadbConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	drop := []string{"DROP SEQUENCE IF EXISTS it_maria_seq", "DROP TABLE IF EXISTS it_maria_versioned", "DROP TABLE IF EXISTS it_maria_json"}
	mustExec(t, d, drop...)
	defer mustExec(t, d, drop...)
	mustExec(t, d, "CREATE SEQUENCE it_maria_seq START WITH 10 INCREMENT BY 5",
		"CREATE TABLE it_maria_versioned (id int PRIMARY KEY, v int) WITH SYSTEM VERSIONING",
		"CREATE TABLE it_maria_json (id int PRIMARY KEY, doc JSON, other LONGTEXT)")
	objs, err := d.Dialect.Objects(ctx, d.SQL, "shop")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Object{}
	for _, o := range objs {
		byName[o.Name] = o
	}
	if _, ok := byName["it_maria_seq"]; ok {
		t.Errorf("the sequence listed as a table: %+v", byName["it_maria_seq"])
	}
	if v := byName["it_maria_versioned"]; v.Kind != KindTable || v.Engine != "InnoDB, system-versioned" {
		t.Errorf("versioned table %+v", v)
	}
	if names, err := d.Dialect.ObjectNames(ctx, d.SQL, "shop"); err != nil || slices.ContainsFunc(names, func(o Object) bool { return o.Name == "it_maria_seq" }) {
		t.Errorf("names %+v %v", names, err)
	}
	items, err := d.Dialect.Items(ctx, d.SQL, "shop")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(items, func(it Item) bool { return it.Name == "it_maria_seq" })
	if i < 0 || items[i].Kind != ItemSequence {
		t.Fatalf("items %+v", items)
	}
	if ddl, err := d.Dialect.ItemDDL(ctx, d.SQL, items[i]); err != nil || !strings.Contains(ddl, "CREATE SEQUENCE `it_maria_seq` start with 10") {
		t.Errorf("sequence ddl %q %v", ddl, err)
	}
	hits, _, err := Search(ctx, d, "it_maria_seq", false)
	if err != nil || len(hits) != 1 || hits[0].Kind != string(ItemSequence) || hits[0].Schema != "shop" {
		t.Errorf("search for the sequence %+v %v", hits, err)
	}
	if hits, _, err := Search(ctx, d, "next_not_cached_value", false); err != nil || slices.ContainsFunc(hits, func(h SearchHit) bool { return h.Table == "it_maria_seq" }) {
		t.Errorf("the sequence's columns found %+v %v", hits, err)
	}
	cols, err := d.Dialect.Columns(ctx, d.SQL, "shop", "it_maria_json")
	if err != nil || len(cols) != 3 || cols[1].Type != "json" || cols[2].Type != "longtext" {
		t.Fatalf("columns %+v %v", cols, err)
	}
	all, err := d.Dialect.SchemaColumns(ctx, d.SQL, "shop")
	if err != nil || !slices.Equal(all["it_maria_json"], cols) {
		t.Fatalf("schema columns %+v %v", all["it_maria_json"], err)
	}
}

// Roles have no host on MariaDB: they are listed as roles, their members
// with them, and granted, read and dropped by name.
func TestIntegrationMariaDBRoles(t *testing.T) {
	integration(t)
	skipUnlessRunning(t, mariadbConfig)
	ctx := context.Background()
	d, err := Open(ctx, mariadbConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ada, reader := Account{Name: "it_maria_ada", Host: "%"}, Account{Name: "it_maria_reader", Role: true}
	cleanup := func() {
		d.SQL.ExecContext(ctx, "DROP USER IF EXISTS 'it_maria_ada'@'%'")
		d.SQL.ExecContext(ctx, "DROP ROLE IF EXISTS it_maria_reader")
	}
	cleanup()
	defer cleanup()
	role, _ := CreateAccountSQL(d.Dialect, NewAccount{Account: reader})
	user, _ := CreateAccountSQL(d.Dialect, NewAccount{Account: ada, Password: "Correct-Horse-1"})
	grants, err := GrantSQL(d.Dialect, []string{"SELECT"}, "shop", "", reader)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, append([]string{role, user, GrantRoleSQL(d.Dialect, reader, ada), "ALTER USER 'it_maria_ada'@'%' ACCOUNT LOCK"}, grants...)...)
	accounts, err := ListAccounts(ctx, d.Dialect, d.SQL)
	if err != nil {
		t.Fatal(err)
	}
	find := func(name string) Account {
		i := slices.IndexFunc(accounts, func(a Account) bool { return a.Name == name })
		if i < 0 {
			t.Fatalf("%s not in %+v", name, accounts)
		}
		return accounts[i]
	}
	if r := find(reader.Name); !r.Role || r.Host != "" || r.Label() != reader.Name {
		t.Errorf("role %+v", r)
	}
	if a := find(ada.Name); a.Role || !slices.Equal(a.MemberOf, []string{reader.Name}) || !slices.Contains(a.Attributes, "locked") ||
		!slices.Contains(a.Attributes, "logs in with mysql_native_password") {
		t.Errorf("user %+v", a)
	}
	privs, err := AccountPrivileges(ctx, d.Dialect, d.SQL, find(reader.Name))
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(privs, func(p Privilege) bool { return strings.Contains(p.Text, "SELECT ON `shop`.*") })
	if i < 0 {
		t.Fatalf("privileges %+v", privs)
	}
	mustExec(t, d, privs[i].Revoke)
	privs, err = AccountPrivileges(ctx, d.Dialect, d.SQL, ada)
	if err != nil || slices.ContainsFunc(privs, func(p Privilege) bool { return strings.Contains(p.Text, "PASSWORD") }) {
		t.Fatalf("privileges %+v %v", privs, err)
	}
	mustExec(t, d, DropAccountSQL(d.Dialect, find(reader.Name))...)
}

// Every activity page reads, and the locks page shows a session waiting
// for a row another holds.
func TestIntegrationMariaDBActivity(t *testing.T) {
	integration(t)
	d := open(t, fixtureOf(t, MariaDB))
	ctx := context.Background()
	r, _ := SQL(MariaDB)
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
	time.Sleep(300 * time.Millisecond)
	for _, page := range r.ActivityPages() {
		rows, err := d.SQL.QueryContext(ctx, page.Query)
		if err != nil {
			t.Fatalf("%s: %v", page.Name, err)
		}
		cols, _ := rows.Columns()
		var statuses []string
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
				statuses = append(statuses, Display(vals[i])+" "+Display(vals[slices.Index(cols, "object")]))
			}
		}
		rows.Close()
		if page.Name == "Locks" && !slices.Contains(statuses, "WAITING shop.customers") {
			t.Errorf("locks %q", statuses)
		}
	}
	holder.Rollback(ctx)
	if err := <-waited; err != nil {
		t.Fatal(err)
	}
	waiter.Rollback(ctx)
}

func TestMariaDBDropConstraintSQL(t *testing.T) {
	r, _ := SQL(MariaDB)
	d := DialectOf(MariaDB)
	if got := r.DropConstraintSQL(d, ConstraintCheck, "shop", "t", "c"); got != "ALTER TABLE `shop`.`t` DROP CONSTRAINT `c`" {
		t.Errorf("check: %s", got)
	}
	if got := r.DropConstraintSQL(d, ConstraintForeignKey, "shop", "t", "k"); got != "ALTER TABLE `shop`.`t` DROP FOREIGN KEY `k`" {
		t.Errorf("foreign key: %s", got)
	}
}

// MariaDB's information_schema writes a default as SQL, as these columns'
// read: a, nullable without one; b DEFAULT 'x”y'; g DEFAULT (1 + 2); h
// DEFAULT 'NULL', the text.
func TestMariaDBDefault(t *testing.T) {
	text := func(s string) *string { return &s }
	for def, want := range map[*string]string{nil: "", text("NULL"): "", text("'x''y'"): "'x''y'", text("(1 + 2)"): "(1 + 2)",
		text("'NULL'"): "'NULL'", text("current_timestamp()"): "current_timestamp()", text("5"): "5"} {
		if got := mariadbDefault(def, "varchar", ""); got != want {
			t.Errorf("%v: %q, want %q", def, got, want)
		}
	}
}

// A read-only connection sets the session's transactions read-only by a
// variable every supported server has: MariaDB before 11.1 has no
// transaction_read_only, only tx_read_only.
func TestReadOnlySessionVariable(t *testing.T) {
	for e, want := range map[Engine]string{MySQL: "SET transaction_read_only = 1", MariaDB: "SET tx_read_only = 1"} {
		r, _ := SQL(e)
		p, err := r.open(context.Background(), Config{Engine: e, User: "u", ReadOnly: true}, endpoint{host: "127.0.0.1", port: 1}, nil, wire{engine: e})
		if err != nil {
			t.Fatal(err)
		}
		p.sql.Close()
		if c, ok := p.connector.(*wireConnector); !ok || !slices.Equal(c.onConnect, []string{want}) {
			t.Errorf("%s: sends %+v, want %q", e, p.connector, want)
		}
	}
}

// MariaDB names a JSON column's check after the column, in every table
// that has one: a table's design reads only its own.
func TestIntegrationMariaDBChecksOfOwnTable(t *testing.T) {
	integration(t)
	skipUnlessRunning(t, mariadbConfig)
	ctx := context.Background()
	d, err := Open(ctx, mariadbConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	drop := []string{"DROP TABLE IF EXISTS it_maria_doc_a", "DROP TABLE IF EXISTS it_maria_doc_b"}
	mustExec(t, d, drop...)
	defer mustExec(t, d, drop...)
	mustExec(t, d, "CREATE TABLE it_maria_doc_a (id int PRIMARY KEY, doc JSON)",
		"CREATE TABLE it_maria_doc_b (id int PRIMARY KEY, doc JSON, CONSTRAINT positive CHECK (id > 0))")
	for table, want := range map[string]int{"it_maria_doc_a": 1, "it_maria_doc_b": 2} {
		design, err := ReadTableDesign(ctx, d, Object{Schema: "shop", Name: table})
		if err != nil {
			t.Fatal(err)
		}
		if len(design.Checks) != want {
			t.Errorf("%s: checks %+v, want %d", table, design.Checks, want)
		}
	}
}

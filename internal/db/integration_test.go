package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"dgopher/internal/netproxy"
	"dgopher/internal/netproxy/proxytest"
	"dgopher/internal/sshtunnel"
	"dgopher/internal/sshtunnel/sshtest"
	"dgopher/internal/testutil/serverlock"
)

// The integration tests run against the servers of `docker ps --filter
// name=dbgopher` (see docs/development.md): DGOPHER_IT=1 go test ./internal/db/
func integration(t *testing.T) {
	t.Helper()
	if os.Getenv("DGOPHER_IT") == "" {
		t.Skip("set DGOPHER_IT=1 to run against the test servers")
	}
	// The packages' tests run in parallel processes: one at a time uses
	// the servers, as testutil.Integration has the others wait.
	serverlock.Lock(t)
}

type fixture struct {
	cfg    Config
	schema string
	setup  []string
	sleep  string // a statement that runs for seconds
}

func fixtures(t *testing.T) []fixture {
	dir := t.TempDir()
	sqliteFile := filepath.Join(dir, "shop.sqlite")
	os.WriteFile(sqliteFile, nil, 0o600)
	duckFile := filepath.Join(dir, "shop.duckdb")
	common := []string{
		`CREATE TABLE customers (id INTEGER PRIMARY KEY, name VARCHAR(100) NOT NULL, email VARCHAR(200), note TEXT)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER, total DECIMAL(10,2), FOREIGN KEY (customer_id) REFERENCES customers (id))`,
		`CREATE INDEX orders_customer ON orders (customer_id)`,
		`CREATE VIEW big_orders AS SELECT * FROM orders WHERE total > 100`,
		`INSERT INTO customers VALUES (1, 'Ada', 'ada@example.com', NULL), (2, 'Grace', 'grace@example.com', 'line one
line two'), (3, 'Linus', NULL, NULL)`,
		`INSERT INTO orders VALUES (1, 1, 99.50), (2, 1, 250.00), (3, 2, 12.00)`,
	}
	fs := []fixture{
		{
			cfg:    Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dbgopher", Database: "postgres"},
			schema: "it",
			setup:  append([]string{`DROP SCHEMA IF EXISTS it CASCADE`, `CREATE SCHEMA it`, `SET search_path = it`}, common...),
			sleep:  `SELECT pg_sleep(10)`,
		},
		{
			cfg:    Config{Name: "mysql", Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"},
			schema: "shop",
			setup:  append([]string{`DROP VIEW IF EXISTS big_orders`, `DROP TABLE IF EXISTS orders`, `DROP TABLE IF EXISTS customers`}, common...),
			sleep:  `SELECT SLEEP(10)`,
		},
		{
			cfg:    Config{Name: "ch", Engine: ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher", Database: "default"},
			schema: "it",
			setup: []string{
				`DROP DATABASE IF EXISTS it`, `CREATE DATABASE it`,
				`CREATE TABLE it.customers (id UInt32, name String, email Nullable(String), note Nullable(String)) ENGINE = MergeTree ORDER BY id`,
				`CREATE TABLE it.orders (id UInt32, customer_id UInt32, total Decimal(10,2), INDEX by_customer customer_id TYPE minmax GRANULARITY 1) ENGINE = MergeTree ORDER BY id`,
				`CREATE VIEW it.big_orders AS SELECT * FROM it.orders WHERE total > 100`,
				`INSERT INTO it.customers VALUES (1, 'Ada', 'ada@example.com', NULL), (2, 'Grace', 'grace@example.com', 'line one\nline two'), (3, 'Linus', NULL, NULL)`,
				`INSERT INTO it.orders VALUES (1, 1, 99.50), (2, 1, 250.00), (3, 2, 12.00)`,
			},
			sleep: `SELECT sleepEachRow(1) FROM numbers(10) SETTINGS max_block_size = 1, function_sleep_max_microseconds_per_block = 0`,
		},
		{
			cfg:    Config{Name: "sqlite", Engine: SQLite, Database: sqliteFile},
			schema: "main",
			setup:  common,
			sleep:  `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT max(x) FROM c`,
		},
	}
	fs = append(fs, fixture{
		cfg:    Config{Name: "duck", Engine: DuckDB, Database: duckFile},
		schema: "main",
		setup:  common,
		sleep:  `SELECT count(*) FROM range(100000000000) a`,
	})
	return fs
}

func open(t *testing.T, f fixture) *DB {
	t.Helper()
	ctx := context.Background()
	if f.cfg.Engine == DuckDB {
		createDuck(t, f.cfg.Database)
	}
	d, err := Open(ctx, f.cfg, nil)
	if err != nil {
		t.Fatalf("open %s: %v", f.cfg.Name, err)
	}
	t.Cleanup(func() { d.Close() })
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range f.setup {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %s: %v", f.cfg.Name, q, err)
		}
	}
	return d
}

func createDuck(t *testing.T, path string) {
	if _, err := os.Stat(path); err == nil {
		return
	}
	cfg := Config{Name: "x", Engine: DuckDB, Database: ":memory:"}
	d, err := Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("duckdb: %v", err)
	}
	defer d.Close()
	if _, err := d.SQL.Exec("ATTACH '" + path + "' AS created; DETACH created"); err != nil {
		t.Fatalf("create duckdb file: %v", err)
	}
}

func TestIntegrationSchema(t *testing.T) {
	integration(t)
	ctx := context.Background()
	for _, f := range fixtures(t) {
		t.Run(f.cfg.Name, func(t *testing.T) {
			d := open(t, f)
			schemas, err := d.Dialect.Schemas(ctx, d.SQL)
			if err != nil || !contains(schemas, f.schema) {
				t.Fatalf("schemas %v %v", schemas, err)
			}
			objs, err := d.Dialect.Objects(ctx, d.SQL, f.schema)
			if err != nil {
				t.Fatal(err)
			}
			kinds := map[string]ObjectKind{}
			for _, o := range objs {
				kinds[o.Name] = o.Kind
			}
			if kinds["customers"] != KindTable || kinds["orders"] != KindTable || kinds["big_orders"] != KindView {
				t.Fatalf("objects %+v", objs)
			}
			cols, err := d.Dialect.Columns(ctx, d.SQL, f.schema, "customers")
			if err != nil || len(cols) != 4 {
				t.Fatalf("columns %+v %v", cols, err)
			}
			if f.cfg.Engine != ClickHouse {
				if !cols[0].PrimaryKey || cols[1].PrimaryKey || cols[1].Nullable || !cols[2].Nullable {
					t.Errorf("column flags %+v", cols)
				}
			}
			ixs, err := d.Dialect.Indexes(ctx, d.SQL, f.schema, "orders")
			if err != nil || len(ixs) == 0 {
				t.Errorf("indexes %+v %v", ixs, err)
			}
			if f.cfg.Engine != ClickHouse {
				fks, err := d.Dialect.ForeignKeys(ctx, d.SQL, f.schema, "orders")
				if err != nil || len(fks) != 1 || fks[0].RefTable != "customers" || len(fks[0].Columns) != 1 || fks[0].Columns[0] != "customer_id" {
					t.Errorf("foreign keys %+v %v", fks, err)
				}
			}
			for _, name := range []string{"customers", "big_orders"} {
				ddl, err := d.Dialect.DDL(ctx, d.SQL, f.schema, Object{Name: name, Kind: kinds[name]})
				if err != nil || !strings.Contains(strings.ToUpper(ddl), "CREATE") {
					t.Errorf("ddl of %s: %q %v", name, ddl, err)
				}
			}
			if v := d.ServerVersion(ctx); v == "" {
				t.Error("no server version")
			}
		})
	}
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

func TestIntegrationQueryAndTransactions(t *testing.T) {
	integration(t)
	ctx := context.Background()
	for _, f := range fixtures(t) {
		t.Run(f.cfg.Name, func(t *testing.T) {
			d := open(t, f)
			s, err := d.Session(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			table := QualifiedName(d.Dialect, f.schema, "customers")
			c, err := s.Query(ctx, "SELECT id, name, note FROM "+table+" ORDER BY id")
			if err != nil {
				t.Fatal(err)
			}
			rows, err := c.Fetch(2)
			if err != nil || len(rows) != 2 || c.Done() {
				t.Fatalf("first page %v %v done=%v", rows, err, c.Done())
			}
			if Display(rows[0][1]) != "Ada" || rows[0][2] != nil {
				t.Errorf("row values %#v", rows[0])
			}
			if !strings.Contains(Display(rows[1][2]), "\n") || strings.Contains(Cell(rows[1][2], 100), "\n") {
				t.Errorf("multi-line value %q", Display(rows[1][2]))
			}
			rest, err := c.Fetch(10)
			if err != nil || len(rest) != 1 || !c.Done() {
				t.Fatalf("rest %v %v", rest, err)
			}
			if f.cfg.Engine == ClickHouse {
				return
			}
			if err := s.Begin(ctx); err != nil {
				t.Fatal(err)
			}
			if s.Tx() != TxOpen {
				t.Fatalf("tx state %v after BEGIN", s.Tx())
			}
			n, err := s.Exec(ctx, "DELETE FROM "+table+" WHERE id = 3")
			if err != nil || n != 1 {
				t.Fatalf("delete %d %v", n, err)
			}
			if err := s.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if s.Tx() != TxNone {
				t.Fatalf("tx state %v after ROLLBACK", s.Tx())
			}
			var count int64
			if err := d.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 3 {
				t.Fatalf("rows after rollback %d %v", count, err)
			}
		})
	}
}

func TestIntegrationCancel(t *testing.T) {
	integration(t)
	for _, f := range fixtures(t) {
		t.Run(f.cfg.Name, func(t *testing.T) {
			d := open(t, f)
			s, err := d.Session(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			start := time.Now()
			c, err := s.Query(ctx, f.sleep)
			if err == nil {
				_, err = c.Fetch(100)
			}
			if err == nil {
				t.Fatal("a cancelled query succeeded")
			}
			if el := time.Since(start); el > 5*time.Second {
				t.Fatalf("cancel took %v", el)
			}
			// The session goes on working.
			c, err = s.Query(context.Background(), "SELECT 1")
			if err != nil {
				t.Fatalf("after cancel: %v", err)
			}
			if rows, err := c.Fetch(1); err != nil || len(rows) != 1 {
				t.Fatalf("after cancel: %v %v", rows, err)
			}
		})
	}
}

func TestIntegrationReadOnly(t *testing.T) {
	integration(t)
	ctx := context.Background()
	for _, f := range fixtures(t) {
		t.Run(f.cfg.Name, func(t *testing.T) {
			// Closed first: DuckDB shares one instance per file in a process
			// and refuses a second with another access mode.
			open(t, f).Close() // seed
			cfg := f.cfg
			cfg.ReadOnly = true
			d, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			s, err := d.Session(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			table := QualifiedName(d.Dialect, f.schema, "customers")
			if _, err := s.Exec(ctx, "INSERT INTO "+table+" (id, name) VALUES (9, 'x')"); err == nil {
				t.Fatal("the server accepted a write on a read-only connection")
			}
			c, err := s.Query(ctx, "SELECT COUNT(*) FROM "+table)
			if err != nil {
				t.Fatalf("read on read-only: %v", err)
			}
			c.Close()
		})
	}
}

func TestIntegrationEdits(t *testing.T) {
	integration(t)
	ctx := context.Background()
	for _, f := range fixtures(t) {
		if ok, _ := DialectOf(f.cfg.Engine).Editable(); !ok {
			continue
		}
		t.Run(f.cfg.Name, func(t *testing.T) {
			d := open(t, f)
			cols, err := d.Dialect.Columns(ctx, d.SQL, f.schema, "customers")
			if err != nil {
				t.Fatal(err)
			}
			key, err := KeyColumns(cols)
			if err != nil {
				t.Fatal(err)
			}
			// The key as the grid read it.
			s, _ := d.Session(ctx)
			defer s.Close()
			c, err := s.Query(ctx, "SELECT id FROM "+QualifiedName(d.Dialect, f.schema, "customers")+" WHERE id = 1")
			if err != nil {
				t.Fatal(err)
			}
			rows, _ := c.Fetch(1)
			target := &EditTarget{Dialect: d.Dialect, Schema: f.schema, Table: "customers", Columns: cols, Key: key}
			stmts, err := target.Statements([]Change{
				{Kind: ChangeUpdate, Key: []any{rows[0][0]}, Values: map[string]any{"name": Typed("Ada L. 'Countess'"), "note": nil}},
				{Kind: ChangeInsert, Values: map[string]any{"id": Typed("10"), "name": Typed("New")}},
				{Kind: ChangeDelete, Key: []any{int64(3)}},
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, st := range stmts {
				if !strings.HasSuffix(st.Preview(), ";") {
					t.Errorf("preview %q", st.Preview())
				}
				n, err := s.Exec(ctx, st.SQL, st.Args...)
				if err != nil || n != st.Want {
					t.Fatalf("%s %v: %d %v", st.SQL, st.Args, n, err)
				}
			}
			var name string
			if err := d.SQL.QueryRowContext(ctx, "SELECT name FROM "+QualifiedName(d.Dialect, f.schema, "customers")+" WHERE id = 1").Scan(&name); err != nil || name != "Ada L. 'Countess'" {
				t.Fatalf("after update %q %v", name, err)
			}
		})
	}
}

func TestIntegrationRedis(t *testing.T) {
	integration(t)
	ctx := context.Background()
	k, err := OpenRedis(ctx, Config{Name: "r", Engine: Redis, Host: "127.0.0.1", Port: 16379, Database: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	for _, cmd := range [][]string{
		{"FLUSHDB"},
		{"SET", "user:1:name", "Ada", "EX", "3600"},
		{"HSET", "user:1", "name", "Ada", "lang", "en"},
		{"RPUSH", "queue:jobs", "a", "b", "c"},
		{"SADD", "tags", "x", "y"},
		{"ZADD", "scores", "1", "ada", "2", "grace"},
		{"XADD", "events", "*", "kind", "login"},
	} {
		if _, err := k.Do(ctx, cmd); err != nil {
			t.Fatal(cmd, err)
		}
	}

	var keys []string
	var pos ScanPos
	for {
		batch, next, done, err := k.Scan(ctx, pos, "*", "", 100)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, batch...)
		if pos = next; done {
			break
		}
	}
	if len(keys) != 6 {
		t.Fatalf("keys %v", keys)
	}
	info, err := k.Info(ctx, "user:1:name")
	if err != nil || info.Type != "string" || info.TTL <= 0 || info.Length != 3 {
		t.Fatalf("info %+v %v", info, err)
	}
	for key, typ := range map[string]string{"user:1": "hash", "queue:jobs": "list", "tags": "set", "scores": "zset", "events": "stream"} {
		items, _, _, err := k.ReadItems(ctx, key, typ, ItemsPos{}, 100)
		if err != nil || len(items) == 0 {
			t.Errorf("value of %s: %v %v", key, items, err)
		}
	}
	args, _ := SplitCommand(`SET "a b" 'c d'`)
	if len(args) != 3 || args[1] != "a b" || args[2] != "c d" {
		t.Fatalf("split %q", args)
	}
	v, err := k.Do(ctx, []string{"LRANGE", "queue:jobs", "0", "-1"})
	if err != nil || FormatReply(v) != "1) \"a\"\n2) \"b\"\n3) \"c\"" {
		t.Fatalf("reply %q %v", FormatReply(v), err)
	}
	if !k.IsReadOnly("get") || k.IsReadOnly("set") || k.IsReadOnly("flushall") || k.IsReadOnly("client", "kill") || !k.IsReadOnly("client", "list") {
		t.Error("read-only command flags")
	}
	if RiskOf([]string{"flushall"}) == "" || RiskOf([]string{"GET", "x"}) != "" {
		t.Error("risky commands")
	}
	if k.readOnly == nil || !k.readOnly["GET"] || k.readOnly["SET"] {
		t.Error("read-only flags not read from COMMAND")
	}
	if v, err := k.Do(ctx, []string{"GET", "missing"}); err != nil || FormatReply(v) != "(nil)" {
		t.Fatalf("missing key %q %v", FormatReply(v), err)
	}
	items, _, _, err := k.ReadItems(ctx, "events", "stream", ItemsPos{}, 10)
	if err != nil || len(items) != 1 || items[0].Value != "kind=login" {
		t.Fatalf("stream %+v %v", items, err)
	}
	items, _, _, err = k.ReadItems(ctx, "scores", "zset", ItemsPos{}, 10)
	if err != nil || len(items) != 2 || items[1].Value != "grace" || items[1].Score != 2 {
		t.Fatalf("zset %+v %v", items, err)
	}

	// Every type reads page by page to its end.
	for i := range 25 {
		n := strconv.Itoa(i)
		for _, cmd := range [][]string{{"RPUSH", "big:list", n}, {"HSET", "big:hash", "f" + n, n}, {"SADD", "big:set", n},
			{"ZADD", "big:zset", n, "m" + n}, {"XADD", "big:stream", "*", "n", n}} {
			if _, err := k.Do(ctx, cmd); err != nil {
				t.Fatal(cmd, err)
			}
		}
	}
	for key, typ := range map[string]string{"big:list": "list", "big:hash": "hash", "big:set": "set", "big:zset": "zset", "big:stream": "stream"} {
		seen := map[string]bool{}
		var pos ItemsPos
		for pages := 0; ; pages++ {
			if pages > 30 {
				t.Fatalf("%s: no end", typ)
			}
			items, next, done, err := k.ReadItems(ctx, key, typ, pos, 10)
			if err != nil {
				t.Fatal(typ, err)
			}
			for _, it := range items {
				seen[it.Name+"="+it.Value] = true
			}
			if pos = next; done {
				break
			}
		}
		if len(seen) != 25 {
			t.Errorf("%s: %d items read page by page, want 25", typ, len(seen))
		}
	}
	if s, err := k.ReadString(ctx, "user:1:name", 2); err != nil || s != "Ad" {
		t.Fatalf("a string's start: %q %v", s, err)
	}
	if !k.FieldExpiry() {
		t.Fatal("Redis 7.4 keeps hash fields' expiry")
	}
	if _, err := k.Do(ctx, []string{"HEXPIRE", "big:hash", "100", "FIELDS", "1", "f1"}); err != nil {
		t.Fatal(err)
	}
	ttls, err := k.FieldTTLs(ctx, "big:hash", []string{"f1", "f2", "gone"})
	if err != nil || ttls[0] <= 90*time.Second || ttls[1] != -1 || ttls[2] != -1 {
		t.Fatalf("field TTLs %v %v", ttls, err)
	}
}

// A blocking command typed in the console takes its own connection, so
// the key browser's reads on the shared one are not held behind it.
func TestIntegrationRedisBlockingConsole(t *testing.T) {
	integration(t)
	ctx := context.Background()
	k, err := OpenRedis(ctx, Config{Name: "r", Engine: Redis, Host: "127.0.0.1", Port: 16379, Database: "3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Do(ctx, []string{"SET", "k", "v"})
	defer k.Do(ctx, []string{"DEL", "k"})
	done := make(chan error, 1)
	go func() {
		_, err := k.Do(ctx, []string{"BLPOP", "nothing-here", "2"})
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if _, _, _, err := k.Scan(ctx, ScanPos{}, "*", "", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Info(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("reads waited %v behind BLPOP", d)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationMySQLCountsMatchedRows(t *testing.T) {
	integration(t)
	for _, f := range fixtures(t) {
		if f.cfg.Engine != MySQL {
			continue
		}
		d := open(t, f)
		s, _ := d.Session(context.Background())
		defer s.Close()
		n, err := s.Exec(context.Background(), "UPDATE customers SET name = name WHERE id = 1")
		if err != nil || n != 1 {
			t.Fatalf("an unchanged row reported %d affected (%v): edits would fail", n, err)
		}
	}
}

func TestIntegrationTLSPreferFallsBack(t *testing.T) {
	integration(t)
	ctx := context.Background()
	ch, err := Open(ctx, Config{Name: "ch", Engine: ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher", TLS: TLSPrefer}, nil)
	if err != nil {
		t.Fatalf("ClickHouse prefer: %v", err)
	}
	ch.Close()
	k, err := OpenRedis(ctx, Config{Name: "r", Engine: Redis, Host: "127.0.0.1", Port: 16379, TLS: TLSPrefer}, nil)
	if err != nil {
		t.Fatalf("Redis prefer: %v", err)
	}
	k.Close()
}

// A transaction lost while reading rows is reported, never replaced by a
// new connection on which a COMMIT would "succeed".
func TestIntegrationLostTransactionIsReported(t *testing.T) {
	integration(t)
	for _, f := range fixtures(t) {
		if f.cfg.Engine != MySQL && f.cfg.Engine != Postgres {
			continue
		}
		t.Run(f.cfg.Name, func(t *testing.T) {
			d := open(t, f)
			bg := context.Background()
			s, _ := d.Session(bg)
			defer s.Close()
			table := QualifiedName(d.Dialect, f.schema, "customers")
			if f.cfg.Engine == MySQL {
				s.Exec(bg, "SET SESSION cte_max_recursion_depth = 3000000")
			}
			if err := s.Begin(bg); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Exec(bg, "UPDATE "+table+" SET name = 'lost?' WHERE id = 1"); err != nil {
				t.Fatal(err)
			}
			big := "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 2000000) SELECT i FROM n"
			ctx, cancel := context.WithCancel(bg)
			c, err := s.Query(ctx, big)
			if err != nil {
				t.Fatal(err)
			}
			c.Fetch(10)
			cancel() // Esc while the rows come
			c.Fetch(1000)
			err = s.Commit(bg)
			if err == nil {
				t.Fatal("COMMIT succeeded after the transaction was lost or aborted")
			}
			if !errors.Is(err, ErrTxLost) && !errors.Is(err, ErrTxFailed) {
				t.Fatalf("commit error %v", err)
			}
			var name string
			d.SQL.QueryRow("SELECT name FROM " + table + " WHERE id = 1").Scan(&name)
			if name == "lost?" {
				t.Fatal("the update was committed")
			}
		})
	}
}

func postgresFixture(t *testing.T) fixture {
	t.Helper()
	for _, f := range fixtures(t) {
		if f.cfg.Engine == Postgres {
			return f
		}
	}
	t.Fatal("no Postgres fixture")
	return fixture{}
}

// Text holding two statements is refused by the server, so a boundary the
// splitter missed cannot run a hidden statement.
func TestIntegrationPostgresRefusesMultiStatementExec(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, postgresFixture(t))
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []string{`CREATE TABLE it.multi_exec (a integer)`, `INSERT INTO it.multi_exec VALUES (0)`} {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_, err = s.Exec(ctx, "UPDATE it.multi_exec SET a = 1; DROP TABLE it.multi_exec")
	if err == nil {
		t.Fatal("the server ran text holding two statements")
	}
	if info := DescribeError(err); info.Code != "42601" {
		t.Errorf("refusal described as %+v, want SQLSTATE 42601", info)
	}
	var a int
	if err := d.SQL.QueryRowContext(ctx, "SELECT a FROM it.multi_exec").Scan(&a); err != nil {
		t.Fatalf("the table is gone: %v", err)
	}
	if a != 0 {
		t.Fatalf("the UPDATE ran: a = %d", a)
	}
	if s.Tx() != TxNone {
		t.Fatalf("tx state %v after the refused statement", s.Tx())
	}
}

// Statements a user types without arguments still run through the
// extended protocol.
func TestIntegrationPostgresExtendedExec(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, postgresFixture(t))
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	exec := func(q string) int64 {
		t.Helper()
		n, err := s.Exec(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	exec("BEGIN")
	if s.Tx() != TxOpen {
		t.Fatalf("tx state %v after BEGIN", s.Tx())
	}
	exec("CREATE TEMP TABLE extended_exec (id integer PRIMARY KEY, a integer)")
	if n := exec("INSERT INTO extended_exec VALUES (1, 0), (2, 0), (3, 0)"); n != 3 {
		t.Errorf("INSERT affected %d rows, want 3", n)
	}
	if n := exec("UPDATE extended_exec SET a = 1 WHERE id < 3"); n != 2 {
		t.Errorf("UPDATE affected %d rows, want 2", n)
	}
	exec("COMMIT")
	if s.Tx() != TxNone {
		t.Fatalf("tx state %v after COMMIT", s.Tx())
	}
	exec("BEGIN")
	exec("DELETE FROM extended_exec")
	exec("ROLLBACK")
	if s.Tx() != TxNone {
		t.Fatalf("tx state %v after ROLLBACK", s.Tx())
	}
	c, err := s.Query(ctx, "SELECT count(*) FROM extended_exec WHERE a = 1")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Fetch(1)
	if err != nil || len(rows) != 1 || Display(rows[0][0]) != "2" {
		t.Fatalf("rows after COMMIT and ROLLBACK %v %v", rows, err)
	}
	_, err = s.Exec(ctx, "SELEC 1")
	if info := DescribeError(err); info.Code != "42601" || info.Position != 1 {
		t.Errorf("syntax error described as %+v", info)
	}
	exec("DO $$BEGIN END$$")
	exec("VACUUM it.customers")
	exec("SET search_path TO public")
	schema, err := s.CurrentSchema(ctx)
	if err != nil || schema != "public" {
		t.Fatalf("schema after SET search_path %q %v", schema, err)
	}
	if s.Tx() != TxNone {
		t.Fatalf("tx state %v at the end", s.Tx())
	}
}

func TestIntegrationClickHouseServerParams(t *testing.T) {
	integration(t)
	ctx := context.Background()
	var cfg Config
	for _, f := range fixtures(t) {
		if f.cfg.Engine == ClickHouse {
			cfg = f.cfg
		}
	}
	d, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.Query(ctx, "SELECT {id:UInt32} + 1 AS x", ServerParam{Name: "id", Value: "41"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Fetch(10)
	c.Close()
	if err != nil || len(rows) != 1 || fmt.Sprint(rows[0][0]) != "42" {
		t.Fatalf("rows %v %v", rows, err)
	}
	if _, err := s.Exec(ctx, "CREATE TEMPORARY TABLE server_params (s String) ENGINE = Memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, "INSERT INTO server_params SELECT {s:String}", ServerParam{Name: "s", Value: "it's"}); err != nil {
		t.Fatal(err)
	}
	c, err = s.Query(ctx, "SELECT s FROM server_params")
	if err != nil {
		t.Fatal(err)
	}
	rows, err = c.Fetch(10)
	c.Close()
	if err != nil || len(rows) != 1 || fmt.Sprint(rows[0][0]) != "it's" {
		t.Fatalf("rows %v %v", rows, err)
	}
}

// A connection the server terminates with a transaction open reports the
// lost transaction, and the session no longer counts it as open.
func TestIntegrationTerminatedTransactionIsLost(t *testing.T) {
	integration(t)
	for _, f := range fixtures(t) {
		if f.cfg.Engine != Postgres {
			continue
		}
		d := open(t, f)
		bg := context.Background()
		s, err := d.Session(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.Begin(bg); err != nil {
			t.Fatal(err)
		}
		var pid int
		c, err := s.Query(bg, "SELECT pg_backend_pid()")
		if err != nil {
			t.Fatal(err)
		}
		rows, _ := c.Fetch(1)
		c.Close()
		fmt.Sscan(fmt.Sprint(rows[0][0]), &pid)
		if _, err := d.SQL.Exec("SELECT pg_terminate_backend($1)", pid); err != nil {
			t.Fatal(err)
		}
		err = s.Commit(bg)
		if !errors.Is(err, ErrTxLost) {
			t.Fatalf("commit error %v, want ErrTxLost", err)
		}
		if s.Tx() != TxNone {
			t.Fatalf("Tx() = %v after the transaction was lost", s.Tx())
		}
	}
}

// redisCluster is the test cluster of three masters, on the ports of the
// dbgopher-redis-cluster container (docs/development.md).
func redisCluster() Config {
	return Config{Name: "cluster", Engine: Redis, Host: "127.0.0.1", Port: 17000, Password: "dbgopher",
		Redis: RedisConfig{Mode: RedisCluster, Nodes: "127.0.0.1:17001"}}
}

// scanAll reads every key a pattern matches.
func scanAll(t *testing.T, k *KV, match string) []string {
	t.Helper()
	var keys []string
	var pos ScanPos
	for {
		batch, next, done, err := k.Scan(context.Background(), pos, match, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, batch...)
		if pos = next; done {
			return keys
		}
	}
}

// A cluster's keys are spread over its masters: the browser scans them
// all, and the console reaches a key wherever it is.
func TestIntegrationRedisCluster(t *testing.T) {
	integration(t)
	ctx := context.Background()
	testCluster(t, ctx, redisCluster(), nil)

	standalone := Config{Name: "r", Engine: Redis, Host: "127.0.0.1", Port: 16379, Redis: RedisConfig{Mode: RedisCluster}}
	if _, err := OpenRedis(ctx, standalone, nil); err == nil || !strings.Contains(err.Error(), "not a node of a cluster") {
		t.Fatalf("a single server opened as a cluster: %v", err)
	}
	other := redisCluster()
	other.Database = "1"
	if _, err := OpenRedis(ctx, other, nil); err == nil || !strings.Contains(err.Error(), "only database 0") {
		t.Fatalf("a cluster's database 1: %v", err)
	}
}

// Through an SSH server, every node of a cluster is reached, the nodes it
// redirects to included.
func TestIntegrationRedisClusterThroughSSH(t *testing.T) {
	integration(t)
	s := sshtest.Start(t, "secret", nil)
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := sshtunnel.Trust(known, s.Addr, s.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	cfg := redisCluster()
	cfg.Redis.Nodes = "" // one address: the others come from the cluster
	cfg.SSH = SSHConfig{Enabled: true, Host: s.Host, Port: s.Port, User: "tester", Password: "secret"}
	testCluster(t, context.Background(), cfg, []string{known})
}

func testCluster(t *testing.T, ctx context.Context, cfg Config, known []string) {
	t.Helper()
	k, err := OpenRedis(ctx, cfg, known)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	const n = 300
	for i := range n {
		if _, err := k.Do(ctx, []string{"SET", fmt.Sprintf("dgopher-it:%d", i), strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for i := range n {
			k.Do(ctx, []string{"DEL", fmt.Sprintf("dgopher-it:%d", i)})
		}
	}()
	if keys := scanAll(t, k, "dgopher-it:*"); len(keys) != n {
		t.Fatalf("scanned %d keys of %d", len(keys), n)
	}
	if size, err := k.DBSize(ctx); err != nil || size < n {
		t.Fatalf("size %d: %v", size, err)
	}
	for _, i := range []int{0, 1, 2, 299} {
		v, err := k.Do(ctx, []string{"GET", fmt.Sprintf("dgopher-it:%d", i)})
		if err != nil || v != strconv.Itoa(i) {
			t.Fatalf("GET %d: %v %v", i, v, err)
		}
	}
	if info, err := k.Info(ctx, "dgopher-it:7"); err != nil || info.Type != "string" {
		t.Fatalf("info %+v: %v", info, err)
	}
}

// Through Sentinel the connection reaches the master it names, logging in
// to the sentinels with their own password, which no error shows.
func TestIntegrationRedisSentinel(t *testing.T) {
	integration(t)
	ctx := context.Background()
	cfg := Config{Name: "sentinel", Engine: Redis, Host: "127.0.0.1", Port: 26379, Password: "dbgopher",
		Redis: RedisConfig{Mode: RedisSentinel, Master: "mymaster", SentinelPassword: "sentinelpw"}}
	k, err := OpenRedis(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if _, err := k.Do(ctx, []string{"SET", "dgopher-it", "via sentinel"}); err != nil {
		t.Fatal(err)
	}
	defer k.Do(ctx, []string{"DEL", "dgopher-it"})
	info, err := k.Do(ctx, []string{"INFO", "server"})
	if err != nil || !strings.Contains(fmt.Sprint(info), "tcp_port:16380") {
		t.Fatalf("not on the master: %v", err)
	}

	wrong := cfg
	wrong.Redis.SentinelPassword = "wrong-sentinel-secret"
	if _, err := OpenRedis(ctx, wrong, nil); err == nil || strings.Contains(err.Error(), "wrong-sentinel-secret") {
		t.Fatalf("a wrong sentinel password: %v", err)
	}
	unnamed := cfg
	unnamed.Redis.Master = ""
	if _, err := OpenRedis(ctx, unnamed, nil); err == nil || !strings.Contains(err.Error(), "name of the master") {
		t.Fatalf("no master name: %v", err)
	}
}

// An account made, granted, listed, taken back and dropped on each
// server; one made with a password logs in with it, which on PostgreSQL
// proves the verifier hashed here.
func TestIntegrationUsers(t *testing.T) {
	integration(t)
	ctx := context.Background()
	for _, c := range []struct {
		cfg    Config
		schema string
		table  string
	}{
		{Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dbgopher", Database: "postgres"}, "public", "it_users"},
		{Config{Name: "my", Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"}, "shop", "it_users"},
		{Config{Name: "ch", Engine: ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher"}, "default", "it_users"},
	} {
		t.Run(string(c.cfg.Engine), func(t *testing.T) {
			d, err := Open(ctx, c.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			table := QualifiedName(d.Dialect, c.schema, c.table)
			create := "CREATE TABLE " + table + " (id int)"
			if c.cfg.Engine == ClickHouse {
				create += " ENGINE = Memory"
			}
			ada := Account{Name: "dgopher_it_ada"}
			if c.cfg.Engine == MySQL {
				ada.Host = "%"
			}
			cleanup := func() {
				for _, q := range DropAccountSQL(d.Dialect, ada) {
					d.SQL.ExecContext(ctx, q)
				}
				d.SQL.ExecContext(ctx, "DROP TABLE IF EXISTS "+table)
			}
			cleanup()
			defer cleanup()
			stmt, err := CreateAccountSQL(d.Dialect, NewAccount{Account: ada, Password: "Correct-Horse-1"})
			if err != nil {
				t.Fatal(err)
			}
			grants, err := GrantSQL(d.Dialect, []string{"SELECT"}, c.schema, c.table, ada)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range append([]string{create, stmt}, grants...) {
				if _, err := d.SQL.ExecContext(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			accounts, err := ListAccounts(ctx, d.Dialect, d.SQL)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, a := range accounts {
				found = found || a.Name == ada.Name
			}
			if !found {
				t.Fatalf("%s not listed in %+v", ada.Name, accounts)
			}
			privs, err := AccountPrivileges(ctx, d.Dialect, d.SQL, ada)
			if err != nil {
				t.Fatal(err)
			}
			var revoke string
			for _, p := range privs {
				if strings.Contains(p.Text, "SELECT") && strings.Contains(p.Text, c.table) {
					revoke = p.Revoke
				}
			}
			if revoke == "" {
				t.Fatalf("no SELECT on %s in %+v", c.table, privs)
			}
			// The new account logs in with its password, and reads.
			as := c.cfg
			as.User, as.Password = ada.Name, "Correct-Horse-1"
			login, err := Open(ctx, as, nil)
			if err != nil {
				t.Fatalf("logging in as the new account: %v", err)
			}
			var n int
			err = login.SQL.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n)
			login.Close()
			if err != nil {
				t.Fatalf("reading as the new account: %v", err)
			}
			if _, err := d.SQL.ExecContext(ctx, revoke); err != nil {
				t.Fatal(revoke, err)
			}
			// Dropped although it still holds USAGE on the schema, on
			// PostgreSQL.
			for _, q := range DropAccountSQL(d.Dialect, ada) {
				if _, err := d.SQL.ExecContext(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
		})
	}
}

// Each item a schema lists has a definition that makes it again: dropped,
// then made from its definition, it is listed as before.
func TestIntegrationItems(t *testing.T) {
	integration(t)
	ctx := context.Background()
	for _, c := range []struct {
		cfg    Config
		schema string
		setup  []string
		// drop drops an item for its definition to make it again, by its
		// label.
		drop map[string]string
	}{
		{Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dbgopher", Database: "postgres"}, "it_items",
			[]string{"DROP SCHEMA IF EXISTS it_items CASCADE", "CREATE SCHEMA it_items",
				"CREATE FUNCTION it_items.twice(a int) RETURNS int LANGUAGE sql AS 'SELECT a * 2'",
				"CREATE FUNCTION it_items.twice(a text) RETURNS text LANGUAGE sql AS 'SELECT a || a'",
				"CREATE PROCEDURE it_items.noop() LANGUAGE sql AS 'SELECT 1'",
				"CREATE TABLE it_items.events (id int, at date) PARTITION BY RANGE (at)",
				"CREATE TABLE it_items.events_2026 PARTITION OF it_items.events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')",
				"CREATE FUNCTION it_items.stamp() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$",
				"CREATE TABLE it_items.t (id int)",
				"CREATE TRIGGER stamped BEFORE INSERT ON it_items.t FOR EACH ROW EXECUTE FUNCTION it_items.stamp()",
				"CREATE SEQUENCE it_items.counter START 10 INCREMENT 5",
				"CREATE TYPE it_items.mood AS ENUM ('sad', 'it''s ok')",
				"CREATE DOMAIN it_items.positive AS int NOT NULL CHECK (VALUE > 0)",
				"CREATE TYPE it_items.pair AS (a int, b text)",
				"CREATE TYPE it_items.floats AS RANGE (subtype = float8)",
			},
			map[string]string{
				"twice(a integer)": "DROP FUNCTION it_items.twice(int)", "twice(a text)": "DROP FUNCTION it_items.twice(text)",
				"noop()": "DROP PROCEDURE it_items.noop()", "events_2026": "DROP TABLE it_items.events_2026",
				"stamped on t": "DROP TRIGGER stamped ON it_items.t", "counter": "DROP SEQUENCE it_items.counter",
				"mood": "DROP TYPE it_items.mood", "positive": "DROP DOMAIN it_items.positive",
				"pair": "DROP TYPE it_items.pair", "floats": "DROP TYPE it_items.floats",
			}},
		{Config{Name: "my", Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"}, "shop",
			[]string{"DROP TABLE IF EXISTS it_items_t", "CREATE TABLE it_items_t (id int)",
				"DROP FUNCTION IF EXISTS it_twice", "CREATE FUNCTION it_twice(a int) RETURNS int DETERMINISTIC RETURN a * 2",
				"DROP PROCEDURE IF EXISTS it_noop", "CREATE PROCEDURE it_noop() BEGIN SELECT 1; END",
				"CREATE TRIGGER it_stamp BEFORE INSERT ON it_items_t FOR EACH ROW SET NEW.id = NEW.id + 1",
				"DROP EVENT IF EXISTS it_tick", "CREATE EVENT it_tick ON SCHEDULE EVERY 1 DAY DISABLE DO SELECT 1",
			},
			map[string]string{"it_twice()": "DROP FUNCTION it_twice", "it_noop()": "DROP PROCEDURE it_noop",
				"it_stamp on it_items_t": "DROP TRIGGER it_stamp", "it_tick": "DROP EVENT it_tick"}},
		{Config{Name: "lite", Engine: SQLite, Database: filepath.Join(t.TempDir(), "items.sqlite")}, "main",
			[]string{"CREATE TABLE t (id int)", "CREATE TRIGGER stamp AFTER INSERT ON t BEGIN UPDATE t SET id = id + 1 WHERE rowid = NEW.rowid; END"},
			map[string]string{"stamp on t": "DROP TRIGGER stamp"}},
		{Config{Name: "duck", Engine: DuckDB, Database: ":memory:"}, "main",
			[]string{"CREATE MACRO add1(a) AS a + 1", "CREATE MACRO three() AS TABLE SELECT 3 AS x", "CREATE SEQUENCE counter START 5",
				"CREATE TYPE mood AS ENUM ('sad', 'it''s ok')"},
			map[string]string{"add1(a)": "DROP MACRO add1", "three()": "DROP MACRO TABLE three", "counter": "DROP SEQUENCE counter", "mood": "DROP TYPE mood"}},
		{Config{Name: "ch", Engine: ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher"}, "default",
			[]string{"DROP TABLE IF EXISTS it_items_p", "CREATE TABLE it_items_p (id UInt64, v UInt64) ENGINE = MergeTree ORDER BY id",
				"ALTER TABLE it_items_p ADD PROJECTION by_v (SELECT * ORDER BY v)"},
			map[string]string{"by_v on it_items_p": "ALTER TABLE it_items_p DROP PROJECTION by_v"}},
	} {
		t.Run(string(c.cfg.Engine), func(t *testing.T) {
			if c.cfg.Engine == SQLite {
				os.WriteFile(c.cfg.Database, nil, 0o600)
			}
			d, err := Open(ctx, c.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			s, err := d.Session(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for _, q := range c.setup {
				if _, err := s.Exec(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			list := func() map[string]Item {
				items, err := d.Dialect.Items(ctx, d.SQL, c.schema)
				if err != nil {
					t.Fatal(err)
				}
				out := map[string]Item{}
				for _, it := range items {
					out[it.Label()] = it
				}
				return out
			}
			items := list()
			for label, drop := range c.drop {
				it, ok := items[label]
				if !ok {
					t.Fatalf("no %s among %v", label, items)
				}
				def, err := d.Dialect.ItemDDL(ctx, d.SQL, it)
				if err != nil {
					t.Fatalf("%s: %v", label, err)
				}
				for _, q := range []string{drop, def} {
					if _, err := s.Exec(ctx, q); err != nil {
						t.Fatalf("%s: %s: %v", label, q, err)
					}
				}
				if _, ok := list()[label]; !ok {
					t.Fatalf("%s is not made again by\n%s", label, def)
				}
			}
		})
	}
}

// A table and one of its columns rename on every engine.
func TestIntegrationRename(t *testing.T) {
	integration(t)
	ctx := context.Background()
	for _, c := range []struct {
		cfg    Config
		schema string
	}{
		{Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dbgopher", Database: "postgres"}, "public"},
		{Config{Name: "my", Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"}, "shop"},
		{Config{Name: "lite", Engine: SQLite, Database: filepath.Join(t.TempDir(), "r.sqlite")}, "main"},
		{Config{Name: "duck", Engine: DuckDB, Database: ":memory:"}, "main"},
		{Config{Name: "ch", Engine: ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher"}, "default"},
	} {
		t.Run(string(c.cfg.Engine), func(t *testing.T) {
			if c.cfg.Engine == SQLite {
				os.WriteFile(c.cfg.Database, nil, 0o600)
			}
			d, err := Open(ctx, c.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			old, renamed := QualifiedName(d.Dialect, c.schema, "it_rename"), QualifiedName(d.Dialect, c.schema, "it_renamed")
			create := "CREATE TABLE " + old + " (a int)"
			if c.cfg.Engine == ClickHouse {
				create += " ENGINE = Memory"
			}
			for _, q := range []string{"DROP TABLE IF EXISTS " + old, "DROP TABLE IF EXISTS " + renamed, create} {
				if _, err := d.SQL.ExecContext(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			defer d.SQL.ExecContext(ctx, "DROP TABLE IF EXISTS "+renamed)
			table, err := RenameObjectSQL(d.Dialect, Object{Schema: c.schema, Name: "it_rename", Kind: KindTable}, "it_renamed")
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{table, RenameColumnSQL(d.Dialect, c.schema, "it_renamed", "a", "b")} {
				if _, err := d.SQL.ExecContext(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			cols, err := d.Dialect.Columns(ctx, d.SQL, c.schema, "it_renamed")
			if err != nil || len(cols) != 1 || cols[0].Name != "b" {
				t.Fatalf("columns %+v: %v", cols, err)
			}
		})
	}
}

// A search finds tables, views and columns by name in any schema, and
// views by their query when definitions are searched.
func TestIntegrationSearch(t *testing.T) {
	integration(t)
	ctx := context.Background()
	for _, c := range []struct {
		cfg    Config
		schema string
	}{
		{Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dbgopher", Database: "postgres"}, "public"},
		{Config{Name: "my", Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"}, "shop"},
		{Config{Name: "lite", Engine: SQLite, Database: filepath.Join(t.TempDir(), "s.sqlite")}, "main"},
		{Config{Name: "duck", Engine: DuckDB, Database: ":memory:"}, "main"},
		{Config{Name: "ch", Engine: ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher"}, "default"},
	} {
		t.Run(string(c.cfg.Engine), func(t *testing.T) {
			if c.cfg.Engine == SQLite {
				os.WriteFile(c.cfg.Database, nil, 0o600)
			}
			d, err := Open(ctx, c.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			table, view := QualifiedName(d.Dialect, c.schema, "It_Find_Orders"), QualifiedName(d.Dialect, c.schema, "it_find_paid")
			create := "CREATE TABLE " + table + " (it_find_total int, note varchar(20))"
			if c.cfg.Engine == ClickHouse {
				create = "CREATE TABLE " + table + " (it_find_total Int32, note String) ENGINE = Memory"
			}
			drop := []string{"DROP VIEW IF EXISTS " + view, "DROP TABLE IF EXISTS " + table}
			for _, q := range append(drop, create, "CREATE VIEW "+view+" AS SELECT it_find_total FROM "+table+" WHERE note = 'Zebra marker'") {
				if _, err := d.SQL.ExecContext(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			defer func() {
				for _, q := range drop {
					d.SQL.ExecContext(ctx, q)
				}
			}()
			hits, more, err := Search(ctx, d, "IT_FIND", false)
			if err != nil || more {
				t.Fatal(more, err)
			}
			var found []string
			for _, h := range hits {
				if h.Schema == c.schema {
					found = append(found, strings.TrimSpace(h.Kind+" "+h.Table+"."+h.Name+" "+string(h.TableKind)+h.Excerpt))
				}
			}
			slices.Sort(found)
			want := []string{"column It_Find_Orders.it_find_total table", "column it_find_paid.it_find_total view", "table .It_Find_Orders", "view .it_find_paid"}
			if !slices.Equal(found, want) {
				t.Fatalf("by name:\n%q\nwant\n%q", found, want)
			}
			if hits, _, err := Search(ctx, d, "zebra marker", false); err != nil || len(hits) != 0 {
				t.Fatalf("names only: %+v %v", hits, err)
			}
			hits, _, err = Search(ctx, d, "zebra marker", true)
			if err != nil || len(hits) != 1 || hits[0].Name != "it_find_paid" || !strings.Contains(hits[0].Excerpt, "Zebra marker") {
				t.Fatalf("by definition: %+v %v", hits, err)
			}
			if hits[0].Object().Kind != KindView {
				t.Fatalf("object %+v", hits[0].Object())
			}
		})
	}
}

// A table made from a design reads back as designed, and changes as the
// design does: columns renamed, retyped, defaulted, commented and added,
// indexes, keys and checks dropped and added, where the engine can.
func TestIntegrationTableDesign(t *testing.T) {
	integration(t)
	ctx := context.Background()
	for _, c := range []struct {
		cfg                 Config
		schema              string
		integer, big, text  string
		keys, indexes, more bool // foreign keys and checks; indexes; changing the key and checks later
	}{
		{Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dbgopher", Database: "postgres"}, "public", "integer", "bigint", "text", true, true, true},
		{Config{Name: "my", Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"}, "shop", "int", "bigint", "varchar(50)", true, true, true},
		{Config{Name: "duck", Engine: DuckDB, Database: ":memory:"}, "main", "INTEGER", "BIGINT", "VARCHAR", false, true, false},
		{Config{Name: "ch", Engine: ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher"}, "default", "Int32", "Int64", "String", false, false, false},
	} {
		t.Run(string(c.cfg.Engine), func(t *testing.T) {
			d, err := Open(ctx, c.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			parent := QualifiedName(d.Dialect, c.schema, "it_design_customers")
			drop := []string{"DROP TABLE IF EXISTS " + QualifiedName(d.Dialect, c.schema, "it_design"), "DROP TABLE IF EXISTS " + parent}
			for _, q := range drop {
				d.SQL.ExecContext(ctx, q)
			}
			defer func() {
				for _, q := range drop {
					d.SQL.ExecContext(ctx, q)
				}
			}()
			design := TableDesign{Schema: c.schema, Name: "it_design", Comment: "orders", Columns: []ColumnDesign{
				{Name: "id", Type: c.integer, PrimaryKey: true},
				{Name: "customer", Type: c.integer, Nullable: true},
				{Name: "qty", Type: c.integer, Default: "1"},
				{Name: "note", Type: c.text, Nullable: true, Comment: "free text"},
			}}
			if c.indexes {
				design.Indexes = []IndexDesign{{Name: "it_design_customer", Columns: []string{"customer"}}}
			}
			if c.keys {
				if _, err := d.SQL.ExecContext(ctx, "CREATE TABLE "+parent+" (id "+c.integer+" PRIMARY KEY)"); err != nil {
					t.Fatal(err)
				}
				design.ForeignKeys = []ForeignKeyDesign{{Name: "it_design_customer_fkey", Columns: []string{"customer"},
					RefSchema: c.schema, RefTable: "it_design_customers", RefColumns: []string{"id"}, OnDelete: "SET NULL"}}
				design.Checks = []CheckDesign{{Name: "it_design_qty", Expression: "qty > 0"}}
			}
			ch, err := NewTableChange(d.Dialect, design)
			if err != nil {
				t.Fatal(err)
			}
			if ran, err := apply(t, d, ch); err != nil {
				t.Fatalf("%v:\n%s", err, strings.Join(ran, "\n"))
			}
			read := func() TableDesign {
				t.Helper()
				objs, err := d.Dialect.Objects(ctx, d.SQL, c.schema)
				if err != nil {
					t.Fatal(err)
				}
				i := slices.IndexFunc(objs, func(o Object) bool { return o.Name == "it_design" })
				if i < 0 {
					t.Fatal("no table")
				}
				got, err := ReadTableDesign(ctx, d, objs[i])
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			was := read()
			if len(was.Columns) != 4 || !was.Columns[0].PrimaryKey || was.Columns[0].Nullable || !was.Columns[1].Nullable ||
				!strings.Contains(was.Columns[2].Default, "1") || was.Columns[3].Comment != "free text" || was.Comment != "orders" {
				t.Fatalf("read %+v", was)
			}
			if c.indexes && (len(was.Indexes) != 1 || was.Indexes[0].Name != "it_design_customer") {
				t.Fatalf("indexes %+v", was.Indexes)
			}
			if c.keys && (len(was.ForeignKeys) != 1 || was.ForeignKeys[0].OnDelete != "SET NULL" || was.ForeignKeys[0].OnUpdate != "" ||
				len(was.Checks) != 1 || !strings.Contains(was.Checks[0].Expression, "qty")) {
				t.Fatalf("keys %+v, checks %+v", was.ForeignKeys, was.Checks)
			}
			if again, err := AlterTableChange(d.Dialect, was, clone(was)); err != nil || len(again.Steps) != 0 {
				t.Fatalf("an unchanged design changes: %v\n%s", err, again.Text())
			}

			now := clone(was)
			now.Columns[3].Name, now.Columns[3].Default = "remark", Literal(c.cfg.Engine, "none")
			now.Columns[2].Type, now.Columns[2].Comment = c.big, "how many"
			now.Columns[1].Nullable = c.cfg.Engine == MySQL // the others make it NOT NULL
			if c.cfg.Engine == ClickHouse {
				now.Columns[1].Default = "0" // which ClickHouse needs to take NULL away
			}
			now.Columns = append(now.Columns, ColumnDesign{Name: "placed", Type: c.text, Nullable: true})
			now.Comment = "the orders"
			if c.indexes {
				now.Indexes = []IndexDesign{{Name: "it_design_placed", Columns: []string{"placed"}, Unique: true}}
			}
			if c.more {
				now.ForeignKeys = nil
				now.Checks = []CheckDesign{{Name: "it_design_qty_small", Expression: "qty < 1000"}}
				now.Columns[2].PrimaryKey = true
			}
			ch, err = AlterTableChange(d.Dialect, was, now)
			if err != nil {
				t.Fatal(err)
			}
			if ran, err := apply(t, d, ch); err != nil {
				t.Fatalf("%v:\n%s", err, strings.Join(ran, "\n"))
			}
			after := read()
			names := make([]string, len(after.Columns))
			for i, col := range after.Columns {
				names[i] = col.Name
			}
			if !slices.Equal(names, []string{"id", "customer", "qty", "remark", "placed"}) || !strings.EqualFold(after.Columns[2].Type, c.big) ||
				after.Columns[2].Comment != "how many" || !strings.Contains(after.Columns[3].Default, "none") || after.Comment != "the orders" ||
				after.Columns[1].Nullable != (c.cfg.Engine == MySQL) {
				t.Fatalf("after %+v\n%s", after, ch.Text())
			}
			if c.indexes && (len(after.Indexes) != 1 || after.Indexes[0].Name != "it_design_placed" || !after.Indexes[0].Unique) {
				t.Fatalf("indexes %+v", after.Indexes)
			}
			if c.more && (len(after.ForeignKeys) != 0 || len(after.Checks) != 1 || !after.Columns[2].PrimaryKey) {
				t.Fatalf("keys %+v, checks %+v, columns %+v", after.ForeignKeys, after.Checks, after.Columns)
			}
			if again, err := AlterTableChange(d.Dialect, after, clone(after)); err != nil || len(again.Steps) != 0 {
				t.Fatalf("an unchanged design changes: %v\n%s", err, again.Text())
			}
		})
	}
}

// PostgreSQL's enums are read from its catalog, by the type's name as
// format_type writes it.
func TestIntegrationPostgresEnum(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, err := Open(ctx, Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dbgopher", Database: "postgres"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{`DROP TYPE IF EXISTS "It Mood"`, `CREATE TYPE "It Mood" AS ENUM ('ok', 'sad')`} {
		if _, err := d.SQL.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer d.SQL.ExecContext(ctx, `DROP TYPE IF EXISTS "It Mood"`)
	if got, err := EnumValues(ctx, d, `"It Mood"`); err != nil || !slices.Equal(got, []string{"ok", "sad"}) {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := EnumValues(ctx, d, "integer"); err != nil || len(got) != 0 {
		t.Fatalf("integer: %q %v", got, err)
	}
}

// A connection goes through a SOCKS5 or HTTP proxy: to its server, or to
// its SSH host. MySQL, whose server speaks first, goes through HTTP.
func TestIntegrationThroughProxy(t *testing.T) {
	integration(t)
	ctx := context.Background()
	socks := proxytest.SOCKS5(t, "proxy-user", "proxy-pass")
	web := proxytest.HTTP(t, "web-user", "web-pass")
	proxy := func(p netproxy.Config) ProxyConfig {
		return ProxyConfig{Kind: p.Kind, Host: p.Host, Port: p.Port, User: p.User, Password: p.Password}
	}
	pg := Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dbgopher", Database: "postgres", Proxy: proxy(socks)}
	my := Config{Name: "my", Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop", Proxy: proxy(web)}
	for _, cfg := range []Config{pg, my} {
		d, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("%s: %v", cfg.Name, err)
		}
		if d.route == nil {
			t.Fatalf("%s connected without the proxy", cfg.Name)
		}
		if _, err := d.Database(ctx, "template1"); cfg.Engine == Postgres && err != nil {
			t.Fatalf("another database: %v", err)
		}
		d.Close()
	}
	wrong := pg
	wrong.Proxy.Password = "wrong-pass"
	if _, err := Open(ctx, wrong, nil); err == nil || strings.Contains(err.Error(), "wrong-pass") {
		t.Fatalf("a refused proxy login: %v", err)
	}

	s := sshtest.Start(t, "secret", nil)
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := sshtunnel.Trust(known, s.Addr, s.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	viaSSH := pg
	viaSSH.SSH = SSHConfig{Enabled: true, Host: s.Host, Port: s.Port, User: "tester", Password: "secret"}
	d, err := Open(ctx, viaSSH, []string{known})
	if err != nil {
		t.Fatal(err)
	}
	d.Close()

	k, err := OpenRedis(ctx, Config{Name: "r", Engine: Redis, Host: "127.0.0.1", Port: 16379, Proxy: proxy(socks)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if _, err := k.Do(ctx, []string{"PING"}); err != nil {
		t.Fatal(err)
	}
}

// fakeCLI puts a program named name first on PATH, which prints token
// and counts its runs in the file it returns.
func fakeCLI(t *testing.T, name, token string) string {
	t.Helper()
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	script := "#!/bin/sh\necho run >> '" + runs + "'\necho '" + token + "'\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return runs
}

func countRuns(path string) int {
	data, _ := os.ReadFile(path)
	return strings.Count(string(data), "run")
}

// A cloud identity logs in with the token its CLI prints, made again for
// the connections opened once it is old.
func TestIntegrationCloudIdentity(t *testing.T) {
	integration(t)
	ctx := context.Background()
	runs := fakeCLI(t, "aws", "dbgopher")
	my := Config{Name: "my", Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Database: "shop", TLS: TLSRequire, Identity: IdentityAWS}
	d, err := Open(ctx, my, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if n := countRuns(runs); n != 1 {
		t.Fatalf("the CLI ran %d times", n)
	}
	// A token past its reuse is made again for the next connection.
	tokens.Lock()
	for k, tok := range tokens.byCommand {
		tok.made = tok.made.Add(-tokenReuse)
		tokens.byCommand[k] = tok
	}
	tokens.Unlock()
	d.SQL.SetMaxIdleConns(0)
	if err := d.SQL.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRuns(runs); n != 2 {
		t.Fatalf("the CLI ran %d times, not again for a new connection", n)
	}

	// PostgreSQL's test server has no TLS, which Validate asks for: its
	// connections are opened as Open would.
	pg := Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Database: "postgres", Identity: IdentityAWS, IdentityRegion: "eu-west-1"}
	fakeCLI(t, "aws", "dbgopher")
	sqldb, err := openPostgres(pg, endpoint{host: pg.Host, port: pg.Port, serverName: pg.Host}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := sqldb.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := (&Config{Name: "x", Engine: Postgres, Host: "h", Identity: IdentityAWS, TLS: TLSPrefer}).Validate(); err == nil {
		t.Fatal("a token allowed without required TLS")
	}
}

// Each engine's EXPLAIN, in the form ExplainPrefix asks for, reads as a
// plan, and a scan of every row to keep few is advised against.
func TestIntegrationPlans(t *testing.T) {
	integration(t)
	ctx := context.Background()
	for _, c := range []struct {
		cfg    Config
		schema string
		setup  []string
		query  string
		advice string // what the advice holds, "" for none expected
	}{
		{Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dbgopher", Database: "postgres"}, "public",
			[]string{"CREATE TABLE it_plan AS SELECT i AS id, i % 1000 AS g FROM generate_series(1, 20000) i", "ANALYZE it_plan"},
			"SELECT * FROM it_plan WHERE g = 5", "an index on (g)"},
		{Config{Name: "my", Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"}, "shop",
			[]string{"CREATE TABLE it_plan (id int, g int)",
				"INSERT INTO it_plan WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM r WHERE i < 1000) " +
					"SELECT r.i * 3 + k.i, r.i % 100 FROM r CROSS JOIN (SELECT 0 AS i UNION ALL SELECT 1 UNION ALL SELECT 2) k", "ANALYZE TABLE it_plan"},
			"SELECT * FROM it_plan WHERE g = 5", "table scan reads every row of it_plan"},
		{Config{Name: "lite", Engine: SQLite, Database: filepath.Join(t.TempDir(), "p.sqlite")}, "main",
			[]string{"CREATE TABLE it_plan (id INTEGER, g INTEGER)"},
			"SELECT * FROM it_plan WHERE g = 5 ORDER BY id", "SCAN reads every row of it_plan"},
		{Config{Name: "duck", Engine: DuckDB, Database: ":memory:"}, "main",
			[]string{"CREATE TABLE it_plan AS SELECT range AS id, range % 1000 AS g FROM range(20000)"},
			"SELECT g, count(*) FROM it_plan WHERE id > 5 GROUP BY g", ""},
		{Config{Name: "ch", Engine: ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher"}, "default",
			[]string{"CREATE TABLE it_plan (id UInt64, g UInt64) ENGINE = MergeTree ORDER BY id SETTINGS index_granularity = 1024",
				"INSERT INTO it_plan SELECT number, number % 1000 FROM numbers(20000)"},
			"SELECT * FROM it_plan WHERE g = 5", "all 20 granules of"},
	} {
		t.Run(string(c.cfg.Engine), func(t *testing.T) {
			if c.cfg.Engine == SQLite {
				os.WriteFile(c.cfg.Database, nil, 0o600)
			}
			d, err := Open(ctx, c.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			table := QualifiedName(d.Dialect, c.schema, "it_plan")
			d.SQL.ExecContext(ctx, "DROP TABLE IF EXISTS "+table)
			defer d.SQL.ExecContext(ctx, "DROP TABLE IF EXISTS "+table)
			for _, q := range c.setup {
				if _, err := d.SQL.ExecContext(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			for _, analyze := range []bool{false, true} {
				prefix := ExplainPrefix(c.cfg.Engine, analyze)
				if prefix == "" {
					continue
				}
				rows, err := d.SQL.QueryContext(ctx, prefix+c.query)
				if err != nil {
					t.Fatal(prefix, err)
				}
				cols, _ := rows.Columns()
				var all [][]any
				for rows.Next() {
					vals := make([]any, len(cols))
					ptrs := make([]any, len(cols))
					for i := range vals {
						ptrs[i] = &vals[i]
					}
					rows.Scan(ptrs...)
					all = append(all, vals)
				}
				rows.Close()
				p, ok := ParsePlan(c.cfg.Engine, cols, all)
				if !ok || p.Root == nil || p.Analyzed != analyze {
					t.Fatalf("%s: plan %+v %v from %v", prefix, p, ok, all)
				}
				steps := 0
				p.Root.Walk(func(n *PlanNode, _ int) {
					steps++
					if n.Op == "" {
						t.Errorf("%s: a step without its operation: %+v", prefix, n)
					}
				})
				advice := Advise(c.cfg.Engine, p)
				var texts []string
				for _, a := range advice {
					texts = append(texts, a.Text)
				}
				wantAdvice := c.advice != "" && (analyze || c.cfg.Engine != Postgres)
				if got := strings.Join(texts, "\n"); wantAdvice && !strings.Contains(strings.ToLower(got), strings.ToLower(c.advice)) {
					t.Errorf("%s: %d steps, advice %q, want %q", prefix, steps, got, c.advice)
				}
			}
		})
	}
}

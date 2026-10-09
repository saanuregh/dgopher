package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"dgopher/internal/sshtunnel"
	"dgopher/internal/sshtunnel/sshtest"
)

// The integration tests run against the servers of `docker ps --filter
// name=dbgopher` (see docs/development.md): DGOPHER_IT=1 go test ./internal/db/
func integration(t *testing.T) {
	t.Helper()
	if os.Getenv("DGOPHER_IT") == "" {
		t.Skip("set DGOPHER_IT=1 to run against the test servers")
	}
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
		_, items, err := k.Value(ctx, key, typ, 100)
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
	_, items, err := k.Value(ctx, "events", "stream", 10)
	if err != nil || len(items) != 1 || items[0].Value != "kind=login" {
		t.Fatalf("stream %+v %v", items, err)
	}
	_, items, err = k.Value(ctx, "scores", "zset", 10)
	if err != nil || len(items) != 2 || items[1].Value != "grace" || items[1].Score != 2 {
		t.Fatalf("zset %+v %v", items, err)
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

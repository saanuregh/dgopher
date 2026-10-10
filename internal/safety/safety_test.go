package safety

import (
	"slices"
	"strings"
	"testing"

	"dgopher/internal/db"
)

func TestReviewSQL(t *testing.T) {
	dev := &db.Config{Name: "dev", Engine: db.Postgres, Env: db.Development}
	staging := &db.Config{Name: "stg", Engine: db.Postgres, Env: db.Staging}
	prod := &db.Config{Name: "prod", Engine: db.Postgres, Env: db.Production}
	ro := &db.Config{Name: "replica", Engine: db.MySQL, Env: db.Development, ReadOnly: true}
	cases := []struct {
		name     string
		cfg      *db.Config
		sql      []string
		blocked  bool
		confirm  bool
		typeName bool
		writes   bool
	}{
		{"select on prod", prod, []string{"SELECT * FROM t"}, false, false, false, false},
		{"update with where on dev", dev, []string{"UPDATE t SET a = 1 WHERE id = 2"}, false, false, false, true},
		{"delete without where on dev", dev, []string{"DELETE FROM t"}, false, true, false, true},
		{"delete without where on prod", prod, []string{"DELETE FROM t"}, false, true, true, true},
		{"insert on prod", prod, []string{"INSERT INTO t VALUES (1)"}, false, true, false, true},
		{"create on staging", staging, []string{"CREATE TABLE x (a int)"}, false, true, false, true},
		{"insert on staging", staging, []string{"INSERT INTO t VALUES (1)"}, false, false, false, true},
		{"drop on dev", dev, []string{"DROP TABLE t"}, false, true, false, true},
		{"write on read-only", ro, []string{"SELECT 1", "INSERT INTO t VALUES (1)"}, true, false, false, true},
		{"read on read-only", ro, []string{"SELECT 1"}, false, false, false, false},
		{"turning read-only off", ro, []string{"SET SESSION TRANSACTION READ WRITE"}, true, false, false, false},
		{"read write with comments", ro, []string{"SET SESSION TRANSACTION READ/**/WRITE"}, true, false, false, false},
		{"begin read write", ro, []string{"BEGIN READ  WRITE"}, true, false, false, false},
		{"start transaction read write", ro, []string{"START TRANSACTION READ WRITE"}, true, false, false, false},
		{"set_config", &db.Config{Name: "pg", Engine: db.Postgres, ReadOnly: true}, []string{"SELECT set_config('default_transaction_read_only', 'off', false)"}, true, false, false, true},
		{"plain begin on read-only", ro, []string{"BEGIN"}, false, false, false, false},
		{"unknown statement on prod", prod, []string{"FROBNICATE everything"}, false, true, false, true},
		{"mysql executable comment hides a delete", &db.Config{Name: "my", Engine: db.MySQL}, []string{"/*! DELETE FROM t */"}, false, true, false, true},
		{"mysql executable comment into outfile on read-only", &db.Config{Name: "my", Engine: db.MySQL, ReadOnly: true}, []string{"SELECT 1 /*!50100 INTO OUTFILE '/tmp/x' */"}, true, false, false, true},
		{"a string holding /*! stays a string", &db.Config{Name: "my", Engine: db.MySQL, ReadOnly: true}, []string{"SELECT '/*! DELETE FROM t */'"}, false, false, false, false},
		{"a quote in a comment does not hide what follows", &db.Config{Name: "my", Engine: db.MySQL, Env: db.Production}, []string{"/* don't */ /*!DELETE FROM t*/"}, false, true, true, true},
		{"a quote in a line comment", &db.Config{Name: "my", Engine: db.MySQL, Env: db.Production}, []string{"# it's\n/*!DELETE FROM t*/"}, false, true, true, true},
		{"executable comment hides a second statement", &db.Config{Name: "my", Engine: db.MySQL}, []string{"SELECT 1 /*!; DELETE FROM t */"}, false, true, false, true},
		{"mariadb executable comment", &db.Config{Name: "my", Engine: db.MySQL, Env: db.Production}, []string{"/*M!100000 DELETE FROM t */"}, false, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := ReviewSQL(c.cfg, Analyze(c.cfg, c.sql))
			if (v.Blocked != "") != c.blocked || v.Confirm != c.confirm || v.TypeName != c.typeName || v.Writes != c.writes {
				t.Errorf("verdict %+v", v)
			}
			if v.Confirm && len(v.Reasons) == 0 {
				t.Error("a confirmation without a reason")
			}
		})
	}
}

func TestDisablesReadOnlyEscapes(t *testing.T) {
	ro := &db.Config{Name: "pg", Engine: db.Postgres, ReadOnly: true}
	for _, sql := range []string{
		`SET U&"default_transaction_read_onl\0079" = off`,
		`SET u&"default_transaction_read_onl\+000079" = off`,
		`SELECT U&"set_confi\0067"('x', 'y', false)`,
		`SELECT pg_catalog.set_config(U&'default_transaction_read_onl\0079', 'off', false)`,
		`SELECT set_config(E'default_transaction_read_onl\x79', 'off', false)`,
		`SELECT set_config(E'default_transaction_read_onl\171', 'off', false)`,
		`SELECT set_config(E'default_transaction_read_only', 'off', false)`,
		`SET U&"default_transaction_read_onl!0079" UESCAPE '!' = off`,
		`SELECT current_setting(E'transaction_\562ead_only')`,
	} {
		if v := ReviewSQL(ro, Analyze(ro, []string{sql})); v.Blocked == "" {
			t.Errorf("not blocked on a read-only connection: %s", sql)
		}
	}
	if v := ReviewSQL(ro, Analyze(ro, []string{`SELECT U&'caf\00e9'`})); v.Blocked != "" {
		t.Errorf("a plain U& string was blocked: %s", v.Blocked)
	}
}

func TestDisablesReadOnlyClickHouseEscapes(t *testing.T) {
	ro := &db.Config{Name: "ch", Engine: db.ClickHouse, ReadOnly: true}
	for _, sql := range []string{
		"SET `read\\x6fnly` = 0",
		`SET "read\x6fnly" = 0`,
		"SET readonly = 0",
	} {
		if v := ReviewSQL(ro, Analyze(ro, []string{sql})); v.Blocked == "" {
			t.Errorf("not blocked on a read-only connection: %s", sql)
		}
	}
}

// DuckDB's access_mode and an ATTACH's READ_WRITE hold the read-only mode
// too.
func TestDisablesReadOnly(t *testing.T) {
	ro := &db.Config{Name: "duck", Engine: db.DuckDB, ReadOnly: true}
	for _, sql := range []string{
		"SET access_mode = 'READ_WRITE'",
		`SET "access_mode" = 'read_write'`,
		"RESET access_mode",
		"ATTACH 'other.duckdb' AS o (READ_WRITE)",
	} {
		if v := ReviewSQL(ro, Analyze(ro, []string{sql})); v.Blocked == "" {
			t.Errorf("not blocked on a read-only connection: %s", sql)
		}
	}
	if v := ReviewSQL(ro, Analyze(ro, []string{"SELECT 1"})); v.Blocked != "" {
		t.Errorf("a SELECT was blocked: %s", v.Blocked)
	}
}

// DuckDB settings that name a file to write are writes: read-only
// connections refuse them and production asks first.
func TestDuckDBFileSettings(t *testing.T) {
	ro := &db.Config{Name: "duck", Engine: db.DuckDB, ReadOnly: true}
	prod := &db.Config{Name: "duck", Engine: db.DuckDB, Env: db.Production}
	dev := &db.Config{Name: "duck", Engine: db.DuckDB, Env: db.Development}
	for _, sql := range []string{
		"SET log_query_path = '/tmp/queries.log'",
		"SET profiling_output = '/tmp/profile.json'",
		"SET temp_directory = '/tmp/spill'",
	} {
		if v := ReviewSQL(ro, Analyze(ro, []string{sql})); v.Blocked == "" {
			t.Errorf("not blocked on a read-only connection: %s", sql)
		}
		if v := ReviewSQL(prod, Analyze(prod, []string{sql})); !v.Confirm || v.TypeName {
			t.Errorf("on production %s: %+v", sql, v)
		}
		if v := ReviewSQL(dev, Analyze(dev, []string{sql})); v.Confirm || v.Blocked != "" {
			t.Errorf("on development %s: %+v", sql, v)
		}
	}
	if v := ReviewSQL(ro, Analyze(ro, []string{"SET threads = 4"})); v.Blocked != "" {
		t.Errorf("SET threads was blocked: %s", v.Blocked)
	}
}

func TestReviewRedis(t *testing.T) {
	prod := &db.Config{Name: "cache", Engine: db.Redis, Env: db.Production}
	ro := &db.Config{Name: "cache", Engine: db.Redis, Env: db.Development, ReadOnly: true}
	dev := &db.Config{Name: "cache", Engine: db.Redis, Env: db.Development}
	// Without a server, the static list of read-only commands decides.
	kv := &db.KV{}
	if v := ReviewRedis(prod, kv, []string{"GET", "a"}); v.Confirm || v.Blocked != "" {
		t.Errorf("GET on prod %+v", v)
	}
	if v := ReviewRedis(prod, kv, []string{"SET", "a", "b"}); !v.Confirm || v.TypeName {
		t.Errorf("SET on prod %+v", v)
	}
	if v := ReviewRedis(prod, kv, []string{"FLUSHALL"}); !v.Confirm || !v.TypeName {
		t.Errorf("FLUSHALL on prod %+v", v)
	}
	if v := ReviewRedis(dev, kv, []string{"KEYS", "*"}); !v.Confirm {
		t.Errorf("KEYS on dev %+v", v)
	}
	if v := ReviewRedis(ro, kv, []string{"DEL", "a"}); v.Blocked == "" {
		t.Errorf("DEL on read-only %+v", v)
	}
}

func TestRedisStatefulCommandsRefused(t *testing.T) {
	cfg := &db.Config{Name: "cache", Engine: db.Redis}
	for _, cmd := range [][]string{{"SELECT", "3"}, {"auth", "x"}, {"MULTI"}, {"subscribe", "c"}, {"CLIENT", "SETNAME", "x"}} {
		if v := ReviewRedis(cfg, &db.KV{}, cmd); v.Blocked == "" {
			t.Errorf("%v was allowed", cmd)
		}
	}
	ro := &db.Config{Name: "cache", Engine: db.Redis, ReadOnly: true}
	for _, cmd := range [][]string{{"CLIENT", "KILL", "ID", "1"}, {"SLOWLOG", "RESET"}, {"CONFIG", "SET", "x", "y"}, {"MEMORY", "PURGE"}} {
		if v := ReviewRedis(ro, &db.KV{}, cmd); v.Blocked == "" {
			t.Errorf("%v was allowed on a read-only connection", cmd)
		}
	}
	for _, cmd := range [][]string{{"CLIENT", "LIST"}, {"SLOWLOG", "GET"}, {"INFO"}, {"MEMORY", "USAGE", "k"}} {
		if v := ReviewRedis(ro, &db.KV{}, cmd); v.Blocked != "" {
			t.Errorf("%v was refused: %s", cmd, v.Blocked)
		}
	}
}

// Manual commit opens no transaction for a statement that cannot run in
// one.
func TestOutsideTransaction(t *testing.T) {
	for _, c := range []struct {
		e   db.Engine
		sql string
		out bool
	}{
		{db.Postgres, "VACUUM ANALYZE shop.orders", true},
		{db.Postgres, "/* nightly */ vacuum", true},
		{db.Postgres, "CREATE INDEX CONCURRENTLY i ON t (a)", true},
		{db.Postgres, "CREATE INDEX i ON t (a)", false},
		{db.Postgres, "DROP DATABASE scratch", true},
		{db.Postgres, "ALTER SYSTEM SET work_mem = '64MB'", true},
		{db.Postgres, "UPDATE t SET concurrently = 1", false},
		{db.Postgres, "CREATE TABLE concurrently (a int)", false},
		{db.Postgres, "CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)", true},
		{db.Postgres, "DROP INDEX CONCURRENTLY i", true},
		{db.Postgres, "REINDEX TABLE CONCURRENTLY t", true},
		{db.SQLite, "VACUUM", true},
		{db.MySQL, "OPTIMIZE TABLE t", false},
	} {
		if got := OutsideTransaction(c.e, c.sql); got != c.out {
			t.Errorf("%s %q: %v", c.e, c.sql, got)
		}
	}
	cfg := db.Config{Engine: db.Postgres, Env: db.Production}
	if v := ReviewSQL(&cfg, Analyze(&cfg, []string{"VACUUM t"})); v.Writes || !v.Confirm {
		t.Fatalf("VACUUM on production: %+v", v)
	}
}

// Many rows changed a row at a time ask on production for the name, as an
// UPDATE without WHERE does; within the limit, or elsewhere, they do not.
func TestManyRows(t *testing.T) {
	for _, c := range []struct {
		env         db.Environment
		rows, limit int
		typeName    bool
	}{
		{db.Production, 1001, 1000, true},
		{db.Production, 1000, 1000, false},
		{db.Production, 5000, 0, false},
		{db.Staging, 5000, 1000, false},
	} {
		var v Verdict
		v.ManyRows(&db.Config{Env: c.env}, c.rows, c.limit)
		if v.TypeName != c.typeName || v.Confirm != c.typeName {
			t.Errorf("%+v: %+v", c, v)
		}
	}
}

// With a transaction open, a MySQL statement that commits it implicitly
// asks first, saying Roll Back will not undo what ran before it; without
// one, a temporary table, or another engine, nothing changes. When manual
// commit's BEGIN opens the run's transaction (opens), only such a
// statement after a write asks: before one, it commits nothing.
func TestEndsTransactionWarns(t *testing.T) {
	my := &db.Config{Name: "my", Engine: db.MySQL, Env: db.Development}
	pg := &db.Config{Name: "pg", Engine: db.Postgres, Env: db.Development}
	for _, c := range []struct {
		cfg   *db.Config
		sql   string // statements split at ";"
		open  bool
		opens bool
		asks  bool
	}{
		{my, "CREATE INDEX i ON t (a)", true, false, true},
		{my, "/*!50000 ALTER TABLE t ADD COLUMN b INT */", true, false, true},
		{my, "CREATE INDEX i ON t (a)", false, false, false},
		{my, "CREATE TEMPORARY TABLE x (a INT)", true, false, false},
		{my, "INSERT INTO t VALUES (1)", true, false, false},
		{pg, "CREATE INDEX i ON t (a)", true, false, false},
		{my, "INSERT INTO t VALUES (1); CREATE INDEX i ON t (a)", false, true, true},
		{my, "INSERT INTO t VALUES (1); CREATE INDEX i ON t (a); INSERT INTO t VALUES (2); DROP INDEX i ON t", false, true, true},
		{my, "UPDATE t SET a = 1 WHERE id = 2; /*!50000 ALTER TABLE t ADD COLUMN b INT */", false, true, true},
		{my, "CREATE INDEX i ON t (a)", false, true, false},
		{my, "CREATE INDEX i ON t (a); INSERT INTO t VALUES (1)", false, true, false},
		{my, "SELECT * FROM t; CREATE INDEX i ON t (a)", false, true, false},
		{my, "CREATE TABLE u (a INT); CREATE INDEX i ON u (a)", false, true, false},
		{my, "INSERT INTO t VALUES (1); CREATE INDEX i ON t (a); CREATE INDEX j ON t (b)", false, true, true},
		{my, "INSERT INTO t VALUES (1); CREATE TEMPORARY TABLE x (a INT)", false, true, false},
		{my, "INSERT INTO t VALUES (1); COMMIT; CREATE INDEX i ON t (a)", false, true, false},
		{my, "INSERT INTO t VALUES (1); SAVEPOINT s; ROLLBACK WORK TO SAVEPOINT s; CREATE INDEX i ON t (a)", false, true, true},
		{pg, "INSERT INTO t VALUES (1); CREATE INDEX i ON t (a)", false, true, false},
	} {
		stmts := Analyze(c.cfg, strings.Split(c.sql, ";"))
		v := ReviewSQL(c.cfg, stmts)
		if c.opens {
			v.EndsRunTransaction(c.cfg, stmts, true, false)
		} else {
			v.EndsTransaction(c.cfg, stmts, c.open)
		}
		want := "commits the open transaction: Roll Back will not undo what ran before it"
		if c.opens {
			want = "commits the writes before it, which manual commit holds in a transaction: Roll Back will not undo them"
		}
		warned := slices.ContainsFunc(v.Reasons, func(r string) bool { return strings.Contains(r, want) })
		if warned != c.asks || v.Confirm != c.asks {
			t.Errorf("%s %q open %v opens %v: %+v", c.cfg.Engine, c.sql, c.open, c.opens, v)
		}
	}
	// A second statement after the same writes is not said twice; one after
	// other writes, in the transaction opened again, is.
	stmts := Analyze(my, strings.Split("INSERT INTO t VALUES (1); CREATE INDEX i ON t (a); CREATE INDEX j ON t (b); INSERT INTO t VALUES (2); DROP INDEX i ON t", ";"))
	var v Verdict
	v.EndsRunTransaction(my, stmts, true, false)
	if len(v.Reasons) != 2 || !strings.HasPrefix(v.Reasons[0], "CREATE ") || !strings.HasPrefix(v.Reasons[1], "DROP ") {
		t.Fatalf("reasons %q", v.Reasons)
	}
	// Under auto-commit, with no transaction open, a typed BEGIN or START
	// TRANSACTION holds the writes after it, up to the statement that
	// ends it; a write before it, or after the implicit commit, commits
	// on its own.
	for sql, want := range map[string]string{
		"BEGIN; INSERT INTO t VALUES (1); CREATE INDEX i ON t (a); ROLLBACK":                                    "CREATE commits the writes since BEGIN",
		"start transaction; UPDATE t SET a = 1 WHERE id = 2; DROP INDEX i ON t":                                 "DROP commits the writes since START TRANSACTION",
		"BEGIN WORK; INSERT INTO t VALUES (1); BEGIN":                                                           "BEGIN commits the writes since BEGIN",
		"BEGIN; INSERT INTO t VALUES (1); SAVEPOINT s; ROLLBACK TO SAVEPOINT s; CREATE INDEX i ON t (a)":        "CREATE commits the writes since BEGIN",
		"BEGIN; INSERT INTO t VALUES (1); CREATE INDEX i ON t (a); INSERT INTO t VALUES (2); DROP INDEX i ON t": "CREATE commits the writes since BEGIN",
		"INSERT INTO t VALUES (1); CREATE INDEX i ON t (a)":                                                     "",
		"BEGIN; CREATE INDEX i ON t (a)":                                                                        "",
		"BEGIN; INSERT INTO t VALUES (1); COMMIT; CREATE INDEX i ON t (a)":                                      "",
		"BEGIN; INSERT INTO t VALUES (1); ROLLBACK; INSERT INTO t VALUES (2); CREATE INDEX i ON t (a)":          "",
		"BEGIN; INSERT INTO t VALUES (1); CREATE TEMPORARY TABLE x (a INT)":                                     "",
	} {
		stmts := Analyze(my, strings.Split(sql, ";"))
		var v Verdict
		v.EndsRunTransaction(my, stmts, false, false)
		if want == "" && (v.Confirm || len(v.Reasons) > 0) || want != "" && (!v.Confirm || len(v.Reasons) != 1 || !strings.HasPrefix(v.Reasons[0], want)) {
			t.Errorf("%q: %+v", sql, v)
		}
	}
	pgStmts := Analyze(pg, strings.Split("BEGIN; INSERT INTO t VALUES (1); CREATE INDEX i ON t (a)", ";"))
	var pv Verdict
	if pv.EndsRunTransaction(pg, pgStmts, false, false); pv.Confirm || len(pv.Reasons) > 0 {
		t.Errorf("PostgreSQL's DDL commits nothing: %+v", pv)
	}
}

// With MySQL's autocommit off, as the session had it or as the run turns
// it off, every write waits for COMMIT: a statement that commits them
// implicitly asks first.
func TestEndsRunTransactionAutocommitOff(t *testing.T) {
	my := &db.Config{Name: "my", Engine: db.MySQL, Env: db.Development}
	for _, c := range []struct {
		sql  string
		off  bool
		want string
	}{
		{"INSERT INTO t VALUES (1); CREATE INDEX i ON t (a)", true, "CREATE commits the writes before it, which autocommit off holds"},
		{"CREATE INDEX i ON t (a)", true, ""},
		{"SELECT 1; CREATE INDEX i ON t (a)", true, ""},
		{"INSERT INTO t VALUES (1); COMMIT; CREATE INDEX i ON t (a)", true, ""},
		{"SET autocommit = 0; INSERT INTO t VALUES (1); CREATE INDEX i ON t (a)", false, "CREATE commits the writes before it, which autocommit off holds"},
		{"SET autocommit = 0; INSERT INTO t VALUES (1); SET autocommit = 1", false, "SET commits the writes before it, which autocommit off holds"},
		{"SET autocommit = 1; INSERT INTO t VALUES (1); CREATE INDEX i ON t (a)", true, ""},
		{"INSERT INTO t VALUES (1); CREATE INDEX i ON t (a)", false, ""},
	} {
		var v Verdict
		v.EndsRunTransaction(my, Analyze(my, strings.Split(c.sql, ";")), false, c.off)
		if c.want == "" && (v.Confirm || len(v.Reasons) > 0) || c.want != "" && (!v.Confirm || len(v.Reasons) != 1 || !strings.HasPrefix(v.Reasons[0], c.want)) {
			t.Errorf("%q, autocommit off %v: %+v", c.sql, c.off, v)
		}
	}
}

// A statement that ends the transaction, as the classifier reads it: not
// ROLLBACK TO a savepoint, which keeps it.
func TestCommitsOrRollsBack(t *testing.T) {
	cfg := db.Config{Engine: db.SQLite}
	for sql, want := range map[string]bool{
		"COMMIT": true, "commit work": true, "END TRANSACTION": true, "ROLLBACK": true, "ROLLBACK TRANSACTION": true, "ABORT": true,
		"/* undo */ ROLLBACK": true, "COMMIT AND CHAIN": true,
		"ROLLBACK TO SAVEPOINT s": false, "ROLLBACK TRANSACTION TO s": false, "rollback work to s": false, "ROLLBACK TO s": false,
		"BEGIN": false, "SAVEPOINT s": false, "RELEASE SAVEPOINT s": false, "SELECT 1": false,
	} {
		if got := CommitsOrRollsBack(cfg.Engine, Analyze(&cfg, []string{sql})[0]); got != want {
			t.Errorf("%s: %v", sql, got)
		}
	}
}

// A statement that begins a transaction, as the classifier reads it.
func TestBeginsTransaction(t *testing.T) {
	cfg := db.Config{Engine: db.SQLite}
	for sql, want := range map[string]bool{
		"BEGIN": true, "begin immediate": true, "BEGIN TRANSACTION": true, "START TRANSACTION": true,
		"COMMIT": false, "SAVEPOINT s": false, "SELECT 1": false,
	} {
		if got := BeginsTransaction(Analyze(&cfg, []string{sql})[0]); got != want {
			t.Errorf("%s: %v", sql, got)
		}
	}
}

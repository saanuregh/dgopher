package safety

import (
	"context"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/testutil/optionalserver"
)

func TestReviewSQL(t *testing.T) {
	dev := &db.Config{Name: "dev", Engine: db.Postgres, Env: db.Development}
	staging := &db.Config{Name: "stg", Engine: db.Postgres, Env: db.Staging}
	prod := &db.Config{Name: "prod", Engine: db.Postgres, Env: db.Production}
	ro := &db.Config{Name: "replica", Engine: db.MySQL, Env: db.Development, ReadOnly: true}
	pgRO := &db.Config{Name: "pg-replica", Engine: db.Postgres, Env: db.Development, ReadOnly: true}
	tidbRO := &db.Config{Name: "tidb-replica", Engine: db.TiDB, Env: db.Development, ReadOnly: true}
	tidbProd := &db.Config{Name: "tidb-prod", Engine: db.TiDB, Env: db.Production}
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
		{"analyze of a select on read-only", ro, []string{"ANALYZE FORMAT=JSON SELECT 1"}, false, false, false, false},
		{"analyze of an update on read-only", ro, []string{"ANALYZE FORMAT=JSON UPDATE t SET a=1"}, true, false, false, true},
		{"plain analyze of a select on read-only", ro, []string{"ANALYZE SELECT 1"}, false, false, false, false},
		{"analyze table on read-only", ro, []string{"ANALYZE TABLE t"}, true, false, false, true},
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
		{"an ordinary comment in an executable one ends only itself", &db.Config{Name: "my", Engine: db.MySQL, ReadOnly: true}, []string{"SELECT 1 /*! /* x */ , GET_LOCK('k', 0) */"}, true, false, false, true},
		{"an ordinary comment in an executable one hides no delete", &db.Config{Name: "my", Engine: db.MySQL, Env: db.Production}, []string{"/*! /* x */ DELETE FROM t */"}, false, true, true, true},
		{"a comment that closes early hides no second statement", &db.Config{Name: "lite", Engine: db.SQLite, Env: db.Production}, []string{"SELECT 1 /*@/*/ ; DELETE FROM t"}, false, true, true, true},
		{"a comment that closes early hides no outfile", &db.Config{Name: "my", Engine: db.MySQL, ReadOnly: true}, []string{"SELECT 1 /*@/*/ INTO OUTFILE '/tmp/x'"}, true, false, false, true},
		{"a comment that closes early hides no locking read", &db.Config{Name: "my", Engine: db.MySQL, ReadOnly: true}, []string{"SELECT * FROM t /*@/*/ FOR UPDATE"}, true, false, false, true},
		{"a call after a backslash with standard_conforming_strings off", pgRO, []string{`SELECT 'a\' , ' , pg_advisory_lock(1) --'`}, true, false, false, true},
		{"a condition after a backslash under NO_BACKSLASH_ESCAPES", &db.Config{Name: "my", Engine: db.MySQL}, []string{`DELETE FROM t WHERE id = 'a\' OR 1=1 -- '`}, false, true, false, true},
		{"a call after a name under ANSI_QUOTES", ro, []string{`SELECT "a\" , GET_LOCK('x', 0) FROM (SELECT 1 AS "a\") t -- "`}, true, false, false, true},
		{"an executable comment after a backslash under NO_BACKSLASH_ESCAPES", ro, []string{`SELECT 'a\' /*! , SLEEP(1) */ '`}, true, false, false, true},
		{"an outfile in an executable comment after a backslash", ro, []string{`SELECT 'a\' /*! INTO OUTFILE '/tmp/x' */ -- '`}, true, false, false, true},
		{"a second statement after a backslash under NO_BACKSLASH_ESCAPES", ro, []string{`SELECT 'a\' ; SET SESSION TRANSACTION READ WRITE -- '`}, true, false, false, true},
		{"an escaped quote on read-only", ro, []string{`SELECT 'it\'s'`}, false, false, false, false},
		{"escaped quotes and another string on read-only", ro, []string{`SELECT 'O\'Brien' AS n, 'x' AS m`}, false, false, false, false},
		{"escaped quotes in a condition on read-only", ro, []string{`SELECT * FROM t WHERE a='O\'Brien' AND b='x'`}, false, false, false, false},
		{"an E string on read-only", pgRO, []string{`SELECT E'a\'b'`}, false, false, false, false},
		{"tidb set config on read-only", tidbRO, []string{"SET CONFIG tikv `split.qps-threshold` = 1000"}, true, false, false, true},
		{"tidb set binding on read-only", tidbRO, []string{"SET BINDING DISABLED FOR SELECT * FROM t"}, true, false, false, true},
		{"tidb set config on prod", tidbProd, []string{"SET CONFIG pd `log.level` = 'info'"}, false, true, false, true},
		{"tidb set binding on prod", tidbProd, []string{"SET BINDING ENABLED FOR SELECT * FROM t"}, false, true, false, true},
		{"tidb feature comment locks rows on read-only", tidbRO, []string{"SELECT * FROM t /*T![zz] ' */ /*T![clustered_index] FOR UPDATE */ /*T![zz] ' */"}, true, false, false, true},
		{"tidb feature comment writes a file on read-only", tidbRO, []string{"SELECT * FROM t /*T![zz] ' */ /*T![clustered_index] INTO OUTFILE '/tmp/x' */ /*T![zz] ' */"}, true, false, false, true},
		{"a path on read-only", pgRO, []string{`SELECT 'C:\path'`}, false, false, false, false},
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

// A read-only switch hidden in a string by one quoting rule is read under
// the others too.
// A versioned executable comment, or MariaDB's or TiDB's on another
// server, the server may skip: a read-only connection refuses what either
// reading writes.
func TestVersionedExecutableCommentHidesNothing(t *testing.T) {
	for _, e := range []db.Engine{db.MySQL, db.MariaDB, db.TiDB} {
		ro := &db.Config{Name: "ro", Engine: e, ReadOnly: true}
		for _, sql := range []string{
			"/*M!999999 SELECT */ DELETE FROM t WHERE id = 1",
			"/*!99999 SELECT */ DELETE FROM t WHERE id = 1",
			"/*!90000 SELECT */ /*!50000 DELETE */ FROM t WHERE id = 1",
			"SET TRANSACTION READ /*!99999 ' */ WRITE -- '",
			"/*T![clustered_index] DELETE FROM t WHERE id = 1 */",
			"/*T! SELECT */ DELETE FROM t WHERE id = 1",
			"SELECT * FROM t /*T![clustered_index] FOR UPDATE */",
		} {
			if v := ReviewSQL(ro, Analyze(ro, []string{sql})); v.Blocked == "" {
				t.Errorf("%s: not blocked on a read-only connection: %s", e, sql)
			}
		}
		if v := ReviewSQL(ro, Analyze(ro, []string{"SELECT 1 /*!50000 , 2 */ /*M!100000 , 3 */"})); v.Blocked != "" {
			t.Errorf("%s: a SELECT was blocked: %s", e, v.Blocked)
		}
	}
}

func TestDisablesReadOnlyQuoting(t *testing.T) {
	sql := `SELECT 'a\' ; SET SESSION TRANSACTION READ WRITE -- '`
	if !disablesReadOnly(sql, Dialect(db.MySQL)) {
		t.Errorf("not seen: %s", sql)
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
	maria := &db.Config{Name: "maria", Engine: db.MariaDB, Env: db.Development}
	tidb := &db.Config{Name: "tidb", Engine: db.TiDB, Env: db.Development}
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
		{my, "INSERT INTO t VALUES (1); COMMIT; CREATE INDEX i ON t (a)", false, true, false},
		{my, "INSERT INTO t VALUES (1); SAVEPOINT s; ROLLBACK WORK TO SAVEPOINT s; CREATE INDEX i ON t (a)", false, true, true},
		{pg, "INSERT INTO t VALUES (1); CREATE INDEX i ON t (a)", false, true, false},
		// MariaDB's Explain Analyze runs its statement in the transaction.
		{maria, "ANALYZE FORMAT=JSON SELECT 1", true, false, false},
		{maria, "INSERT INTO t VALUES (1); ANALYZE FORMAT=JSON UPDATE t SET a = 2 WHERE id = 1", false, true, false},
		{maria, "INSERT INTO t VALUES (1); ANALYZE TABLE t", false, true, true},
		{maria, "UPDATE t SET a = 1 WHERE id = 2; CREATE SEQUENCE s", false, true, true},
		// TiDB commits on any DDL.
		{tidb, "UPDATE t SET a = 1 WHERE id = 2; CREATE SEQUENCE s", false, true, true},
		{tidb, "UPDATE t SET a = 1 WHERE id = 2; CREATE PLACEMENT POLICY p FOLLOWERS=1", false, true, true},
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
	// Manual commit opens a transaction before MariaDB's ANALYZE of a
	// write, which it does only for a write that does not commit at once.
	if s := Analyze(maria, []string{"ANALYZE UPDATE t SET a = 1"})[0]; CommitsImplicitly(maria.Engine, s) || !WritesInTransaction(maria.Engine, s) {
		t.Errorf("ANALYZE UPDATE: commits %v, writes %v", CommitsImplicitly(maria.Engine, s), WritesInTransaction(maria.Engine, s))
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

// Explain Analyze on a read-only MariaDB connection is a read, and the
// server runs it.
func TestIntegrationMariaDBExplainAnalyzeReadOnly(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "mariadb")
	cfg := db.Config{Name: "mariadb", Engine: db.MariaDB, Host: "127.0.0.1", Port: 13307, User: "root", Password: "dgopher", Database: "shop", ReadOnly: true}
	run, ok := db.Explain(cfg.Engine, "SELECT 1", true)
	if !ok {
		t.Fatal("no Explain Analyze")
	}
	if v := ReviewSQL(&cfg, Analyze(&cfg, []string{run.Statement})); v.Blocked != "" || v.Writes {
		t.Fatalf("%q: %+v", run.Statement, v)
	}
	ctx := context.Background()
	d, err := db.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.Query(ctx, run.Statement)
	if err != nil {
		t.Fatalf("%q on read-only: %v", run.Statement, err)
	}
	c.Close()
}

// TiDB has no read-only session: a read-only connection opens, the app
// refuses its writes, and its Explain Analyze of a read runs.
func TestIntegrationTiDBReadOnlyIsTheAppsCheck(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "tidb")
	cfg := db.Config{Name: "tidb", Engine: db.TiDB, Host: "127.0.0.1", Port: 14000, User: "root", Password: "dgopher", Database: "shop", ReadOnly: true}
	if v := ReviewSQL(&cfg, Analyze(&cfg, []string{"INSERT INTO t VALUES (1)"})); v.Blocked == "" {
		t.Fatal("a write was not refused on a read-only connection")
	}
	run, ok := db.Explain(cfg.Engine, "SELECT 1", true)
	if !ok {
		t.Fatal("no Explain Analyze")
	}
	if v := ReviewSQL(&cfg, Analyze(&cfg, []string{run.Statement})); v.Blocked != "" || v.Writes {
		t.Fatalf("%q: %+v", run.Statement, v)
	}
	ctx := context.Background()
	d, err := db.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("a read-only connection did not open: %v", err)
	}
	defer d.Close()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.Query(ctx, run.Statement)
	if err != nil {
		t.Fatalf("%q on read-only: %v", run.Statement, err)
	}
	c.Close()
}

// CockroachDB refuses the writes of a read-only session, after a COMMIT
// too, but runs some statements that change the cluster all the same,
// which the app refuses; its Explain Analyze of a read runs.
func TestIntegrationCockroachDBReadOnly(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "cockroachdb")
	cfg := db.Config{Name: "crdb", Engine: db.CockroachDB, Host: "127.0.0.1", Port: 26257, User: "dgopher", Password: "dgopher", Database: "shop", ReadOnly: true}
	for _, sql := range []string{
		"INSERT INTO t VALUES (1)", "EXPLAIN ANALYZE (DEBUG) SELECT 1", "SELECT crdb_internal.reset_sql_stats()",
		"CREATE STATISTICS s FROM t", "SET CLUSTER SETTING sql.defaults.distsql = off", "CANCEL JOB 1", "PAUSE JOB 1",
		"SELECT * FROM [DELETE FROM t WHERE a = 1 RETURNING a]", "SET search_path = crdb_internal, public",
		`EXPLAIN ("analyze") DELETE FROM t WHERE a = 1`, `EXPLAIN ANALYZE ("debug") SELECT 1`, "SELECT crdb_internal.inject_hint('SELECT 1', 'x')",
	} {
		if v := ReviewSQL(&cfg, Analyze(&cfg, []string{sql})); v.Blocked == "" {
			t.Errorf("%q not refused on a read-only connection", sql)
		}
	}
	run, ok := db.Explain(cfg.Engine, "SELECT 1", true)
	if !ok {
		t.Fatal("no Explain Analyze")
	}
	if v := ReviewSQL(&cfg, Analyze(&cfg, []string{run.Statement})); v.Blocked != "" || v.Writes {
		t.Fatalf("%q: %+v", run.Statement, v)
	}
	ctx := context.Background()
	d, err := db.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.Query(ctx, run.Statement)
	if err != nil {
		t.Fatalf("%q on read-only: %v", run.Statement, err)
	}
	c.Close()
	for _, sql := range []string{"BEGIN", "COMMIT", "CREATE TABLE crdb_read_only (a INT)", "BEGIN READ WRITE", "CREATE TABLE crdb_read_only (a INT)"} {
		_, err := s.Exec(ctx, sql)
		if strings.HasPrefix(sql, "CREATE") && err == nil {
			t.Fatalf("the server ran %q on a read-only session", sql)
		}
	}
}

// YugabyteDB refuses the writes of a read-only session, after a COMMIT
// too, its functions that write among them; the app refuses those, and
// the ones changing another transaction or the server's files, first.
func TestIntegrationYugabyteDBReadOnly(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "yugabytedb")
	cfg := db.Config{Name: "yb", Engine: db.YugabyteDB, Host: "127.0.0.1", Port: 15433, User: "yugabyte", Password: "dgopher", Database: "shop", ReadOnly: true}
	for _, sql := range []string{
		"INSERT INTO t VALUES (1)", "SELECT yb_reset_analyze_statistics(NULL)", "SELECT yb_cancel_transaction('6a074fc7-5ede-46c9-8017-caad24f14ddf')",
		"SELECT yb_query_diagnostics(query_id => 1)", "CREATE TABLEGROUP g", "EXPLAIN (ANALYZE, DIST) DELETE FROM t",
	} {
		if v := ReviewSQL(&cfg, Analyze(&cfg, []string{sql})); v.Blocked == "" {
			t.Errorf("%q not refused on a read-only connection", sql)
		}
	}
	run, _ := db.Explain(cfg.Engine, "SELECT 1", true)
	if v := ReviewSQL(&cfg, Analyze(&cfg, []string{run.Statement})); v.Blocked != "" || v.Writes {
		t.Fatalf("%q: %+v", run.Statement, v)
	}
	ctx := context.Background()
	d, err := db.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.Query(ctx, run.Statement)
	if err != nil {
		t.Fatalf("%q on read-only: %v", run.Statement, err)
	}
	c.Close()
	for _, sql := range []string{"BEGIN", "COMMIT", "CREATE TABLE yb_read_only (a INT)", "SELECT yb_reset_analyze_statistics(NULL)"} {
		_, err := s.Exec(ctx, sql)
		if (strings.HasPrefix(sql, "CREATE") || strings.Contains(sql, "yb_")) && err == nil {
			t.Fatalf("the server ran %q on a read-only session", sql)
		}
	}
}

// TimescaleDB refuses most of its functions that write on a read-only
// session, as add_job, but runs some (reorder_chunk, remove_all_policies,
// internal functions; 2.31): the app refuses them all first.
func TestIntegrationTimescaleDBReadOnly(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "timescaledb")
	cfg := db.Config{Name: "ts", Engine: db.TimescaleDB, Host: "127.0.0.1", Port: 15434, User: "postgres", Password: "dgopher", Database: "shop", ReadOnly: true}
	for _, sql := range []string{
		"SELECT add_retention_policy('m', INTERVAL '1 day')", "SELECT timescaledb_experimental.remove_all_policies('m')",
		"SELECT reorder_chunk('c', 'i')", "SELECT _timescaledb_functions.stop_background_workers()", "SELECT drop_chunks('m', older_than => now())",
		"CALL refresh_continuous_aggregate('m', NULL, NULL)", "SELECT timescaledb_pre_restore()",
	} {
		if v := ReviewSQL(&cfg, Analyze(&cfg, []string{sql})); v.Blocked == "" {
			t.Errorf("%q not refused on a read-only connection", sql)
		}
	}
	ctx := context.Background()
	d, err := db.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Exec(ctx, "SELECT add_job('pg_catalog.now'::regproc, INTERVAL '1 hour')"); err == nil {
		t.Fatal("the server ran add_job on a read-only session")
	}
}

// A quoted EXPLAIN option is read as the server reads it: a quoted
// ANALYZE of a write asks on production, is refused on read-only and gets
// manual commit's BEGIN, as does a quoted DEBUG on CockroachDB.
func TestExplainQuotedOptions(t *testing.T) {
	for _, e := range []db.Engine{db.Postgres, db.CockroachDB} {
		prod := &db.Config{Name: "prod", Engine: e, Env: db.Production}
		ro := &db.Config{Name: "replica", Engine: e, ReadOnly: true}
		for _, sql := range []string{`EXPLAIN ("analyze") DELETE FROM t WHERE id = 1`, `EXPLAIN ANALYZE ("debug") SELECT 1`, `EXPLAIN (U&"analyze") DELETE FROM t WHERE id = 1`} {
			if v := ReviewSQL(prod, Analyze(prod, []string{sql})); !v.Confirm || !v.Writes {
				t.Errorf("%s %q on production: %+v", e, sql, v)
			}
			if v := ReviewSQL(ro, Analyze(ro, []string{sql})); v.Blocked == "" {
				t.Errorf("%s %q not refused on read-only", e, sql)
			}
			if !WritesInTransaction(e, Analyze(prod, []string{sql})[0]) {
				t.Errorf("%s %q gets no BEGIN under manual commit", e, sql)
			}
		}
	}
}

// A schema change or TRUNCATE on YugabyteDB takes effect at once, neither
// part of the open transaction nor committing it: inside one it asks
// first, saying Roll Back will not undo it, and manual commit opens no
// transaction for it. A CREATE INDEX there should run outside one too.
// PostgreSQL's are part of the transaction, and CockroachDB's commit it.
func TestSchemaChangeTakesEffectAtOnce(t *testing.T) {
	const atOnce = "takes effect at once: Roll Back will not undo it"
	has := func(v Verdict, part string) bool {
		return slices.ContainsFunc(v.Reasons, func(r string) bool { return strings.Contains(r, part) })
	}
	yb := &db.Config{Name: "yb", Engine: db.YugabyteDB}
	for _, sql := range []string{"CREATE TABLE t (a int)", "TRUNCATE t", "ALTER TABLE t ADD b int", "DROP TABLE t", "GRANT SELECT ON t TO public",
		"CREATE INDEX i ON t (a)", "CREATE INDEX NONCONCURRENTLY i ON t (a)"} {
		st := Analyze(yb, []string{sql})
		var v Verdict
		v.EndsTransaction(yb, st, true)
		if !v.Confirm || !has(v, atOnce) {
			t.Errorf("%q in an open transaction: %+v", sql, v)
		}
		if outside := has(v, "should run outside a transaction"); outside != (sql == "CREATE INDEX i ON t (a)") {
			t.Errorf("%q: outside a transaction %v: %+v", sql, outside, v)
		}
		if WritesInTransaction(yb.Engine, st[0]) {
			t.Errorf("%q: manual commit opens a transaction for it", sql)
		}
		var none Verdict
		if none.EndsTransaction(yb, st, false); none.Confirm {
			t.Errorf("%q with no transaction open: %+v", sql, none)
		}
	}
	if ins := Analyze(yb, []string{"INSERT INTO t VALUES (1)"})[0]; !WritesInTransaction(yb.Engine, ins) {
		t.Error("INSERT: manual commit opens no transaction for it")
	}
	for _, c := range []struct {
		sql    string
		manual bool
		warns  bool
	}{
		{"BEGIN;TRUNCATE t", false, true},
		{"INSERT INTO t VALUES (1);CREATE TABLE u (a int)", true, true},
		{"TRUNCATE t", true, false},
		{"TRUNCATE t", false, false},
		{"BEGIN;COMMIT;TRUNCATE t", false, false},
	} {
		var v Verdict
		v.EndsRunTransaction(yb, Analyze(yb, strings.Split(c.sql, ";")), c.manual, false)
		if v.Confirm != c.warns || has(v, atOnce) != c.warns {
			t.Errorf("%q, manual %v: %+v", c.sql, c.manual, v)
		}
	}
	pg := &db.Config{Name: "pg", Engine: db.Postgres}
	crdb := &db.Config{Name: "crdb", Engine: db.CockroachDB}
	for _, cfg := range []*db.Config{pg, crdb} {
		st := Analyze(cfg, []string{"TRUNCATE t"})
		var v Verdict
		v.EndsTransaction(cfg, st, true)
		if has(v, atOnce) || cfg == pg && (v.Confirm || !WritesInTransaction(cfg.Engine, st[0])) {
			t.Errorf("%s TRUNCATE in an open transaction: %+v", cfg.Engine, v)
		}
	}
	// The MySQL family's CREATE TEMPORARY TABLE commits nothing, and
	// ROLLBACK keeps the table (checked on MySQL 8.4, MariaDB 11.4 and
	// TiDB 8.5).
	for _, e := range []db.Engine{db.MySQL, db.MariaDB, db.TiDB} {
		cfg := &db.Config{Name: "my", Engine: e}
		st := Analyze(cfg, []string{"CREATE TEMPORARY TABLE x (a INT)"})
		var v Verdict
		if v.EndsTransaction(cfg, st, true); !v.Confirm || !has(v, atOnce) || has(v, "commits") || WritesInTransaction(e, st[0]) {
			t.Errorf("%s CREATE TEMPORARY TABLE in an open transaction: %+v", e, v)
		}
		for _, run := range []struct {
			sql    string
			manual bool
		}{
			{"INSERT INTO t VALUES (1); CREATE TEMPORARY TABLE x (a INT)", true},
			{"BEGIN; INSERT INTO t VALUES (1); CREATE TEMPORARY TABLE x (a INT)", false},
		} {
			var v Verdict
			if v.EndsRunTransaction(cfg, Analyze(cfg, strings.Split(run.sql, ";")), run.manual, false); !v.Confirm || len(v.Reasons) != 1 || !has(v, atOnce) {
				t.Errorf("%s %q: %+v", e, run.sql, v)
			}
		}
	}
}

// Turning off YugabyteDB's transactional writes or turning on its upsert
// mode is refused on read-only and asks on production.
func TestYugabyteDBUnsafeSettings(t *testing.T) {
	prod := &db.Config{Name: "prod", Engine: db.YugabyteDB, Env: db.Production}
	ro := &db.Config{Name: "replica", Engine: db.YugabyteDB, ReadOnly: true}
	for _, sql := range []string{"SET yb_disable_transactional_writes = true", "RESET yb_enable_upsert_mode", "SET LOCAL yb_enable_upsert_mode = on"} {
		if v := ReviewSQL(ro, Analyze(ro, []string{sql})); v.Blocked == "" {
			t.Errorf("%q not refused on read-only", sql)
		}
		if v := ReviewSQL(prod, Analyze(prod, []string{sql})); !v.Confirm {
			t.Errorf("%q on production: %+v", sql, v)
		}
	}
}

// Telling TimescaleDB a restore runs, which turns its hypertables' inserts
// and chunks off for the session, is refused on read-only and asks on
// production.
func TestTimescaleDBRestoringSetting(t *testing.T) {
	prod := &db.Config{Name: "prod", Engine: db.TimescaleDB, Env: db.Production}
	ro := &db.Config{Name: "replica", Engine: db.TimescaleDB, ReadOnly: true}
	for _, sql := range []string{"SET timescaledb.restoring = on", "RESET timescaledb.restoring", "SET LOCAL timescaledb.restoring TO off", `SET "timescaledb.restoring" = on`} {
		if v := ReviewSQL(ro, Analyze(ro, []string{sql})); v.Blocked == "" {
			t.Errorf("%q not refused on read-only", sql)
		}
		if v := ReviewSQL(prod, Analyze(prod, []string{sql})); !v.Confirm {
			t.Errorf("%q on production: %+v", sql, v)
		}
	}
}

// Opening crdb_internal to the session is refused on read-only and asks
// on production.
func TestAllowUnsafeInternals(t *testing.T) {
	prod := &db.Config{Name: "prod", Engine: db.CockroachDB, Env: db.Production}
	ro := &db.Config{Name: "replica", Engine: db.CockroachDB, ReadOnly: true}
	for _, sql := range []string{"SET allow_unsafe_internals = on", "RESET allow_unsafe_internals", "SET LOCAL allow_unsafe_internals = true"} {
		if v := ReviewSQL(ro, Analyze(ro, []string{sql})); v.Blocked == "" {
			t.Errorf("%q not refused on read-only", sql)
		}
		if v := ReviewSQL(prod, Analyze(prod, []string{sql})); !v.Confirm {
			t.Errorf("%q on production: %+v", sql, v)
		}
	}
}

// A T-SQL batch is read statement by statement for manual commit: UPDATE
// STATISTICS among writes runs in the transaction opened for them, and
// asks first, as it takes effect at once; a statement that refuses a
// transaction among writes asks first, as manual commit then opens none.
func TestSQLServerBatchTransaction(t *testing.T) {
	cfg := &db.Config{Name: "ms", Engine: db.SQLServer, Env: db.Development, Commit: db.CommitManual}
	for _, c := range []struct {
		sql        string
		writesInTx bool
		manual     string // the reason manual commit asks with, "" for none
	}{
		{"UPDATE STATISTICS t", false, ""},
		{"EXEC sp_updatestats", false, ""},
		{"UPDATE STATISTICS t\nUPDATE t SET a = 1 WHERE id = 1", true, "UPDATE STATISTICS takes effect at once"},
		{"INSERT INTO t VALUES (1)\nEXEC sp_updatestats", true, "EXEC sp_updatestats takes effect at once"},
		{"INSERT INTO t VALUES (1)\nKILL 52", false, "KILL cannot run in a transaction"},
		{"BACKUP DATABASE shop TO DISK = 'x'\nDELETE FROM t WHERE id = 1", false, "BACKUP cannot run in a transaction"},
		{"BACKUP DATABASE shop TO DISK = 'x'", false, ""},
		{"SELECT 1\nBACKUP DATABASE shop TO DISK = 'x'", false, ""},
		{"KILL 52\nUPDATE STATISTICS t", false, ""},
		{"UPDATE t SET a = 1 WHERE id = 1\nSELECT 2", true, ""},
		{"CREATE PROCEDURE p AS BEGIN UPDATE t SET a = 1 WHERE id = 1; BACKUP DATABASE shop TO DISK = 'x' END", true, ""},
	} {
		s := Analyze(cfg, []string{c.sql})
		if got := WritesInTransaction(cfg.Engine, s[0]); got != c.writesInTx {
			t.Errorf("%q: WritesInTransaction %v, want %v", c.sql, got, c.writesInTx)
		}
		var v Verdict
		v.EndsRunTransaction(cfg, s, true, false)
		if c.manual == "" && (v.Confirm || len(v.Reasons) > 0) || c.manual != "" && (!v.Confirm || len(v.Reasons) != 1 || !strings.HasPrefix(v.Reasons[0], c.manual)) {
			t.Errorf("%q under manual commit: %+v", c.sql, v)
		}
		var auto Verdict
		if auto.EndsRunTransaction(cfg, s, false, false); auto.Confirm {
			t.Errorf("%q under autocommit: %+v", c.sql, auto)
		}
	}
}

// A T-SQL batch that writes and begins or ends a transaction of its own
// controls it: the change guard's transaction would end inside it.
func TestControlsOwnTransaction(t *testing.T) {
	for _, c := range []struct {
		engine db.Engine
		sql    string
		want   bool
	}{
		{db.SQLServer, "UPDATE t SET a = 1 WHERE id > 0\nCOMMIT", true},
		{db.SQLServer, "DELETE FROM t WHERE id > 0\nROLLBACK TRANSACTION", true},
		{db.SQLServer, "BEGIN TRAN\nUPDATE t SET a = 1 WHERE id > 0", true},
		{db.SQLServer, "UPDATE t SET a = 1 WHERE id > 0\nSELECT 1", false},
		{db.SQLServer, "COMMIT", false},
		{db.SQLServer, "SELECT 1\nCOMMIT", false},
		{db.SQLServer, "CREATE PROCEDURE p AS BEGIN UPDATE t SET a = 1 WHERE id > 0 COMMIT END", false},
		{db.Postgres, "UPDATE t SET a = 1 WHERE id > 0", false},
	} {
		cfg := &db.Config{Engine: c.engine}
		if got := ControlsOwnTransaction(c.engine, Analyze(cfg, []string{c.sql})[0]); got != c.want {
			t.Errorf("%s %q: %v, want %v", c.engine, c.sql, got, c.want)
		}
	}
}

// A T-SQL batch holding more than one statement that writes makes
// several changes; one write among reads, or another engine's statement,
// does not.
func TestMakesSeveralChanges(t *testing.T) {
	for _, c := range []struct {
		engine db.Engine
		sql    string
		want   bool
	}{
		{db.SQLServer, "INSERT INTO t VALUES (1)\nUPDATE t SET a = 1 WHERE id > 0", true},
		{db.SQLServer, "UPDATE t SET a = 1 WHERE id > 0\nUPDATE t SET b = 2 WHERE id > 0", true},
		{db.SQLServer, "DELETE FROM t WHERE id = 1; DELETE FROM u WHERE id = 1", true},
		{db.SQLServer, "UPDATE t SET a = 1 WHERE id > 0\nSELECT 1", false},
		{db.SQLServer, "DECLARE @n int = 1\nUPDATE t SET a = @n WHERE id > 0", false},
		{db.SQLServer, "CREATE PROCEDURE p AS BEGIN UPDATE t SET a = 1 WHERE id > 0 DELETE FROM t WHERE id = 1 END", false},
		{db.Postgres, "WITH d AS (DELETE FROM t RETURNING id) UPDATE u SET a = 1 WHERE id IN (SELECT id FROM d)", false},
	} {
		cfg := &db.Config{Engine: c.engine}
		if got := MakesSeveralChanges(c.engine, Analyze(cfg, []string{c.sql})[0]); got != c.want {
			t.Errorf("%s %q: %v, want %v", c.engine, c.sql, got, c.want)
		}
	}
}

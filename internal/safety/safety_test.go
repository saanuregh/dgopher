package safety

import (
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

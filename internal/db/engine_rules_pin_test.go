package db

// Characterization pins: what every engine-dependent hook of this package
// gives today, for each engine and for an ID no build knows. Each test
// reaches its hook through one local closure; when a switch moves into a
// per-engine method, re-point that closure and leave the pinned values
// alone. A value printed as "unpinned" is one the table lacks.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/go-sql-driver/mysql"
)

// pinEngines are the engine IDs every pin covers: the six engines and one
// no build knows.
var pinEngines = []Engine{Postgres, MySQL, ClickHouse, SQLite, DuckDB, Redis, "unknown"}

// sqlDialectEngines are the engines with a Dialect: the hooks that take one
// cannot be reached for Redis or an unknown engine.
var sqlDialectEngines = []Engine{Postgres, MySQL, ClickHouse, SQLite, DuckDB}

// pinValues checks the values produce gives against want, by key: each
// key given must be pinned with the same value, and each pinned key given.
func pinValues(t *testing.T, name string, want map[string]string, produce func(add func(key, got string))) {
	t.Helper()
	seen := map[string]bool{}
	produce(func(key, got string) {
		t.Helper()
		if seen[key] {
			t.Fatalf("%s: key %q given twice", name, key)
		}
		seen[key] = true
		w, ok := want[key]
		switch {
		case !ok:
			t.Errorf("unpinned %s: %q: %q,", name, key, got)
		case got != w:
			t.Errorf("%s %s changed:\n got  %q\n want %q", name, key, got, w)
		}
	})
	for key := range want {
		if !seen[key] {
			t.Errorf("%s: %q is pinned but no longer given", name, key)
		}
	}
}

// dialectEngine names a dialect by its engine, "nil" for none: the type
// that holds it may change.
func dialectEngine(d Dialect) string {
	if d == nil {
		return "nil"
	}
	return string(d.Engine())
}

// txName names a transaction state.
func txName(s TxState) string {
	switch s {
	case TxNone:
		return "none"
	case TxOpen:
		return "open"
	case TxFailed:
		return "failed"
	}
	return fmt.Sprint(int(s))
}

func errText(err error) string {
	if err == nil {
		return "ok"
	}
	return "error: " + err.Error()
}

// Engine facts that stay Engine methods.
func TestPinEngineCapabilities(t *testing.T) {
	pinValues(t, "capabilities", map[string]string{
		"postgres":   "label=PostgreSQL port=5432 user=\"postgres\" sql=true file=false transactions=true savepoints=true transactionalDDL=true dialect=postgres lex=1",
		"mysql":      "label=MySQL port=3306 user=\"root\" sql=true file=false transactions=true savepoints=true transactionalDDL=false dialect=mysql lex=2",
		"clickhouse": "label=ClickHouse port=9000 user=\"default\" sql=true file=false transactions=false savepoints=false transactionalDDL=false dialect=clickhouse lex=3",
		"sqlite":     "label=SQLite port=0 user=\"\" sql=true file=true transactions=true savepoints=true transactionalDDL=true dialect=sqlite lex=4",
		"duckdb":     "label=DuckDB port=0 user=\"\" sql=true file=true transactions=true savepoints=false transactionalDDL=true dialect=duckdb lex=1",
		"redis":      "label=Redis port=6379 user=\"\" sql=false file=false transactions=false savepoints=false transactionalDDL=false dialect=nil lex=0",
		"unknown":    "label=unknown port=0 user=\"\" sql=true file=false transactions=true savepoints=false transactionalDDL=false dialect=nil lex=0",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			add(string(e), fmt.Sprintf("label=%s port=%d user=%q sql=%v file=%v transactions=%v savepoints=%v transactionalDDL=%v dialect=%s lex=%v",
				e.Label(), e.DefaultPort(), e.DefaultUser(), e.IsSQL(), e.IsFile(), e.Transactions(), e.Savepoints(), e.TransactionalDDL(), dialectEngine(DialectOf(e)), LexDialect(e)))
		}
	})
}

// TestPinExplainPrefix pins the prefix of Explain's statement, which was
// ExplainPrefix.
func TestPinExplainPrefix(t *testing.T) {
	explain := func(e Engine, analyze bool) string {
		run, _ := Explain(e, "", analyze)
		return run.Statement[:run.Offset]
	}
	pinValues(t, "explain prefix", map[string]string{
		"postgres analyze=false":   "EXPLAIN (VERBOSE, FORMAT JSON) ",
		"postgres analyze=true":    "EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON) ",
		"mysql analyze=false":      "EXPLAIN FORMAT=TREE ",
		"mysql analyze=true":       "EXPLAIN ANALYZE ",
		"clickhouse analyze=false": "EXPLAIN json = 1, description = 1, indexes = 1 ",
		"clickhouse analyze=true":  "",
		"sqlite analyze=false":     "EXPLAIN QUERY PLAN ",
		"sqlite analyze=true":      "",
		"duckdb analyze=false":     "EXPLAIN (FORMAT JSON) ",
		"duckdb analyze=true":      "EXPLAIN (ANALYZE, FORMAT JSON) ",
		"redis analyze=false":      "EXPLAIN ",
		"redis analyze=true":       "",
		"unknown analyze=false":    "EXPLAIN ",
		"unknown analyze=true":     "",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			for _, analyze := range []bool{false, true} {
				add(fmt.Sprintf("%s analyze=%v", e, analyze), explain(e, analyze))
			}
		}
	})
}

// TestPinUsersSupported pins which engines manage accounts.
func TestPinUsersSupported(t *testing.T) {
	managesAccounts := func(r SQLRules) bool { return r.ManagesAccounts() }
	privileges := func(r SQLRules) []string { return r.Privileges() }
	pinValues(t, "users supported", map[string]string{
		"postgres":   "manages=true privileges=[\"SELECT\" \"INSERT\" \"UPDATE\" \"DELETE\" \"ALL PRIVILEGES\"]",
		"mysql":      "manages=true privileges=[\"SELECT\" \"INSERT\" \"UPDATE\" \"DELETE\" \"ALL PRIVILEGES\"]",
		"clickhouse": "manages=true privileges=[\"SELECT\" \"INSERT\" \"ALTER\" \"ALL\"]",
		"sqlite":     "manages=false privileges=[\"SELECT\" \"INSERT\" \"UPDATE\" \"DELETE\" \"ALL PRIVILEGES\"]",
		"duckdb":     "manages=false privileges=[\"SELECT\" \"INSERT\" \"UPDATE\" \"DELETE\" \"ALL PRIVILEGES\"]",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			// Redis and an unknown engine: the app's usersSupported.
			if r, ok := SQL(e); ok {
				add(string(e), fmt.Sprintf("manages=%v privileges=%q", managesAccounts(r), privileges(r)))
			}
		}
	})
}

// TestPinCommonTypes pins each engine's common types.
func TestPinCommonTypes(t *testing.T) {
	commonTypes := func(r SQLRules) []string { return r.CommonTypes() }
	pinValues(t, "common types", map[string]string{
		"postgres":   "[\"integer\" \"bigint\" \"smallint\" \"numeric(12,2)\" \"real\" \"double precision\" \"boolean\" \"text\" \"varchar(255)\" \"date\" \"timestamp\" \"timestamptz\" \"time\" \"interval\" \"uuid\" \"jsonb\" \"bytea\"]",
		"mysql":      "[\"int\" \"bigint\" \"smallint\" \"tinyint(1)\" \"decimal(12,2)\" \"float\" \"double\" \"varchar(255)\" \"text\" \"date\" \"datetime\" \"timestamp\" \"time\" \"json\" \"blob\"]",
		"clickhouse": "[\"Int32\" \"Int64\" \"UInt32\" \"UInt64\" \"Float64\" \"Decimal(12,2)\" \"Bool\" \"String\" \"LowCardinality(String)\" \"Date\" \"Date32\" \"DateTime\" \"DateTime64(3)\" \"UUID\"]",
		"sqlite":     "[\"INTEGER\" \"REAL\" \"TEXT\" \"BLOB\" \"NUMERIC\"]",
		"duckdb":     "[\"INTEGER\" \"BIGINT\" \"SMALLINT\" \"DECIMAL(12,2)\" \"DOUBLE\" \"BOOLEAN\" \"VARCHAR\" \"DATE\" \"TIMESTAMP\" \"TIMESTAMPTZ\" \"TIME\" \"INTERVAL\" \"UUID\" \"JSON\" \"BLOB\"]",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			// Redis and an unknown engine open no table form.
			if r, ok := SQL(e); ok {
				add(string(e), fmt.Sprintf("%q", commonTypes(r)))
			}
		}
	})
}

// What the wire records a driver sending: pingStatement, beginStatements
// and endStatement become hooks of the same names.
func TestPinWireStatements(t *testing.T) {
	ping := func(e Engine) string { return pingStatement(e) }
	begin := func(e Engine, opts driver.TxOptions) []string { return beginStatements(e, opts) }
	end := func(e Engine, commit bool) string { return endStatement(e, commit) }
	options := []struct {
		name string
		opts driver.TxOptions
	}{
		{"default", driver.TxOptions{}},
		{"read only", driver.TxOptions{ReadOnly: true}},
		{"read uncommitted", driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelReadUncommitted)}},
		{"read committed read only", driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelReadCommitted), ReadOnly: true}},
		{"repeatable read", driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelRepeatableRead)}},
		{"snapshot", driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelSnapshot)}},
		{"serializable", driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelSerializable)}},
	}
	pinValues(t, "wire statements", map[string]string{
		"postgres ping":                             "-- ping",
		"postgres begin default":                    "[\"begin\"] nil=false",
		"postgres begin read only":                  "[\"begin read only\"] nil=false",
		"postgres begin read uncommitted":           "[\"begin isolation level read uncommitted\"] nil=false",
		"postgres begin read committed read only":   "[\"begin isolation level read committed read only\"] nil=false",
		"postgres begin repeatable read":            "[\"begin isolation level repeatable read\"] nil=false",
		"postgres begin snapshot":                   "[\"begin isolation level repeatable read\"] nil=false",
		"postgres begin serializable":               "[\"begin isolation level serializable\"] nil=false",
		"postgres commit":                           "commit",
		"postgres rollback":                         "rollback",
		"mysql ping":                                "-- ping",
		"mysql begin default":                       "[\"START TRANSACTION\"] nil=false",
		"mysql begin read only":                     "[\"START TRANSACTION READ ONLY\"] nil=false",
		"mysql begin read uncommitted":              "[\"SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED\" \"START TRANSACTION\"] nil=false",
		"mysql begin read committed read only":      "[\"SET TRANSACTION ISOLATION LEVEL READ COMMITTED\" \"START TRANSACTION READ ONLY\"] nil=false",
		"mysql begin repeatable read":               "[\"SET TRANSACTION ISOLATION LEVEL REPEATABLE READ\" \"START TRANSACTION\"] nil=false",
		"mysql begin snapshot":                      "[\"SET TRANSACTION ISOLATION LEVEL \" \"START TRANSACTION\"] nil=false",
		"mysql begin serializable":                  "[\"SET TRANSACTION ISOLATION LEVEL SERIALIZABLE\" \"START TRANSACTION\"] nil=false",
		"mysql commit":                              "COMMIT",
		"mysql rollback":                            "ROLLBACK",
		"clickhouse ping":                           "-- ping",
		"clickhouse begin default":                  "[] nil=true",
		"clickhouse begin read only":                "[] nil=true",
		"clickhouse begin read uncommitted":         "[] nil=true",
		"clickhouse begin read committed read only": "[] nil=true",
		"clickhouse begin repeatable read":          "[] nil=true",
		"clickhouse begin snapshot":                 "[] nil=true",
		"clickhouse begin serializable":             "[] nil=true",
		"clickhouse commit":                         "",
		"clickhouse rollback":                       "",
		"sqlite ping":                               "select 1",
		"sqlite begin default":                      "[\"begin\"] nil=false",
		"sqlite begin read only":                    "[\"begin\"] nil=false",
		"sqlite begin read uncommitted":             "[\"begin\"] nil=false",
		"sqlite begin read committed read only":     "[\"begin\"] nil=false",
		"sqlite begin repeatable read":              "[\"begin\"] nil=false",
		"sqlite begin snapshot":                     "[\"begin\"] nil=false",
		"sqlite begin serializable":                 "[\"begin\"] nil=false",
		"sqlite commit":                             "commit",
		"sqlite rollback":                           "rollback",
		"duckdb ping":                               "-- ping",
		"duckdb begin default":                      "[\"BEGIN TRANSACTION\"] nil=false",
		"duckdb begin read only":                    "[\"BEGIN TRANSACTION\"] nil=false",
		"duckdb begin read uncommitted":             "[\"BEGIN TRANSACTION\"] nil=false",
		"duckdb begin read committed read only":     "[\"BEGIN TRANSACTION\"] nil=false",
		"duckdb begin repeatable read":              "[\"BEGIN TRANSACTION\"] nil=false",
		"duckdb begin snapshot":                     "[\"BEGIN TRANSACTION\"] nil=false",
		"duckdb begin serializable":                 "[\"BEGIN TRANSACTION\"] nil=false",
		"duckdb commit":                             "COMMIT TRANSACTION",
		"duckdb rollback":                           "ROLLBACK",
		"redis ping":                                "-- ping",
		"redis begin default":                       "[] nil=true",
		"redis begin read only":                     "[] nil=true",
		"redis begin read uncommitted":              "[] nil=true",
		"redis begin read committed read only":      "[] nil=true",
		"redis begin repeatable read":               "[] nil=true",
		"redis begin snapshot":                      "[] nil=true",
		"redis begin serializable":                  "[] nil=true",
		"redis commit":                              "",
		"redis rollback":                            "",
		"unknown ping":                              "-- ping",
		"unknown begin default":                     "[] nil=true",
		"unknown begin read only":                   "[] nil=true",
		"unknown begin read uncommitted":            "[] nil=true",
		"unknown begin read committed read only":    "[] nil=true",
		"unknown begin repeatable read":             "[] nil=true",
		"unknown begin snapshot":                    "[] nil=true",
		"unknown begin serializable":                "[] nil=true",
		"unknown commit":                            "",
		"unknown rollback":                          "",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			add(string(e)+" ping", ping(e))
			for _, o := range options {
				got := begin(e, o.opts)
				add(string(e)+" begin "+o.name, fmt.Sprintf("%q nil=%v", got, got == nil))
			}
			add(string(e)+" commit", end(e, true))
			add(string(e)+" rollback", end(e, false))
		}
	})
}

// What Session.Begin and the grid's changes send to begin.
func TestPinBeginStatement(t *testing.T) {
	pinValues(t, "begin statement", map[string]string{
		"postgres":   "BEGIN",
		"mysql":      "START TRANSACTION",
		"clickhouse": "BEGIN",
		"sqlite":     "BEGIN",
		"duckdb":     "BEGIN",
		"redis":      "BEGIN",
		"unknown":    "BEGIN",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			add(string(e), beginStatement(e))
		}
	})
}

// MySQL's implicit commits and autocommit, false everywhere else.
func TestPinImplicitCommitAndAutocommit(t *testing.T) {
	stmts := []string{
		"CREATE TABLE t (a INT)",
		"CREATE TEMPORARY TABLE t (a INT)",
		"TRUNCATE t",
		"BEGIN",
		"BEGIN NOT ATOMIC SELECT 1; END",
		"SELECT 1",
		"SET autocommit = 0",
		"SET autocommit = 1",
		"SET GLOBAL autocommit = 0",
		"/*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50001 VIEW v AS SELECT 1 */",
	}
	pinValues(t, "implicit commit", map[string]string{
		"postgres CREATE TABLE t (a INT)":           "commits=false setsAutocommit=false,false",
		"postgres CREATE TEMPORARY TABLE t (a INT)": "commits=false setsAutocommit=false,false",
		"postgres TRUNCATE t":                       "commits=false setsAutocommit=false,false",
		"postgres BEGIN":                            "commits=false setsAutocommit=false,false",
		"postgres BEGIN NOT ATOMIC SELECT 1; END":   "commits=false setsAutocommit=false,false",
		"postgres SELECT 1":                         "commits=false setsAutocommit=false,false",
		"postgres SET autocommit = 0":               "commits=false setsAutocommit=false,false",
		"postgres SET autocommit = 1":               "commits=false setsAutocommit=false,false",
		"postgres SET GLOBAL autocommit = 0":        "commits=false setsAutocommit=false,false",
		"postgres /*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50001 VIEW v AS SELECT 1 */": "commits=false setsAutocommit=false,false",
		"mysql CREATE TABLE t (a INT)":           "commits=true setsAutocommit=false,false",
		"mysql CREATE TEMPORARY TABLE t (a INT)": "commits=false setsAutocommit=false,false",
		"mysql TRUNCATE t":                       "commits=true setsAutocommit=false,false",
		"mysql BEGIN":                            "commits=true setsAutocommit=false,false",
		"mysql BEGIN NOT ATOMIC SELECT 1; END":   "commits=false setsAutocommit=false,false",
		"mysql SELECT 1":                         "commits=false setsAutocommit=false,false",
		"mysql SET autocommit = 0":               "commits=false setsAutocommit=false,true",
		"mysql SET autocommit = 1":               "commits=true setsAutocommit=true,true",
		"mysql SET GLOBAL autocommit = 0":        "commits=false setsAutocommit=false,false",
		"mysql /*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50001 VIEW v AS SELECT 1 */": "commits=true setsAutocommit=false,false",
		"clickhouse CREATE TABLE t (a INT)":                                           "commits=false setsAutocommit=false,false",
		"clickhouse CREATE TEMPORARY TABLE t (a INT)":                                 "commits=false setsAutocommit=false,false",
		"clickhouse TRUNCATE t":                                                       "commits=false setsAutocommit=false,false",
		"clickhouse BEGIN":                                                            "commits=false setsAutocommit=false,false",
		"clickhouse BEGIN NOT ATOMIC SELECT 1; END":                                   "commits=false setsAutocommit=false,false",
		"clickhouse SELECT 1":                                                         "commits=false setsAutocommit=false,false",
		"clickhouse SET autocommit = 0":                                               "commits=false setsAutocommit=false,false",
		"clickhouse SET autocommit = 1":                                               "commits=false setsAutocommit=false,false",
		"clickhouse SET GLOBAL autocommit = 0":                                        "commits=false setsAutocommit=false,false",
		"clickhouse /*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50001 VIEW v AS SELECT 1 */": "commits=false setsAutocommit=false,false",
		"sqlite CREATE TABLE t (a INT)":           "commits=false setsAutocommit=false,false",
		"sqlite CREATE TEMPORARY TABLE t (a INT)": "commits=false setsAutocommit=false,false",
		"sqlite TRUNCATE t":                       "commits=false setsAutocommit=false,false",
		"sqlite BEGIN":                            "commits=false setsAutocommit=false,false",
		"sqlite BEGIN NOT ATOMIC SELECT 1; END":   "commits=false setsAutocommit=false,false",
		"sqlite SELECT 1":                         "commits=false setsAutocommit=false,false",
		"sqlite SET autocommit = 0":               "commits=false setsAutocommit=false,false",
		"sqlite SET autocommit = 1":               "commits=false setsAutocommit=false,false",
		"sqlite SET GLOBAL autocommit = 0":        "commits=false setsAutocommit=false,false",
		"sqlite /*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50001 VIEW v AS SELECT 1 */": "commits=false setsAutocommit=false,false",
		"duckdb CREATE TABLE t (a INT)":           "commits=false setsAutocommit=false,false",
		"duckdb CREATE TEMPORARY TABLE t (a INT)": "commits=false setsAutocommit=false,false",
		"duckdb TRUNCATE t":                       "commits=false setsAutocommit=false,false",
		"duckdb BEGIN":                            "commits=false setsAutocommit=false,false",
		"duckdb BEGIN NOT ATOMIC SELECT 1; END":   "commits=false setsAutocommit=false,false",
		"duckdb SELECT 1":                         "commits=false setsAutocommit=false,false",
		"duckdb SET autocommit = 0":               "commits=false setsAutocommit=false,false",
		"duckdb SET autocommit = 1":               "commits=false setsAutocommit=false,false",
		"duckdb SET GLOBAL autocommit = 0":        "commits=false setsAutocommit=false,false",
		"duckdb /*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50001 VIEW v AS SELECT 1 */": "commits=false setsAutocommit=false,false",
		"redis CREATE TABLE t (a INT)":           "commits=false setsAutocommit=false,false",
		"redis CREATE TEMPORARY TABLE t (a INT)": "commits=false setsAutocommit=false,false",
		"redis TRUNCATE t":                       "commits=false setsAutocommit=false,false",
		"redis BEGIN":                            "commits=false setsAutocommit=false,false",
		"redis BEGIN NOT ATOMIC SELECT 1; END":   "commits=false setsAutocommit=false,false",
		"redis SELECT 1":                         "commits=false setsAutocommit=false,false",
		"redis SET autocommit = 0":               "commits=false setsAutocommit=false,false",
		"redis SET autocommit = 1":               "commits=false setsAutocommit=false,false",
		"redis SET GLOBAL autocommit = 0":        "commits=false setsAutocommit=false,false",
		"redis /*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50001 VIEW v AS SELECT 1 */": "commits=false setsAutocommit=false,false",
		"unknown CREATE TABLE t (a INT)":                                              "commits=false setsAutocommit=false,false",
		"unknown CREATE TEMPORARY TABLE t (a INT)":                                    "commits=false setsAutocommit=false,false",
		"unknown TRUNCATE t":                                                          "commits=false setsAutocommit=false,false",
		"unknown BEGIN":                                                               "commits=false setsAutocommit=false,false",
		"unknown BEGIN NOT ATOMIC SELECT 1; END":                                      "commits=false setsAutocommit=false,false",
		"unknown SELECT 1":                                                            "commits=false setsAutocommit=false,false",
		"unknown SET autocommit = 0":                                                  "commits=false setsAutocommit=false,false",
		"unknown SET autocommit = 1":                                                  "commits=false setsAutocommit=false,false",
		"unknown SET GLOBAL autocommit = 0":                                           "commits=false setsAutocommit=false,false",
		"unknown /*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50001 VIEW v AS SELECT 1 */": "commits=false setsAutocommit=false,false",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			for _, s := range stmts {
				on, ok := SetsAutocommit(e, s)
				add(string(e)+" "+s, fmt.Sprintf("commits=%v setsAutocommit=%v,%v", CommitsImplicitly(e, s), on, ok))
			}
		}
	})
}

// Validate knows the six engines, a cloud identity only for PostgreSQL
// and MySQL, and a clear-text password only for MySQL.
func TestPinValidateByEngine(t *testing.T) {
	pinValues(t, "validate", map[string]string{
		"postgres plain":        "ok",
		"postgres identity":     "ok",
		"postgres clear text":   "error: a password sent as clear text goes only to MySQL",
		"mysql plain":           "ok",
		"mysql identity":        "ok",
		"mysql clear text":      "ok",
		"clickhouse plain":      "ok",
		"clickhouse identity":   "error: a cloud identity logs in to an engine of PostgreSQL's or MySQL's family",
		"clickhouse clear text": "error: a password sent as clear text goes only to MySQL",
		"sqlite plain":          "ok",
		"sqlite identity":       "error: a cloud identity logs in to an engine of PostgreSQL's or MySQL's family",
		"sqlite clear text":     "error: a password sent as clear text goes only to MySQL",
		"duckdb plain":          "ok",
		"duckdb identity":       "error: a cloud identity logs in to an engine of PostgreSQL's or MySQL's family",
		"duckdb clear text":     "error: a password sent as clear text goes only to MySQL",
		"redis plain":           "ok",
		"redis identity":        "error: a cloud identity logs in to an engine of PostgreSQL's or MySQL's family",
		"redis clear text":      "error: a password sent as clear text goes only to MySQL",
		"unknown plain":         "error: unknown engine \"unknown\"",
		"unknown identity":      "error: unknown engine \"unknown\"; a cloud identity logs in to an engine of PostgreSQL's or MySQL's family",
		"unknown clear text":    "error: unknown engine \"unknown\"; a password sent as clear text goes only to MySQL",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			base := Config{Name: "n", Engine: e, Host: "db.example.com", Database: "0", TLS: TLSVerifyFull}
			add(string(e)+" plain", errText(base.Validate()))
			gcp := base
			gcp.Identity = IdentityGCP
			add(string(e)+" identity", errText(gcp.Validate()))
			clear := base
			clear.ClearTextPassword = true
			add(string(e)+" clear text", errText(clear.Validate()))
		}
	})
}

// Each URL scheme's engine, and which engines' TLS parameters a URL takes.
func TestPinURLSchemes(t *testing.T) {
	urls := []string{
		"postgres://h/d", "postgresql://h/d", "mysql://h/d", "mariadb://h/d", "clickhouse://h/d", "ch://h/d",
		"redis://h/0", "rediss://h/0", "sqlite:///tmp/a.db", "sqlite3:///tmp/a.db", "file:///tmp/a.db", "duckdb:///tmp/a.duckdb",
		"oracle://h/d", "sqlserver://h/d",
		"postgres://h/d?sslmode=require", "postgres://h/d?ssl-mode=REQUIRED", "postgres://h/d?secure=true",
		"mysql://h/d?ssl-mode=REQUIRED", "mysql://h/d?sslmode=require", "mysql://h/d?secure=true",
		"clickhouse://h/d?secure=true", "clickhouse://h/d?sslmode=require", "clickhouse://h/d?skip_verify=true",
		"redis://h/0?sslmode=require", "redis://h/0?skip_verify=true",
	}
	pinValues(t, "url schemes", map[string]string{
		"postgres://h/d":                    "engine=postgres tls=\"\" database=\"d\"",
		"postgresql://h/d":                  "engine=postgres tls=\"\" database=\"d\"",
		"mysql://h/d":                       "engine=mysql tls=\"\" database=\"d\"",
		"mariadb://h/d":                     "engine=mariadb tls=\"\" database=\"d\"",
		"clickhouse://h/d":                  "engine=clickhouse tls=\"\" database=\"d\"",
		"ch://h/d":                          "engine=clickhouse tls=\"\" database=\"d\"",
		"redis://h/0":                       "engine=redis tls=\"\" database=\"0\"",
		"rediss://h/0":                      "engine=redis tls=\"verify-full\" database=\"0\"",
		"sqlite:///tmp/a.db":                "engine=sqlite tls=\"\" database=\"/tmp/a.db\"",
		"sqlite3:///tmp/a.db":               "engine=sqlite tls=\"\" database=\"/tmp/a.db\"",
		"file:///tmp/a.db":                  "engine=sqlite tls=\"\" database=\"/tmp/a.db\"",
		"duckdb:///tmp/a.duckdb":            "engine=duckdb tls=\"\" database=\"/tmp/a.duckdb\"",
		"oracle://h/d":                      "error: unknown kind of URL \"oracle\": use postgres://, mysql://, clickhouse://, redis:// or a file path",
		"sqlserver://h/d":                   "error: unknown kind of URL \"sqlserver\": use postgres://, mysql://, clickhouse://, redis:// or a file path",
		"postgres://h/d?sslmode=require":    "engine=postgres tls=\"require\" database=\"d\"",
		"postgres://h/d?ssl-mode=REQUIRED":  "error: ssl-mode is no PostgreSQL URL parameter",
		"postgres://h/d?secure=true":        "error: secure is no PostgreSQL URL parameter",
		"mysql://h/d?ssl-mode=REQUIRED":     "engine=mysql tls=\"require\" database=\"d\"",
		"mysql://h/d?sslmode=require":       "error: sslmode is no MySQL URL parameter",
		"mysql://h/d?secure=true":           "error: secure is no MySQL URL parameter",
		"clickhouse://h/d?secure=true":      "engine=clickhouse tls=\"verify-full\" database=\"d\"",
		"clickhouse://h/d?sslmode=require":  "error: sslmode is no ClickHouse URL parameter",
		"clickhouse://h/d?skip_verify=true": "error: the URL's skip_verify needs TLS",
		"redis://h/0?sslmode=require":       "error: sslmode is no Redis URL parameter",
		"redis://h/0?skip_verify=true":      "error: the URL's skip_verify needs TLS",
	}, func(add func(key, got string)) {
		for _, u := range urls {
			cfg, err := ParseURL(u)
			if err != nil {
				add(u, errText(err))
				continue
			}
			add(u, fmt.Sprintf("engine=%s tls=%q database=%q", cfg.Engine, cfg.TLS, cfg.Database))
		}
	})
}

// Literals of each engine, and of engine "": standard SQL as SQLite reads it.
func TestPinLiterals(t *testing.T) {
	values := []struct {
		name string
		v    any
	}{
		{"quote", "it's"},
		{"backslash", `a\b`},
		{"nul", "a\x00b"},
		{"newline", "a\nb"},
		{"bytes", []byte{0xde, 0xad, 0xbe, 0xef}},
		{"bool", true},
		{"number", 42},
		{"nil", nil},
	}
	pinValues(t, "literals", map[string]string{
		"\"postgres\" quote":       "'it''s'",
		"\"postgres\" backslash":   "E'a\\\\b'",
		"\"postgres\" nul":         "('a' || chr(0) || 'b')",
		"\"postgres\" newline":     "'a\nb'",
		"\"postgres\" bytes":       "E'\\\\xdeadbeef'::bytea",
		"\"postgres\" bool":        "TRUE",
		"\"postgres\" number":      "42",
		"\"postgres\" nil":         "NULL",
		"\"mysql\" quote":          "'it''s'",
		"\"mysql\" backslash":      "'a\\\\b'",
		"\"mysql\" nul":            "'a\\0b'",
		"\"mysql\" newline":        "'a\\nb'",
		"\"mysql\" bytes":          "X'deadbeef'",
		"\"mysql\" bool":           "TRUE",
		"\"mysql\" number":         "42",
		"\"mysql\" nil":            "NULL",
		"\"clickhouse\" quote":     "'it\\'s'",
		"\"clickhouse\" backslash": "'a\\\\b'",
		"\"clickhouse\" nul":       "'a\\0b'",
		"\"clickhouse\" newline":   "'a\nb'",
		"\"clickhouse\" bytes":     "unhex('deadbeef')",
		"\"clickhouse\" bool":      "TRUE",
		"\"clickhouse\" number":    "42",
		"\"clickhouse\" nil":       "NULL",
		"\"sqlite\" quote":         "'it''s'",
		"\"sqlite\" backslash":     "'a\\b'",
		"\"sqlite\" nul":           "('a' || char(0) || 'b')",
		"\"sqlite\" newline":       "'a\nb'",
		"\"sqlite\" bytes":         "X'deadbeef'",
		"\"sqlite\" bool":          "TRUE",
		"\"sqlite\" number":        "42",
		"\"sqlite\" nil":           "NULL",
		"\"duckdb\" quote":         "'it''s'",
		"\"duckdb\" backslash":     "E'a\\\\b'",
		"\"duckdb\" nul":           "('a' || chr(0) || 'b')",
		"\"duckdb\" newline":       "'a\nb'",
		"\"duckdb\" bytes":         "'\\xDE\\xAD\\xBE\\xEF'::BLOB",
		"\"duckdb\" bool":          "TRUE",
		"\"duckdb\" number":        "42",
		"\"duckdb\" nil":           "NULL",
		"\"redis\" quote":          "'it''s'",
		"\"redis\" backslash":      "'a\\b'",
		"\"redis\" nul":            "('a' || chr(0) || 'b')",
		"\"redis\" newline":        "'a\nb'",
		"\"redis\" bytes":          "X'deadbeef'",
		"\"redis\" bool":           "TRUE",
		"\"redis\" number":         "42",
		"\"redis\" nil":            "NULL",
		"\"unknown\" quote":        "'it''s'",
		"\"unknown\" backslash":    "'a\\b'",
		"\"unknown\" nul":          "('a' || chr(0) || 'b')",
		"\"unknown\" newline":      "'a\nb'",
		"\"unknown\" bytes":        "X'deadbeef'",
		"\"unknown\" bool":         "TRUE",
		"\"unknown\" number":       "42",
		"\"unknown\" nil":          "NULL",
		"\"\" quote":               "'it''s'",
		"\"\" backslash":           "'a\\b'",
		"\"\" nul":                 "('a' || chr(0) || 'b')",
		"\"\" newline":             "'a\nb'",
		"\"\" bytes":               "X'deadbeef'",
		"\"\" bool":                "TRUE",
		"\"\" number":              "42",
		"\"\" nil":                 "NULL",
	}, func(add func(key, got string)) {
		for _, e := range append(slices.Clone(pinEngines), "") {
			for _, v := range values {
				add(fmt.Sprintf("%q %s", e, v.name), Literal(e, v.v))
			}
		}
	})
}

// How a value of a file goes into an INSERT: booleans as numbers on MySQL
// and SQLite, a time with an offset as UTC on ClickHouse.
func TestPinInsertValue(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 30, 0, 0, time.FixedZone("x", 2*3600))
	pinValues(t, "insert value", map[string]string{
		"postgres boolean":        "\"true\"",
		"postgres boolean text":   "\"false\"",
		"postgres timestamptz":    "\"2026-10-26 12:30:00+02:00\"",
		"mysql boolean":           "\"1\"",
		"mysql boolean text":      "\"0\"",
		"mysql timestamptz":       "\"2026-10-26 12:30:00+02:00\"",
		"clickhouse boolean":      "\"true\"",
		"clickhouse boolean text": "\"false\"",
		"clickhouse timestamptz":  "\"2026-10-26 10:30:00\"",
		"sqlite boolean":          "\"1\"",
		"sqlite boolean text":     "\"0\"",
		"sqlite timestamptz":      "\"2026-10-26 12:30:00+02:00\"",
		"duckdb boolean":          "\"true\"",
		"duckdb boolean text":     "\"false\"",
		"duckdb timestamptz":      "\"2026-10-26 12:30:00+02:00\"",
		"redis boolean":           "\"true\"",
		"redis boolean text":      "\"false\"",
		"redis timestamptz":       "\"2026-10-26 12:30:00+02:00\"",
		"unknown boolean":         "\"true\"",
		"unknown boolean text":    "\"false\"",
		"unknown timestamptz":     "\"2026-10-26 12:30:00+02:00\"",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			add(string(e)+" boolean", fmt.Sprintf("%#v", InsertValue(e, true, "BOOLEAN")))
			add(string(e)+" boolean text", fmt.Sprintf("%#v", InsertValue(e, "f", "BOOLEAN")))
			add(string(e)+" timestamptz", fmt.Sprintf("%#v", InsertValue(e, at, "TIMESTAMPTZ")))
		}
	})
}

// A column type of each engine as DuckDB's, through which copies are typed.
func TestPinCanonicalType(t *testing.T) {
	types := []string{"tinyint(1)", "int unsigned", "bigint unsigned", "numeric(10,2)", "timestamp(3) with time zone", "datetime",
		"timetz", "double precision", "INTEGER", "uuid", "Nullable(String)", "LowCardinality(String)", "UInt64", "DateTime64(3)",
		"Decimal(18, 4)", "text[]", "BLOB", "jsonb", "VARCHAR(20)"}
	pinValues(t, "canonical type", map[string]string{
		"postgres tinyint(1)":                    "BIGINT",
		"postgres int unsigned":                  "BIGINT",
		"postgres bigint unsigned":               "UBIGINT",
		"postgres numeric(10,2)":                 "DECIMAL(10,2)",
		"postgres timestamp(3) with time zone":   "TIMESTAMPTZ",
		"postgres datetime":                      "TIMESTAMP",
		"postgres timetz":                        "TIME",
		"postgres double precision":              "DOUBLE",
		"postgres INTEGER":                       "BIGINT",
		"postgres uuid":                          "UUID",
		"postgres Nullable(String)":              "VARCHAR",
		"postgres LowCardinality(String)":        "VARCHAR",
		"postgres UInt64":                        "VARCHAR",
		"postgres DateTime64(3)":                 "VARCHAR",
		"postgres Decimal(18, 4)":                "DECIMAL(18,4)",
		"postgres text[]":                        "VARCHAR",
		"postgres BLOB":                          "BLOB",
		"postgres jsonb":                         "JSON",
		"postgres VARCHAR(20)":                   "VARCHAR",
		"mysql tinyint(1)":                       "BOOLEAN",
		"mysql int unsigned":                     "BIGINT",
		"mysql bigint unsigned":                  "UBIGINT",
		"mysql numeric(10,2)":                    "DECIMAL(10,2)",
		"mysql timestamp(3) with time zone":      "TIMESTAMPTZ",
		"mysql datetime":                         "TIMESTAMP",
		"mysql timetz":                           "TIME",
		"mysql double precision":                 "DOUBLE",
		"mysql INTEGER":                          "BIGINT",
		"mysql uuid":                             "UUID",
		"mysql Nullable(String)":                 "VARCHAR",
		"mysql LowCardinality(String)":           "VARCHAR",
		"mysql UInt64":                           "VARCHAR",
		"mysql DateTime64(3)":                    "VARCHAR",
		"mysql Decimal(18, 4)":                   "DECIMAL(18,4)",
		"mysql text[]":                           "VARCHAR",
		"mysql BLOB":                             "BLOB",
		"mysql jsonb":                            "JSON",
		"mysql VARCHAR(20)":                      "VARCHAR",
		"clickhouse tinyint(1)":                  "VARCHAR",
		"clickhouse int unsigned":                "VARCHAR",
		"clickhouse bigint unsigned":             "VARCHAR",
		"clickhouse numeric(10,2)":               "DECIMAL(10,2)",
		"clickhouse timestamp(3) with time zone": "VARCHAR",
		"clickhouse datetime":                    "VARCHAR",
		"clickhouse timetz":                      "VARCHAR",
		"clickhouse double precision":            "VARCHAR",
		"clickhouse INTEGER":                     "VARCHAR",
		"clickhouse uuid":                        "VARCHAR",
		"clickhouse Nullable(String)":            "VARCHAR",
		"clickhouse LowCardinality(String)":      "VARCHAR",
		"clickhouse UInt64":                      "UBIGINT",
		"clickhouse DateTime64(3)":               "TIMESTAMP",
		"clickhouse Decimal(18, 4)":              "DECIMAL(18,4)",
		"clickhouse text[]":                      "VARCHAR",
		"clickhouse BLOB":                        "VARCHAR",
		"clickhouse jsonb":                       "VARCHAR",
		"clickhouse VARCHAR(20)":                 "VARCHAR",
		"sqlite tinyint(1)":                      "BIGINT",
		"sqlite int unsigned":                    "BIGINT",
		"sqlite bigint unsigned":                 "BIGINT",
		"sqlite numeric(10,2)":                   "DECIMAL(10,2)",
		"sqlite timestamp(3) with time zone":     "TIMESTAMP",
		"sqlite datetime":                        "TIMESTAMP",
		"sqlite timetz":                          "VARCHAR",
		"sqlite double precision":                "DOUBLE",
		"sqlite INTEGER":                         "BIGINT",
		"sqlite uuid":                            "VARCHAR",
		"sqlite Nullable(String)":                "VARCHAR",
		"sqlite LowCardinality(String)":          "VARCHAR",
		"sqlite UInt64":                          "BIGINT",
		"sqlite DateTime64(3)":                   "TIMESTAMP",
		"sqlite Decimal(18, 4)":                  "DECIMAL(18,4)",
		"sqlite text[]":                          "VARCHAR",
		"sqlite BLOB":                            "BLOB",
		"sqlite jsonb":                           "VARCHAR",
		"sqlite VARCHAR(20)":                     "VARCHAR",
		"duckdb tinyint(1)":                      "BIGINT",
		"duckdb int unsigned":                    "BIGINT",
		"duckdb bigint unsigned":                 "UBIGINT",
		"duckdb numeric(10,2)":                   "DECIMAL(10,2)",
		"duckdb timestamp(3) with time zone":     "TIMESTAMPTZ",
		"duckdb datetime":                        "TIMESTAMP",
		"duckdb timetz":                          "TIME",
		"duckdb double precision":                "DOUBLE",
		"duckdb INTEGER":                         "BIGINT",
		"duckdb uuid":                            "UUID",
		"duckdb Nullable(String)":                "VARCHAR",
		"duckdb LowCardinality(String)":          "VARCHAR",
		"duckdb UInt64":                          "VARCHAR",
		"duckdb DateTime64(3)":                   "VARCHAR",
		"duckdb Decimal(18, 4)":                  "DECIMAL(18,4)",
		"duckdb text[]":                          "VARCHAR",
		"duckdb BLOB":                            "BLOB",
		"duckdb jsonb":                           "JSON",
		"duckdb VARCHAR(20)":                     "VARCHAR",
		"redis tinyint(1)":                       "BIGINT",
		"redis int unsigned":                     "BIGINT",
		"redis bigint unsigned":                  "UBIGINT",
		"redis numeric(10,2)":                    "DECIMAL(10,2)",
		"redis timestamp(3) with time zone":      "TIMESTAMPTZ",
		"redis datetime":                         "TIMESTAMP",
		"redis timetz":                           "TIME",
		"redis double precision":                 "DOUBLE",
		"redis INTEGER":                          "BIGINT",
		"redis uuid":                             "UUID",
		"redis Nullable(String)":                 "VARCHAR",
		"redis LowCardinality(String)":           "VARCHAR",
		"redis UInt64":                           "VARCHAR",
		"redis DateTime64(3)":                    "VARCHAR",
		"redis Decimal(18, 4)":                   "DECIMAL(18,4)",
		"redis text[]":                           "VARCHAR",
		"redis BLOB":                             "BLOB",
		"redis jsonb":                            "JSON",
		"redis VARCHAR(20)":                      "VARCHAR",
		"unknown tinyint(1)":                     "BIGINT",
		"unknown int unsigned":                   "BIGINT",
		"unknown bigint unsigned":                "UBIGINT",
		"unknown numeric(10,2)":                  "DECIMAL(10,2)",
		"unknown timestamp(3) with time zone":    "TIMESTAMPTZ",
		"unknown datetime":                       "TIMESTAMP",
		"unknown timetz":                         "TIME",
		"unknown double precision":               "DOUBLE",
		"unknown INTEGER":                        "BIGINT",
		"unknown uuid":                           "UUID",
		"unknown Nullable(String)":               "VARCHAR",
		"unknown LowCardinality(String)":         "VARCHAR",
		"unknown UInt64":                         "VARCHAR",
		"unknown DateTime64(3)":                  "VARCHAR",
		"unknown Decimal(18, 4)":                 "DECIMAL(18,4)",
		"unknown text[]":                         "VARCHAR",
		"unknown BLOB":                           "BLOB",
		"unknown jsonb":                          "JSON",
		"unknown VARCHAR(20)":                    "VARCHAR",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			for _, typ := range types {
				add(string(e)+" "+typ, CanonicalType(e, typ))
			}
		}
	})
}

// The column type of each engine holding a DuckDB type's values: an
// engine without its own falls to DuckDB's names.
func TestPinColumnType(t *testing.T) {
	types := []string{"DECIMAL(10,2)", "DECIMAL(20,4)", "BOOLEAN", "INTEGER", "UBIGINT", "HUGEINT", "FLOAT", "DOUBLE", "DATE",
		"TIMESTAMP", "TIMESTAMPTZ", "TIME", "UUID", "JSON", "BLOB", "VARCHAR", "INTERVAL"}
	pinValues(t, "column type", map[string]string{
		"postgres DECIMAL(10,2)":          "numeric(10,2)",
		"postgres DECIMAL(20,4)":          "numeric(20,4)",
		"postgres BOOLEAN":                "boolean",
		"postgres INTEGER":                "bigint",
		"postgres UBIGINT":                "numeric(39,0)",
		"postgres HUGEINT":                "numeric(39,0)",
		"postgres FLOAT":                  "real",
		"postgres DOUBLE":                 "double precision",
		"postgres DATE":                   "date",
		"postgres TIMESTAMP":              "timestamp",
		"postgres TIMESTAMPTZ":            "timestamptz",
		"postgres TIME":                   "time",
		"postgres UUID":                   "uuid",
		"postgres JSON":                   "jsonb",
		"postgres BLOB":                   "bytea",
		"postgres VARCHAR":                "text",
		"postgres INTERVAL":               "text",
		"postgres batch of 10 columns":    "1000",
		"postgres batch of 100 columns":   "655",
		"mysql DECIMAL(10,2)":             "DECIMAL(10,2)",
		"mysql DECIMAL(20,4)":             "DECIMAL(20,4)",
		"mysql BOOLEAN":                   "BOOLEAN",
		"mysql INTEGER":                   "BIGINT",
		"mysql UBIGINT":                   "DECIMAL(39,0)",
		"mysql HUGEINT":                   "DECIMAL(39,0)",
		"mysql FLOAT":                     "FLOAT",
		"mysql DOUBLE":                    "DOUBLE",
		"mysql DATE":                      "DATE",
		"mysql TIMESTAMP":                 "DATETIME(6)",
		"mysql TIMESTAMPTZ":               "DATETIME(6)",
		"mysql TIME":                      "TIME(6)",
		"mysql UUID":                      "CHAR(36)",
		"mysql JSON":                      "JSON",
		"mysql BLOB":                      "LONGBLOB",
		"mysql VARCHAR":                   "LONGTEXT",
		"mysql INTERVAL":                  "LONGTEXT",
		"mysql batch of 10 columns":       "1000",
		"mysql batch of 100 columns":      "655",
		"clickhouse DECIMAL(10,2)":        "Decimal(10,2)",
		"clickhouse DECIMAL(20,4)":        "Decimal(20,4)",
		"clickhouse BOOLEAN":              "Bool",
		"clickhouse INTEGER":              "Int64",
		"clickhouse UBIGINT":              "Int128",
		"clickhouse HUGEINT":              "Int128",
		"clickhouse FLOAT":                "Float32",
		"clickhouse DOUBLE":               "Float64",
		"clickhouse DATE":                 "Date32",
		"clickhouse TIMESTAMP":            "DateTime64(6)",
		"clickhouse TIMESTAMPTZ":          "DateTime64(6, 'UTC')",
		"clickhouse TIME":                 "String",
		"clickhouse UUID":                 "UUID",
		"clickhouse JSON":                 "String",
		"clickhouse BLOB":                 "String",
		"clickhouse VARCHAR":              "String",
		"clickhouse INTERVAL":             "String",
		"clickhouse batch of 10 columns":  "1000",
		"clickhouse batch of 100 columns": "655",
		"sqlite DECIMAL(10,2)":            "NUMERIC",
		"sqlite DECIMAL(20,4)":            "TEXT",
		"sqlite BOOLEAN":                  "INTEGER",
		"sqlite INTEGER":                  "INTEGER",
		"sqlite UBIGINT":                  "TEXT",
		"sqlite HUGEINT":                  "TEXT",
		"sqlite FLOAT":                    "REAL",
		"sqlite DOUBLE":                   "REAL",
		"sqlite DATE":                     "TEXT",
		"sqlite TIMESTAMP":                "TEXT",
		"sqlite TIMESTAMPTZ":              "TEXT",
		"sqlite TIME":                     "TEXT",
		"sqlite UUID":                     "TEXT",
		"sqlite JSON":                     "TEXT",
		"sqlite BLOB":                     "BLOB",
		"sqlite VARCHAR":                  "TEXT",
		"sqlite INTERVAL":                 "TEXT",
		"sqlite batch of 10 columns":      "1000",
		"sqlite batch of 100 columns":     "327",
		"duckdb DECIMAL(10,2)":            "DECIMAL(10,2)",
		"duckdb DECIMAL(20,4)":            "DECIMAL(20,4)",
		"duckdb BOOLEAN":                  "BOOLEAN",
		"duckdb INTEGER":                  "BIGINT",
		"duckdb UBIGINT":                  "HUGEINT",
		"duckdb HUGEINT":                  "HUGEINT",
		"duckdb FLOAT":                    "FLOAT",
		"duckdb DOUBLE":                   "DOUBLE",
		"duckdb DATE":                     "DATE",
		"duckdb TIMESTAMP":                "TIMESTAMP",
		"duckdb TIMESTAMPTZ":              "TIMESTAMPTZ",
		"duckdb TIME":                     "TIME",
		"duckdb UUID":                     "UUID",
		"duckdb JSON":                     "JSON",
		"duckdb BLOB":                     "BLOB",
		"duckdb VARCHAR":                  "VARCHAR",
		"duckdb INTERVAL":                 "VARCHAR",
		"duckdb batch of 10 columns":      "1000",
		"duckdb batch of 100 columns":     "655",
		"redis DECIMAL(10,2)":             "DECIMAL(10,2)",
		"redis DECIMAL(20,4)":             "DECIMAL(20,4)",
		"redis BOOLEAN":                   "BOOLEAN",
		"redis INTEGER":                   "BIGINT",
		"redis UBIGINT":                   "HUGEINT",
		"redis HUGEINT":                   "HUGEINT",
		"redis FLOAT":                     "FLOAT",
		"redis DOUBLE":                    "DOUBLE",
		"redis DATE":                      "DATE",
		"redis TIMESTAMP":                 "TIMESTAMP",
		"redis TIMESTAMPTZ":               "TIMESTAMPTZ",
		"redis TIME":                      "TIME",
		"redis UUID":                      "UUID",
		"redis JSON":                      "JSON",
		"redis BLOB":                      "BLOB",
		"redis VARCHAR":                   "VARCHAR",
		"redis INTERVAL":                  "VARCHAR",
		"redis batch of 10 columns":       "1000",
		"redis batch of 100 columns":      "655",
		"unknown DECIMAL(10,2)":           "DECIMAL(10,2)",
		"unknown DECIMAL(20,4)":           "DECIMAL(20,4)",
		"unknown BOOLEAN":                 "BOOLEAN",
		"unknown INTEGER":                 "BIGINT",
		"unknown UBIGINT":                 "HUGEINT",
		"unknown HUGEINT":                 "HUGEINT",
		"unknown FLOAT":                   "FLOAT",
		"unknown DOUBLE":                  "DOUBLE",
		"unknown DATE":                    "DATE",
		"unknown TIMESTAMP":               "TIMESTAMP",
		"unknown TIMESTAMPTZ":             "TIMESTAMPTZ",
		"unknown TIME":                    "TIME",
		"unknown UUID":                    "UUID",
		"unknown JSON":                    "JSON",
		"unknown BLOB":                    "BLOB",
		"unknown VARCHAR":                 "VARCHAR",
		"unknown INTERVAL":                "VARCHAR",
		"unknown batch of 10 columns":     "1000",
		"unknown batch of 100 columns":    "655",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			for _, typ := range types {
				add(string(e)+" "+typ, ColumnType(e, typ))
			}
			add(string(e)+" batch of 10 columns", fmt.Sprint(InsertBatch(e, 10)))
			add(string(e)+" batch of 100 columns", fmt.Sprint(InsertBatch(e, 100)))
		}
	})
}

// The table a file or a copy makes, and renames.
func TestPinCreateAndRenameTable(t *testing.T) {
	cols := []NewColumn{{Name: "id", Type: "BIGINT"}, {Name: "note", Type: "Nullable(String)"}}
	pinValues(t, "create and rename", map[string]string{
		"postgres create":                     "CREATE TABLE \"s\".\"t\" (\n  \"id\" BIGINT,\n  \"note\" Nullable(String)\n)",
		"postgres rename table":               "ALTER TABLE \"s\".\"t\" RENAME TO \"u\" ok",
		"postgres rename view":                "ALTER VIEW \"s\".\"t\" RENAME TO \"u\" ok",
		"postgres rename materialized view":   "ALTER MATERIALIZED VIEW \"s\".\"t\" RENAME TO \"u\" ok",
		"mysql create":                        "CREATE TABLE `s`.`t` (\n  `id` BIGINT,\n  `note` Nullable(String)\n)",
		"mysql rename table":                  "RENAME TABLE `s`.`t` TO `s`.`u` ok",
		"mysql rename view":                   "RENAME TABLE `s`.`t` TO `s`.`u` ok",
		"mysql rename materialized view":      "RENAME TABLE `s`.`t` TO `s`.`u` ok",
		"clickhouse create":                   "CREATE TABLE `s`.`t` (\n  `id` Nullable(BIGINT),\n  `note` Nullable(String)\n) ENGINE = MergeTree ORDER BY tuple()",
		"clickhouse rename table":             "RENAME TABLE `s`.`t` TO `s`.`u` ok",
		"clickhouse rename view":              "RENAME TABLE `s`.`t` TO `s`.`u` ok",
		"clickhouse rename materialized view": "RENAME TABLE `s`.`t` TO `s`.`u` ok",
		"sqlite create":                       "CREATE TABLE \"s\".\"t\" (\n  \"id\" BIGINT,\n  \"note\" Nullable(String)\n)",
		"sqlite rename table":                 "ALTER TABLE \"s\".\"t\" RENAME TO \"u\" ok",
		"sqlite rename view":                  " error: SQLite cannot rename a view: drop it and create it again under the new name",
		"sqlite rename materialized view":     " error: SQLite cannot rename a view: drop it and create it again under the new name",
		"duckdb create":                       "CREATE TABLE \"s\".\"t\" (\n  \"id\" BIGINT,\n  \"note\" Nullable(String)\n)",
		"duckdb rename table":                 "ALTER TABLE \"s\".\"t\" RENAME TO \"u\" ok",
		"duckdb rename view":                  "ALTER VIEW \"s\".\"t\" RENAME TO \"u\" ok",
		"duckdb rename materialized view":     "ALTER MATERIALIZED VIEW \"s\".\"t\" RENAME TO \"u\" ok",
	}, func(add func(key, got string)) {
		for _, e := range sqlDialectEngines {
			d := DialectOf(e)
			add(string(e)+" create", CreateTableSQL(d, "s", "t", cols))
			for _, kind := range []ObjectKind{KindTable, KindView, KindMaterializedView} {
				sql, err := RenameObjectSQL(d, Object{Schema: "s", Name: "t", Kind: kind}, "u")
				add(fmt.Sprintf("%s rename %s", e, kind), sql+" "+errText(err))
			}
		}
	})
}

// The design of a table copied from one engine to another, or the same.
func TestPinCopyDesign(t *testing.T) {
	from := map[Engine][]Column{
		Postgres:   {{Name: "id", Type: "bigint", PrimaryKey: true}, {Name: "at", Type: "timestamp with time zone", Nullable: true}, {Name: "ok", Type: "boolean", Nullable: true}},
		MySQL:      {{Name: "id", Type: "int unsigned", PrimaryKey: true}, {Name: "ok", Type: "tinyint(1)", Nullable: true}},
		ClickHouse: {{Name: "id", Type: "UInt64", PrimaryKey: true}, {Name: "note", Type: "Nullable(String)", Nullable: true}},
		SQLite:     {{Name: "id", Type: "INTEGER", PrimaryKey: true}, {Name: "price", Type: "NUMERIC", Nullable: true}},
		DuckDB:     {{Name: "id", Type: "HUGEINT", PrimaryKey: true}, {Name: "v", Type: "VARCHAR", Nullable: true}},
	}
	pinValues(t, "copy design", map[string]string{
		"postgres to postgres":     "id bigint null=false pk=true; at timestamp with time zone null=true pk=false; ok boolean null=true pk=false",
		"postgres to mysql":        "id BIGINT null=false pk=true; at DATETIME(6) null=true pk=false; ok BOOLEAN null=true pk=false",
		"postgres to clickhouse":   "id Int64 null=false pk=true; at DateTime64(6, 'UTC') null=true pk=false; ok Bool null=true pk=false",
		"postgres to sqlite":       "id INTEGER null=false pk=true; at TEXT null=true pk=false; ok INTEGER null=true pk=false",
		"postgres to duckdb":       "id BIGINT null=false pk=true; at TIMESTAMPTZ null=true pk=false; ok BOOLEAN null=true pk=false",
		"postgres to redis":        "id BIGINT null=false pk=true; at TIMESTAMPTZ null=true pk=false; ok BOOLEAN null=true pk=false",
		"postgres to unknown":      "id BIGINT null=false pk=true; at TIMESTAMPTZ null=true pk=false; ok BOOLEAN null=true pk=false",
		"mysql to postgres":        "id bigint null=false pk=true; ok boolean null=true pk=false",
		"mysql to mysql":           "id int unsigned null=false pk=true; ok tinyint(1) null=true pk=false",
		"mysql to clickhouse":      "id Int64 null=false pk=true; ok Bool null=true pk=false",
		"mysql to sqlite":          "id INTEGER null=false pk=true; ok INTEGER null=true pk=false",
		"mysql to duckdb":          "id BIGINT null=false pk=true; ok BOOLEAN null=true pk=false",
		"mysql to redis":           "id BIGINT null=false pk=true; ok BOOLEAN null=true pk=false",
		"mysql to unknown":         "id BIGINT null=false pk=true; ok BOOLEAN null=true pk=false",
		"clickhouse to postgres":   "id numeric(39,0) null=false pk=true; note text null=true pk=false",
		"clickhouse to mysql":      "id DECIMAL(39,0) null=false pk=true; note LONGTEXT null=true pk=false",
		"clickhouse to clickhouse": "id UInt64 null=false pk=true; note String null=true pk=false",
		"clickhouse to sqlite":     "id TEXT null=false pk=true; note TEXT null=true pk=false",
		"clickhouse to duckdb":     "id HUGEINT null=false pk=true; note VARCHAR null=true pk=false",
		"clickhouse to redis":      "id HUGEINT null=false pk=true; note VARCHAR null=true pk=false",
		"clickhouse to unknown":    "id HUGEINT null=false pk=true; note VARCHAR null=true pk=false",
		"sqlite to postgres":       "id bigint null=false pk=true; price text null=true pk=false",
		"sqlite to mysql":          "id BIGINT null=false pk=true; price LONGTEXT null=true pk=false",
		"sqlite to clickhouse":     "id Int64 null=false pk=true; price String null=true pk=false",
		"sqlite to sqlite":         "id INTEGER null=false pk=true; price NUMERIC null=true pk=false",
		"sqlite to duckdb":         "id BIGINT null=false pk=true; price VARCHAR null=true pk=false",
		"sqlite to redis":          "id BIGINT null=false pk=true; price VARCHAR null=true pk=false",
		"sqlite to unknown":        "id BIGINT null=false pk=true; price VARCHAR null=true pk=false",
		"duckdb to postgres":       "id bigint null=false pk=true; v text null=true pk=false",
		"duckdb to mysql":          "id BIGINT null=false pk=true; v LONGTEXT null=true pk=false",
		"duckdb to clickhouse":     "id Int64 null=false pk=true; v String null=true pk=false",
		"duckdb to sqlite":         "id INTEGER null=false pk=true; v TEXT null=true pk=false",
		"duckdb to duckdb":         "id HUGEINT null=false pk=true; v VARCHAR null=true pk=false",
		"duckdb to redis":          "id BIGINT null=false pk=true; v VARCHAR null=true pk=false",
		"duckdb to unknown":        "id BIGINT null=false pk=true; v VARCHAR null=true pk=false",
	}, func(add func(key, got string)) {
		for _, f := range sqlDialectEngines {
			for _, to := range pinEngines {
				d := CopyDesign(f, from[f], to, "s", "t")
				var cols []string
				for _, c := range d.Columns {
					cols = append(cols, fmt.Sprintf("%s %s null=%v pk=%v", c.Name, c.Type, c.Nullable, c.PrimaryKey))
				}
				add(fmt.Sprintf("%s to %s", f, to), strings.Join(cols, "; "))
			}
		}
	})
}

// designOf is a table with a bit of everything a design writes.
func designOf() TableDesign {
	return TableDesign{Schema: "s", Name: "orders", Comment: "the orders",
		Columns: []ColumnDesign{
			{Name: "id", Type: "bigint", PrimaryKey: true},
			{Name: "customer", Type: "bigint", Nullable: true, Comment: "who"},
			{Name: "total", Type: "numeric(12,2)", Default: "0"},
			{Name: "status", Type: "text", Extra: "COLLATE utf8mb4_bin"},
		},
		Indexes: []IndexDesign{
			{Name: "orders_status", Columns: []string{"status"}},
			{Name: "orders_customer_status", Columns: []string{"customer", "status"}, Unique: true, Constraint: true},
		},
		ForeignKeys: []ForeignKeyDesign{{Name: "orders_customer", Columns: []string{"customer"}, RefSchema: "s", RefTable: "customers",
			RefColumns: []string{"id"}, OnDelete: "CASCADE"}},
		Checks: []CheckDesign{{Name: "orders_total", Expression: "total >= 0"}},
	}
}

// The statements making a designed table.
func TestPinNewTableChange(t *testing.T) {
	plain := TableDesign{Schema: "s", Name: "t", Columns: []ColumnDesign{{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
		{Name: "v", Type: "text", Nullable: true}}}
	designs := map[string]TableDesign{"rich": designOf(), "auto key": plain}
	pinValues(t, "new table", map[string]string{
		"postgres rich":                 "atomic=true ok\nCREATE TABLE \"s\".\"orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" text NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"s\".\"customers\" (\"id\") ON DELETE CASCADE,\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  CONSTRAINT \"orders_customer_status\" UNIQUE (\"customer\", \"status\")\n);\nCREATE INDEX \"orders_status\" ON \"s\".\"orders\" (\"status\");\nCOMMENT ON TABLE \"s\".\"orders\" IS 'the orders';\nCOMMENT ON COLUMN \"s\".\"orders\".\"customer\" IS 'who';",
		"postgres auto key":             "atomic=true ok\nCREATE TABLE \"s\".\"t\" (\n  \"id\" INTEGER NOT NULL GENERATED BY DEFAULT AS IDENTITY,\n  \"v\" text,\n  PRIMARY KEY (\"id\")\n);",
		"postgres rich with comments":   "ok steps=4",
		"postgres add key":              "ALTER TABLE \"s\".\"orders\" ADD CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"s\".\"customers\" (\"id\") ON DELETE CASCADE",
		"mysql rich":                    "atomic=false ok\nCREATE TABLE `s`.`orders` (\n  `id` bigint NOT NULL,\n  `customer` bigint COMMENT 'who',\n  `total` numeric(12,2) NOT NULL DEFAULT 0,\n  `status` text COLLATE utf8mb4_bin NOT NULL,\n  PRIMARY KEY (`id`),\n  CONSTRAINT `orders_customer` FOREIGN KEY (`customer`) REFERENCES `s`.`customers` (`id`) ON DELETE CASCADE,\n  CONSTRAINT `orders_total` CHECK (total >= 0),\n  CONSTRAINT `orders_customer_status` UNIQUE (`customer`, `status`)\n) COMMENT = 'the orders';\nCREATE INDEX `orders_status` ON `s`.`orders` (`status`);",
		"mysql auto key":                "atomic=false ok\nCREATE TABLE `s`.`t` (\n  `id` INTEGER NOT NULL AUTO_INCREMENT,\n  `v` text,\n  PRIMARY KEY (`id`)\n);",
		"mysql rich with comments":      "ok steps=2",
		"mysql add key":                 "ALTER TABLE `s`.`orders` ADD CONSTRAINT `orders_customer` FOREIGN KEY (`customer`) REFERENCES `s`.`customers` (`id`) ON DELETE CASCADE",
		"clickhouse rich":               "atomic=false ok\nCREATE TABLE `s`.`orders` (\n  `id` bigint,\n  `customer` Nullable(bigint) COMMENT 'who',\n  `total` numeric(12,2) DEFAULT 0,\n  `status` text,\n  CONSTRAINT `orders_customer` FOREIGN KEY (`customer`) REFERENCES `s`.`customers` (`id`) ON DELETE CASCADE,\n  CONSTRAINT `orders_total` CHECK (total >= 0),\n  CONSTRAINT `orders_customer_status` UNIQUE (`customer`, `status`)\n) ENGINE = MergeTree ORDER BY (`id`) COMMENT 'the orders';\nCREATE INDEX `orders_status` ON `s`.`orders` (`status`);",
		"clickhouse auto key":           "atomic=false error: ClickHouse numbers no rows of its own: use a sequence\n",
		"clickhouse rich with comments": "ok steps=2",
		"clickhouse add key":            "ALTER TABLE `s`.`orders` ADD CONSTRAINT `orders_customer` FOREIGN KEY (`customer`) REFERENCES `s`.`customers` (`id`) ON DELETE CASCADE",
		"sqlite rich":                   "atomic=true ok\nCREATE TABLE \"s\".\"orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" text NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE,\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  UNIQUE (\"customer\", \"status\")\n);\nCREATE INDEX \"s\".\"orders_status\" ON \"orders\" (\"status\");",
		"sqlite auto key":               "atomic=true ok\nCREATE TABLE \"s\".\"t\" (\n  \"id\" INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,\n  \"v\" text\n);",
		"sqlite rich with comments":     "error: SQLite keeps no comments steps=0",
		"sqlite add key":                "ALTER TABLE \"s\".\"orders\" ADD CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE",
		"duckdb rich":                   "atomic=true ok\nCREATE TABLE \"s\".\"orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" text NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"s\".\"customers\" (\"id\") ON DELETE CASCADE,\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  CONSTRAINT \"orders_customer_status\" UNIQUE (\"customer\", \"status\")\n);\nCREATE INDEX \"orders_status\" ON \"s\".\"orders\" (\"status\");\nCOMMENT ON TABLE \"s\".\"orders\" IS 'the orders';\nCOMMENT ON COLUMN \"s\".\"orders\".\"customer\" IS 'who';",
		"duckdb auto key":               "atomic=false error: DuckDB numbers no rows of its own: use a sequence\n",
		"duckdb rich with comments":     "ok steps=4",
		"duckdb add key":                "ALTER TABLE \"s\".\"orders\" ADD CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"s\".\"customers\" (\"id\") ON DELETE CASCADE",
	}, func(add func(key, got string)) {
		for _, e := range sqlDialectEngines {
			for _, name := range []string{"rich", "auto key"} {
				t := designs[name]
				if e == SQLite && name == "rich" {
					t.Comment = ""
					t.Columns = slices.Clone(t.Columns)
					t.Columns[1].Comment = ""
				}
				ch, err := NewTableChange(DialectOf(e), t)
				add(string(e)+" "+name, fmt.Sprintf("atomic=%v %s\n%s", ch.Atomic, errText(err), ch.Text()))
			}
			ch, err := NewTableChange(DialectOf(e), designOf())
			add(string(e)+" rich with comments", errText(err)+" steps="+fmt.Sprint(len(ch.Steps)))
			add(string(e)+" add key", AddForeignKeySQL(DialectOf(e), designOf(), designOf().ForeignKeys[0]))
		}
	})
}

// readDesign is designOf as a table read: its columns, indexes, keys and
// checks marked read.
func readDesign() TableDesign {
	t := designOf()
	t.PrimaryKeyName = "orders_pkey"
	for i := range t.Columns {
		t.Columns[i].Was = t.Columns[i].Name
	}
	for i := range t.Indexes {
		t.Indexes[i].Read = true
	}
	for i := range t.ForeignKeys {
		t.ForeignKeys[i].Read = true
	}
	for i := range t.Checks {
		t.Checks[i].Read = true
	}
	return t
}

// The statements changing a table read into what the form made of it.
func TestPinAlterTableChange(t *testing.T) {
	changes := map[string]func(t *TableDesign){
		"rename column": func(t *TableDesign) { t.Columns[3].Name = "state" },
		"change type":   func(t *TableDesign) { t.Columns[3].Type = "varchar(20)" },
		"type to int":   func(t *TableDesign) { t.Columns[3].Type = "integer" },
		"set default":   func(t *TableDesign) { t.Columns[3].Default = "'new'" },
		"drop default":  func(t *TableDesign) { t.Columns[2].Default = "" },
		"nullable":      func(t *TableDesign) { t.Columns[2].Nullable = true },
		"not null":      func(t *TableDesign) { t.Columns[1].Nullable = false },
		"column comment": func(t *TableDesign) {
			t.Columns[1].Comment = "the customer"
		},
		"add column": func(t *TableDesign) {
			t.Columns = append(t.Columns, ColumnDesign{Name: "note", Type: "text", Nullable: true})
		},
		"drop column": func(t *TableDesign) { t.Columns = t.Columns[:3] },
		"drop index":  func(t *TableDesign) { t.Indexes = t.Indexes[1:] },
		"drop unique": func(t *TableDesign) { t.Indexes = t.Indexes[:1] },
		"add index": func(t *TableDesign) {
			t.Indexes = append(t.Indexes, IndexDesign{Name: "orders_total", Columns: []string{"total"}})
		},
		"add unique": func(t *TableDesign) {
			t.Indexes = append(t.Indexes, IndexDesign{Name: "orders_u", Columns: []string{"total"}, Constraint: true})
		},
		"drop key": func(t *TableDesign) { t.ForeignKeys = nil },
		"add key": func(t *TableDesign) {
			t.ForeignKeys = append(t.ForeignKeys, ForeignKeyDesign{Columns: []string{"total"}, RefTable: "x", RefColumns: []string{"id"}})
		},
		"drop check":    func(t *TableDesign) { t.Checks = nil },
		"add check":     func(t *TableDesign) { t.Checks = append(t.Checks, CheckDesign{Name: "c2", Expression: "id > 0"}) },
		"primary key":   func(t *TableDesign) { t.Columns[1].PrimaryKey, t.Columns[1].Nullable = true, false },
		"table comment": func(t *TableDesign) { t.Comment = "all orders" },
		"drop comment":  func(t *TableDesign) { t.Comment = "" },
		"rename table":  func(t *TableDesign) { t.Name = "purchases" },
	}
	names := make([]string, 0, len(changes))
	for n := range changes {
		names = append(names, n)
	}
	slices.Sort(names)
	pinValues(t, "alter table", map[string]string{
		"postgres add check":        "atomic=true ok\nALTER TABLE \"s\".\"orders\" ADD CONSTRAINT \"c2\" CHECK (id > 0);",
		"postgres add column":       "atomic=true ok\nALTER TABLE \"s\".\"orders\" ADD COLUMN \"note\" text;",
		"postgres add index":        "atomic=true ok\nCREATE INDEX \"orders_total\" ON \"s\".\"orders\" (\"total\");",
		"postgres add key":          "atomic=true ok\nALTER TABLE \"s\".\"orders\" ADD FOREIGN KEY (\"total\") REFERENCES \"x\" (\"id\");",
		"postgres add unique":       "atomic=true ok\nALTER TABLE \"s\".\"orders\" ADD CONSTRAINT \"orders_u\" UNIQUE (\"total\");",
		"postgres change type":      "atomic=true ok\nALTER TABLE \"s\".\"orders\" ALTER COLUMN \"status\" TYPE varchar(20);",
		"postgres column comment":   "atomic=true ok\nCOMMENT ON COLUMN \"s\".\"orders\".\"customer\" IS 'the customer';",
		"postgres drop check":       "atomic=true ok\nALTER TABLE \"s\".\"orders\" DROP CONSTRAINT \"orders_total\";",
		"postgres drop column":      "atomic=true ok\nALTER TABLE \"s\".\"orders\" DROP COLUMN \"status\";",
		"postgres drop comment":     "atomic=true ok\nCOMMENT ON TABLE \"s\".\"orders\" IS NULL;",
		"postgres drop default":     "atomic=true ok\nALTER TABLE \"s\".\"orders\" ALTER COLUMN \"total\" DROP DEFAULT;",
		"postgres drop index":       "atomic=true ok\nDROP INDEX \"s\".\"orders_status\";",
		"postgres drop key":         "atomic=true ok\nALTER TABLE \"s\".\"orders\" DROP CONSTRAINT \"orders_customer\";",
		"postgres drop unique":      "atomic=true ok\nALTER TABLE \"s\".\"orders\" DROP CONSTRAINT \"orders_customer_status\";",
		"postgres not null":         "atomic=true ok\nALTER TABLE \"s\".\"orders\" ALTER COLUMN \"customer\" SET NOT NULL;",
		"postgres nullable":         "atomic=true ok\nALTER TABLE \"s\".\"orders\" ALTER COLUMN \"total\" DROP NOT NULL;",
		"postgres primary key":      "atomic=true ok\nALTER TABLE \"s\".\"orders\" DROP CONSTRAINT \"orders_pkey\";\nALTER TABLE \"s\".\"orders\" ALTER COLUMN \"customer\" SET NOT NULL;\nALTER TABLE \"s\".\"orders\" ADD PRIMARY KEY (\"id\", \"customer\");",
		"postgres rename column":    "atomic=true ok\nALTER TABLE \"s\".\"orders\" RENAME COLUMN \"status\" TO \"state\";",
		"postgres rename table":     "atomic=true ok\nALTER TABLE \"s\".\"orders\" RENAME TO \"purchases\";",
		"postgres set default":      "atomic=true ok\nALTER TABLE \"s\".\"orders\" ALTER COLUMN \"status\" SET DEFAULT 'new';",
		"postgres table comment":    "atomic=true ok\nCOMMENT ON TABLE \"s\".\"orders\" IS 'all orders';",
		"postgres type to int":      "atomic=true ok\nALTER TABLE \"s\".\"orders\" ALTER COLUMN \"status\" TYPE integer USING \"status\"::integer;",
		"mysql add check":           "atomic=false ok\nALTER TABLE `s`.`orders` ADD CONSTRAINT `c2` CHECK (id > 0);",
		"mysql add column":          "atomic=false ok\nALTER TABLE `s`.`orders` ADD COLUMN `note` text;",
		"mysql add index":           "atomic=false ok\nCREATE INDEX `orders_total` ON `s`.`orders` (`total`);",
		"mysql add key":             "atomic=false ok\nALTER TABLE `s`.`orders` ADD FOREIGN KEY (`total`) REFERENCES `x` (`id`);",
		"mysql add unique":          "atomic=false ok\nALTER TABLE `s`.`orders` ADD CONSTRAINT `orders_u` UNIQUE (`total`);",
		"mysql change type":         "atomic=false ok\nALTER TABLE `s`.`orders` MODIFY COLUMN `status` varchar(20) COLLATE utf8mb4_bin NOT NULL;",
		"mysql column comment":      "atomic=false ok\nALTER TABLE `s`.`orders` MODIFY COLUMN `customer` bigint COMMENT 'the customer';",
		"mysql drop check":          "atomic=false ok\nALTER TABLE `s`.`orders` DROP CHECK `orders_total`;",
		"mysql drop column":         "atomic=false ok\nALTER TABLE `s`.`orders` DROP COLUMN `status`;",
		"mysql drop comment":        "atomic=false ok\nALTER TABLE `s`.`orders` COMMENT = '';",
		"mysql drop default":        "atomic=false ok\nALTER TABLE `s`.`orders` ALTER COLUMN `total` DROP DEFAULT;",
		"mysql drop index":          "atomic=false ok\nDROP INDEX `orders_status` ON `s`.`orders`;",
		"mysql drop key":            "atomic=false ok\nALTER TABLE `s`.`orders` DROP FOREIGN KEY `orders_customer`;",
		"mysql drop unique":         "atomic=false ok\nALTER TABLE `s`.`orders` DROP CONSTRAINT `orders_customer_status`;",
		"mysql not null":            "atomic=false ok\nALTER TABLE `s`.`orders` MODIFY COLUMN `customer` bigint NOT NULL COMMENT 'who';",
		"mysql nullable":            "atomic=false ok\nALTER TABLE `s`.`orders` MODIFY COLUMN `total` numeric(12,2) DEFAULT 0;",
		"mysql primary key":         "atomic=false ok\nALTER TABLE `s`.`orders` DROP PRIMARY KEY;\nALTER TABLE `s`.`orders` MODIFY COLUMN `customer` bigint NOT NULL COMMENT 'who';\nALTER TABLE `s`.`orders` ADD PRIMARY KEY (`id`, `customer`);",
		"mysql rename column":       "atomic=false ok\nALTER TABLE `s`.`orders` RENAME COLUMN `status` TO `state`;",
		"mysql rename table":        "atomic=false ok\nRENAME TABLE `s`.`orders` TO `s`.`purchases`;",
		"mysql set default":         "atomic=false ok\nALTER TABLE `s`.`orders` ALTER COLUMN `status` SET DEFAULT 'new';",
		"mysql table comment":       "atomic=false ok\nALTER TABLE `s`.`orders` COMMENT = 'all orders';",
		"mysql type to int":         "atomic=false ok\nALTER TABLE `s`.`orders` MODIFY COLUMN `status` integer COLLATE utf8mb4_bin NOT NULL;",
		"clickhouse add check":      "atomic=false error: ClickHouse cannot change a table's key, indexes or constraints here: its sorting key is fixed when the table is made\n",
		"clickhouse add column":     "atomic=false ok\nALTER TABLE `s`.`orders` ADD COLUMN `note` Nullable(text);",
		"clickhouse add index":      "atomic=false error: ClickHouse cannot change a table's key, indexes or constraints here: its sorting key is fixed when the table is made\n",
		"clickhouse add key":        "atomic=false error: ClickHouse cannot change a table's key, indexes or constraints here: its sorting key is fixed when the table is made\n",
		"clickhouse add unique":     "atomic=false error: ClickHouse cannot change a table's key, indexes or constraints here: its sorting key is fixed when the table is made\n",
		"clickhouse change type":    "atomic=false ok\nALTER TABLE `s`.`orders` MODIFY COLUMN `status` varchar(20);",
		"clickhouse column comment": "atomic=false ok\nALTER TABLE `s`.`orders` COMMENT COLUMN `customer` 'the customer';",
		"clickhouse drop check":     "atomic=false error: ClickHouse cannot change a table's key, indexes or constraints here: its sorting key is fixed when the table is made\n",
		"clickhouse drop column":    "atomic=false ok\nALTER TABLE `s`.`orders` DROP COLUMN `status`;",
		"clickhouse drop comment":   "atomic=false ok\nALTER TABLE `s`.`orders` MODIFY COMMENT '';",
		"clickhouse drop default":   "atomic=false ok\nALTER TABLE `s`.`orders` MODIFY COLUMN `total` REMOVE DEFAULT;",
		"clickhouse drop index":     "atomic=false error: ClickHouse cannot change a table's key, indexes or constraints here: its sorting key is fixed when the table is made\n",
		"clickhouse drop key":       "atomic=false error: ClickHouse cannot change a table's key, indexes or constraints here: its sorting key is fixed when the table is made\n",
		"clickhouse drop unique":    "atomic=false error: ClickHouse cannot change a table's key, indexes or constraints here: its sorting key is fixed when the table is made\n",
		"clickhouse not null":       "atomic=false ok\nALTER TABLE `s`.`orders` MODIFY COLUMN `customer` bigint;",
		"clickhouse nullable":       "atomic=false ok\nALTER TABLE `s`.`orders` MODIFY COLUMN `total` Nullable(numeric(12,2));",
		"clickhouse primary key":    "atomic=false error: ClickHouse cannot change a table's key, indexes or constraints here: its sorting key is fixed when the table is made\n",
		"clickhouse rename column":  "atomic=false ok\nALTER TABLE `s`.`orders` RENAME COLUMN `status` TO `state`;",
		"clickhouse rename table":   "atomic=false ok\nRENAME TABLE `s`.`orders` TO `s`.`purchases`;",
		"clickhouse set default":    "atomic=false ok\nALTER TABLE `s`.`orders` MODIFY COLUMN `status` DEFAULT 'new';",
		"clickhouse table comment":  "atomic=false ok\nALTER TABLE `s`.`orders` MODIFY COMMENT 'all orders';",
		"clickhouse type to int":    "atomic=false ok\nALTER TABLE `s`.`orders` MODIFY COLUMN `status` integer;",
		"sqlite add check":          "atomic=true ok\n-- SQLite cannot make this change in place: orders is made again.\nCREATE TABLE \"s\".\"dgopher_new_orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" text NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE,\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  CONSTRAINT \"c2\" CHECK (id > 0),\n  UNIQUE (\"customer\", \"status\")\n);\nINSERT INTO \"s\".\"dgopher_new_orders\" (\"id\", \"customer\", \"total\", \"status\") SELECT \"id\", \"customer\", \"total\", \"status\" FROM \"s\".\"orders\";\nDROP TABLE \"s\".\"orders\";\nALTER TABLE \"s\".\"dgopher_new_orders\" RENAME TO \"orders\";\n-- Then its indexes and triggers are created again.",
		"sqlite add column":         "atomic=true ok\nALTER TABLE \"s\".\"orders\" ADD COLUMN \"note\" text;",
		"sqlite add index":          "atomic=true ok\nCREATE INDEX \"s\".\"orders_total\" ON \"orders\" (\"total\");",
		"sqlite add key":            "atomic=true ok\n-- SQLite cannot make this change in place: orders is made again.\nCREATE TABLE \"s\".\"dgopher_new_orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" text NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE,\n  FOREIGN KEY (\"total\") REFERENCES \"x\" (\"id\"),\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  UNIQUE (\"customer\", \"status\")\n);\nINSERT INTO \"s\".\"dgopher_new_orders\" (\"id\", \"customer\", \"total\", \"status\") SELECT \"id\", \"customer\", \"total\", \"status\" FROM \"s\".\"orders\";\nDROP TABLE \"s\".\"orders\";\nALTER TABLE \"s\".\"dgopher_new_orders\" RENAME TO \"orders\";\n-- Then its indexes and triggers are created again.",
		"sqlite add unique":         "atomic=true ok\n-- SQLite cannot make this change in place: orders is made again.\nCREATE TABLE \"s\".\"dgopher_new_orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" text NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE,\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  UNIQUE (\"customer\", \"status\"),\n  UNIQUE (\"total\")\n);\nINSERT INTO \"s\".\"dgopher_new_orders\" (\"id\", \"customer\", \"total\", \"status\") SELECT \"id\", \"customer\", \"total\", \"status\" FROM \"s\".\"orders\";\nDROP TABLE \"s\".\"orders\";\nALTER TABLE \"s\".\"dgopher_new_orders\" RENAME TO \"orders\";\n-- Then its indexes and triggers are created again.",
		"sqlite change type":        "atomic=true ok\n-- SQLite cannot make this change in place: orders is made again.\nCREATE TABLE \"s\".\"dgopher_new_orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" varchar(20) NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE,\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  UNIQUE (\"customer\", \"status\")\n);\nINSERT INTO \"s\".\"dgopher_new_orders\" (\"id\", \"customer\", \"total\", \"status\") SELECT \"id\", \"customer\", \"total\", \"status\" FROM \"s\".\"orders\";\nDROP TABLE \"s\".\"orders\";\nALTER TABLE \"s\".\"dgopher_new_orders\" RENAME TO \"orders\";\n-- Then its indexes and triggers are created again.",
		"sqlite drop check":         "atomic=true ok\n-- SQLite cannot make this change in place: orders is made again.\nCREATE TABLE \"s\".\"dgopher_new_orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" text NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE,\n  UNIQUE (\"customer\", \"status\")\n);\nINSERT INTO \"s\".\"dgopher_new_orders\" (\"id\", \"customer\", \"total\", \"status\") SELECT \"id\", \"customer\", \"total\", \"status\" FROM \"s\".\"orders\";\nDROP TABLE \"s\".\"orders\";\nALTER TABLE \"s\".\"dgopher_new_orders\" RENAME TO \"orders\";\n-- Then its indexes and triggers are created again.",
		"sqlite drop column":        "atomic=false error: the index orders_status uses status: drop it too\n",
		"sqlite drop default":       "atomic=true ok\n-- SQLite cannot make this change in place: orders is made again.\nCREATE TABLE \"s\".\"dgopher_new_orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) NOT NULL,\n  \"status\" text NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE,\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  UNIQUE (\"customer\", \"status\")\n);\nINSERT INTO \"s\".\"dgopher_new_orders\" (\"id\", \"customer\", \"total\", \"status\") SELECT \"id\", \"customer\", \"total\", \"status\" FROM \"s\".\"orders\";\nDROP TABLE \"s\".\"orders\";\nALTER TABLE \"s\".\"dgopher_new_orders\" RENAME TO \"orders\";\n-- Then its indexes and triggers are created again.",
		"sqlite drop index":         "atomic=true ok\nDROP INDEX \"s\".\"orders_status\";",
		"sqlite drop key":           "atomic=true ok\n-- SQLite cannot make this change in place: orders is made again.\nCREATE TABLE \"s\".\"dgopher_new_orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" text NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  UNIQUE (\"customer\", \"status\")\n);\nINSERT INTO \"s\".\"dgopher_new_orders\" (\"id\", \"customer\", \"total\", \"status\") SELECT \"id\", \"customer\", \"total\", \"status\" FROM \"s\".\"orders\";\nDROP TABLE \"s\".\"orders\";\nALTER TABLE \"s\".\"dgopher_new_orders\" RENAME TO \"orders\";\n-- Then its indexes and triggers are created again.",
		"sqlite drop unique":        "atomic=true ok\n-- SQLite cannot make this change in place: orders is made again.\nCREATE TABLE \"s\".\"dgopher_new_orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" text NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE,\n  CONSTRAINT \"orders_total\" CHECK (total >= 0)\n);\nINSERT INTO \"s\".\"dgopher_new_orders\" (\"id\", \"customer\", \"total\", \"status\") SELECT \"id\", \"customer\", \"total\", \"status\" FROM \"s\".\"orders\";\nDROP TABLE \"s\".\"orders\";\nALTER TABLE \"s\".\"dgopher_new_orders\" RENAME TO \"orders\";\n-- Then its indexes and triggers are created again.",
		"sqlite not null":           "atomic=true ok\n-- SQLite cannot make this change in place: orders is made again.\nCREATE TABLE \"s\".\"dgopher_new_orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint NOT NULL,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" text NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE,\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  UNIQUE (\"customer\", \"status\")\n);\nINSERT INTO \"s\".\"dgopher_new_orders\" (\"id\", \"customer\", \"total\", \"status\") SELECT \"id\", \"customer\", \"total\", \"status\" FROM \"s\".\"orders\";\nDROP TABLE \"s\".\"orders\";\nALTER TABLE \"s\".\"dgopher_new_orders\" RENAME TO \"orders\";\n-- Then its indexes and triggers are created again.",
		"sqlite nullable":           "atomic=true ok\n-- SQLite cannot make this change in place: orders is made again.\nCREATE TABLE \"s\".\"dgopher_new_orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) DEFAULT 0,\n  \"status\" text NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE,\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  UNIQUE (\"customer\", \"status\")\n);\nINSERT INTO \"s\".\"dgopher_new_orders\" (\"id\", \"customer\", \"total\", \"status\") SELECT \"id\", \"customer\", \"total\", \"status\" FROM \"s\".\"orders\";\nDROP TABLE \"s\".\"orders\";\nALTER TABLE \"s\".\"dgopher_new_orders\" RENAME TO \"orders\";\n-- Then its indexes and triggers are created again.",
		"sqlite primary key":        "atomic=true ok\n-- SQLite cannot make this change in place: orders is made again.\nCREATE TABLE \"s\".\"dgopher_new_orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint NOT NULL,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" text NOT NULL,\n  PRIMARY KEY (\"id\", \"customer\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE,\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  UNIQUE (\"customer\", \"status\")\n);\nINSERT INTO \"s\".\"dgopher_new_orders\" (\"id\", \"customer\", \"total\", \"status\") SELECT \"id\", \"customer\", \"total\", \"status\" FROM \"s\".\"orders\";\nDROP TABLE \"s\".\"orders\";\nALTER TABLE \"s\".\"dgopher_new_orders\" RENAME TO \"orders\";\n-- Then its indexes and triggers are created again.",
		"sqlite rename column":      "atomic=true ok\nALTER TABLE \"s\".\"orders\" RENAME COLUMN \"status\" TO \"state\";",
		"sqlite rename table":       "atomic=true ok\nALTER TABLE \"s\".\"orders\" RENAME TO \"purchases\";",
		"sqlite set default":        "atomic=true ok\n-- SQLite cannot make this change in place: orders is made again.\nCREATE TABLE \"s\".\"dgopher_new_orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" text NOT NULL DEFAULT 'new',\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE,\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  UNIQUE (\"customer\", \"status\")\n);\nINSERT INTO \"s\".\"dgopher_new_orders\" (\"id\", \"customer\", \"total\", \"status\") SELECT \"id\", \"customer\", \"total\", \"status\" FROM \"s\".\"orders\";\nDROP TABLE \"s\".\"orders\";\nALTER TABLE \"s\".\"dgopher_new_orders\" RENAME TO \"orders\";\n-- Then its indexes and triggers are created again.",
		"sqlite type to int":        "atomic=true ok\n-- SQLite cannot make this change in place: orders is made again.\nCREATE TABLE \"s\".\"dgopher_new_orders\" (\n  \"id\" bigint NOT NULL,\n  \"customer\" bigint,\n  \"total\" numeric(12,2) NOT NULL DEFAULT 0,\n  \"status\" integer NOT NULL,\n  PRIMARY KEY (\"id\"),\n  CONSTRAINT \"orders_customer\" FOREIGN KEY (\"customer\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE,\n  CONSTRAINT \"orders_total\" CHECK (total >= 0),\n  UNIQUE (\"customer\", \"status\")\n);\nINSERT INTO \"s\".\"dgopher_new_orders\" (\"id\", \"customer\", \"total\", \"status\") SELECT \"id\", \"customer\", \"total\", \"status\" FROM \"s\".\"orders\";\nDROP TABLE \"s\".\"orders\";\nALTER TABLE \"s\".\"dgopher_new_orders\" RENAME TO \"orders\";\n-- Then its indexes and triggers are created again.",
		"duckdb add check":          "atomic=false error: DuckDB cannot change the keys or checks of a table it made: make the table again\n",
		"duckdb add column":         "atomic=true ok\nALTER TABLE \"s\".\"orders\" ADD COLUMN \"note\" text;",
		"duckdb add index":          "atomic=true ok\nCREATE INDEX \"orders_total\" ON \"s\".\"orders\" (\"total\");",
		"duckdb add key":            "atomic=false error: DuckDB cannot change the keys or checks of a table it made: make the table again\n",
		"duckdb add unique":         "atomic=true ok\nCREATE INDEX \"orders_u\" ON \"s\".\"orders\" (\"total\");",
		"duckdb change type":        "atomic=true ok\nALTER TABLE \"s\".\"orders\" ALTER COLUMN \"status\" TYPE varchar(20) USING \"status\"::varchar(20);",
		"duckdb column comment":     "atomic=true ok\nCOMMENT ON COLUMN \"s\".\"orders\".\"customer\" IS 'the customer';",
		"duckdb drop check":         "atomic=false error: DuckDB cannot change the keys or checks of a table it made: make the table again\n",
		"duckdb drop column":        "atomic=true ok\nALTER TABLE \"s\".\"orders\" DROP COLUMN \"status\";",
		"duckdb drop comment":       "atomic=true ok\nCOMMENT ON TABLE \"s\".\"orders\" IS NULL;",
		"duckdb drop default":       "atomic=true ok\nALTER TABLE \"s\".\"orders\" ALTER COLUMN \"total\" DROP DEFAULT;",
		"duckdb drop index":         "atomic=true ok\nDROP INDEX \"s\".\"orders_status\";",
		"duckdb drop key":           "atomic=false error: DuckDB cannot change the keys or checks of a table it made: make the table again\n",
		"duckdb drop unique":        "atomic=true ok\nALTER TABLE \"s\".\"orders\" DROP CONSTRAINT \"orders_customer_status\";",
		"duckdb not null":           "atomic=true ok\nALTER TABLE \"s\".\"orders\" ALTER COLUMN \"customer\" SET NOT NULL;",
		"duckdb nullable":           "atomic=true ok\nALTER TABLE \"s\".\"orders\" ALTER COLUMN \"total\" DROP NOT NULL;",
		"duckdb primary key":        "atomic=false error: DuckDB cannot change the keys or checks of a table it made: make the table again\n",
		"duckdb rename column":      "atomic=true ok\nALTER TABLE \"s\".\"orders\" RENAME COLUMN \"status\" TO \"state\";",
		"duckdb rename table":       "atomic=true ok\nALTER TABLE \"s\".\"orders\" RENAME TO \"purchases\";",
		"duckdb set default":        "atomic=true ok\nALTER TABLE \"s\".\"orders\" ALTER COLUMN \"status\" SET DEFAULT 'new';",
		"duckdb table comment":      "atomic=true ok\nCOMMENT ON TABLE \"s\".\"orders\" IS 'all orders';",
		"duckdb type to int":        "atomic=true ok\nALTER TABLE \"s\".\"orders\" ALTER COLUMN \"status\" TYPE integer USING \"status\"::integer;",
	}, func(add func(key, got string)) {
		for _, e := range sqlDialectEngines {
			for _, n := range names {
				was := readDesign()
				if e == SQLite {
					was.Comment = ""
					was.Columns[1].Comment = ""
				}
				now := cloneTableDesign(was)
				changes[n](&now)
				if e == SQLite && (n == "column comment" || n == "table comment" || n == "drop comment") {
					continue
				}
				ch, err := AlterTableChange(DialectOf(e), was, now)
				add(string(e)+" "+n, fmt.Sprintf("atomic=%v %s\n%s", ch.Atomic, errText(err), ch.Text()))
			}
		}
	})
}

func cloneTableDesign(t TableDesign) TableDesign {
	t.Columns = slices.Clone(t.Columns)
	t.Indexes = slices.Clone(t.Indexes)
	t.ForeignKeys = slices.Clone(t.ForeignKeys)
	t.Checks = slices.Clone(t.Checks)
	return t
}

// The design form's checks that differ by engine: SQLite keeps no
// comments; DuckDB and ClickHouse number no rows.
func TestPinValidateDesign(t *testing.T) {
	cases := map[string]TableDesign{
		"column comment": {Name: "t", Columns: []ColumnDesign{{Name: "a", Type: "int", Comment: "x"}}},
		"table comment":  {Name: "t", Comment: "x", Columns: []ColumnDesign{{Name: "a", Type: "int"}}},
		"auto increment": {Name: "t", Columns: []ColumnDesign{{Name: "a", Type: "int", PrimaryKey: true, AutoIncrement: true}}},
	}
	pinValues(t, "validate design", map[string]string{
		"postgres column comment":   "ok",
		"postgres table comment":    "ok",
		"postgres auto increment":   "ok",
		"mysql column comment":      "ok",
		"mysql table comment":       "ok",
		"mysql auto increment":      "ok",
		"clickhouse column comment": "ok",
		"clickhouse table comment":  "ok",
		"clickhouse auto increment": "error: ClickHouse numbers no rows of its own: use a sequence",
		"sqlite column comment":     "error: SQLite keeps no comments",
		"sqlite table comment":      "error: SQLite keeps no comments",
		"sqlite auto increment":     "ok",
		"duckdb column comment":     "ok",
		"duckdb table comment":      "ok",
		"duckdb auto increment":     "error: DuckDB numbers no rows of its own: use a sequence",
	}, func(add func(key, got string)) {
		for _, e := range sqlDialectEngines {
			for _, n := range []string{"column comment", "table comment", "auto increment"} {
				_, err := NewTableChange(DialectOf(e), cases[n])
				add(string(e)+" "+n, errText(err))
			}
		}
	})
}

// Grid edits: a typed value cast to its column, an empty row inserted.
func TestPinEditStatements(t *testing.T) {
	cols := []Column{{Name: "id", Type: "integer", PrimaryKey: true}, {Name: "at", Type: "timestamp"}, {Name: "note", Type: "text"}}
	pinValues(t, "edit statements", map[string]string{
		"postgres":               "ok\nUPDATE \"s\".\"t\" SET \"at\" = $1::text::timestamp, \"note\" = $2 WHERE \"id\" = $3 [\"2026-01-02\" \"x\" '\\x01'] want=1\nINSERT INTO \"s\".\"t\" DEFAULT VALUES [] want=1\nUPDATE \"s\".\"t\" SET \"note\" = DEFAULT WHERE \"id\" = $1 ['\\x02'] want=1",
		"postgres insert rows":   "INSERT INTO \"s\".\"t\" (\"id\", \"at\") VALUES ($1::text::integer, $2::text::timestamp) [\"1\" \"2026-01-02\"]",
		"mysql":                  "ok\nUPDATE `s`.`t` SET `at` = ?, `note` = ? WHERE `id` = ? [\"2026-01-02\" \"x\" '\\x01'] want=1\nINSERT INTO `s`.`t` () VALUES () [] want=1\nUPDATE `s`.`t` SET `note` = DEFAULT WHERE `id` = ? ['\\x02'] want=1",
		"mysql insert rows":      "INSERT INTO `s`.`t` (`id`, `at`) VALUES (?, ?) [\"1\" \"2026-01-02\"]",
		"clickhouse":             "ok\nUPDATE `s`.`t` SET `at` = ?, `note` = ? WHERE `id` = ? [\"2026-01-02\" \"x\" '\\x01'] want=1\nINSERT INTO `s`.`t` DEFAULT VALUES [] want=1\nUPDATE `s`.`t` SET `note` = DEFAULT WHERE `id` = ? ['\\x02'] want=1",
		"clickhouse insert rows": "INSERT INTO `s`.`t` (`id`, `at`) VALUES (?, ?) [\"1\" \"2026-01-02\"]",
		"sqlite":                 "ok\nUPDATE \"s\".\"t\" SET \"at\" = ?, \"note\" = ? WHERE \"id\" = ? [\"2026-01-02\" \"x\" '\\x01'] want=1\nINSERT INTO \"s\".\"t\" DEFAULT VALUES [] want=1\nUPDATE \"s\".\"t\" SET \"note\" = DEFAULT WHERE \"id\" = ? ['\\x02'] want=1",
		"sqlite insert rows":     "INSERT INTO \"s\".\"t\" (\"id\", \"at\") VALUES (?, ?) [\"1\" \"2026-01-02\"]",
		"duckdb":                 "ok\nUPDATE \"s\".\"t\" SET \"at\" = CAST(? AS timestamp), \"note\" = ? WHERE \"id\" = ? [\"2026-01-02\" \"x\" '\\x01'] want=1\nINSERT INTO \"s\".\"t\" DEFAULT VALUES [] want=1\nUPDATE \"s\".\"t\" SET \"note\" = DEFAULT WHERE \"id\" = ? ['\\x02'] want=1",
		"duckdb insert rows":     "INSERT INTO \"s\".\"t\" (\"id\", \"at\") VALUES (CAST(? AS integer), CAST(? AS timestamp)) [\"1\" \"2026-01-02\"]",
	}, func(add func(key, got string)) {
		for _, e := range sqlDialectEngines {
			et := &EditTarget{Dialect: DialectOf(e), Schema: "s", Table: "t", Columns: cols, Key: []string{"id"}}
			stmts, err := et.Statements([]Change{
				{Kind: ChangeUpdate, Key: []any{int64(1)}, Values: map[string]any{"at": Typed("2026-01-02"), "note": Typed("x")}},
				{Kind: ChangeInsert, Values: map[string]any{}},
				{Kind: ChangeUpdate, Key: []any{int64(2)}, Values: map[string]any{"note": Default}},
			})
			var out []string
			for _, s := range stmts {
				out = append(out, fmt.Sprintf("%s %q want=%d", s.SQL, s.Args, s.Want))
			}
			add(string(e), errText(err)+"\n"+strings.Join(out, "\n"))
			rows := et.InsertRows(cols[:2], [][]any{{Typed("1"), Typed("2026-01-02")}})
			add(string(e)+" insert rows", fmt.Sprintf("%s %q", rows.SQL, rows.Args))
		}
	})
}

// Enum values that the types spell; PostgreSQL reads them from its
// catalog, covered by the integration tests.
func TestPinEnumValues(t *testing.T) {
	types := []string{"enum('a','b''c')", "Enum8('x' = 1, 'y' = 2)", "Nullable(Enum16('p' = 1))", "ENUM('a\\'b', 'c')", "text"}
	pinValues(t, "enum values", map[string]string{
		"mysql enum('a','b''c')":               "[\"a\" \"b'c\"] nil=false ok",
		"mysql Enum8('x' = 1, 'y' = 2)":        "[] nil=true ok",
		"mysql Nullable(Enum16('p' = 1))":      "[] nil=true ok",
		"mysql ENUM('a\\'b', 'c')":             "[\"a'b\" \"c\"] nil=false ok",
		"mysql text":                           "[] nil=true ok",
		"clickhouse enum('a','b''c')":          "[] nil=true ok",
		"clickhouse Enum8('x' = 1, 'y' = 2)":   "[\"x\" \"y\"] nil=false ok",
		"clickhouse Nullable(Enum16('p' = 1))": "[\"p\"] nil=false ok",
		"clickhouse ENUM('a\\'b', 'c')":        "[] nil=true ok",
		"clickhouse text":                      "[] nil=true ok",
		"sqlite enum('a','b''c')":              "[] nil=true ok",
		"sqlite Enum8('x' = 1, 'y' = 2)":       "[] nil=true ok",
		"sqlite Nullable(Enum16('p' = 1))":     "[] nil=true ok",
		"sqlite ENUM('a\\'b', 'c')":            "[] nil=true ok",
		"sqlite text":                          "[] nil=true ok",
		"duckdb enum('a','b''c')":              "[\"a\" \"b'c\"] nil=false ok",
		"duckdb Enum8('x' = 1, 'y' = 2)":       "[] nil=true ok",
		"duckdb Nullable(Enum16('p' = 1))":     "[] nil=true ok",
		"duckdb ENUM('a\\'b', 'c')":            "[\"a\\\\\" \", \"] nil=false ok",
		"duckdb text":                          "[] nil=true ok",
	}, func(add func(key, got string)) {
		for _, e := range []Engine{MySQL, ClickHouse, SQLite, DuckDB} {
			d := &DB{Config: Config{Engine: e}, Dialect: DialectOf(e)}
			for _, typ := range types {
				vals, err := EnumValues(context.Background(), d, typ)
				add(string(e)+" "+typ, fmt.Sprintf("%q nil=%v %s", vals, vals == nil, errText(err)))
			}
		}
	})
}

// The statements of the Users tab. PostgreSQL's passwords are salted, so
// its pins go without one.
func TestPinAccountSQL(t *testing.T) {
	user := Account{Name: "ada", Host: "%"}
	role := Account{Name: "readers", Host: "%", Role: true}
	pinValues(t, "account sql", map[string]string{
		"postgres create user":                 "CREATE ROLE \"ada\" WITH LOGIN CREATEDB CREATEROLE ok",
		"postgres create role":                 "CREATE ROLE \"readers\" ok",
		"postgres drop user":                   "[\"REASSIGN OWNED BY \\\"ada\\\" TO CURRENT_USER\" \"DROP OWNED BY \\\"ada\\\"\" \"DROP ROLE \\\"ada\\\"\"]",
		"postgres drop role":                   "[\"REASSIGN OWNED BY \\\"readers\\\" TO CURRENT_USER\" \"DROP OWNED BY \\\"readers\\\"\" \"DROP ROLE \\\"readers\\\"\"]",
		"postgres grant schema":                "[\"GRANT USAGE ON SCHEMA \\\"shop\\\" TO \\\"ada\\\"\" \"GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA \\\"shop\\\" TO \\\"ada\\\"\"] ok",
		"postgres grant table":                 "[\"GRANT USAGE ON SCHEMA \\\"shop\\\" TO \\\"ada\\\"\" \"GRANT SELECT ON TABLE \\\"shop\\\".\\\"orders\\\" TO \\\"ada\\\"\"] ok",
		"postgres grant role":                  "GRANT \"readers\" TO \"ada\"",
		"mysql create user":                    "CREATE USER 'ada'@'%' IDENTIFIED BY '' ok",
		"mysql create role":                    "CREATE ROLE 'readers'@'%' ok",
		"mysql create user with password":      "CREATE USER 'ada'@'%' IDENTIFIED BY 'pw' ok",
		"mysql password":                       "ALTER USER 'ada'@'%' IDENTIFIED BY 'pw' ok",
		"mysql drop user":                      "[\"DROP USER 'ada'@'%'\"]",
		"mysql drop role":                      "[\"DROP ROLE 'readers'@'%'\"]",
		"mysql grant schema":                   "[\"GRANT SELECT, INSERT ON `shop`.* TO 'ada'@'%'\"] ok",
		"mysql grant table":                    "[\"GRANT SELECT ON `shop`.`orders` TO 'ada'@'%'\"] ok",
		"mysql grant role":                     "GRANT 'readers'@'%' TO 'ada'@'%'",
		"clickhouse create user":               "CREATE USER `ada` IDENTIFIED WITH no_password ok",
		"clickhouse create role":               "CREATE ROLE `readers` ok",
		"clickhouse create user with password": "CREATE USER `ada` IDENTIFIED WITH sha256_password BY 'pw' ok",
		"clickhouse password":                  "ALTER USER `ada` IDENTIFIED WITH sha256_password BY 'pw' ok",
		"clickhouse drop user":                 "[\"DROP USER `ada`\"]",
		"clickhouse drop role":                 "[\"DROP ROLE `readers`\"]",
		"clickhouse grant schema":              "[\"GRANT SELECT, INSERT ON `shop`.* TO `ada`\"] ok",
		"clickhouse grant table":               "[\"GRANT SELECT ON `shop`.`orders` TO `ada`\"] ok",
		"clickhouse grant role":                "GRANT `readers` TO `ada`",
		"sqlite create user":                   " error: SQLite has no users the app manages",
		"sqlite create role":                   "CREATE ROLE \"readers\" ok",
		"sqlite create user with password":     " error: SQLite has no users the app manages",
		"sqlite password":                      " error: SQLite has no users the app manages",
		"sqlite drop user":                     "[\"DROP USER \\\"ada\\\"\"]",
		"sqlite drop role":                     "[\"DROP ROLE \\\"readers\\\"\"]",
		"sqlite grant schema":                  "[] error: SQLite has no users the app manages",
		"sqlite grant table":                   "[] error: SQLite has no users the app manages",
		"sqlite grant role":                    "GRANT \"readers\" TO \"ada\"",
		"sqlite list":                          "error: SQLite has no users the app manages",
		"sqlite privileges":                    "error: SQLite has no users the app manages",
		"duckdb create user":                   " error: DuckDB has no users the app manages",
		"duckdb create role":                   "CREATE ROLE \"readers\" ok",
		"duckdb create user with password":     " error: DuckDB has no users the app manages",
		"duckdb password":                      " error: DuckDB has no users the app manages",
		"duckdb drop user":                     "[\"DROP USER \\\"ada\\\"\"]",
		"duckdb drop role":                     "[\"DROP ROLE \\\"readers\\\"\"]",
		"duckdb grant schema":                  "[] error: DuckDB has no users the app manages",
		"duckdb grant table":                   "[] error: DuckDB has no users the app manages",
		"duckdb grant role":                    "GRANT \"readers\" TO \"ada\"",
		"duckdb list":                          "error: DuckDB has no users the app manages",
		"duckdb privileges":                    "error: DuckDB has no users the app manages",
	}, func(add func(key, got string)) {
		for _, e := range sqlDialectEngines {
			d := DialectOf(e)
			s, err := CreateAccountSQL(d, NewAccount{Account: user, CreateDB: true, CreateRole: true})
			add(string(e)+" create user", s+" "+errText(err))
			s, err = CreateAccountSQL(d, NewAccount{Account: role})
			add(string(e)+" create role", s+" "+errText(err))
			if e != Postgres {
				s, err = CreateAccountSQL(d, NewAccount{Account: user, Password: "pw"})
				add(string(e)+" create user with password", s+" "+errText(err))
				s, err = PasswordSQL(d, user, "pw")
				add(string(e)+" password", s+" "+errText(err))
			}
			add(string(e)+" drop user", fmt.Sprintf("%q", DropAccountSQL(d, user)))
			add(string(e)+" drop role", fmt.Sprintf("%q", DropAccountSQL(d, role)))
			g, err := GrantSQL(d, []string{"SELECT", "INSERT"}, "shop", "", user)
			add(string(e)+" grant schema", fmt.Sprintf("%q %s", g, errText(err)))
			g, err = GrantSQL(d, []string{"SELECT"}, "shop", "orders", user)
			add(string(e)+" grant table", fmt.Sprintf("%q %s", g, errText(err)))
			add(string(e)+" grant role", GrantRoleSQL(d, role, user))
			if e == SQLite || e == DuckDB {
				_, err = ListAccounts(context.Background(), d, nil)
				add(string(e)+" list", errText(err))
				_, err = AccountPrivileges(context.Background(), d, nil, user)
				add(string(e)+" privileges", errText(err))
			}
		}
	})
}

// fixedPort is an SSH tunnel's or a proxy's local port.
type fixedPort int

func (r fixedPort) LocalPort() int      { return int(r) }
func (fixedPort) Close() error          { return nil }
func (fixedPort) Done() <-chan struct{} { return nil }
func (fixedPort) Err() error            { return nil }

// How a backup runs, by engine; a restore of PostgreSQL's archive.
// DuckDB's restore reads the connection and is pinned in
// TestPinDuckDBRestore.
func TestPinPlanBackup(t *testing.T) {
	ctx := context.Background()
	pinValues(t, "plan backup", map[string]string{
		"postgres backup archive":        "pg_dump --host db.example.com --port 7000 --username app --dbname dbname='shop' --no-password --format custom --file /backups/b --verbose --schema-only statement=\"\" ok",
		"postgres backup sql":            "pg_dump --host db.example.com --port 7000 --username app --dbname dbname='shop' --no-password --format plain --file /backups/b --verbose --schema-only statement=\"\" ok",
		"postgres restore":               "pg_restore --host db.example.com --port 7000 --username app --dbname dbname='shop' --no-password --verbose --single-transaction --exit-on-error --clean --if-exists -- /backups/b.dump ok",
		"postgres dump checks CA only":   "tunnel=false direct=false",
		"mysql backup sql":               "mysqldump --host=db.example.com --port=7000 --protocol=TCP --user=app --ssl-mode=DISABLED --single-transaction --routines --triggers --events --verbose --result-file=/backups/b --no-data --databases -- shop statement=\"\" ok",
		"mysql restore":                  " error: MySQL restores a SQL file with Run SQL File",
		"mysql dump checks CA only":      "tunnel=true direct=false",
		"clickhouse backup sql":          " statement=\"\" error: ClickHouse backs up as []",
		"clickhouse restore":             " error: ClickHouse restores a SQL file with Run SQL File",
		"clickhouse dump checks CA only": "tunnel=false direct=false",
		"sqlite backup copy":             " statement=\"VACUUM INTO '/backups/b'\" ok",
		"sqlite restore":                 " error: SQLite restores a SQL file with Run SQL File",
		"sqlite dump checks CA only":     "tunnel=false direct=false",
		"duckdb backup parquet":          " statement=\"EXPORT DATABASE '/backups/b' (FORMAT parquet)\" ok",
		"duckdb dump checks CA only":     "tunnel=false direct=false",
		"redis backup sql":               " statement=\"\" error: Redis backs up as []",
		"redis restore":                  " error: Redis restores a SQL file with Run SQL File",
		"redis dump checks CA only":      "tunnel=false direct=false",
		"unknown backup sql":             " statement=\"\" error: unknown backs up as []",
		"unknown restore":                " error: unknown restores a SQL file with Run SQL File",
		"unknown dump checks CA only":    "tunnel=false direct=false",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			d := &DB{Config: Config{Name: "n", Engine: e, Host: "db.example.com", Port: 7000, User: "app", Database: "shop"}}
			var formats []BackupFormat
			if r, ok := SQL(e); ok {
				formats = r.BackupFormats()
			}
			if len(formats) == 0 {
				formats = []BackupFormat{BackupSQL}
			}
			for _, f := range formats {
				run, err := PlanBackup(ctx, d, BackupOptions{Format: f, Content: BackupSchemaOnly, Path: "/backups/b"})
				add(fmt.Sprintf("%s backup %s", e, f), fmt.Sprintf("%s statement=%q %s", strings.Join(run.Argv, " "), run.Statement, errText(err)))
			}
			if e != DuckDB {
				run, err := PlanRestore(ctx, d, "", "/backups/b.dump", true)
				add(string(e)+" restore", fmt.Sprintf("%s %s", strings.Join(run.Argv, " "), errText(err)))
			}
			verify := &DB{Config: Config{Engine: e, Host: "db.example.com", TLS: TLSVerifyFull}, route: fixedPort(4000)}
			direct := &DB{Config: Config{Engine: e, Host: "db.example.com", TLS: TLSVerifyFull}}
			add(string(e)+" dump checks CA only", fmt.Sprintf("tunnel=%v direct=%v", MysqldumpChecksCAOnly(verify), MysqldumpChecksCAOnly(direct)))
		}
	})
}

// A DuckDB backup folder restores through a stage database of its own.
func TestPinDuckDBRestore(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, Config{Name: "duck", Engine: DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	run, err := PlanRestore(ctx, d, "", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	var verbs []string
	for _, s := range run.Statements {
		verbs = append(verbs, strings.Fields(s)[0])
	}
	pinValues(t, "duckdb restore", map[string]string{
		"shape": "import=true stage=stage.duckdb argv=[] statements=[\"ATTACH\" \"COPY\" \"DETACH\"]",
	}, func(add func(key, got string)) {
		add("shape", fmt.Sprintf("import=%v stage=%v argv=%q statements=%q", run.Import != "", filepath.Base(run.Stage), run.Argv, verbs))
	})
}

// Plan advice: each engine's own steps, and the estimate check of all.
func TestPinAdvise(t *testing.T) {
	advise := func(e Engine, p *Plan) []Advice { return Advise(e, p) }
	node := func(op, target string, props ...[2]string) *PlanNode {
		n := newNode()
		n.Op, n.Target, n.Props = op, target, props
		return n
	}
	mysqlScan := node("Table scan on t", "t")
	mysqlScan.Rows = 5000
	estimate := node("Scan", "big")
	estimate.Rows, estimate.ActualRows = 10, 5000
	root := node("Root", "")
	root.Children = []*PlanNode{
		node("Sort", "", [2]string{"Sort Space Type", "Disk"}, [2]string{"Sort Space Used", "2048"}),
		node("Hash", "", [2]string{"Hash Batches", "4"}),
		mysqlScan,
		node("SCAN t", "t"),
		node("USE TEMP B-TREE FOR ORDER BY", ""),
		node("ReadFromMergeTree", "db.t", [2]string{"Indexes", `[{"Type":"PrimaryKey","Initial Granules":8,"Selected Granules":8}]`}),
		estimate,
	}
	pinValues(t, "advise", map[string]string{
		"postgres":   "Sort: The sort spills to disk (2048 kB): more work_mem, or an index in the sort's order, avoids it.\nHash: The hash is built in 4 batches on disk: more work_mem would keep it in memory.\nScan: The planner expected 10 rows of big and found 5000: fresher statistics (ANALYZE) may give it a better plan.",
		"mysql":      "Table scan on t: A table scan reads every row of t: an index on the columns of the WHERE may let it read only those it keeps.\nScan: The planner expected 10 rows of big and found 5000: fresher statistics (ANALYZE) may give it a better plan.",
		"clickhouse": "ReadFromMergeTree: The primary key narrows nothing: all 8 granules of db.t are read. A WHERE on the sorting key's first columns would skip most.\nScan: The planner expected 10 rows of big and found 5000: fresher statistics (ANALYZE) may give it a better plan.",
		"sqlite":     "SCAN t: SCAN reads every row of t: an index on the columns of the WHERE may let SQLite SEARCH it instead.\nUSE TEMP B-TREE FOR ORDER BY: SQLite builds a temporary index to order by: an index in that order may save it.\nScan: The planner expected 10 rows of big and found 5000: fresher statistics (ANALYZE) may give it a better plan.",
		"duckdb":     "Scan: The planner expected 10 rows of big and found 5000: fresher statistics (ANALYZE) may give it a better plan.",
		"redis":      "Scan: The planner expected 10 rows of big and found 5000: fresher statistics (ANALYZE) may give it a better plan.",
		"unknown":    "Scan: The planner expected 10 rows of big and found 5000: fresher statistics (ANALYZE) may give it a better plan.",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			var out []string
			for _, a := range advise(e, &Plan{Root: root}) {
				out = append(out, a.Node.Op+": "+a.Text)
			}
			add(string(e), strings.Join(out, "\n"))
		}
	})
}

// Which engine's parser reads which plan.
func TestPinParsePlan(t *testing.T) {
	samples := []struct {
		name string
		cols []string
		rows [][]any
	}{
		{"postgres json", []string{"QUERY PLAN"}, [][]any{{`[{"Plan": {"Node Type": "Seq Scan", "Relation Name": "t"}}]`}}},
		{"mysql tree", []string{"EXPLAIN"}, [][]any{{"-> Table scan on t  (cost=1 rows=10)"}}},
		{"sqlite query plan", []string{"id", "parent", "notused", "detail"}, [][]any{{int64(2), int64(0), int64(0), "SCAN t"}}},
		{"duckdb json", []string{"explain_key", "explain_value"}, [][]any{{"physical_plan", `[{"name": "SEQ_SCAN", "children": []}]`}}},
		{"clickhouse json", []string{"explain"}, [][]any{{`[{"Plan": {"Node Type": "ReadFromMergeTree", "Description": "db.t"}}]`}}},
	}
	pinValues(t, "parse plan", map[string]string{
		"postgres postgres json":       "Seq Scan / t",
		"postgres mysql tree":          "none",
		"postgres sqlite query plan":   "none",
		"postgres duckdb json":         "none",
		"postgres clickhouse json":     "ReadFromMergeTree / ",
		"mysql postgres json":          "none",
		"mysql mysql tree":             "Table scan on t / t",
		"mysql sqlite query plan":      "none",
		"mysql duckdb json":            "none",
		"mysql clickhouse json":        "none",
		"clickhouse postgres json":     "Seq Scan / ",
		"clickhouse mysql tree":        "none",
		"clickhouse sqlite query plan": "none",
		"clickhouse duckdb json":       "none",
		"clickhouse clickhouse json":   "ReadFromMergeTree / db.t",
		"sqlite postgres json":         "none",
		"sqlite mysql tree":            "none",
		"sqlite sqlite query plan":     "QUERY PLAN /  > SCAN t",
		"sqlite duckdb json":           "none",
		"sqlite clickhouse json":       "none",
		"duckdb postgres json":         " / ",
		"duckdb mysql tree":            "none",
		"duckdb sqlite query plan":     "none",
		"duckdb duckdb json":           "SEQ_SCAN / ",
		"duckdb clickhouse json":       " / ",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			// Redis and an unknown engine run no statement to explain.
			r, isSQL := SQL(e)
			if !isSQL {
				continue
			}
			for _, s := range samples {
				p, ok := r.ParsePlan(s.cols, s.rows)
				got := "none"
				if ok {
					got = p.Root.Op + " / " + p.Root.Target
					if len(p.Root.Children) > 0 {
						got += " > " + p.Root.Children[0].Op
					}
				}
				add(string(e)+" "+s.name, got)
			}
		}
	})
}

// Server parameters go to ClickHouse alone.
func TestPinServerParameters(t *testing.T) {
	takes := func(e Engine, args []any) ([]any, error) {
		_, rest, err := withServerParams(context.Background(), e, args)
		return rest, err
	}
	pinValues(t, "server parameters", map[string]string{
		"postgres":   "[] error: postgres does not take server parameters",
		"mysql":      "[] error: mysql does not take server parameters",
		"clickhouse": "[2] ok",
		"sqlite":     "[] error: sqlite does not take server parameters",
		"duckdb":     "[] error: duckdb does not take server parameters",
		"redis":      "[] error: redis does not take server parameters",
		"unknown":    "[] error: unknown does not take server parameters",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			rest, err := takes(e, []any{ServerParam{Name: "p", Value: "1"}, 2})
			add(string(e), fmt.Sprintf("%v %s", rest, errText(err)))
		}
	})
}

// A new database file of each engine.
func TestPinCreateFile(t *testing.T) {
	dir := t.TempDir()
	pinValues(t, "create file", map[string]string{
		"postgres":   "error: PostgreSQL keeps no database in a file made=false",
		"mysql":      "error: MySQL keeps no database in a file made=false",
		"clickhouse": "error: ClickHouse keeps no database in a file made=false",
		"sqlite":     "ok made=true",
		"duckdb":     "ok made=true",
		"redis":      "error: Redis keeps no database in a file made=false",
		"unknown":    "error: unknown keeps no database in a file made=false",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			path := filepath.Join(dir, "new-"+string(e))
			err := CreateFile(context.Background(), e, path)
			_, statErr := os.Stat(path)
			add(string(e), fmt.Sprintf("%s made=%v", errText(err), statErr == nil))
		}
	})
}

// Opening: SQL engines only, known ones only; DuckDB and an in-memory
// SQLite share one connection, which a SQLite file does not.
func TestPinOpen(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "a.sqlite")
	os.WriteFile(file, nil, 0o600)
	configs := []Config{
		{Name: "unknown", Engine: "unknown", Host: "h"},
		{Name: "redis", Engine: Redis, Host: "h"},
		{Name: "lite", Engine: SQLite, Database: ":memory:"},
		{Name: "lite file", Engine: SQLite, Database: file},
		{Name: "duck", Engine: DuckDB, Database: ":memory:"},
	}
	pinValues(t, "open", map[string]string{
		"unknown":   "error: unknown engine \"unknown\"",
		"redis":     "error: Redis is not a SQL database",
		"lite":      "dialect=sqlite single=true tls=\"\" version=true other database is the same pool=true",
		"lite file": "dialect=sqlite single=false tls=\"\" version=true other database is the same pool=true",
		"duck":      "dialect=duckdb single=true tls=\"\" version=true other database is the same pool=true",
	}, func(add func(key, got string)) {
		for _, cfg := range configs {
			d, err := Open(ctx, cfg, nil)
			if err != nil {
				add(cfg.Name, errText(err))
				continue
			}
			same, _ := d.Database(ctx, "other")
			add(cfg.Name, fmt.Sprintf("dialect=%s single=%v tls=%q version=%v other database is the same pool=%v",
				dialectEngine(d.Dialect), d.single, d.TLSState(), d.ServerVersion(ctx) != "", same == d))
			d.Close()
		}
	})
}

// Database opens another pool only for PostgreSQL: a database of the
// others is a schema of the one connection.
func TestPinDatabaseNeedsOwnConnection(t *testing.T) {
	ctx := context.Background()
	pinValues(t, "database own connection", map[string]string{
		"postgres its own":   "same=true ok",
		"mysql its own":      "same=true ok",
		"mysql another":      "same=true ok",
		"clickhouse its own": "same=true ok",
		"clickhouse another": "same=true ok",
		"sqlite its own":     "same=true ok",
		"sqlite another":     "same=true ok",
		"duckdb its own":     "same=true ok",
		"duckdb another":     "same=true ok",
		"redis its own":      "same=true ok",
		"redis another":      "same=true ok",
		"unknown its own":    "same=true ok",
		"unknown another":    "same=true ok",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			d := &DB{Config: Config{Engine: e, Database: "shop"}, used: new(atomic.Int64)}
			same, err := d.Database(ctx, "shop")
			add(string(e)+" its own", fmt.Sprintf("same=%v %s", same == d, errText(err)))
			if e == Postgres {
				continue // another database opens a pool on the server
			}
			same, err = d.Database(ctx, "other")
			add(string(e)+" another", fmt.Sprintf("same=%v %s", same == d, errText(err)))
		}
	})
}

// Session hooks reachable without a server: SQLite's bytes stay binary,
// COPY FROM STDIN is PostgreSQL's, autocommit off is MySQL's.
func TestPinSessionHooks(t *testing.T) {
	ctx := context.Background()
	pinValues(t, "session hooks", map[string]string{
		"sqlite values":           "[]byte{0x61, 0x62, 0x63} \"abc\"",
		"sqlite copy from stdin":  "error: COPY FROM STDIN is PostgreSQL's",
		"sqlite autocommit off":   "false",
		"sqlite server parameter": "error: sqlite does not take server parameters",
		"duckdb values":           "[]byte{0x61, 0x62, 0x63} \"abc\"",
		"duckdb copy from stdin":  "error: COPY FROM STDIN is PostgreSQL's",
		"duckdb autocommit off":   "false",
		"duckdb server parameter": "error: duckdb does not take server parameters",
	}, func(add func(key, got string)) {
		for _, cfg := range []Config{{Name: "lite", Engine: SQLite, Database: ":memory:"}, {Name: "duck", Engine: DuckDB, Database: ":memory:"}} {
			d, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			s, err := d.Session(ctx)
			if err != nil {
				t.Fatal(err)
			}
			c, err := s.Query(ctx, "SELECT CAST('abc' AS BLOB) AS b, 'abc' AS t")
			if err != nil {
				t.Fatal(err)
			}
			rows, _ := c.Fetch(1)
			c.Close()
			add(string(cfg.Engine)+" values", fmt.Sprintf("%#v %#v", rows[0][0], rows[0][1]))
			_, err = s.CopyFrom(ctx, "COPY t FROM STDIN", strings.NewReader(""))
			add(string(cfg.Engine)+" copy from stdin", errText(err))
			add(string(cfg.Engine)+" autocommit off", fmt.Sprint(s.AutocommitOff()))
			_, err = s.Exec(ctx, "SELECT 1", ServerParam{Name: "p", Value: "1"})
			add(string(cfg.Engine)+" server parameter", errText(err))
			s.Close()
			d.Close()
		}
	})
}

// The transaction a session shows after statements, as the engines'
// drivers report it.
func TestPinSessionTransactions(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "tx.sqlite")
	os.WriteFile(file, nil, 0o600)
	sequences := [][]string{
		{"BEGIN", "INSERT INTO t VALUES (1)", "INSERT INTO t VALUES (1)", "SELECT 1", "ROLLBACK"},
		{"BEGIN", "SELEC 1", "COMMIT"},
		{"SAVEPOINT a", "RELEASE a"},
		{"BEGIN", "CREATE TABLE u (a INT)", "ROLLBACK"},
	}
	pinValues(t, "session transactions", map[string]string{
		"sqlite BEGIN; INSERT INTO t VALUES (1); INSERT INTO t VALUES (1); SELECT 1; ROLLBACK": "BEGIN -> open\nINSERT INTO t VALUES (1) -> open\nINSERT INTO t VALUES (1) -> open (failed)\nSELECT 1 -> open\nROLLBACK -> none",
		"sqlite BEGIN; SELEC 1; COMMIT":                                                        "BEGIN -> open\nSELEC 1 -> open (failed)\nCOMMIT -> none",
		"sqlite SAVEPOINT a; RELEASE a":                                                        "SAVEPOINT a -> open\nRELEASE a -> none",
		"sqlite BEGIN; CREATE TABLE u (a INT); ROLLBACK":                                       "BEGIN -> open\nCREATE TABLE u (a INT) -> open\nROLLBACK -> none",
		"duckdb BEGIN; INSERT INTO t VALUES (1); INSERT INTO t VALUES (1); SELECT 1; ROLLBACK": "BEGIN -> open\nINSERT INTO t VALUES (1) -> open\nINSERT INTO t VALUES (1) -> failed (failed)\nSELECT 1 -> failed (failed)\nROLLBACK -> none",
		"duckdb BEGIN; SELEC 1; COMMIT":                                                        "BEGIN -> open\nSELEC 1 -> open (failed)\nCOMMIT -> none",
		"duckdb SAVEPOINT a; RELEASE a":                                                        "SAVEPOINT a -> none (failed)\nRELEASE a -> none (failed)",
		"duckdb BEGIN; CREATE TABLE u (a INT); ROLLBACK":                                       "BEGIN -> open\nCREATE TABLE u (a INT) -> open\nROLLBACK -> none",
	}, func(add func(key, got string)) {
		for _, cfg := range []Config{{Name: "lite", Engine: SQLite, Database: file}, {Name: "duck", Engine: DuckDB, Database: ":memory:"}} {
			d, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.SQL.Exec("CREATE TABLE t (a INT PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			for _, seq := range sequences {
				s, err := d.Session(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var steps []string
				for _, q := range seq {
					_, err := s.Exec(ctx, q)
					step := fmt.Sprintf("%s -> %s", q, txName(s.Tx()))
					if err != nil {
						step += " (failed)"
					}
					steps = append(steps, step)
				}
				add(string(cfg.Engine)+" "+strings.Join(seq, "; "), strings.Join(steps, "\n"))
				s.Close()
			}
			d.Close()
		}
	})
}

// The transaction followed from the statements' verbs, where the driver
// cannot tell, step by step: the order of its checks is what is pinned
// (MySQL's autocommit off, implicit commits, failed statements).
func TestPinTxByVerbsSequences(t *testing.T) {
	after := func(v txByVerbs, e Engine, query string, err error, rollsBack bool) txByVerbs {
		return v.after(e, query, err, func() bool { return rollsBack })
	}
	failed := errors.New("failed")
	duplicate := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}
	deadlock := &mysql.MySQLError{Number: 1213, Message: "Deadlock found"}
	lockWait := &mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"}
	duckConstraint := &duckdb.Error{Type: duckdb.ErrorTypeConstraint, Msg: "Constraint Error"}
	duckParser := &duckdb.Error{Type: duckdb.ErrorTypeParser, Msg: "Parser Error"}
	type step struct {
		sql       string
		err       error
		rollsBack bool
	}
	sequences := []struct {
		name  string
		steps []step
	}{
		{"autocommit off then a read", []step{{sql: "SET autocommit = 0"}, {sql: "SELECT 1"}, {sql: "COMMIT"}, {sql: "SELECT 1"}}},
		{"autocommit off then on", []step{{sql: "SET autocommit = 0"}, {sql: "SELECT 1"}, {sql: "SET autocommit = 1"}, {sql: "SELECT 1"}}},
		{"autocommit off then DDL", []step{{sql: "SET autocommit = 0"}, {sql: "INSERT INTO t VALUES (1)"}, {sql: "CREATE TABLE u (a INT)"}, {sql: "SELECT 1"}}},
		{"autocommit off then a failure", []step{{sql: "SET autocommit = 0"}, {sql: "INSERT INTO t VALUES (1)", err: duplicate}}},
		{"autocommit off then a deadlock", []step{{sql: "SET autocommit = 0"}, {sql: "SELECT 1"}, {sql: "UPDATE t SET a = 1", err: deadlock}}},
		{"DDL in a transaction", []step{{sql: "BEGIN"}, {sql: "INSERT INTO t VALUES (1)"}, {sql: "CREATE TABLE u (a INT)"}, {sql: "SELECT 1"}}},
		{"temporary table in a transaction", []step{{sql: "START TRANSACTION"}, {sql: "CREATE TEMPORARY TABLE u (a INT)"}}},
		{"failure in a transaction", []step{{sql: "BEGIN"}, {sql: "INSERT INTO t VALUES (1)", err: failed}, {sql: "SELECT 1"}, {sql: "ROLLBACK"}}},
		{"constraint failure in a transaction", []step{{sql: "BEGIN"}, {sql: "INSERT INTO t VALUES (1)", err: duckConstraint}, {sql: "ROLLBACK"}}},
		{"parse failure in a transaction", []step{{sql: "BEGIN"}, {sql: "SELEC 1", err: duckParser}}},
		{"deadlock in a transaction", []step{{sql: "BEGIN"}, {sql: "UPDATE t SET a = 1", err: deadlock}}},
		{"lock wait, rolled back", []step{{sql: "BEGIN"}, {sql: "UPDATE t SET a = 1", err: lockWait, rollsBack: true}}},
		{"lock wait, kept", []step{{sql: "BEGIN"}, {sql: "UPDATE t SET a = 1", err: lockWait}}},
		{"savepoint outside a transaction", []step{{sql: "SAVEPOINT a"}, {sql: "RELEASE a"}}},
		{"compound statement", []step{{sql: "BEGIN NOT ATOMIC SELECT 1; END"}}},
		{"chained commit", []step{{sql: "BEGIN"}, {sql: "COMMIT AND CHAIN"}, {sql: "ROLLBACK TO SAVEPOINT s"}, {sql: "END"}}},
		{"XA", []step{{sql: "XA START 'x'"}, {sql: "XA END 'x'"}, {sql: "XA PREPARE 'x'"}}},
		{"executable comment", []step{{sql: "BEGIN"}, {sql: "/*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50001 VIEW v AS SELECT 1 */"}}},
		{"abort", []step{{sql: "BEGIN"}, {sql: "ABORT"}}},
	}
	pinValues(t, "tx by verbs", map[string]string{
		"postgres autocommit off then a read":            "none/off=false none/off=false none/off=false none/off=false",
		"postgres autocommit off then on":                "none/off=false none/off=false none/off=false none/off=false",
		"postgres autocommit off then DDL":               "none/off=false none/off=false none/off=false none/off=false",
		"postgres autocommit off then a failure":         "none/off=false none/off=false",
		"postgres autocommit off then a deadlock":        "none/off=false none/off=false none/off=false",
		"postgres DDL in a transaction":                  "open/off=false open/off=false open/off=false open/off=false",
		"postgres temporary table in a transaction":      "open/off=false open/off=false",
		"postgres failure in a transaction":              "open/off=false open/off=false open/off=false none/off=false",
		"postgres constraint failure in a transaction":   "open/off=false open/off=false none/off=false",
		"postgres parse failure in a transaction":        "open/off=false open/off=false",
		"postgres deadlock in a transaction":             "open/off=false open/off=false",
		"postgres lock wait, rolled back":                "open/off=false open/off=false",
		"postgres lock wait, kept":                       "open/off=false open/off=false",
		"postgres savepoint outside a transaction":       "none/off=false none/off=false",
		"postgres compound statement":                    "open/off=false",
		"postgres chained commit":                        "open/off=false open/off=false open/off=false none/off=false",
		"postgres XA":                                    "open/off=false open/off=false none/off=false",
		"postgres executable comment":                    "open/off=false open/off=false",
		"postgres abort":                                 "open/off=false none/off=false",
		"mysql autocommit off then a read":               "none/off=true open/off=true none/off=true open/off=true",
		"mysql autocommit off then on":                   "none/off=true open/off=true none/off=false none/off=false",
		"mysql autocommit off then DDL":                  "none/off=true open/off=true none/off=true open/off=true",
		"mysql autocommit off then a failure":            "none/off=true open/off=true",
		"mysql autocommit off then a deadlock":           "none/off=true open/off=true none/off=true",
		"mysql DDL in a transaction":                     "open/off=false open/off=false none/off=false none/off=false",
		"mysql temporary table in a transaction":         "open/off=false open/off=false",
		"mysql failure in a transaction":                 "open/off=false open/off=false open/off=false none/off=false",
		"mysql constraint failure in a transaction":      "open/off=false open/off=false none/off=false",
		"mysql parse failure in a transaction":           "open/off=false open/off=false",
		"mysql deadlock in a transaction":                "open/off=false none/off=false",
		"mysql lock wait, rolled back":                   "open/off=false none/off=false",
		"mysql lock wait, kept":                          "open/off=false open/off=false",
		"mysql savepoint outside a transaction":          "none/off=false none/off=false",
		"mysql compound statement":                       "none/off=false",
		"mysql chained commit":                           "open/off=false open/off=false open/off=false none/off=false",
		"mysql XA":                                       "open/off=false open/off=false none/off=false",
		"mysql executable comment":                       "open/off=false none/off=false",
		"mysql abort":                                    "open/off=false none/off=false",
		"clickhouse autocommit off then a read":          "none/off=false none/off=false none/off=false none/off=false",
		"clickhouse autocommit off then on":              "none/off=false none/off=false none/off=false none/off=false",
		"clickhouse autocommit off then DDL":             "none/off=false none/off=false none/off=false none/off=false",
		"clickhouse autocommit off then a failure":       "none/off=false none/off=false",
		"clickhouse autocommit off then a deadlock":      "none/off=false none/off=false none/off=false",
		"clickhouse DDL in a transaction":                "open/off=false open/off=false open/off=false open/off=false",
		"clickhouse temporary table in a transaction":    "open/off=false open/off=false",
		"clickhouse failure in a transaction":            "open/off=false open/off=false open/off=false none/off=false",
		"clickhouse constraint failure in a transaction": "open/off=false open/off=false none/off=false",
		"clickhouse parse failure in a transaction":      "open/off=false open/off=false",
		"clickhouse deadlock in a transaction":           "open/off=false open/off=false",
		"clickhouse lock wait, rolled back":              "open/off=false open/off=false",
		"clickhouse lock wait, kept":                     "open/off=false open/off=false",
		"clickhouse savepoint outside a transaction":     "none/off=false none/off=false",
		"clickhouse compound statement":                  "open/off=false",
		"clickhouse chained commit":                      "open/off=false open/off=false open/off=false none/off=false",
		"clickhouse XA":                                  "open/off=false open/off=false none/off=false",
		"clickhouse executable comment":                  "open/off=false open/off=false",
		"clickhouse abort":                               "open/off=false none/off=false",
		"sqlite autocommit off then a read":              "none/off=false none/off=false none/off=false none/off=false",
		"sqlite autocommit off then on":                  "none/off=false none/off=false none/off=false none/off=false",
		"sqlite autocommit off then DDL":                 "none/off=false none/off=false none/off=false none/off=false",
		"sqlite autocommit off then a failure":           "none/off=false none/off=false",
		"sqlite autocommit off then a deadlock":          "none/off=false none/off=false none/off=false",
		"sqlite DDL in a transaction":                    "open/off=false open/off=false open/off=false open/off=false",
		"sqlite temporary table in a transaction":        "open/off=false open/off=false",
		"sqlite failure in a transaction":                "open/off=false open/off=false open/off=false none/off=false",
		"sqlite constraint failure in a transaction":     "open/off=false open/off=false none/off=false",
		"sqlite parse failure in a transaction":          "open/off=false open/off=false",
		"sqlite deadlock in a transaction":               "open/off=false open/off=false",
		"sqlite lock wait, rolled back":                  "open/off=false open/off=false",
		"sqlite lock wait, kept":                         "open/off=false open/off=false",
		"sqlite savepoint outside a transaction":         "open/off=false open/off=false",
		"sqlite compound statement":                      "open/off=false",
		"sqlite chained commit":                          "open/off=false open/off=false open/off=false none/off=false",
		"sqlite XA":                                      "open/off=false open/off=false none/off=false",
		"sqlite executable comment":                      "open/off=false open/off=false",
		"sqlite abort":                                   "open/off=false none/off=false",
		"duckdb autocommit off then a read":              "none/off=false none/off=false none/off=false none/off=false",
		"duckdb autocommit off then on":                  "none/off=false none/off=false none/off=false none/off=false",
		"duckdb autocommit off then DDL":                 "none/off=false none/off=false none/off=false none/off=false",
		"duckdb autocommit off then a failure":           "none/off=false none/off=false",
		"duckdb autocommit off then a deadlock":          "none/off=false none/off=false none/off=false",
		"duckdb DDL in a transaction":                    "open/off=false open/off=false open/off=false open/off=false",
		"duckdb temporary table in a transaction":        "open/off=false open/off=false",
		"duckdb failure in a transaction":                "open/off=false failed/off=false failed/off=false none/off=false",
		"duckdb constraint failure in a transaction":     "open/off=false failed/off=false none/off=false",
		"duckdb parse failure in a transaction":          "open/off=false open/off=false",
		"duckdb deadlock in a transaction":               "open/off=false failed/off=false",
		"duckdb lock wait, rolled back":                  "open/off=false failed/off=false",
		"duckdb lock wait, kept":                         "open/off=false failed/off=false",
		"duckdb savepoint outside a transaction":         "none/off=false none/off=false",
		"duckdb compound statement":                      "open/off=false",
		"duckdb chained commit":                          "open/off=false open/off=false open/off=false none/off=false",
		"duckdb XA":                                      "open/off=false open/off=false none/off=false",
		"duckdb executable comment":                      "open/off=false open/off=false",
		"duckdb abort":                                   "open/off=false none/off=false",
		"redis autocommit off then a read":               "none/off=false none/off=false none/off=false none/off=false",
		"redis autocommit off then on":                   "none/off=false none/off=false none/off=false none/off=false",
		"redis autocommit off then DDL":                  "none/off=false none/off=false none/off=false none/off=false",
		"redis autocommit off then a failure":            "none/off=false none/off=false",
		"redis autocommit off then a deadlock":           "none/off=false none/off=false none/off=false",
		"redis DDL in a transaction":                     "open/off=false open/off=false open/off=false open/off=false",
		"redis temporary table in a transaction":         "open/off=false open/off=false",
		"redis failure in a transaction":                 "open/off=false open/off=false open/off=false none/off=false",
		"redis constraint failure in a transaction":      "open/off=false open/off=false none/off=false",
		"redis parse failure in a transaction":           "open/off=false open/off=false",
		"redis deadlock in a transaction":                "open/off=false open/off=false",
		"redis lock wait, rolled back":                   "open/off=false open/off=false",
		"redis lock wait, kept":                          "open/off=false open/off=false",
		"redis savepoint outside a transaction":          "none/off=false none/off=false",
		"redis compound statement":                       "open/off=false",
		"redis chained commit":                           "open/off=false open/off=false open/off=false none/off=false",
		"redis XA":                                       "open/off=false open/off=false none/off=false",
		"redis executable comment":                       "open/off=false open/off=false",
		"redis abort":                                    "open/off=false none/off=false",
		"unknown autocommit off then a read":             "none/off=false none/off=false none/off=false none/off=false",
		"unknown autocommit off then on":                 "none/off=false none/off=false none/off=false none/off=false",
		"unknown autocommit off then DDL":                "none/off=false none/off=false none/off=false none/off=false",
		"unknown autocommit off then a failure":          "none/off=false none/off=false",
		"unknown autocommit off then a deadlock":         "none/off=false none/off=false none/off=false",
		"unknown DDL in a transaction":                   "open/off=false open/off=false open/off=false open/off=false",
		"unknown temporary table in a transaction":       "open/off=false open/off=false",
		"unknown failure in a transaction":               "open/off=false open/off=false open/off=false none/off=false",
		"unknown constraint failure in a transaction":    "open/off=false open/off=false none/off=false",
		"unknown parse failure in a transaction":         "open/off=false open/off=false",
		"unknown deadlock in a transaction":              "open/off=false open/off=false",
		"unknown lock wait, rolled back":                 "open/off=false open/off=false",
		"unknown lock wait, kept":                        "open/off=false open/off=false",
		"unknown savepoint outside a transaction":        "none/off=false none/off=false",
		"unknown compound statement":                     "open/off=false",
		"unknown chained commit":                         "open/off=false open/off=false open/off=false none/off=false",
		"unknown XA":                                     "open/off=false open/off=false none/off=false",
		"unknown executable comment":                     "open/off=false open/off=false",
		"unknown abort":                                  "open/off=false none/off=false",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			for _, s := range sequences {
				v := txByVerbs{}
				var states []string
				for _, st := range s.steps {
					v = after(v, e, st.sql, st.err, st.rollsBack)
					states = append(states, fmt.Sprintf("%s/off=%v", txName(v.tx), v.autocommitOff))
				}
				add(string(e)+" "+s.name, strings.Join(states, " "))
			}
		}
	})
}

// Search on the file engines; the server engines' are the integration
// tests'.
func TestPinSearch(t *testing.T) {
	ctx := context.Background()
	pinValues(t, "search", map[string]string{
		"sqlite definitions=false": "more=false ok\nmain column cheap.gizmo_id table=\"view\" excerpt=false\nmain column widgets.gizmo_id table=\"table\" excerpt=false",
		"sqlite definitions=true":  "more=false ok\nmain view .cheap table=\"\" excerpt=true\nmain column cheap.gizmo_id table=\"view\" excerpt=false\nmain column widgets.gizmo_id table=\"table\" excerpt=false",
		"duckdb definitions=false": "more=false ok\nmain column cheap.gizmo_id table=\"view\" excerpt=false\nmain column widgets.gizmo_id table=\"table\" excerpt=false",
		"duckdb definitions=true":  "more=false ok\nmain view .cheap table=\"\" excerpt=true\nmain column cheap.gizmo_id table=\"view\" excerpt=false\nmain column widgets.gizmo_id table=\"table\" excerpt=false",
	}, func(add func(key, got string)) {
		for _, cfg := range []Config{{Name: "lite", Engine: SQLite, Database: ":memory:"}, {Name: "duck", Engine: DuckDB, Database: ":memory:"}} {
			d, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{"CREATE TABLE widgets (gizmo_id INT)", "CREATE VIEW cheap AS SELECT gizmo_id FROM widgets WHERE gizmo_id < 3"} {
				if _, err := d.SQL.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			for _, defs := range []bool{false, true} {
				hits, more, err := Search(ctx, d, "GIZMO", defs)
				var out []string
				for _, h := range hits {
					out = append(out, fmt.Sprintf("%s %s %s.%s table=%q excerpt=%v", h.Schema, h.Kind, h.Table, h.Name, h.TableKind, h.Excerpt != ""))
				}
				add(fmt.Sprintf("%s definitions=%v", cfg.Engine, defs), fmt.Sprintf("more=%v %s\n%s", more, errText(err), strings.Join(out, "\n")))
			}
			d.Close()
		}
	})
}

// The design a table form reads, with each file engine's extras: SQLite's
// table text, DuckDB's checks.
func TestPinReadTableDesign(t *testing.T) {
	ctx := context.Background()
	pinValues(t, "read table design", map[string]string{
		"sqlite": "ok pk=\"\" unkept=\"\"\ncolumns: id INTEGER null=false pk=true default=\"\"; parent INTEGER null=true pk=false default=\"\"; n INTEGER null=true pk=false default=\"1\"; label VARCHAR null=true pk=false default=\"\"\nindexes: kids_n [\"n\"] unique=false constraint=false; sqlite_autoindex_kids_1 [\"label\"] unique=true constraint=true\nkeys: 1\nchecks: [{Read:true Name: Expression:n > 0}]",
		"duckdb": "ok pk=\"PRIMARY KEY\" unkept=\"\"\ncolumns: id INTEGER null=false pk=true default=\"\"; parent INTEGER null=true pk=false default=\"\"; n INTEGER null=true pk=false default=\"1\"; label VARCHAR null=true pk=false default=\"\"\nindexes: kids_n [\"n\"] unique=false constraint=false\nkeys: 1\nchecks: [{Read:true Name:kids_n_check Expression:(n > 0)}]",
	}, func(add func(key, got string)) {
		for _, cfg := range []Config{{Name: "lite", Engine: SQLite, Database: ":memory:"}, {Name: "duck", Engine: DuckDB, Database: ":memory:"}} {
			d, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				"CREATE TABLE parents (id INTEGER PRIMARY KEY)",
				"CREATE TABLE kids (id INTEGER PRIMARY KEY, parent INTEGER REFERENCES parents (id), n INTEGER DEFAULT 1 CHECK (n > 0), label VARCHAR UNIQUE)",
				"CREATE INDEX kids_n ON kids (n)",
			} {
				if _, err := d.SQL.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			td, err := ReadTableDesign(ctx, d, Object{Schema: "main", Name: "kids", Kind: KindTable})
			var cols []string
			for _, c := range td.Columns {
				cols = append(cols, fmt.Sprintf("%s %s null=%v pk=%v default=%q", c.Name, c.Type, c.Nullable, c.PrimaryKey, c.Default))
			}
			var ixs []string
			for _, ix := range td.Indexes {
				ixs = append(ixs, fmt.Sprintf("%s %q unique=%v constraint=%v", ix.Name, ix.Columns, ix.Unique, ix.Constraint))
			}
			add(string(cfg.Engine), fmt.Sprintf("%s pk=%q unkept=%q\ncolumns: %s\nindexes: %s\nkeys: %d\nchecks: %+v",
				errText(err), td.PrimaryKeyName, td.Unkept(), strings.Join(cols, "; "), strings.Join(ixs, "; "), len(td.ForeignKeys), td.Checks))
			d.Close()
		}
	})
}

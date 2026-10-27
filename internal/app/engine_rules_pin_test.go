package app

// Characterization pins: what the app's engine-dependent rules give
// today, for each engine and for an ID no build knows. Each test reaches
// its rule through one local closure; when a switch moves into a
// per-engine method (ActivityPages, MaintenanceCommands, BackupNote,
// BackupChoices, AccountForm and the like), re-point that closure and
// leave the pinned values alone. A value printed as "unpinned" is one the
// table lacks.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/lists"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

var pinEngines = []db.Engine{db.Postgres, db.MySQL, db.ClickHouse, db.SQLite, db.DuckDB, db.Redis, "unknown"}

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

// pinPage is an activity page as its pins read it, whatever type holds it.
type pinPage struct {
	name, query, hint string
	live, metrics     bool
	stop, kill        func(row []any) string
}

// digest is a short fingerprint of a long text, as a page's query.
func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// TestPinActivityPages pins each engine's ActivityPages; a page's query
// is pinned by its digest.
func TestPinActivityPages(t *testing.T) {
	pagesOf := func(e db.Engine) []pinPage {
		return lists.Map(e.ActivityPages(), func(p db.ActivityPage) pinPage {
			return pinPage{p.Name, p.Query, p.Hint, p.Live, p.Metrics, p.Stop, p.Kill}
		})
	}
	pinValues(t, "activity pages", map[string]string{
		"postgres":   "pages=3\nSessions live=true metrics=false query=adeb2ab451b6 stop=\"SELECT pg_cancel_backend(42)\" kill=\"SELECT pg_terminate_backend(42)\" hint=\"\"\nLocks live=true metrics=false query=4c912117f600 stop=\"SELECT pg_cancel_backend(42)\" kill=\"SELECT pg_terminate_backend(42)\" hint=\"\"\nStatistics live=false metrics=false query=59cb20f4c08f stop=\"none\" kill=\"none\" hint=\"These come from the pg_stat_statements extension: add it to shared_preload_libraries, restart the server, and run CREATE EXTENSION pg_stat_statements.\"",
		"mysql":      "pages=3\nSessions live=true metrics=false query=7774042c96b0 stop=\"KILL QUERY 42\" kill=\"KILL 42\" hint=\"\"\nLocks live=true metrics=false query=8771fdc26e50 stop=\"KILL QUERY 42\" kill=\"KILL 42\" hint=\"Locks come from performance_schema, which the server must have on, and the user may read.\"\nStatistics live=false metrics=false query=6cd557864ed2 stop=\"none\" kill=\"none\" hint=\"Statistics come from performance_schema, which the server must have on, and the user may read.\"",
		"clickhouse": "pages=3\nSessions live=true metrics=false query=3023cb4387c5 stop=\"KILL QUERY WHERE query_id = 42 ASYNC\" kill=\"none\" hint=\"\"\nStatistics live=false metrics=false query=b00036412537 stop=\"none\" kill=\"none\" hint=\"Statistics come from system.query_log, which the server keeps when log_queries is on.\"\nMetrics live=true metrics=true query=1e3b29167be7 stop=\"none\" kill=\"none\" hint=\"\"",
		"sqlite":     "pages=0\n",
		"duckdb":     "pages=0\n",
		"redis":      "pages=1\nClients live=true metrics=false query=e3b0c44298fc stop=\"none\" kill=\"none\" hint=\"\"",
		"unknown":    "pages=0\n",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			pages := pagesOf(e)
			var out []string
			for _, p := range pages {
				stop, kill := "none", "none"
				if p.stop != nil {
					stop = p.stop([]any{int64(42)})
				}
				if p.kill != nil {
					kill = p.kill([]any{int64(42)})
				}
				out = append(out, fmt.Sprintf("%s live=%v metrics=%v query=%s stop=%q kill=%q hint=%q", p.name, p.live, p.metrics, digest(p.query), stop, kill, p.hint))
			}
			add(string(e), fmt.Sprintf("pages=%d\n%s", len(pages), strings.Join(out, "\n")))
		}
	})
}

// TestPinMaintenanceCommands pins each engine's MaintenanceCommands.
func TestPinMaintenanceCommands(t *testing.T) {
	commandsOf := func(r db.SQLRules, quoted string) []string {
		return lists.Map(r.MaintenanceCommands(quoted), func(m db.MaintenanceCommand) string { return m.Label + ": " + m.SQL })
	}
	pinValues(t, "maintenance", map[string]string{
		"postgres":   "[\"Vacuum: VACUUM \\\"s\\\".\\\"t\\\"\" \"Vacuum and Analyze: VACUUM ANALYZE \\\"s\\\".\\\"t\\\"\" \"Analyze: ANALYZE \\\"s\\\".\\\"t\\\"\" \"Reindex (locks writes): REINDEX TABLE \\\"s\\\".\\\"t\\\"\" \"Vacuum Full (locks reads and writes): VACUUM FULL \\\"s\\\".\\\"t\\\"\"]",
		"mysql":      "[\"Analyze: ANALYZE TABLE \\\"s\\\".\\\"t\\\"\" \"Optimize (rebuilds the table): OPTIMIZE TABLE \\\"s\\\".\\\"t\\\"\" \"Check: CHECK TABLE \\\"s\\\".\\\"t\\\"\"]",
		"clickhouse": "[\"Optimize: OPTIMIZE TABLE \\\"s\\\".\\\"t\\\"\" \"Optimize Final (merges every part): OPTIMIZE TABLE \\\"s\\\".\\\"t\\\" FINAL\"]",
		"sqlite":     "[\"Analyze: ANALYZE \\\"s\\\".\\\"t\\\"\" \"Reindex: REINDEX \\\"s\\\".\\\"t\\\"\"]",
		"duckdb":     "[]",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			// Redis and an unknown engine list no tables, whose menu
			// offers the commands.
			if r, ok := db.SQL(e); ok {
				add(string(e), fmt.Sprintf("%q", commandsOf(r, `"s"."t"`)))
			}
		}
	})
}

// TestPinBackupNote pins each engine's BackupNote. An engine with no
// note of its own reads mysqldump's.
func TestPinBackupNote(t *testing.T) {
	noteOf := func(r db.SQLRules, restore bool) string { return r.BackupNote(restore) }
	pinValues(t, "backup note", map[string]string{
		"postgres restore=false":   "pg_dump writes it, reaching the server as the connection does; it must be installed, of the server's version or later.",
		"postgres restore=true":    "A .dump of pg_dump, or a SQL file.",
		"mysql restore=false":      "mysqldump writes it, in one transaction, with routines, triggers and events; it must be installed.",
		"mysql restore=true":       "A SQL file, as mysqldump writes, runs as Run SQL File runs it.",
		"clickhouse restore=false": "mysqldump writes it, in one transaction, with routines, triggers and events; it must be installed.",
		"clickhouse restore=true":  "A SQL file, as mysqldump writes, runs as Run SQL File runs it.",
		"sqlite restore=false":     "VACUUM INTO writes a copy of the database as it is, while it stays in use: open the copy as a connection to read it.",
		"sqlite restore=true":      "A SQL file runs as Run SQL File runs it; a copy of the database opens as a connection of its own.",
		"duckdb restore=false":     "EXPORT DATABASE writes the schema and each table's rows, in Parquet, in a new folder.",
		"duckdb restore=true":      "IMPORT DATABASE reads the folder in a DuckDB of its own, which reaches no file outside it; what it makes is then copied into the database, which must not have its tables.",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			// Redis and an unknown engine open no backup dialog
			// (TestPinBackupScope).
			r, ok := db.SQL(e)
			if !ok {
				continue
			}
			for _, restore := range []bool{false, true} {
				add(fmt.Sprintf("%s restore=%v", e, restore), noteOf(r, restore))
			}
		}
	})
}

// Which connections back up, and what of a navigator node.
func TestPinBackupScope(t *testing.T) {
	pinValues(t, "backup scope", map[string]string{
		"postgres":   "supported=true database node=\"d\",true schema node=\"\",false",
		"mysql":      "supported=true database node=\"\",false schema node=\"s\",true",
		"clickhouse": "supported=false database node=\"\",false schema node=\"\",false",
		"sqlite":     "supported=true database node=\"\",false schema node=\"\",false",
		"duckdb":     "supported=true database node=\"\",false schema node=\"\",false",
		"redis":      "supported=false database node=\"\",false schema node=\"\",false",
		"unknown":    "supported=false database node=\"\",false schema node=\"\",false",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			cn := &connection.Conn{Config: db.Config{Engine: e}}
			db1, ok1 := backupTarget(cn, navNode{kind: nodeDatabase, database: "d", schema: "s"})
			db2, ok2 := backupTarget(cn, navNode{kind: nodeSchema, database: "d", schema: "s"})
			add(string(e), fmt.Sprintf("supported=%v database node=%q,%v schema node=%q,%v", backupSupported(e), db1, ok1, db2, ok2))
		}
	})
}

// What the backup and restore dialogs ask, by engine.
func TestPinBackupDialog(t *testing.T) {
	labels := []string{"Format", "Keep", "File", "Folder", "Drop what the backup makes before making it"}
	pinValues(t, "backup dialog", map[string]string{
		"postgres restore=false":   "Format=true Keep=true File=true Folder=false Drop what the backup makes before making it=false note shown=true",
		"postgres restore=true":    "Format=false Keep=false File=true Folder=false Drop what the backup makes before making it=true note shown=true",
		"mysql restore=false":      "Format=false Keep=true File=true Folder=false Drop what the backup makes before making it=false note shown=true",
		"mysql restore=true":       "Format=false Keep=false File=true Folder=false Drop what the backup makes before making it=false note shown=true",
		"clickhouse restore=false": "Format=false Keep=false File=true Folder=false Drop what the backup makes before making it=false note shown=true",
		"clickhouse restore=true":  "Format=false Keep=false File=true Folder=false Drop what the backup makes before making it=false note shown=true",
		"sqlite restore=false":     "Format=false Keep=false File=true Folder=false Drop what the backup makes before making it=false note shown=true",
		"sqlite restore=true":      "Format=false Keep=false File=true Folder=false Drop what the backup makes before making it=false note shown=true",
		"duckdb restore=false":     "Format=false Keep=false File=false Folder=true Drop what the backup makes before making it=false note shown=true",
		"duckdb restore=true":      "Format=false Keep=false File=false Folder=true Drop what the backup makes before making it=false note shown=true",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		tt := ui.NewTester(a.view, 1200, 800)
		for _, e := range pinEngines {
			// Redis and an unknown engine open no backup dialog
			// (TestPinBackupScope).
			r, ok := db.SQL(e)
			if !ok {
				continue
			}
			cn := addConn(a, db.Config{ID: "b-" + string(e), Name: "b", Engine: e, Host: "h", Database: "shop"})
			for _, restore := range []bool{false, true} {
				a.openBackup(cn, "", restore)
				tt.Frame()
				var shown []string
				for _, l := range labels {
					shown = append(shown, fmt.Sprintf("%s=%v", l, tt.HasText(l)))
				}
				add(fmt.Sprintf("%s restore=%v", e, restore), strings.Join(shown, " ")+
					fmt.Sprintf(" note shown=%v", testutil.HasTextContaining(tt, r.BackupNote(restore)[:20])))
				a.backup = nil
				tt.Frame()
			}
		}
	})
}

// What the Users tab's forms ask, by engine, and the users of which
// engines the app manages.
func TestPinAccountForm(t *testing.T) {
	labels := []string{"Host", "May create databases", "May create roles", "Password", "Schema", "Database", "Table",
		"SELECT", "INSERT", "UPDATE", "DELETE", "ALTER", "ALL", "ALL PRIVILEGES"}
	kinds := []struct {
		name string
		kind accountFormKind
	}{{"new user", formNewUser}, {"new role", formNewRole}, {"grant", formGrant}}
	pinValues(t, "account form", map[string]string{
		"postgres new user":   "May create databases, May create roles, Password",
		"postgres new role":   "",
		"postgres grant":      "Schema, Table, SELECT, INSERT, UPDATE, DELETE, ALL, ALL PRIVILEGES",
		"mysql new user":      "Host, Password",
		"mysql new role":      "Host",
		"mysql grant":         "Database, Table, SELECT, INSERT, UPDATE, DELETE, ALL, ALL PRIVILEGES",
		"clickhouse new user": "Password",
		"clickhouse new role": "",
		"clickhouse grant":    "Database, Table, SELECT, INSERT, ALTER, ALL",
		"sqlite open":         "No users to manage: SQLite has no users DGopher manages.",
		"duckdb open":         "No users to manage: DuckDB has no users DGopher manages.",
		"redis open":          "No users to manage: Redis has no users DGopher manages.",
		"unknown open":        "No users to manage: unknown has no users DGopher manages.",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			a := newTestApp(t)
			cn := addConn(a, db.Config{ID: "u", Name: "u", Engine: e, Host: "h"})
			if !usersSupported(e) {
				a.openUsers(cn)
				alert := ""
				if r := a.frontAlert(); r != nil {
					alert = r.report.Title + ": " + r.report.Detail
				}
				add(string(e)+" open", alert)
				continue
			}
			ut := &usersTab{a: a, conn: cn, sel: -1}
			tt := ui.NewTester(func(c *ui.Context) {
				if ut.form != nil {
					ut.formView(c)
				}
			}, 1000, 800)
			for _, k := range kinds {
				ut.openForm(k.kind)
				tt.Frame()
				var shown []string
				for _, l := range labels {
					if tt.HasText(l) {
						shown = append(shown, l)
					}
				}
				add(string(e)+" "+k.name, strings.Join(shown, ", "))
				ut.form = nil
				tt.Frame()
			}
		}
	})
}

// The sources a connection's password may come from, by engine.
func TestPinPasswordSources(t *testing.T) {
	pinValues(t, "password sources", map[string]string{
		"postgres":   "[\"System keychain\" \"Environment variable\" \"Command\" \"Ask every time\" \"Cloud identity\"]",
		"mysql":      "[\"System keychain\" \"Environment variable\" \"Command\" \"Ask every time\" \"Cloud identity\"]",
		"clickhouse": "[\"System keychain\" \"Environment variable\" \"Command\" \"Ask every time\"]",
		"sqlite":     "[\"System keychain\" \"Environment variable\" \"Command\" \"Ask every time\"]",
		"duckdb":     "[\"System keychain\" \"Environment variable\" \"Command\" \"Ask every time\"]",
		"redis":      "[\"System keychain\" \"Environment variable\" \"Command\" \"Ask every time\"]",
		"unknown":    "[\"System keychain\" \"Environment variable\" \"Command\" \"Ask every time\"]",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			add(string(e), fmt.Sprintf("%q", passwordSourcesOf(e)))
		}
	})
}

// The connection form keeps a clear-text password only for MySQL, and
// the trust question lists it for MySQL whether on or off.
func TestPinClearTextPassword(t *testing.T) {
	pinValues(t, "clear text password", map[string]string{
		"postgres":   "form engine=postgres keeps=false details=[\"on=true: yes: the password goes as it is, as LDAP and PAM logins need\"]",
		"mysql":      "form engine=mysql keeps=true details=[\"on=false: no\" \"on=true: yes: the password goes as it is, as LDAP and PAM logins need\"]",
		"clickhouse": "form engine=clickhouse keeps=false details=[\"on=true: yes: the password goes as it is, as LDAP and PAM logins need\"]",
		"sqlite":     "form engine=sqlite keeps=false details=[\"on=true: yes: the password goes as it is, as LDAP and PAM logins need\"]",
		"duckdb":     "form engine=duckdb keeps=false details=[\"on=true: yes: the password goes as it is, as LDAP and PAM logins need\"]",
		"redis":      "form engine=redis keeps=false details=[\"on=true: yes: the password goes as it is, as LDAP and PAM logins need\"]",
		"unknown":    "form engine=postgres keeps=false details=[\"on=true: yes: the password goes as it is, as LDAP and PAM logins need\"]",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		for _, e := range pinEngines {
			a.openConnForm(nil)
			f := a.connForm
			f.engine = e.Label()
			f.cfg.Name, f.cfg.Host, f.cfg.ClearTextPassword = "c", "h", true
			cfg := f.config()
			var rows []string
			for _, on := range []bool{false, true} {
				c := db.Config{Engine: e, Host: "h", ClearTextPassword: on}
				for _, d := range secretDetails(&c) {
					if d.Label == "Clear-text password" {
						rows = append(rows, fmt.Sprintf("on=%v: %s", on, d.Value))
					}
				}
			}
			add(string(e), fmt.Sprintf("form engine=%s keeps=%v details=%q", cfg.Engine, cfg.ClearTextPassword, rows))
			a.connForm = nil
		}
	})
}

// The status bar says how statements commit only where a client holds
// transactions (layout.go: IsSQL && != ClickHouse, the truth table pinned).
func TestPinCommitModeShown(t *testing.T) {
	pinValues(t, "commit mode shown", map[string]string{
		"postgres development":   "auto-commit=true manual commit=false",
		"postgres production":    "auto-commit=false manual commit=true",
		"mysql development":      "auto-commit=true manual commit=false",
		"mysql production":       "auto-commit=false manual commit=true",
		"clickhouse development": "auto-commit=false manual commit=false",
		"clickhouse production":  "auto-commit=false manual commit=false",
		"sqlite development":     "auto-commit=true manual commit=false",
		"sqlite production":      "auto-commit=false manual commit=true",
		"duckdb development":     "auto-commit=true manual commit=false",
		"duckdb production":      "auto-commit=false manual commit=true",
		"redis development":      "auto-commit=false manual commit=false",
		"redis production":       "auto-commit=false manual commit=false",
		"unknown development":    "auto-commit=true manual commit=false",
		"unknown production":     "auto-commit=false manual commit=true",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		for _, e := range pinEngines {
			for _, env := range []db.Environment{db.Development, db.Production} {
				cn := addConn(a, db.Config{ID: "s-" + string(e) + string(env), Name: "s", Engine: e, Host: "h", Env: env})
				tt := ui.NewTester(func(c *ui.Context) { a.statusOf(c, nil, []connPlaces{{conn: cn}}) }, 1000, 40)
				tt.Frame()
				add(fmt.Sprintf("%s %s", e, env), fmt.Sprintf("auto-commit=%v manual commit=%v", tt.HasText("auto-commit"), tt.HasText("manual commit")))
			}
		}
	})
}

// sqliteFile makes an empty SQLite database file of the test.
func sqliteFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The truth table of writeRows' "creating a table commits"
// (importfile.go: engine == MySQL || engine == ClickHouse) and of its
// transaction (Transactions()), by the connection's engine, on a SQLite
// pool: whether the table made is seen outside the write before its rows
// go in, and whether it stays after a write that fails.
func TestPinWriteRowsCreateCommits(t *testing.T) {
	ctx := context.Background()
	pinValues(t, "write rows", map[string]string{
		"postgres":   "table seen before the rows=false rows=0 stays=false error=\"the file ends badly\\n\\nNothing was imported, and the table was not created\"",
		"mysql":      "table seen before the rows=true rows=0 stays=false error=\"the file ends badly\\n\\nNothing was imported, and the table was not created\"",
		"clickhouse": "table seen before the rows=true rows=0 stays=false error=\"the file ends badly\\n\\nNothing was imported, and the table was not created\"",
		"sqlite":     "table seen before the rows=false rows=0 stays=false error=\"the file ends badly\\n\\nNothing was imported, and the table was not created\"",
		"duckdb":     "table seen before the rows=false rows=0 stays=false error=\"the file ends badly\\n\\nNothing was imported, and the table was not created\"",
		"redis":      "table seen before the rows=true rows=1 stays=true error=\"the file ends badly\\n\\nClickHouse has no transactions: the 1 rows before stay in t\"",
		"unknown":    "table seen before the rows=false rows=0 stays=false error=\"the file ends badly\\n\\nNothing was imported, and the table was not created\"",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		for _, e := range pinEngines {
			pool, err := db.Open(ctx, db.Config{Name: "w", Engine: db.SQLite, Database: sqliteFile(t, "w.sqlite")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			exists := func() bool {
				var n int
				if err := pool.SQL.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 't'").Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n > 0
			}
			cols := []db.Column{{Name: "a", Type: "INTEGER"}}
			target := &db.EditTarget{Dialect: pool.Dialect, Schema: "main", Table: "t", Columns: cols}
			seen := false
			w := rowWrite{target: target, cols: cols, create: `CREATE TABLE "main"."t" ("a" INTEGER)`, verb: "imported", progress: func(int64) {},
				read: func(ctx context.Context, n int, emit func([][]any) error) error {
					seen = exists()
					if err := emit([][]any{{db.Typed("1")}}); err != nil {
						return err
					}
					return errors.New("the file ends badly")
				}}
			n, err := writeRows(ctx, a, pool, db.Config{Name: "w", Engine: e}, "", w)
			add(string(e), fmt.Sprintf("table seen before the rows=%v rows=%d stays=%v error=%q", seen, n, exists(), err))
			pool.Close()
		}
	})
}

// A copy that replaces a table's rows empties it as each engine can:
// ClickHouse with TRUNCATE (which this SQLite pool refuses), the others
// with DELETE.
func TestPinCopyReplaceEmpties(t *testing.T) {
	ctx := context.Background()
	pinValues(t, "copy replace", map[string]string{
		"postgres":   "copied=2 rows after=2 error=<nil>",
		"mysql":      "copied=2 rows after=2 error=<nil>",
		"clickhouse": "copied=0 rows after=1 error=SQL logic error: near \"TRUNCATE\": syntax error (1)\n\nNothing was copied: the transaction was rolled back",
		"sqlite":     "copied=2 rows after=2 error=<nil>",
		"duckdb":     "copied=2 rows after=2 error=<nil>",
		"redis":      "copied=2 rows after=2 error=<nil>",
		"unknown":    "copied=2 rows after=2 error=<nil>",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		for _, e := range pinEngines {
			src, err := db.Open(ctx, db.Config{Name: "src", Engine: db.SQLite, Database: sqliteFile(t, "src.sqlite")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			dst, err := db.Open(ctx, db.Config{Name: "dst", Engine: db.SQLite, Database: sqliteFile(t, "dst.sqlite")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range []*db.DB{src, dst} {
				if _, err := d.SQL.Exec("CREATE TABLE t (a INTEGER)"); err != nil {
					t.Fatal(err)
				}
			}
			src.SQL.Exec("INSERT INTO t VALUES (1), (2)")
			dst.SQL.Exec("INSERT INTO t VALUES (9)")
			from := &connection.Conn{Config: db.Config{Name: "src", Engine: db.SQLite}, Store: src}
			to := &connection.Conn{Config: db.Config{Name: "dst", Engine: e}, Store: dst}
			pool := func(d *db.DB) func(context.Context) (*db.DB, error) {
				return func(context.Context) (*db.DB, error) { return d, nil }
			}
			n, err := a.copyTable(ctx, from, pool(src), "", "main", to, pool(dst), "", "main", "t", copyReplace, func(int64) {})
			var rows int
			dst.SQL.QueryRow("SELECT count(*) FROM t").Scan(&rows)
			add(string(e), fmt.Sprintf("copied=%d rows after=%d error=%v", n, rows, err))
			src.Close()
			dst.Close()
		}
	})
}

// The connection form's database field, by engine: Redis's is an index.
func TestPinConnectionFormDatabaseField(t *testing.T) {
	labels := []string{"Database", "Database index", "Database file"}
	pinValues(t, "connection form database", map[string]string{
		"postgres":  "Database=true Database index=false Database file=false",
		"mysql":     "Database=true Database index=false Database file=false",
		"sqlserver": "Database=true Database index=false Database file=false",
		// Oracle's is its service name.
		"oracle":     "Database=false Database index=false Database file=false",
		"clickhouse": "Database=true Database index=false Database file=false",
		"redis":      "Database=true Database index=true Database file=false",
		"sqlite":     "Database=false Database index=false Database file=false",
		"duckdb":     "Database=false Database index=false Database file=false",

		// The flavors, as their parents.
		"cockroachdb": "Database=true Database index=false Database file=false",
		"yugabytedb":  "Database=true Database index=false Database file=false",
		"timescaledb": "Database=true Database index=false Database file=false",
		"mariadb":     "Database=true Database index=false Database file=false",
		"tidb":        "Database=true Database index=false Database file=false",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		tt := ui.NewTester(a.view, 1200, 900)
		for _, e := range db.Engines() {
			a.openConnForm(nil)
			a.syncEngineChoice(a.connForm, e)
			tt.Frame()
			var shown []string
			for _, l := range labels {
				shown = append(shown, fmt.Sprintf("%s=%v", l, tt.HasText(l)))
			}
			add(string(e), strings.Join(shown, " "))
			a.connForm = nil
			tt.Frame()
		}
	})
}

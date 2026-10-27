package db

import (
	"context"
	"slices"
	"testing"

	"dgopher/internal/sqltext"
)

// Every engine's facts, and an unknown engine's fallbacks, stay as they
// are when the code that holds them moves.
func TestEngineFactsPinned(t *testing.T) {
	type facts struct {
		label        string
		port         int
		user         string
		lex          sqltext.Dialect
		versionQuery string
		dialect      Dialect
		backups      []BackupFormat
	}
	want := map[Engine]facts{
		Postgres:   {"PostgreSQL", 5432, "postgres", sqltext.Postgres, "SHOW server_version", postgresDialect{}, []BackupFormat{BackupArchive, BackupSQL}},
		MySQL:      {"MySQL", 3306, "root", sqltext.MySQL, "SELECT VERSION()", mysqlDialect{}, []BackupFormat{BackupSQL}},
		ClickHouse: {"ClickHouse", 9000, "default", sqltext.ClickHouse, "SELECT version()", clickhouseDialect{}, nil},
		Redis:      {"Redis", 6379, "", sqltext.Generic, "", nil, nil},
		SQLite:     {"SQLite", 0, "", sqltext.SQLite, "SELECT sqlite_version()", sqliteDialect{}, []BackupFormat{BackupCopy}},
		DuckDB:     {"DuckDB", 0, "", sqltext.Postgres, "SELECT version()", duckdbDialect{}, []BackupFormat{BackupParquet}},
		"unknown":  {"unknown", 0, "", sqltext.Generic, "", nil, nil},

		// The flavors answer as their parent, but for their names, ports
		// and users, until their own PRs.
		CockroachDB: {"CockroachDB", 26257, "root", sqltext.Postgres, "SHOW server_version", cockroachdbDialect{}, []BackupFormat{BackupSQL}},
		YugabyteDB:  {"YugabyteDB", 5433, "yugabyte", sqltext.Postgres, "SHOW server_version", yugabytedbDialect{}, []BackupFormat{BackupArchive, BackupSQL}},
		TimescaleDB: {"TimescaleDB", 5432, "postgres", sqltext.Postgres, "SHOW server_version", timescaledbDialect{}, []BackupFormat{BackupArchive, BackupSQL}},
		MariaDB:     {"MariaDB", 3306, "root", sqltext.MySQL, "SELECT VERSION()", mariadbDialect{}, []BackupFormat{BackupSQL}},
		TiDB:        {"TiDB", 4000, "root", sqltext.MySQL, "SELECT VERSION()", tidbDialect{}, []BackupFormat{BackupSQL}},

		SQLServer: {"SQL Server", 1433, "sa", sqltext.TSQL, "SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128))", sqlserverDialect{}, []BackupFormat{BackupBacpac, BackupServerFile}},
	}
	engines := append(Engines(), "unknown")
	if len(engines) != len(want) {
		t.Fatalf("engines %v, want one entry each in %v", engines, want)
	}
	for _, e := range engines {
		w, ok := want[e]
		if !ok {
			t.Fatalf("no expected facts for %q", e)
		}
		var backups []BackupFormat
		if r, ok := SQL(e); ok {
			backups = r.BackupFormats()
		}
		got := facts{e.Label(), e.DefaultPort(), e.DefaultUser(), LexDialect(e), serverVersionQuery(e), DialectOf(e), backups}
		if got.label != w.label || got.port != w.port || got.user != w.user || got.lex != w.lex || got.versionQuery != w.versionQuery {
			t.Errorf("%q: got %+v, want %+v", e, got, w)
		}
		if got.dialect != w.dialect {
			t.Errorf("%q: dialect %T, want %T", e, got.dialect, w.dialect)
		}
		if !slices.Equal(got.backups, w.backups) || (got.backups == nil) != (w.backups == nil) {
			t.Errorf("%q: backups %#v, want %#v", e, got.backups, w.backups)
		}
	}
}

// Every engine has rules of its own, the SQL ones and only they answer
// SQL's hooks, and each lexes as a dialect sqltext knows.
func TestEngineRulesCover(t *testing.T) {
	sqlEngines := []Engine{Postgres, CockroachDB, YugabyteDB, TimescaleDB, MySQL, MariaDB, TiDB, ClickHouse, SQLServer, SQLite, DuckDB}
	lexers := []sqltext.Dialect{sqltext.Generic, sqltext.Postgres, sqltext.MySQL, sqltext.ClickHouse, sqltext.SQLite, sqltext.TSQL}
	if len(registeredRules) != len(Engines()) {
		t.Errorf("%d engines have rules, want one each of %v", len(registeredRules), Engines())
	}
	for _, e := range Engines() {
		r, ok := registeredRules[e]
		if !ok {
			t.Errorf("%q has no rules", e)
			continue
		}
		if rulesOf(e) != r {
			t.Errorf("rulesOf(%q) = %T, want its registered %T", e, rulesOf(e), r)
		}
		s, isSQL := SQL(e)
		if isSQL != slices.Contains(sqlEngines, e) {
			t.Errorf("%q: SQL ok = %v, want %v", e, isSQL, !isSQL)
		}
		switch r.Kind() {
		case KindSQL, KindKeyValue:
		default:
			t.Errorf("%q: Kind() = %v, no kind", e, r.Kind())
		}
		if (r.Kind() == KindSQL) != isSQL {
			t.Errorf("%q: Kind() = %v, but SQL ok = %v", e, r.Kind(), isSQL)
		}
		if _, isKeyValue := KeyValue(e); (r.Kind() == KindKeyValue) != isKeyValue {
			t.Errorf("%q: Kind() = %v, but KeyValue ok = %v", e, r.Kind(), isKeyValue)
		}
		if isSQL {
			if s.Engine() != e {
				t.Errorf("%q: its rules' Engine() is %q", e, s.Engine())
			}
			// Hooks reached through a Dialect find the rules by its
			// Engine(): an engine reusing another's dialect would get the
			// other's rules.
			if d := DialectOf(e); d == nil || d.Engine() != e {
				t.Errorf("%q: DialectOf gives %T, whose Engine() is not %q", e, d, e)
			}
			if !slices.Contains(lexers, s.LexDialect()) {
				t.Errorf("%q: LexDialect() %v is no dialect of sqltext", e, s.LexDialect())
			}
		}
	}
	if _, ok := rulesOf("unknown").(unknownEngine); !ok {
		t.Errorf("rulesOf(unknown) = %T, want unknownEngine", rulesOf("unknown"))
	}
	if _, ok := SQL("unknown"); ok {
		t.Error("SQL(unknown) is ok: an unknown engine has no SQL rules")
	}
	if _, ok := KeyValue("unknown"); ok {
		t.Error("KeyValue(unknown) is ok: an unknown engine has no key-value rules")
	}
	if k := Engine("unknown").Kind(); k != KindSQL {
		t.Errorf("unknown engine's Kind() = %v, want KindSQL", k)
	}
}

// A URL's scheme names one engine: one claimed twice would open as
// whichever Engines() lists first.
func TestURLSchemesAreUnique(t *testing.T) {
	owner := map[string]Engine{}
	for _, e := range Engines() {
		for _, scheme := range rulesOf(e).URLSchemes() {
			if was, ok := owner[scheme]; ok {
				t.Errorf("%s:// belongs to both %q and %q", scheme, was, e)
			}
			owner[scheme] = e
		}
	}
}

// A store that failed to open is a nil interface, not a nil pointer in
// one, so callers' nil checks and closes see that nothing opened.
func TestOpenStoreFailsWithNoStore(t *testing.T) {
	for _, e := range []Engine{Postgres, Redis} {
		s, err := OpenStore(context.Background(), Config{Name: "x", Engine: e}, nil)
		if err == nil {
			t.Fatalf("%s: opened without a host", e)
		}
		if s != nil {
			t.Errorf("%s: OpenStore gave %T alongside its error, want nil", e, s)
		}
	}
}

// The connection form says whether the server refuses writes of a
// read-only connection too: TiDB and Redis have no read-only mode, so
// there the app's check is the only one.
func TestServerEnforcesReadOnly(t *testing.T) {
	want := map[Engine]bool{
		Postgres: true, CockroachDB: true, YugabyteDB: true, TimescaleDB: true, MySQL: true, MariaDB: true, TiDB: false,
		ClickHouse: true, SQLite: true, DuckDB: true, Redis: false, "unknown": false, SQLServer: false,
	}
	for _, e := range append(Engines(), "unknown") {
		w, ok := want[e]
		if !ok {
			t.Errorf("no expectation for %q", e)
		}
		if got := e.ServerEnforcesReadOnly(); got != w {
			t.Errorf("%q: %v, want %v", e, got, w)
		}
	}
}

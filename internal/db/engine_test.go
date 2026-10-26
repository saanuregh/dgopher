package db

import (
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
	sqlEngines := []Engine{Postgres, MySQL, ClickHouse, SQLite, DuckDB}
	lexers := []sqltext.Dialect{sqltext.Generic, sqltext.Postgres, sqltext.MySQL, sqltext.ClickHouse, sqltext.SQLite}
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
		if r.IsSQL() != isSQL {
			t.Errorf("%q: IsSQL() = %v, but SQL ok = %v", e, r.IsSQL(), isSQL)
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

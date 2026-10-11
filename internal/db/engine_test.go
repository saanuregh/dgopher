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
		got := facts{e.Label(), e.DefaultPort(), e.DefaultUser(), LexDialect(e), serverVersionQuery(e), DialectOf(e), BackupFormats(e)}
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

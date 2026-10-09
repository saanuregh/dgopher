// Package state keeps a project's own state, which git ignores, in one
// SQLite file: the open editors, query history, grid filters and row
// colours, and the audit log.
package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"dgopher/internal/redact"
	"dgopher/internal/store"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// FileName is the state's file in the project's .dgopher folder.
const FileName = "state.sqlite"

const (
	historyMax  = 5000
	historyKeep = 4000
)

// migrations bring the file's schema up to date, in order; user_version
// counts those applied. A change of schema is a new one at the end.
var migrations = []func(*sql.Tx) error{
	func(tx *sql.Tx) error {
		_, err := tx.Exec(`
CREATE TABLE kv (name TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE history (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	time TEXT NOT NULL,
	connection_id TEXT NOT NULL,
	connection TEXT NOT NULL,
	database TEXT NOT NULL,
	sql TEXT NOT NULL,
	duration_ns INTEGER NOT NULL,
	rows INTEGER NOT NULL,
	error TEXT NOT NULL
);
-- The audit log, in the order written: event is the entry's JSON, which
-- its hash covers. seq is not the key: a log two windows wrote at once
-- has numbers twice, which Verify reports.
CREATE TABLE audit (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	seq INTEGER NOT NULL,
	time TEXT NOT NULL,
	kind TEXT NOT NULL,
	hash TEXT NOT NULL,
	event TEXT NOT NULL
);`)
		return err
	},
}

// DB is a project's state file.
type DB struct {
	dir string
	sql *sql.DB
	// historyMu keeps the trim of one append from racing another's.
	historyMu sync.Mutex
}

// Open opens, or creates, the state file of a .dgopher folder, brings its
// schema up to date, and imports what older versions kept in files.
func Open(dir string) (*DB, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, FileName)
	// Private before SQLite writes to it; its -wal and -shm files take its mode.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	// A write transaction locks the file from its start: the audit log
	// reads its last entry and appends after it in one.
	q.Set("_txlock", "immediate")
	s, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	d := &DB{dir: dir, sql: s}
	if err := d.useWAL(); err != nil {
		s.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := d.migrate(); err != nil {
		s.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := d.importLegacy(); err != nil {
		s.Close()
		return nil, err
	}
	return d, nil
}

// useWAL turns the file to write-ahead logging, which it keeps. Changing
// the mode takes the file whole, which another window opening it at the
// same moment can hold: the busy timeout does not wait for that, so this
// does.
func (d *DB) useWAL() error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := d.sql.Exec("PRAGMA journal_mode = WAL")
		var se *sqlite.Error
		if err == nil || !errors.As(err, &se) || se.Code()&0xff != sqlite3.SQLITE_BUSY || time.Now().After(deadline) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// migrate brings the schema up to date in one transaction, which locks the
// file from its start: windows opening it at once migrate it once.
func (d *DB) migrate() error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("written by a newer DGopher (schema %d, this one knows %d)", version, len(migrations))
	}
	if version == len(migrations) {
		return nil
	}
	for i := version; i < len(migrations); i++ {
		if err := migrations[i](tx); err != nil {
			return fmt.Errorf("schema %d: %w", i+1, err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations))); err != nil {
		return err
	}
	return tx.Commit()
}

// Dir is the .dgopher folder.
func (d *DB) Dir() string { return d.dir }

// Path is the state file.
func (d *DB) Path() string { return filepath.Join(d.dir, FileName) }

// SQL is the file's connection pool, for the audit log.
func (d *DB) SQL() *sql.DB { return d.sql }

func (d *DB) Close() error { return d.sql.Close() }

// LoadJSON decodes the named value into v; store.ErrNotFound when there is none.
func (d *DB) LoadJSON(name string, v any) error {
	var data string
	err := d.sql.QueryRow("SELECT value FROM kv WHERE name = ?", name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), v)
}

// SaveJSON replaces the named value with v encoded as JSON.
func (d *DB) SaveJSON(name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec("INSERT INTO kv (name, value) VALUES (?, ?) ON CONFLICT (name) DO UPDATE SET value = excluded.value", name, string(data))
	return err
}

// AppendHistory adds an entry, its secrets redacted, and trims the
// history to the newest entries once it grows too large. An error may
// quote the statement it is about.
func (d *DB) AppendHistory(e store.HistoryEntry) error {
	e.Error = redact.Error(e.Error, e.SQL)
	e.SQL = redact.Secrets(e.SQL)
	d.historyMu.Lock()
	defer d.historyMu.Unlock()
	if err := insertHistory(d.sql, e); err != nil {
		return err
	}
	var n int
	if err := d.sql.QueryRow("SELECT count(*) FROM history").Scan(&n); err != nil || n <= historyMax {
		return err
	}
	_, err := d.sql.Exec("DELETE FROM history WHERE id NOT IN (SELECT id FROM history ORDER BY id DESC LIMIT ?)", historyKeep)
	return err
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertHistory(x execer, e store.HistoryEntry) error {
	_, err := x.Exec(`INSERT INTO history (time, connection_id, connection, database, sql, duration_ns, rows, error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, e.Time.UTC().Format(time.RFC3339Nano), e.ConnectionID, e.Connection, e.Database, e.SQL, int64(e.Duration), e.Rows, e.Error)
	return err
}

// History returns up to limit entries, newest first; limit <= 0 returns all.
func (d *DB) History(limit int) ([]store.HistoryEntry, error) {
	if limit <= 0 {
		limit = -1
	}
	rows, err := d.sql.Query(`SELECT time, connection_id, connection, database, sql, duration_ns, rows, error
FROM history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.HistoryEntry
	for rows.Next() {
		var e store.HistoryEntry
		var at string
		var dur int64
		if err := rows.Scan(&at, &e.ConnectionID, &e.Connection, &e.Database, &e.SQL, &dur, &e.Rows, &e.Error); err != nil {
			return out, err
		}
		e.Time, _ = time.Parse(time.RFC3339Nano, at)
		e.Duration = time.Duration(dur)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ClearHistory removes every entry.
func (d *DB) ClearHistory() error {
	_, err := d.sql.Exec("DELETE FROM history")
	return err
}

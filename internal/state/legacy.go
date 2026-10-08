package state

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/store"
)

// The files older versions kept in .dgopher, each kv value by its name.
var legacyValues = []string{"workspace.json", "filters.json", "colors.json"}

const (
	legacyHistory = "history.jsonl"
	importedKey   = "legacy-imported"
)

// importLegacy copies the files of an older version into the state file,
// once and whole: in one transaction, which fails, leaving the files as
// they are, when one cannot be read. The files are then renamed
// <name>.migrated, kept for whoever wants to check them.
func (d *DB) importLegacy() error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Read in the transaction, which locks the file: windows opening it at
	// once import once.
	var done int
	if err := tx.QueryRow("SELECT count(*) FROM kv WHERE name = ?", importedKey).Scan(&done); err != nil || done > 0 {
		return err
	}
	var read []string
	for _, name := range legacyValues {
		path := filepath.Join(d.dir, name)
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !json.Valid(data) {
			return fmt.Errorf("%s is not valid JSON: fix or remove it to open the project", path)
		}
		if _, err := tx.Exec("INSERT INTO kv (name, value) VALUES (?, ?)", name, string(bytes.TrimSpace(data))); err != nil {
			return err
		}
		read = append(read, path)
	}
	history, err := importHistory(tx, filepath.Join(d.dir, legacyHistory))
	if err != nil {
		return err
	}
	if history {
		read = append(read, filepath.Join(d.dir, legacyHistory))
	}
	auditFiles, entries, err := audit.ImportFiles(tx, d.dir)
	if err != nil {
		return err
	}
	read = append(read, auditFiles...)
	var rows int
	if err := tx.QueryRow("SELECT count(*) FROM audit").Scan(&rows); err != nil {
		return err
	}
	if rows != entries {
		return fmt.Errorf("the audit log's import kept %d of its %d entries", rows, entries)
	}
	stamp, _ := json.Marshal(map[string]any{"time": time.Now().UTC(), "files": len(read)})
	if _, err := tx.Exec("INSERT INTO kv (name, value) VALUES (?, ?)", importedKey, string(stamp)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, path := range read {
		// Imported already: a file left in place is only clutter.
		if err := os.Rename(path, path+".migrated"); err != nil {
			log.Printf("state: %v", err)
		}
	}
	return nil
}

// importHistory copies history.jsonl, oldest first, skipping lines that
// are not entries, as the old reader did; false when there is no file.
func importHistory(tx *sql.Tx, path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, len(data)+1)
	for sc.Scan() {
		var e store.HistoryEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if err := insertHistory(tx, e); err != nil {
			return false, err
		}
	}
	return true, sc.Err()
}

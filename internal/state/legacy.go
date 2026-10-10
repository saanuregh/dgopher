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
	"strings"
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
		if name == "workspace.json" {
			data = insideWorkspace(filepath.Dir(d.dir), data)
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

// insideWorkspace drops, from an older workspace.json, the files that
// are not inside the project folder dir, as Contains says; a file it
// cannot read as a workspace is kept as it is, for the restore to skip.
func insideWorkspace(dir string, data []byte) []byte {
	var w map[string]json.RawMessage
	if json.Unmarshal(data, &w) != nil {
		return data
	}
	for _, key := range []string{"dashboards", "models", "files"} {
		var paths []string
		if raw, ok := w[key]; !ok || json.Unmarshal(raw, &paths) != nil {
			continue
		}
		kept := []string{}
		for _, path := range paths {
			if Contains(dir, path) {
				kept = append(kept, path)
			}
		}
		w[key], _ = json.Marshal(kept)
	}
	var editors []map[string]any
	if raw, ok := w["editors"]; ok && json.Unmarshal(raw, &editors) == nil {
		var active int
		hasActive := json.Unmarshal(w["active"], &active) == nil
		kept, newActive := []map[string]any{}, -1
		for i, e := range editors {
			if path, _ := e["path"].(string); Contains(dir, path) {
				if i == active {
					newActive = len(kept)
				}
				kept = append(kept, e)
			}
		}
		active = newActive
		w["editors"], _ = json.Marshal(kept)
		if hasActive {
			w["active"], _ = json.Marshal(active)
		}
	}
	out, err := json.Marshal(w)
	if err != nil {
		return data
	}
	return out
}

// Contains says whether path, relative to the project folder dir or
// absolute, names a file inside it, once "..", and symlinks of the part
// that exists, are resolved; anything under the project's .git or
// .dgopher is not. A path starting with "~" never is, nor one through a
// symlink whose target is missing, wherever it points: writing to it
// would create that target.
func Contains(dir, path string) bool {
	if path == "" || strings.HasPrefix(path, "~") {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, filepath.FromSlash(path))
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	resolved, err := resolveExisting(filepath.Clean(path))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	first, _, _ := strings.Cut(rel, string(filepath.Separator))
	return !strings.EqualFold(first, ".git") && !strings.EqualFold(first, ".dgopher")
}

// resolveExisting resolves the symlinks of path's longest existing part
// and joins the rest to it; a symlink whose target is missing is an
// error.
func resolveExisting(path string) (string, error) {
	var rest []string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		// path is there, yet does not resolve: a symlink to nothing.
		if _, err := os.Lstat(path); err == nil {
			return "", fmt.Errorf("%s: a symlink whose target is missing", path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		rest = append([]string{filepath.Base(path)}, rest...)
		path = parent
	}
}

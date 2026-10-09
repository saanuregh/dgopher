// Package project reads and writes a project folder: its dgopher.json of
// connections, snippets and virtual keys, and its .dgopher folder.
package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/state"
)

// A project is a folder, as a Git repository, that holds connections,
// queries and snippets to share: dgopher.json lists the connections and
// snippets, without secrets, and the queries are .sql files. What is the
// user's own (history, open files, the audit log) is in its .dgopher
// folder, which git ignores; passwords are in the keychain.
const File = "dgopher.json"

// localDir is the project's folder of the user's own state. Its
// .gitignore ignores everything in it, itself included.
const LocalDir = ".dgopher"

// Config is the content of dgopher.json.
type Config struct {
	// Queries is the folder of the .sql files, relative to the project.
	Queries     string      `json:"queries,omitempty"`
	Connections []db.Config `json:"connections"`
	Snippets    []Snippet   `json:"snippets,omitempty"`
	// VirtualKeys are the columns that tell rows apart in tables without
	// a key, by "connection/schema.table", as the team agreed on them.
	VirtualKeys map[string][]string `json:"virtualKeys,omitempty"`
	// HiddenValues are the columns whose values the grids hide on
	// screen, by "connection/schema.table", as the team agreed on them.
	HiddenValues map[string][]string `json:"hiddenValues,omitempty"`
}

type Project struct {
	Dir     string
	Name    string
	Queries string // absolute
	Prefix  string // what the IDs of its connections start with, in the app
	Files   []string
	Scanned time.Time
	// Err says why the folder could not be read: the project stays in
	// the sidebar, without connections, until it is removed or reloaded.
	Err string
	// ScanErr says why the queries could not all be listed.
	ScanErr      string
	Snippets     []Snippet
	VirtualKeys  map[string][]string
	HiddenValues map[string][]string
	Local        *state.DB  // the .dgopher folder's state file
	Audit        *audit.Log // nil when it could not be opened, as auditErr says
	AuditErr     string
	// disk is dgopher.json as last read or written: a file that differs
	// was changed by someone else, as by a git pull, and is not
	// overwritten.
	disk []byte
	// Workspace is .dgopher/workspace.json as last read or written.
	Workspace []byte
}

// Prefix scopes a project's connection IDs, and their secrets in
// the keychain, to its folder: two clones may name connections alike.
func Prefix(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return "p-" + hex.EncodeToString(sum[:4]) + "-"
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns a name into an ID that reads well in a file under review.
func Slug(name string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		return "connection"
	}
	return s
}

// Load reads a project folder, creating dgopher.json when create
// is set and it has none. The project it returns names its folder even
// when it fails, for the sidebar to show why.
func Load(dir string, create bool) (*Project, Config, error) {
	p := &Project{Dir: dir, Name: filepath.Base(dir), Prefix: Prefix(dir)}
	var pc Config
	if info, err := os.Stat(dir); err != nil {
		return p, pc, err
	} else if !info.IsDir() {
		return p, pc, fmt.Errorf("%s is not a folder", dir)
	}
	path := filepath.Join(dir, File)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist) && create:
		pc = Config{Queries: "queries", Connections: []db.Config{}}
		if data, err = encodeProject(pc); err != nil {
			return p, pc, err
		}
		if err := writeProjectFile(path, data); err != nil {
			return p, pc, err
		}
		if err := os.MkdirAll(filepath.Join(dir, pc.Queries), 0o755); err != nil {
			return p, pc, err
		}
	case errors.Is(err, fs.ErrNotExist):
		return p, pc, fmt.Errorf("%s has no %s", dir, File)
	case err != nil:
		return p, pc, err
	default:
		if err := json.Unmarshal(data, &pc); err != nil {
			return p, pc, fmt.Errorf("%s: %w", path, err)
		}
	}
	p.disk = data
	if pc.Queries == "" {
		pc.Queries = "queries"
	}
	p.Queries = filepath.Join(dir, filepath.Clean(filepath.FromSlash(pc.Queries)))
	if rel, err := filepath.Rel(dir, p.Queries); err != nil || isOutside(rel) {
		return p, pc, fmt.Errorf("the queries folder %q is outside the project", pc.Queries)
	}
	local, err := state.Open(filepath.Join(dir, LocalDir))
	if err != nil {
		return p, pc, err
	}
	ignore := filepath.Join(local.Dir(), ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(ignore, []byte("*\n"), 0o644); err != nil {
			return p, pc, err
		}
	}
	p.Local = local
	if l, err := audit.Open(local.SQL()); err != nil {
		log.Println("audit:", err) // the project still works; the viewer says why
		p.AuditErr = err.Error()
	} else {
		p.Audit = l
	}
	p.Snippets, p.VirtualKeys, p.HiddenValues = pc.Snippets, pc.VirtualKeys, pc.HiddenValues
	return p, pc, nil
}

func isOutside(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel)
}

// Close closes the project's state file.
func (p *Project) Close() error {
	if p.Local == nil {
		return nil
	}
	return p.Local.Close()
}

// ResolvePath turns a database file's path from dgopher.json, relative
// to the project so that it holds in every clone, into one to open.
func (p *Project) ResolvePath(path string) string {
	if path == "" || path == ":memory:" || filepath.IsAbs(path) || strings.HasPrefix(path, "~") {
		return path
	}
	return filepath.Join(p.Dir, filepath.FromSlash(path))
}

// StoredPath is resolvePath's reverse, for files inside the project.
func (p *Project) StoredPath(path string) string {
	if !filepath.IsAbs(path) {
		return path
	}
	rel, err := filepath.Rel(p.Dir, path)
	if err != nil || isOutside(rel) {
		return path
	}
	return filepath.ToSlash(rel)
}

var errChanged = errors.New("the file changed on disk since it was read, as by a git pull. Reload the project from its menu in the sidebar, then make the change again")

// Writable says why the project's file may not be written now. Callers
// ask before changing anything else, as the keychain, so that a refused
// save leaves nothing half done.
func (p *Project) Writable() error {
	if p.Err != "" {
		return errors.New(p.Err)
	}
	if disk, err := os.ReadFile(filepath.Join(p.Dir, File)); err == nil && !bytes.Equal(disk, p.disk) {
		return errChanged
	}
	return nil
}

func (p *Project) Save(cfgs []db.Config) error {
	if err := p.Writable(); err != nil {
		return err
	}
	pc := Config{Connections: []db.Config{}, Snippets: p.Snippets, VirtualKeys: p.VirtualKeys, HiddenValues: p.HiddenValues}
	if rel, err := filepath.Rel(p.Dir, p.Queries); err == nil && rel != "queries" {
		pc.Queries = filepath.ToSlash(rel)
	}
	for _, cfg := range cfgs {
		cfg.ID = strings.TrimPrefix(cfg.ID, p.Prefix)
		cfg.Password, cfg.SSH.Password, cfg.SSH.KeyPassphrase = "", "", ""
		if cfg.Engine.IsFile() {
			cfg.Database = p.StoredPath(cfg.Database)
		}
		pc.Connections = append(pc.Connections, cfg)
	}
	sort.SliceStable(pc.Connections, func(i, j int) bool { return pc.Connections[i].ID < pc.Connections[j].ID })
	sort.SliceStable(pc.Snippets, func(i, j int) bool {
		return strings.ToLower(pc.Snippets[i].Name) < strings.ToLower(pc.Snippets[j].Name)
	})
	data, err := encodeProject(pc)
	if err != nil {
		return err
	}
	if err := writeProjectFile(filepath.Join(p.Dir, File), data); err != nil {
		return err
	}
	p.disk = data
	return nil
}

// encodeProject renders JSON for people to read and diff: indented, with
// a final newline.
func encodeProject(pc Config) ([]byte, error) {
	data, err := json.MarshalIndent(pc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ErrChanged is a save refused: the file changed since it was read, as by
// a pull of a teammate's version, which the save would undo.
var ErrChanged = errors.New("the file changed on disk since it was read: reload it to see what changed")

// SaveShared writes a file a project shares with its team, as a
// dashboard, whole or not at all, when it still holds was, as read; a new
// one, when there is none.
func SaveShared(path string, was, data []byte) error {
	switch disk, err := os.ReadFile(path); {
	case err == nil && (was == nil || !bytes.Equal(disk, was)):
		return ErrChanged
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	case err != nil && was != nil:
		return ErrChanged // deleted since
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".shared-*.json")
	if err != nil {
		return err
	}
	if err = tmp.Chmod(0o644); err == nil {
		if _, err = tmp.Write(data); err == nil {
			err = tmp.Sync()
		}
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// ListFiles lists the JSON files of a project's folder, in order; none
// when the folder is missing.
func ListFiles(p *Project, folder string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(p.Dir, folder))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, filepath.Join(p.Dir, folder, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// LoadJSON reads a shared file into a T that check accepts, with the
// file's bytes, which a save checks the file still holds.
func LoadJSON[T any](path string, check func(*T) error) (*T, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if err := check(&v); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return &v, data, nil
}

// SaveJSON writes v, indented, to a shared file as SaveShared does, and
// returns what it wrote.
func SaveJSON(path string, was []byte, v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := SaveShared(path, was, data); err != nil {
		return nil, err
	}
	return data, nil
}

// writeProjectFile replaces the file at once.
func writeProjectFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dgopher-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Shared with the team: readable by the group, as other files of a
	// repository are, but it holds no secrets.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// headerLines is how many lines at the top of a query file may name its
// connection.
const headerLines = 5

var (
	connHeader = regexp.MustCompile(`^\s*--\s*connection:\s*(\S+)`)
	// connHeaderTyped is a header line up to a caret in its connection.
	connHeaderTyped = regexp.MustCompile(`^\s*--\s*connection:\s*(\S*)$`)
)

// HeaderConnection returns the connection a query file names in one of
// its first lines, "" when it names none.
func HeaderConnection(text string) string {
	for i, line := range strings.SplitN(text, "\n", headerLines+1) {
		if i == headerLines {
			break
		}
		if m := connHeader.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// HeaderAt reports whether caret, a rune offset into text, is at the end
// of the connection a header line names, as while typing it, and where
// that name starts and what of it is typed.
func HeaderAt(text string, caret int) (start int, typed string, ok bool) {
	runes := []rune(text)
	caret = max(0, min(caret, len(runes)))
	line := caret
	for line > 0 && runes[line-1] != '\n' {
		line--
	}
	if strings.Count(string(runes[:line]), "\n") >= headerLines || caret < len(runes) && !unicode.IsSpace(runes[caret]) {
		return 0, "", false
	}
	m := connHeaderTyped.FindStringSubmatch(string(runes[line:caret]))
	if m == nil {
		return 0, "", false
	}
	return caret - utf8.RuneCountInString(m[1]), m[1], true
}

// Snippet is a piece of SQL saved under a name in a project's
// dgopher.json, for any of its connections.
type Snippet struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
	// Keyword is what, typed in an editor, completes to the snippet.
	Keyword string `json:"keyword,omitempty"`
}

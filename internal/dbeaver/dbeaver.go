// Package dbeaver reads the connections DBeaver Community Edition saved.
package dbeaver

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/db"
)

// Project is one DBeaver project: a folder holding .dbeaver/.
type Project struct {
	Name string // the folder's name, as DBeaver shows it ("General")
	Dir  string // the project folder, parent of .dbeaver
}

// ErrLocked is returned for a project DBeaver encrypts with a project
// password, which this package cannot read.
var ErrLocked = errors.New("the DBeaver project is protected by a project password")

// metadataFolder is the folder of a project DBeaver keeps its files in.
const metadataFolder = ".dbeaver"

// DataDirs lists the DBeaver data folders (DBeaverData) that exist on this machine,
// in the order: the platform's default, the dotted Linux fallback, Flatpak, Snap.
func DataDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var dirs []string
	for _, dir := range candidateDataDirs(runtime.GOOS, home, os.Getenv("APPDATA")) {
		if isDir(dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// candidateDataDirs are the folders DBeaver keeps its data in on goos,
// for the user whose home is home; appData is Windows' %APPDATA%.
func candidateDataDirs(goos, home, appData string) []string {
	switch goos {
	case "windows":
		return []string{filepath.Join(cmp.Or(appData, filepath.Join(home, "AppData", "Roaming")), "DBeaverData")}
	case "darwin":
		return []string{filepath.Join(home, "Library", "DBeaverData")}
	}
	return []string{
		filepath.Join(home, ".local", "share", "DBeaverData"),
		filepath.Join(home, ".local", "share", ".DBeaverData"),
		filepath.Join(home, ".var", "app", "io.dbeaver.DBeaverCommunity", "data", "DBeaverData"),
		filepath.Join(home, "snap", "dbeaver-ce", "current", ".local", "share", "DBeaverData"),
	}
}

// Projects lists the projects under dir, which may be a DBeaverData folder, its workspace6,
// a project folder, or a .dbeaver folder. Projects without data-sources*.json are left out.
func Projects(dir string) ([]Project, error) {
	if filepath.Base(dir) == metadataFolder {
		dir = filepath.Dir(dir)
	}
	if isDir(filepath.Join(dir, metadataFolder)) {
		if len(dataSourceFiles(dir)) == 0 {
			return nil, nil
		}
		return []Project{{Name: filepath.Base(dir), Dir: dir}}, nil
	}
	if ws := filepath.Join(dir, "workspace6"); isDir(ws) {
		dir = ws
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var projects []Project
	for _, e := range entries {
		sub := filepath.Join(dir, e.Name())
		if e.IsDir() && len(dataSourceFiles(sub)) > 0 {
			projects = append(projects, Project{Name: e.Name(), Dir: sub})
		}
	}
	return projects, nil
}

// dataSourceFiles are a project's data-sources*.json, the default
// data-sources.json first, then by name.
func dataSourceFiles(projectDir string) []string {
	files, _ := filepath.Glob(filepath.Join(projectDir, metadataFolder, "data-sources*.json"))
	slices.SortFunc(files, func(a, b string) int {
		aDefault, bDefault := filepath.Base(a) == "data-sources.json", filepath.Base(b) == "data-sources.json"
		if aDefault != bDefault {
			if aDefault {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	return files
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// Entry is one saved connection, as DGopher would save it.
type Entry struct {
	ID     string    // DBeaver's connection id
	Folder string    // DBeaver's folder path, "" for none (shown only)
	Config db.Config // Name and every mapped field, secrets included; ID left empty
	Skip   string    // why it cannot be imported; "" when it can
	Driver string    // DBeaver's driver id, shown for an engine DGopher lacks
	// EnvChosen is whether the user chose Config.Env in DBeaver: false
	// for its default type, dev.
	EnvChosen bool
	Notes     []string // what did not carry over, one short sentence each
}

// HasSecrets reports whether the entry carries a saved password of any kind.
func (e *Entry) HasSecrets() bool {
	c := &e.Config
	return c.Password != "" || c.SSH.Password != "" || c.SSH.KeyPassphrase != "" || c.Proxy.Password != ""
}

// Read reads every data-sources*.json of a project with its credentials file, in
// file then name order. A project locked with a DBeaver project password returns
// ErrLocked. A credentials file that does not decrypt is an error naming the file,
// wrapping the cause.
func Read(p Project) ([]Entry, error) {
	if locked(p.Dir) {
		return nil, ErrLocked
	}
	var entries []Entry
	for _, path := range dataSourceFiles(p.Dir) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		trimmed := bytes.TrimSpace(data)
		if len(trimmed) > 0 && trimmed[0] != '{' {
			// DBeaver encrypts the whole file in a project with a password.
			return nil, fmt.Errorf("%s: %w", path, ErrLocked)
		}
		var file dataSourcesFile
		if err := json.Unmarshal(data, &file); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		suffix := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "data-sources"), ".json")
		creds, err := readCredentials(filepath.Join(filepath.Dir(path), "credentials-config"+suffix+".json"))
		if err != nil {
			return nil, err
		}
		var mapped []Entry
		for id, c := range file.Connections {
			mapped = append(mapped, mapConnection(id, c, file.ConnectionTypes, creds))
		}
		slices.SortFunc(mapped, func(a, b Entry) int {
			return cmp.Or(cmp.Compare(strings.ToLower(a.Config.Name), strings.ToLower(b.Config.Name)), cmp.Compare(a.ID, b.ID))
		})
		entries = append(entries, mapped...)
	}
	return entries, nil
}

// locked reports whether project-settings.json marks the project as
// protected by a password: secureProject encrypts the configuration,
// useProjectPassword the credentials (DataSourceSerializerModern.java).
func locked(projectDir string) bool {
	data, err := os.ReadFile(filepath.Join(projectDir, metadataFolder, "project-settings.json"))
	if err != nil {
		return false
	}
	var settings map[string]any
	if json.Unmarshal(data, &settings) != nil {
		return false
	}
	return text(settings["secureProject"]) == "true" || text(settings["useProjectPassword"]) == "true"
}

// dataSourcesFile is the part of a data-sources*.json this package reads.
type dataSourcesFile struct {
	Connections     map[string]connection     `json:"connections"`
	ConnectionTypes map[string]connectionType `json:"connection-types"`
}

type connection struct {
	Provider      string        `json:"provider"`
	Driver        string        `json:"driver"`
	Name          string        `json:"name"`
	Folder        string        `json:"folder"`
	ReadOnly      bool          `json:"read-only"`
	Configuration configuration `json:"configuration"`
}

type configuration struct {
	Host               string             `json:"host"`
	Port               any                `json:"port"` // a string, in the files seen, or a number
	Database           string             `json:"database"`
	URL                string             `json:"url"`
	ConfigurationType  string             `json:"configurationType"`
	Type               string             `json:"type"`
	User               string             `json:"user"`
	Password           string             `json:"password"`
	AuthModel          string             `json:"auth-model"`
	ProviderProperties map[string]any     `json:"provider-properties"`
	Handlers           map[string]handler `json:"handlers"`
	Bootstrap          struct {
		Autocommit *bool `json:"autocommit"`
	} `json:"bootstrap"`
}

// handler is a network handler: SSH, TLS or a proxy.
type handler struct {
	Enabled    bool           `json:"enabled"`
	User       string         `json:"user"`
	Password   string         `json:"password"`
	Properties map[string]any `json:"properties"`
}

func (h handler) prop(key string) string { return text(h.Properties[key]) }

type connectionType struct {
	AutoCommit        *bool `json:"auto-commit"`
	ConfirmExecute    bool  `json:"confirm-execute"`
	ConfirmDataChange bool  `json:"confirm-data-change"`
}

// text is a JSON value as DBeaver's string properties hold it.
func text(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func port(v any) int {
	n, _ := strconv.Atoi(strings.TrimSpace(text(v)))
	return n
}

// Package store persists the app's settings and secrets locally.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultDir returns the config directory; DGOPHER_CONFIG_DIR overrides it.
func DefaultDir() (string, error) {
	if d := os.Getenv("DGOPHER_CONFIG_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "dgopher"), nil
}

// DefaultDataDir returns the folder of what the app keeps as it runs, as
// the layout of its windows: DGOPHER_DATA_DIR, else DGOPHER_CONFIG_DIR's
// folder, so that a run pointed away from the real config keeps away from
// the real data too, else the platform's data folder.
func DefaultDataDir() (string, error) {
	if d := os.Getenv("DGOPHER_DATA_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("DGOPHER_CONFIG_DIR"); d != "" {
		return d, nil
	}
	base, err := userDataDir(runtime.GOOS)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "dgopher"), nil
}

// userDataDir is a system's folder for the data of its users' apps:
// %LOCALAPPDATA% on Windows, Application Support on macOS, and
// $XDG_DATA_HOME elsewhere, ~/.local/share when it is unset or, against
// the XDG specification, not absolute.
func userDataDir(goos string) (string, error) {
	if goos == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return d, nil
		}
		return "", errors.New("%LOCALAPPDATA% is not set")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if goos == "darwin" || goos == "ios" {
		return filepath.Join(home, "Library", "Application Support"), nil
	}
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return d, nil
	}
	return filepath.Join(home, ".local", "share"), nil
}

// Store is a directory of private JSON files plus a Secrets backend.
type Store struct {
	dir     string
	secrets Secrets
}

// Open creates dir with mode 0700 and tightens it if it is group or world accessible.
func Open(dir string, secrets Secrets) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, err
		}
	}
	return &Store{dir: dir, secrets: secrets}, nil
}

func (s *Store) Dir() string      { return s.dir }
func (s *Store) Secrets() Secrets { return s.secrets }

func (s *Store) path(name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "..") ||
		strings.ContainsAny(name, `/\`) || filepath.Base(name) != name {
		return "", fmt.Errorf("store: invalid file name %q", name)
	}
	return filepath.Join(s.dir, name), nil
}

// LoadJSON decodes the named file into v; ErrNotFound if it does not exist.
func (s *Store) LoadJSON(name string, v any) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// SaveJSON atomically replaces the named file with v encoded as indented JSON.
func (s *Store) SaveJSON(name string, v any) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(p, append(data, '\n'))
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}

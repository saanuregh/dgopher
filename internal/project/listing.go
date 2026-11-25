package project

import (
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Listing is what a folder of a project's files holds: its files of one
// extension and the folders under it, as / paths relative to it, sorted.
type Listing struct {
	Files    []string
	Folders  []string
	Modified map[string]time.Time // by file
}

// List lists the files under root whose extension is ext, in any case,
// and the folders, at any depth; it skips a folder whose name starts with
// a dot, as .git. A missing root holds nothing.
func List(root, ext string) (Listing, error) {
	l := Listing{Modified: map[string]time.Time{}}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir() && strings.HasPrefix(d.Name(), "."):
			return filepath.SkipDir
		case d.IsDir():
			l.Folders = append(l.Folders, rel)
		case strings.EqualFold(filepath.Ext(path), ext):
			l.Files = append(l.Files, rel)
			if info, err := d.Info(); err == nil {
				l.Modified[rel] = info.ModTime()
			}
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	// A folder's files come right after it as walked; sorted, "a-b" goes
	// before "a/b", as callers compare.
	slices.Sort(l.Files)
	slices.Sort(l.Folders)
	return l, err
}

// ListFiles lists the JSON files under a project's folder, at any depth,
// as absolute paths, in order; none when it is missing.
func ListFiles(p *Project, folder string) ([]string, error) {
	root := filepath.Join(p.Dir, folder)
	l, err := List(root, ".json")
	out := make([]string, len(l.Files))
	for i, rel := range l.Files {
		out[i] = filepath.Join(root, filepath.FromSlash(rel))
	}
	return out, err
}

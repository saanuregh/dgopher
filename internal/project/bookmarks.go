package project

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// BookmarksFile holds a project's bookmarks, beside its dgopher.json:
// shared with the project, as DBeaver keeps bookmarks in its project.
const BookmarksFile = "bookmarks.json"

// Bookmark is a link to a database object or to a file of the project,
// listed at the top of the navigator's files pane. It names either an
// object, by its place, or a file.
type Bookmark struct {
	Name string `json:"name"` // what it shows as
	// Connection is the ID in dgopher.json of the object's connection;
	// Database its database, "" for the connection's own; Object a
	// table's or a view's name, or an item's label. Kind is a table's or
	// a view's db.ObjectKind; Item an item's db.ItemKind instead, as
	// "function".
	Connection string `json:"connection,omitempty"`
	Database   string `json:"database,omitempty"`
	Schema     string `json:"schema,omitempty"`
	Object     string `json:"object,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Item       string `json:"item,omitempty"`
	// File is a file's path in the project folder, / separated.
	File string `json:"file,omitempty"`
}

// Target names what a bookmark links to: two bookmarks of one target are
// one.
func (b Bookmark) Target() string {
	if b.File != "" {
		return "file\x00" + b.File
	}
	return "object\x00" + b.Connection + "\x00" + b.Database + "\x00" + b.Schema + "\x00" + b.Item + "\x00" + b.Object
}

// bookmarks is the content of BookmarksFile.
type bookmarks struct {
	Bookmarks []Bookmark `json:"bookmarks"`
}

func (f *bookmarks) check() error {
	for _, b := range f.Bookmarks {
		switch {
		case b.Name == "":
			return errors.New("a bookmark has no name")
		case (b.File == "") == (b.Object == ""):
			return errors.New("a bookmark " + b.Name + " needs a file or an object, one of them")
		case b.Object != "" && b.Connection == "":
			return errors.New("the bookmark " + b.Name + " has no connection")
		}
	}
	return nil
}

// LoadBookmarks reads a project's bookmarks, with the file's bytes, which
// a save checks the file still holds; none, and no bytes, without a file.
func LoadBookmarks(p *Project) ([]Bookmark, []byte, error) {
	f, data, err := LoadJSON(filepath.Join(p.Dir, BookmarksFile), (*bookmarks).check)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return f.Bookmarks, data, nil
}

// SaveBookmarks writes a project's bookmarks, when the file still holds
// was, as read, and returns what it wrote; none removes the file, which a
// project without bookmarks does not have.
func SaveBookmarks(p *Project, list []Bookmark, was []byte) ([]byte, error) {
	path := filepath.Join(p.Dir, BookmarksFile)
	if len(list) > 0 {
		return SaveJSON(path, was, bookmarks{Bookmarks: list})
	}
	switch disk, err := os.ReadFile(path); {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	case !bytes.Equal(disk, was):
		return nil, ErrChanged
	}
	return nil, os.Remove(path)
}

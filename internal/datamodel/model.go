// Package datamodel is a data model: the tables of a schema, kept in a
// project's file for its team, written as DDL for any SQL engine, and
// compared with another model or a database, which a migration then makes
// like it.
package datamodel

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/project"
)

// Folder is where a project keeps its data models.
const Folder = "models"

// Model is a data model as its file keeps it.
type Model struct {
	Name   string    `json:"name"`
	Engine db.Engine `json:"engine"`
	// Source is where the model was built from, to build it again.
	Source *Source          `json:"source,omitempty"`
	Tables []db.TableDesign `json:"tables"`
}

// Source is the schema a model was built from.
type Source struct {
	// Connection is the connection's ID in the project's dgopher.json.
	Connection string `json:"connection"`
	Database   string `json:"database,omitempty"`
	Schema     string `json:"schema"`
}

// File is a model's file in a project, by its name.
func File(p *project.Project, name string) string {
	return filepath.Join(p.Dir, Folder, project.Slug(name)+".json")
}

// List names a project's models, by their files, in order.
func List(p *project.Project) ([]string, error) {
	return project.ListFiles(p, Folder)
}

// Load reads a model's file.
func Load(path string) (*Model, error) {
	m, _, err := LoadFile(path)
	return m, err
}

// LoadFile reads a model's file, with its bytes, which a save checks the
// file still holds.
func LoadFile(path string) (*Model, []byte, error) {
	return project.LoadJSON(path, (*Model).Check)
}

// Check refuses a model its file holds wrong, as edited by hand.
func (m *Model) Check() error {
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("the model has no name")
	}
	if !Engine(m.Engine) {
		return fmt.Errorf("%q is not an engine a model is of", m.Engine)
	}
	seen := map[string]bool{}
	for _, t := range m.Tables {
		key := t.Schema + "." + t.Name
		switch {
		case strings.TrimSpace(t.Name) == "":
			return errors.New("a table has no name")
		case seen[key]:
			return fmt.Errorf("the table %s is in the model twice", key)
		case len(t.Columns) == 0:
			return fmt.Errorf("the table %s has no columns", t.Name)
		}
		seen[key] = true
		for _, c := range t.Columns {
			if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Type) == "" {
				return fmt.Errorf("a column of %s has no name or no type", t.Name)
			}
		}
	}
	return nil
}

// Engine reports whether a model may be of an engine: one of SQL tables.
func Engine(e db.Engine) bool { return e != db.Redis && db.DialectOf(e) != nil }

// Table finds a table of the model by its schema and name.
func (m *Model) Table(schema, name string) (db.TableDesign, bool) {
	i := slices.IndexFunc(m.Tables, func(t db.TableDesign) bool { return t.Schema == schema && t.Name == name })
	if i < 0 {
		return db.TableDesign{}, false
	}
	return m.Tables[i], true
}

// Save writes a model's file, its tables in order, whole or not at all,
// when it still holds was, as read; a new one, when there is none. It
// returns what it wrote.
func (m *Model) Save(path string, was []byte) ([]byte, error) {
	m.sortTables()
	return project.SaveJSON(path, was, m)
}

// Move writes a model's file at to in place of path, which must still
// hold was, as project.MoveJSON does.
func (m *Model) Move(path, to string, was []byte) ([]byte, error) {
	m.sortTables()
	return project.MoveJSON(path, to, was, m)
}

// sortTables orders the tables as the file keeps them.
func (m *Model) sortTables() {
	slices.SortFunc(m.Tables, func(a, b db.TableDesign) int {
		return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
	})
}

// Build reads tables of a schema into a model's: those named, or every
// table for none. done is told how many are read as each is. Notes say
// what of a table the model does not keep.
func Build(ctx context.Context, d *db.DB, schema string, names []string, done func(int)) (tables []db.TableDesign, notes []string, err error) {
	objs, err := d.Dialect.Objects(ctx, d.Catalog(), schema)
	if err != nil {
		return nil, nil, err
	}
	for _, o := range objs {
		if o.Kind != db.KindTable || names != nil && !slices.Contains(names, o.Name) {
			continue
		}
		t, err := db.ReadTableDesign(ctx, d, o)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", o.Name, err)
		}
		if unkept := t.Unkept(); unkept != "" {
			notes = append(notes, fmt.Sprintf("%s has %s, which the model does not keep.", o.Name, unkept))
		}
		tables = append(tables, Modelled(d.Dialect.Engine(), t))
		if done != nil {
			done(len(tables))
		}
	}
	return tables, notes, nil
}

// Modelled is a table of an engine as a model keeps it: what only
// changes a table read, as its columns' names before, is left out.
func Modelled(e db.Engine, t db.TableDesign) db.TableDesign {
	for i := range t.Columns {
		t.Columns[i].Was = ""
	}
	for i := range t.Indexes {
		t.Indexes[i].Read = false
	}
	for i := range t.ForeignKeys {
		t.ForeignKeys[i].Read, t.ForeignKeys[i].Definition = false, ""
	}
	for i := range t.Checks {
		t.Checks[i].Read = false
	}
	if e == db.MySQL {
		// MySQL makes an index for a key, which it names as the key: the
		// key makes it again.
		t.Indexes = slices.DeleteFunc(t.Indexes, func(ix db.IndexDesign) bool {
			return slices.ContainsFunc(t.ForeignKeys, func(fk db.ForeignKeyDesign) bool {
				return fk.Name == ix.Name && slices.Equal(fk.Columns, ix.Columns[:min(len(fk.Columns), len(ix.Columns))])
			})
		})
	}
	if e == db.ClickHouse {
		// Its sorting key is the primary key's columns, written from them.
		t.Indexes = slices.DeleteFunc(t.Indexes, func(ix db.IndexDesign) bool { return ix.Name == "ORDER BY" })
		t.PrimaryKeyName = ""
	}
	return t
}

package dataview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dgopher/internal/db"
)

// unreadable fails to read the columns of table cols, the foreign keys
// of table fks, and, when refs is set, which tables refer to any.
type unreadable struct {
	db.Dialect
	cols, fks string
	refs      bool
}

func (u unreadable) Columns(ctx context.Context, q db.Querier, schema, table string) ([]db.Column, error) {
	if table == u.cols {
		return nil, errors.New("no columns for you")
	}
	return u.Dialect.Columns(ctx, q, schema, table)
}

func (u unreadable) ForeignKeys(ctx context.Context, q db.Querier, schema, table string) ([]db.ForeignKey, error) {
	if table == u.fks {
		return nil, errors.New("no keys for you")
	}
	return u.Dialect.ForeignKeys(ctx, q, schema, table)
}

func (u unreadable) ReferencedBy(ctx context.Context, q db.Querier, schema, table string) ([]db.Reference, error) {
	if u.refs {
		return nil, errors.New("no references for you")
	}
	return u.Dialect.ReferencedBy(ctx, q, schema, table)
}

// A table diagram draws a neighbour it cannot read as a box saying so,
// and says which foreign keys it could not read, rather than failing.
func TestTableDiagramKeepsUnreadableNeighbours(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, db.Config{Name: "m", Engine: db.SQLite, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{
		"CREATE TABLE a (id INTEGER PRIMARY KEY)",
		"CREATE TABLE b (id INTEGER PRIMARY KEY, a_id INTEGER REFERENCES a(id))",
		"CREATE TABLE c (id INTEGER PRIMARY KEY, a_id INTEGER REFERENCES a(id))",
	} {
		if _, err := d.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	real := d.Dialect
	focus := db.Object{Schema: "main", Name: "a", Kind: db.KindTable}

	d.Dialect = unreadable{Dialect: real, cols: "b", fks: "c"}
	tables, notes, err := loadNeighbours(ctx, d, focus)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*erTable{}
	for _, tb := range tables {
		byName[tb.obj.Name] = tb
	}
	if len(tables) != 3 || byName["b"] == nil || byName["b"].unreadable == "" || byName["c"] == nil || byName["c"].unreadable != "" {
		t.Fatalf("tables %+v", tables)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "c") || !strings.Contains(notes[0], "no keys for you") {
		t.Fatalf("notes %q", notes)
	}

	d.Dialect = unreadable{Dialect: real, refs: true}
	tables, notes, err = loadNeighbours(ctx, d, focus)
	if err != nil || len(tables) != 1 || len(notes) != 1 || !strings.Contains(notes[0], "no references for you") {
		t.Fatalf("%d tables, notes %q: %v", len(tables), notes, err)
	}
}

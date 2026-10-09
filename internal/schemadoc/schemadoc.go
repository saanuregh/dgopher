// Package schemadoc writes what a schema holds, as the script creating
// its objects or as documentation of it.
package schemadoc

import (
	"context"
	"sync"
	"sync/atomic"

	"dgopher/internal/db"
)

// Table is a table or a view, with its definition and, when its
// structure was read, its columns, indexes and keys.
type Table struct {
	db.Object
	Definition   string
	Columns      []db.Column
	Indexes      []db.Index
	ForeignKeys  []db.ForeignKey
	ReferencedBy []db.Reference
	// Err says why it could not be read, "" when it was.
	Err string
}

// Item is a routine, a trigger or another object besides tables and
// views, with its definition.
type Item struct {
	db.Item
	Definition string
	Err        string
}

// Schema is the objects of a schema that were read, in the order asked.
type Schema struct {
	Engine db.Engine
	Tables []Table
	Items  []Item
}

// readers is how many objects are read at once.
const readers = 4

// Read reads the definitions of tables, views and items, and with
// structure the columns, indexes and keys of the tables and views. An
// object that cannot be read keeps why in its Err; Read fails only when
// ctx ends. progress, when set, hears how many objects of all are read,
// from other goroutines.
func Read(ctx context.Context, d *db.DB, objs []db.Object, items []db.Item, structure bool, progress func(done, total int)) (*Schema, error) {
	s := &Schema{Engine: d.Dialect.Engine(), Tables: make([]Table, len(objs)), Items: make([]Item, len(items))}
	total := len(objs) + len(items)
	jobs := make(chan int)
	var done atomic.Int64
	var wg sync.WaitGroup
	for range min(readers, max(1, total)) {
		wg.Go(func() {
			for i := range jobs {
				if i < len(objs) {
					s.Tables[i] = readTable(ctx, d, objs[i], structure)
				} else {
					s.Items[i-len(objs)] = readItem(ctx, d, items[i-len(objs)])
				}
				if n := done.Add(1); progress != nil {
					progress(int(n), total)
				}
			}
		})
	}
send:
	for i := range total {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

func readTable(ctx context.Context, d *db.DB, o db.Object, structure bool) Table {
	t := Table{Object: o}
	var err error
	t.Definition, err = d.Dialect.DDL(ctx, d.SQL, o.Schema, o)
	if err == nil && structure {
		t.Columns, err = d.Dialect.Columns(ctx, d.SQL, o.Schema, o.Name)
	}
	if err == nil && structure && o.Kind == db.KindTable {
		if t.Indexes, err = d.Dialect.Indexes(ctx, d.SQL, o.Schema, o.Name); err == nil {
			if t.ForeignKeys, err = d.Dialect.ForeignKeys(ctx, d.SQL, o.Schema, o.Name); err == nil {
				t.ReferencedBy, err = d.Dialect.ReferencedBy(ctx, d.SQL, o.Schema, o.Name)
			}
		}
	}
	if err != nil {
		t.Err = err.Error()
	}
	return t
}

func readItem(ctx context.Context, d *db.DB, it db.Item) Item {
	def, err := d.Dialect.ItemDDL(ctx, d.SQL, it)
	out := Item{Item: it, Definition: def}
	if err != nil {
		out.Err = err.Error()
	}
	return out
}

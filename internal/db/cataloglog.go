package db

import (
	"context"
	"database/sql"
	"slices"
	"sync"
	"time"
)

// catalogLogSize is how many of the latest catalog queries a pool keeps.
const catalogLogSize = 500

// CatalogQuery is a query the app ran on its own to read a catalog, as
// the tables of a schema or a table's columns.
type CatalogQuery struct {
	At       time.Time
	Database string
	SQL      string
	Args     []any
	// Took is how long the server took to answer, before the rows were
	// read.
	Took time.Duration
	Err  string
}

// catalogLog keeps the latest catalog queries of a pool.
type catalogLog struct {
	mu      sync.Mutex
	entries []CatalogQuery
}

func (l *catalogLog) add(q CatalogQuery) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) == catalogLogSize {
		l.entries = slices.Delete(l.entries, 0, 1)
	}
	l.entries = append(l.entries, q)
}

// Catalog is the pool as the app reads catalogs with it: each query is
// kept in the log CatalogQueries reads.
func (d *DB) Catalog() Querier { return catalogQuerier{d} }

type catalogQuerier struct{ d *DB }

func (q catalogQuerier) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := q.d.SQL.QueryContext(ctx, query, args...)
	entry := CatalogQuery{At: start, Database: q.d.Config.Database, SQL: query, Args: args, Took: time.Since(start)}
	if err != nil {
		entry.Err = err.Error()
	}
	q.d.catalog.add(entry)
	return rows, err
}

// CatalogQueries are the latest catalog queries of the pool and of its
// other databases, the newest first.
func (d *DB) CatalogQueries() []CatalogQuery {
	d.catalog.mu.Lock()
	out := slices.Clone(d.catalog.entries)
	d.catalog.mu.Unlock()
	d.mu.Lock()
	others := make([]*DB, 0, len(d.databases))
	for _, o := range d.databases {
		others = append(others, o)
	}
	d.mu.Unlock()
	for _, o := range others {
		o.catalog.mu.Lock()
		out = append(out, o.catalog.entries...)
		o.catalog.mu.Unlock()
	}
	slices.SortFunc(out, func(a, b CatalogQuery) int { return b.At.Compare(a.At) })
	return out
}

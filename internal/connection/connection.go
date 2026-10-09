// Package connection is a saved connection's live state: its pool, its
// status, and the schema read so far, read and changed on the UI thread.
package connection

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/project"
)

// Status is where a connection is in its life.
type Status int

const (
	StatusIdle Status = iota
	StatusConnecting
	StatusConnected
	StatusFailed
)

// Conn is a saved connection and, once connected, its pool and what is
// known of its schema. Like the rest of the app's state, it is read and
// changed on the main thread only.
type Conn struct {
	Config  db.Config
	Project *project.Project // whose dgopher.json keeps it
	// Generation counts disconnects: a connect that finishes after one belongs
	// to settings that may have changed, and is dropped.
	Generation int
	// ExpandWhenLoaded opens the default schema in the navigator once the
	// schemas are read.
	ExpandWhenLoaded bool
	Status           Status
	Err              string
	DB               *db.DB
	KV               *db.KV
	Version          string
	Waiters          []func()

	// The schema, as far as it was read.
	DefaultSchema string
	Databases     []string // PostgreSQL's other databases
	Schemas       map[string][]string
	Objects       map[SchemaKey][]db.Object
	Items         map[SchemaKey][]db.Item
	Columns       map[ObjectKey][]db.Column
	Loading       map[any]bool
	LoadErr       map[any]string
}

type SchemaKey struct{ Database, Schema string }

type ObjectKey struct{ Database, Schema, Name string }

func (cn *Conn) Reset() {
	cn.Schemas = map[string][]string{}
	cn.Objects = map[SchemaKey][]db.Object{}
	cn.Items = map[SchemaKey][]db.Item{}
	cn.Columns = map[ObjectKey][]db.Column{}
	cn.Loading = map[any]bool{}
	cn.LoadErr = map[any]string{}
	cn.Databases = nil
	cn.DefaultSchema = ""
}

// ForgetCatalog forgets the tables, items and columns read, for the
// navigator to read them again, as after statements that change them.
func (cn *Conn) ForgetCatalog() {
	clear(cn.Objects)
	clear(cn.Items)
	clear(cn.Columns)
}

// PoolFor returns how to get the pool of one of the connection's
// databases. It reads the connection on the main thread, where a
// disconnect changes it; the function it returns runs anywhere.
func (cn *Conn) PoolFor(database string) func(ctx context.Context) (*db.DB, error) {
	pool := cn.DB
	return func(ctx context.Context) (*db.DB, error) {
		if pool == nil {
			return nil, errors.New("not connected")
		}
		return pool.Database(ctx, database)
	}
}

// Runner runs work for the connections: off the UI thread, and back on it.
type Runner interface {
	// Post runs fn on the UI thread before the next frame.
	Post(fn func())
	// Background runs work off the UI thread; the function it returns
	// runs on the UI thread.
	Background(work func() func())
}

// schemasKey is what LoadSchemas marks a database's loading and errors by.
func schemasKey(database string) SchemaKey {
	return SchemaKey{Database: database, Schema: "\x00schemas"}
}

// SchemasError is why a database's schemas could not be read, "" when
// they were or are being.
func (cn *Conn) SchemasError(database string) string { return cn.LoadErr[schemasKey(database)] }

// LoadSchemas reads the schemas (and PostgreSQL's databases) of a
// connection's database, then calls then.
func LoadSchemas(r Runner, cn *Conn, database string, then func()) {
	key := schemasKey(database)
	if cn.Loading[key] {
		return
	}
	cn.Loading[key] = true
	delete(cn.LoadErr, key)
	poolOf := cn.PoolFor(database) // read on the main thread
	r.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var schemas, dbs []string
		var current string
		if err == nil {
			schemas, err = d.Dialect.Schemas(ctx, d.Catalog())
			current, _ = d.Dialect.CurrentSchema(ctx, d.Catalog())
			if database == "" {
				dbs, _ = d.Dialect.Databases(ctx, d.Catalog())
			}
		}
		return func() {
			delete(cn.Loading, key)
			if err != nil {
				cn.LoadErr[key] = err.Error()
				return
			}
			cn.Schemas[database] = schemas
			if database == "" {
				cn.Databases = dbs
				cn.DefaultSchema = current
			}
			if then != nil {
				then()
			}
		}
	})
}

// LoadObjects reads the tables and views of a schema.
func LoadObjects(r Runner, cn *Conn, database, schema string) {
	key := SchemaKey{Database: database, Schema: schema}
	if cn.Loading[key] {
		return
	}
	cn.Loading[key] = true
	delete(cn.LoadErr, key)
	poolOf := cn.PoolFor(database) // read on the main thread
	r.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var objs []db.Object
		if err == nil {
			objs, err = d.Dialect.Objects(ctx, d.Catalog(), schema)
		}
		return func() {
			delete(cn.Loading, key)
			if err != nil {
				cn.LoadErr[key] = err.Error()
				return
			}
			if objs == nil {
				objs = []db.Object{}
			}
			cn.Objects[key] = objs
		}
	})
}

// itemsKey is what LoadItems marks a schema's loading and errors by,
// apart from its tables'.
type itemsKey SchemaKey

// LoadItems reads the routines, triggers, sequences, types and the like
// of a schema.
func LoadItems(r Runner, cn *Conn, database, schema string) {
	key := SchemaKey{Database: database, Schema: schema}
	if cn.Loading[itemsKey(key)] {
		return
	}
	cn.Loading[itemsKey(key)] = true
	delete(cn.LoadErr, itemsKey(key))
	poolOf := cn.PoolFor(database) // read on the main thread
	r.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var items []db.Item
		if err == nil {
			items, err = d.Dialect.Items(ctx, d.Catalog(), schema)
		}
		return func() {
			delete(cn.Loading, itemsKey(key))
			if err != nil {
				cn.LoadErr[itemsKey(key)] = err.Error()
				return
			}
			if items == nil {
				items = []db.Item{}
			}
			cn.Items[key] = items
		}
	})
}

// ItemsError is why a schema's items could not be read, "" when they
// were or are being.
func (cn *Conn) ItemsError(database, schema string) string {
	return cn.LoadErr[itemsKey(SchemaKey{Database: database, Schema: schema})]
}

// LoadColumns reads the columns of a table, then calls then.
func LoadColumns(r Runner, cn *Conn, database, schema, name string, then func([]db.Column)) {
	key := ObjectKey{Database: database, Schema: schema, Name: name}
	if cols, ok := cn.Columns[key]; ok {
		if then != nil {
			then(cols)
		}
		return
	}
	if cn.Loading[key] {
		return
	}
	cn.Loading[key] = true
	poolOf := cn.PoolFor(database) // read on the main thread
	r.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var cols []db.Column
		if err == nil {
			cols, err = d.Dialect.Columns(ctx, d.Catalog(), schema, name)
		}
		return func() {
			delete(cn.Loading, key)
			if err != nil {
				cn.LoadErr[key] = err.Error()
				return
			}
			cn.Columns[key] = cols
			if then != nil {
				then(cols)
			}
		}
	})
}

// SortedObjects returns the objects of a schema of the kind asked, by
// name.
func SortedObjects(objs []db.Object, views bool) []db.Object {
	var out []db.Object
	for _, o := range objs {
		isView := o.Kind == db.KindView || o.Kind == db.KindMaterializedView
		if isView == views {
			out = append(out, o)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// DefaultIdleTx is how long a transaction may stay idle on an
// environment before it is rolled back: DBeaver's defaults.
func DefaultIdleTx(e db.Environment) time.Duration {
	switch e {
	case db.Production:
		return 10 * time.Minute
	case db.Staging:
		return 15 * time.Minute
	}
	return 30 * time.Minute
}

// IdleTxLimit is a connection's idle transaction limit; 0 for none.
func IdleTxLimit(cfg *db.Config) time.Duration {
	switch {
	case cfg.IdleTxTimeout < 0:
		return 0
	case cfg.IdleTxTimeout > 0:
		return time.Duration(cfg.IdleTxTimeout) * time.Second
	}
	return DefaultIdleTx(cfg.Env)
}

// HeaderLine is what starts a query file of a connection.
func HeaderLine(cn *Conn) string {
	return "-- connection: " + strings.TrimPrefix(cn.Config.ID, cn.Project.Prefix) + "\n\n"
}

// CloseThenCancel closes cursors before cancelling their context: a
// context cancelled while rows are still open stops the query on the
// server, or drops the connection, and an open transaction with it.
func CloseThenCancel(cursors []*db.Cursor, cancel context.CancelFunc) {
	for _, c := range cursors {
		c.Close()
	}
	if cancel != nil {
		cancel()
	}
}

// OpenSession starts a session on a database of a pool, off the main
// thread.
func OpenSession(ctx context.Context, pool *db.DB, database string) (*db.Session, error) {
	if pool == nil {
		return nil, errors.New("not connected")
	}
	d, err := pool.Database(ctx, database)
	if err != nil {
		return nil, err
	}
	return d.Session(ctx)
}

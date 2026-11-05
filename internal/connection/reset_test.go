package connection

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dgopher/internal/db"
)

// A read begun before a Reset, as a reconnect or a refresh does, belongs to
// the pool and catalog before it: its result, its error and the end of its
// loading are dropped, and the read begun after it stays loading.
func TestLoadAcrossReset(t *testing.T) {
	file := filepath.Join(t.TempDir(), "c.sqlite")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := db.Config{Name: "lite", Engine: db.SQLite, Database: file}
	d, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{
		`CREATE TABLE a (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE b (id INTEGER PRIMARY KEY, a_id INTEGER REFERENCES a(id))`,
	} {
		if _, err := d.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	loaders := map[string]struct {
		start  func(r Runner, cn *Conn, called *bool)
		landed func(cn *Conn) bool
	}{
		"schemas": {
			func(r Runner, cn *Conn, called *bool) { LoadSchemas(r, cn, "", func() { *called = true }) },
			func(cn *Conn) bool { return len(cn.Schemas) != 0 },
		},
		"objects": {
			func(r Runner, cn *Conn, called *bool) { LoadObjects(r, cn, "", "main") },
			func(cn *Conn) bool { return len(cn.Objects) != 0 },
		},
		"schema columns": {
			func(r Runner, cn *Conn, called *bool) { loadSchemaColumns(r, cn, "", "main") },
			func(cn *Conn) bool { return len(cn.Columns) != 0 },
		},
		"items": {
			func(r Runner, cn *Conn, called *bool) { LoadItems(r, cn, "", "main") },
			func(cn *Conn) bool { return len(cn.Items) != 0 },
		},
		"columns": {
			func(r Runner, cn *Conn, called *bool) {
				LoadColumns(r, cn, "", "main", "a", func([]db.Column) { *called = true })
			},
			func(cn *Conn) bool { return len(cn.Columns) != 0 },
		},
		"relations": {
			func(r Runner, cn *Conn, called *bool) {
				LoadRelations(r, cn, "", "main", "b", func(Relations) { *called = true })
			},
			func(cn *Conn) bool { return len(cn.Relations) != 0 },
		},
		"signatures": {
			func(r Runner, cn *Conn, called *bool) {
				LoadSignatures(r, cn, func(map[string][]db.Signature) { *called = true })
			},
			func(cn *Conn) bool { return cn.Signatures != nil },
		},
	}
	for name, l := range loaders {
		t.Run(name, func(t *testing.T) {
			for _, failing := range []bool{false, true} {
				cn := &Conn{Config: cfg, Store: d, Status: StatusConnected}
				if failing {
					// The old pool is gone: its read fails.
					cn.Store = (*db.DB)(nil)
				}
				cn.Reset()
				h := &held{}
				var oldCalled, newCalled bool
				l.start(h, cn, &oldCalled)
				if len(h.work) != 1 {
					t.Fatalf("%d reads started", len(h.work))
				}
				old := h.work[0]
				cn.Store = d
				cn.Reset()
				l.start(h, cn, &newCalled)
				if len(h.work) != 2 {
					t.Fatalf("read after reset not started: %d reads", len(h.work))
				}
				old()()
				if l.landed(cn) || oldCalled || len(cn.LoadErr) != 0 || len(cn.Loading) != 1 {
					t.Fatalf("failing %v: old read landed: then %v, errors %v, loading %v", failing, oldCalled, cn.LoadErr, cn.Loading)
				}
				h.work[1]()()
				if !l.landed(cn) || len(cn.Loading) != 0 {
					t.Fatalf("failing %v: new read did not land: loading %v, errors %v", failing, cn.Loading, cn.LoadErr)
				}
			}
		})
	}
}

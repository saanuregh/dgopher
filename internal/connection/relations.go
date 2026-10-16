package connection

import (
	"context"
	"time"

	"dgopher/internal/db"
)

// Relations are how a table joins others: its foreign keys, and the
// foreign keys of other tables pointing at it.
type Relations struct {
	FKs  []db.ForeignKey
	Refs []db.Reference
}

// relationsKey marks the reading of a table's relations in Loading.
type relationsKey ObjectKey

// LoadRelations reads a table's relations once, then calls then.
func LoadRelations(r Runner, cn *Conn, database, schema, name string, then func(Relations)) {
	key := ObjectKey{Database: database, Schema: schema, Name: name}
	if rel, ok := cn.Relations[key]; ok {
		if then != nil {
			then(rel)
		}
		return
	}
	if cn.Loading[relationsKey(key)] || cn.LoadErr[relationsKey(key)] != "" {
		return // a failed read waits for the catalog to be read again
	}
	cn.Loading[relationsKey(key)] = true
	poolOf := cn.PoolFor(database) // read on the main thread
	r.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var rel Relations
		if err == nil {
			rel.FKs, err = d.Dialect.ForeignKeys(ctx, d.Catalog(), schema, name)
		}
		if err == nil {
			rel.Refs, err = d.Dialect.ReferencedBy(ctx, d.Catalog(), schema, name)
		}
		return func() {
			delete(cn.Loading, relationsKey(key))
			if err != nil {
				cn.LoadErr[relationsKey(key)] = err.Error()
				return
			}
			if cn.Relations == nil {
				cn.Relations = map[ObjectKey]Relations{}
			}
			cn.Relations[key] = rel
			if then != nil {
				then(rel)
			}
		}
	})
}

// RelationsError is why a table's relations could not be read, "" when
// they were or are being.
func (cn *Conn) RelationsError(database, schema, name string) string {
	return cn.LoadErr[relationsKey(ObjectKey{Database: database, Schema: schema, Name: name})]
}

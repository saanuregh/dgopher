package db

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// DocDatabase is a database of a MongoDB server.
type DocDatabase struct {
	Name       string
	SizeOnDisk int64
	Empty      bool
}

// DocCollection is a collection or view of a database.
type DocCollection struct {
	Name string
	// Type is collection, view or timeseries.
	Type string
	// ViewOn and Pipeline are what a view reads, "" and nil for a
	// collection.
	ViewOn   string
	Pipeline bson.A
	Capped   bool
}

// DocIndex is an index of a collection.
type DocIndex struct {
	Name                   string
	Keys                   bson.D
	Unique, Sparse, Hidden bool
	// ExpireAfter is a TTL index's seconds; nil for other indexes.
	ExpireAfter *int32
	// Partial is a partial index's filter; nil for other indexes.
	Partial bson.D
}

// badValue is the server's error code for an argument it does not take.
const badValue = 2

// Databases are the server's databases the user may read, sorted by
// name.
func (d *Doc) Databases(ctx context.Context) ([]DocDatabase, error) {
	r, err := d.Client.ListDatabases(ctx, bson.D{}, options.ListDatabases().SetAuthorizedDatabases(true))
	if se, ok := errors.AsType[mongo.ServerError](err); ok && se.HasErrorCode(badValue) {
		// FerretDB knows no authorizedDatabases, and lists every database.
		r, err = d.Client.ListDatabases(ctx, bson.D{})
	}
	if err != nil {
		return nil, err
	}
	out := make([]DocDatabase, len(r.Databases))
	for i, s := range r.Databases {
		out[i] = DocDatabase{Name: s.Name, SizeOnDisk: s.SizeOnDisk, Empty: s.Empty}
	}
	slices.SortFunc(out, func(a, b DocDatabase) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

// Collections are a database's collections and views, sorted by name,
// without the server's own system.* collections.
func (d *Doc) Collections(ctx context.Context, database string) ([]DocCollection, error) {
	cur, err := d.Client.Database(database).ListCollections(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	var specs []struct {
		Name    string `bson:"name"`
		Type    string `bson:"type"`
		Options struct {
			ViewOn   string `bson:"viewOn"`
			Pipeline bson.A `bson:"pipeline"`
			Capped   bool   `bson:"capped"`
		} `bson:"options"`
	}
	if err := cur.All(ctx, &specs); err != nil {
		return nil, err
	}
	out := make([]DocCollection, 0, len(specs))
	for _, s := range specs {
		if strings.HasPrefix(s.Name, "system.") {
			continue
		}
		out = append(out, DocCollection{Name: s.Name, Type: cmp.Or(s.Type, "collection"), ViewOn: s.Options.ViewOn,
			Pipeline: s.Options.Pipeline, Capped: s.Options.Capped})
	}
	slices.SortFunc(out, func(a, b DocCollection) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

// Indexes are a collection's indexes, in the server's order: _id's first.
func (d *Doc) Indexes(ctx context.Context, database, coll string) ([]DocIndex, error) {
	cur, err := d.Client.Database(database).Collection(coll).Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []DocIndex
	for cur.Next(ctx) {
		raw := cur.Current
		ix := DocIndex{
			Name:   lookupString(raw, "name"),
			Unique: truthyRaw(raw.Lookup("unique")),
			Sparse: truthyRaw(raw.Lookup("sparse")),
			Hidden: truthyRaw(raw.Lookup("hidden")),
		}
		if key, ok := raw.Lookup("key").DocumentOK(); ok {
			if err := bson.Unmarshal(key, &ix.Keys); err != nil {
				return nil, err
			}
		}
		// The server keeps expireAfterSeconds as the number it was given:
		// an int32, int64 or double.
		if n, ok := raw.Lookup("expireAfterSeconds").AsInt64OK(); ok {
			s := int32(n)
			ix.ExpireAfter = &s
		}
		if p, ok := raw.Lookup("partialFilterExpression").DocumentOK(); ok {
			if err := bson.Unmarshal(p, &ix.Partial); err != nil {
				return nil, err
			}
		}
		out = append(out, ix)
	}
	return out, cur.Err()
}

func lookupString(raw bson.Raw, key string) string {
	s, _ := raw.Lookup(key).StringValueOK()
	return s
}

// truthyRaw is an index option's flag, which old servers kept as 1 for
// true.
func truthyRaw(v bson.RawValue) bool {
	if b, ok := v.BooleanOK(); ok {
		return b
	}
	n, ok := v.AsInt64OK()
	return ok && n != 0
}

package db

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// The document tests' servers, at internal/testutil/mongoserver's
// addresses: the replica set's member is reached directly, as it names
// itself by its address inside the container.
func docReplicaSet(t *testing.T) Config {
	return mongoServer(t, "DGOPHER_MONGO_URL", "mongodb://127.0.0.1:27018/?directConnection=true")
}

func docStandalone(t *testing.T) Config {
	return mongoServer(t, "DGOPHER_MONGO_STANDALONE_URL", "mongodb://127.0.0.1:27019/?directConnection=true")
}

func docFerretDB(t *testing.T) Config {
	cfg := mongoServer(t, "DGOPHER_FERRETDB_URL", "mongodb://dgopher:dgopher@127.0.0.1:27020/?directConnection=true")
	cfg.Engine = FerretDB
	return cfg
}

// scratchDatabase is a database of the test's own, dropped when it ends.
func scratchDatabase(t *testing.T, d *Doc) string {
	t.Helper()
	name := "dgopher_" + strings.ToLower(strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	db := d.Client.Database(name)
	if err := db.Drop(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Drop(context.Background()) })
	return name
}

func mustCommand(t *testing.T, d *Doc, database string, cmd bson.D) {
	t.Helper()
	if err := d.Client.Database(database).RunCommand(context.Background(), cmd).Err(); err != nil {
		t.Fatalf("%v: %v", cmd, err)
	}
}

func TestDocCatalog(t *testing.T) {
	d := openMongo(t, docReplicaSet(t), nil)
	testDocCatalog(t, d, true)
}

func TestDocCatalogFerretDB(t *testing.T) {
	d := openMongo(t, docFerretDB(t), nil)
	testDocCatalog(t, d, false)
}

func testDocCatalog(t *testing.T, d *Doc, full bool) {
	ctx := context.Background()
	name := scratchDatabase(t, d)
	mustCommand(t, d, name, bson.D{{Key: "create", Value: "people"}})
	mustCommand(t, d, name, bson.D{{Key: "create", Value: "logs"}, {Key: "capped", Value: true}, {Key: "size", Value: int32(4096)}})
	pipeline := bson.A{bson.D{{Key: "$match", Value: bson.D{{Key: "age", Value: bson.D{{Key: "$gte", Value: int32(18)}}}}}}}
	if full {
		mustCommand(t, d, name, bson.D{{Key: "create", Value: "adults"}, {Key: "viewOn", Value: "people"}, {Key: "pipeline", Value: pipeline}})
		mustCommand(t, d, name, bson.D{{Key: "create", Value: "metrics"}, {Key: "timeseries", Value: bson.D{{Key: "timeField", Value: "at"}}}})
	}
	mustCommand(t, d, name, bson.D{{Key: "createIndexes", Value: "people"}, {Key: "indexes", Value: bson.A{
		bson.D{{Key: "key", Value: bson.D{{Key: "email", Value: int32(1)}}}, {Key: "name", Value: "email_1"}, {Key: "unique", Value: true}, {Key: "sparse", Value: true}},
		bson.D{{Key: "key", Value: bson.D{{Key: "seen", Value: int32(1)}}}, {Key: "name", Value: "seen_1"}, {Key: "expireAfterSeconds", Value: int32(3600)}},
		bson.D{{Key: "key", Value: bson.D{{Key: "age", Value: int32(-1)}, {Key: "name", Value: int32(1)}}}, {Key: "name", Value: "adult_age"},
			{Key: "partialFilterExpression", Value: bson.D{{Key: "age", Value: bson.D{{Key: "$gte", Value: int32(18)}}}}}},
	}}})

	dbs, err := d.Databases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(dbs, func(x DocDatabase) bool { return x.Name == name }) {
		t.Errorf("Databases %v lack %s", dbs, name)
	}
	if !slices.IsSortedFunc(dbs, func(a, b DocDatabase) int { return strings.Compare(a.Name, b.Name) }) {
		t.Errorf("Databases not sorted: %v", dbs)
	}

	colls, err := d.Collections(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	var want []DocCollection
	if full {
		want = append(want, DocCollection{Name: "adults", Type: "view", ViewOn: "people", Pipeline: pipeline})
	}
	// FerretDB takes capped: true but keeps no cap.
	want = append(want, DocCollection{Name: "logs", Type: "collection", Capped: full})
	if full {
		want = append(want, DocCollection{Name: "metrics", Type: "timeseries"})
	}
	want = append(want, DocCollection{Name: "people", Type: "collection"})
	if !reflect.DeepEqual(colls, want) {
		t.Errorf("Collections =\n %+v\nwant\n %+v", colls, want)
	}

	if full {
		mustCommand(t, d, name, bson.D{{Key: "collMod", Value: "people"}, {Key: "index", Value: bson.D{{Key: "name", Value: "seen_1"}, {Key: "hidden", Value: true}}}})
	}
	idx, err := d.Indexes(ctx, name, "people")
	if err != nil {
		t.Fatal(err)
	}
	hour := int32(3600)
	wantIdx := []DocIndex{
		{Name: "_id_", Keys: bson.D{{Key: "_id", Value: int32(1)}}},
		{Name: "email_1", Keys: bson.D{{Key: "email", Value: int32(1)}}, Unique: true, Sparse: true},
		{Name: "seen_1", Keys: bson.D{{Key: "seen", Value: int32(1)}}, ExpireAfter: &hour, Hidden: full},
		{Name: "adult_age", Keys: bson.D{{Key: "age", Value: int32(-1)}, {Key: "name", Value: int32(1)}},
			Partial: bson.D{{Key: "age", Value: bson.D{{Key: "$gte", Value: int32(18)}}}}},
	}
	if !reflect.DeepEqual(idx, wantIdx) {
		t.Errorf("Indexes =\n %+v\nwant\n %+v", idx, wantIdx)
	}
}

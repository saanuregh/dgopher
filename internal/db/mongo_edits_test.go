package db

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func rawID(t *testing.T, v any) bson.RawValue {
	t.Helper()
	typ, data, err := bson.MarshalValue(v)
	if err != nil {
		t.Fatal(err)
	}
	return bson.RawValue{Type: typ, Value: data}
}

// editsFixture is a collection of three people, and what it holds after.
func editsFixture(t *testing.T, d *Doc) (database string, contents func() string) {
	t.Helper()
	database = scratchDatabase(t, d)
	c := d.Client.Database(database).Collection("people")
	if _, err := c.InsertMany(context.Background(), []any{
		bson.D{{Key: "_id", Value: int32(1)}, {Key: "name", Value: "ada"}, {Key: "age", Value: int32(36)}},
		bson.D{{Key: "_id", Value: int32(2)}, {Key: "name", Value: "bob"}},
		bson.D{{Key: "_id", Value: int32(3)}, {Key: "name", Value: "cy"}},
	}); err != nil {
		t.Fatal(err)
	}
	return database, func() string {
		cur, err := c.Find(context.Background(), bson.D{})
		if err != nil {
			t.Fatal(err)
		}
		var docs []bson.D
		if err := cur.All(context.Background(), &docs); err != nil {
			t.Fatal(err)
		}
		return extJSON(t, bson.D{{Key: "docs", Value: docs}})
	}
}

const editsBefore = `{"docs":[{"_id":1,"name":"ada","age":36},{"_id":2,"name":"bob"},{"_id":3,"name":"cy"}]}`

// editsThatMiss change ada, then delete a document that is not there.
func editsThatMiss(t *testing.T) []DocEdit {
	return []DocEdit{
		{Kind: DocSetFields, ID: rawID(t, int32(1)), Set: bson.D{{Key: "age", Value: int32(37)}}},
		{Kind: DocDelete, ID: rawID(t, int32(99))},
	}
}

func TestApplyEdits(t *testing.T) {
	d := openMongo(t, docReplicaSet(t), nil)
	if !d.Transactions() {
		t.Fatal("the replica set holds no transactions")
	}
	ctx := context.Background()
	t.Run("all apply", func(t *testing.T) {
		db, contents := editsFixture(t, d)
		applied, err := d.ApplyEdits(ctx, db, "people", []DocEdit{
			{Kind: DocSetFields, ID: rawID(t, int32(1)), Set: bson.D{{Key: "city", Value: "london"}}, Unset: []string{"age"}},
			{Kind: DocSetFields, ID: rawID(t, int32(2)), Set: bson.D{{Key: "name", Value: "bo"}}},
			{Kind: DocDelete, ID: rawID(t, int32(3))},
			{Kind: DocInsert, Doc: bson.D{{Key: "_id", Value: int32(4)}, {Key: "name", Value: "di"}}},
		}, nil)
		if err != nil || applied != (DocApplied{Applied: 4}) {
			t.Fatalf("%+v, %v", applied, err)
		}
		if got, want := contents(), `{"docs":[{"_id":1,"name":"ada","city":"london"},{"_id":2,"name":"bo"},{"_id":4,"name":"di"}]}`; got != want {
			t.Errorf("after:\n %s\nwant\n %s", got, want)
		}
	})
	t.Run("a miss rolls back", func(t *testing.T) {
		db, contents := editsFixture(t, d)
		applied, err := d.ApplyEdits(ctx, db, "people", editsThatMiss(t), nil)
		if err == nil || !strings.Contains(err.Error(), "edit 2, _id 99: matched 0 documents: it was changed or deleted since it was read") ||
			!strings.Contains(err.Error(), "nothing was saved") {
			t.Errorf("error %v", err)
		}
		if applied != (DocApplied{Applied: 0}) {
			t.Errorf("applied %+v", applied)
		}
		if got := contents(); got != editsBefore {
			t.Errorf("the first edit stayed: %s", got)
		}
	})
	t.Run("a failed insert rolls back", func(t *testing.T) {
		db, contents := editsFixture(t, d)
		_, err := d.ApplyEdits(ctx, db, "people", []DocEdit{
			{Kind: DocDelete, ID: rawID(t, int32(3))},
			{Kind: DocInsert, Doc: bson.D{{Key: "_id", Value: int32(1)}}},
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "edit 2, _id 1: ") || !strings.Contains(err.Error(), "E11000") {
			t.Errorf("error %v", err)
		}
		if got := contents(); got != editsBefore {
			t.Errorf("the delete stayed: %s", got)
		}
	})
	t.Run("stop", func(t *testing.T) {
		db, contents := editsFixture(t, d)
		var stop atomic.Bool
		stop.Store(true)
		applied, err := d.ApplyEdits(ctx, db, "people", editsThatMiss(t)[:1], &stop)
		if err == nil || !strings.Contains(err.Error(), "stopped") || applied.Applied != 0 {
			t.Errorf("%+v, %v", applied, err)
		}
		if got := contents(); got != editsBefore {
			t.Errorf("changed after stop: %s", got)
		}
	})
}

// Without transactions, the edits before a failure stay, and Applied
// says how many.
func TestApplyEditsStandalone(t *testing.T) {
	testApplyEditsOneByOne(t, openMongo(t, docStandalone(t), nil))
}

func TestApplyEditsFerretDB(t *testing.T) {
	testApplyEditsOneByOne(t, openMongo(t, docFerretDB(t), nil))
}

func testApplyEditsOneByOne(t *testing.T, d *Doc) {
	if d.Transactions() {
		t.Fatal("the server holds transactions")
	}
	ctx := context.Background()
	db, contents := editsFixture(t, d)
	applied, err := d.ApplyEdits(ctx, db, "people", editsThatMiss(t), nil)
	if err == nil || !strings.Contains(err.Error(), "edit 2, _id 99: matched 0 documents") {
		t.Errorf("error %v", err)
	}
	if applied != (DocApplied{Applied: 1}) {
		t.Errorf("applied %+v", applied)
	}
	if got, want := contents(), `{"docs":[{"_id":1,"name":"ada","age":37},{"_id":2,"name":"bob"},{"_id":3,"name":"cy"}]}`; got != want {
		t.Errorf("after:\n %s\nwant\n %s", got, want)
	}

	var stop atomic.Bool
	stop.Store(true)
	applied, err = d.ApplyEdits(ctx, db, "people", []DocEdit{{Kind: DocDelete, ID: rawID(t, int32(2))}}, &stop)
	if err == nil || !strings.Contains(err.Error(), "stopped before edit 1") || applied != (DocApplied{}) {
		t.Errorf("stop: %+v, %v", applied, err)
	}
}

// namedFixture is a document whose fields' names $set reads as a path or
// an operator, and a field beside them that the edit does not name.
func namedFixture(t *testing.T, d *Doc) (database string, contents func() string) {
	t.Helper()
	database = scratchDatabase(t, d)
	c := d.Client.Database(database).Collection("named")
	// An insert keeps a dotted or $ name as it is.
	if _, err := c.InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: int32(1)}, {Key: "a.b", Value: int32(1)}, {Key: "$p", Value: int32(1)},
		{Key: "gone.x", Value: int32(1)}, {Key: "n", Value: int32(1)}, {Key: "m", Value: int32(1)}, {Key: "other", Value: "kept"},
	}); err != nil {
		t.Fatal(err)
	}
	return database, func() string {
		var doc bson.D
		if err := c.FindOne(context.Background(), bson.D{}).Decode(&doc); err != nil {
			t.Fatal(err)
		}
		return extJSON(t, doc)
	}
}

// namedEdit changes "a.b", "$p" and n, and removes "gone.x" and m, but
// leaves other alone.
func namedEdit(t *testing.T) DocEdit {
	return DocEdit{Kind: DocSetFields, ID: rawID(t, int32(1)),
		Set:   bson.D{{Key: "a.b", Value: int32(2)}, {Key: "$p", Value: bson.D{{Key: "$x", Value: "y"}}}, {Key: "n", Value: bson.D{{Key: "$gt", Value: int32(2)}}}},
		Unset: []string{"gone.x", "m"}}
}

// A field named with a dot or a $ changes by its name, with the fields
// beside it, and the others stay: nothing replaces the document.
func TestApplyEditsFieldsNamedByPath(t *testing.T) {
	d := openMongo(t, docReplicaSet(t), nil)
	database, contents := namedFixture(t, d)
	applied, err := d.ApplyEdits(context.Background(), database, "named", []DocEdit{namedEdit(t)}, nil)
	if err != nil || applied.Applied != 1 {
		t.Fatalf("%+v, %v", applied, err)
	}
	if got, want := contents(), `{"_id":1,"a.b":2,"$p":{"$x":"y"},"n":{"$gt":2},"other":"kept"}`; got != want {
		t.Errorf("after:\n %s\nwant\n %s", got, want)
	}
}

// FerretDB keeps a second _id in a document an update pipeline replaces:
// such an edit is refused before it is sent, and says why, even on a
// connection that takes FerretDB for MongoDB.
func TestApplyEditsFieldsNamedByPathFerretDB(t *testing.T) {
	cfg := docFerretDB(t)
	cfg.Engine = MongoDB
	d := openMongo(t, cfg, nil)
	database, contents := namedFixture(t, d)
	before := contents()
	applied, err := d.ApplyEdits(context.Background(), database, "named", []DocEdit{namedEdit(t)}, nil)
	if err == nil || applied.Applied != 0 || !strings.Contains(err.Error(), "edit 1, _id 1: FerretDB cannot change a field whose name holds a dot") {
		t.Errorf("%+v, %v", applied, err)
	}
	if got := contents(); got != before {
		t.Errorf("changed:\n %s\nwas\n %s", got, before)
	}
}

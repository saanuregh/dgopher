package db

import (
	"context"
	"slices"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// docRun runs stmt against database, failing the test on an error.
func docRun(t *testing.T, d *Doc, database, stmt string) DocResult {
	t.Helper()
	res, err := d.Run(context.Background(), database, mustParse(t, stmt))
	if err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
	return res
}

// docAll is every document of a result's cursor.
func docAll(t *testing.T, res DocResult) []bson.Raw {
	t.Helper()
	if res.Cursor == nil {
		t.Fatal("no cursor")
	}
	defer res.Cursor.Close(context.Background())
	var all []bson.Raw
	for {
		docs, done, err := res.Cursor.Fetch(context.Background(), 100)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, docs...)
		if done {
			return all
		}
	}
}

func replyJSON(t *testing.T, res DocResult) string {
	t.Helper()
	if res.Reply == nil {
		return "null"
	}
	return extJSON(t, res.Reply)
}

// wantReply checks the reply of stmt, and its Affected.
func wantReply(t *testing.T, d *Doc, database, stmt, want string, affected int64) {
	t.Helper()
	res := docRun(t, d, database, stmt)
	if got := replyJSON(t, res); got != want || res.Affected != affected {
		t.Errorf("%s:\n got %s, affected %d\nwant %s, affected %d", stmt, got, res.Affected, want, affected)
	}
}

func TestDocRun(t *testing.T) {
	cfg := docReplicaSet(t)
	sends := captureSends(t, cfg.Name)
	d := openMongo(t, cfg, nil)
	testDocRun(t, d, true)
	var sawInsert bool
	for _, s := range sends.take() {
		sawInsert = sawInsert || s.Mongo == "insert"
	}
	if !sawInsert {
		t.Error("Run's insert did not reach the send hook")
	}
}

func TestDocRunFerretDB(t *testing.T) {
	testDocRun(t, openMongo(t, docFerretDB(t), nil), false)
}

func testDocRun(t *testing.T, d *Doc, mongoDB bool) {
	ctx := context.Background()
	db := scratchDatabase(t, d)

	// Writes.
	res := docRun(t, d, db, `db.people.insertMany([{_id: 1, name: "ada", age: 36, city: "london"}, {_id: 2, name: "bob", age: 17, city: "paris"},
		{_id: 3, name: "cy", age: 52, city: "london"}, {_id: 4, name: "di", age: 29, city: "rome"}, {_id: 5, name: "ed", age: 41, city: "paris"}])`)
	if got := replyJSON(t, res); got != `{"acknowledged":true,"insertedIds":[1,2,3,4,5]}` || res.Affected != 5 {
		t.Errorf("insertMany: %s, affected %d", got, res.Affected)
	}
	res = docRun(t, d, db, `db.people.insertOne({name: "fay", age: 8})`)
	if id, ok := res.Reply.Lookup("insertedId").ObjectIDOK(); !ok || id.IsZero() || res.Affected != 1 {
		t.Errorf("insertOne: %s, affected %d", replyJSON(t, res), res.Affected)
	}
	wantReply(t, d, db, `db.people.updateOne({_id: 1}, {$set: {age: 37}})`,
		`{"acknowledged":true,"insertedId":null,"matchedCount":1,"modifiedCount":1,"upsertedCount":0}`, 1)
	wantReply(t, d, db, `db.people.updateMany({city: "paris"}, {$inc: {visits: 1}})`,
		`{"acknowledged":true,"insertedId":null,"matchedCount":2,"modifiedCount":2,"upsertedCount":0}`, 2)
	wantReply(t, d, db, `db.people.updateOne({_id: 9}, {$set: {name: "gus"}}, {upsert: true})`,
		`{"acknowledged":true,"insertedId":9,"matchedCount":0,"modifiedCount":0,"upsertedCount":1}`, 1)
	wantReply(t, d, db, `db.people.replaceOne({_id: 9}, {name: "gus", age: 60})`,
		`{"acknowledged":true,"insertedId":null,"matchedCount":1,"modifiedCount":1,"upsertedCount":0}`, 1)
	wantReply(t, d, db, `db.people.findOneAndUpdate({_id: 9}, {$set: {age: 61}}, {returnDocument: "after", projection: {_id: 0}})`,
		`{"name":"gus","age":61}`, 1)
	wantReply(t, d, db, `db.people.findOneAndDelete({_id: 9})`, `{"_id":9,"name":"gus","age":61}`, 1)
	wantReply(t, d, db, `db.people.findOneAndDelete({_id: 9})`, `null`, 0)
	wantReply(t, d, db, `db.people.deleteOne({name: "fay"})`, `{"acknowledged":true,"deletedCount":1}`, 1)
	res = docRun(t, d, db, `db.people.bulkWrite([{insertOne: {document: {_id: 10}}}, {updateOne: {filter: {_id: 10}, update: {$set: {x: 1}}}},
		{deleteMany: {filter: {_id: {$gte: 10}}}}])`)
	if res.Affected != 3 || res.Reply.Lookup("deletedCount").AsInt64() != 1 {
		t.Errorf("bulkWrite: %s, affected %d", replyJSON(t, res), res.Affected)
	}

	// A write that fails part way says what it did.
	res, err := d.Run(ctx, db, mustParse(t, `db.people.insertMany([{_id: 20}, {_id: 1}, {_id: 21}])`))
	if err == nil || !strings.Contains(err.Error(), "changed 1 documents, then failed") || !mongo.IsDuplicateKeyError(err) || res.Affected != 1 {
		t.Errorf("insertMany of a duplicate: %v, affected %d", err, res.Affected)
	}
	docRun(t, d, db, `db.people.deleteOne({_id: 20})`)

	// Reads.
	wantReply(t, d, db, `db.people.findOne({_id: 2}, {name: 1})`, `{"_id":2,"name":"bob"}`, -1)
	wantReply(t, d, db, `db.people.findOne({_id: 99})`, `null`, -1)
	wantReply(t, d, db, `db.people.countDocuments({city: "london"})`, `{"countDocuments":2}`, -1)
	wantReply(t, d, db, `db.people.countDocuments({nobody: 1})`, `{"countDocuments":0}`, -1)
	wantReply(t, d, db, `db.people.estimatedDocumentCount()`, `{"estimatedDocumentCount":5}`, -1)
	wantReply(t, d, db, `db.people.count({age: {$gt: 30}})`, `{"count":3}`, -1)
	wantReply(t, d, db, `db.people.find({age: {$gt: 30}}).skip(1).limit(1).size()`, `{"size":1}`, -1)
	wantReply(t, d, db, `db.people.distinct("city", {age: {$gt: 20}})`, `{"distinct":["london","paris","rome"]}`, -1)

	// A cursor pages through its documents, across batches.
	res = docRun(t, d, db, `db.people.find({}, {name: 1, _id: 0}).sort({age: -1}).batchSize(2)`)
	page, done, err := res.Cursor.Fetch(ctx, 3)
	if err != nil || done || len(page) != 3 || page[0].Lookup("name").StringValue() != "cy" {
		t.Errorf("first page %v, done %v, %v", page, done, err)
	}
	page, done, err = res.Cursor.Fetch(ctx, 3)
	if err != nil || !done || len(page) != 2 || page[1].Lookup("name").StringValue() != "bob" {
		t.Errorf("last page %v, done %v, %v", page, done, err)
	}
	res.Cursor.Close(ctx)
	names := func(docs []bson.Raw) []string {
		var out []string
		for _, x := range docs {
			out = append(out, x.Lookup("name").StringValue())
		}
		return out
	}
	if got := names(docAll(t, docRun(t, d, db, `db.people.find({city: "london"}).sort({name: -1}).projection({name: 1}).toArray()`))); !slices.Equal(got, []string{"cy", "ada"}) {
		t.Errorf("find chain: %v", got)
	}
	if got := names(docAll(t, docRun(t, d, db, `db.people.find().sort({_id: 1}).skip(1).limit(-2)`))); !slices.Equal(got, []string{"bob", "cy"}) {
		t.Errorf("negative limit: %v", got)
	}
	agg := docAll(t, docRun(t, d, db, `db.people.aggregate([{$group: {_id: "$city", n: {$sum: 1}}}, {$sort: {n: -1, _id: 1}}])`))
	if len(agg) != 3 || extJSON(t, agg[0]) != `{"_id":"london","n":2}` {
		t.Errorf("aggregate: %v", agg)
	}

	// Explain, in its two forms.
	explains := []string{`db.people.find({age: {$gt: 30}}).explain("executionStats")`, `db.people.explain().find({age: 1}).sort({age: 1})`,
		`db.people.aggregate([{$match: {}}]).explain()`}
	if mongoDB { // FerretDB explains no writes
		explains = append(explains, `db.people.explain("queryPlanner").deleteMany({age: 1})`)
	}
	for _, stmt := range explains {
		if res := docRun(t, d, db, stmt); res.Reply.Lookup("queryPlanner").Type == 0 && res.Reply.Lookup("stages").Type == 0 {
			t.Errorf("%s: %s", stmt, replyJSON(t, res))
		}
	}

	// Indexes and collections.
	docRun(t, d, db, `db.people.createIndex({city: 1, age: -1}, {name: "city_age"})`)
	docRun(t, d, db, `db.people.createIndexes([{name: 1}])`)
	var indexNames []string
	for _, x := range docAll(t, docRun(t, d, db, `db.people.getIndexes()`)) {
		indexNames = append(indexNames, x.Lookup("name").StringValue())
	}
	if !slices.Equal(indexNames, []string{"_id_", "city_age", "name_1"}) {
		t.Errorf("getIndexes: %v", indexNames)
	}
	wantReply(t, d, db, `db.people.getIndexKeys()`, `{"getIndexKeys":[{"_id":1},{"city":1,"age":-1},{"name":1}]}`, -1)
	docRun(t, d, db, `db.people.dropIndex("city_age")`)
	docRun(t, d, db, `db.people.dropIndexes()`)
	if n := len(docAll(t, docRun(t, d, db, `db.people.getIndexes()`))); n != 1 {
		t.Errorf("%d indexes after dropIndexes", n)
	}
	docRun(t, d, db, `db.createCollection("scratch")`)
	docRun(t, d, db, `db.scratch.renameCollection("renamed")`)
	wantShown := []string{"people:collection", "renamed:collection"}
	if mongoDB {
		docRun(t, d, db, `db.createView("adults", "people", [{$match: {age: {$gte: 18}}}])`)
		infos := docAll(t, docRun(t, d, db, `db.getCollectionInfos({name: "adults"})`))
		if len(infos) != 1 || infos[0].Lookup("type").StringValue() != "view" {
			t.Errorf("getCollectionInfos: %v", infos)
		}
		wantShown = append([]string{"adults:view"}, wantShown...)
	}
	// getCollectionNames lists the server's system.* collections too, as
	// mongosh does; show collections leaves them out.
	var listed []string
	listedNames, err := docRun(t, d, db, `db.getCollectionNames()`).Reply.Lookup("getCollectionNames").Array().Values()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range listedNames {
		if name := v.StringValue(); !strings.HasPrefix(name, "system.") {
			listed = append(listed, name)
		}
	}
	var shown []string
	for _, x := range docAll(t, docRun(t, d, db, `show collections`)) {
		shown = append(shown, x.Lookup("name").StringValue()+":"+x.Lookup("type").StringValue())
	}
	if !slices.Equal(shown, wantShown) || len(listed) != len(wantShown) {
		t.Errorf("show collections: %v, getCollectionNames %v, want %v", shown, listed, wantShown)
	}
	docRun(t, d, db, `db.renamed.drop()`)
	wantReply(t, d, db, `db.people.getFullName()`, `{"getFullName":"`+db+`.people"}`, -1)
	if res := docRun(t, d, db, `db.people.stats()`); res.Reply.Lookup("count").AsInt64() != 5 {
		t.Errorf("stats: %s", replyJSON(t, res))
	}
	if res := docRun(t, d, db, `db.people.dataSize()`); res.Reply.Lookup("dataSize").AsInt64() <= 0 {
		t.Errorf("dataSize: %s", replyJSON(t, res))
	}

	// Database methods, getSiblingDB and use.
	for _, stmt := range []string{`db.runCommand({ping: 1})`, `db.runCommand("ping")`, `db.adminCommand({ping: 1})`, `db.hello()`, `db.stats()`, `db.serverStatus()`} {
		if ok := docRun(t, d, db, stmt).Reply.Lookup("ok").AsInt64(); ok != 1 {
			t.Errorf("%s: ok %d", stmt, ok)
		}
	}
	if v := docRun(t, d, db, `db.version()`).Reply.Lookup("version").StringValue(); v == "" {
		t.Error("version: none")
	}
	wantReply(t, d, "admin", `db.getSiblingDB("`+db+`").people.countDocuments({_id: 3})`, `{"countDocuments":1}`, -1)
	wantReply(t, d, "admin", `db.getSiblingDB("`+db+`").getName()`, `{"getName":"`+db+`"}`, -1)
	wantReply(t, d, db, `use shop`, `{"database":"shop"}`, -1)
	var dbNames []string
	for _, x := range docAll(t, docRun(t, d, db, `show dbs`)) {
		dbNames = append(dbNames, x.Lookup("name").StringValue())
	}
	if !slices.Contains(dbNames, db) {
		t.Errorf("show dbs: %v lacks %s", dbNames, db)
	}
	if mongoDB {
		if res := docRun(t, d, db, `db.currentOp()`); res.Reply.Lookup("inprog").Type == 0 {
			t.Errorf("currentOp: %s", replyJSON(t, res))
		}
		if n := len(docAll(t, docRun(t, d, "admin", `db.aggregate([{$currentOp: {}}, {$limit: 1}])`))); n != 1 {
			t.Errorf("db.aggregate: %d documents", n)
		}
	}

	// What the server refuses comes back as its error.
	if _, err := d.Run(ctx, db, mustParse(t, `db.runCommand({noSuchCommand: 1})`)); err == nil {
		t.Error("an unknown command succeeded")
	}
	wantReply(t, d, db, `db.people.deleteMany({age: {$lt: 30}})`, `{"acknowledged":true,"deletedCount":2}`, 2)
	docRun(t, d, db, `db.dropDatabase()`)
	if n := len(docAll(t, docRun(t, d, db, `show collections`))); n != 0 {
		t.Errorf("%d collections after dropDatabase", n)
	}
}

package db

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"dgopher/internal/mongosh"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func mustParse(t *testing.T, stmt string) mongosh.Op {
	t.Helper()
	op, err := mongosh.Parse(stmt)
	if err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
	return op
}

func extJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := bson.MarshalExtJSON(v, false, false)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Each statement becomes the server command mongosh sends for it.
func TestPlanDocOpCommands(t *testing.T) {
	cases := []struct {
		stmt, want string
		admin      bool
		cursor     bool
	}{
		{`db.users.find()`, `{"find":"users"}`, false, true},
		{`db.users.find({age: {$gt: 30}}, {name: 1})`, `{"find":"users","filter":{"age":{"$gt":30}},"projection":{"name":1}}`, false, true},
		{`db.users.find({}, null, {sort: {a: 1}, limit: 3})`, `{"find":"users","filter":{},"sort":{"a":1},"limit":3}`, false, true},
		{`db.users.find({a: 1}).sort({b: -1}).limit(5).skip(2).projection({b: 1}).hint("b_1").maxTimeMS(500).comment("x").batchSize(10).allowDiskUse()`,
			`{"find":"users","filter":{"a":1},"sort":{"b":-1},"limit":5,"skip":2,"projection":{"b":1},"hint":"b_1","maxTimeMS":500,"comment":"x","batchSize":10,"allowDiskUse":true}`, false, true},
		{`db.users.find().limit(-3).toArray()`, `{"find":"users","limit":3,"singleBatch":true}`, false, true},
		{`db.users.find().sort({a: 1}).sort({b: 1})`, `{"find":"users","sort":{"b":1}}`, false, true},
		{`db.users.find().collation({locale: "fr"}).pretty()`, `{"find":"users","collation":{"locale":"fr"}}`, false, true},
		{`db.users.findOne({a: 1})`, `{"find":"users","filter":{"a":1},"limit":1,"singleBatch":true}`, false, false},
		{`db.users.find({a: 1}).count()`, `{"count":"users","query":{"a":1}}`, false, false},
		{`db.users.find({a: 1}).skip(1).limit(2).size()`, `{"count":"users","query":{"a":1},"skip":1,"limit":2}`, false, false},
		{`db.users.find({a: 1}).explain("executionStats")`, `{"explain":{"find":"users","filter":{"a":1}},"verbosity":"executionStats"}`, false, false},
		{`db.users.find().explain()`, `{"explain":{"find":"users"},"verbosity":"queryPlanner"}`, false, false},
		{`db.users.explain().find({a: 1}).sort({b: 1})`, `{"explain":{"find":"users","filter":{"a":1},"sort":{"b":1}},"verbosity":"queryPlanner"}`, false, false},
		{`db.users.explain(true).aggregate([{$match: {}}])`, `{"explain":{"aggregate":"users","pipeline":[{"$match":{}}],"cursor":{}},"verbosity":"allPlansExecution"}`, false, false},
		{`db.users.explain("queryPlanner").deleteMany({a: 1})`, `{"explain":{"delete":"users","deletes":[{"q":{"a":1},"limit":0}]},"verbosity":"queryPlanner"}`, false, false},
		{`db.users.countDocuments({a: 1}, {skip: 1, limit: 2})`,
			`{"aggregate":"users","pipeline":[{"$match":{"a":1}},{"$skip":1},{"$limit":2},{"$group":{"_id":1,"n":{"$sum":1}}}],"cursor":{}}`, false, false},
		{`db.users.estimatedDocumentCount()`, `{"count":"users"}`, false, false},
		{`db.users.count({a: 1})`, `{"count":"users","query":{"a":1}}`, false, false},
		{`db.users.distinct("city", {a: 1})`, `{"distinct":"users","key":"city","query":{"a":1}}`, false, false},
		{`db.users.aggregate([{$match: {a: 1}}], {allowDiskUse: true, batchSize: 5})`,
			`{"aggregate":"users","pipeline":[{"$match":{"a":1}}],"cursor":{"batchSize":5},"allowDiskUse":true}`, false, true},
		{`db.users.aggregate({$match: {a: 1}}, {$limit: 1})`, `{"aggregate":"users","pipeline":[{"$match":{"a":1}},{"$limit":1}],"cursor":{}}`, false, true},
		{`db.users.getIndexes()`, `{"listIndexes":"users"}`, false, true},
		{`db.users.stats()`, `{"collStats":"users"}`, false, false},
		{`db.users.updateOne({_id: 1}, {$set: {a: 2}}, {upsert: true})`,
			`{"update":"users","updates":[{"q":{"_id":1},"u":{"$set":{"a":2}},"upsert":true}]}`, false, false},
		{`db.users.updateMany({}, [{$set: {a: 2}}])`, `{"update":"users","updates":[{"q":{},"u":[{"$set":{"a":2}}],"multi":true}]}`, false, false},
		{`db.users.replaceOne({_id: 1}, {a: 3})`, `{"update":"users","updates":[{"q":{"_id":1},"u":{"a":3}}]}`, false, false},
		{`db.users.deleteOne({_id: 1}, {comment: "c"})`, `{"delete":"users","deletes":[{"q":{"_id":1},"limit":1}],"comment":"c"}`, false, false},
		{`db.users.remove({a: 1}, true)`, `{"delete":"users","deletes":[{"q":{"a":1},"limit":1}]}`, false, false},
		{`db.users.update({a: 1}, {$set: {b: 1}}, {multi: true})`, `{"update":"users","updates":[{"q":{"a":1},"u":{"$set":{"b":1}},"multi":true}]}`, false, false},
		{`db.users.findOneAndUpdate({a: 1}, {$inc: {n: 1}}, {returnDocument: "after", upsert: true})`,
			`{"findAndModify":"users","query":{"a":1},"update":{"$inc":{"n":1}},"new":true,"upsert":true}`, false, false},
		{`db.users.findOneAndDelete({a: 1}, {sort: {b: 1}})`, `{"findAndModify":"users","query":{"a":1},"remove":true,"sort":{"b":1}}`, false, false},
		{`db.users.findOneAndReplace({a: 1}, {a: 2})`, `{"findAndModify":"users","query":{"a":1},"update":{"a":2}}`, false, false},
		{`db.users.drop()`, `{"drop":"users"}`, false, false},
		{`db.users.createIndex({a: 1, b: -1}, {unique: true})`, `{"createIndexes":"users","indexes":[{"key":{"a":1,"b":-1},"name":"a_1_b_-1","unique":true}]}`, false, false},
		{`db.users.createIndexes([{a: 1}, {b: "text"}])`, `{"createIndexes":"users","indexes":[{"key":{"a":1},"name":"a_1"},{"key":{"b":"text"},"name":"b_text"}]}`, false, false},
		{`db.users.dropIndex("a_1")`, `{"dropIndexes":"users","index":"a_1"}`, false, false},
		{`db.users.dropIndexes()`, `{"dropIndexes":"users","index":"*"}`, false, false},
		{`db.users.renameCollection("people", true)`, `{"renameCollection":"shop.users","to":"shop.people","dropTarget":true}`, true, false},
		{`db.getSiblingDB("crm").users.renameCollection("people")`, `{"renameCollection":"crm.users","to":"crm.people"}`, true, false},
		{`db.runCommand({ping: 1})`, `{"ping":1}`, false, false},
		{`db.runCommand("ping")`, `{"ping":1}`, false, false},
		{`db.adminCommand({listDatabases: 1})`, `{"listDatabases":1}`, true, false},
		{`db.getCollectionInfos({name: "users"})`, `{"listCollections":1,"filter":{"name":"users"}}`, false, true},
		{`db.createCollection("logs", {capped: true, size: 4096})`, `{"create":"logs","capped":true,"size":4096}`, false, false},
		{`db.createView("adults", "users", [{$match: {age: {$gte: 18}}}])`, `{"create":"adults","viewOn":"users","pipeline":[{"$match":{"age":{"$gte":18}}}]}`, false, false},
		{`db.dropDatabase()`, `{"dropDatabase":1}`, false, false},
		{`db.stats(1024)`, `{"dbStats":1,"scale":1024}`, false, false},
		{`db.serverStatus()`, `{"serverStatus":1}`, true, false},
		{`db.hello()`, `{"hello":1}`, false, false},
		{`db.currentOp(true)`, `{"currentOp":1,"$all":true}`, true, false},
		{`db.currentOp({active: true})`, `{"currentOp":1,"active":true}`, true, false},
		{`db.aggregate([{$currentOp: {}}])`, `{"aggregate":1,"pipeline":[{"$currentOp":{}}],"cursor":{}}`, false, true},
		{`db.killOp(12)`, `{"killOp":1,"op":12}`, true, false},
		{`db.getProfilingStatus()`, `{"profile":-1}`, false, false},
		{`db.setProfilingLevel(1, {slowms: 20})`, `{"profile":1,"slowms":20}`, false, false},
		{`show users`, `{"usersInfo":1}`, false, false},
		{`show roles`, `{"rolesInfo":1,"showBuiltinRoles":true}`, false, false},
		{`show profile`, `{"find":"system.profile","filter":{"millis":{"$gt":0}},"sort":{"$natural":-1},"limit":5}`, false, true},
		{`show dbs`, `{"listDatabases":1}`, true, false},
	}
	for _, c := range cases {
		p, err := planDocOp("shop", mustParse(t, c.stmt))
		if err != nil {
			t.Errorf("%s: %v", c.stmt, err)
			continue
		}
		if p.do != nil {
			t.Errorf("%s: planned as a function, want the command %s", c.stmt, c.want)
			continue
		}
		if got := extJSON(t, p.command); got != c.want || p.admin != c.admin || p.cursor != c.cursor {
			t.Errorf("%s:\n got %s admin=%v cursor=%v\nwant %s admin=%v cursor=%v", c.stmt, got, p.admin, p.cursor, c.want, c.admin, c.cursor)
		}
	}
}

// insertOne and insertMany name each new document's _id, so the reply can
// say them.
func TestPlanDocOpInsertNamesIDs(t *testing.T) {
	p, err := planDocOp("shop", mustParse(t, `db.users.insertMany([{a: 1}, {_id: 7, a: 2}], {ordered: false})`))
	if err != nil {
		t.Fatal(err)
	}
	docs, _ := p.command[1].Value.(bson.A)
	if p.command[0].Key != "insert" || len(docs) != 2 {
		t.Fatalf("command %s", extJSON(t, p.command))
	}
	first := docs[0].(bson.D)
	if first[0].Key != "_id" {
		t.Errorf("no _id put first in %v", first)
	}
	if _, ok := first[0].Value.(bson.ObjectID); !ok {
		t.Errorf("_id %T, want an ObjectID", first[0].Value)
	}
	if got := extJSON(t, docs[1]); got != `{"_id":7,"a":2}` {
		t.Errorf("second %s", got)
	}
	if got := extJSON(t, p.command[2:]); got != `{"ordered":false}` {
		t.Errorf("options %s", got)
	}
}

// What Parse accepts but Run cannot do is refused, naming the method.
func TestPlanDocOpRefuses(t *testing.T) {
	cases := map[string]string{
		`db.users.watch()`:                       "watch",
		`db.users.find().forEach(1)`:             "forEach",
		`db.users.find().next()`:                 "next",
		`db.users.find().count().limit(1)`:       "limit() cannot follow count()",
		`db.users.findOne().sort({a: 1})`:        "findOne does not return a cursor: .sort() cannot follow it",
		`db.users.insertOne({a: 1}).pretty()`:    "insertOne does not return a cursor",
		`db.users.aggregate([]).sort({a: 1})`:    "sort",
		`db.users.explain()`:                     "explain() needs the method it explains",
		`db.users.explain().insertOne({a: 1})`:   "insertOne cannot be explained",
		`db.users.find({}, {}, {nope: 1})`:       `find: unknown option "nope"`,
		`db.users.updateOne({}, {}, {multi: 1})`: `updateOne: unknown option "multi"`,
		`db.users.find().limit("x")`:             "limit expects a number",
		`db.users.find("x")`:                     "find expects a filter document",
		`db.users.insertOne([1])`:                "insertOne expects a document",
		`db.users.mapReduce(1, 2)`:               "mapReduce",
		`db.shutdownServer()`:                    "shutdownServer",
		`db.runCommand({ping: 1}).ok()`:          "runCommand does not return a cursor",
		`db.runCommand(5)`:                       "runCommand expects a command document",
		`db.users.find().readPref("sometimes")`:  "sometimes",
		`db.users.bulkWrite([{explode: {}}])`:    `bulkWrite: unknown request "explode"`,
		`use a/b`:                                `the database name "a/b" cannot hold "/"`,
	}
	for stmt, want := range cases {
		_, err := planDocOp("shop", mustParse(t, stmt))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want an error with %q", stmt, err, want)
		}
	}
}

// remove's second argument is read the same way by the classifier and the
// planner, as mongosh reads it, so a remove of many is never let through
// as a remove of one.
func TestRemoveJustOneClassAndPlanAgree(t *testing.T) {
	cases := []struct {
		second string
		one    bool
	}{
		{``, false},
		{`, true`, true},
		{`, false`, false},
		{`, 1`, false},
		{`, 0`, false},
		{`, "x"`, false},
		{`, {}`, false},
		{`, {justOne: true}`, true},
		{`, {justOne: 1}`, true},
		{`, {justOne: "x"}`, true},
		{`, {justOne: 0}`, false},
		{`, {justOne: false}`, false},
		{`, {justOne: ""}`, false},
		{`, {justOne: null}`, false},
	}
	for _, c := range cases {
		stmt := `db.users.remove({}` + c.second + `)`
		op := mustParse(t, stmt)
		class := mongosh.Classify(op)
		if (class.Risk == "") != c.one {
			t.Errorf("%s: risk %q, want one=%v", stmt, class.Risk, c.one)
		}
		p, err := planDocOp("shop", op)
		if err != nil {
			// Nothing is sent; the classifier must still not take it for one.
			if c.one {
				t.Errorf("%s: %v", stmt, err)
			}
			continue
		}
		deletes, _ := p.command[1].Value.(bson.A)
		stmtDoc, _ := deletes[0].(bson.D)
		limit, _ := docField(stmtDoc, "limit")
		if want := map[bool]int32{true: 1, false: 0}[c.one]; limit != want {
			t.Errorf("%s: limit %v, want %d (class risk %q)", stmt, limit, want, class.Risk)
		}
		if _, ok := docField(stmtDoc, "justOne"); ok {
			t.Errorf("%s: justOne sent to the server: %s", stmt, extJSON(t, p.command))
		}
	}
}

// Close may come from another goroutine while a Fetch runs, as a Refresh
// during Fetch All does: it waits for the Fetch, and later ones fail.
// Run with -race.
func TestDocCursorCloseDuringFetch(t *testing.T) {
	docs := make([]any, 2000)
	for i := range docs {
		docs[i] = bson.D{{Key: "i", Value: int32(i)}}
	}
	ctx := context.Background()
	for range 50 {
		cur, err := mongo.NewCursorFromDocuments(docs, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		c := &DocCursor{cur: cur}
		var wg sync.WaitGroup
		wg.Go(func() {
			for {
				if _, done, err := c.Fetch(ctx, 10); done || err != nil {
					return
				}
			}
		})
		if err := c.Close(ctx); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		if _, _, err := c.Fetch(ctx, 10); !errors.Is(err, errCursorClosed) {
			t.Fatalf("Fetch after Close: %v, want %v", err, errCursorClosed)
		}
		if err := c.Close(ctx); err != nil {
			t.Fatalf("second Close: %v", err)
		}
	}
}

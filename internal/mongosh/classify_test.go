package mongosh

import (
	"reflect"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func classify(t *testing.T, src string) Class {
	t.Helper()
	op, err := Parse(src)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return Classify(op)
}

func TestClassifyReads(t *testing.T) {
	for _, c := range []struct{ src, command string }{
		{`db.c.find({a: 1}).sort({b: 1}).limit(3).skip(1).projection({a: 1}).hint({a: 1}).collation({locale: "en"}).maxTimeMS(5).comment("x").batchSize(2).toArray()`, "find"},
		{`db.c.find().pretty()`, "find"},
		{`db.c.find().count()`, "find"},
		{`db.c.find().explain("executionStats")`, "find"},
		{`db.c.findOne({_id: 1})`, "findOne"},
		{`db.c.countDocuments({})`, "countDocuments"},
		{`db.c.estimatedDocumentCount()`, "estimatedDocumentCount"},
		{`db.c.count()`, "count"},
		{`db.c.distinct("a")`, "distinct"},
		{`db.c.aggregate([{$match: {a: 1}}, {$group: {_id: "$a"}}])`, "aggregate"},
		{`db.c.aggregate([{$lookup: {from: "o", pipeline: [{$match: {}}], as: "x"}}])`, "aggregate"},
		{`db.c.getIndexes()`, "getIndexes"},
		{`db.c.stats()`, "stats"},
		{`db.c.validate({full: true})`, "validate"},
		{`db.c.explain().find({a: 1})`, "explain"},
		{`db.c.explain("queryPlanner").aggregate([{$match: {}}])`, "explain"},
		{`db.c.mapReduce("function(){}", "function(k, v){}", {out: {inline: 1}})`, "mapReduce"},
		{`db.getCollectionNames()`, "getCollectionNames"},
		{`db.getCollectionInfos()`, "getCollectionInfos"},
		{`db.stats()`, "stats"},
		{`db.serverStatus()`, "serverStatus"},
		{`db.currentOp()`, "currentOp"},
		{`db.version()`, "version"},
		{`db.aggregate([{$currentOp: {}}])`, "aggregate"},
		{`db.runCommand({find: "c", filter: {}})`, "find"},
		{`db.runCommand({aggregate: "c", pipeline: [{$match: {}}], cursor: {}})`, "aggregate"},
		{`db.runCommand({count: "c"})`, "count"},
		{`db.runCommand({distinct: "c", key: "a"})`, "distinct"},
		{`db.runCommand({listCollections: 1})`, "listCollections"},
		{`db.adminCommand({listDatabases: 1})`, "listDatabases"},
		{`db.runCommand({listIndexes: "c"})`, "listIndexes"},
		{`db.runCommand({collStats: "c"})`, "collStats"},
		{`db.runCommand({dbStats: 1})`, "dbStats"},
		{`db.adminCommand({serverStatus: 1})`, "serverStatus"},
		{`db.runCommand({buildInfo: 1})`, "buildInfo"},
		{`db.runCommand({hello: 1})`, "hello"},
		{`db.runCommand({isMaster: 1})`, "isMaster"},
		{`db.runCommand({ismaster: 1})`, "ismaster"},
		{`db.runCommand("ping")`, "ping"},
		{`db.runCommand({explain: {find: "c"}, verbosity: "queryPlanner"})`, "explain"},
		{`db.adminCommand({currentOp: 1})`, "currentOp"},
		{`db.adminCommand({getParameter: "*"})`, "getParameter"},
		{`db.runCommand({connectionStatus: 1})`, "connectionStatus"},
		{`db.adminCommand({hostInfo: 1})`, "hostInfo"},
		{`db.adminCommand({getLog: "global"})`, "getLog"},
		{`db.runCommand({validate: "c"})`, "validate"},
		{`db.runCommand({usersInfo: 1})`, "usersInfo"},
		{`db.runCommand({mapReduce: "c", map: "f", reduce: "g", out: {inline: 1}})`, "mapReduce"},
		{`use shop`, "use"},
		{`show dbs`, "show dbs"},
		{`show collections`, "show collections"},
		{`show profile`, "show profile"},
	} {
		got := classify(t, c.src)
		if got.Writes || got.Risk != "" || got.Command != c.command {
			t.Errorf("%s = %+v, want a read named %s", c.src, got, c.command)
		}
	}
}

func TestClassifyWrites(t *testing.T) {
	for _, c := range []struct{ src, command string }{
		{`db.c.insertOne({a: 1})`, "insertOne"},
		{`db.c.insertMany([{a: 1}])`, "insertMany"},
		{`db.c.insert({a: 1})`, "insert"},
		{`db.c.updateOne({_id: 1}, {$set: {a: 2}})`, "updateOne"},
		{`db.c.updateMany({a: 1}, {$set: {a: 2}})`, "updateMany"},
		{`db.c.update({a: 1}, {$set: {a: 2}})`, "update"},
		{`db.c.replaceOne({_id: 1}, {a: 2})`, "replaceOne"},
		{`db.c.deleteOne({_id: 1})`, "deleteOne"},
		{`db.c.deleteMany({a: 1})`, "deleteMany"},
		{`db.c.remove({a: 1})`, "remove"},
		{`db.c.findOneAndUpdate({a: 1}, {$set: {a: 2}})`, "findOneAndUpdate"},
		{`db.c.findOneAndReplace({a: 1}, {a: 2})`, "findOneAndReplace"},
		{`db.c.findOneAndDelete({a: 1})`, "findOneAndDelete"},
		{`db.c.findAndModify({query: {a: 1}, remove: true})`, "findAndModify"},
		{`db.c.bulkWrite([{insertOne: {document: {a: 1}}}])`, "bulkWrite"},
		{`db.c.createIndex({a: 1})`, "createIndex"},
		{`db.c.createIndexes([{a: 1}])`, "createIndexes"},
		{`db.c.dropIndex("a_1")`, "dropIndex"},
		{`db.createCollection("x")`, "createCollection"},
		{`db.createView("v", "c", [])`, "createView"},
		{`db.createUser({user: "u", pwd: "p", roles: []})`, "createUser"},
		{`db.c.aggregate([{$match: {}}, {$out: "o"}])`, "aggregate"},
		{`db.c.aggregate([{$merge: {into: "o"}}])`, "aggregate"},
		{`db.c.aggregate({$match: {}}, {$out: "o"})`, "aggregate"},
		{`db.c.aggregate([{$facet: {a: [{$out: "o"}]}}])`, "aggregate"},
		{`db.aggregate([{$documents: [{a: 1}]}, {$out: "o"}])`, "aggregate"},
		{`db.c.explain().aggregate([{$out: "o"}])`, "explain"},
		{`db.c.explain().remove({a: 1})`, "explain"},
		{`db.c.mapReduce("f", "g", {out: "o"})`, "mapReduce"},
		{`db.c.mapReduce("f", "g", "o")`, "mapReduce"},
		{`db.c.validate({repair: true})`, "validate"},
		{`db.runCommand({insert: "c", documents: [{a: 1}]})`, "insert"},
		{`db.runCommand({update: "c", updates: [{q: {a: 1}, u: {$set: {b: 1}}}]})`, "update"},
		{`db.runCommand({delete: "c", deletes: [{q: {a: 1}, limit: 1}]})`, "delete"},
		{`db.runCommand({create: "x"})`, "create"},
		{`db.runCommand({aggregate: "c", pipeline: [{$out: "o"}], cursor: {}})`, "aggregate"},
		{`db.runCommand({explain: {delete: "c", deletes: []}})`, "explain"},
		{`db.runCommand({mapReduce: "c", map: "f", reduce: "g", out: "o"})`, "mapReduce"},
		{`db.runCommand({validate: "c", repair: true})`, "validate"},
		{`db.adminCommand({setParameter: 1, x: 1})`, "setParameter"},
		{`db.runCommand({})`, "runCommand"},
		{`db.runCommand()`, "runCommand"},
		{`db.runCommand(1)`, "runCommand"},
		// what is not known counts as a write
		{`db.c.frobnicate()`, "frobnicate"},
		{`db.frobnicate()`, "frobnicate"},
		{`db.runCommand({frobnicate: 1})`, "frobnicate"},
		{`db.c.find().frobnicate()`, "find"},
		{`db.c.initializeOrderedBulkOp().insert({a: 1}).execute()`, "initializeOrderedBulkOp"},
	} {
		got := classify(t, c.src)
		if !got.Writes || got.Command != c.command {
			t.Errorf("%s = %+v, want a write named %s", c.src, got, c.command)
		}
	}
}

func TestClassifyRisks(t *testing.T) {
	for _, c := range []struct{ src, risk string }{
		{`db.c.drop()`, "drop removes the collection c and all of its documents"},
		{`db.getSiblingDB("shop").dropDatabase()`, "dropDatabase removes the database shop and all of its collections"},
		{`db.dropDatabase()`, "dropDatabase removes the database and all of its collections"},
		{`db.c.renameCollection("d")`, "renameCollection renames c; code that uses the old name stops finding it"},
		{`db.c.dropIndexes()`, "dropIndexes removes the indexes of c"},
		{`db.c.dropIndexes("a_1")`, "dropIndexes removes the indexes of c"},
		{`db.c.dropIndex("*")`, "dropIndex(\"*\") removes the indexes of c"},
		{`db.c.deleteMany({})`, "deleteMany with an empty filter deletes every document in c"},
		{`db.c.deleteMany()`, "deleteMany with an empty filter deletes every document in c"},
		{`db.c.remove({})`, "remove with an empty filter deletes every document in c"},
		{`db.c.updateMany({}, {$set: {a: 1}})`, "updateMany with an empty filter changes every document in c"},
		{`db.c.update({}, {$set: {a: 1}}, {multi: true})`, "update with an empty filter changes every document in c"},
		{`db.c.bulkWrite([{deleteMany: {filter: {}}}])`, "bulkWrite with an empty filter deletes every document in c"},
		{`db.c.bulkWrite([{updateMany: {filter: {}, update: {$set: {a: 1}}}}])`, "bulkWrite with an empty filter changes every document in c"},
		{`db.killOp(123)`, "killOp stops an operation that is running on the server"},
		{`db.shutdownServer()`, "shutdownServer stops the server"},
		{`db.runCommand({drop: "c"})`, "drop removes the collection c and all of its documents"},
		{`db.runCommand({dropDatabase: 1})`, "dropDatabase removes the database and all of its collections"},
		{`db.adminCommand({renameCollection: "a.b", to: "a.c"})`, "renameCollection renames a.b; code that uses the old name stops finding it"},
		{`db.runCommand({dropIndexes: "c", index: "*"})`, "dropIndexes removes the indexes of c"},
		{`db.runCommand({delete: "c", deletes: [{q: {}, limit: 0}]})`, "delete with an empty filter deletes every document in c"},
		{`db.runCommand({update: "c", updates: [{q: {}, u: {$set: {a: 1}}, multi: true}]})`, "update with an empty filter changes every document in c"},
		{`db.adminCommand({killOp: 1, op: 5})`, "killOp stops an operation that is running on the server"},
		{`db.adminCommand({shutdown: 1})`, "shutdown stops the server"},
		{`db.adminCommand("shutdown")`, "shutdown stops the server"},
	} {
		got := classify(t, c.src)
		if !got.Writes || got.Risk != c.risk {
			t.Errorf("%s = %+v, want the risk %q", c.src, got, c.risk)
		}
	}
	for _, src := range []string{
		`db.c.deleteMany({a: 1})`, `db.c.updateMany({a: 1}, {$set: {b: 1}})`, `db.c.dropIndex("a_1")`,
		`db.c.update({}, {$set: {a: 1}})`, `db.c.deleteOne({})`, `db.runCommand({delete: "c", deletes: [{q: {}, limit: 1}]})`,
		`db.runCommand({update: "c", updates: [{q: {}, u: {$set: {a: 1}}}]})`,
	} {
		if got := classify(t, src); got.Risk != "" {
			t.Errorf("%s has the risk %q", src, got.Risk)
		}
	}
}

// The filter of a change to many documents is kept, for the count of
// what it matches.
func TestClassifyFilter(t *testing.T) {
	for _, c := range []struct {
		src  string
		want Value
	}{
		{`db.c.deleteMany({a: 1})`, bson.D{{Key: "a", Value: int32(1)}}},
		{`db.c.updateMany({a: {$gt: 2}}, {$set: {b: 1}})`, bson.D{{Key: "a", Value: bson.D{{Key: "$gt", Value: int32(2)}}}}},
		{`db.c.remove({a: 1})`, bson.D{{Key: "a", Value: int32(1)}}},
		{`db.c.deleteMany()`, bson.D{}},
		{`db.c.update({a: 1}, {$set: {b: 1}}, {multi: true})`, bson.D{{Key: "a", Value: int32(1)}}},
	} {
		if got := classify(t, c.src); !reflect.DeepEqual(got.Filter, c.want) {
			t.Errorf("%s filter = %#v, want %#v", c.src, got.Filter, c.want)
		}
	}
	for _, src := range []string{`db.c.deleteOne({a: 1})`, `db.c.update({a: 1}, {$set: {b: 1}})`, `db.c.find({a: 1})`} {
		if got := classify(t, src); got.Filter != nil {
			t.Errorf("%s has a filter %v", src, got.Filter)
		}
	}
}

func TestClassifyCommandNamesFirstKey(t *testing.T) {
	// The command is the first key, whatever the others are named.
	if got := classify(t, `db.runCommand({drop: "c", find: "c"})`); !got.Writes || !strings.HasPrefix(got.Risk, "drop") {
		t.Errorf("drop then find = %+v", got)
	}
	if got := classify(t, `db.runCommand({find: "c", drop: "c"})`); got.Writes {
		t.Errorf("find then drop = %+v", got)
	}
}

// commandHelp sends {<name>: 1, help: true}, which a server that ignores
// help runs: it is classified as the command it names.
func TestClassifyCommandHelpAsItsCommand(t *testing.T) {
	for _, c := range []struct {
		src          string
		writes, risk bool
	}{
		{`db.commandHelp("ping")`, false, false},
		{`db.commandHelp("find")`, false, false},
		{`db.commandHelp("insert")`, true, false},
		{`db.commandHelp("frobnicate")`, true, false},
		{`db.commandHelp("dropDatabase")`, true, true},
		{`db.commandHelp("shutdown")`, true, true},
		{`db.commandHelp(1)`, true, false},
		{`db.commandHelp({ping: 1})`, true, false},
		{`db.commandHelp()`, true, false},
		{`db.commandHelp("ping").pretty()`, true, false},
	} {
		got := classify(t, c.src)
		if got.Writes != c.writes || (got.Risk != "") != c.risk || got.Command != "commandHelp" {
			t.Errorf("%s = %+v, want writes=%v risk=%v named commandHelp", c.src, got, c.writes, c.risk)
		}
	}
}

// A delete's limit of zero, in any numeric form the parser makes, deletes
// every match.
func TestClassifyDeleteLimitZeroForms(t *testing.T) {
	for _, limit := range []string{`0`, `-0`, `0.0`, `NumberInt(0)`, `NumberLong(0)`, `Double(0)`, `NumberDecimal("0")`,
		`NumberDecimal("0.00")`, `NumberDecimal("-0")`, `Decimal128("0E+10")`, `{$numberDecimal: "0"}`, `{$numberLong: "0"}`, `false`} {
		src := `db.runCommand({delete: "c", deletes: [{q: {}, limit: ` + limit + `}]})`
		if got := classify(t, src); got.Risk == "" {
			t.Errorf("%s has no risk", src)
		}
	}
	for _, limit := range []string{`1`, `NumberLong(1)`, `NumberDecimal("1")`, `true`} {
		src := `db.runCommand({delete: "c", deletes: [{q: {}, limit: ` + limit + `}]})`
		if got := classify(t, src); got.Risk != "" {
			t.Errorf("%s has the risk %q", src, got.Risk)
		}
	}
}

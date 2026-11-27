package mongosh

import (
	"fmt"
	"math"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Class is what a statement does, for the safety policy.
type Class struct {
	// Writes reports a statement that may change data, schema or the
	// server; one that is not known counts as a write.
	Writes bool
	// Risk is why the statement needs confirmation; "" for none.
	Risk string
	// Command is the name shown in reasons and the audit log.
	Command string
	// Filter is the filter of a change to many documents (deleteMany,
	// updateMany, remove, update with multi), for the count of what it
	// matches first; nil for other statements.
	Filter Value
}

// collectionReads are the collection methods that only read. aggregate,
// explain, mapReduce and validate read unless their arguments say
// otherwise.
var collectionReads = set(
	"find", "findOne", "countDocuments", "estimatedDocumentCount", "count", "distinct", "aggregate",
	"getIndexes", "getIndexKeys", "getIndexSpecs", "getSearchIndexes", "stats", "dataSize", "storageSize",
	"totalIndexSize", "totalSize", "isCapped", "validate", "latencyStats", "getShardDistribution", "watch",
	"explain", "mapReduce", "getName", "getFullName",
)

// cursorReads are the methods of a cursor, which only read.
var cursorReads = set(
	"sort", "limit", "skip", "projection", "hint", "collation", "maxTimeMS", "maxAwaitTimeMS", "comment",
	"explain", "toArray", "pretty", "count", "size", "itcount", "batchSize", "min", "max", "readConcern",
	"readPref", "allowDiskUse", "allowPartialResults", "noCursorTimeout", "returnKey", "showRecordId",
	"tailable", "hasNext", "next", "objsLeftInBatch", "isExhausted", "close",
)

// databaseReads are the database methods that only read; runCommand,
// adminCommand and commandHelp are classified by their command,
// aggregate by its stages.
var databaseReads = set(
	"getCollectionNames", "getCollectionInfos", "getName", "stats", "version", "serverStatus",
	"serverBuildInfo", "serverCmdLineOpts", "hostInfo", "isMaster", "hello", "currentOp",
	"getProfilingStatus", "listCommands", "getLogComponents", "getUsers", "getUser", "getRoles", "getRole",
	"getReplicationInfo", "aggregate",
)

// readCommands are the server commands, in lower case, that only read.
// aggregate, explain, mapReduce and validate read unless their fields say
// otherwise. validate reads: it checks a collection and changes it only
// with repair. getMore reads on: a cursor's writes, as $out's, happen
// when its aggregate runs.
var readCommands = set(
	"find", "aggregate", "count", "distinct", "listcollections", "listdatabases", "listindexes",
	"listsearchindexes", "collstats", "dbstats", "datasize", "serverstatus", "buildinfo", "hello",
	"ismaster", "ping", "explain", "currentop", "getparameter", "connectionstatus", "hostinfo", "getlog",
	"getcmdlineopts", "listcommands", "usersinfo", "rolesinfo", "getmore", "top", "replsetgetstatus",
	"replsetgetconfig", "whatsmyuri", "lockinfo", "getdefaultrwconcern", "features", "dbhash",
	"validate", "mapreduce",
)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// Classify says whether a statement writes, and whether it needs
// confirmation.
func Classify(op Op) Class {
	switch {
	case op.Method == "use" && op.Collection == "":
		return Class{Command: "use"}
	case op.Method == "show" && op.Collection == "":
		topic, _ := arg(op.Args, 0).(string)
		return Class{Command: "show " + topic}
	case op.Collection == "":
		return classifyDatabase(op)
	}
	return classifyCollection(op)
}

func classifyCollection(op Op) Class {
	c := Class{Command: op.Method, Writes: !collectionReads[op.Method]}
	name := op.Collection
	switch op.Method {
	case "explain":
		if len(op.Chain) == 0 {
			return c
		}
		// What explain runs writes nothing, but is classified as if it
		// ran, so that an explained write is never taken for a read.
		inner := classifyCollection(Op{Collection: op.Collection, Method: op.Chain[0].Name, Args: op.Chain[0].Args, Chain: op.Chain[1:]})
		c.Writes = inner.Writes
		return c
	case "aggregate":
		c.Writes = hasStageThatWrites(op.Args)
	case "mapReduce":
		opts, _ := arg(op.Args, 2).(bson.D)
		out, _ := field(opts, "out")
		c.Writes = !isInline(out)
	case "validate":
		opts, _ := arg(op.Args, 0).(bson.D)
		c.Writes = truthyField(opts, "repair")
	case "drop":
		c.Risk = fmt.Sprintf("drop removes the collection %s and all of its documents", name)
	case "renameCollection":
		c.Risk = renameRisk(name)
	case "dropIndexes":
		c.Risk = "dropIndexes removes the indexes of " + name
	case "dropIndex":
		if arg(op.Args, 0) == "*" {
			c.Risk = `dropIndex("*") removes the indexes of ` + name
		}
	case "deleteMany":
		c.Filter = filterArg(op.Args)
		c.Risk = emptyFilterRisk(op.Method, c.Filter, true, name)
	case "updateMany":
		c.Filter = filterArg(op.Args)
		c.Risk = emptyFilterRisk(op.Method, c.Filter, false, name)
	case "remove":
		if !RemovesOne(arg(op.Args, 1)) {
			c.Filter = filterArg(op.Args)
			c.Risk = emptyFilterRisk(op.Method, c.Filter, true, name)
		}
	case "update":
		if opts, _ := arg(op.Args, 2).(bson.D); truthyField(opts, "multi") {
			c.Filter = filterArg(op.Args)
			c.Risk = emptyFilterRisk(op.Method, c.Filter, false, name)
		}
	case "bulkWrite":
		requests, _ := arg(op.Args, 0).(bson.A)
		for _, r := range requests {
			req, _ := r.(bson.D)
			if len(req) == 0 {
				continue
			}
			body, _ := req[0].Value.(bson.D)
			filter, _ := field(body, "filter")
			switch req[0].Key {
			case "deleteMany":
				c.Risk = emptyFilterRisk(op.Method, filter, true, name)
			case "updateMany":
				c.Risk = emptyFilterRisk(op.Method, filter, false, name)
			}
			if c.Risk != "" {
				break
			}
		}
	}
	for _, call := range op.Chain {
		if !cursorReads[call.Name] {
			c.Writes = true
		}
	}
	if c.Risk != "" {
		c.Writes = true
	}
	return c
}

func classifyDatabase(op Op) Class {
	var c Class
	switch op.Method {
	case "runCommand", "adminCommand":
		c = classifyCommand(op, arg(op.Args, 0))
	case "aggregate":
		c = Class{Command: op.Method, Writes: hasStageThatWrites(op.Args)}
	case "commandHelp":
		// It sends {<name>: 1, help: true}; a server that ignores help runs
		// the command, so it is classified as the command.
		name, ok := arg(op.Args, 0).(string)
		if !ok {
			c = Class{Command: op.Method, Writes: true}
			break
		}
		c = classifyCommand(op, name)
		c.Command = op.Method
	case "dropDatabase":
		c = Class{Command: op.Method, Risk: dropDatabaseRisk(op.Database)}
	case "killOp":
		c = Class{Command: op.Method, Risk: "killOp stops an operation that is running on the server"}
	case "shutdownServer":
		c = Class{Command: op.Method, Risk: "shutdownServer stops the server"}
	default:
		c = Class{Command: op.Method, Writes: !databaseReads[op.Method]}
	}
	// What is called on a command's result is not known to read.
	if len(op.Chain) > 0 && (op.Method == "runCommand" || op.Method == "adminCommand" || !databaseReads[op.Method]) {
		c.Writes = true
	}
	if c.Risk != "" {
		c.Writes = true
	}
	return c
}

// classifyCommand classifies a server command: a document whose first key
// names it, or its name alone.
func classifyCommand(op Op, cmd Value) Class {
	var name string
	var doc bson.D
	switch v := cmd.(type) {
	case string:
		name = v
	case bson.D:
		if len(v) > 0 {
			name, doc = v[0].Key, v
		}
	}
	if name == "" {
		return Class{Command: op.Method, Writes: true}
	}
	c := Class{Command: name, Writes: !readCommands[strings.ToLower(name)]}
	var first Value
	if len(doc) > 0 {
		first = doc[0].Value
	}
	target, _ := first.(string)
	switch strings.ToLower(name) {
	case "aggregate":
		c.Writes = hasStageThatWrites(doc)
	case "explain":
		inner, ok := first.(bson.D)
		c.Writes = !ok || classifyCommand(op, inner).Writes
	case "mapreduce":
		out, _ := field(doc, "out")
		c.Writes = !isInline(out)
	case "validate":
		c.Writes = truthyField(doc, "repair")
	case "drop":
		c.Risk = fmt.Sprintf("drop removes the collection %s and all of its documents", target)
	case "dropdatabase":
		c.Risk = dropDatabaseRisk(op.Database)
	case "renamecollection":
		c.Risk = renameRisk(target)
	case "dropindexes", "deleteindexes":
		c.Risk = name + " removes the indexes of " + target
	case "delete":
		deletes, _ := field(doc, "deletes")
		for _, d := range asArray(deletes) {
			stmt, _ := d.(bson.D)
			q, _ := field(stmt, "q")
			limit, _ := field(stmt, "limit")
			if isZero(limit) && c.Risk == "" {
				c.Risk = emptyFilterRisk(name, q, true, target)
			}
		}
	case "update":
		updates, _ := field(doc, "updates")
		for _, u := range asArray(updates) {
			stmt, _ := u.(bson.D)
			q, _ := field(stmt, "q")
			if truthyField(stmt, "multi") && c.Risk == "" {
				c.Risk = emptyFilterRisk(name, q, false, target)
			}
		}
	case "killop":
		c.Risk = name + " stops an operation that is running on the server"
	case "shutdown":
		c.Risk = name + " stops the server"
	}
	if c.Risk != "" {
		c.Writes = true
	}
	return c
}

func arg(args []Value, i int) Value {
	if i < len(args) {
		return args[i]
	}
	return nil
}

// filterArg is the filter a method's first argument gives, or the empty
// one when it gives none.
func filterArg(args []Value) Value {
	if len(args) == 0 || args[0] == nil {
		return bson.D{}
	}
	return args[0]
}

func emptyFilterRisk(method string, filter Value, deletes bool, collection string) string {
	if d, ok := filter.(bson.D); filter != nil && (!ok || len(d) > 0) {
		return ""
	}
	if deletes {
		return fmt.Sprintf("%s with an empty filter deletes every document in %s", method, collection)
	}
	return fmt.Sprintf("%s with an empty filter changes every document in %s", method, collection)
}

func dropDatabaseRisk(database string) string {
	if database == "" {
		return "dropDatabase removes the database and all of its collections"
	}
	return fmt.Sprintf("dropDatabase removes the database %s and all of its collections", database)
}

func renameRisk(collection string) string {
	return fmt.Sprintf("renameCollection renames %s; code that uses the old name stops finding it", collection)
}

// RemovesOne reports whether remove's second argument limits it to one
// document, as mongosh reads it: the boolean true, or a document whose
// justOne is truthy. Any other value removes every match. The classifier
// and the planner both use it, so a remove taken for one never deletes
// many without asking.
func RemovesOne(v Value) bool {
	switch v := v.(type) {
	case bool:
		return v
	case bson.D:
		return truthyField(v, "justOne")
	}
	return false
}

// isInline reports whether mapReduce's out returns its results instead of
// writing them to a collection.
func isInline(out Value) bool {
	d, ok := out.(bson.D)
	return ok && len(d) > 0 && d[0].Key == "inline"
}

// hasStageThatWrites reports whether a pipeline holds $out or $merge at any
// depth, so that none hides inside another stage.
func hasStageThatWrites(v Value) bool {
	switch v := v.(type) {
	case bson.D:
		for _, e := range v {
			if e.Key == "$out" || e.Key == "$merge" || hasStageThatWrites(e.Value) {
				return true
			}
		}
	case bson.A:
		for _, x := range v {
			if hasStageThatWrites(x) {
				return true
			}
		}
	case []Value:
		for _, x := range v {
			if hasStageThatWrites(x) {
				return true
			}
		}
	}
	return false
}

func truthyField(d bson.D, key string) bool {
	v, _ := field(d, key)
	return truthy(v)
}

func truthy(v Value) bool {
	switch v := v.(type) {
	case bool:
		return v
	case int32:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0 && !math.IsNaN(v)
	case string:
		return v != ""
	case nil, bson.Undefined:
		return false
	}
	return true
}

func isZero(v Value) bool {
	switch v := v.(type) {
	case int32:
		return v == 0
	case int64:
		return v == 0
	case float64:
		return v == 0
	case bson.Decimal128:
		n, _, err := v.BigInt()
		return err == nil && n.Sign() == 0
	case bool:
		return !v
	}
	return false
}

func asArray(v Value) bson.A {
	a, _ := v.(bson.A)
	return a
}

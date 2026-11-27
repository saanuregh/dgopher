package db

import (
	"context"
	"fmt"
	"strings"

	"dgopher/internal/mongosh"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// planDocOp is how op runs against database. It sends nothing, so what
// Parse accepts but Run cannot do is refused before anything is sent.
func planDocOp(database string, op mongosh.Op) (docPlan, error) {
	if op.Database != "" {
		database = op.Database
	}
	switch {
	case op.Collection == "" && op.Method == "use":
		return planUse(op)
	case op.Collection == "" && op.Method == "show":
		return planShow(op)
	case op.Collection == "":
		return planDatabase(database, op)
	}
	return planCollection(database, op)
}

func planCollection(database string, op mongosh.Op) (docPlan, error) {
	switch op.Method {
	case "find":
		return planFind(op)
	case "aggregate":
		cmd, err := aggregateCommand(op.Collection, op.Method, op.Args)
		if err != nil {
			return docPlan{}, err
		}
		return cursorChain(op, docPlan{command: cmd, cursor: true, write: mongosh.Classify(op).Writes, explainable: true})
	case "explain":
		if len(op.Chain) == 0 {
			return docPlan{}, fmt.Errorf("explain() needs the method it explains, as db.%s.explain().find()", op.Collection)
		}
		inner, err := planCollection(database, mongosh.Op{Collection: op.Collection, Method: op.Chain[0].Name, Args: op.Chain[0].Args, Chain: op.Chain[1:]})
		if err != nil {
			return docPlan{}, err
		}
		if !inner.explainable {
			return docPlan{}, fmt.Errorf("%s cannot be explained", op.Chain[0].Name)
		}
		return explainPlan(inner, op.Args)
	}
	p, err := collectionCommand(database, op)
	if err != nil {
		return docPlan{}, err
	}
	if len(op.Chain) > 0 {
		return docPlan{}, fmt.Errorf("%s does not return a cursor: .%s() cannot follow it", op.Method, op.Chain[0].Name)
	}
	return p, nil
}

// findFields are the find command's fields a cursor method or find's
// options set, by the method's name.
var findFields = nameSet("sort", "limit", "skip", "projection", "hint", "collation", "maxTimeMS", "comment", "batchSize",
	"allowDiskUse", "min", "max", "returnKey", "showRecordId", "noCursorTimeout", "allowPartialResults", "readConcern")

func planFind(op mongosh.Op) (docPlan, error) {
	cmd := bson.D{{Key: "find", Value: op.Collection}}
	if err := docFilterInto(&cmd, "filter", op.Method, op.Args, 0); err != nil {
		return docPlan{}, err
	}
	if v := docArg(op.Args, 1); v != nil {
		proj, err := asDocument(op.Method, v, "a projection document")
		if err != nil {
			return docPlan{}, err
		}
		cmd = append(cmd, bson.E{Key: "projection", Value: proj})
	}
	opts, err := docOptions(op.Method, docArg(op.Args, 2), withNames(findFields, "let", "singleBatch"))
	if err != nil {
		return docPlan{}, err
	}
	cmd = append(cmd, opts...)
	if len(op.Args) > 3 {
		return docPlan{}, fmt.Errorf("find takes a filter, a projection and options")
	}
	p := docPlan{cursor: true, explainable: true}
	for i, call := range op.Chain {
		switch call.Name {
		case "toArray", "pretty":
			continue
		case "count", "size", "itcount", "explain":
			if i < len(op.Chain)-1 {
				return docPlan{}, fmt.Errorf("%s() cannot follow %s()", op.Chain[i+1].Name, call.Name)
			}
			p.command = cmd
			if call.Name == "explain" {
				return explainPlan(p, call.Args)
			}
			return countOfFind(cmd, call)
		case "readPref":
			rp, err := readPrefArg(call.Args)
			if err != nil {
				return docPlan{}, err
			}
			p.readPref = rp
			continue
		}
		if !findFields[call.Name] {
			return docPlan{}, fmt.Errorf("the cursor method %s() is not supported: the results load as you scroll", call.Name)
		}
		v, err := cursorArg(call)
		if err != nil {
			return docPlan{}, err
		}
		if call.Name == "limit" {
			// A negative limit asks for one batch of that many.
			cmd = withoutDocField(cmd, "singleBatch")
			if n := v.(int64); n < 0 {
				cmd = setDocField(setDocField(cmd, "limit", -n), "singleBatch", true)
				continue
			}
		}
		cmd = setDocField(cmd, call.Name, v)
	}
	p.command = cmd
	return p, nil
}

// cursorArg is the find field's value a cursor method gives.
func cursorArg(call mongosh.Call) (any, error) {
	v := docArg(call.Args, 0)
	switch call.Name {
	case "allowDiskUse", "returnKey", "showRecordId", "noCursorTimeout", "allowPartialResults":
		if v == nil {
			return true, nil
		}
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return nil, fmt.Errorf("%s expects true or false", call.Name)
	case "limit", "skip", "maxTimeMS", "batchSize":
		if n, ok := docInteger(v); ok {
			return n, nil
		}
		return nil, fmt.Errorf("%s expects a number", call.Name)
	case "hint":
		if _, ok := v.(string); ok {
			return v, nil
		}
		return asDocument(call.Name, v, "an index name or key pattern")
	case "comment":
		if v == nil {
			return nil, fmt.Errorf("comment expects a value")
		}
		return v, nil
	case "readConcern":
		if s, ok := v.(string); ok {
			return bson.D{{Key: "level", Value: s}}, nil
		}
		return nil, fmt.Errorf("readConcern expects a level, as \"majority\"")
	}
	return asDocument(call.Name, v, "a document")
}

// countOfFind is the count a cursor's count(), size() or itcount() asks:
// count() leaves out skip and limit unless given true.
func countOfFind(find bson.D, call mongosh.Call) (docPlan, error) {
	withLimits := call.Name != "count" || docArg(call.Args, 0) == true
	cmd := bson.D{{Key: "count", Value: find[0].Value}}
	for _, e := range find[1:] {
		switch e.Key {
		case "filter":
			cmd = append(cmd, bson.E{Key: "query", Value: e.Value})
		case "skip", "limit":
			if withLimits {
				cmd = append(cmd, e)
			}
		case "hint", "collation", "maxTimeMS", "comment", "readConcern":
			cmd = append(cmd, e)
		}
	}
	return docPlan{command: cmd, explainable: true, finish: valueReply(call.Name, "n")}, nil
}

// cursorChain accepts what may follow aggregate: toArray, pretty and,
// last, explain.
func cursorChain(op mongosh.Op, p docPlan) (docPlan, error) {
	for i, call := range op.Chain {
		switch call.Name {
		case "toArray", "pretty":
		case "explain":
			if i < len(op.Chain)-1 {
				return docPlan{}, fmt.Errorf("%s() cannot follow explain()", op.Chain[i+1].Name)
			}
			return explainPlan(p, call.Args)
		default:
			return docPlan{}, fmt.Errorf("aggregate's cursor has no %s() here: set it in aggregate's options", call.Name)
		}
	}
	return p, nil
}

// explainPlan explains inner's command, at the verbosity args give.
func explainPlan(inner docPlan, args []mongosh.Value) (docPlan, error) {
	verbosity := "queryPlanner"
	switch v := docArg(args, 0).(type) {
	case nil:
	case string:
		verbosity = v
	case bool:
		if v {
			verbosity = "allPlansExecution"
		}
	default:
		return docPlan{}, fmt.Errorf("explain expects a verbosity, as \"executionStats\"")
	}
	return docPlan{command: bson.D{{Key: "explain", Value: inner.command}, {Key: "verbosity", Value: verbosity}},
		admin: inner.admin, readPref: inner.readPref}, nil
}

func aggregateCommand(target any, method string, args []mongosh.Value) (bson.D, error) {
	var pipeline bson.A
	var opts mongosh.Value
	switch first := docArg(args, 0).(type) {
	case bson.A:
		pipeline = first
		opts = docArg(args, 1)
		if len(args) > 2 {
			return nil, fmt.Errorf("%s takes a pipeline and options", method)
		}
	case nil:
		pipeline = bson.A{}
	default:
		for _, a := range args {
			if _, ok := a.(bson.D); !ok {
				return nil, fmt.Errorf("%s expects a pipeline: an array of stages", method)
			}
			pipeline = append(pipeline, a)
		}
	}
	o, err := docOptions(method, opts, nameSet("allowDiskUse", "maxTimeMS", "comment", "hint", "collation", "let",
		"bypassDocumentValidation", "readConcern", "writeConcern", "batchSize"))
	if err != nil {
		return nil, err
	}
	cursor := bson.D{}
	var rest bson.D
	for _, e := range o {
		if e.Key == "batchSize" {
			cursor = append(cursor, e)
		} else {
			rest = append(rest, e)
		}
	}
	return append(bson.D{{Key: "aggregate", Value: target}, {Key: "pipeline", Value: pipeline}, {Key: "cursor", Value: cursor}}, rest...), nil
}

// collectionCommand plans the collection methods that return no cursor
// to page, and getIndexes.
func collectionCommand(database string, op mongosh.Op) (docPlan, error) {
	coll, m, args := op.Collection, op.Method, op.Args
	statsOf := func(key, field string) (docPlan, error) {
		return docPlan{command: bson.D{{Key: "collStats", Value: coll}}, finish: valueReply(key, field)}, nil
	}
	switch m {
	case "findOne":
		p, err := planFind(mongosh.Op{Collection: coll, Method: m, Args: args})
		if err != nil {
			return docPlan{}, err
		}
		p.command = setDocField(setDocField(p.command, "limit", int64(1)), "singleBatch", true)
		p.cursor, p.explainable, p.finish = false, false, firstOfBatch
		return p, nil
	case "countDocuments":
		return countDocumentsPlan(coll, args)
	case "estimatedDocumentCount":
		cmd := bson.D{{Key: "count", Value: coll}}
		o, err := docOptions(m, docArg(args, 0), nameSet("maxTimeMS", "comment"))
		return docPlan{command: append(cmd, o...), finish: valueReply(m, "n")}, err
	case "count":
		cmd := bson.D{{Key: "count", Value: coll}}
		if err := docFilterInto(&cmd, "query", m, args, 0); err != nil {
			return docPlan{}, err
		}
		o, err := docOptions(m, docArg(args, 1), nameSet("limit", "skip", "hint", "maxTimeMS", "readConcern", "collation", "comment"))
		return docPlan{command: append(cmd, o...), explainable: true, finish: valueReply(m, "n")}, err
	case "distinct":
		key, ok := docArg(args, 0).(string)
		if !ok {
			return docPlan{}, fmt.Errorf("distinct expects a field name")
		}
		cmd := bson.D{{Key: "distinct", Value: coll}, {Key: "key", Value: key}}
		if err := docFilterInto(&cmd, "query", m, args, 1); err != nil {
			return docPlan{}, err
		}
		o, err := docOptions(m, docArg(args, 2), nameSet("collation", "maxTimeMS", "comment", "hint", "readConcern"))
		return docPlan{command: append(cmd, o...), explainable: true, finish: valueReply(m, "values")}, err
	case "getIndexes", "getIndexSpecs":
		return docPlan{command: bson.D{{Key: "listIndexes", Value: coll}}, cursor: true}, nil
	case "getIndexKeys":
		return docPlan{do: func(ctx context.Context, d *Doc, db *mongo.Database) (DocResult, error) {
			idx, err := d.Indexes(ctx, db.Name(), coll)
			if err != nil {
				return DocResult{Affected: -1}, err
			}
			keys := bson.A{}
			for _, i := range idx {
				keys = append(keys, i.Keys)
			}
			return replyOf(bson.D{{Key: m, Value: keys}})
		}}, nil
	case "stats":
		cmd := bson.D{{Key: "collStats", Value: coll}}
		if n, ok := docInteger(docArg(args, 0)); ok {
			return docPlan{command: append(cmd, bson.E{Key: "scale", Value: n})}, nil
		}
		o, err := docOptions(m, docArg(args, 0), nameSet("scale"))
		return docPlan{command: append(cmd, o...)}, err
	case "dataSize":
		return statsOf(m, "size")
	case "storageSize", "totalIndexSize":
		return statsOf(m, m)
	case "isCapped":
		return statsOf(m, "capped")
	case "totalSize":
		return docPlan{command: bson.D{{Key: "collStats", Value: coll}}, finish: func(reply bson.Raw) (DocResult, error) {
			s, _ := reply.Lookup("storageSize").AsInt64OK()
			i, _ := reply.Lookup("totalIndexSize").AsInt64OK()
			return replyOf(bson.D{{Key: m, Value: s + i}})
		}}, nil
	case "validate":
		o, err := docOptions(m, docArg(args, 0), nameSet("full", "repair", "metadata", "checkBSONConformance", "background"))
		return docPlan{command: append(bson.D{{Key: "validate", Value: coll}}, o...), write: mongosh.Classify(op).Writes}, err
	case "latencyStats":
		o, err := docOptions(m, docArg(args, 0), nameSet("histograms"))
		cmd, _ := aggregateCommand(coll, m, []mongosh.Value{bson.A{bson.D{{Key: "$collStats", Value: bson.D{{Key: "latencyStats", Value: o}}}}}})
		return docPlan{command: cmd, cursor: true}, err
	case "getSearchIndexes":
		stage := bson.D{}
		if name, ok := docArg(args, 0).(string); ok {
			stage = bson.D{{Key: "name", Value: name}}
		}
		cmd, _ := aggregateCommand(coll, m, []mongosh.Value{bson.A{bson.D{{Key: "$listSearchIndexes", Value: stage}}}})
		return docPlan{command: cmd, cursor: true}, nil
	case "getName":
		return docPlan{do: func(context.Context, *Doc, *mongo.Database) (DocResult, error) {
			return replyOf(bson.D{{Key: m, Value: coll}})
		}}, nil
	case "getFullName":
		return docPlan{do: func(context.Context, *Doc, *mongo.Database) (DocResult, error) {
			return replyOf(bson.D{{Key: m, Value: database + "." + coll}})
		}}, nil
	case "bulkWrite":
		return bulkWritePlan(coll, args)
	}
	if p, ok, err := writeCommand(database, coll, m, args); ok {
		p.write = true
		return p, err
	}
	return docPlan{}, fmt.Errorf("db.%s.%s() is not supported here", coll, m)
}

func countDocumentsPlan(coll string, args []mongosh.Value) (docPlan, error) {
	filter := bson.D{}
	if v := docArg(args, 0); v != nil {
		f, err := asDocument("countDocuments", v, "a filter document")
		if err != nil {
			return docPlan{}, err
		}
		filter = f
	}
	o, err := docOptions("countDocuments", docArg(args, 1), nameSet("skip", "limit", "hint", "maxTimeMS", "collation", "comment"))
	if err != nil {
		return docPlan{}, err
	}
	pipeline := bson.A{bson.D{{Key: "$match", Value: filter}}}
	var rest bson.D
	for _, e := range o {
		switch e.Key {
		case "skip", "limit":
			pipeline = append(pipeline, bson.D{{Key: "$" + e.Key, Value: e.Value}})
		default:
			rest = append(rest, e)
		}
	}
	pipeline = append(pipeline, bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: int32(1)}, {Key: "n", Value: bson.D{{Key: "$sum", Value: int32(1)}}}}}})
	cmd := append(bson.D{{Key: "aggregate", Value: coll}, {Key: "pipeline", Value: pipeline}, {Key: "cursor", Value: bson.D{}}}, rest...)
	return docPlan{command: cmd, finish: countOfBatch("countDocuments")}, nil
}

// readPrefArg is the read preference readPref(mode) names.
func readPrefArg(args []mongosh.Value) (*readpref.ReadPref, error) {
	s, _ := docArg(args, 0).(string)
	mode, err := readpref.ModeFromString(s)
	if err != nil {
		return nil, fmt.Errorf("readPref: unknown read preference %q", s)
	}
	return readpref.New(mode)
}

// Helpers for arguments.

func docArg(args []mongosh.Value, i int) mongosh.Value {
	if i < len(args) {
		return args[i]
	}
	return nil
}

func nameSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// keys is the set with more names.
func withNames(s map[string]bool, more ...string) map[string]bool {
	out := nameSet(more...)
	for k := range s {
		out[k] = true
	}
	return out
}

// document is v as a document, or an error saying method expects what.
func asDocument(method string, v mongosh.Value, what string) (bson.D, error) {
	if d, ok := v.(bson.D); ok {
		return d, nil
	}
	return nil, fmt.Errorf("%s expects %s", method, what)
}

// filterInto appends args[i] as the field key when it is given.
func docFilterInto(cmd *bson.D, key, method string, args []mongosh.Value, i int) error {
	if i >= len(args) {
		return nil
	}
	if args[i] == nil {
		*cmd = append(*cmd, bson.E{Key: key, Value: bson.D{}})
		return nil
	}
	f, err := asDocument(method, args[i], "a filter document")
	if err != nil {
		return err
	}
	*cmd = append(*cmd, bson.E{Key: key, Value: f})
	return nil
}

// options is an options argument's fields, each one allowed; nil
// allowed takes any, for commands whose options the server checks.
func docOptions(method string, v mongosh.Value, allowed map[string]bool) (bson.D, error) {
	if v == nil {
		return nil, nil
	}
	d, ok := v.(bson.D)
	if !ok {
		return nil, fmt.Errorf("%s expects an options document", method)
	}
	for _, e := range d {
		if allowed != nil && !allowed[e.Key] {
			return nil, fmt.Errorf("%s: unknown option %q", method, e.Key)
		}
	}
	return d, nil
}

func docInteger(v mongosh.Value) (int64, bool) {
	switch n := v.(type) {
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
	}
	return 0, false
}

// setField sets key in d, in place of an earlier value.
func setDocField(d bson.D, key string, v any) bson.D {
	for i := range d {
		if d[i].Key == key {
			d[i].Value = v
			return d
		}
	}
	return append(d, bson.E{Key: key, Value: v})
}

func withoutDocField(d bson.D, key string) bson.D {
	out := d[:0:0]
	for _, e := range d {
		if e.Key != key {
			out = append(out, e)
		}
	}
	return out
}

// hasOperators reports whether an update is an aggregation pipeline or
// a document of update operators, not a replacement.
func isOperatorUpdate(v mongosh.Value) bool {
	switch u := v.(type) {
	case bson.A:
		return true
	case bson.D:
		return len(u) > 0 && strings.HasPrefix(u[0].Key, "$")
	}
	return false
}

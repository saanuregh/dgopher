package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dgopher/internal/mongosh"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// writeOptions are the fields of a write command that a write method's
// options may set, beside those of each statement.
var writeOptions = nameSet("writeConcern", "bypassDocumentValidation", "comment", "let")

// writeCommand plans a collection method that writes; false when m is
// none.
func writeCommand(database, coll, m string, args []mongosh.Value) (docPlan, bool, error) {
	var p docPlan
	var err error
	switch m {
	case "insertOne", "insertMany", "insert":
		p, err = insertPlan(coll, m, args)
	case "updateOne", "updateMany", "replaceOne", "update":
		p, err = updatePlan(coll, m, args)
	case "deleteOne", "deleteMany", "remove":
		p, err = deletePlan(coll, m, args)
	case "findOneAndUpdate", "findOneAndReplace", "findOneAndDelete", "findAndModify":
		p, err = findAndModifyPlan(coll, m, args)
	case "drop":
		o, oerr := docOptions(m, docArg(args, 0), nameSet("writeConcern"))
		p, err = docPlan{command: append(bson.D{{Key: "drop", Value: coll}}, o...)}, oerr
	case "createIndex", "createIndexes":
		p, err = createIndexPlan(coll, m, args)
	case "dropIndex", "dropIndexes":
		index := docArg(args, 0)
		switch {
		case index == nil && m == "dropIndex":
			err = errors.New("dropIndex expects an index name or key pattern")
		case index == nil:
			index = "*"
		}
		p = docPlan{command: bson.D{{Key: "dropIndexes", Value: coll}, {Key: "index", Value: index}}}
	case "hideIndex", "unhideIndex":
		var spec bson.D
		switch v := docArg(args, 0).(type) {
		case string:
			spec = bson.D{{Key: "name", Value: v}}
		case bson.D:
			spec = bson.D{{Key: "keyPattern", Value: v}}
		default:
			err = fmt.Errorf("%s expects an index name or key pattern", m)
		}
		spec = append(spec, bson.E{Key: "hidden", Value: m == "hideIndex"})
		p = docPlan{command: bson.D{{Key: "collMod", Value: coll}, {Key: "index", Value: spec}}}
	case "renameCollection":
		to, ok := docArg(args, 0).(string)
		if !ok || to == "" {
			return docPlan{}, true, errors.New("renameCollection expects the new name")
		}
		cmd := bson.D{{Key: "renameCollection", Value: database + "." + coll}, {Key: "to", Value: database + "." + to}}
		if v := docArg(args, 1); v != nil {
			b, ok := v.(bool)
			if !ok {
				return docPlan{}, true, errors.New("renameCollection's second argument, dropTarget, is true or false")
			}
			cmd = append(cmd, bson.E{Key: "dropTarget", Value: b})
		}
		p = docPlan{command: cmd, admin: true}
	default:
		return docPlan{}, false, nil
	}
	return p, true, err
}

func insertPlan(coll, m string, args []mongosh.Value) (docPlan, error) {
	var docs bson.A
	switch v := docArg(args, 0).(type) {
	case bson.D:
		if m == "insertMany" {
			return docPlan{}, errors.New("insertMany expects an array of documents")
		}
		docs = bson.A{v}
	case bson.A:
		if m == "insertOne" {
			return docPlan{}, errors.New("insertOne expects a document")
		}
		docs = v
	default:
		return docPlan{}, fmt.Errorf("%s expects a document", m)
	}
	allowed := writeOptions
	if m != "insertOne" {
		allowed = withNames(writeOptions, "ordered")
	}
	o, err := docOptions(m, docArg(args, 1), allowed)
	if err != nil {
		return docPlan{}, err
	}
	ids := make([]any, len(docs))
	withIDs := make(bson.A, len(docs))
	for i, x := range docs {
		d, ok := x.(bson.D)
		if !ok {
			return docPlan{}, fmt.Errorf("%s expects documents; item %d is not one", m, i+1)
		}
		if id, ok := docField(d, "_id"); ok {
			ids[i] = id
		} else {
			id := bson.NewObjectID()
			ids[i] = id
			d = append(bson.D{{Key: "_id", Value: id}}, d...)
		}
		withIDs[i] = d
	}
	cmd := append(bson.D{{Key: "insert", Value: coll}, {Key: "documents", Value: withIDs}}, o...)
	return docPlan{command: cmd, finish: insertReply(ids, isArray(docArg(args, 0)))}, nil
}

func isArray(v mongosh.Value) bool {
	_, ok := v.(bson.A)
	return ok
}

func updatePlan(coll, m string, args []mongosh.Value) (docPlan, error) {
	filter, err := asDocument(m, docArg(args, 0), "a filter document")
	if err != nil {
		return docPlan{}, err
	}
	u := docArg(args, 1)
	if _, ok := u.(bson.D); !ok && (m == "replaceOne" || !isArray(u)) {
		return docPlan{}, fmt.Errorf("%s expects an update document or pipeline", m)
	}
	stmtKeys := nameSet("upsert", "arrayFilters", "collation", "hint", "sort")
	if m == "update" {
		stmtKeys["multi"] = true
	}
	stmt, rest, err := splitOptions(m, docArg(args, 2), stmtKeys)
	if err != nil {
		return docPlan{}, err
	}
	switch {
	case m == "replaceOne" && isOperatorUpdate(u):
		return docPlan{}, errors.New("replaceOne expects a replacement document, without update operators; use updateOne to change fields")
	case (m == "updateOne" || m == "updateMany") && !isOperatorUpdate(u):
		return docPlan{}, fmt.Errorf("%s expects update operators, as {$set: {…}}; use replaceOne to replace the document", m)
	}
	s := bson.D{{Key: "q", Value: filter}, {Key: "u", Value: u}}
	if m == "updateMany" {
		s = append(s, bson.E{Key: "multi", Value: true})
	}
	s = append(s, stmt...)
	cmd := append(bson.D{{Key: "update", Value: coll}, {Key: "updates", Value: bson.A{s}}}, rest...)
	return docPlan{command: cmd, explainable: true, finish: updateReply}, nil
}

func deletePlan(coll, m string, args []mongosh.Value) (docPlan, error) {
	filter := bson.D{}
	if v := docArg(args, 0); v != nil || m != "remove" {
		f, err := asDocument(m, v, "a filter document")
		if err != nil {
			return docPlan{}, err
		}
		filter = f
	}
	limit := int32(0)
	if m == "deleteOne" {
		limit = 1
	}
	opts := docArg(args, 1)
	if m == "remove" {
		// remove(filter, true) or remove(filter, {justOne: true}), read by
		// the rule the classifier uses.
		if mongosh.RemovesOne(opts) {
			limit = 1
		}
		switch v := opts.(type) {
		case bool:
			opts = nil
		case bson.D:
			if _, ok := docField(v, "justOne"); ok {
				opts = withoutDocField(v, "justOne")
			}
		}
	}
	stmt, rest, err := splitOptions(m, opts, nameSet("collation", "hint"))
	if err != nil {
		return docPlan{}, err
	}
	s := append(bson.D{{Key: "q", Value: filter}, {Key: "limit", Value: limit}}, stmt...)
	cmd := append(bson.D{{Key: "delete", Value: coll}, {Key: "deletes", Value: bson.A{s}}}, rest...)
	return docPlan{command: cmd, explainable: true, finish: deleteReply}, nil
}

// splitOptions parts write options into those of the statement and those
// of the command.
func splitOptions(m string, v mongosh.Value, stmtKeys map[string]bool) (stmt, cmd bson.D, err error) {
	o, err := docOptions(m, v, withNames(stmtKeys, keysOf(writeOptions)...))
	if err != nil {
		return nil, nil, err
	}
	for _, e := range o {
		if stmtKeys[e.Key] {
			stmt = append(stmt, e)
		} else {
			cmd = append(cmd, e)
		}
	}
	return stmt, cmd, nil
}

func keysOf(s map[string]bool) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	return out
}

func findAndModifyPlan(coll, m string, args []mongosh.Value) (docPlan, error) {
	cmd := bson.D{{Key: "findAndModify", Value: coll}}
	if m == "findAndModify" {
		spec, err := asDocument(m, docArg(args, 0), "a document of query, update or remove, and options")
		if err != nil {
			return docPlan{}, err
		}
		o, err := docOptions(m, spec, withNames(writeOptions, "query", "sort", "remove", "update", "new", "fields", "upsert",
			"arrayFilters", "collation", "hint", "maxTimeMS"))
		if err != nil {
			return docPlan{}, err
		}
		return docPlan{command: append(cmd, o...), explainable: true, finish: findAndModifyReply}, nil
	}
	filter, err := asDocument(m, docArg(args, 0), "a filter document")
	if err != nil {
		return docPlan{}, err
	}
	cmd = append(cmd, bson.E{Key: "query", Value: filter})
	optsAt := 2
	switch m {
	case "findOneAndDelete":
		cmd = append(cmd, bson.E{Key: "remove", Value: true})
		optsAt = 1
	case "findOneAndUpdate":
		if !isOperatorUpdate(docArg(args, 1)) {
			return docPlan{}, errors.New("findOneAndUpdate expects update operators, as {$set: {…}}; use findOneAndReplace to replace the document")
		}
		cmd = append(cmd, bson.E{Key: "update", Value: docArg(args, 1)})
	case "findOneAndReplace":
		r, ok := docArg(args, 1).(bson.D)
		if !ok || isOperatorUpdate(r) {
			return docPlan{}, errors.New("findOneAndReplace expects a replacement document, without update operators")
		}
		cmd = append(cmd, bson.E{Key: "update", Value: r})
	}
	allowed := withNames(writeOptions, "projection", "sort", "collation", "hint", "maxTimeMS")
	if m != "findOneAndDelete" {
		allowed = withNames(allowed, "upsert", "returnDocument", "returnNewDocument")
		if m == "findOneAndUpdate" {
			allowed["arrayFilters"] = true
		}
	}
	o, err := docOptions(m, docArg(args, optsAt), allowed)
	if err != nil {
		return docPlan{}, err
	}
	for _, e := range o {
		switch e.Key {
		case "projection":
			cmd = append(cmd, bson.E{Key: "fields", Value: e.Value})
		case "returnDocument":
			switch e.Value {
			case "after":
				cmd = append(cmd, bson.E{Key: "new", Value: true})
			case "before":
			default:
				return docPlan{}, fmt.Errorf(`%s: returnDocument is "before" or "after"`, m)
			}
		case "returnNewDocument":
			cmd = append(cmd, bson.E{Key: "new", Value: e.Value})
		default:
			cmd = append(cmd, e)
		}
	}
	return docPlan{command: cmd, explainable: true, finish: findAndModifyReply}, nil
}

func createIndexPlan(coll, m string, args []mongosh.Value) (docPlan, error) {
	var specs bson.A
	if m == "createIndex" {
		specs = bson.A{docArg(args, 0)}
	} else {
		a, ok := docArg(args, 0).(bson.A)
		if !ok {
			return docPlan{}, errors.New("createIndexes expects an array of key patterns")
		}
		specs = a
	}
	opts, err := docOptions(m, docArg(args, 1), nil)
	if err != nil {
		return docPlan{}, err
	}
	indexes := make(bson.A, len(specs))
	for i, s := range specs {
		key, ok := s.(bson.D)
		if !ok || len(key) == 0 {
			return docPlan{}, fmt.Errorf("%s expects a key pattern, as {name: 1}", m)
		}
		index := bson.D{{Key: "key", Value: key}, {Key: "name", Value: indexName(key)}}
		for _, e := range opts {
			index = setDocField(index, e.Key, e.Value)
		}
		indexes[i] = index
	}
	cmd := bson.D{{Key: "createIndexes", Value: coll}, {Key: "indexes", Value: indexes}}
	if q := docArg(args, 2); q != nil {
		cmd = append(cmd, bson.E{Key: "commitQuorum", Value: q})
	}
	return docPlan{command: cmd}, nil
}

// indexName is the name the shell gives an index of key: each field and
// its direction or type, joined by _.
func indexName(key bson.D) string {
	parts := make([]string, 0, 2*len(key))
	for _, e := range key {
		parts = append(parts, e.Key, fmt.Sprint(e.Value))
	}
	return strings.Join(parts, "_")
}

// docField is the value of key in d.
func docField(d bson.D, key string) (any, bool) {
	for _, e := range d {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

// bulkWriteRequests are the fields of each request of bulkWrite.
var bulkWriteRequests = map[string]map[string]bool{
	"insertOne":  nameSet("document"),
	"updateOne":  nameSet("filter", "update", "upsert", "arrayFilters", "hint"),
	"updateMany": nameSet("filter", "update", "upsert", "arrayFilters", "hint"),
	"replaceOne": nameSet("filter", "replacement", "upsert", "hint"),
	"deleteOne":  nameSet("filter", "hint"),
	"deleteMany": nameSet("filter", "hint"),
}

func bulkWritePlan(coll string, args []mongosh.Value) (docPlan, error) {
	requests, ok := docArg(args, 0).(bson.A)
	if !ok {
		return docPlan{}, errors.New("bulkWrite expects an array of requests")
	}
	models := make([]mongo.WriteModel, 0, len(requests))
	for i, r := range requests {
		req, ok := r.(bson.D)
		if !ok || len(req) != 1 {
			return docPlan{}, fmt.Errorf("bulkWrite: request %d is not {insertOne: {…}} or another request", i+1)
		}
		kind := req[0].Key
		fields, known := bulkWriteRequests[kind]
		if !known {
			return docPlan{}, fmt.Errorf("bulkWrite: unknown request %q", kind)
		}
		if _, ok := req[0].Value.(bson.D); !ok {
			return docPlan{}, fmt.Errorf("bulkWrite: request %d, %s, expects a document", i+1, kind)
		}
		body, err := docOptions("bulkWrite "+kind, req[0].Value, fields)
		if err != nil {
			return docPlan{}, err
		}
		models = append(models, writeModel(kind, body))
	}
	o, err := docOptions("bulkWrite", docArg(args, 1), nameSet("ordered", "bypassDocumentValidation", "comment", "let", "writeConcern"))
	if err != nil {
		return docPlan{}, err
	}
	opts := options.BulkWrite()
	for _, e := range o {
		switch e.Key {
		case "ordered":
			opts.SetOrdered(e.Value == true)
		case "bypassDocumentValidation":
			opts.SetBypassDocumentValidation(e.Value == true)
		case "comment":
			opts.SetComment(e.Value)
		case "let":
			opts.SetLet(e.Value)
		case "writeConcern":
			return docPlan{}, errors.New("bulkWrite: writeConcern is the connection's here")
		}
	}
	return docPlan{write: true, do: func(ctx context.Context, _ *Doc, db *mongo.Database) (DocResult, error) {
		r, err := db.Collection(coll).BulkWrite(ctx, models, opts)
		res := DocResult{Affected: -1}
		if r != nil {
			ids := bson.D{}
			for i, id := range r.UpsertedIDs {
				ids = append(ids, bson.E{Key: fmt.Sprint(i), Value: id})
			}
			res, _ = replyOf(bson.D{{Key: "acknowledged", Value: r.Acknowledged}, {Key: "insertedCount", Value: r.InsertedCount},
				{Key: "matchedCount", Value: r.MatchedCount}, {Key: "modifiedCount", Value: r.ModifiedCount},
				{Key: "deletedCount", Value: r.DeletedCount}, {Key: "upsertedCount", Value: r.UpsertedCount}, {Key: "upsertedIds", Value: ids}})
			res.Affected = r.InsertedCount + r.ModifiedCount + r.DeletedCount + r.UpsertedCount
		}
		if err != nil {
			if res.Affected > 0 {
				return res, fmt.Errorf("bulkWrite changed %d documents, then failed: %w", res.Affected, err)
			}
			return DocResult{Affected: -1}, err
		}
		return res, nil
	}}, nil
}

func writeModel(kind string, body bson.D) mongo.WriteModel {
	get := func(k string) any { v, _ := docField(body, k); return v }
	upsert := get("upsert") == true
	switch kind {
	case "insertOne":
		return mongo.NewInsertOneModel().SetDocument(get("document"))
	case "updateOne":
		m := mongo.NewUpdateOneModel().SetFilter(get("filter")).SetUpdate(get("update")).SetUpsert(upsert)
		if a, ok := get("arrayFilters").(bson.A); ok {
			m.SetArrayFilters([]any(a))
		}
		if h := get("hint"); h != nil {
			m.SetHint(h)
		}
		return m
	case "updateMany":
		m := mongo.NewUpdateManyModel().SetFilter(get("filter")).SetUpdate(get("update")).SetUpsert(upsert)
		if a, ok := get("arrayFilters").(bson.A); ok {
			m.SetArrayFilters([]any(a))
		}
		if h := get("hint"); h != nil {
			m.SetHint(h)
		}
		return m
	case "replaceOne":
		m := mongo.NewReplaceOneModel().SetFilter(get("filter")).SetReplacement(get("replacement")).SetUpsert(upsert)
		if h := get("hint"); h != nil {
			m.SetHint(h)
		}
		return m
	case "deleteOne":
		m := mongo.NewDeleteOneModel().SetFilter(get("filter"))
		if h := get("hint"); h != nil {
			m.SetHint(h)
		}
		return m
	}
	m := mongo.NewDeleteManyModel().SetFilter(get("filter"))
	if h := get("hint"); h != nil {
		m.SetHint(h)
	}
	return m
}

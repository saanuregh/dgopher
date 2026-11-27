package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"dgopher/internal/mongosh"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// DocResult is what a statement returns.
//
// A method whose value is a number, a string or a list, such as
// countDocuments or distinct, replies one field named for it:
// {countDocuments: 3}, {distinct: […]}. use replies {database: name}.
// A write replies as mongosh says it, as {acknowledged: true, insertedId: …}.
// findOne and the findOneAnd… methods reply the document, nil when none
// matched.
type DocResult struct {
	Cursor *DocCursor // find, aggregate, listX: documents to page through; nil otherwise
	Reply  bson.Raw   // a single reply: a command's, an insert's ids, a count
	// Affected is how many documents the statement inserted, changed or
	// deleted; -1 for reads, commands and changes of schema.
	Affected int64
}

// DocCursor is the documents of a statement's result, read a page at a
// time. Fetch and Close may be called from different goroutines: a
// mongo.Cursor is not safe for that, so Close stops a Fetch that is
// running and closes the cursor only once that Fetch has returned.
type DocCursor struct {
	cur *mongo.Cursor
	// use is held while the cursor is read or closed.
	use sync.Mutex
	// mu guards stop and closed.
	mu sync.Mutex
	// stop cancels the Fetch holding use; nil when none runs.
	stop   context.CancelFunc
	closed bool
}

// errCursorClosed is what a Fetch on a closed cursor returns.
var errCursorClosed = errors.New("the documents were closed before they were read")

// Fetch reads up to n more documents; done reports that none remain.
func (c *DocCursor) Fetch(ctx context.Context, n int) (docs []bson.Raw, done bool, err error) {
	c.use.Lock()
	defer c.use.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, false, errCursorClosed
	}
	c.stop = cancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.stop = nil
		c.mu.Unlock()
	}()
	for len(docs) < n {
		if !c.cur.Next(ctx) {
			if err := c.cur.Err(); err != nil {
				return docs, false, err
			}
			return docs, true, nil
		}
		docs = append(docs, slices.Clone(c.cur.Current))
	}
	return docs, c.cur.ID() == 0 && c.cur.RemainingBatchLength() == 0, nil
}

// Close ends the cursor on the server, first stopping a Fetch that is
// running; a second Close does nothing.
func (c *DocCursor) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	if c.stop != nil {
		c.stop()
	}
	c.mu.Unlock()
	c.use.Lock()
	defer c.use.Unlock()
	return c.cur.Close(ctx)
}

// Run runs one parsed statement against database, or the one the
// statement names (getSiblingDB, use). It keeps no state: for use, the
// caller takes the new database from the reply.
func (d *Doc) Run(ctx context.Context, database string, op mongosh.Op) (DocResult, error) {
	if op.Database != "" {
		database = op.Database
	}
	p, err := planDocOp(database, op)
	if err != nil {
		return DocResult{Affected: -1}, err
	}
	db := d.Client.Database(database)
	if p.do != nil {
		return p.do(ctx, d, db)
	}
	return d.runPlan(ctx, db, p, op.Method)
}

// Count is how many documents of coll match filter; nil matches all.
func (d *Doc) Count(ctx context.Context, database, coll string, filter any) (int64, error) {
	if filter == nil {
		filter = bson.D{}
	}
	return d.Client.Database(database).Collection(coll, options.Collection().SetReadPreference(d.readPref())).CountDocuments(ctx, filter)
}

// docPlan is how a statement runs: one command, or do.
type docPlan struct {
	command bson.D
	admin   bool // runs on the admin database
	cursor  bool // the reply is a cursor to page through
	write   bool // goes to the primary; finish says what it changed
	// readPref is the cursor's readPref(); nil for the connection's.
	readPref *readpref.ReadPref
	// explainable is set for commands explain takes.
	explainable bool
	// finish makes the result of the reply; nil keeps the reply as it is.
	finish func(reply bson.Raw) (DocResult, error)
	// do runs a statement that is not one command.
	do func(ctx context.Context, d *Doc, db *mongo.Database) (DocResult, error)
}

func (d *Doc) runPlan(ctx context.Context, db *mongo.Database, p docPlan, method string) (DocResult, error) {
	if p.admin {
		db = d.Client.Database("admin")
	}
	opts := options.RunCmd()
	switch {
	case p.readPref != nil:
		opts.SetReadPreference(p.readPref)
	case !p.write:
		if rp := d.readPref(); rp != nil {
			opts.SetReadPreference(rp)
		}
	}
	if p.cursor {
		cur, err := db.RunCommandCursor(ctx, p.command, opts)
		if err != nil {
			return DocResult{Affected: -1}, err
		}
		return DocResult{Cursor: &DocCursor{cur: cur}, Affected: -1}, nil
	}
	reply, err := db.RunCommand(ctx, p.command, opts).Raw()
	if err != nil {
		// A write that failed part way says what it did before.
		if p.write && p.finish != nil && reply != nil {
			if res, ferr := p.finish(reply); ferr == nil && res.Affected > 0 {
				return res, fmt.Errorf("%s changed %d documents, then failed: %w", method, res.Affected, err)
			}
		}
		return DocResult{Affected: -1}, err
	}
	if p.finish == nil {
		return DocResult{Reply: reply, Affected: -1}, nil
	}
	return p.finish(reply)
}

// readPref is the connection's read preference; nil for the primary.
func (d *Doc) readPref() *readpref.ReadPref {
	if d.Config.Mongo.ReadPreference == "" {
		return nil
	}
	mode, err := readpref.ModeFromString(d.Config.Mongo.ReadPreference)
	if err != nil {
		return nil
	}
	rp, err := readpref.New(mode)
	if err != nil {
		return nil
	}
	return rp
}

// replyOf is a result whose reply is doc.
func replyOf(doc any) (DocResult, error) {
	raw, err := bson.Marshal(doc)
	if err != nil {
		return DocResult{Affected: -1}, err
	}
	return DocResult{Reply: raw, Affected: -1}, nil
}

// cursorOf is a result of documents already read.
func cursorOf(docs []any) (DocResult, error) {
	cur, err := mongo.NewCursorFromDocuments(docs, nil, nil)
	if err != nil {
		return DocResult{Affected: -1}, err
	}
	return DocResult{Cursor: &DocCursor{cur: cur}, Affected: -1}, nil
}

// valueReply replies {key: the reply's field}, for a method whose value
// is one field of its command's reply.
func valueReply(key string, path ...string) func(bson.Raw) (DocResult, error) {
	return func(reply bson.Raw) (DocResult, error) {
		v, err := reply.LookupErr(path...)
		if err != nil {
			return DocResult{Affected: -1}, fmt.Errorf("%s: the reply has no %v", key, path)
		}
		return replyOf(bson.D{{Key: key, Value: v}})
	}
}

// arrayAsCursor pages through an array field of the reply, as
// listDatabases' databases.
func arrayAsCursor(field string) func(bson.Raw) (DocResult, error) {
	return func(reply bson.Raw) (DocResult, error) {
		arr, ok := reply.Lookup(field).ArrayOK()
		if !ok {
			return DocResult{Affected: -1}, fmt.Errorf("the reply has no %s", field)
		}
		vals, err := arr.Values()
		if err != nil {
			return DocResult{Affected: -1}, err
		}
		docs := make([]any, len(vals))
		for i, v := range vals {
			docs[i] = v.Document()
		}
		return cursorOf(docs)
	}
}

// firstOfBatch replies the first document of a cursor's first batch, nil
// when it is empty.
func firstOfBatch(reply bson.Raw) (DocResult, error) {
	arr, ok := reply.Lookup("cursor", "firstBatch").ArrayOK()
	if !ok {
		return DocResult{Affected: -1}, errors.New("the reply has no cursor")
	}
	first, err := arr.IndexErr(0)
	if err != nil {
		return DocResult{Affected: -1}, nil
	}
	doc, _ := first.DocumentOK()
	return DocResult{Reply: doc, Affected: -1}, nil
}

// countOfBatch replies {key: n} from a $group stage's {n} in a cursor's
// first batch, 0 when nothing matched.
func countOfBatch(key string) func(bson.Raw) (DocResult, error) {
	return func(reply bson.Raw) (DocResult, error) {
		first, err := firstOfBatch(reply)
		if err != nil {
			return first, err
		}
		var n int64
		if first.Reply != nil {
			n, _ = first.Reply.Lookup("n").AsInt64OK()
		}
		return replyOf(bson.D{{Key: key, Value: n}})
	}
}

func insertReply(ids []any, many bool) func(bson.Raw) (DocResult, error) {
	return func(reply bson.Raw) (DocResult, error) {
		n, _ := reply.Lookup("n").AsInt64OK()
		out := bson.D{{Key: "acknowledged", Value: true}}
		if many {
			out = append(out, bson.E{Key: "insertedIds", Value: bson.A(ids[:min(int(n), len(ids))])})
		} else if len(ids) > 0 {
			out = append(out, bson.E{Key: "insertedId", Value: ids[0]})
		}
		res, err := replyOf(out)
		res.Affected = n
		return res, err
	}
}

func updateReply(reply bson.Raw) (DocResult, error) {
	n, _ := reply.Lookup("n").AsInt64OK()
	modified, _ := reply.Lookup("nModified").AsInt64OK()
	var upserted []bson.RawValue
	if arr, ok := reply.Lookup("upserted").ArrayOK(); ok {
		upserted, _ = arr.Values()
	}
	var insertedID any
	if len(upserted) > 0 {
		insertedID = upserted[0].Document().Lookup("_id")
	}
	ups := int64(len(upserted))
	res, err := replyOf(bson.D{
		{Key: "acknowledged", Value: true},
		{Key: "insertedId", Value: insertedID},
		{Key: "matchedCount", Value: n - ups},
		{Key: "modifiedCount", Value: modified},
		{Key: "upsertedCount", Value: ups},
	})
	res.Affected = modified + ups
	return res, err
}

func deleteReply(reply bson.Raw) (DocResult, error) {
	n, _ := reply.Lookup("n").AsInt64OK()
	res, err := replyOf(bson.D{{Key: "acknowledged", Value: true}, {Key: "deletedCount", Value: n}})
	res.Affected = n
	return res, err
}

// findAndModifyReply replies the document findAndModify returns, nil for
// none.
func findAndModifyReply(reply bson.Raw) (DocResult, error) {
	n, _ := reply.Lookup("lastErrorObject", "n").AsInt64OK()
	res := DocResult{Affected: n}
	if doc, ok := reply.Lookup("value").DocumentOK(); ok {
		res.Reply = doc
	}
	return res, nil
}

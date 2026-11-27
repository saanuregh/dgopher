package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// DocEditKind is what a pending document edit does.
type DocEditKind int

const (
	// DocInsert inserts Doc.
	DocInsert DocEditKind = iota + 1
	// DocSetFields sets Set's fields and removes Unset's in the document
	// of ID, with Update.
	DocSetFields
	// DocDelete deletes the document of ID.
	DocDelete
)

// DocEdit is one pending change of a collection's documents, by _id.
type DocEdit struct {
	Kind  DocEditKind
	ID    bson.RawValue
	Doc   bson.D
	Set   bson.D
	Unset []string
}

// Update is the update of a DocSetFields edit: $set and $unset, or,
// when a field's name holds a dot or starts with $, which those read as
// a path or an operator, a pipeline that also sets and removes such
// fields by name with $setField and $unsetField. A server without them
// (before MongoDB 5.0) refuses it.
func (e DocEdit) Update() any {
	if !e.setsByName() {
		update := bson.D{}
		if len(e.Set) > 0 {
			update = append(update, bson.E{Key: "$set", Value: e.Set})
		}
		if len(e.Unset) > 0 {
			unset := make(bson.D, len(e.Unset))
			for j, f := range e.Unset {
				unset[j] = bson.E{Key: f, Value: ""}
			}
			update = append(update, bson.E{Key: "$unset", Value: unset})
		}
		return update
	}
	replaceRoot := func(op string, field any, more ...bson.E) bson.D {
		args := append(bson.D{{Key: "field", Value: field}, {Key: "input", Value: "$$ROOT"}}, more...)
		return bson.D{{Key: "$replaceWith", Value: bson.D{{Key: op, Value: args}}}}
	}
	literal := func(v any) bson.D { return bson.D{{Key: "$literal", Value: v}} }
	// A $ name would be read as a field path, so it is given as a literal.
	fieldName := func(name string) any {
		if strings.HasPrefix(name, "$") {
			return literal(name)
		}
		return name
	}
	var set bson.D
	var unset bson.A
	var byName bson.A
	for _, f := range e.Set {
		if !namedByPath(f.Key) {
			set = append(set, bson.E{Key: f.Key, Value: literal(f.Value)})
			continue
		}
		byName = append(byName, replaceRoot("$setField", fieldName(f.Key), bson.E{Key: "value", Value: literal(f.Value)}))
	}
	for _, name := range e.Unset {
		if !namedByPath(name) {
			unset = append(unset, name)
			continue
		}
		byName = append(byName, replaceRoot("$unsetField", fieldName(name)))
	}
	pipeline := bson.A{}
	if len(set) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$set", Value: set}})
	}
	if len(unset) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$unset", Value: unset}})
	}
	return append(pipeline, byName...)
}

// setsByName reports whether e's update is a pipeline that sets or
// removes a field by its name.
func (e DocEdit) setsByName() bool {
	return e.Kind == DocSetFields && (slices.ContainsFunc(e.Set, func(f bson.E) bool { return namedByPath(f.Key) }) ||
		slices.ContainsFunc(e.Unset, namedByPath))
}

// namedByPath reports whether $set and $unset would read a top-level
// field's name as other than the name: as a path, for one holding a dot,
// or as an operator, for one starting with $.
func namedByPath(name string) bool {
	return strings.Contains(name, ".") || strings.HasPrefix(name, "$")
}

// DocApplied is what ApplyEdits saved.
type DocApplied struct {
	// Applied is how many edits stayed applied: all or none in a
	// transaction.
	Applied int
}

// errEditMissed is an edit's document that is no longer as it was read.
var errEditMissed = errors.New("matched 0 documents: it was changed or deleted since it was read")

// ApplyEdits saves edits to coll in order, each required to change
// exactly one document by its _id. Where the server has transactions,
// they run in one, rolled back on any failure or when stop is set;
// elsewhere they run one at a time and stop at the first failure or at
// stop, and those before it stay.
func (d *Doc) ApplyEdits(ctx context.Context, database, coll string, edits []DocEdit, stop *atomic.Bool) (DocApplied, error) {
	if err := d.refuseEdits(ctx, edits); err != nil {
		return DocApplied{}, err
	}
	c := d.Client.Database(database).Collection(coll)
	if !d.Transactions() {
		for i, e := range edits {
			if stop != nil && stop.Load() {
				return DocApplied{Applied: i}, fmt.Errorf("stopped before edit %d; the %d before it stay saved", i+1, i)
			}
			if err := applyEdit(ctx, c, i, e); err != nil {
				return DocApplied{Applied: i}, err
			}
		}
		return DocApplied{Applied: len(edits)}, nil
	}

	// The transaction is started, committed and aborted by hand, not by
	// WithTransaction, which runs the edits again on a transient error.
	sess, err := d.Client.StartSession()
	if err != nil {
		return DocApplied{}, err
	}
	defer sess.EndSession(context.WithoutCancel(ctx))
	if err := sess.StartTransaction(); err != nil {
		return DocApplied{}, err
	}
	abort := func(cause error) (DocApplied, error) {
		// Rolled back even when ctx is cancelled, so no transaction is
		// left open on the server until it times out.
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := sess.AbortTransaction(actx); err != nil {
			return DocApplied{}, fmt.Errorf("%w; rolling back failed too: %v", cause, err)
		}
		return DocApplied{}, fmt.Errorf("%w; nothing was saved", cause)
	}
	sctx := mongo.NewSessionContext(ctx, sess)
	for i, e := range edits {
		if stop != nil && stop.Load() {
			return abort(fmt.Errorf("stopped before edit %d", i+1))
		}
		if err := applyEdit(sctx, c, i, e); err != nil {
			return abort(err)
		}
	}
	if stop != nil && stop.Load() {
		return abort(errors.New("stopped before saving"))
	}
	if err := sess.CommitTransaction(ctx); err != nil {
		var labeled mongo.LabeledError
		if errors.As(err, &labeled) && labeled.HasErrorLabel("UnknownTransactionCommitResult") {
			return DocApplied{}, fmt.Errorf("saving the %d edits may or may not have happened; reload to see: %w", len(edits), err)
		}
		// A commit that failed otherwise left the transaction rolled back,
		// and it cannot be aborted after a commit.
		return DocApplied{}, fmt.Errorf("saving the edits: %w; nothing was saved", err)
	}
	return DocApplied{Applied: len(edits)}, nil
}

// refuseEdits says why edits cannot be saved on the server, before any
// is sent: the server as it names itself, whatever the connection says.
func (d *Doc) refuseEdits(ctx context.Context, edits []DocEdit) error {
	i := slices.IndexFunc(edits, DocEdit.setsByName)
	if i < 0 {
		return nil
	}
	e, _, err := d.Identify(ctx)
	if err != nil {
		return fmt.Errorf("edit %d, _id %s: reading which server this is: %w; nothing was saved", i+1, editID(edits[i]), err)
	}
	if r, ok := Document(e); ok {
		if why := r.Lacks(DocFieldsByName); why != "" {
			return fmt.Errorf("edit %d, _id %s: %s Nothing was saved.", i+1, editID(edits[i]), why)
		}
	}
	return nil
}

// applyEdit applies edit i, which must change exactly one document.
func applyEdit(ctx context.Context, c *mongo.Collection, i int, e DocEdit) error {
	fail := func(err error) error {
		return fmt.Errorf("edit %d, _id %s: %w", i+1, editID(e), err)
	}
	if e.Kind != DocInsert && e.ID.Type == 0 {
		return fmt.Errorf("edit %d has no _id", i+1)
	}
	byID := bson.D{{Key: "_id", Value: e.ID}}
	var matched int64
	switch e.Kind {
	case DocInsert:
		if _, err := c.InsertOne(ctx, e.Doc); err != nil {
			return fail(err)
		}
		return nil
	case DocSetFields:
		if len(e.Set) == 0 && len(e.Unset) == 0 {
			return fail(errors.New("it sets and removes no field"))
		}
		r, err := c.UpdateOne(ctx, byID, e.Update())
		if err != nil {
			return fail(err)
		}
		matched = r.MatchedCount
	case DocDelete:
		r, err := c.DeleteOne(ctx, byID)
		if err != nil {
			return fail(err)
		}
		matched = r.DeletedCount
	default:
		return fmt.Errorf("edit %d is of no known kind (%d)", i+1, e.Kind)
	}
	switch matched {
	case 1:
		return nil
	case 0:
		return fail(errEditMissed)
	}
	return fail(fmt.Errorf("matched %d documents, not one", matched))
}

// editID is how an edit's _id is shown: as relaxed Extended JSON, or the
// new document's _id for an insert.
func editID(e DocEdit) string {
	var id any = e.ID
	if e.ID.Type == 0 {
		v, ok := docField(e.Doc, "_id")
		if !ok {
			return "(new)"
		}
		id = v
	}
	b, err := bson.MarshalExtJSON(bson.D{{Key: "v", Value: id}}, false, false)
	if err != nil {
		return "?"
	}
	return string(b[len(`{"v":`) : len(b)-1])
}

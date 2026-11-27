package mongo

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/mongosh"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// pendingEdits are a collection tab's changes of its documents, not
// saved yet. Documents are named by their place among those read.
type pendingEdits struct {
	changed  map[int]bson.Raw // the document as edited
	deleted  map[int]bool
	inserted []bson.Raw
	// gen counts the changes, for the views made of them.
	gen int
}

func newPendingEdits() *pendingEdits {
	return &pendingEdits{changed: map[int]bson.Raw{}, deleted: map[int]bool{}}
}

func (p *pendingEdits) count() int { return len(p.changed) + len(p.deleted) + len(p.inserted) }

func (p *pendingEdits) clear() {
	clear(p.changed)
	clear(p.deleted)
	p.inserted = nil
	p.gen++
}

// summary says how many changes there are, of which kinds:
// "3 changes: 1 edited, 1 new, 1 deleted".
func (p *pendingEdits) summary() string {
	n := p.count()
	var parts []string
	for _, k := range []struct {
		n    int
		word string
	}{{len(p.changed), "edited"}, {len(p.inserted), "new"}, {len(p.deleted), "deleted"}} {
		if k.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", k.n, k.word))
		}
	}
	noun := "changes"
	if n == 1 {
		noun = "change"
	}
	return fmt.Sprintf("%d %s: %s", n, noun, strings.Join(parts, ", "))
}

// edit keeps doc as the new state of the document read at i; one edited
// back to how it was read is no change.
func (p *pendingEdits) edit(read []bson.Raw, i int, doc bson.Raw) {
	p.gen++
	if set, unset := fieldChanges(read[i], doc); len(set) == 0 && len(unset) == 0 {
		delete(p.changed, i)
		return
	}
	p.changed[i] = doc
}

// shown is the document at place i of those shown: a read one as edited,
// then the new ones.
func (p *pendingEdits) shown(read []bson.Raw, i int) bson.Raw {
	if i >= len(read) {
		return p.inserted[i-len(read)]
	}
	if d, ok := p.changed[i]; ok {
		return d
	}
	return read[i]
}

// fieldChanges are the top-level fields of now that differ from was, and
// those it lost.
func fieldChanges(was, now bson.Raw) (set bson.D, unset []string) {
	for _, e := range elementsOf(now) {
		old, err := was.LookupErr(e.Key)
		if err != nil || !sameValue(old, e.Value.(bson.RawValue)) {
			set = append(set, e)
		}
	}
	for _, e := range elementsOf(was) {
		if _, err := now.LookupErr(e.Key); err != nil {
			unset = append(unset, e.Key)
		}
	}
	return set, unset
}

// savePlan is the changes as they are saved: each an edit of the
// collection, and the statement that does the same, which the safety
// policy reviews, the confirmation lists and the audit log keeps.
type savePlan struct {
	edits []db.DocEdit
	ops   []mongosh.Op
	// places are the changes' places among those shown, for what stays
	// once some were saved: a read document's, or readLen+i for the i-th
	// new one.
	places []int
	// readLen is how many documents were read when the plan was made.
	readLen int
}

// plan is the pending changes as saved, in order: edits, then new
// documents, then deletions. A read document without an _id cannot be
// changed.
func (p *pendingEdits) plan(read []bson.Raw, coll string) (savePlan, error) {
	s := savePlan{readLen: len(read)}
	add := func(e db.DocEdit, op mongosh.Op, place int) {
		op.Collection = coll
		op.Text = statementText(coll, op)
		s.edits, s.ops, s.places = append(s.edits, e), append(s.ops, op), append(s.places, place)
	}
	idOf := func(i int) (bson.RawValue, error) {
		id, err := read[i].LookupErr("_id")
		if err != nil {
			return id, fmt.Errorf("document %d has no _id: it cannot be changed here", i+1)
		}
		return id, nil
	}
	for _, i := range slices.Sorted(maps.Keys(p.changed)) {
		if p.deleted[i] {
			continue
		}
		id, err := idOf(i)
		if err != nil {
			return s, err
		}
		set, unset := fieldChanges(read[i], p.changed[i])
		if v, ok := fieldOf(set, "_id"); ok && !sameValue(v.(bson.RawValue), id) {
			return s, fmt.Errorf("document %d: its _id cannot change; insert a copy and delete it instead", i+1)
		}
		e := db.DocEdit{Kind: db.DocSetFields, ID: id, Set: set, Unset: unset}
		add(e, mongosh.Op{Method: "updateOne", Args: []mongosh.Value{bson.D{{Key: "_id", Value: id}}, e.Update()}}, i)
	}
	for j, d := range p.inserted {
		doc := elementsOf(d)
		add(db.DocEdit{Kind: db.DocInsert, Doc: doc}, mongosh.Op{Method: "insertOne", Args: []mongosh.Value{doc}}, len(read)+j)
	}
	for _, i := range slices.Sorted(maps.Keys(p.deleted)) {
		id, err := idOf(i)
		if err != nil {
			return s, err
		}
		add(db.DocEdit{Kind: db.DocDelete, ID: id}, mongosh.Op{Method: "deleteOne", Args: []mongosh.Value{bson.D{{Key: "_id", Value: id}}}}, i)
	}
	return s, nil
}

// applySaved takes the first n changes of s, which were saved, out of
// the pending ones, and returns the documents read as they now are: those
// edited as saved, those deleted gone, those inserted after the rest. The
// changes after them stay pending, on the same documents. read may have
// grown since s was planned.
func (p *pendingEdits) applySaved(read []bson.Raw, s savePlan, n int) []bson.Raw {
	savedDelete, savedChange := map[int]bool{}, map[int]bool{}
	var savedNew []int
	for i, place := range s.places[:n] {
		switch {
		case place >= s.readLen:
			savedNew = append(savedNew, place-s.readLen)
		case s.edits[i].Kind == db.DocDelete:
			savedDelete[place] = true
		default:
			savedChange[place] = true
		}
	}
	var now []bson.Raw
	changed, deleted := map[int]bson.Raw{}, map[int]bool{}
	for i, d := range read {
		if savedDelete[i] {
			continue
		}
		at := len(now)
		switch {
		case savedChange[i]:
			d = p.changed[i]
		case p.changed[i] != nil:
			changed[at] = p.changed[i]
		}
		if p.deleted[i] {
			deleted[at] = true
		}
		now = append(now, d)
	}
	var inserted []bson.Raw
	for j, d := range p.inserted {
		if slices.Contains(savedNew, j) {
			now = append(now, d)
		} else {
			inserted = append(inserted, d)
		}
	}
	p.changed, p.deleted, p.inserted = changed, deleted, inserted
	p.gen++
	return now
}

// statementText is op written as the console would run it on coll.
func statementText(coll string, op mongosh.Op) string {
	args := make([]string, len(op.Args))
	for i, a := range op.Args {
		args[i] = shellOf(a)
	}
	return collectionRef(coll) + "." + op.Method + "(" + strings.Join(args, ", ") + ")"
}

// collectionRef is how a statement names a collection: db.name, or
// db.getCollection("name") for a name that is not one of the shell's, or
// is a database method's.
func collectionRef(coll string) string {
	if identifier.MatchString(coll) && !strings.Contains(coll, "$") && !databaseMethod[coll] {
		return "db." + coll
	}
	return "db.getCollection(" + quote(coll) + ")"
}

// databaseMethod are names that db.name would read as a method of the
// database, not a collection.
var databaseMethod = map[string]bool{
	"getCollection": true, "getSiblingDB": true, "runCommand": true, "adminCommand": true, "aggregate": true,
	"getName": true, "stats": true, "version": true, "dropDatabase": true, "createCollection": true, "createView": true,
	"getCollectionNames": true, "getCollectionInfos": true, "serverStatus": true, "currentOp": true, "killOp": true,
	"hello": true, "isMaster": true, "help": true,
}

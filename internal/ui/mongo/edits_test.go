package mongo

import (
	"slices"
	"strings"
	"testing"

	"dgopher/internal/db"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// The pending changes save as one statement each, by _id: changed
// fields set, lost ones unset, then the new documents, then deletions.
func TestSavePlan(t *testing.T) {
	read := []bson.Raw{
		rawDoc(t, bson.D{{Key: "_id", Value: int32(1)}, {Key: "total", Value: 289.0}, {Key: "status", Value: "paid"}, {Key: "coupon", Value: "W10"}}),
		rawDoc(t, bson.D{{Key: "_id", Value: int32(2)}, {Key: "status", Value: "paid"}}),
		rawDoc(t, bson.D{{Key: "_id", Value: int32(3)}}),
	}
	p := newPendingEdits()
	p.edit(read, 0, rawDoc(t, bson.D{{Key: "_id", Value: int32(1)}, {Key: "total", Value: 259.0}, {Key: "status", Value: "refunded"}}))
	p.edit(read, 1, read[1]) // back as it was: no change
	p.inserted = append(p.inserted, rawDoc(t, bson.D{{Key: "_id", Value: int32(9)}, {Key: "status", Value: "draft"}}))
	p.deleted[2] = true
	if got := p.summary(); got != "3 changes: 1 edited, 1 new, 1 deleted" {
		t.Errorf("summary %q", got)
	}
	s, err := p.plan(read, "orders")
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, op := range s.ops {
		texts = append(texts, op.Text)
	}
	want := []string{
		`db.orders.updateOne({ _id: 1 }, { $set: { total: 259.0, status: "refunded" }, $unset: { coupon: "" } })`,
		`db.orders.insertOne({ _id: 9, status: "draft" })`,
		`db.orders.deleteOne({ _id: 3 })`,
	}
	if strings.Join(texts, "\n") != strings.Join(want, "\n") {
		t.Errorf("statements\n%s\nwant\n%s", strings.Join(texts, "\n"), strings.Join(want, "\n"))
	}
	if s.edits[0].Kind != db.DocSetFields || len(s.edits[0].Set) != 2 || len(s.edits[0].Unset) != 1 || s.edits[2].Kind != db.DocDelete {
		t.Errorf("edits %+v", s.edits)
	}

	// The first two saved, on a server without transactions: the third
	// stays pending, on the same document.
	now := p.applySaved(read, s, 2)
	if len(now) != 4 || p.count() != 1 || !p.deleted[2] || now[0].Lookup("status").StringValue() != "refunded" || now[3].Lookup("_id").Int32() != 9 {
		t.Errorf("after two saved: %d documents, %d pending %+v", len(now), p.count(), p.deleted)
	}
}

// A field whose name $set would read as a path or an operator changes
// by its name in a pipeline, beside the other fields changed, so the
// fields the edit does not name stay as they are.
func TestSavePlanSetsFieldsByName(t *testing.T) {
	read := []bson.Raw{rawDoc(t, bson.D{{Key: "_id", Value: int32(1)}, {Key: "a.b", Value: int32(1)}, {Key: "$p", Value: int32(1)},
		{Key: "x.y", Value: int32(1)}, {Key: "n", Value: int32(1)}, {Key: "m", Value: int32(1)}})}
	p := newPendingEdits()
	p.edit(read, 0, rawDoc(t, bson.D{{Key: "_id", Value: int32(1)}, {Key: "a.b", Value: int32(2)}, {Key: "$p", Value: "v"}, {Key: "n", Value: int32(2)}}))
	s, err := p.plan(read, "c")
	if err != nil {
		t.Fatal(err)
	}
	want := `db.c.updateOne({ _id: 1 }, [ { $set: { n: { $literal: 2 } } }, { $unset: [ "m" ] }, ` +
		`{ $replaceWith: { $setField: { field: "a.b", input: "$$ROOT", value: { $literal: 2 } } } }, ` +
		`{ $replaceWith: { $setField: { field: { $literal: "$p" }, input: "$$ROOT", value: { $literal: "v" } } } }, ` +
		`{ $replaceWith: { $unsetField: { field: "x.y", input: "$$ROOT" } } } ])`
	if len(s.edits) != 1 || s.edits[0].Kind != db.DocSetFields || s.ops[0].Text != want {
		t.Errorf("edits %+v\nstatement\n%s\nwant\n%s", s.edits, s.ops[0].Text, want)
	}
	// Without such a name, the update stays $set and $unset.
	read = []bson.Raw{rawDoc(t, bson.D{{Key: "_id", Value: int32(1)}, {Key: "n", Value: int32(1)}})}
	p = newPendingEdits()
	p.edit(read, 0, rawDoc(t, bson.D{{Key: "_id", Value: int32(1)}, {Key: "n", Value: int32(2)}}))
	if s, err := p.plan(read, "c"); err != nil || s.ops[0].Text != `db.c.updateOne({ _id: 1 }, { $set: { n: 2 } })` {
		t.Errorf("plain: %v %s", err, s.ops[0].Text)
	}
}

// A save that stopped part way applies what it saved to the documents
// it started from, even when more were read since.
func TestApplySavedAfterMoreWereRead(t *testing.T) {
	doc := func(id int32) bson.Raw { return rawDoc(t, bson.D{{Key: "_id", Value: id}}) }
	read := []bson.Raw{doc(1), doc(2)}
	p := newPendingEdits()
	p.edit(read, 0, rawDoc(t, bson.D{{Key: "_id", Value: int32(1)}, {Key: "n", Value: int32(1)}}))
	p.inserted = append(p.inserted, doc(9))
	p.deleted[1] = true
	s, err := p.plan(read, "c")
	if err != nil {
		t.Fatal(err)
	}
	read = append(read, doc(3), doc(4))
	now := p.applySaved(read, s, 2)
	var ids []int32
	for _, d := range now {
		ids = append(ids, d.Lookup("_id").Int32())
	}
	if !slices.Equal(ids, []int32{1, 2, 3, 4, 9}) || now[0].Lookup("n").Type == 0 || p.count() != 1 || !p.deleted[1] {
		t.Errorf("documents %v, first %v, pending %d %v", ids, now[0], p.count(), p.deleted)
	}
}

func TestSavePlanRefuses(t *testing.T) {
	read := []bson.Raw{rawDoc(t, bson.D{{Key: "_id", Value: int32(1)}}), rawDoc(t, bson.D{{Key: "n", Value: int32(1)}})}
	p := newPendingEdits()
	p.edit(read, 0, rawDoc(t, bson.D{{Key: "_id", Value: int32(2)}}))
	if _, err := p.plan(read, "c"); err == nil || !strings.Contains(err.Error(), "_id cannot change") {
		t.Errorf("a changed _id: %v", err)
	}
	p = newPendingEdits()
	p.deleted[1] = true
	if _, err := p.plan(read, "c"); err == nil || !strings.Contains(err.Error(), "has no _id") {
		t.Errorf("no _id: %v", err)
	}
}

func TestCollectionRef(t *testing.T) {
	for coll, want := range map[string]string{
		"orders": "db.orders", "order-items": `db.getCollection("order-items")`, "stats": `db.getCollection("stats")`,
		"system.views": `db.getCollection("system.views")`, "$x": `db.getCollection("$x")`,
	} {
		if got := collectionRef(coll); got != want {
			t.Errorf("%s: %s, want %s", coll, got, want)
		}
	}
}

func TestCompletions(t *testing.T) {
	labels := func(text string, explicit bool) (int, string) {
		start, items := completionsAt(text, len(text), []string{"orders", "order-items", "customers"}, explicit)
		var out []string
		for _, it := range items {
			out = append(out, it.Label+"|"+it.Detail)
		}
		return start, strings.Join(out, " ")
	}
	if start, got := labels("db.or", false); start != 3 || got != "orders|collection" {
		t.Errorf("collections: %d %s", start, got)
	}
	if _, got := labels("db.customers.upd", false); got != "updateOne|(filter, update) write updateMany|(filter, update) write · risky" {
		t.Errorf("methods: %s", got)
	}
	if _, got := labels("db.customers.find", false); !strings.Contains(got, "find|(filter, projection) read") {
		t.Errorf("a read: %s", got)
	}
	if _, got := labels("db.c.find({ total: { $gt", false); got != "$gt|query $gte|query" {
		t.Errorf("operators: %s", got)
	}
	if _, got := labels("db.c.find().so", false); got != "sort|cursor" {
		t.Errorf("cursor: %s", got)
	}
	if _, got := labels("", false); got != "" {
		t.Errorf("nothing typed: %s", got)
	}
	if _, got := labels("", true); !strings.Contains(got, "db|") {
		t.Errorf("asked: %s", got)
	}
}

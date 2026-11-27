package safety

import (
	"slices"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/mongosh"
)

func mongoOp(t *testing.T, src string) mongosh.Op {
	t.Helper()
	op, err := mongosh.Parse(src)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return op
}

func TestReviewMongo(t *testing.T) {
	dev := &db.Config{Name: "docs", Env: db.Development}
	staging := &db.Config{Name: "docs", Env: db.Staging}
	prod := &db.Config{Name: "docs", Env: db.Production}
	ro := &db.Config{Name: "docs", Env: db.Development, ReadOnly: true}
	for _, c := range []struct {
		cfg     *db.Config
		src     string
		want    Verdict
		comment string
	}{
		{dev, `db.c.find({a: 1})`, Verdict{}, "a read runs"},
		{prod, `db.c.find({a: 1})`, Verdict{}, "a read runs on production"},
		{ro, `db.c.aggregate([{$match: {}}])`, Verdict{}, "a read runs on a read-only connection"},
		{ro, `show collections`, Verdict{}, "show reads"},
		{dev, `db.c.insertOne({a: 1})`, Verdict{Writes: true}, "a write runs on development"},
		{staging, `db.c.insertOne({a: 1})`, Verdict{Writes: true}, "a write runs on staging"},
		{prod, `db.c.insertOne({a: 1})`, Verdict{Writes: true, Confirm: true,
			Reasons: []string{"insertOne changes data on production"}}, "a write asks on production"},
		{ro, `db.c.insertOne({a: 1})`, Verdict{Writes: true,
			Blocked: "docs is read-only: insertOne writes. Edit the connection to allow writes."}, "read-only blocks a write"},
		{ro, `db.c.aggregate([{$out: "o"}])`, Verdict{Writes: true,
			Blocked: "docs is read-only: aggregate writes. Edit the connection to allow writes."}, "read-only blocks $out"},
		{ro, `db.runCommand({frobnicate: 1})`, Verdict{Writes: true,
			Blocked: "docs is read-only: frobnicate writes. Edit the connection to allow writes."}, "an unknown command counts as a write"},
		{ro, `db.c.drop()`, Verdict{Writes: true,
			Blocked: "docs is read-only: drop writes. Edit the connection to allow writes."}, "read-only blocks before it asks"},
		{dev, `db.c.drop()`, Verdict{Writes: true, Confirm: true,
			Reasons: []string{"drop removes the collection c and all of its documents"}}, "a risk asks everywhere"},
		{prod, `db.c.drop()`, Verdict{Writes: true, Confirm: true, TypeName: true,
			Reasons: []string{"drop removes the collection c and all of its documents"}}, "a risk asks for the name on production"},
		{prod, `db.c.deleteMany({})`, Verdict{Writes: true, Confirm: true, TypeName: true,
			Reasons: []string{"deleteMany with an empty filter deletes every document in c"}}, "an empty filter is a risk"},
		{dev, `db.c.deleteMany({a: 1})`, Verdict{Writes: true}, "a filter is no risk"},
	} {
		got := ReviewMongo(c.cfg, mongoOp(t, c.src))
		if got.Blocked != c.want.Blocked || got.Confirm != c.want.Confirm || got.TypeName != c.want.TypeName ||
			got.Writes != c.want.Writes || !slices.Equal(got.Reasons, c.want.Reasons) {
			t.Errorf("%s (%s) on %s:\n got %+v\nwant %+v", c.src, c.comment, c.cfg.Env, got, c.want)
		}
	}
}

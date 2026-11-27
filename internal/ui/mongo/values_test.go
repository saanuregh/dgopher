package mongo

import (
	"math"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func rawDoc(t *testing.T, d bson.D) bson.Raw {
	t.Helper()
	raw, err := rawOf(d)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// What the console's syntax shows of a value reads back as the same BSON,
// so that a document edited as text keeps the types of its fields.
func TestShellValuesReadBack(t *testing.T) {
	id, _ := bson.ObjectIDFromHex("6745ef3c1a2b3c4d5e6f77b2")
	dec, _ := bson.ParseDecimal128("1204.90")
	doc := bson.D{
		{Key: "_id", Value: id},
		{Key: "name", Value: `Ada "the first" <Lovelace>`},
		{Key: "int", Value: int32(7)},
		{Key: "long", Value: int64(7)},
		{Key: "whole", Value: 259.0},
		{Key: "frac", Value: 412.5},
		{Key: "big", Value: 1e300},
		{Key: "inf", Value: math.Inf(-1)},
		{Key: "dec", Value: dec},
		{Key: "yes", Value: true},
		{Key: "none", Value: nil},
		{Key: "at", Value: bson.NewDateTimeFromTime(time.Date(2026, 11, 26, 18, 2, 11, 345e6, time.UTC))},
		{Key: "ts", Value: bson.Timestamp{T: 1700000000, I: 3}},
		{Key: "uuid", Value: bson.Binary{Subtype: 4, Data: []byte("0123456789abcdef")}},
		{Key: "bin", Value: bson.Binary{Subtype: 0, Data: []byte{1, 2, 3}}},
		{Key: "re", Value: bson.Regex{Pattern: "^ab+c", Options: "i"}},
		{Key: "slash", Value: bson.Regex{Pattern: "a/b", Options: ""}},
		{Key: "min", Value: bson.MinKey{}},
		{Key: "max", Value: bson.MaxKey{}},
		{Key: "nested", Value: bson.D{{Key: "a b", Value: bson.A{int32(1), "x", bson.D{}}}}},
		{Key: "empty", Value: bson.A{}},
	}
	raw := rawDoc(t, doc)
	for _, text := range []string{shellDocument(raw), shellLine(bson.RawValue{Type: bson.TypeEmbeddedDocument, Value: raw})} {
		back, err := parseDocument(text, "it")
		if err != nil {
			t.Fatalf("%s\ndoes not read: %v", text, err)
		}
		for _, e := range elementsOf(raw) {
			v, ok := fieldOf(back, e.Key)
			if !ok {
				t.Errorf("%s: missing after reading back", e.Key)
				continue
			}
			bt, bv, _ := bson.MarshalValue(v)
			if v == nil {
				bt = bson.TypeNull
			}
			if was := e.Value.(bson.RawValue); !sameValue(was, bson.RawValue{Type: bt, Value: bv}) {
				t.Errorf("%s: %s read back as %s (%s)", e.Key, shellLine(was), shellLine(bson.RawValue{Type: bt, Value: bv}), bt)
			}
		}
	}
	if got := shellLine(raw.Lookup("whole")); got != "259.0" {
		t.Errorf("a whole double shows %q", got)
	}
	if got := shellLine(raw.Lookup("long")); got != "NumberLong(7)" {
		t.Errorf("a long shows %q", got)
	}
}

func TestShellDocumentLines(t *testing.T) {
	raw := rawDoc(t, bson.D{{Key: "a", Value: int32(1)}, {Key: "customer", Value: bson.D{{Key: "name", Value: "Ada"}}},
		{Key: "long", Value: strings.Repeat("x", 80)}})
	want := "{\n  a: 1,\n  customer: { name: \"Ada\" },\n  long: \"" + strings.Repeat("x", 80) + "\"\n}"
	if got := shellDocument(raw); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestCellValues(t *testing.T) {
	id, _ := bson.ObjectIDFromHex("6745ef3c1a2b3c4d5e6f77b2")
	raw := rawDoc(t, bson.D{{Key: "_id", Value: id}, {Key: "c", Value: bson.D{{Key: "a", Value: 1}, {Key: "b", Value: 1}, {Key: "c", Value: 1}, {Key: "d", Value: 1}}},
		{Key: "items", Value: bson.A{1, 2, 3}}, {Key: "n", Value: int32(5)}, {Key: "nil", Value: nil}})
	for key, want := range map[string]any{
		"_id": "6745ef3c1a2b3c4d5e6f77b2", "c": "{ a, b, c, … }", "items": "[ 3 ]", "n": int64(5), "nil": nil,
	} {
		if got := cellValue(raw.Lookup(key)); got != want {
			t.Errorf("%s: %#v, want %#v", key, got, want)
		}
	}
	if got := cellValue(bson.RawValue{}); got != missing {
		t.Errorf("missing: %#v", got)
	}
	if got := shortID(raw.Lookup("_id")); got != "6745ef…77b2" {
		t.Errorf("short id %q", got)
	}
}

func TestColumnsOf(t *testing.T) {
	docs := []bson.Raw{
		rawDoc(t, bson.D{{Key: "total", Value: 1.5}, {Key: "_id", Value: int32(1)}, {Key: "coupon", Value: nil}}),
		rawDoc(t, bson.D{{Key: "_id", Value: int32(2)}, {Key: "total", Value: 2.5}, {Key: "coupon", Value: "W10"}, {Key: "status", Value: "paid"}}),
		rawDoc(t, bson.D{{Key: "_id", Value: int32(3)}, {Key: "coupon", Value: int32(10)}, {Key: "total", Value: int32(3)}}),
	}
	var got []string
	for _, c := range columnsOf(docs) {
		got = append(got, c.name+":"+c.typ)
	}
	if want := "_id:int total:number coupon:mixed status:string"; strings.Join(got, " ") != want {
		t.Errorf("columns %v, want %s", got, want)
	}
	if cols := columnsOf([]bson.Raw{rawDoc(t, bson.D{{Key: "n", Value: 1}})}); len(cols) != 1 || cols[0].name != "n" {
		t.Errorf("without _id: %+v", cols)
	}
}

// A value's error names its own line and column, not the statement's it
// is read in.
func TestParseValueErrors(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{`{ a: }`, `1:6: expected a value`},
		{"{\n  a: 1,,\n}", `2:8: expected a key or "}"`},
		{`{a: 1}), db.c.drop(`, "expected"},
		{`{a: 1}).limit(2`, "expected one value"},
		{`{a: x}`, "variables are not supported"},
	} {
		_, err := parseValue(c.text)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.text, err, c.want)
		}
	}
	if v, err := parseValue("  "); v != nil || err != nil {
		t.Errorf("blank: %v %v", v, err)
	}
	if _, err := parseDocument("[1]", "The sort"); err == nil || !strings.Contains(err.Error(), "The sort is not a document") {
		t.Errorf("array as a document: %v", err)
	}
}

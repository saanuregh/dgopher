package mongosh

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func mustDecimal(t *testing.T, s string) bson.Decimal128 {
	t.Helper()
	d, err := bson.ParseDecimal128(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mustOID(t *testing.T, s string) bson.ObjectID {
	t.Helper()
	id, err := bson.ObjectIDFromHex(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func date(s string) bson.DateTime {
	tm, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return bson.NewDateTimeFromTime(tm)
}

// parseValue parses one value as the argument of a find.
func parseValue(t *testing.T, src string) Value {
	t.Helper()
	op, err := Parse("db.c.find(" + src + ")")
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	if len(op.Args) != 1 {
		t.Fatalf("%s: %d arguments", src, len(op.Args))
	}
	return op.Args[0]
}

func TestParseValues(t *testing.T) {
	uuid := bson.Binary{Subtype: 4, Data: []byte{0x12, 0x34, 0x56, 0x78, 0x12, 0x34, 0x12, 0x34, 0x12, 0x34, 0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc}}
	for _, c := range []struct {
		src  string
		want Value
	}{
		{`{}`, bson.D{}},
		{`{a: 1, "b": 'x', 'c d': "y",}`, bson.D{{Key: "a", Value: int32(1)}, {Key: "b", Value: "x"}, {Key: "c d", Value: "y"}}},
		{`{$and: [{_id: 1}], z_9: null}`, bson.D{{Key: "$and", Value: bson.A{bson.D{{Key: "_id", Value: int32(1)}}}}, {Key: "z_9", Value: nil}}},
		{`{2: true, b: false}`, bson.D{{Key: "2", Value: true}, {Key: "b", Value: false}}},
		{`[1, 2,]`, bson.A{int32(1), int32(2)}},
		{`[]`, bson.A{}},
		{`-5`, int32(-5)},
		{`3000000000`, int64(3000000000)},
		{`99999999999999999999`, float64(1e20)},
		{`1.5`, 1.5},
		{`-2.5e3`, -2500.0},
		{`1E-2`, 0.01},
		{`0x1F`, int32(31)},
		{`Infinity`, math.Inf(1)},
		{`-Infinity`, math.Inf(-1)},
		{`"tab\tquote\"nl\n\u00e9\x41"`, "tab\tquote\"nl\né" + "A"},
		{`'it\'s'`, "it's"},
		{"`plain`", "plain"},
		{`undefined`, bson.Undefined{}},
		{`ObjectId("5f1b2c3d4e5f6a7b8c9d0e1f")`, bson.ObjectID{0x5f, 0x1b, 0x2c, 0x3d, 0x4e, 0x5f, 0x6a, 0x7b, 0x8c, 0x9d, 0x0e, 0x1f}},
		{`new ObjectId('5f1b2c3d4e5f6a7b8c9d0e1f')`, bson.ObjectID{0x5f, 0x1b, 0x2c, 0x3d, 0x4e, 0x5f, 0x6a, 0x7b, 0x8c, 0x9d, 0x0e, 0x1f}},
		{`ISODate("2024-01-02T03:04:05Z")`, date("2024-01-02T03:04:05Z")},
		{`ISODate("2024-01-02T03:04:05.123+02:00")`, date("2024-01-02T01:04:05.123Z")},
		{`ISODate("2024-01-02")`, date("2024-01-02T00:00:00Z")},
		{`ISODate("2024-01-02T03:04:05")`, date("2024-01-02T03:04:05Z")},
		{`new Date("2024-01-02T03:04:05Z")`, date("2024-01-02T03:04:05Z")},
		{`new Date(1700000000000)`, bson.DateTime(1700000000000)},
		{`NumberInt(7)`, int32(7)},
		{`NumberInt("7")`, int32(7)},
		{`NumberLong("9007199254740993")`, int64(9007199254740993)},
		{`NumberLong(5)`, int64(5)},
		{`Long(5)`, int64(5)},
		{`Int32(5)`, int32(5)},
		{`Double(5)`, 5.0},
		{`NumberDecimal("1.10")`, mustDecimal(t, "1.10")},
		{`Decimal128("0.1")`, mustDecimal(t, "0.1")},
		{`UUID("12345678-1234-1234-1234-123456789abc")`, uuid},
		{`BinData(0, "AQID")`, bson.Binary{Subtype: 0, Data: []byte{1, 2, 3}}},
		{`Timestamp(1700000000, 5)`, bson.Timestamp{T: 1700000000, I: 5}},
		{`Timestamp({t: 1, i: 2})`, bson.Timestamp{T: 1, I: 2}},
		{`MinKey`, bson.MinKey{}},
		{`MaxKey()`, bson.MaxKey{}},
		{`/ab+c/i`, bson.Regex{Pattern: "ab+c", Options: "i"}},
		{`/a\/b[/]/mi`, bson.Regex{Pattern: `a\/b[/]`, Options: "im"}},
		{`{a: /x/}`, bson.D{{Key: "a", Value: bson.Regex{Pattern: "x"}}}},
		{"{a: 1, // note\n b: /* inline */ 2}", bson.D{{Key: "a", Value: int32(1)}, {Key: "b", Value: int32(2)}}},
		// relaxed Extended JSON
		{`{"$oid": "5f1b2c3d4e5f6a7b8c9d0e1f"}`, mustOID(t, "5f1b2c3d4e5f6a7b8c9d0e1f")},
		{`{"$date": "2024-01-02T03:04:05Z"}`, date("2024-01-02T03:04:05Z")},
		{`{"$date": 1700000000000}`, bson.DateTime(1700000000000)},
		{`{"$date": {"$numberLong": "1700000000000"}}`, bson.DateTime(1700000000000)},
		{`{"$numberInt": "3"}`, int32(3)},
		{`{"$numberLong": "3"}`, int64(3)},
		{`{"$numberDouble": "Infinity"}`, math.Inf(1)},
		{`{"$numberDecimal": "2.5"}`, mustDecimal(t, "2.5")},
		{`{"$binary": {"base64": "AQID", "subType": "00"}}`, bson.Binary{Subtype: 0, Data: []byte{1, 2, 3}}},
		{`{"$uuid": "12345678-1234-1234-1234-123456789abc"}`, uuid},
		{`{"$timestamp": {"t": 1, "i": 2}}`, bson.Timestamp{T: 1, I: 2}},
		{`{"$regularExpression": {"pattern": "a", "options": "si"}}`, bson.Regex{Pattern: "a", Options: "is"}},
		{`{"$minKey": 1}`, bson.MinKey{}},
		{`{"$maxKey": 1}`, bson.MaxKey{}},
		// a query operator is no Extended JSON wrapper
		{`{$regex: "a", $options: "i"}`, bson.D{{Key: "$regex", Value: "a"}, {Key: "$options", Value: "i"}}},
	} {
		got := parseValue(t, c.src)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.src, got, c.want)
		}
	}
	if v := parseValue(t, `NaN`); !math.IsNaN(v.(float64)) {
		t.Errorf("NaN = %v", v)
	}
	if v, ok := parseValue(t, `ObjectId()`).(bson.ObjectID); !ok || v.IsZero() {
		t.Errorf("ObjectId() = %v", v)
	}
	before := time.Now().Add(-time.Second)
	for _, src := range []string{`new Date()`, `ISODate()`} {
		if v, ok := parseValue(t, src).(bson.DateTime); !ok || v.Time().Before(before) {
			t.Errorf("%s = %v", src, v)
		}
	}
	// As in mongosh, Date() without new is the time as a string.
	if v, ok := parseValue(t, `Date()`).(string); !ok || !strings.Contains(v, "GMT") {
		t.Errorf("Date() = %#v", v)
	}
	if v, ok := parseValue(t, `UUID()`).(bson.Binary); !ok || v.Subtype != 4 || len(v.Data) != 16 {
		t.Errorf("UUID() = %v", v)
	}
}

func TestParseForms(t *testing.T) {
	for _, c := range []struct {
		src  string
		want Op
	}{
		{`db.users.find({a: 1})`, Op{Collection: "users", Method: "find", Args: []Value{bson.D{{Key: "a", Value: int32(1)}}}}},
		{`db.users.find().sort({a: -1}).limit(5);`, Op{Collection: "users", Method: "find",
			Chain: []Call{{Name: "sort", Args: []Value{bson.D{{Key: "a", Value: int32(-1)}}}}, {Name: "limit", Args: []Value{int32(5)}}}}},
		{"db.users\n  .find()\n  .skip(1)", Op{Collection: "users", Method: "find", Chain: []Call{{Name: "skip", Args: []Value{int32(1)}}}}},
		{`db["order items"].countDocuments()`, Op{Collection: "order items", Method: "countDocuments"}},
		{`db.getCollection("a.b").drop()`, Op{Collection: "a.b", Method: "drop"}},
		{`db.system.profile.find()`, Op{Collection: "system.profile", Method: "find"}},
		{`db.a["b"].find()`, Op{Collection: "a.b", Method: "find"}},
		{`db.stats.find()`, Op{Collection: "stats", Method: "find"}},
		{`db.getSiblingDB("shop").orders.find()`, Op{Database: "shop", Collection: "orders", Method: "find"}},
		{`db.getSiblingDB('admin').runCommand({ping: 1})`, Op{Database: "admin", Method: "runCommand", Args: []Value{bson.D{{Key: "ping", Value: int32(1)}}}}},
		{`db.getSiblingDB("x").getCollection("y").findOne()`, Op{Database: "x", Collection: "y", Method: "findOne"}},
		{`db.runCommand({listCollections: 1})`, Op{Method: "runCommand", Args: []Value{bson.D{{Key: "listCollections", Value: int32(1)}}}}},
		{`db.adminCommand("ping")`, Op{Method: "adminCommand", Args: []Value{"ping"}}},
		{`db.getCollectionNames()`, Op{Method: "getCollectionNames"}},
		{`db.createCollection("logs", {capped: true, size: 1024})`, Op{Method: "createCollection",
			Args: []Value{"logs", bson.D{{Key: "capped", Value: true}, {Key: "size", Value: int32(1024)}}}}},
		{`db.dropDatabase()`, Op{Method: "dropDatabase"}},
		{`db.stats()`, Op{Method: "stats"}},
		{`db.c.explain("executionStats").find({})`, Op{Collection: "c", Method: "explain", Args: []Value{"executionStats"},
			Chain: []Call{{Name: "find", Args: []Value{bson.D{}}}}}},
		{`use shop-2024`, Op{Database: "shop-2024", Method: "use", Args: []Value{"shop-2024"}}},
		{`use shop;`, Op{Database: "shop", Method: "use", Args: []Value{"shop"}}},
		{`show dbs`, Op{Method: "show", Args: []Value{"dbs"}}},
		{`show databases`, Op{Method: "show", Args: []Value{"dbs"}}},
		{`show collections`, Op{Method: "show", Args: []Value{"collections"}}},
		{`show tables`, Op{Method: "show", Args: []Value{"collections"}}},
		{`show users`, Op{Method: "show", Args: []Value{"users"}}},
		{`show roles`, Op{Method: "show", Args: []Value{"roles"}}},
		{`show profile`, Op{Method: "show", Args: []Value{"profile"}}},
		{"  // a comment\n db.c.find() // trailing", Op{Collection: "c", Method: "find"}},
	} {
		got, err := Parse(c.src)
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		c.want.Text = c.src
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s\n got %#v\nwant %#v", c.src, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, c := range []struct {
		src, want string
	}{
		{``, `1:1: expected a statement`},
		{`var x = 1`, `1:1: the console runs MongoDB shell statements, not JavaScript: variables are not supported`},
		{`let x = db.c.find()`, `variables are not supported`},
		{`const x = 1`, `variables are not supported`},
		{`x = 1`, `1:3: the console runs MongoDB shell statements, not JavaScript: assignments are not supported`},
		{`function f() {}`, `functions are not supported`},
		{`for (let i = 0; i < 3; i++) {}`, `loops are not supported`},
		{`while (true) {}`, `loops are not supported`},
		{`if (x) db.c.drop()`, `conditions are not supported`},
		{`db.c.find().forEach(function (d) { print(d) })`, `1:21: the console runs MongoDB shell statements, not JavaScript: functions are not supported`},
		{`db.c.find().forEach(d => print(d))`, `functions are not supported`},
		{`db.c.find().map((d) => d)`, `functions are not supported`},
		{`db.c.insertOne({a: x})`, `1:20: the console runs MongoDB shell statements, not JavaScript: x is a variable, and variables are not supported`},
		{`db.c.insertOne({a})`, `1:18: expected ":" after the key, found "}"`},
		{`db.c.insertOne({a: 1 + 2})`, `1:22: the console runs MongoDB shell statements, not JavaScript: arithmetic is not supported`},
		{`db.c.find({a: new Date().getTime()})`, `1:25: the console runs MongoDB shell statements, not JavaScript: property access on a value is not supported`},
		{`db.x = 1`, `assignments are not supported`},
		{`print("hi")`, `1:1: the console runs MongoDB shell statements, not JavaScript: print() is not a shell statement here; expected db, use or show`},
		{`rs.status()`, `expected db, use or show`},
		{`it`, `1:1: it is not supported: the results load more rows as you scroll`},
		{`exit`, `exit is not supported: close the tab instead`},
		{`quit()`, `quit is not supported: close the tab instead`},
		{`db`, `1:3: expected "." and a collection or method after db`},
		{`db.c`, `1:5: expected a method call such as .find() after the collection`},
		{`db.c.find(`, `1:11: expected a value or ")", found the end of the statement`},
		{`db.c.find({a: 1)`, `1:16: expected "," or "}", found ")"`},
		{"db.c.find(\n  {a: 1,,})", `2:9: expected a key or "}", found ","`},
		{`db.c.find([1,,2])`, `1:14: expected a value or "]", found ","`},
		{`db.c.find() db.c.find()`, `1:13: expected "." or the end of the statement, found "db"`},
		{`db.c.find().length`, `1:19: expected "(" after length`},
		{`db.c.find("abc)`, `1:11: the string does not end`},
		{`db.c.find(/* x)`, `1:11: the comment does not end`},
		{`db.c.find(ObjectId("zz"))`, `1:20: ObjectId expects 24 hexadecimal digits`},
		{`db.c.find(ISODate("yesterday"))`, `1:19: ISODate expects a date such as "2024-01-31T12:00:00Z"`},
		{`db.c.find(NumberInt("x"))`, `NumberInt expects an integer`},
		{`db.c.find(NumberInt(3000000000))`, `NumberInt expects an integer`},
		{`db.c.find(NumberDecimal("x"))`, `NumberDecimal expects a decimal number`},
		{`db.c.find(UUID("nope"))`, `UUID expects 32 hexadecimal digits`},
		{`db.c.find(BinData(0, "!!"))`, `BinData expects a subtype and a base64 string`},
		{`db.c.find(Timestamp(1))`, `Timestamp expects seconds and an increment`},
		{`db.c.find(Frob(1))`, `1:11: the console runs MongoDB shell statements, not JavaScript: Frob() is not a known shell helper`},
		{`db.c.find(/a/g)`, `1:11: the regular expression flag "g" is not supported; use i, m, s, x or u`},
		{`db.c.find({"$oid": 5})`, `1:11: $oid expects 24 hexadecimal digits`},
		{`db.c.find(12abc)`, `1:11: 12abc is not a number`},
		{"db.c.find(`a${b}`)", `template expressions are not supported`},
		{`use`, `1:4: expected a database name after use`},
		{`use a b`, `1:7: expected one database name after use`},
		{`show logs`, `1:6: expected dbs, databases, collections, tables, users, roles or profile after show`},
		{`db.getSiblingDB(1).c.find()`, `1:17: getSiblingDB expects a database name`},
		{`db.getSiblingDB("x")`, `expected "." and a collection or method after db.getSiblingDB()`},
		{`db.c.find(); db.d.find()`, `1:14: expected the end of the statement, found "db"`},
		{strings.Repeat("[", 300), `expected db, use or show`},
		{"db.c.find(" + strings.Repeat("[", 300), `nested too deeply`},
	} {
		_, err := Parse(c.src)
		if err == nil {
			t.Errorf("%s: no error, want %q", c.src, c.want)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.src, err, c.want)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`db.c.find({a: 1}).sort({b: -1}).limit(2)`,
		`db.getSiblingDB("x").runCommand({drop: "c"})`,
		`db.c.aggregate([{$match: {}}, {$out: "o"}])`,
		`use shop`, `show dbs`, `db.c.insertOne({d: ISODate("2024-01-01"), r: /x/i, n: NumberLong("1")})`,
		"db.c.find(\n// c\n{a: '\\u00e9'})\n.limit(1); db.d.drop()",
		`{"$date": {"$numberLong": "1"}}`, `db.c.find(/[/]/)`, "`${", `db.c.find(-Infinity, 0x1, 1e400)`,
		`db.c.find(Timestamp({t: 1, i: 2}), BinData(4, "AA=="), UUID())`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if op, err := Parse(s); err == nil {
			Classify(op)
		}
		last := 0
		for _, sp := range Split(s) {
			if sp.From < last || sp.To < sp.From || sp.To > len(s) {
				t.Fatalf("Split(%q) gave %+v after %d", s, sp, last)
			}
			last = sp.To
		}
	})
}

package mongosh

import (
	"reflect"
	"testing"
)

func TestSplit(t *testing.T) {
	for _, c := range []struct {
		script string
		want   []string
	}{
		{``, nil},
		{"// only a comment\n/* and\nanother */", nil},
		{"db.a.find()\ndb.b.find()", []string{"db.a.find()", "db.b.find()"}},
		{"db.a.find(); db.b.find();", []string{"db.a.find()", "db.b.find()"}},
		{"db.a.find()  ;  ;\n\n", []string{"db.a.find()"}},
		{"db.a.find(\n  {a: 1}\n)\n  .limit(1)\ndb.b.drop()", []string{"db.a.find(\n  {a: 1}\n)\n  .limit(1)", "db.b.drop()"}},
		{"db.a\n.find()", []string{"db.a\n.find()"}},
		{"db.a.find({s: 'a;b\\'\\n'})", []string{"db.a.find({s: 'a;b\\'\\n'})"}},
		{"db.a.find({s: /a;b[/;]/})\ndb.b.find()", []string{"db.a.find({s: /a;b[/;]/})", "db.b.find()"}},
		{"// x; y\n db.a.find() // z; w\ndb.b.find() /* ; */", []string{"db.a.find()", "db.b.find()"}},
		{"db.a.find(/* ;\n */ {})\ndb.b.find()", []string{"db.a.find(/* ;\n */ {})", "db.b.find()"}},
		{"use shop\nshow collections\ndb.c.find()", []string{"use shop", "show collections", "db.c.find()"}},
		{"db.a.insertOne({a: 1,\n b: 2})", []string{"db.a.insertOne({a: 1,\n b: 2})"}},
		// A statement that does not close keeps the rest of the script, for
		// Parse to name what is missing.
		{"db.a.find({\ndb.b.find()", []string{"db.a.find({\ndb.b.find()"}},
		{"db.a.find('x\ndb.b.find()", []string{"db.a.find('x\ndb.b.find()"}},
		{"db.a.find())\ndb.b.find()", []string{"db.a.find())", "db.b.find()"}},
	} {
		var got []string
		for _, sp := range Split(c.script) {
			got = append(got, c.script[sp.From:sp.To])
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Split(%q)\n got %q\nwant %q", c.script, got, c.want)
		}
	}
}

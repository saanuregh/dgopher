package redact

import (
	"strings"
	"testing"
)

func TestRedactMongo(t *testing.T) {
	for _, c := range []struct {
		text, secret, keep string
	}{
		{`db.createUser({user: "app", pwd: "s3cret", roles: ["read"]})`, "s3cret", `db.createUser({user: "app", pwd: "•••", roles: ["read"]})`},
		{`db.updateUser("app", {pwd: 'n3wpw'})`, "n3wpw", `db.updateUser("app", {pwd: '•••'})`},
		{`db.auth("app", "s3cret")`, "s3cret", `db.auth("app", "•••")`},
		{`db.getSiblingDB("admin").auth("app", "s3cret")`, "s3cret", `.auth("app", "•••")`},
		{`db.auth({user: "u", pwd: "pw1"})`, "pw1", `db.auth({user: "u", pwd: "•••"})`},
		{`db.changeUserPassword("app", "n3w pw")`, "n3w pw", `db.changeUserPassword("app", "•••")`},
		{`{"createUser": "app", "pwd": "s3cret", "mechanisms": ["SCRAM-SHA-256"]}`, "s3cret", `{"createUser": "app", "pwd": "•••", "mechanisms": ["SCRAM-SHA-256"]}`},
		{`{"updateUser": "app", "password": "a\"b"}`, `a\"b`, `"password": "•••"`},
		{`{"tlsCertificateKeyFilePassword": "kp"}`, "kp", `{"tlsCertificateKeyFilePassword": "•••"}`},
		{`{PWD: /* note */ "x1y"}`, "x1y", `PWD: /* note */ "•••"`},
		{`{pwd: 12345}`, "12345", `{pwd: •••}`},
		{`{pwd: "cut off`, "cut off", `{pwd: "•••`},
		{"db.auth('app',\n  'multi')", "multi", "db.auth('app',\n  '•••')"},
		{`Mongo("mongodb://app:s3cret@host:27017/db")`, "s3cret", `mongodb://app:•••@host`},
		// A regular expression's quote starts no string, as the parser
		// reads it.
		{`db.c.insertOne({name: /O'Brien/, password: "s3cretpw"})`, "s3cretpw", `{name: /O'Brien/, password: "•••"}`},
		{`db.c.find({a: /"/}); db.createUser({user: "u", pwd: "s3cretpw"})`, "s3cretpw", `pwd: "•••"`},
	} {
		out := Mongo(c.text)
		if strings.Contains(out, c.secret) || !strings.Contains(out, c.keep) {
			t.Errorf("%s became %s", c.text, out)
		}
	}
	for _, text := range []string{
		`db.c.find({user: "app"})`,
		`db.c.find({note: "pwd: abc"})`,
		`db.createUser({user: "u", pwd: passwordPrompt(), roles: []})`,
		`{"createUser": "u", "passwordDigestor": "server", "digestPassword": true}`,
		`db.auth("u")`,
		`db.c.find({a: "it's"}) // pwd: here`,
	} {
		if out := Mongo(text); out != text {
			t.Errorf("%s became %s", text, out)
		}
	}
}

// An error that quotes the statement's secret, as a duplicate key error
// quotes the key's value, keeps it hidden.
func TestRedactMongoError(t *testing.T) {
	stmt := `db.users.insertOne({_id: 1, password: "s3cretpw"})`
	msg := `E11000 duplicate key error collection: shop.users index: password_1 dup key: { password: "s3cretpw" }`
	got := MongoError(msg, stmt)
	if strings.Contains(got, "s3cretpw") || !strings.Contains(got, "dup key: { password: \"•••\" }") {
		t.Errorf("%s", got)
	}
}

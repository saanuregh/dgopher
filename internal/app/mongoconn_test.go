package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// The keys a SQL and a Redis connection's passwords are kept under stay
// as they were before the document family; a MongoDB connection's tell
// its deployments apart.
func TestSecretKeyByFamily(t *testing.T) {
	pg := db.Config{ID: "p", Engine: db.Postgres, Host: "h", Port: 5432, User: "u", Database: "d"}
	rd := db.Config{ID: "r", Engine: db.Redis, Host: "h", Port: 6379, Redis: db.RedisConfig{Mode: db.RedisCluster, Nodes: "h2:7000"}}
	pinValues(t, "secret key", map[string]string{
		"postgres":        "conn/p/8c10df4e897f/password",
		"postgres legacy": "conn/p/8c10df4e897f/password",
		"redis":           "conn/r/e180149bfffb/password",
		"redis legacy":    "conn/r/e180149bfffb/password",
	}, func(add func(key, got string)) {
		add("postgres", secretKey(&pg, "password"))
		add("postgres legacy", legacySecretKey(&pg, "password"))
		add("redis", secretKey(&rd, "password"))
		add("redis legacy", legacySecretKey(&rd, "password"))
	})

	base := db.Config{ID: "m", Engine: db.MongoDB, Host: "h", Port: 27017, User: "u", Database: "admin"}
	keys := map[string]db.Config{"one server": base}
	srv := base
	srv.Port, srv.Mongo.SRV = 0, true
	keys["srv"] = srv
	srvPort := srv
	srvPort.Mongo.SRV = false
	keys["no srv, no port"] = srvPort
	hosts := base
	hosts.Mongo.Hosts = []string{"h2:27017"}
	keys["hosts"] = hosts
	otherHosts := base
	otherHosts.Mongo.Hosts = []string{"h3:27017"}
	keys["other hosts"] = otherHosts
	set := hosts
	set.Mongo.ReplicaSet = "rs0"
	keys["replica set"] = set
	otherSet := hosts
	otherSet.Mongo.ReplicaSet = "rs1"
	keys["other replica set"] = otherSet
	seen := map[string]string{}
	for name, cfg := range keys {
		k := secretKey(&cfg, "password")
		if other, ok := seen[k]; ok {
			t.Errorf("%s and %s share the key %s", name, other, k)
		}
		seen[k] = name
		if legacySecretKey(&cfg, "password") != k {
			t.Errorf("%s: the legacy key differs from the key", name)
		}
	}
	if sharedFingerprint(&hosts, "d") == sharedFingerprint(&otherHosts, "d") {
		t.Error("the shared fingerprint ignores the MongoDB hosts")
	}
}

// What a MongoDB connection sends is its project's traffic: the body
// redacted whole and cut as a SQL statement is, a command sent without
// one by its name; what it runs is audited as kind mongo, redacted too,
// as the app's unless typed, and kept in the history redacted.
func TestMongoTrafficAndRunsRedacted(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	cn := a.addConn(p, db.Config{Name: "mg", Engine: db.MongoDB, Host: "h", Database: "app"})
	a.recordSent(&cn.Config, db.Sent{Mongo: "createUser", Text: `{"createUser": "u", "pwd": "hunter22", "roles": []}`, Database: "admin"})
	a.recordSent(&cn.Config, db.Sent{Mongo: "saslStart", Database: "admin"})
	a.recordSent(&cn.Config, db.Sent{Mongo: "insert", Text: `{"insert": "t", "documents": [` + strings.Repeat(`{"a": "é"},`, 2000) + `]}`})
	a.RecordRun(cn.Config, audit.KindMongo, "app", `db.changeUserPassword("u", "hunter22")`, -1, 0, nil)
	a.RecordTyped(cn.Config, audit.TypedRun{Kind: audit.KindMongo, Database: "app", Sent: `db.auth({user: "u", pwd: "hunter22"})`, Rows: -1, History: true})
	events, err := p.Audit.Read(0, true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		if strings.Contains(e.Statement, "hunter22") {
			t.Errorf("a secret reached the log: %q", e.Statement)
		}
		got = append(got, e.Kind+" "+e.Origin+" "+e.Database+": "+e.Statement[:min(len(e.Statement), 58)])
	}
	want := []string{
		"mongo typed app: db.auth({user: \"u\", pwd: \"•••\"})",
		"mongo app app: db.changeUserPassword(\"u\", \"•••\")",
		"traffic  app: {\"insert\": \"t\", \"documents\": [{\"a\": \"é\"},{\"a\": \"é\"},{\"a\"",
		"traffic  admin: saslStart",
		"traffic  admin: {\"createUser\": \"u\", \"pwd\": \"•••\", \"roles\": []}",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if long := events[2].Statement; len(long) > trafficTextMax+40 || !strings.HasSuffix(long, " bytes)") || !utf8.ValidString(long) {
		t.Errorf("a long command kept as %d bytes", len(long))
	}
	history, err := p.Local.History(10)
	if err != nil || len(history) != 1 || strings.Contains(history[0].SQL, "hunter22") {
		t.Fatalf("history %+v, %v", history, err)
	}
}

// An error that quotes a statement's secret, as a duplicate key error
// quotes the key's value, keeps it hidden in the audit log, from a run, a
// typed run and the traffic alike.
func TestMongoErrorHidesStatementSecrets(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	cn := a.addConn(p, db.Config{Name: "mg", Engine: db.MongoDB, Host: "h", Database: "app"})
	stmt := `db.users.insertOne({_id: 1, password: "s3cretpw"})`
	dup := errors.New(`E11000 duplicate key error collection: app.users index: password_1 dup key: { password: "s3cretpw" }`)
	a.RecordRun(cn.Config, audit.KindMongo, "app", stmt, 0, 0, dup)
	a.RecordTyped(cn.Config, audit.TypedRun{Kind: audit.KindMongo, Database: "app", Sent: stmt, Err: dup, History: true})
	a.recordSent(&cn.Config, db.Sent{Mongo: "insert", Text: `{"insert": "users", "documents": [{"_id": 1, "password": "s3cretpw"}]}`, Err: dup})
	events, err := p.Audit.Read(0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("%d events", len(events))
	}
	for _, e := range events {
		if strings.Contains(e.Statement+e.Error, "s3cretpw") || !strings.Contains(e.Error, `dup key: { password: "•••" }`) {
			t.Errorf("%s: %q, error %q", e.Kind, e.Statement, e.Error)
		}
	}
}

// The form of a MongoDB connection asks how it finds its servers, logs
// in and reads: a DNS SRV record takes the port and more hosts away, a
// direct connection shows for one server alone, and what is shown is
// what Save keeps.
func TestConnectionFormMongoFields(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 1400)
	a.openConnForm(nil)
	f := a.connForm
	a.syncEngineChoice(f, db.MongoDB)
	f.cfg.Name, f.cfg.Host, f.port = "mg", "h1", "27018"
	testutil.Settled(t, tt, "DNS SRV")
	testutil.Snapshot(t, tt, "connform-mongodb")
	for _, l := range []string{"More hosts", "Replica set", "Auth source", "Auth mechanism", "Read preference", "Direct connection: talk to this server alone, not to its replica set"} {
		if !tt.HasText(l) {
			t.Errorf("%q is not shown", l)
		}
	}
	for _, l := range []string{"Topology", "Key separator", "Statement timeout"} {
		if tt.HasText(l) {
			t.Errorf("%q is shown", l)
		}
	}
	f.mongo.hosts = " h2:27019,  h3 "
	f.cfg.Mongo.DirectConnection = true
	f.cfg.Mongo.ReplicaSet, f.cfg.Mongo.AuthSource = " rs0 ", " admin "
	f.mongo.auth, f.mongo.read = "SCRAM-SHA-256", "secondaryPreferred"
	tt.Frame()
	if tt.HasText("Direct connection: talk to this server alone, not to its replica set") {
		t.Error("a direct connection is offered with more hosts")
	}
	cfg := f.config()
	want := db.MongoConfig{Hosts: []string{"h2:27019", "h3"}, ReplicaSet: "rs0", AuthSource: "admin", AuthMechanism: "SCRAM-SHA-256", ReadPreference: "secondaryPreferred"}
	if cfg.Port != 27018 || !mongoEqual(cfg.Mongo, want) {
		t.Fatalf("port %d, mongo %+v", cfg.Port, cfg.Mongo)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	f.cfg.Mongo.SRV = true
	tt.Frame()
	testutil.Snapshot(t, tt, "connform-mongodb-srv")
	if tt.HasText("More hosts") || tt.HasText("27017") {
		t.Error("a DNS SRV connection is asked a port or more hosts")
	}
	cfg = f.config()
	if cfg.Port != 0 || len(cfg.Mongo.Hosts) != 0 || !cfg.Mongo.SRV {
		t.Fatalf("SRV: port %d, mongo %+v", cfg.Port, cfg.Mongo)
	}

	// Another engine keeps none of it.
	a.syncEngineChoice(f, db.Postgres)
	if cfg := f.config(); !mongoEqual(cfg.Mongo, db.MongoConfig{}) {
		t.Fatalf("postgres keeps %+v", cfg.Mongo)
	}
	a.connForm = nil
	tt.Frame()

	// An edited connection shows what it has.
	cn := addConn(a, db.Config{ID: "mg", Name: "mg", Engine: db.FerretDB, Host: "h", Mongo: db.MongoConfig{Hosts: []string{"h2"}, AuthMechanism: "PLAIN", ReadPreference: "nearest"}})
	a.openConnForm(cn)
	if f := a.connForm; f.mongo.hosts != "h2" || f.mongo.auth != "PLAIN" || f.mongo.read != "nearest" || !mongoEqual(f.config().Mongo, cn.Config.Mongo) {
		t.Fatalf("edit form: hosts %q auth %q read %q", f.mongo.hosts, f.mongo.auth, f.mongo.read)
	}
}

// Pasting a MongoDB URL fills in the form, its hosts, options and
// certificates too.
func TestConnectionFromMongoURL(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 1400)
	for url, want := range map[string]string{
		"mongodb+srv://app:pw@cluster0.example.net/shop?authSource=admin&readPreference=secondary":                 `engine=mongodb host="cluster0.example.net" port=0 user="app" pw=true db="shop" tls=verify-full ca="" mongo={SRV:true Hosts:[] ReplicaSet: AuthSource:admin AuthMechanism: ReadPreference:secondary DirectConnection:false}`,
		"mongodb://app@h1:27018,h2:27019/shop?replicaSet=rs0&authMechanism=SCRAM-SHA-1&tls=true&tlsCAFile=/ca.pem": `engine=mongodb host="h1" port=27018 user="app" pw=false db="shop" tls=verify-full ca="/ca.pem" mongo={SRV:false Hosts:[h2:27019] ReplicaSet:rs0 AuthSource: AuthMechanism:SCRAM-SHA-1 ReadPreference: DirectConnection:false}`,
		"mongodb://localhost/?directConnection=true":                                                               `engine=mongodb host="localhost" port=0 user="" pw=false db="" tls=prefer ca="" mongo={SRV:false Hosts:[] ReplicaSet: AuthSource: AuthMechanism: ReadPreference: DirectConnection:true}`,
	} {
		a.openConnForm(nil)
		f := a.connForm
		f.url = url
		a.fillFromURL(f)
		if f.urlErr != "" {
			t.Fatalf("%s: %s", url, f.urlErr)
		}
		cfg := f.config()
		if cfg.Mongo.Hosts == nil {
			cfg.Mongo.Hosts = []string{}
		}
		got := fmt.Sprintf("engine=%s host=%q port=%d user=%q pw=%v db=%q tls=%s ca=%q mongo=%+v", cfg.Engine, cfg.Host, cfg.Port, cfg.User, cfg.Password != "", cfg.Database, cfg.TLS, cfg.CAFile, cfg.Mongo)
		if got != want {
			t.Errorf("%s:\n got  %s\n want %s", url, got, want)
		}
		tt.Frame()
		a.connForm = nil
	}

	// An auth mechanism or read preference the form does not offer is
	// the URL's error, not the default.
	for url, want := range map[string]string{
		"mongodb://h/?authMechanism=MONGODB-OIDC": `the URL's authMechanism "MONGODB-OIDC" is not one of`,
		"mongodb://h/?readPreference=closest":     `the URL's readPreference "closest" is not one of`,
	} {
		a.openConnForm(nil)
		f := a.connForm
		f.url = url
		a.fillFromURL(f)
		if !strings.Contains(f.urlErr, want) || f.cfg.Host == "h" {
			t.Errorf("%s: error %q, host %q", url, f.urlErr, f.cfg.Host)
		}
		a.connForm = nil
	}
	// One kept before is shown as it is, and Save refuses it.
	cn := addConn(a, db.Config{ID: "old", Name: "old", Engine: db.MongoDB, Host: "h", Mongo: db.MongoConfig{AuthMechanism: "MONGODB-OIDC"}})
	a.openConnForm(cn)
	f := a.connForm
	if cfg := f.config(); f.mongo.auth != "MONGODB-OIDC" || cfg.Validate() == nil {
		t.Errorf("a kept mechanism shows as %q", f.mongo.auth)
	}
	a.connForm = nil
}

func mongoEqual(a, b db.MongoConfig) bool {
	return fmt.Sprintf("%+v", a) == fmt.Sprintf("%+v", b)
}

package db

import (
	"cmp"
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"dgopher/internal/netproxy"
	"dgopher/internal/sshtunnel"
	"dgopher/internal/sshtunnel/sshtest"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
)

func TestMongoURL(t *testing.T) {
	cases := []struct {
		in   string
		want Config
	}{
		{"mongodb://h", Config{Engine: MongoDB, Host: "h", Name: "h"}},
		{"mongodb://ada:p%40ss@db.example.com:27018/shop?authSource=admin&authMechanism=SCRAM-SHA-256",
			Config{Engine: MongoDB, Host: "db.example.com", Port: 27018, User: "ada", Password: "p@ss", Database: "shop", Name: "db.example.com/shop",
				Mongo: MongoConfig{AuthSource: "admin", AuthMechanism: "SCRAM-SHA-256"}}},
		{"mongodb://h1:27017,h2:27018,h3/app?replicaSet=rs0&readPreference=secondaryPreferred",
			Config{Engine: MongoDB, Host: "h1", Port: 27017, Database: "app", Name: "h1/app",
				Mongo: MongoConfig{Hosts: []string{"h2:27018", "h3"}, ReplicaSet: "rs0", ReadPreference: "secondaryPreferred"}}},
		{"mongodb://h1,h2:2/", Config{Engine: MongoDB, Host: "h1", Name: "h1", Mongo: MongoConfig{Hosts: []string{"h2:2"}}}},
		{"mongodb://[::1]:27017,h2", Config{Engine: MongoDB, Host: "::1", Port: 27017, Name: "::1", Mongo: MongoConfig{Hosts: []string{"h2"}}}},
		{"mongodb://127.0.0.1:27019/?directConnection=true", Config{Engine: MongoDB, Host: "127.0.0.1", Port: 27019, Name: "127.0.0.1",
			Mongo: MongoConfig{DirectConnection: true}}},
		{"mongodb+srv://ada:x@cluster0.example.net/shop?retryWrites=true&w=majority",
			Config{Engine: MongoDB, Host: "cluster0.example.net", User: "ada", Password: "x", Database: "shop", TLS: TLSVerifyFull,
				Name: "cluster0.example.net/shop", Mongo: MongoConfig{SRV: true}}},
		{"mongodb://h/?tls=true&tlsCAFile=/etc/ca.pem&tlsCertificateKeyFile=/etc/me.pem",
			Config{Engine: MongoDB, Host: "h", TLS: TLSVerifyFull, CAFile: "/etc/ca.pem", CertFile: "/etc/me.pem", KeyFile: "/etc/me.pem", Name: "h"}},
		{"mongodb://h/?ssl=true&tlsAllowInvalidCertificates=true", Config{Engine: MongoDB, Host: "h", TLS: TLSRequire, Name: "h"}},
		{"mongodb://u@h/?authMechanism=MONGODB-X509&tls=true", Config{Engine: MongoDB, Host: "h", User: "u", TLS: TLSVerifyFull, Name: "h",
			Mongo: MongoConfig{AuthMechanism: "MONGODB-X509"}}},
		// Matched whatever their case, as the driver reads them.
		{"mongodb://h/?readPreference=SECONDARYPREFERRED&authMechanism=scram-sha-256", Config{Engine: MongoDB, Host: "h", Name: "h",
			Mongo: MongoConfig{AuthMechanism: "SCRAM-SHA-256", ReadPreference: "secondaryPreferred"}}},
	}
	for _, c := range cases {
		got, err := ParseURL(c.in)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseURL(%q) =\n %+v, %v\nwant\n %+v", c.in, got, err, c.want)
		}
	}
	for in, want := range map[string]string{
		"mongodb+srv://h:27017/":                                 "a mongodb+srv:// URL names no port",
		"mongodb+srv://h1,h2/":                                   "a mongodb+srv:// URL names one host",
		"mongodb://h/?directConnection=maybe":                    "unknown directConnection=maybe in the URL",
		"mongodb://h/?tlsAllowInvalidHostnames=1":                "the URL's tlsAllowInvalidHostnames needs TLS",
		"mongodb://h/?tls=true&tlsDisableOCSPEndpointCheck=true": "tlsDisableOCSPEndpointCheck in the URL cannot be used",
		"mongodb://h1:x,h2/":                                     "invalid port",
		"mongodb://h/?readPreference=closest":                    `the URL's readPreference "closest" is not one of primary, primaryPreferred`,
		"mongodb://h/?authMechanism=MONGODB-OIDC":                `the URL's authMechanism "MONGODB-OIDC" is not one of SCRAM-SHA-1`,
		"mongodb://h/?authMechanism=GSSAPI":                      `the URL's authMechanism "GSSAPI" is not one of`,
	} {
		if _, err := ParseURL(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseURL(%q) = %v, want an error with %q", in, err, want)
		}
	}
}

func TestMongoConfigValidate(t *testing.T) {
	base := Config{Name: "n", Engine: MongoDB, Host: "h"}
	cases := []struct {
		name string
		edit func(c *Config)
		want string
	}{
		{"plain", func(*Config) {}, ""},
		{"replica set", func(c *Config) { c.Mongo = MongoConfig{Hosts: []string{"h2:27018", "h3"}, ReplicaSet: "rs0"} }, ""},
		{"srv", func(c *Config) { c.Mongo.SRV = true }, ""},
		{"srv with port", func(c *Config) { c.Mongo.SRV, c.Port = true, 27017 }, "a mongodb+srv:// connection names no port"},
		{"srv over ssh", func(c *Config) { c.Mongo.SRV, c.SSH = true, SSHConfig{Enabled: true, Host: "jump"} }, "an SSH tunnel cannot reach the servers a DNS SRV record names"},
		{"srv through a proxy", func(c *Config) {
			c.Mongo.SRV, c.Proxy = true, ProxyConfig{Kind: netproxy.SOCKS5, Host: "proxy", Port: 1080}
		}, "a DNS SRV record is looked up on this machine, not through the proxy"},
		{"srv with hosts", func(c *Config) { c.Mongo.SRV, c.Mongo.Hosts = true, []string{"h2"} }, "a mongodb+srv:// connection names one host"},
		{"direct with hosts", func(c *Config) { c.Mongo.DirectConnection, c.Mongo.Hosts = true, []string{"h2"} }, "a direct connection goes to one host"},
		{"bad host", func(c *Config) { c.Mongo.Hosts = []string{"h2:x"} }, `the host "h2:x" is not host or host:port`},
		{"mechanism", func(c *Config) { c.Mongo.AuthMechanism = "MONGODB-AWS" }, ""},
		{"bad mechanism", func(c *Config) { c.Mongo.AuthMechanism = "MONGODB-CR" }, `unknown authentication mechanism "MONGODB-CR"`},
		{"oidc", func(c *Config) { c.Mongo.AuthMechanism = "MONGODB-OIDC" }, `unknown authentication mechanism "MONGODB-OIDC"`},
		{"read preference", func(c *Config) { c.Mongo.ReadPreference = "nearest" }, ""},
		{"bad read preference", func(c *Config) { c.Mongo.ReadPreference = "closest" }, `unknown read preference "closest"`},
	}
	for _, c := range cases {
		for _, e := range documentEngines {
			cfg := base
			cfg.Engine = e
			c.edit(&cfg)
			err := cfg.Validate()
			if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Errorf("%s %s: %v, want %q", e, c.name, err, c.want)
			}
		}
	}
}

// Each command goes to the send hook with its body, but for those that
// carry credentials, which go by name alone.
func TestMongoSentHidesCredentials(t *testing.T) {
	raw := func(d bson.D) bson.Raw {
		b, err := bson.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := []struct {
		name string
		cmd  bson.D
		want string
	}{
		{"find", bson.D{{Key: "find", Value: "users"}, {Key: "filter", Value: bson.D{{Key: "age", Value: int32(3)}}}, {Key: "$db", Value: "shop"},
			{Key: "lsid", Value: bson.D{{Key: "id", Value: "x"}}}}, `{"find":"users","filter":{"age":3}}`},
		{"saslStart", bson.D{{Key: "saslStart", Value: 1}, {Key: "payload", Value: "secret"}}, ""},
		{"createUser", bson.D{{Key: "createUser", Value: "ada"}, {Key: "pwd", Value: "secret"}}, ""},
		{"hello", bson.D{{Key: "hello", Value: 1}, {Key: "speculativeAuthenticate", Value: bson.D{{Key: "payload", Value: "secret"}}}}, ""},
	}
	for _, c := range cases {
		name, text := mongoSent(&event.CommandStartedEvent{CommandName: c.name, Command: raw(c.cmd)})
		if name != c.name || text != c.want {
			t.Errorf("%s: (%q, %q), want (%q, %q)", c.name, name, text, c.name, c.want)
		}
	}
}

// OpenMongo takes engines of the document family only.
func TestOpenMongoRefusesOtherEngines(t *testing.T) {
	for _, e := range []Engine{Postgres, Redis, "unknown"} {
		if d, err := OpenMongo(context.Background(), Config{Name: "n", Engine: e, Host: "h"}, nil); d != nil || err == nil {
			t.Errorf("%s: opened, %v", e, err)
		}
	}
	if _, err := OpenStore(context.Background(), Config{Name: "n", Engine: MongoDB}, nil); err == nil || !strings.Contains(err.Error(), "a host is required") {
		t.Errorf("OpenStore without a host: %v", err)
	}
}

// mongoServer is a test server's configuration, from its URL in env or
// fallback; the test skips when it is not running.
func mongoServer(t *testing.T, env, fallback string) Config {
	t.Helper()
	integration(t)
	cfg, err := ParseURL(cmp.Or(os.Getenv(env), fallback))
	if err != nil {
		t.Fatalf("%s: %v", env, err)
	}
	cfg.Name = "mongo-" + strings.ToLower(env)
	cfg.Port = cmp.Or(cfg.Port, cfg.DefaultPort())
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Skipf("no MongoDB server at %s (%s)", addr, env)
	}
	c.Close()
	return cfg
}

func mongoReplicaSet(t *testing.T) Config {
	return mongoServer(t, "DGOPHER_MONGO_URL", "mongodb://127.0.0.1:27018/?directConnection=true")
}

func mongoStandalone(t *testing.T) Config {
	return mongoServer(t, "DGOPHER_MONGO_STANDALONE_URL", "mongodb://127.0.0.1:27019/?directConnection=true")
}

func ferretDB(t *testing.T) Config {
	cfg := mongoServer(t, "DGOPHER_FERRETDB_URL", "mongodb://dgopher:dgopher@127.0.0.1:27020/?directConnection=true")
	cfg.Engine = FerretDB
	return cfg
}

func openMongo(t *testing.T, cfg Config, known []string) *Doc {
	t.Helper()
	d, err := OpenMongo(context.Background(), cfg, known)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestOpenMongoReplicaSet(t *testing.T) {
	cfg := mongoReplicaSet(t)
	sends := captureSends(t, cfg.Name)
	d := openMongo(t, cfg, nil)
	if !d.topo.ReplicaSet || !d.Transactions() {
		t.Errorf("topology %+v, transactions %v: want a replica set that holds transactions", d.topo, d.Transactions())
	}
	e, v, err := d.Identify(context.Background())
	if err != nil || e != MongoDB || v == "" {
		t.Errorf("Identify = %q %q %v, want MongoDB and a version", e, v, err)
	}
	if got := d.ServerVersion(context.Background()); got != v {
		t.Errorf("ServerVersion %q, Identify's %q", got, v)
	}
	if d.TLSState() != "no TLS" {
		t.Errorf("TLSState %q", d.TLSState())
	}
	var sawPing bool
	for _, s := range sends.take() {
		if s.Mongo == "ping" && strings.HasPrefix(s.Text, `{"ping":1`) && s.Err == nil {
			sawPing = true
		}
	}
	if !sawPing {
		t.Error("the ping did not reach the send hook")
	}
}

func TestOpenMongoStandalone(t *testing.T) {
	d := openMongo(t, mongoStandalone(t), nil)
	if d.topo.ReplicaSet || d.topo.Sharded || d.Transactions() {
		t.Errorf("topology %+v, transactions %v: want a standalone server without transactions", d.topo, d.Transactions())
	}
}

func TestOpenMongoFerretDBIdentity(t *testing.T) {
	cfg := ferretDB(t)
	cfg.Engine = MongoDB // set as MongoDB, it still says what it is
	d := openMongo(t, cfg, nil)
	e, v, err := d.Identify(context.Background())
	if err != nil || e != FerretDB || !strings.HasPrefix(v, "2.") {
		t.Errorf("Identify = %q %q %v, want FerretDB 2.x", e, v, err)
	}
	cfg.Engine = FerretDB
	if d := openMongo(t, cfg, nil); d.Transactions() {
		t.Error("FerretDB holds no transactions")
	}
}

func TestOpenMongoWrongPassword(t *testing.T) {
	cfg := ferretDB(t)
	cfg.Password = "not-the-password"
	_, err := OpenMongo(context.Background(), cfg, nil)
	if err == nil {
		t.Fatal("logged in with a wrong password")
	}
	if strings.Contains(err.Error(), cfg.Password) {
		t.Errorf("the error shows the password: %v", err)
	}
	if kind := ClassifyConnectError(err); kind != ProblemAuth {
		t.Errorf("ClassifyConnectError(%v) = %v, want ProblemAuth", err, kind)
	}
}

func TestOpenMongoThroughSSH(t *testing.T) {
	cfg := mongoStandalone(t)
	s := sshtest.Start(t, "secret", nil)
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := sshtunnel.Trust(known, s.Addr, s.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	cfg.SSH = SSHConfig{Enabled: true, Host: s.Host, Port: s.Port, User: "tester", Password: "secret"}
	d := openMongo(t, cfg, []string{known})
	if d.RouteDone() == nil {
		t.Fatal("no tunnel under the connection")
	}
	if _, _, err := d.Identify(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A server that speaks no TLS is refused under require, never used in
// plain text.
func TestOpenMongoTLSRefused(t *testing.T) {
	cfg := mongoStandalone(t)
	cfg.TLS = TLSRequire
	d, err := OpenMongo(context.Background(), cfg, nil)
	if err == nil {
		d.Close()
		t.Fatal("connected without TLS under require")
	}
	if !strings.Contains(err.Error(), "TLS") {
		t.Errorf("the error does not say TLS failed: %v", err)
	}
}

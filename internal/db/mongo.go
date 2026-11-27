package db

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"dgopher/internal/sshtunnel"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/connstring"
)

// Doc is an open MongoDB connection.
type Doc struct {
	Config Config
	Client *mongo.Client
	// who is the connection the client's commands are recorded as.
	who    *sendsAs
	tunnel *sshtunnel.Tunnel
	// tlsState is whether the client uses TLS, as TLSState says it.
	tlsState string
	topo     DocTopology

	// buildInfo is the server's reply to buildInfo, read once.
	buildMu   sync.Mutex
	buildInfo bson.Raw
}

// DocTopology is how the servers of a MongoDB connection stand together.
type DocTopology struct {
	ReplicaSet bool
	Sharded    bool
}

var _ Store = (*Doc)(nil)

// OpenMongo connects to a MongoDB server, replica set or sharded cluster,
// and checks the connection.
func OpenMongo(ctx context.Context, cfg Config, knownHosts []string) (*Doc, error) {
	if !cfg.Engine.IsDocument() {
		return nil, fmt.Errorf("%s is not MongoDB", cfg.Engine.Label())
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Every server, the first as those a replica set announces, is dialed
	// the same way: through the SSH server when there is one.
	var tunnel *sshtunnel.Tunnel
	var err error
	dialTCP := func(ctx context.Context, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: 15 * time.Second}
		return d.DialContext(ctx, "tcp", addr)
	}
	switch {
	case cfg.SSH.Enabled:
		if tunnel, err = sshtunnel.Connect(ctx, sshConfig(&cfg), knownHosts); err != nil {
			return nil, redact(err, cfg)
		}
		dialTCP = func(ctx context.Context, addr string) (net.Conn, error) {
			c, err := tunnel.Dial(ctx, addr)
			if err != nil {
				return nil, err
			}
			return withDeadlines(c), nil
		}
	case cfg.Proxy.Kind != "":
		proxy := cfg.Proxy.dialer()
		dialTCP = func(ctx context.Context, addr string) (net.Conn, error) { return proxy(ctx, "tcp", addr) }
	}
	var client *mongo.Client
	fail := func(err error) (*Doc, error) {
		if client != nil {
			dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			client.Disconnect(dctx)
			cancel()
		}
		if tunnel != nil {
			tunnel.Close()
		}
		return nil, redact(err, cfg)
	}
	// The driver names each server for its certificate itself.
	tc, err := tlsConfig(&cfg, "")
	if err != nil {
		return fail(err)
	}
	// An SRV record's servers are not known before the driver looks them
	// up: under prefer, they are taken to speak TLS, as SRV's do.
	if cfg.TLS == TLSPrefer && !cfg.Mongo.SRV && !speaksTLSVia(ctx, dialTCP, cfg.mongoAddrs()[0], cfg.Host, tc) {
		tc = nil
	}
	who := newSendsAs(cfg)
	opts := options.Client()
	if cfg.Mongo.SRV {
		uri := "mongodb+srv://" + cfg.Host + "/"
		if tc == nil {
			uri += "?tls=false"
		}
		opts.ApplyURI(uri)
	} else {
		opts.SetHosts(cfg.mongoAddrs())
	}
	opts.SetAppName("dgopher").
		SetDialer(contextDialer(func(ctx context.Context, _, addr string) (net.Conn, error) { return dialTCP(ctx, addr) })).
		SetConnectTimeout(15 * time.Second).
		SetMonitor(newMongoWire(who).monitor())
	if tc != nil {
		opts.SetTLSConfig(tc)
	}
	if cred, ok := mongoCredential(&cfg); ok {
		if cfg.Mongo.SRV && cfg.Mongo.AuthSource == "" && !slices.Contains(externalMechanisms, cfg.Mongo.AuthMechanism) {
			// SetAuth replaces the credential whole, so the authSource an
			// SRV record's TXT names, which outranks the database, is read
			// here, as mongosh reads it.
			cs, err := connstring.ParseAndValidate(opts.GetURI())
			if err != nil {
				return fail(err)
			}
			if cs.AuthSourceSet {
				cred.AuthSource = cs.AuthSource
			}
		}
		opts.SetAuth(cred)
	}
	if cfg.Mongo.ReplicaSet != "" {
		opts.SetReplicaSet(cfg.Mongo.ReplicaSet)
	}
	if cfg.Mongo.DirectConnection {
		opts.SetDirect(true)
	}
	if cfg.Mongo.ReadPreference != "" {
		mode, err := readpref.ModeFromString(cfg.Mongo.ReadPreference)
		if err != nil {
			return fail(err)
		}
		rp, err := readpref.New(mode)
		if err != nil {
			return fail(err)
		}
		opts.SetReadPreference(rp)
	}
	if client, err = mongo.Connect(opts); err != nil {
		return fail(err)
	}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := client.Ping(pctx, nil); err != nil {
		if tc != nil && strings.Contains(err.Error(), "EOF") {
			err = fmt.Errorf("%w: the server closed the connection at the TLS handshake; it may not speak TLS", err)
		}
		return fail(err)
	}
	topo, err := readTopology(pctx, client)
	if err != nil {
		return fail(err)
	}
	return &Doc{Config: cfg, Client: client, who: who, tunnel: tunnel, tlsState: tlsWord(tc != nil), topo: topo}, nil
}

// contextDialer is the driver's dialer of a function.
type contextDialer func(ctx context.Context, network, addr string) (net.Conn, error)

func (f contextDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

// mongoCredential is how the connection logs in; false for no login.
func mongoCredential(cfg *Config) (options.Credential, bool) {
	m := cfg.Mongo
	if cfg.User == "" && cfg.Password == "" && m.AuthMechanism == "" {
		return options.Credential{}, false
	}
	source := m.AuthSource
	if source == "" && !slices.Contains(externalMechanisms, m.AuthMechanism) {
		source = cmp.Or(cfg.Database, "admin")
	}
	return options.Credential{
		AuthMechanism: m.AuthMechanism,
		AuthSource:    source,
		Username:      cfg.User,
		Password:      cfg.Password,
		PasswordSet:   cfg.Password != "",
	}, true
}

// readTopology reads from hello how the servers stand together; servers
// older than MongoDB 4.4.2 answer isMaster only.
func readTopology(ctx context.Context, client *mongo.Client) (DocTopology, error) {
	var hello struct {
		SetName string `bson:"setName"`
		Msg     string `bson:"msg"`
	}
	admin := client.Database("admin")
	err := admin.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello)
	var ce mongo.CommandError
	if errors.As(err, &ce) && ce.Code == 59 { // CommandNotFound
		err = admin.RunCommand(ctx, bson.D{{Key: "isMaster", Value: 1}}).Decode(&hello)
	}
	if err != nil {
		return DocTopology{}, err
	}
	return DocTopology{ReplicaSet: hello.SetName != "", Sharded: hello.Msg == "isdbgrid"}, nil
}

// Transactions reports whether the servers hold multi-document
// transactions: those of a replica set or a sharded cluster, of an engine
// that has them.
func (d *Doc) Transactions() bool {
	r, ok := Document(d.Config.Engine)
	return ok && (d.topo.ReplicaSet || d.topo.Sharded) && r.Lacks(DocTransactions) == ""
}

// Identify reads which engine of MongoDB's family the server is, and its
// own version: FerretDB names itself in buildInfo's ferretdb field.
func (d *Doc) Identify(ctx context.Context) (Engine, string, error) {
	info, err := d.readBuildInfo(ctx)
	if err != nil {
		return "", "", err
	}
	if v, ok := info.Lookup("ferretdb", "version").StringValueOK(); ok {
		return FerretDB, strings.TrimPrefix(v, "v"), nil
	}
	v, _ := info.Lookup("version").StringValueOK()
	return MongoDB, v, nil
}

// ServerVersion is the server's version, as MongoDB's: FerretDB says the
// MongoDB version it answers as.
func (d *Doc) ServerVersion(ctx context.Context) string {
	info, err := d.readBuildInfo(ctx)
	if err != nil {
		return ""
	}
	v, _ := info.Lookup("version").StringValueOK()
	return v
}

func (d *Doc) readBuildInfo(ctx context.Context) (bson.Raw, error) {
	d.buildMu.Lock()
	defer d.buildMu.Unlock()
	if d.buildInfo != nil {
		return d.buildInfo, nil
	}
	info, err := d.Client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Raw()
	if err != nil {
		return nil, err
	}
	d.buildInfo = info
	return info, nil
}

// TLSState says whether the connection uses TLS, "TLS" or "no TLS".
func (d *Doc) TLSState() string { return d.tlsState }

// RecordAs sets the connection the client's commands are recorded as,
// from now on.
func (d *Doc) RecordAs(cfg Config) { d.who.set(cfg) }

// RouteDone closes when the SSH tunnel the client connects through ends;
// nil when there is none.
func (d *Doc) RouteDone() <-chan struct{} {
	if d.tunnel == nil {
		return nil
	}
	return d.tunnel.Done()
}

// RouteErr is why the tunnel ended, as a *TunnelClosedError, nil while it
// runs or after the client closed it.
func (d *Doc) RouteErr() error {
	if d.tunnel == nil {
		return nil
	}
	if err := d.tunnel.Err(); err != nil {
		return &TunnelClosedError{Err: err}
	}
	return nil
}

// Close ends the client's sessions and closes its connections and tunnel.
func (d *Doc) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := d.Client.Disconnect(ctx)
	if d.tunnel != nil {
		d.tunnel.Close()
	}
	return err
}

// mongoWire records the commands a client sends, each once it is
// answered.
type mongoWire struct {
	who     *sendsAs
	pending sync.Map // by request ID: mongoStarted
}

type mongoStarted struct{ name, text, database string }

func newMongoWire(who *sendsAs) *mongoWire { return &mongoWire{who: who} }

func (w *mongoWire) monitor() *event.CommandMonitor {
	return &event.CommandMonitor{
		Started: func(_ context.Context, e *event.CommandStartedEvent) {
			name, text := mongoSent(e)
			w.pending.Store(e.RequestID, mongoStarted{name, text, e.DatabaseName})
		},
		Succeeded: func(_ context.Context, e *event.CommandSucceededEvent) {
			w.finish(e.CommandFinishedEvent, nil)
		},
		Failed: func(_ context.Context, e *event.CommandFailedEvent) {
			w.finish(e.CommandFinishedEvent, e.Failure)
		},
	}
}

func (w *mongoWire) finish(e event.CommandFinishedEvent, err error) {
	v, ok := w.pending.LoadAndDelete(e.RequestID)
	if !ok {
		return
	}
	s := v.(mongoStarted)
	sent(w.who.config(), Sent{Mongo: s.name, Text: s.text, Database: s.database, Took: e.Duration, Err: err})
}

// credentialCommands carry a password or a login's secrets: they are
// recorded by name alone.
var credentialCommands = []string{"authenticate", "saslStart", "saslContinue", "getnonce", "createUser", "updateUser",
	"copydbgetnonce", "copydbsaslstart", "copydb"}

// driverFields are what the driver adds to every command, which the
// command's text leaves out.
var driverFields = []string{"$db", "lsid", "$clusterTime"}

// mongoSent is a command's name, and its body as relaxed Extended JSON,
// "" for a command that carries credentials.
func mongoSent(e *event.CommandStartedEvent) (name, text string) {
	if slices.Contains(credentialCommands, e.CommandName) {
		return e.CommandName, ""
	}
	if _, err := e.Command.LookupErr("speculativeAuthenticate"); err == nil {
		return e.CommandName, ""
	}
	var doc bson.D
	if err := bson.Unmarshal(e.Command, &doc); err != nil {
		return e.CommandName, ""
	}
	doc = slices.DeleteFunc(doc, func(el bson.E) bool { return slices.Contains(driverFields, el.Key) })
	b, err := bson.MarshalExtJSON(doc, false, false)
	if err != nil {
		return e.CommandName, ""
	}
	return e.CommandName, string(b)
}

type mongodbEngine struct{}

var _ DocumentRules = mongodbEngine{}

func (mongodbEngine) Label() string                   { return "MongoDB" }
func (mongodbEngine) Base() Engine                    { return MongoDB }
func (mongodbEngine) ColorHex() string                { return "#00ed64" }
func (mongodbEngine) Mark() string                    { return "MG" }
func (mongodbEngine) DefaultPort() int                { return 27017 }
func (mongodbEngine) DefaultUser() string             { return "" }
func (mongodbEngine) Kind() EngineKind                { return KindDocument }
func (mongodbEngine) Lacks(DocFeature) string         { return "" }
func (mongodbEngine) Transactions() bool              { return false }
func (mongodbEngine) Savepoints() bool                { return false }
func (mongodbEngine) TransactionalDDL() bool          { return false }
func (mongodbEngine) DatabaseField() (string, string) { return "Database", "admin" }
func (mongodbEngine) URLSchemes() []string            { return []string{"mongodb", "mongodb+srv"} }

func (mongodbEngine) URLTLSParameters() map[string]map[string]TLSMode {
	return map[string]map[string]TLSMode{
		"tls": {"true": TLSVerifyFull, "false": TLSDisable},
		"ssl": {"true": TLSVerifyFull, "false": TLSDisable},
	}
}

func (mongodbEngine) CloudIdentities() []Identity      { return nil }
func (mongodbEngine) ClearTextPassword() bool          { return false }
func (mongodbEngine) ServerEnforcesReadOnly() bool     { return false }
func (mongodbEngine) ServerReadOnlySince() string      { return "" }
func (mongodbEngine) DatabasesNeedOwnConnection() bool { return false }
func (mongodbEngine) DefaultSchemaName() string        { return "" }
func (mongodbEngine) FileExtensions() []string         { return nil }
func (mongodbEngine) ActivityPages() []ActivityPage    { return nil }

// withDeadlines carries c, an SSH channel, which takes no deadlines, over
// a pipe, which does: the driver sets one on every read and write.
func withDeadlines(c net.Conn) net.Conn {
	near, far := net.Pipe()
	go func() {
		io.Copy(c, far)
		c.Close()
	}()
	go func() {
		io.Copy(far, c)
		far.Close()
	}()
	return near
}

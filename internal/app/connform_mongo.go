package app

import (
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The connection form's fields of a document engine: how it finds its
// servers, and how it logs in and reads, beyond Host and Port.

// mongoForm is what the form holds of a document engine beyond its
// config: its more hosts as typed, and its auth mechanism and read
// preference as labels.
type mongoForm struct{ hosts, auth, read string }

// mongoAuthLabels and mongoReadLabels are the choices of
// db.MongoAuthMechanisms and db.MongoReadPreferences, in their order,
// "" named for what it leaves to the server.
var (
	mongoAuthLabels = mongoChoiceLabels(db.MongoAuthMechanisms, "Default (the server's)")
	mongoReadLabels = mongoChoiceLabels(db.MongoReadPreferences, "Default (primary)")
)

func mongoChoiceLabels(values []string, unset string) []string {
	labels := slices.Clone(values)
	for i, v := range labels {
		if v == "" {
			labels[i] = unset
		}
	}
	return labels
}

// mongoChoice is the value of a label of labels, which values holds at
// its place; a label none has is a value kept as it was, which Save
// refuses.
func mongoChoice(values, labels []string, label string) string {
	if i := slices.Index(labels, label); i >= 0 {
		return values[i]
	}
	return label
}

// mongoLabel is the label of a value of values; a value none has shows
// as it is, rather than as the default it is not.
func mongoLabel(values, labels []string, value string) string {
	if i := slices.Index(values, value); i >= 0 {
		return labels[i]
	}
	return value
}

// loadMongo shows cfg's MongoDB settings in the form.
func (f *connForm) loadMongo(cfg *db.Config) {
	f.mongo.hosts = strings.Join(cfg.Mongo.Hosts, ", ")
	f.mongo.auth = mongoLabel(db.MongoAuthMechanisms, mongoAuthLabels, cfg.Mongo.AuthMechanism)
	f.mongo.read = mongoLabel(db.MongoReadPreferences, mongoReadLabels, cfg.Mongo.ReadPreference)
}

// mongoConfig keeps in cfg the MongoDB settings the form shows, none for
// an engine of another family; a DNS SRV record names the ports, so the
// port typed before it was chosen goes.
func (f *connForm) mongoConfig(cfg *db.Config) {
	if !cfg.Engine.IsDocument() {
		cfg.Mongo = db.MongoConfig{}
		return
	}
	m := &cfg.Mongo
	m.Hosts = nil
	if !m.SRV {
		for h := range strings.FieldsFuncSeq(f.mongo.hosts, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
			m.Hosts = append(m.Hosts, h)
		}
	} else {
		cfg.Port = 0
	}
	if m.SRV || len(m.Hosts) > 0 {
		m.DirectConnection = false // to one host only
	}
	m.ReplicaSet, m.AuthSource = strings.TrimSpace(m.ReplicaSet), strings.TrimSpace(m.AuthSource)
	m.AuthMechanism = mongoChoice(db.MongoAuthMechanisms, mongoAuthLabels, f.mongo.auth)
	m.ReadPreference = mongoChoice(db.MongoReadPreferences, mongoReadLabels, f.mongo.read)
}

// fillMongo keeps in the form what a pasted URL says of a document
// engine: its MongoDB settings and certificates, which only a MongoDB
// URL names.
func (f *connForm) fillMongo(cfg *db.Config) {
	f.cfg.Mongo = cfg.Mongo
	f.loadMongo(cfg)
	if cfg.CAFile != "" {
		f.cfg.CAFile = cfg.CAFile
	}
	if cfg.CertFile != "" {
		f.cfg.CertFile, f.cfg.KeyFile = cfg.CertFile, cfg.KeyFile
	}
}

// mongoDestination is at, where a connection goes, for a document
// engine: its DNS SRV record's host, or its host and port, then its more
// hosts.
func mongoDestination(cfg *db.Config, at string) string {
	if !cfg.Engine.IsDocument() {
		return at
	}
	if cfg.Mongo.SRV {
		at = cfg.Host + " (DNS SRV)"
	}
	for _, h := range cfg.Mongo.Hosts {
		at += ", " + h
	}
	return at
}

// mongoMoreHosts are a document engine's hosts beside its host; none for
// another engine.
func mongoMoreHosts(cfg *db.Config) []string {
	if !cfg.Engine.IsDocument() {
		return nil
	}
	return cfg.Mongo.Hosts
}

// mongoSRVField chooses, for a document engine, whether the host is a
// DNS SRV record's, as a mongodb+srv:// URL's is, and reports whether it
// is.
func mongoSRVField(c *ui.Context, f *connForm, engine db.Engine) bool {
	if !engine.IsDocument() {
		return false
	}
	ui.Field(c, "DNS SRV", func() {
		ui.Checkbox(c, &f.cfg.Mongo.SRV, "Find the servers by the host's DNS SRV record (mongodb+srv://)")
	}).Description("The record names the servers and their ports, as hosted services such as MongoDB Atlas publish them.")
	return f.cfg.Mongo.SRV
}

// mongoHostsField lists, for a document engine not found by DNS SRV, the
// servers to start from beside the host.
func mongoHostsField(c *ui.Context, f *connForm, engine db.Engine, port int) {
	if !engine.IsDocument() || f.cfg.Mongo.SRV {
		return
	}
	p := strconv.Itoa(port)
	ui.Field(c, "More hosts", func() {
		ui.TextInput(c, &f.mongo.hosts).Placeholder("10.0.0.2:" + p + ", 10.0.0.3:" + p).Font(widgets.MonoFont).FontSize(widgets.TextBody).Label("More hosts")
	}).Description("Other members of the replica set or routers of the cluster: any one that answers is enough.")
}

// mongoLoginFields hold how a document engine's connection joins its
// deployment, logs in and chooses the server it reads from.
func mongoLoginFields(c *ui.Context, f *connForm, engine db.Engine) {
	if !engine.IsDocument() {
		return
	}
	textField(c, "Replica set", &f.cfg.Mongo.ReplicaSet, "none, or rs0").Description("The replica set the servers must belong to.")
	textField(c, "Auth source", &f.cfg.Mongo.AuthSource, "the database above, else admin").Description("The database the user is defined in.")
	ui.Field(c, "Auth mechanism", func() {
		ui.Select(c, &f.mongo.auth, mongoAuthLabels).Label("Auth mechanism")
	})
	ui.Field(c, "Read preference", func() {
		ui.Select(c, &f.mongo.read, mongoReadLabels).Label("Read preference")
	}).Description("The members of a replica set reads go to; writes always go to the primary.")
	if !f.cfg.Mongo.SRV && strings.TrimSpace(f.mongo.hosts) == "" {
		ui.Field(c, "", func() {
			ui.Checkbox(c, &f.cfg.Mongo.DirectConnection, "Direct connection: talk to this server alone, not to its replica set")
		})
	}
}

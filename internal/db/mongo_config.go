package db

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// MongoConfig is how a MongoDB connection finds its servers and logs in,
// beyond Host and Port.
type MongoConfig struct {
	// SRV finds the servers by Host's DNS SRV record, as mongodb+srv://
	// does; Port is then unset.
	SRV bool `json:"srv,omitempty"`
	// Hosts are more servers to start from, host or host:port, beside
	// Host and Port: any one answering is enough.
	Hosts []string `json:"hosts,omitempty"`
	// ReplicaSet is the name of the replica set the servers must be of.
	ReplicaSet string `json:"replicaSet,omitempty"`
	// AuthSource is the database the user is defined in; "" for the
	// connection's database, else admin, or $external for a mechanism
	// that logs in outside the server.
	AuthSource string `json:"authSource,omitempty"`
	// AuthMechanism is one of MongoAuthMechanisms; "" lets the server and
	// the driver agree on one.
	AuthMechanism string `json:"authMechanism,omitempty"`
	// ReadPreference is one of MongoReadPreferences; "" for primary.
	ReadPreference string `json:"readPreference,omitempty"`
	// DirectConnection talks to the one server of Host and Port alone,
	// not to the replica set it belongs to.
	DirectConnection bool `json:"directConnection,omitempty"`
}

// MongoAuthMechanisms are the authentication mechanisms a MongoDB
// connection may name, "" for the one the server offers.
// MONGODB-OIDC is not offered: the driver logs in with it only given an
// ENVIRONMENT property or a callback, which the app does not set.
var MongoAuthMechanisms = []string{"", "SCRAM-SHA-1", "SCRAM-SHA-256", "MONGODB-X509", "MONGODB-AWS", "PLAIN"}

// MongoReadPreferences are the servers of a replica set reads may go to,
// "" for the primary.
var MongoReadPreferences = []string{"", "primary", "primaryPreferred", "secondary", "secondaryPreferred", "nearest"}

// externalMechanisms log in outside the server, whose users are then in
// the $external database the driver chooses.
var externalMechanisms = []string{"MONGODB-X509", "MONGODB-AWS", "PLAIN"}

// mongoAddrs are the host:port addresses a MongoDB connection starts
// from: Host and Port, then Hosts.
func (c *Config) mongoAddrs() []string {
	addrs := []string{net.JoinHostPort(c.Host, strconv.Itoa(c.port()))}
	for _, h := range c.Mongo.Hosts {
		if _, _, err := net.SplitHostPort(h); err != nil {
			h = net.JoinHostPort(h, strconv.Itoa(c.port()))
		}
		addrs = append(addrs, h)
	}
	return addrs
}

func (mongodbEngine) checkConfig(c *Config) []string {
	var errs []string
	m := c.Mongo
	if m.SRV {
		if c.Port != 0 {
			errs = append(errs, "a mongodb+srv:// connection names no port: its DNS SRV record does")
		}
		if c.SSH.Enabled {
			errs = append(errs, "an SSH tunnel cannot reach the servers a DNS SRV record names: list the hosts instead")
		}
		if c.Proxy.Kind != "" {
			errs = append(errs, "a DNS SRV record is looked up on this machine, not through the proxy: list the hosts instead")
		}
		if len(m.Hosts) > 0 {
			errs = append(errs, "a mongodb+srv:// connection names one host, whose DNS SRV record lists the others")
		}
	}
	if m.DirectConnection && (m.SRV || len(m.Hosts) > 0) {
		errs = append(errs, "a direct connection goes to one host")
	}
	for _, h := range m.Hosts {
		if !mongoHost(h) {
			errs = append(errs, fmt.Sprintf("the host %q is not host or host:port", h))
		}
	}
	if !slices.Contains(MongoAuthMechanisms, m.AuthMechanism) {
		errs = append(errs, fmt.Sprintf("unknown authentication mechanism %q", m.AuthMechanism))
	}
	if !slices.Contains(MongoReadPreferences, m.ReadPreference) {
		errs = append(errs, fmt.Sprintf("unknown read preference %q", m.ReadPreference))
	}
	return errs
}

// mongoHost reports whether s is a host, or host:port, as a MongoDB URL
// lists them.
func mongoHost(s string) bool {
	if host, port, err := net.SplitHostPort(s); err == nil {
		p, err := strconv.Atoi(port)
		return err == nil && p >= 1 && p <= 65535 && host != ""
	}
	return s != "" && (!strings.ContainsAny(s, ":[]/@?#, ") || net.ParseIP(s) != nil)
}

// splitMongoHosts takes the hosts after the first out of a MongoDB URL,
// whose host list net/url cannot read.
func splitMongoHosts(s string) (string, []string, error) {
	scheme, rest, _ := strings.Cut(s, "://")
	end := strings.IndexAny(rest, "/?")
	if end < 0 {
		end = len(rest)
	}
	authority := rest[:end]
	at := strings.LastIndex(authority, "@") + 1
	hosts := strings.Split(authority[at:], ",")
	if len(hosts) == 1 {
		return s, nil, nil
	}
	if strings.EqualFold(scheme, "mongodb+srv") {
		return "", nil, fmt.Errorf("a mongodb+srv:// URL names one host, whose DNS SRV record lists the others")
	}
	for _, h := range hosts[1:] {
		if !mongoHost(h) {
			return "", nil, fmt.Errorf("the URL's host %q is not host or host:port", h)
		}
	}
	return scheme + "://" + authority[:at] + hosts[0] + rest[end:], hosts[1:], nil
}

// readURL reads what a MongoDB URL says beyond the others; the TLS
// options that loosen verification ask for TLS that the URL turned on.
func (mongodbEngine) readURL(cfg *Config, u *url.URL) error {
	q := u.Query()
	if strings.EqualFold(u.Scheme, "mongodb+srv") {
		cfg.Mongo.SRV = true
		if u.Port() != "" {
			return fmt.Errorf("a mongodb+srv:// URL names no port: its DNS SRV record does")
		}
		// The servers of an SRV record speak TLS unless the URL says not.
		if !q.Has("tls") && !q.Has("ssl") {
			cfg.TLS = TLSVerifyFull
		}
	}
	for key, values := range q {
		v := values[len(values)-1]
		switch key {
		case "replicaSet":
			cfg.Mongo.ReplicaSet = v
		case "authSource":
			cfg.Mongo.AuthSource = v
		case "authMechanism", "readPreference":
			// Matched whatever their case, as the driver reads them; one
			// the form does not offer is refused, not left out.
			known, field := MongoAuthMechanisms, &cfg.Mongo.AuthMechanism
			if key == "readPreference" {
				known, field = MongoReadPreferences, &cfg.Mongo.ReadPreference
			}
			i := slices.IndexFunc(known[1:], func(k string) bool { return strings.EqualFold(k, v) })
			if i < 0 {
				return fmt.Errorf("the URL's %s %q is not one of %s", key, v, strings.Join(known[1:], ", "))
			}
			*field = known[1+i]
		case "directConnection":
			b, err := urlBool(key, v)
			if err != nil {
				return err
			}
			cfg.Mongo.DirectConnection = b
		}
	}
	for key, values := range q {
		v := values[len(values)-1]
		switch key {
		case "tls", "ssl":
		case "tlsCAFile":
			cfg.CAFile = v
		case "tlsCertificateKeyFile":
			cfg.CertFile, cfg.KeyFile = v, v
		case "tlsInsecure", "tlsAllowInvalidCertificates", "tlsAllowInvalidHostnames":
			b, err := urlBool(key, v)
			if err != nil {
				return err
			}
			if cfg.TLS == "" || cfg.TLS == TLSDisable {
				return fmt.Errorf("the URL's %s needs TLS", key)
			}
			if b {
				cfg.TLS = TLSRequire
			}
		default:
			if strings.HasPrefix(key, "tls") || strings.HasPrefix(key, "ssl") {
				return fmt.Errorf("%s in the URL cannot be used: set TLS in the connection's settings", key)
			}
		}
	}
	return nil
}

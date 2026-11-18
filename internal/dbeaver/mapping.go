package dbeaver

import (
	"cmp"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/netproxy"
	"dgopher/internal/redact"
)

// target is what a DBeaver driver maps to.
type target struct {
	engine   db.Engine
	protocol db.ClickHouseProtocol
	skip     string
}

// byDriver maps "provider/driver" ids, checked before byProvider.
var byDriver = map[string]target{
	"postgresql/postgres-timescale-jdbc":  {engine: db.TimescaleDB},
	"postgresql/postgres-yugabytedb-jdbc": {engine: db.YugabyteDB},
	"postgresql/postgres-cockroach-jdbc":  {engine: db.CockroachDB},
	"mysql/mariaDB":                       {engine: db.MariaDB},
	"generic/yandex_clickhouse":           {engine: db.ClickHouse, protocol: db.ClickHouseHTTP},
	"generic/oracle":                      {engine: db.Oracle},
	"generic/sqlite_xerial":               {engine: db.SQLite},
	"duckdb/duckdb_jdbc":                  {engine: db.DuckDB},
	"duckdb/motherduck_jdbc":              {skip: "MotherDuck is not supported"},
}

// byProvider maps provider ids. DBeaver's ClickHouse drivers speak HTTP.
var byProvider = map[string]target{
	"postgresql": {engine: db.Postgres},
	"mysql":      {engine: db.MySQL},
	"tidb":       {engine: db.TiDB},
	"clickhouse": {engine: db.ClickHouse, protocol: db.ClickHouseHTTP},
	"sqlserver":  {engine: db.SQLServer},
	"mssql":      {engine: db.SQLServer},
	"oracle":     {engine: db.Oracle},
	"sqlite":     {engine: db.SQLite},
}

func targetOf(provider, driver string) target {
	if t, ok := byDriver[provider+"/"+driver]; ok {
		return t
	}
	if t, ok := byProvider[provider]; ok {
		return t
	}
	return target{skip: fmt.Sprintf("DBeaver's %s/%s driver is not supported", provider, driver)}
}

// builtinTypes are DBeaver's own connection types, as it ships them.
var builtinTypes = map[string]connectionType{
	"dev":  {AutoCommit: new(true)},
	"test": {AutoCommit: new(true), ConfirmDataChange: true},
	"prod": {AutoCommit: new(false), ConfirmExecute: true, ConfirmDataChange: true},
}

var builtinEnvironments = map[string]db.Environment{
	"dev":  db.Development,
	"test": db.Staging,
	"prod": db.Production,
}

// environment is the connection type's environment, and the commit mode
// when DBeaver's autocommit differs from DGopher's default for it. chosen
// is false for DBeaver's default type, dev, which every new connection
// has unless the user changed it.
func environment(conf configuration, types map[string]connectionType) (env db.Environment, commit db.CommitMode, chosen bool) {
	id := conf.Type
	t, ok := types[id]
	if !ok {
		if t, ok = builtinTypes[id]; !ok {
			id, t = "dev", builtinTypes["dev"]
		}
	}
	chosen = id != "dev"
	env, ok = builtinEnvironments[id]
	if !ok {
		switch {
		case t.ConfirmExecute:
			env = db.Production
		case t.ConfirmDataChange:
			env = db.Staging
		default:
			env = db.Development
		}
	}
	auto := true
	if t.AutoCommit != nil {
		auto = *t.AutoCommit
	}
	if conf.Bootstrap.Autocommit != nil {
		auto = *conf.Bootstrap.Autocommit
	}
	switch defaultAuto := env != db.Production; {
	case auto == defaultAuto:
		return env, db.CommitDefault, chosen
	case auto:
		return env, db.CommitAuto, chosen
	}
	return env, db.CommitManual, chosen
}

// mapConnection is the entry of one DBeaver connection.
func mapConnection(id string, c connection, types map[string]connectionType, creds credentials) Entry {
	e := Entry{ID: id, Folder: c.Folder}
	cfg := &e.Config
	conf := c.Configuration
	cfg.Name = cmp.Or(c.Name, id)
	cfg.ReadOnly = c.ReadOnly
	cfg.Env, cfg.Commit, e.EnvChosen = environment(conf, types)
	t := targetOf(c.Provider, c.Driver)
	if t.skip != "" {
		e.Skip = t.skip
		return e
	}
	cfg.Engine = t.engine

	oracleKind := text(conf.ProviderProperties["@dbeaver-connection-type@"])
	switch {
	case oracleKind == "TNS":
		cfg.Database = conf.Database
		e.Skip = "TNS aliases are not supported"
		return e
	case cfg.Engine.IsFile():
		cfg.Database = conf.Database
		if conf.ConfigurationType == "URL" {
			// jdbc:sqlite:<file>, jdbc:duckdb:<file>
			_, rest, _ := strings.Cut(strings.TrimSpace(conf.URL), ":")
			_, cfg.Database, _ = strings.Cut(rest, ":")
		}
	case conf.ConfigurationType == "URL" || oracleKind == "CUSTOM":
		u := strings.TrimSpace(conf.URL)
		if len(u) >= 5 && strings.EqualFold(u[:5], "jdbc:") {
			u = u[5:]
		}
		parsed, err := db.ParseURL(u)
		if err != nil {
			// The URL may hold a password, which the error quotes.
			e.Skip = "its URL could not be read: " + redact.Secrets(err.Error())
			return e
		}
		parsed.Name, parsed.Engine, parsed.Env, parsed.Commit, parsed.ReadOnly = cfg.Name, cfg.Engine, cfg.Env, cfg.Commit, cfg.ReadOnly
		*cfg = parsed
	default:
		cfg.Host, cfg.Port, cfg.Database = conf.Host, port(conf.Port), conf.Database
		cfg.Oracle.SID = text(conf.ProviderProperties["@dbeaver-sid-service@"]) == "SID"
	}
	if cfg.ClickHouseProtocol == "" {
		cfg.ClickHouseProtocol = t.protocol
	}
	cfg.User = cmp.Or(creds.get(id, "#connection", "user"), conf.User, cfg.User)
	cfg.Password = cmp.Or(creds.get(id, "#connection", "password"), conf.Password, cfg.Password)
	if conf.AuthModel != "" && conf.AuthModel != "native" {
		e.Notes = append(e.Notes, fmt.Sprintf("DBeaver's %s login was not imported; set the login in the connection form", conf.AuthModel))
	}
	mapTLS(cfg, conf.Handlers)
	e.Notes = append(e.Notes, mapSSH(cfg, conf.Handlers["ssh_tunnel"], creds[id]["network/ssh_tunnel"])...)
	mapProxy(cfg, conf.Handlers["socks_proxy"], creds[id]["network/socks_proxy"])
	if cfg.Port == 0 && !cfg.Engine.IsFile() {
		// After TLS: ClickHouse's follows its protocol and TLS.
		cfg.Port = cfg.DefaultPort()
	}
	if err := cfg.Validate(); err != nil {
		e.Skip = redact.Secrets(err.Error())
	}
	return e
}

// tlsHandlers map each DBeaver TLS handler, by id, to the mode it asks for.
var tlsHandlers = map[string]func(h handler) db.TLSMode{
	"postgre_ssl": func(h handler) db.TLSMode {
		switch h.prop("sslMode") {
		case "disable":
			return db.TLSDisable
		case "allow", "prefer":
			return db.TLSPrefer
		case "verify-ca", "verify-full":
			// DGopher has no verify-ca; verify-full is stricter.
			return db.TLSVerifyFull
		}
		return db.TLSRequire
	},
	"mysql_ssl": func(h handler) db.TLSMode {
		switch {
		case h.prop("ssl.verify.server") == "true":
			return db.TLSVerifyFull
		case h.prop("ssl.require") == "false":
			// DBeaver then falls back to plain text.
			return db.TLSPrefer
		}
		return db.TLSRequire
	},
	"mssql_ssl":      func(handler) db.TLSMode { return db.TLSVerifyFull },
	"clickhouse-ssl": func(handler) db.TLSMode { return db.TLSVerifyFull },
}

func mapTLS(cfg *db.Config, handlers map[string]handler) {
	for _, id := range slices.Sorted(maps.Keys(tlsHandlers)) {
		h, ok := handlers[id]
		if !ok || !h.Enabled {
			continue
		}
		cfg.TLS = tlsHandlers[id](h)
		cfg.CAFile = cmp.Or(h.prop("ssl.ca.cert"), h.prop("rootCert"))
		cfg.CertFile = cmp.Or(h.prop("ssl.client.cert"), h.prop("clientCert"))
		cfg.KeyFile = cmp.Or(h.prop("ssl.client.key"), h.prop("clientKey"))
	}
}

// mapSSH sets the SSH tunnel and returns the notes on what it could not.
func mapSSH(cfg *db.Config, h handler, secrets map[string]string) []string {
	if !h.Enabled {
		return nil
	}
	var notes []string
	ssh := &cfg.SSH
	ssh.Enabled = true
	ssh.Host = h.prop("host")
	ssh.Port = cmp.Or(port(h.Properties["port"]), 22)
	ssh.User = cmp.Or(secrets["user"], h.User, h.prop("user"))
	password := cmp.Or(secrets["password"], h.Password, h.prop("password"))
	switch h.prop("authType") {
	case "PUBLIC_KEY":
		ssh.KeyPath = h.prop("keyPath")
		ssh.KeyPassphrase = password
	case "AGENT":
		ssh.UseAgent = true
	default:
		ssh.Password = password
	}
	if secrets["keyValue"] != "" || h.prop("keyValue") != "" {
		notes = append(notes, "the SSH key pasted into DBeaver was not imported; choose its file")
	}
	var jumps []string
	jumpPassword := false
	for i := 0; ; i++ {
		key := "jumpServer" + strconv.Itoa(i) + "."
		host := h.prop(key + "host")
		if host == "" {
			break
		}
		if h.prop(key+"enabled") == "false" {
			continue
		}
		jump := host
		p := h.prop(key + "port")
		if p == "" && strings.Contains(host, ":") {
			p = "22" // an IPv6 address is bracketed only with a port
		}
		if p != "" {
			jump = net.JoinHostPort(host, p)
		}
		if name := h.prop(key + "name"); name != "" {
			jump = name + "@" + jump
		}
		jumps = append(jumps, jump)
		if secrets[key+"password"] != "" || h.prop(key+"authType") == "PASSWORD" {
			jumpPassword = true
		}
	}
	ssh.Jump = strings.Join(jumps, ",")
	if jumpPassword {
		notes = append(notes, "jump hosts log in with a key or the agent only")
	}
	return notes
}

func mapProxy(cfg *db.Config, h handler, secrets map[string]string) {
	if !h.Enabled {
		return
	}
	cfg.Proxy = db.ProxyConfig{
		Kind:     netproxy.SOCKS5,
		Host:     h.prop("socks-host"),
		Port:     cmp.Or(port(h.Properties["socks-port"]), 1080),
		User:     cmp.Or(secrets["user"], h.User),
		Password: cmp.Or(secrets["password"], h.Password),
	}
}

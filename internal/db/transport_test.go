package db

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mysqlPacket writes one MySQL protocol packet.
func mysqlPacket(c net.Conn, seq byte, payload []byte) error {
	head := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
	_, err := c.Write(append(head, payload...))
	return err
}

// readMySQLPacket reads one MySQL protocol packet's payload.
func readMySQLPacket(c net.Conn) ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return nil, err
	}
	payload := make([]byte, int(head[0])|int(head[1])<<8|int(head[2])<<16)
	_, err := io.ReadFull(c, payload)
	return payload, err
}

// mysqlGreeting is a protocol 10 handshake offering TLS or not.
func mysqlGreeting(withTLS bool) []byte {
	const (
		clientMySQL        = 1
		clientLongFlag     = 4
		clientConnectWithD = 8
		clientProtocol41   = 512
		clientSSL          = 2048
		clientTransactions = 8192
		clientSecureConn   = 32768
		clientPluginAuth   = 1 << 19
	)
	caps := uint32(clientMySQL | clientLongFlag | clientConnectWithD | clientProtocol41 | clientTransactions | clientSecureConn | clientPluginAuth)
	if withTLS {
		caps |= clientSSL
	}
	g := []byte{10}
	g = append(g, "8.4.0-fake\x00"...)
	g = binary.LittleEndian.AppendUint32(g, 1)
	g = append(g, "abcdefgh"...)
	g = append(g, 0)
	g = binary.LittleEndian.AppendUint16(g, uint16(caps))
	g = append(g, 255)                                        // character set
	g = binary.LittleEndian.AppendUint16(g, 2)                // status: autocommit
	g = binary.LittleEndian.AppendUint16(g, uint16(caps>>16)) // upper capabilities
	g = append(g, 21)                                         // auth data length
	g = append(g, make([]byte, 10)...)                        // reserved
	g = append(g, "ijklmnopqrst\x00"...)                      // auth data, part 2
	g = append(g, "mysql_native_password\x00"...)
	return g
}

// After one connection of a pool falls back to plain text, the next one
// still asks a server offering TLS for it: the fallback is the
// connection's own, not the pool's.
func TestMySQLEachConnectionNegotiatesTLS(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	secondAsked := make(chan bool, 1)
	go func() {
		for n := 0; ; n++ {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(n int, c net.Conn) {
				defer c.Close()
				if mysqlPacket(c, 0, mysqlGreeting(n > 0)) != nil {
					return
				}
				resp, err := readMySQLPacket(c)
				if err != nil || len(resp) < 4 {
					return
				}
				asksTLS := binary.LittleEndian.Uint32(resp)&2048 != 0
				if n > 0 {
					secondAsked <- asksTLS
					return
				}
				ok := []byte{0, 0, 0, 2, 0, 0, 0}
				if mysqlPacket(c, 2, ok) != nil {
					return
				}
				for {
					if _, err := readMySQLPacket(c); err != nil {
						return
					}
					if mysqlPacket(c, 1, ok) != nil {
						return
					}
				}
			}(n, c)
		}
	}()
	port := l.Addr().(*net.TCPAddr).Port
	cfg := Config{Name: "m", Engine: MySQL, Host: "127.0.0.1", Port: port, User: "root", Password: "pw", TLS: TLSPrefer}
	ep := endpoint{host: "127.0.0.1", port: port, serverName: "127.0.0.1"}
	tc, _ := tlsConfig(&cfg, ep.serverName)
	sqldb, _, err := openMySQL(cfg, ep, tc)
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := sqldb.Conn(ctx)
	if err != nil {
		t.Fatalf("the plain-text connection: %v", err)
	}
	defer first.Close()
	second, err := sqldb.Conn(ctx)
	if err == nil {
		second.Close()
	}
	select {
	case asked := <-secondAsked:
		if !asked {
			t.Fatal("the second connection did not ask for TLS: the first one's fallback turned it off for the pool")
		}
	case <-ctx.Done():
		t.Fatal("the second connection never reached the server")
	}
}

// Under prefer, a server that declines TLS and asks for the password in
// clear text gets nothing: an attacker in the middle could do just that.
func TestPreferRefusesCleartextPassword(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var gotPassword atomic.Bool
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					var size [4]byte
					if _, err := io.ReadFull(c, size[:]); err != nil {
						return
					}
					body := make([]byte, binary.BigEndian.Uint32(size[:])-4)
					if _, err := io.ReadFull(c, body); err != nil {
						return
					}
					switch code := binary.BigEndian.Uint32(body); {
					case len(body) == 4 && (code == 80877103 || code == 80877104): // SSLRequest, GSSENCRequest
						c.Write([]byte{'N'})
					default: // the startup message
						c.Write([]byte{'R', 0, 0, 0, 8, 0, 0, 0, 3}) // AuthenticationCleartextPassword
						var kind [1]byte
						if _, err := io.ReadFull(c, kind[:]); err == nil && kind[0] == 'p' {
							gotPassword.Store(true)
						}
						return
					}
				}
			}(c)
		}
	}()
	port := l.Addr().(*net.TCPAddr).Port
	cfg := Config{Name: "p", Engine: Postgres, Host: "127.0.0.1", Port: port, User: "alice", Password: "s3cret", Database: "x", TLS: TLSPrefer}
	ep := endpoint{host: "127.0.0.1", port: port, serverName: "127.0.0.1"}
	tc, _ := tlsConfig(&cfg, ep.serverName)
	sqldb, err := openPostgres(cfg, ep, tc)
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = sqldb.PingContext(ctx)
	if gotPassword.Load() {
		t.Fatalf("the password went in clear text to a server that declined TLS (ping: %v)", err)
	}
	if err == nil {
		t.Fatal("the connection succeeded")
	}
}

// An empty password stays empty: ~/.pgpass, or PGPASSFILE's, never
// fills it, as a wildcard entry would send it to any server.
func TestPgpassIgnored(t *testing.T) {
	home := t.TempDir()
	pass := filepath.Join(home, ".pgpass")
	if err := os.WriteFile(pass, []byte("*:*:*:*:leaked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PGPASSFILE", pass)
	cfg := Config{Name: "p", Engine: Postgres, Host: "db.example.com", User: "alice", Database: "x"}
	pc, err := postgresConfig(cfg, endpoint{host: cfg.Host, port: cfg.port(), serverName: cfg.Host}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pc.Password != "" {
		t.Fatalf("the password came from .pgpass: %q", pc.Password)
	}
}

// The PG* environment variables libpq reads change nothing of a
// connection: where it goes, as whom, how it uses TLS and what it asks
// the server for are the connection's own.
func TestPGEnvIgnored(t *testing.T) {
	dir := t.TempDir()
	services := filepath.Join(dir, "pg_service.conf")
	os.WriteFile(services, []byte("[evil]\nhost=evil.example.com\nport=1\nuser=evil\noptions=-c search_path=evil\nkrbsrvname=evil\nsearch_path=evil\n"), 0o600)
	for k, v := range map[string]string{
		"PGHOST": "evil.example.com", "PGPORT": "1", "PGDATABASE": "evil", "PGUSER": "evil", "PGPASSWORD": "evil",
		"PGPASSFILE": filepath.Join(dir, "none"), "PGAPPNAME": "evil", "PGCONNECT_TIMEOUT": "1",
		"PGSSLMODE": "verify-full", "PGSSLKEY": filepath.Join(dir, "k"), "PGSSLCERT": filepath.Join(dir, "c"),
		"PGSSLSNI": "0", "PGSSLROOTCERT": "system", "PGSSLPASSWORD": "evil", "PGSSLNEGOTIATION": "direct",
		"PGTARGETSESSIONATTRS": "read-write", "PGSERVICE": "evil", "PGSERVICEFILE": services, "PGTZ": "Antarctica/Troll",
		"PGOPTIONS": "-c search_path=evil", "PGMINPROTOCOLVERSION": "3.2", "PGMAXPROTOCOLVERSION": "3.2",
		"PGCHANNELBINDING": "require", "PGREQUIREAUTH": "gss",
	} {
		t.Setenv(k, v)
	}
	cfg := Config{Name: "p", Engine: Postgres, Host: "db.example.com", Port: 5433, User: "alice", Database: "billing", TLS: TLSPrefer}
	ep := endpoint{host: cfg.Host, port: cfg.port(), serverName: cfg.Host}
	tc, err := tlsConfig(&cfg, ep.serverName)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := postgresConfig(cfg, ep, tc)
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	check := func(what string, ok bool) {
		if !ok {
			problems = append(problems, what)
		}
	}
	check("host "+pc.Host, pc.Host == "db.example.com" && pc.Port == 5433)
	check("user "+pc.User, pc.User == "alice")
	check("database "+pc.Database, pc.Database == "billing")
	check("password "+pc.Password, pc.Password == "")
	check("connect timeout "+pc.ConnectTimeout.String(), pc.ConnectTimeout == 15*time.Second)
	check("TLS", pc.TLSConfig == tc && len(pc.Fallbacks) == 1 && pc.Fallbacks[0].TLSConfig == nil)
	check("TLS negotiation "+pc.SSLNegotiation, pc.SSLNegotiation == "" || pc.SSLNegotiation == "postgres")
	check("target session attributes", pc.ValidateConnect == nil)
	check("protocol "+pc.MinProtocolVersion+"-"+pc.MaxProtocolVersion, pc.MinProtocolVersion == "3.0" && pc.MaxProtocolVersion == "3.0")
	check("channel binding "+pc.ChannelBinding, pc.ChannelBinding == "prefer")
	check("required authentication "+pc.RequireAuth, pc.RequireAuth == "!password")
	check("Kerberos service "+pc.KerberosSrvName, pc.KerberosSrvName == "")
	for k, v := range pc.RuntimeParams {
		check("parameter "+k+"="+v, k == "application_name" && v == appName)
	}
	if len(problems) > 0 {
		t.Fatalf("the environment changed the connection: %s", strings.Join(problems, "; "))
	}
}

// A ClickHouse server over HTTPS that redirects elsewhere does not get
// the password sent there, and no proxy of the environment sees it.
func TestClickHouseHTTPNoRedirectNoEnvProxy(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-ClickHouse-Key") != "" {
			elsewhere.Add(1)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer other.Close()
	var reached atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		http.Redirect(w, r, other.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	cfg := Config{Name: "c", Engine: ClickHouse, Host: "127.0.0.1", Port: 8443, User: "default", Password: "s3cret", TLS: TLSRequire}
	ep := endpoint{host: "127.0.0.1", port: port, serverName: "127.0.0.1"}
	tc, _ := tlsConfig(&cfg, ep.serverName)
	sqldb := openClickHouse(cfg, ep, tc)
	defer sqldb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sqldb.PingContext(ctx)
	if reached.Load() == 0 {
		t.Fatal("the request never reached the configured server")
	}
	if n := elsewhere.Load(); n > 0 {
		t.Fatalf("the password followed %d redirects to another server", n)
	}

	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	rt, err := clickhouseTransport(net.JoinHostPort(ep.host, strconv.Itoa(ep.port)))(transport)
	if err != nil {
		t.Fatal(err)
	}
	if transport.Proxy != nil {
		t.Fatal("the transport takes its proxy from the environment")
	}
	req, _ := http.NewRequest(http.MethodPost, other.URL, nil)
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("the transport sent a request to another server")
	}
}

// The negotiated TLS of each engine's test server: PostgreSQL's has none,
// MySQL's offers it, and every MySQL connection, not only the first, uses
// it. ClickHouse over HTTP reaches its configured address through the
// transport that refuses others.
func TestIntegrationTLSState(t *testing.T) {
	integration(t)
	ctx := context.Background()
	for _, c := range []struct {
		cfg  Config
		want string
	}{
		{Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dgopher", Database: "postgres", TLS: TLSPrefer}, "no TLS"},
		{Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dgopher", Database: "postgres"}, "no TLS"},
		{Config{Name: "my", Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dgopher", Database: "shop", TLS: TLSPrefer}, "TLS"},
		{Config{Name: "my", Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dgopher", Database: "shop", TLS: TLSDisable}, "no TLS"},
		{Config{Name: "ch", Engine: ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dgopher", TLS: TLSPrefer}, "no TLS"},
	} {
		d, err := Open(ctx, c.cfg, nil)
		if err != nil {
			t.Fatalf("%s %s: %v", c.cfg.Engine, c.cfg.TLS, err)
		}
		if got := d.TLSState(); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.cfg.Engine, c.cfg.TLS, got, c.want)
		}
		if c.cfg.Engine == MySQL && c.cfg.TLS == TLSPrefer {
			// Hold one connection so the next is new.
			held, _ := d.SQL.Conn(ctx)
			var name, cipher string
			if err := d.SQL.QueryRowContext(ctx, "SHOW SESSION STATUS LIKE 'Ssl_cipher'").Scan(&name, &cipher); err != nil || cipher == "" {
				t.Errorf("a second MySQL connection: cipher %q, %v", cipher, err)
			}
			held.Close()
		}
		d.Close()
	}
	k, err := OpenRedis(ctx, Config{Name: "r", Engine: Redis, Host: "127.0.0.1", Port: 16379, TLS: TLSPrefer}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := k.TLSState(); got != "no TLS" {
		t.Errorf("Redis prefer: %q", got)
	}
	k.Close()

	// ClickHouse's HTTP interface, on a port the protocol is not chosen by.
	ch := Config{Name: "ch", Engine: ClickHouse, Host: "127.0.0.1", Port: 8123, User: "default", Password: "dgopher"}
	d, err := openWith(ctx, ch, endpoint{host: "127.0.0.1", port: 18123, serverName: "127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("ClickHouse over HTTP: %v", err)
	}
	defer d.Close()
	if v := d.ServerVersion(ctx); v == "" {
		t.Error("ClickHouse over HTTP answered no version")
	}
}

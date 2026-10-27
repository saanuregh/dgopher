package db

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/netproxy/proxytest"
	"dgopher/internal/sshtunnel"
	"dgopher/internal/sshtunnel/sshtest"
	"dgopher/internal/testutil/optionalserver"
)

// oracleTLSConfig reaches the test server's TCPS listener (compose.yaml),
// whose certificate names localhost alone; oracleMTLSPort's asks for a
// client certificate of the test CA too.
var oracleTLSConfig = Config{Name: "oracle-tls", Engine: Oracle, Host: "localhost", Port: 12484, User: "dgopher", Password: "dgopher",
	Database: "FREEPDB1"}

const oracleMTLSPort = 12485

// writeWallet writes an auto-login wallet into a folder of its own and
// returns the folder.
func writeWallet(t *testing.T, key *rsa.PrivateKey, cert *x509.Certificate, trusted ...*x509.Certificate) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cwallet.sso"), oracleWallet(t, key, cert, trusted...), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writePEM writes a certificate's and its key's PEM files and returns
// their paths.
func writePEM(t *testing.T, cert *x509.Certificate, key *rsa.PrivateKey) (string, string) {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// A wallet as Oracle's tools write it gives the connection the
// certificates it trusts and the client certificate it logs in with.
func TestOracleWalletRead(t *testing.T) {
	ca, caKey := rsaCertificate(t, "a CA", 0, nil, nil)
	client, clientKey := rsaCertificate(t, "dgopher", x509.ExtKeyUsageClientAuth, ca, caKey)
	tc, err := withOracleWallet(&tls.Config{}, writeWallet(t, clientKey, client, ca))
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.Certificates) != 1 || !tc.Certificates[0].Leaf.Equal(client) {
		t.Fatalf("client certificates %v", tc.Certificates)
	}
	if _, err := client.Verify(x509.VerifyOptions{Roots: tc.RootCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("the wallet's CA is not trusted: %v", err)
	}
	tc, err = withOracleWallet(&tls.Config{}, writeWallet(t, nil, nil, ca))
	if err != nil || len(tc.Certificates) != 0 {
		t.Fatalf("a wallet of trusted certificates alone: %v %v", tc.Certificates, err)
	}
}

// TLS modes, CA files, wallets and client certificates against the test
// server's TCPS listeners: verify-full refuses a certificate that names
// another host, or of a CA the connection does not trust.
func TestOracleTLS(t *testing.T) {
	integration(t)
	optionalserver.SkipUnless(t, "oracle-tcps")
	optionalserver.SkipUnless(t, "oracle-tcps-mtls")
	ctx := context.Background()
	ca, caKey := oracleTestCA(t)
	caFile := filepath.Join(oracleTLSDir, "ca.pem")
	client, clientKey := rsaCertificate(t, "dgopher", x509.ExtKeyUsageClientAuth, ca, caKey)
	certFile, keyFile := writePEM(t, client, clientKey)
	other, _ := rsaCertificate(t, "another CA", 0, nil, nil)
	caWallet := writeWallet(t, nil, nil, ca)
	clientWallet := writeWallet(t, clientKey, client, ca)
	for _, c := range []struct {
		name   string
		change func(*Config)
		err    string
	}{
		{name: "require", change: func(c *Config) { c.TLS = TLSRequire }},
		{name: "verify-full with the CA", change: func(c *Config) { c.TLS, c.CAFile = TLSVerifyFull, caFile }},
		{name: "verify-full of another host", change: func(c *Config) { c.TLS, c.CAFile, c.Host = TLSVerifyFull, caFile, "127.0.0.1" },
			err: "certificate for 127.0.0.1"},
		{name: "verify-full of an unknown CA", change: func(c *Config) { c.TLS = TLSVerifyFull }, err: "unknown authority"},
		{name: "wallet with the CA", change: func(c *Config) { c.TLS, c.Oracle.Wallet = TLSVerifyFull, caWallet }},
		{name: "wallet of another host", change: func(c *Config) { c.TLS, c.Oracle.Wallet, c.Host = TLSVerifyFull, caWallet, "127.0.0.1" },
			err: "certificate for 127.0.0.1"},
		{name: "wallet of another CA", change: func(c *Config) { c.TLS, c.Oracle.Wallet = TLSVerifyFull, writeWallet(t, nil, nil, other) },
			err: "unknown authority"},
		{name: "client certificate asked, none given", change: func(c *Config) { c.TLS, c.CAFile, c.Port = TLSVerifyFull, caFile, oracleMTLSPort },
			err: "certificate required"},
		{name: "client certificate files", change: func(c *Config) {
			c.TLS, c.CAFile, c.CertFile, c.KeyFile, c.Port = TLSVerifyFull, caFile, certFile, keyFile, oracleMTLSPort
		}},
		{name: "client certificate in the wallet", change: func(c *Config) { c.TLS, c.Oracle.Wallet, c.Port = TLSVerifyFull, clientWallet, oracleMTLSPort }},
		{name: "client certificate in the wallet, require", change: func(c *Config) { c.TLS, c.Oracle.Wallet, c.Port = TLSRequire, clientWallet, oracleMTLSPort }},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := oracleTLSConfig
			c.change(&cfg)
			d, err := Open(ctx, cfg, nil)
			if c.err != "" {
				if err == nil {
					d.Close()
					t.Fatalf("connected, want an error saying %q", c.err)
				}
				if !strings.Contains(err.Error(), c.err) {
					t.Fatalf("%v, want an error saying %q", err, c.err)
				}
				t.Log(err)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			var protocol, service string
			if err := d.SQL.QueryRowContext(ctx, "SELECT SYS_CONTEXT('USERENV', 'NETWORK_PROTOCOL'), SYS_CONTEXT('USERENV', 'SERVICE_NAME') FROM dual").
				Scan(&protocol, &service); err != nil {
				t.Fatal(err)
			}
			if !strings.EqualFold(protocol, "tcps") || !strings.EqualFold(service, "FREEPDB1") || d.TLSState() != "TLS" {
				t.Fatalf("protocol %q, service %q, TLS state %q", protocol, service, d.TLSState())
			}
		})
	}
}

// verify-full checks the host the connection names through an SSH tunnel
// or a proxy too, not the local address the driver dials.
func TestOracleTLSThroughRoutes(t *testing.T) {
	integration(t)
	optionalserver.SkipUnless(t, "oracle-tcps")
	ctx := context.Background()
	socks := proxytest.SOCKS5(t, "proxy-user", "proxy-pass")
	s := sshtest.Start(t, "secret", nil)
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := sshtunnel.Trust(known, s.Addr, s.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	routes := map[string]func(*Config){
		"ssh": func(c *Config) {
			c.SSH = SSHConfig{Enabled: true, Host: s.Host, Port: s.Port, User: "tester", Password: "secret"}
		},
		"socks5": func(c *Config) {
			c.Proxy = ProxyConfig{Kind: socks.Kind, Host: socks.Host, Port: socks.Port, User: socks.User, Password: socks.Password}
		},
	}
	for name, route := range routes {
		cfg := oracleTLSConfig
		cfg.TLS, cfg.CAFile = TLSVerifyFull, filepath.Join(oracleTLSDir, "ca.pem")
		route(&cfg)
		d, err := Open(ctx, cfg, []string{known})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var protocol string
		err = d.SQL.QueryRowContext(ctx, "SELECT SYS_CONTEXT('USERENV', 'NETWORK_PROTOCOL') FROM dual").Scan(&protocol)
		d.Close()
		if err != nil || !strings.EqualFold(protocol, "tcps") || d.route == nil {
			t.Fatalf("%s: %q %v, route %v", name, protocol, err, d.route)
		}
		cfg.Host = "127.0.0.1"
		d, err = Open(ctx, cfg, []string{known})
		if err == nil {
			d.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "certificate for 127.0.0.1") {
			t.Fatalf("%s: another host's name: %v", name, err)
		}
		t.Logf("%s: %v", name, err)
	}
}

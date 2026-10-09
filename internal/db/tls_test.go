package db

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// certificate makes a certificate for name signed by parent's key, or
// self-signed without a parent, and writes it and its key as PEM files.
func certificate(t *testing.T, name string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{name},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	if parent == nil {
		tmpl.IsCA, tmpl.BasicConstraintsValid, tmpl.KeyUsage = true, true, x509.KeyUsageCertSign
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, name+".pem"), filepath.Join(dir, name+".key")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	return cert, key, certPath, keyPath
}

// A connection with a client certificate logs in with it to a server
// that asks for one; without, the server turns it away.
func TestClientCertificate(t *testing.T) {
	ca, caKey, caPath, _ := certificate(t, "ca", nil, nil)
	server, serverKey, _, _ := certificate(t, "db.test", ca, caKey)
	_, _, clientPath, clientKeyPath := certificate(t, "app-user", ca, caKey)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{server.Raw}, PrivateKey: serverKey}},
		ClientAuth:   tls.RequireAndVerifyClientCert, ClientCAs: pool,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				tc := c.(*tls.Conn)
				if tc.Handshake() == nil {
					io.WriteString(c, tc.ConnectionState().PeerCertificates[0].Subject.CommonName)
				}
			}()
		}
	}()
	dial := func(cfg Config) (string, error) {
		tc, err := tlsConfig(&cfg, "db.test")
		if err != nil {
			return "", err
		}
		c, err := tls.Dial("tcp", ln.Addr().String(), tc)
		if err != nil {
			return "", err
		}
		defer c.Close()
		got, err := io.ReadAll(c)
		return string(got), err
	}
	cfg := Config{TLS: TLSVerifyFull, CAFile: caPath, CertFile: clientPath, KeyFile: clientKeyPath}
	if got, err := dial(cfg); err != nil || got != "app-user" {
		t.Fatalf("with the certificate: %q %v", got, err)
	}
	cfg.CertFile, cfg.KeyFile = "", ""
	if got, err := dial(cfg); err == nil && got != "" {
		t.Fatalf("without a certificate the server answered %q", got)
	}
	cfg.CertFile, cfg.KeyFile = clientPath, caPath // the CA's certificate is no key
	if _, err := tlsConfig(&cfg, "db.test"); err == nil || !strings.Contains(err.Error(), "client certificate") {
		t.Fatalf("a wrong key file: %v", err)
	}
	bad := Config{Name: "x", Engine: Postgres, Host: "h", TLS: TLSDisable, CertFile: clientPath, KeyFile: clientKeyPath}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "needs TLS") {
		t.Fatalf("a certificate without TLS: %v", err)
	}
}

package db

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

// cleartextAsker is a server, or one in the middle, that takes TLS with a
// certificate of its own and asks for the password as it is; it sends
// what it is then told to got.
func cleartextAsker(t *testing.T, ln net.Listener, got chan<- string) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Error(err)
		return
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "middle"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Error(err)
		return
	}
	c, err := ln.Accept()
	if err != nil {
		got <- ""
		return
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	var l [4]byte
	io.ReadFull(c, l[:])
	io.ReadFull(c, make([]byte, binary.BigEndian.Uint32(l[:])-4)) // SSLRequest
	c.Write([]byte{'S'})
	tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	if err := tc.Handshake(); err != nil {
		got <- ""
		return
	}
	io.ReadFull(tc, l[:])
	io.ReadFull(tc, make([]byte, binary.BigEndian.Uint32(l[:])-4)) // startup message
	tc.Write([]byte{'R', 0, 0, 0, 8, 0, 0, 0, 3})                  // AuthenticationCleartextPassword
	buf := make([]byte, 256)
	n, _ := tc.Read(buf)
	got <- string(buf[:n])
}

// A mode that checks no certificate, prefer or require, cannot tell the
// server from one in the middle: neither may have the password as it is.
func TestTLSUnverifiedRefusesCleartextPassword(t *testing.T) {
	const password = "not-a-real-password-7"
	for _, mode := range []TLSMode{TLSPrefer, TLSRequire} {
		t.Run(string(mode), func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			got := make(chan string, 1)
			go cleartextAsker(t, ln, got)
			cfg := Config{Name: "x", Engine: Postgres, Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: "u", Password: password, TLS: mode}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if d, err := Open(ctx, cfg, nil); err == nil {
				d.Close()
				t.Fatal("connected")
			}
			if read := <-got; strings.Contains(read, password) {
				t.Fatalf("the server read the password: %q", read)
			}
		})
	}
}

// pg_dump and pg_restore keep the same rule, through libpq's
// PGREQUIREAUTH (PostgreSQL 16 on; older ones ignore it).
func TestPostgresToolRefusesCleartextUnverified(t *testing.T) {
	for _, mode := range []TLSMode{TLSDisable, TLSPrefer, TLSRequire, TLSVerifyFull} {
		d := &DB{Config: Config{Engine: Postgres, Host: "db.example.com", Port: 5432, User: "app", Password: "pw", Database: "shop", TLS: mode, CAFile: "/ca.pem"}}
		run, err := PlanBackup(context.Background(), d, BackupOptions{Format: BackupArchive, Path: "/tmp/shop.dump"})
		if err != nil {
			t.Fatal(mode, err)
		}
		want := mode == TLSPrefer || mode == TLSRequire
		if got := slices.Contains(run.Env, "PGREQUIREAUTH=!password"); got != want {
			t.Errorf("%s: PGREQUIREAUTH set %v, want %v (%q)", mode, got, want, run.Env)
		}
	}
}

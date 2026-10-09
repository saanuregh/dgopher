package db

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

// A server asking for Kerberos reaches the GSSAPI provider, which tells
// what it lacks, here a krb5.conf: pgx has one registered.
func TestKerberosProvider(t *testing.T) {
	t.Setenv("KRB5_CONFIG", filepath.Join(t.TempDir(), "missing-krb5.conf"))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var size [4]byte
		if _, err := io.ReadFull(c, size[:]); err != nil {
			return
		}
		io.ReadFull(c, make([]byte, binary.BigEndian.Uint32(size[:])-4)) // the startup message
		c.Write([]byte{'R', 0, 0, 0, 8, 0, 0, 0, 7})                     // AuthenticationGSS
		io.Copy(io.Discard, c)
	}()
	port := l.Addr().(*net.TCPAddr).Port
	cfg := Config{Name: "k", Engine: Postgres, Host: "127.0.0.1", Port: port, User: "alice", Database: "x"}
	sqldb, err := openPostgres(cfg, endpoint{host: "127.0.0.1", port: port, serverName: "127.0.0.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	err = sqldb.PingContext(context.Background())
	if err == nil || strings.Contains(err.Error(), "no GSSAPI provider") || !strings.Contains(err.Error(), "Kerberos") {
		t.Fatalf("the server's Kerberos request: %v", err)
	}
}

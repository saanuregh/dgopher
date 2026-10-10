package sshtunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dgopher/internal/sshtunnel/sshtest"

	"golang.org/x/crypto/ssh"
)

const testPassword = "secret"

func newSigner(t *testing.T) (ssh.Signer, ed25519.PrivateKey) { return sshtest.NewSigner(t) }

func startServer(t *testing.T, clientKey ssh.PublicKey) *sshtest.Server {
	return sshtest.Start(t, testPassword, clientKey)
}

// startServerWith starts a server with more host keys than its ed25519
// one.
func startServerWith(t *testing.T, clientKey ssh.PublicKey, extraHostKeys ...ssh.Signer) *sshtest.Server {
	return sshtest.Start(t, testPassword, clientKey, extraHostKeys...)
}

func startEcho(t *testing.T) (string, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	}()
	return "127.0.0.1", l.Addr().(*net.TCPAddr).Port
}

func config(s *sshtest.Server) Config {
	return Config{Host: s.Host, Port: s.Port, User: "tester", Password: testPassword}
}

func trustedFile(t *testing.T, s *sshtest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "known_hosts")
	if err := Trust(path, s.Addr, s.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	return path
}

func roundTrip(t *testing.T, addr string, msg string) error {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		return err
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil {
		return err
	}
	if string(buf) != msg {
		return fmt.Errorf("got %q want %q", buf, msg)
	}
	return nil
}

func TestUnknownHostThenTrust(t *testing.T) {
	s := startServer(t, nil)
	echoHost, echoPort := startEcho(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "known_hosts")
	missing := filepath.Join(dir, "missing")

	_, err := Open(context.Background(), config(s), echoHost, echoPort, []string{missing, path})
	var hostKeyErr *HostKeyError
	if !errors.As(err, &hostKeyErr) {
		t.Fatalf("want HostKeyError, got %v", err)
	}
	if hostKeyErr.Mismatch || hostKeyErr.Host != s.Addr || hostKeyErr.Fingerprint != ssh.FingerprintSHA256(s.HostKey.PublicKey()) {
		t.Fatalf("unexpected error contents: %+v", hostKeyErr)
	}

	if err := Trust(path, hostKeyErr.Host, hostKeyErr.Key); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 { // Windows has no Unix modes
		t.Fatalf("known_hosts mode: %v %v", info, err)
	}
	if dirInfo, _ := os.Stat(filepath.Dir(path)); runtime.GOOS != "windows" && dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", dirInfo.Mode().Perm())
	}

	tunnel, err := Open(context.Background(), config(s), echoHost, echoPort, []string{missing, path})
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	if tunnel.LocalAddr() != net.JoinHostPort("127.0.0.1", strconv.Itoa(tunnel.LocalPort())) {
		t.Fatalf("addr %s port %d", tunnel.LocalAddr(), tunnel.LocalPort())
	}

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- roundTrip(t, tunnel.LocalAddr(), fmt.Sprintf("hello %d %s", i, bytes.Repeat([]byte("x"), 50000)))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestMismatchedHostKey(t *testing.T) {
	s := startServer(t, nil)
	other, _ := newSigner(t)
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := Trust(path, s.Addr, other.PublicKey()); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), config(s), "127.0.0.1", 1, []string{path})
	var hostKeyErr *HostKeyError
	if !errors.As(err, &hostKeyErr) || !hostKeyErr.Mismatch {
		t.Fatalf("want mismatch HostKeyError, got %v", err)
	}
}

func TestWrongPassword(t *testing.T) {
	s := startServer(t, nil)
	cfg := config(s)
	cfg.Password = "wrong"
	_, err := Open(context.Background(), cfg, "127.0.0.1", 1, []string{trustedFile(t, s)})
	var hostKeyErr *HostKeyError
	if err == nil || errors.As(err, &hostKeyErr) {
		t.Fatalf("want auth error, got %v", err)
	}
}

func TestNoAuthMethod(t *testing.T) {
	_, err := Open(context.Background(), Config{Host: "127.0.0.1", User: "u"}, "127.0.0.1", 1, nil)
	if err == nil {
		t.Fatal("want error")
	}
}

func TestKeyFileAuth(t *testing.T) {
	clientSigner, clientPriv := newSigner(t)
	s := startServer(t, clientSigner.PublicKey())
	echoHost, echoPort := startEcho(t)

	for _, passphrase := range []string{"", "pass phrase"} {
		var block *pem.Block
		var err error
		if passphrase == "" {
			block, err = ssh.MarshalPrivateKey(clientPriv, "")
		} else {
			block, err = ssh.MarshalPrivateKeyWithPassphrase(clientPriv, "", []byte(passphrase))
		}
		if err != nil {
			t.Fatal(err)
		}
		keyPath := filepath.Join(t.TempDir(), "id_ed25519")
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := Config{Host: s.Host, Port: s.Port, User: "tester", KeyPath: keyPath, KeyPassphrase: passphrase}
		tunnel, err := Open(context.Background(), cfg, echoHost, echoPort, []string{trustedFile(t, s)})
		if err != nil {
			t.Fatalf("passphrase %q: %v", passphrase, err)
		}
		if err := roundTrip(t, tunnel.LocalAddr(), "key auth"); err != nil {
			t.Error(err)
		}
		tunnel.Close()
	}
}

func TestCloseIdempotent(t *testing.T) {
	s := startServer(t, nil)
	echoHost, echoPort := startEcho(t)
	tunnel, err := Open(context.Background(), config(s), echoHost, echoPort, []string{trustedFile(t, s)})
	if err != nil {
		t.Fatal(err)
	}
	held, err := net.Dial("tcp", tunnel.LocalAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := roundTrip(t, tunnel.LocalAddr(), "before close"); err != nil {
		t.Fatal(err)
	}
	if err := tunnel.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tunnel.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tunnel.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed")
	}
	if tunnel.Err() != nil {
		t.Fatalf("Err after Close: %v", tunnel.Err())
	}
	if c, err := net.Dial("tcp", tunnel.LocalAddr()); err == nil {
		c.Close()
		t.Fatal("listener still accepts after Close")
	}
	held.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := held.Read(make([]byte, 1)); err == nil {
		t.Fatal("held connection still open after Close")
	}
}

func TestServerGoneClosesTunnel(t *testing.T) {
	old := keepaliveInterval
	keepaliveInterval = 50 * time.Millisecond
	t.Cleanup(func() { keepaliveInterval = old })

	s := startServer(t, nil)
	tunnel, err := Open(context.Background(), config(s), "127.0.0.1", 1, []string{trustedFile(t, s)})
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	time.Sleep(150 * time.Millisecond)
	select {
	case <-tunnel.Done():
		t.Fatalf("tunnel stopped early: %v", tunnel.Err())
	default:
	}
	s.Stop()
	select {
	case <-tunnel.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel did not notice server going away")
	}
	if tunnel.Err() == nil {
		t.Fatal("want non-nil Err")
	}
}

func TestOpenRespectsContext(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close() // silent server: never sends a banner
		}
	}()
	port := l.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Open(ctx, Config{Host: "127.0.0.1", Port: port, User: "u", Password: "p"}, "127.0.0.1", 1, nil)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("want quick error, got %v after %v", err, time.Since(start))
	}
}

// A host known by its ed25519 key, which also has an RSA one that the
// client would otherwise ask for first, is not taken for a changed key.
func TestKnownKeyTypeIsAskedFor(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaSigner, err := ssh.NewSignerFromKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	s := startServerWith(t, nil, rsaSigner)
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := Trust(known, s.Addr, s.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	echoHost, echoPort := startEcho(t)
	tun, err := Open(context.Background(), config(s), echoHost, echoPort, []string{known})
	if err != nil {
		t.Fatalf("a known host refused: %v", err)
	}
	tun.Close()
}

// Connect forwards no port: Dial reaches each address through the
// server, and its connections close with the tunnel.
func TestConnectDials(t *testing.T) {
	s := startServer(t, nil)
	echoHost, echoPort := startEcho(t)
	tunnel, err := Connect(context.Background(), config(s), []string{trustedFile(t, s)})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tunnel.Dial(context.Background(), net.JoinHostPort(echoHost, strconv.Itoa(echoPort)))
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("through ssh")
	if _, err := conn.Write(msg); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("read %q: %v", got, err)
	}
	tunnel.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("a dialed connection stays open after Close")
	}
	if _, err := tunnel.Dial(context.Background(), net.JoinHostPort(echoHost, strconv.Itoa(echoPort))); err == nil {
		t.Fatal("dialed after Close")
	}
}

// keyFile writes a private key to a file of the test.
func keyFile(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A tunnel goes through jump hosts in turn, each host key checked: an
// unknown jump is named in the error, and once trusted the tunnel opens.
// The jump host logs in with the key, the SSH host with the password.
func TestJumpHosts(t *testing.T) {
	clientSigner, clientPriv := newSigner(t)
	jump, target := startServer(t, clientSigner.PublicKey()), startServer(t, nil)
	echoHost, echoPort := startEcho(t)
	known := trustedFile(t, target)
	cfg := config(target)
	cfg.KeyPath = keyFile(t, clientPriv)
	cfg.Jumps = []Hop{{Host: jump.Host, Port: jump.Port, User: "tester"}}
	_, err := Open(context.Background(), cfg, echoHost, echoPort, []string{known})
	var hostKeyErr *HostKeyError
	if !errors.As(err, &hostKeyErr) || hostKeyErr.Host != jump.Addr {
		t.Fatalf("an unknown jump host: %v", err)
	}
	if err := Trust(known, jump.Addr, jump.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	tun, err := Open(context.Background(), cfg, echoHost, echoPort, []string{known})
	if err != nil {
		t.Fatal(err)
	}
	if err := roundTrip(t, tun.LocalAddr(), "through the jump"); err != nil {
		t.Fatal(err)
	}
	tun.Close()
	cfg.Jumps[0].User = ""
	if _, err := Open(context.Background(), cfg, echoHost, echoPort, []string{known}); err == nil {
		t.Fatal("a jump host without a user")
	}
}

// A jump host is offered the key and the agent, never the password of
// the SSH host behind it; without either, the tunnel says what it needs.
func TestJumpHostGetsNoPassword(t *testing.T) {
	_, clientPriv := newSigner(t)
	jump, target := startServer(t, nil), startServer(t, nil)
	echoHost, echoPort := startEcho(t)
	known := trustedFile(t, target)
	if err := Trust(known, jump.Addr, jump.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	cfg := config(target)
	cfg.KeyPath = keyFile(t, clientPriv)
	cfg.Jumps = []Hop{{Host: jump.Host, Port: jump.Port, User: "tester"}}
	if tun, err := Open(context.Background(), cfg, echoHost, echoPort, []string{known}); err == nil {
		tun.Close()
		t.Fatal("the jump host let in a key it does not know")
	}
	if got := jump.Passwords(); len(got) > 0 {
		t.Fatalf("the jump host was offered %q", got)
	}
	cfg.KeyPath = ""
	_, err := Open(context.Background(), cfg, echoHost, echoPort, []string{known})
	if err == nil || !strings.Contains(err.Error(), "jump host") {
		t.Fatalf("a jump host without a key or the agent: %v", err)
	}
	if got := jump.Passwords(); len(got) > 0 {
		t.Fatalf("the jump host was offered %q", got)
	}
}

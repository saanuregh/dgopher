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
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const testPassword = "secret"

type testServer struct {
	addr     string
	host     string
	port     int
	hostKey  ssh.Signer
	listener net.Listener

	mu    sync.Mutex
	conns []net.Conn
}

func newSigner(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, priv
}

func startServer(t *testing.T, clientKey ssh.PublicKey) *testServer {
	return startServerWith(t, clientKey)
}

// startServerWith starts a server with more host keys than its ed25519
// one.
func startServerWith(t *testing.T, clientKey ssh.PublicKey, extraHostKeys ...ssh.Signer) *testServer {
	t.Helper()
	hostKey, _ := newSigner(t)
	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == testPassword {
				return nil, nil
			}
			return nil, errors.New("bad password")
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if clientKey != nil && bytes.Equal(key.Marshal(), clientKey.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unknown key")
		},
	}
	config.AddHostKey(hostKey)
	for _, k := range extraHostKeys {
		config.AddHostKey(k)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testServer{addr: l.Addr().String(), hostKey: hostKey, listener: l}
	host, port, _ := net.SplitHostPort(s.addr)
	s.host = host
	s.port, _ = strconv.Atoi(port)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, c)
			s.mu.Unlock()
			go s.serve(c, config)
		}
	}()
	t.Cleanup(s.stop)
	return s
}

func (s *testServer) stop() {
	s.listener.Close()
	s.mu.Lock()
	for _, c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
}

func (s *testServer) serve(c net.Conn, config *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, config)
	if err != nil {
		c.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "direct-tcpip" {
			newChan.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		var target struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if err := ssh.Unmarshal(newChan.ExtraData(), &target); err != nil {
			newChan.Reject(ssh.ConnectionFailed, "bad payload")
			continue
		}
		go func() {
			remote, err := net.Dial("tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
			if err != nil {
				newChan.Reject(ssh.ConnectionFailed, err.Error())
				return
			}
			ch, chReqs, err := newChan.Accept()
			if err != nil {
				remote.Close()
				return
			}
			go ssh.DiscardRequests(chReqs)
			go func() {
				io.Copy(ch, remote)
				ch.CloseWrite()
			}()
			io.Copy(remote, ch)
			remote.(*net.TCPConn).CloseWrite()
		}()
	}
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

func (s *testServer) config() Config {
	return Config{Host: s.host, Port: s.port, User: "tester", Password: testPassword}
}

func trustedFile(t *testing.T, s *testServer) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "known_hosts")
	if err := Trust(path, s.addr, s.hostKey.PublicKey()); err != nil {
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

	_, err := Open(context.Background(), s.config(), echoHost, echoPort, []string{missing, path})
	var hostKeyErr *HostKeyError
	if !errors.As(err, &hostKeyErr) {
		t.Fatalf("want HostKeyError, got %v", err)
	}
	if hostKeyErr.Mismatch || hostKeyErr.Host != s.addr || hostKeyErr.Fingerprint != ssh.FingerprintSHA256(s.hostKey.PublicKey()) {
		t.Fatalf("unexpected error contents: %+v", hostKeyErr)
	}

	if err := Trust(path, hostKeyErr.Host, hostKeyErr.Key); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("known_hosts mode: %v %v", info, err)
	}
	if dirInfo, _ := os.Stat(filepath.Dir(path)); dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", dirInfo.Mode().Perm())
	}

	tunnel, err := Open(context.Background(), s.config(), echoHost, echoPort, []string{missing, path})
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
	if err := Trust(path, s.addr, other.PublicKey()); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), s.config(), "127.0.0.1", 1, []string{path})
	var hostKeyErr *HostKeyError
	if !errors.As(err, &hostKeyErr) || !hostKeyErr.Mismatch {
		t.Fatalf("want mismatch HostKeyError, got %v", err)
	}
}

func TestWrongPassword(t *testing.T) {
	s := startServer(t, nil)
	cfg := s.config()
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
		cfg := Config{Host: s.host, Port: s.port, User: "tester", KeyPath: keyPath, KeyPassphrase: passphrase}
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
	tunnel, err := Open(context.Background(), s.config(), echoHost, echoPort, []string{trustedFile(t, s)})
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
	tunnel, err := Open(context.Background(), s.config(), "127.0.0.1", 1, []string{trustedFile(t, s)})
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
	s.stop()
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
	if err := Trust(known, s.addr, s.hostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	echoHost, echoPort := startEcho(t)
	tun, err := Open(context.Background(), s.config(), echoHost, echoPort, []string{known})
	if err != nil {
		t.Fatalf("a known host refused: %v", err)
	}
	tun.Close()
}

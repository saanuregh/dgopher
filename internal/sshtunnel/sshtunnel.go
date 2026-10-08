// Package sshtunnel forwards local TCP connections to a remote host through an SSH server,
// verifying the server's host key against known_hosts files.
package sshtunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// keepaliveInterval is a variable so tests can shorten it.
var keepaliveInterval = 30 * time.Second

const defaultDialTimeout = 15 * time.Second

type Config struct {
	Host          string
	Port          int // 0 means 22
	User          string
	Password      string
	KeyPath       string // "~/" is expanded
	KeyPassphrase string
	UseAgent      bool // use the SSH_AUTH_SOCK agent if set
}

// HostKeyError reports a host key that is unknown or does not match known_hosts.
type HostKeyError struct {
	Host        string // "host:port" as dialed
	Fingerprint string // ssh.FingerprintSHA256 of the presented key
	Key         ssh.PublicKey
	// Mismatch is true when known_hosts holds a different key for the host (possible attack);
	// such a key must not be offered for trust.
	Mismatch bool
}

func (e *HostKeyError) Error() string {
	if e.Mismatch {
		return fmt.Sprintf("ssh host key for %s does not match known_hosts (presented %s): possible man-in-the-middle attack", e.Host, e.Fingerprint)
	}
	return fmt.Sprintf("ssh host %s is not in known_hosts (key %s)", e.Host, e.Fingerprint)
}

type Tunnel struct {
	client    *ssh.Client
	listener  net.Listener
	agentConn net.Conn
	remote    string
	keepalive time.Duration

	stopOnce sync.Once
	done     chan struct{}
	err      error

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
}

// Open connects to the SSH server, verifying its host key against knownHostsFiles (missing files
// are skipped), then listens on 127.0.0.1:0 and forwards each accepted connection to
// remoteHost:remotePort through SSH.
func Open(ctx context.Context, cfg Config, remoteHost string, remotePort int, knownHostsFiles []string) (*Tunnel, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("sshtunnel: listen: %w", err)
	}
	t, err := connect(ctx, cfg, knownHostsFiles, listener, net.JoinHostPort(remoteHost, strconv.Itoa(remotePort)))
	if err != nil {
		listener.Close()
		return nil, err
	}
	go t.acceptLoop()
	return t, nil
}

// Connect connects to the SSH server, verifying its host key as Open does, without forwarding
// a port: Dial reaches each address through it, as for the nodes of a cluster.
func Connect(ctx context.Context, cfg Config, knownHostsFiles []string) (*Tunnel, error) {
	return connect(ctx, cfg, knownHostsFiles, nil, "")
}

// connect connects to the SSH server. The tunnel takes listener, nil for none, before anything
// can stop it.
func connect(ctx context.Context, cfg Config, knownHostsFiles []string, listener net.Listener, remote string) (*Tunnel, error) {
	if cfg.Host == "" {
		return nil, errors.New("sshtunnel: host is required")
	}
	if cfg.User == "" {
		return nil, errors.New("sshtunnel: user is required")
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))

	hostKeyCallback, err := hostKeyCallback(knownHostsFiles)
	if err != nil {
		return nil, err
	}
	auth, agentConn, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	closeAgent := func() {
		if agentConn != nil {
			agentConn.Close()
		}
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultDialTimeout)
		defer cancel()
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		closeAgent()
		return nil, fmt.Errorf("sshtunnel: dial %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	stopWatch := context.AfterFunc(ctx, func() { conn.Close() })
	clientConn, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
		// Ask for a key of a type known_hosts has for the host: a server
		// offering another type would read as a changed key.
		HostKeyAlgorithms: knownAlgorithms(knownHostsFiles, addr, conn.RemoteAddr()),
	})
	if !stopWatch() || err != nil {
		conn.Close()
		closeAgent()
		if err == nil {
			clientConn.Close()
			err = ctx.Err()
		}
		var hostKeyErr *HostKeyError
		if errors.As(err, &hostKeyErr) {
			return nil, hostKeyErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("sshtunnel: handshake with %s: %w", addr, ctxErr)
		}
		return nil, fmt.Errorf("sshtunnel: handshake with %s: %w", addr, err)
	}
	conn.SetDeadline(time.Time{})
	client := ssh.NewClient(clientConn, chans, reqs)

	t := &Tunnel{
		client:    client,
		listener:  listener,
		agentConn: agentConn,
		remote:    remote,
		keepalive: keepaliveInterval,
		done:      make(chan struct{}),
		conns:     make(map[net.Conn]struct{}),
	}
	go t.keepaliveLoop()
	go func() {
		err := client.Wait()
		if err == nil {
			err = io.EOF
		}
		t.stop(fmt.Errorf("sshtunnel: ssh connection closed: %w", err))
	}()
	return t, nil
}

// Dial connects to addr as the SSH server would; the connection closes with the tunnel.
func (t *Tunnel) Dial(ctx context.Context, addr string) (net.Conn, error) {
	c, err := t.client.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("sshtunnel: dial %s through the SSH server: %w", addr, err)
	}
	if !t.track(c) {
		c.Close()
		return nil, errors.New("sshtunnel: the tunnel is closed")
	}
	return &trackedConn{Conn: c, t: t}, nil
}

// trackedConn is a connection of Dial, which the tunnel forgets as it closes.
type trackedConn struct {
	net.Conn
	t *Tunnel
}

func (c *trackedConn) Close() error {
	c.t.untrack(c.Conn)
	return nil
}

func (t *Tunnel) LocalAddr() string { return t.listener.Addr().String() }

func (t *Tunnel) LocalPort() int { return t.listener.Addr().(*net.TCPAddr).Port }

// Close is idempotent; it closes the listener, active forwards and the ssh client.
func (t *Tunnel) Close() error {
	t.stop(nil)
	return nil
}

// Done is closed when the tunnel stops for any reason.
func (t *Tunnel) Done() <-chan struct{} { return t.done }

// Err reports why the tunnel stopped; nil if it is running or Close was called.
func (t *Tunnel) Err() error {
	select {
	case <-t.done:
		return t.err
	default:
		return nil
	}
}

func (t *Tunnel) stop(err error) {
	t.stopOnce.Do(func() {
		t.err = err
		if t.listener != nil {
			t.listener.Close()
		}
		t.client.Close()
		if t.agentConn != nil {
			t.agentConn.Close()
		}
		t.mu.Lock()
		for c := range t.conns {
			c.Close()
		}
		t.mu.Unlock()
		close(t.done)
	})
}

func (t *Tunnel) track(c net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.done:
		return false
	default:
	}
	t.conns[c] = struct{}{}
	return true
}

func (t *Tunnel) untrack(c net.Conn) {
	t.mu.Lock()
	delete(t.conns, c)
	t.mu.Unlock()
	c.Close()
}

func (t *Tunnel) acceptLoop() {
	for {
		local, err := t.listener.Accept()
		if err != nil {
			t.stop(fmt.Errorf("sshtunnel: accept: %w", err))
			return
		}
		if !t.track(local) {
			local.Close()
			return
		}
		go t.forward(local)
	}
}

func (t *Tunnel) forward(local net.Conn) {
	defer t.untrack(local)
	remote, err := t.client.Dial("tcp", t.remote)
	if err != nil {
		return
	}
	if !t.track(remote) {
		remote.Close()
		return
	}
	defer t.untrack(remote)
	copied := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		copied <- struct{}{}
	}
	go pipe(remote, local)
	go pipe(local, remote)
	<-copied
	<-copied
}

func (t *Tunnel) keepaliveLoop() {
	ticker := time.NewTicker(t.keepalive)
	defer ticker.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-ticker.C:
			reply := make(chan error, 1)
			go func() {
				_, _, err := t.client.SendRequest("keepalive@openssh.com", true, nil)
				reply <- err
			}()
			select {
			case err := <-reply:
				if err != nil {
					t.stop(fmt.Errorf("sshtunnel: keepalive failed: %w", err))
					return
				}
			case <-time.After(t.keepalive):
				t.stop(errors.New("sshtunnel: keepalive timed out"))
				return
			case <-t.done:
				return
			}
		}
	}
}

func hostKeyCallback(files []string) (ssh.HostKeyCallback, error) {
	var existing []string
	for _, f := range files {
		f = expandHome(f)
		if _, err := os.Stat(f); err == nil {
			existing = append(existing, f)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("sshtunnel: known_hosts %s: %w", f, err)
		}
	}
	unknown := func(host string, key ssh.PublicKey, mismatch bool) error {
		return &HostKeyError{Host: host, Fingerprint: ssh.FingerprintSHA256(key), Key: key, Mismatch: mismatch}
	}
	if len(existing) == 0 {
		return func(host string, _ net.Addr, key ssh.PublicKey) error {
			return unknown(host, key, false)
		}, nil
	}
	known, err := knownhosts.New(existing...)
	if err != nil {
		return nil, fmt.Errorf("sshtunnel: known_hosts: %w", err)
	}
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		err := known(host, remote, key)
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) {
			return unknown(host, key, len(keyErr.Want) > 0)
		}
		return err
	}, nil
}

func authMethods(cfg Config) ([]ssh.AuthMethod, net.Conn, error) {
	var methods []ssh.AuthMethod
	var agentConn net.Conn
	if cfg.UseAgent {
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			if c, err := net.Dial("unix", sock); err == nil {
				agentConn = c
				methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(c).Signers))
			}
		}
	}
	if cfg.KeyPath != "" {
		signer, err := loadKey(expandHome(cfg.KeyPath), cfg.KeyPassphrase)
		if err != nil {
			if agentConn != nil {
				agentConn.Close()
			}
			return nil, nil, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}
	if len(methods) == 0 {
		return nil, nil, errors.New("sshtunnel: no authentication method: set a password, a key file, or use an ssh agent")
	}
	return methods, agentConn, nil
}

func loadKey(path, passphrase string) (ssh.Signer, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sshtunnel: read key: %w", err)
	}
	var signer ssh.Signer
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(pem)
	}
	if err != nil {
		return nil, fmt.Errorf("sshtunnel: parse key %s: %w", path, err)
	}
	return signer, nil
}

func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}

// Trust appends host's key to the known_hosts-format file at path, creating it 0600 and its
// parent directory 0700 if needed.
func Trust(path string, host string, key ssh.PublicKey) error {
	path = expandHome(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("sshtunnel: trust: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("sshtunnel: trust: %w", err)
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(host)}, key) + "\n"
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return fmt.Errorf("sshtunnel: trust: %w", err)
	}
	return f.Close()
}

var (
	probeKeyOnce sync.Once
	probeKey     ssh.PublicKey
)

// knownAlgorithms returns the host key algorithms of the keys known_hosts
// holds for a host, nil (the defaults) when it holds none. It asks the
// known_hosts callback about a key no host has, whose error lists the
// keys it expected.
func knownAlgorithms(files []string, host string, remote net.Addr) []string {
	var existing []string
	for _, f := range files {
		if _, err := os.Stat(expandHome(f)); err == nil {
			existing = append(existing, expandHome(f))
		}
	}
	if len(existing) == 0 {
		return nil
	}
	known, err := knownhosts.New(existing...)
	if err != nil {
		return nil
	}
	probeKeyOnce.Do(func() {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err == nil {
			probeKey, _ = ssh.NewPublicKey(pub)
		}
	})
	if probeKey == nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if !errors.As(known(host, remote, probeKey), &keyErr) || len(keyErr.Want) == 0 {
		return nil
	}
	var algos []string
	seen := map[string]bool{}
	add := func(a ...string) {
		for _, x := range a {
			if !seen[x] {
				seen[x] = true
				algos = append(algos, x)
			}
		}
	}
	for _, k := range keyErr.Want {
		switch t := k.Key.Type(); t {
		case ssh.KeyAlgoRSA:
			add(ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
		default:
			add(t)
		}
	}
	return algos
}

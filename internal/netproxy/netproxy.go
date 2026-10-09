// Package netproxy reaches servers through a SOCKS5 or an HTTP proxy, and
// forwards a local port to a server through one, for drivers that dial
// an address of their own.
package netproxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Kinds of proxy.
const (
	SOCKS5 = "socks5"
	HTTP   = "http"
)

// Config is a proxy, and who logs in to it, when it asks.
type Config struct {
	Kind           string
	Host           string
	Port           int
	User, Password string
}

const dialTimeout = 15 * time.Second

// Dial connects to addr, host:port, through the proxy.
func (p Config) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, dialTimeout)
		defer cancel()
	}
	proxyAddr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("proxy %s: %w", proxyAddr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	switch p.Kind {
	case SOCKS5:
		err = p.socks5(conn, addr)
	case HTTP:
		conn, err = p.connect(conn, addr)
	default:
		err = fmt.Errorf("unknown proxy kind %q", p.Kind)
	}
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy %s to %s: %w", proxyAddr, addr, err)
	}
	conn.SetDeadline(time.Time{})
	return conn, nil
}

// socks5 asks a SOCKS5 proxy for a connection to addr, as RFC 1928 says,
// logging in as RFC 1929 says when the proxy asks.
func (p Config) socks5(conn net.Conn, addr string) error {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return err
	}
	methods := []byte{0x00} // no login
	if p.User != "" {
		methods = append(methods, 0x02) // user and password
	}
	if _, err := conn.Write(append([]byte{0x05, byte(len(methods))}, methods...)); err != nil {
		return err
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	switch reply[1] {
	case 0x00:
	case 0x02:
		if len(p.User) > 255 || len(p.Password) > 255 {
			return errors.New("the proxy's user and password are 255 bytes at most")
		}
		login := append([]byte{0x01, byte(len(p.User))}, p.User...)
		login = append(append(login, byte(len(p.Password))), p.Password...)
		if _, err := conn.Write(login); err != nil {
			return err
		}
		if _, err := io.ReadFull(conn, reply[:]); err != nil {
			return err
		}
		if reply[1] != 0x00 {
			return errors.New("the proxy refused the user and password")
		}
	default:
		return errors.New("the proxy accepts no way of logging in the app has: give it a user and password")
	}
	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		req = append(append(req, 0x01), ip.To4()...)
	} else if ip != nil {
		req = append(append(req, 0x04), ip.To16()...)
	} else if len(host) <= 255 {
		req = append(append(req, 0x03, byte(len(host))), host...)
	} else {
		return errors.New("the host name is longer than SOCKS5 allows")
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := conn.Write(req); err != nil {
		return err
	}
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	if head[1] != 0x00 {
		return fmt.Errorf("the proxy did not connect: %s", socksReply(head[1]))
	}
	// The address the proxy connected from, which the app has no use for.
	skip := 0
	switch head[3] {
	case 0x01:
		skip = net.IPv4len
	case 0x04:
		skip = net.IPv6len
	case 0x03:
		var n [1]byte
		if _, err := io.ReadFull(conn, n[:]); err != nil {
			return err
		}
		skip = int(n[0])
	default:
		return errors.New("the proxy answered an address of an unknown type")
	}
	_, err = io.ReadFull(conn, make([]byte, skip+2))
	return err
}

func socksReply(code byte) string {
	reasons := map[byte]string{0x01: "general failure", 0x02: "not allowed by its rules", 0x03: "network unreachable",
		0x04: "host unreachable", 0x05: "connection refused", 0x06: "TTL expired", 0x07: "command not supported",
		0x08: "address type not supported"}
	if r, ok := reasons[code]; ok {
		return r
	}
	return fmt.Sprintf("error %d", code)
}

// connect asks an HTTP proxy for a tunnel to addr with CONNECT. What the
// server sends right after the proxy's answer, as MySQL's greeting, is
// read before the connection's own.
func (p Config) connect(conn net.Conn, addr string) (net.Conn, error) {
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
	if p.User != "" {
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.User+":"+p.Password)))
	}
	if err := req.Write(conn); err != nil {
		return conn, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return conn, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return conn, fmt.Errorf("the proxy answered %s", resp.Status)
	}
	return &bufferedConn{Conn: conn, r: br}, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// Forwarder is a local port whose connections go on to a server through a
// dial function, as a proxy's.
type Forwarder struct {
	listener net.Listener
	addr     string
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// Forward listens on 127.0.0.1 and connects each connection made there
// to addr with dial.
func Forward(addr string, dial func(ctx context.Context, network, addr string) (net.Conn, error)) (*Forwarder, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &Forwarder{listener: l, addr: addr, dial: dial, conns: map[net.Conn]struct{}{}}
	go f.accept()
	return f, nil
}

// LocalPort is the port connections to the server are made to.
func (f *Forwarder) LocalPort() int { return f.listener.Addr().(*net.TCPAddr).Port }

// Close stops listening, and closes the connections forwarded.
func (f *Forwarder) Close() error {
	f.mu.Lock()
	f.closed = true
	for c := range f.conns {
		c.Close()
	}
	f.mu.Unlock()
	return f.listener.Close()
}

// track keeps a connection to close with the forwarder, unless it closed.
func (f *Forwarder) track(c net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.conns[c] = struct{}{}
	return true
}

func (f *Forwarder) untrack(c net.Conn) {
	f.mu.Lock()
	delete(f.conns, c)
	f.mu.Unlock()
}

func (f *Forwarder) accept() {
	for {
		local, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.forward(local)
	}
}

func (f *Forwarder) forward(local net.Conn) {
	defer local.Close()
	remote, err := f.dial(context.Background(), "tcp", f.addr)
	if err != nil {
		return // the driver sees its connection close, and says so
	}
	defer remote.Close()
	if !f.track(local) || !f.track(remote) {
		return
	}
	defer f.untrack(local)
	defer f.untrack(remote)
	done := make(chan struct{}, 2)
	go func() { io.Copy(remote, local); done <- struct{}{} }()
	go func() { io.Copy(local, remote); done <- struct{}{} }()
	<-done // either way closing ends the other
}

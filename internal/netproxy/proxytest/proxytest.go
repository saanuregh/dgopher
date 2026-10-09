// Package proxytest runs SOCKS5 and HTTP proxies for tests.
package proxytest

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"

	"dgopher/internal/netproxy"
)

// SOCKS5 starts a SOCKS5 proxy that asks for user and pass, as long as
// the test runs.
func SOCKS5(t testing.TB, user, pass string) netproxy.Config {
	t.Helper()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				head := make([]byte, 2)
				io.ReadFull(r, head)
				io.ReadFull(r, make([]byte, head[1]))
				c.Write([]byte{5, 2})
				ver, _ := r.ReadByte()
				ul, _ := r.ReadByte()
				u := make([]byte, ul)
				io.ReadFull(r, u)
				pl, _ := r.ReadByte()
				p := make([]byte, pl)
				io.ReadFull(r, p)
				if ver != 1 || string(u) != user || string(p) != pass {
					c.Write([]byte{1, 1})
					return
				}
				c.Write([]byte{1, 0})
				req := make([]byte, 4)
				io.ReadFull(r, req)
				var host string
				switch req[3] {
				case 1:
					ip := make([]byte, 4)
					io.ReadFull(r, ip)
					host = net.IP(ip).String()
				case 3:
					n, _ := r.ReadByte()
					h := make([]byte, n)
					io.ReadFull(r, h)
					host = string(h)
				}
				portBytes := make([]byte, 2)
				io.ReadFull(r, portBytes)
				target, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))))
				if err != nil {
					c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer target.Close()
				c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				go io.Copy(target, r)
				io.Copy(c, target)
			}()
		}
	}()
	a := l.Addr().(*net.TCPAddr)
	return netproxy.Config{Kind: netproxy.SOCKS5, Host: "127.0.0.1", Port: a.Port, User: user, Password: pass}
}

// HTTP starts an HTTP proxy that tunnels CONNECT for user and pass, by
// Basic login, as long as the test runs.
func HTTP(t testing.TB, user, pass string) netproxy.Config {
	t.Helper()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)) {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		target, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		c, buf, _ := w.(http.Hijacker).Hijack()
		io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
		go func() { io.Copy(target, buf); target.Close() }()
		io.Copy(c, target)
		c.Close()
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return netproxy.Config{Kind: netproxy.HTTP, Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, User: user, Password: pass}
}

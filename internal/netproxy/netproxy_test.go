package netproxy_test

import (
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"dgopher/internal/netproxy"
	"dgopher/internal/netproxy/proxytest"
)

// greeter is a server that speaks first, as MySQL does, then echoes.
func greeter(t *testing.T) string {
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
				io.WriteString(c, "hello;")
				io.Copy(c, c)
			}()
		}
	}()
	return l.Addr().String()
}

// talk reads the greeting, sends a line and reads it back.
func talk(t *testing.T, c net.Conn) {
	t.Helper()
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len("hello;"))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "hello;" {
		t.Fatalf("greeting %q %v", got, err)
	}
	io.WriteString(c, "ping")
	got = make([]byte, 4)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "ping" {
		t.Fatalf("echo %q %v", got, err)
	}
}

func TestProxies(t *testing.T) {
	target := greeter(t)
	for _, p := range []netproxy.Config{proxytest.SOCKS5(t, "u", "pw"), proxytest.HTTP(t, "p", "qs")} {
		t.Run(p.Kind, func(t *testing.T) {
			c, err := p.Dial(context.Background(), "tcp", target)
			if err != nil {
				t.Fatal(err)
			}
			talk(t, c)
			f, err := netproxy.Forward(target, p.Dial)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			local, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(f.LocalPort())))
			if err != nil {
				t.Fatal(err)
			}
			talk(t, local)
			wrong := p
			wrong.Password = "nope"
			if _, err := wrong.Dial(context.Background(), "tcp", target); err == nil {
				t.Fatal("a wrong password got through")
			} else if !strings.Contains(err.Error(), "proxy") {
				t.Fatalf("error %v", err)
			}
		})
	}
}

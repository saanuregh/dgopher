package redis

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// fakeRedis is a server speaking RESP2 that answers each command as its
// handler says, for tests of what the tab sends and shows without a
// real Redis.
type fakeRedis struct {
	addr string
	// readOnly are the commands COMMAND lists as read only.
	readOnly []string
	version  string

	mu     sync.Mutex
	handle func(args []string) any
	sent   [][]string
}

// fakeStatus is a simple string reply, +OK.
type fakeStatus string

// unhandled lets a handler leave a command to the server's defaults.
type unhandled struct{}

func newFakeRedis(t *testing.T, handle func(args []string) any) *fakeRedis {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRedis{addr: l.Addr().String(), handle: handle, version: "7.4.0"}
	var conns sync.WaitGroup
	var openMu sync.Mutex
	open := map[net.Conn]bool{}
	t.Cleanup(func() {
		l.Close()
		openMu.Lock()
		for c := range open {
			c.Close()
		}
		openMu.Unlock()
		conns.Wait()
	})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			openMu.Lock()
			open[c] = true
			openMu.Unlock()
			conns.Add(1)
			go func() {
				defer conns.Done()
				f.serve(c)
			}()
		}
	}()
	return f
}

// Sent returns the commands sent so far, but for the client's handshake.
func (f *fakeRedis) Sent() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.sent...)
}

// SentNamed returns the commands sent named name.
func (f *fakeRedis) SentNamed(name string) [][]string {
	var out [][]string
	for _, args := range f.Sent() {
		if strings.EqualFold(args[0], name) {
			out = append(out, args)
		}
	}
	return out
}

func (f *fakeRedis) serve(c net.Conn) {
	defer c.Close()
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	for {
		args, err := readCommand(r)
		if err != nil {
			return
		}
		f.mu.Lock()
		handle := f.handle
		switch strings.ToUpper(args[0]) {
		case "CLIENT", "SELECT", "AUTH", "COMMAND", "PING", "HELLO":
		default:
			f.sent = append(f.sent, args)
		}
		f.mu.Unlock()
		var reply any = unhandled{}
		if handle != nil {
			reply = handle(args)
		}
		if _, ok := reply.(unhandled); ok {
			reply = f.defaultReply(args)
		}
		writeReply(w, reply)
		if r.Buffered() == 0 {
			if w.Flush() != nil {
				return
			}
		}
	}
}

func (f *fakeRedis) defaultReply(args []string) any {
	switch strings.ToUpper(args[0]) {
	case "PING":
		return fakeStatus("PONG")
	case "CLIENT", "SELECT", "AUTH":
		return fakeStatus("OK")
	case "COMMAND":
		var out []any
		for _, name := range f.readOnly {
			out = append(out, []any{strings.ToLower(name), int64(-2), []any{fakeStatus("readonly")}})
		}
		return out
	case "INFO":
		return "# Server\r\nredis_version:" + f.version + "\r\n"
	case "DBSIZE":
		return int64(0)
	case "MEMORY":
		return nil
	}
	return fmt.Errorf("ERR unknown command '%s'", args[0])
}

func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return nil, errors.New("not an array")
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil || n < 1 {
		return nil, errors.New("bad array")
	}
	args := make([]string, n)
	for i := range args {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimSpace(line[1:]))
		if err != nil {
			return nil, err
		}
		b := make([]byte, size+2)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		args[i] = string(b[:size])
	}
	return args, nil
}

func writeReply(w *bufio.Writer, v any) {
	switch v := v.(type) {
	case nil:
		w.WriteString("$-1\r\n")
	case fakeStatus:
		w.WriteString("+" + string(v) + "\r\n")
	case error:
		w.WriteString("-" + v.Error() + "\r\n")
	case int:
		fmt.Fprintf(w, ":%d\r\n", v)
	case int64:
		fmt.Fprintf(w, ":%d\r\n", v)
	case string:
		fmt.Fprintf(w, "$%d\r\n%s\r\n", len(v), v)
	case []string:
		fmt.Fprintf(w, "*%d\r\n", len(v))
		for _, e := range v {
			writeReply(w, e)
		}
	case []any:
		fmt.Fprintf(w, "*%d\r\n", len(v))
		for _, e := range v {
			writeReply(w, e)
		}
	default:
		panic(fmt.Sprintf("fake redis: no reply for %T", v))
	}
}

// openFake opens a connected Redis tab on a fake server in env.
func openFake(t *testing.T, f *fakeRedis, env db.Environment) (*dataview.FakeHost, *ui.Tester, *connection.Conn, *Tab) {
	t.Helper()
	host, port, _ := net.SplitHostPort(f.addr)
	p, _ := strconv.Atoi(port)
	a := dataview.NewFakeHost(t)
	cn := a.AddConn(db.Config{ID: "r", Name: "Cache", Engine: db.Redis, Host: host, Port: p, Env: env})
	tt := ui.NewTester(a.View, 1360, 860)
	a.Connect(cn, func() { a.AddTab(New(a, cn)) })
	testutil.WaitFor(t, tt, "redis tab", func() bool { return len(a.Tabs) == 1 })
	return a, tt, cn, a.Tabs[0].(*Tab)
}

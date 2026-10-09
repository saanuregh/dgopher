package db

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/rueidis"
)

// MonitorLine is a command a server ran, as MONITOR tells it.
type MonitorLine struct {
	Node   string // the server's address
	At     time.Time
	DB     string // the database index it ran on
	Client string // the client's address, or "lua" for a script's
	// Command is the command with its arguments, quoted as the server
	// quotes them; Redis leaves secrets, as AUTH's, out.
	Command string
}

// monitorAddrs are the addresses of the servers holding the keys: a
// cluster's masters, or the one server.
func (k *KV) monitorAddrs(ctx context.Context) ([]string, error) {
	nodes := k.Client.Nodes()
	if k.Client.Mode() != rueidis.ClientModeCluster {
		addrs := make([]string, 0, len(nodes))
		for addr := range nodes {
			addrs = append(addrs, addr)
		}
		sort.Strings(addrs)
		return addrs[:min(len(addrs), 1)], nil
	}
	masters, err := k.mastersFor(ctx, false)
	if err != nil {
		return nil, err
	}
	var addrs []string
	for addr, c := range nodes {
		for _, m := range masters {
			if c == m {
				addrs = append(addrs, addr)
			}
		}
	}
	sort.Strings(addrs)
	return addrs, nil
}

// Monitor tells line each command the servers holding the keys run, as
// MONITOR does, on connections of its own, until ctx ends or a server
// fails. Every command the server runs costs it more while it does.
func (k *KV) Monitor(ctx context.Context, line func(MonitorLine)) error {
	addrs, err := k.monitorAddrs(ctx)
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return errors.New("no server to monitor")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, len(addrs))
	for _, addr := range addrs {
		wg.Go(func() {
			if err := k.monitorNode(ctx, addr, line); err != nil && ctx.Err() == nil {
				errs <- fmt.Errorf("%s: %w", addr, err)
				cancel() // one failing stops the others
			}
		})
	}
	wg.Wait()
	select {
	case err := <-errs:
		return redact(err, k.Config)
	default:
		return nil
	}
}

func (k *KV) monitorNode(ctx context.Context, addr string, line func(MonitorLine)) error {
	conn, err := k.dial(ctx, addr)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()
	r := bufio.NewReader(conn)
	// As the client logs in: as its user, whose password may be none.
	if user, pw := k.Config.User, k.Config.Password; user != "" || pw != "" {
		auth := []string{"AUTH", pw}
		if user != "" {
			auth = []string{"AUTH", user, pw}
		}
		if err := sendCommand(conn, r, auth); err != nil {
			return err
		}
	}
	if err := sendCommand(conn, r, []string{"MONITOR"}); err != nil {
		return err
	}
	for {
		text, err := readLine(r, monitorLineMax)
		if err != nil {
			return err
		}
		if strings.HasPrefix(text, "-") {
			return errors.New(text[1:])
		}
		if l, ok := parseMonitorLine(strings.TrimPrefix(text, "+")); ok {
			l.Node = addr
			line(l)
		}
	}
}

// monitorLineMax is how much of a command MONITOR tells is kept: one
// writing a large value would hold it whole.
const monitorLineMax = 4 << 10

// readLine reads a line without its end, keeping its first max bytes.
func readLine(r *bufio.Reader, max int) (string, error) {
	var b []byte
	for {
		part, more, err := r.ReadLine()
		if err != nil {
			return "", err
		}
		if room := max - len(b); room > 0 {
			b = append(b, part[:min(room, len(part))]...)
		}
		if !more {
			return string(b), nil
		}
	}
}

// sendCommand writes a command and reads its simple reply: OK, or the
// server's error.
func sendCommand(conn net.Conn, r *bufio.Reader, args []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return err
	}
	reply, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	if reply, ok := strings.CutPrefix(strings.TrimRight(reply, "\r\n"), "-"); ok {
		return fmt.Errorf("%s: %s", args[0], reply)
	}
	return nil
}

// parseMonitorLine reads a line of MONITOR: a time in seconds, the
// database and the client in brackets, then the command.
func parseMonitorLine(s string) (MonitorLine, bool) {
	at, rest, ok := strings.Cut(s, " [")
	if !ok {
		return MonitorLine{}, false
	}
	where, command, ok := strings.Cut(rest, "] ")
	if !ok {
		return MonitorLine{}, false
	}
	secs, err := strconv.ParseFloat(at, 64)
	if err != nil {
		return MonitorLine{}, false
	}
	db, client, _ := strings.Cut(where, " ")
	whole := int64(secs)
	return MonitorLine{At: time.Unix(whole, int64((secs-float64(whole))*1e9)), DB: db, Client: client, Command: command}, true
}

package db

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// A server that accepts the connection but never answers must not hold
// OpenRedis past its caller's context.
func TestOpenRedisStopsWithContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = OpenRedis(ctx, Config{Name: "r", Engine: Redis, Host: "127.0.0.1", Port: port}, nil)
	if err == nil {
		t.Fatal("connected to a silent server")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("OpenRedis took %v after its context ended", d)
	}
}

func TestSplitCommands(t *testing.T) {
	cmds, err := SplitCommands("# seed\nSET a 1\n\n  HSET h f \"two words\"\r\n")
	if err != nil || len(cmds) != 2 || cmds[1].Line != 4 || cmds[1].Args[3] != "two words" {
		t.Fatalf("%+v %v", cmds, err)
	}
	if _, err := SplitCommands("SET a 1\nSET \"b 2\n"); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("unbalanced quotes: %v", err)
	}
}

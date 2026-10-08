package db

import (
	"context"
	"net"
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

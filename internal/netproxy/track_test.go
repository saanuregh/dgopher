package netproxy

import (
	"context"
	"net"
	"sync"
	"testing"
)

// A connection forwarded as the forwarder closes is forgotten when its
// forward ends, though its other end came too late to be kept.
func TestForwardRacingClose(t *testing.T) {
	for i := 0; i < 500; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		dial := func(context.Context, string, string) (net.Conn, error) {
			remote, other := net.Pipe()
			other.Close()
			return remote, nil
		}
		f := &Forwarder{listener: l, addr: "server", dial: dial, conns: map[net.Conn]struct{}{}}
		var wg sync.WaitGroup
		for range 20 {
			local, other := net.Pipe()
			defer other.Close()
			wg.Add(1)
			go func() { defer wg.Done(); f.forward(local) }()
		}
		f.Close()
		wg.Wait()
		f.mu.Lock()
		n := len(f.conns)
		f.mu.Unlock()
		if n != 0 {
			t.Fatalf("iteration %d: %d connections still kept after their forwards ended", i, n)
		}
	}
}

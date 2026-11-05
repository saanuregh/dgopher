package sshtunnel

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
)

// closeCounted is a connection that counts its closes.
type closeCounted struct {
	net.Conn
	closed atomic.Bool
}

func (c *closeCounted) Close() error { c.closed.Store(true); return nil }

// A connection tracked while the tunnel stops is closed by the stop or
// refused: none is kept open after the tunnel is done.
func TestTrackRacingStop(t *testing.T) {
	s := startServer(t, nil)
	echoHost, echoPort := startEcho(t)
	base, err := Open(context.Background(), config(s), echoHost, echoPort, []string{trustedFile(t, s)})
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	for i := 0; i < 2000; i++ {
		tn := &Tunnel{client: base.client, done: make(chan struct{}), conns: make(map[net.Conn]struct{})}
		var kept []*closeCounted
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			for {
				c := &closeCounted{}
				if !tn.track(c) {
					return
				}
				kept = append(kept, c)
			}
		}()
		tn.stop(nil)
		<-finished
		for _, c := range kept {
			if !c.closed.Load() {
				t.Fatalf("iteration %d: a connection tracked as the tunnel stopped was left open", i)
			}
		}
	}
}

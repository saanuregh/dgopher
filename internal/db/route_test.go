package db

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"dgopher/internal/sshtunnel"
	"dgopher/internal/sshtunnel/sshtest"
)

// TestRouteDoneWhenSSHServerStops checks that a pool learns its SSH tunnel
// ended, and why, when the SSH server goes away.
func TestRouteDoneWhenSSHServerStops(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Close() })

	s := sshtest.Start(t, "secret", nil)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	if err := sshtunnel.Trust(knownHosts, s.Addr, s.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Engine: Postgres,
		Host:   "127.0.0.1",
		Port:   target.Addr().(*net.TCPAddr).Port,
		SSH:    SSHConfig{Enabled: true, Host: s.Host, Port: s.Port, User: "tester", Password: "secret"},
	}
	_, r, err := openRoute(context.Background(), &cfg, []string{knownHosts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	d := &DB{route: r}

	select {
	case <-d.RouteDone():
		t.Fatalf("route ended before the server stopped: %v", d.RouteErr())
	default:
	}
	if err := d.RouteErr(); err != nil {
		t.Fatalf("RouteErr while running = %v, want nil", err)
	}

	s.Stop()
	select {
	case <-d.RouteDone():
	case <-time.After(10 * time.Second):
		t.Fatal("RouteDone did not close after the SSH server stopped")
	}
	if kind := ClassifyConnectError(d.RouteErr()); kind != ProblemTunnelClosed {
		t.Fatalf("ClassifyConnectError(%v) = %v, want ProblemTunnelClosed", d.RouteErr(), kind)
	}
}

// TestNoRouteDoneWithoutTunnel checks that a pool without a route reports
// none.
func TestNoRouteDoneWithoutTunnel(t *testing.T) {
	d := &DB{}
	if d.RouteDone() != nil {
		t.Fatal("RouteDone without a route is not nil")
	}
	if err := d.RouteErr(); err != nil {
		t.Fatalf("RouteErr without a route = %v, want nil", err)
	}
}

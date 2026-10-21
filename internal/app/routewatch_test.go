package app

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/sshtunnel"
	"dgopher/internal/sshtunnel/sshtest"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// A tunnel that ends under a connected connection fails it, tells the
// editor its transaction was rolled back and audits that, and offers to
// connect again; the editor stays.
func TestTunnelLossFailsConnectionAndOffersReconnect(t *testing.T) {
	a, tt, q, _ := txApp(t, 0)
	cn := q.Conn
	notesBefore := len(a.notes)
	done := make(chan struct{})
	a.watchRoute(cn, cn.Generation, done, func() error { return errors.New("keepalive timed out") })
	close(done)
	testutil.WaitFor(t, tt, "the connection to fail", func() bool { return cn.Status == connection.StatusFailed })
	if cn.ErrKind != db.ProblemTunnelClosed || !strings.Contains(cn.Err, "SSH tunnel closed: keepalive timed out") {
		t.Fatalf("kind %v, err %q", cn.ErrKind, cn.Err)
	}
	if q.OpenTx() {
		t.Fatal("the editor still shows its transaction open")
	}
	var rolledBack, disconnected bool
	for _, e := range auditEvents(t, a) {
		rolledBack = rolledBack || e.Kind == audit.KindStatement && e.Statement == "ROLLBACK"
		disconnected = disconnected || e.Kind == audit.KindDisconnect && strings.Contains(e.Detail, "SSH tunnel closed")
	}
	if !rolledBack || !disconnected {
		t.Fatalf("audit: rollback %v, disconnect %v: %+v", rolledBack, disconnected, auditEvents(t, a))
	}
	if !a.holds(q) {
		t.Fatal("the editor closed")
	}
	testutil.WaitFor(t, tt, "the alert", func() bool {
		return a.frontAlert() != nil && a.frontAlert().report.Title == "Lost lite" && tt.HasText("Reconnect")
	})
	added := a.notes[notesBefore:]
	if len(added) != 1 || !added[0].isError || added[0].title != "Lost lite" {
		t.Fatalf("notes added: %+v", added)
	}
	n := added[0]
	if !strings.Contains(n.detail, cn.Err) || n.hint != db.ProblemTunnelClosed.Hint() || !strings.Contains(n.detail, q.Title()) {
		t.Fatalf("note detail %q", n.detail)
	}
	labels := []string{}
	for _, act := range n.actions {
		labels = append(labels, act.Label)
	}
	if !slices.Equal(labels, []string{"Edit Connection", "Reconnect"}) {
		t.Fatalf("note actions %q", labels)
	}
	// Reconnect from Failed connects again.
	n.actions[1].Run()
	testutil.WaitFor(t, tt, "the reconnect", func() bool { return cn.Status == connection.StatusConnected })
}

// A tunnel that ends after its connection was disconnected, edited or
// connected again changes nothing.
func TestStaleTunnelLossIgnored(t *testing.T) {
	a, tt, q, _ := txApp(t, 0)
	cn := q.Conn
	notesBefore, eventsBefore := len(a.notes), len(auditEvents(t, a))
	done := make(chan struct{})
	a.watchRoute(cn, cn.Generation, done, func() error { return errors.New("keepalive timed out") })
	cn.Generation++ // as a disconnect and connect again do
	close(done)
	for range 10 {
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	if cn.Status != connection.StatusConnected || cn.Err != "" || !q.OpenTx() {
		t.Fatalf("status %v, err %q, tx %v", cn.Status, cn.Err, q.OpenTx())
	}
	if len(a.notes) != notesBefore || len(auditEvents(t, a)) != eventsBefore {
		t.Fatalf("notes %+v", a.notes[notesBefore:])
	}
}

// A connection through an SSH server fails when the server goes.
func TestIntegrationTunnelLoss(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	s := sshtest.Start(t, "secret", nil)
	a.appKnownHosts = filepath.Join(t.TempDir(), "known_hosts")
	if err := sshtunnel.Trust(a.appKnownHosts, s.Addr, s.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	cfg := testutil.PGConfig()
	cfg.SSH = db.SSHConfig{Enabled: true, Host: s.Host, Port: s.Port, User: "tester", Password: "secret"}
	cn := addConn(a, cfg)
	tt := ui.NewTester(a.view, 1000, 700)
	connectNow(t, a, tt, cn)
	if cn.DB.RouteDone() == nil {
		t.Fatal("connected without the tunnel")
	}
	s.Stop()
	testutil.WaitFor(t, tt, "the connection to fail", func() bool { return cn.Status == connection.StatusFailed })
	if cn.ErrKind != db.ProblemTunnelClosed {
		t.Fatalf("kind %v, err %q", cn.ErrKind, cn.Err)
	}
}

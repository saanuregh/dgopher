package widgets_test

import (
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The bar says what its connection is at, with the button that goes on:
// Connect, Retry after a failure (with why), Cancel once connecting lasts.
func TestConnectBar(t *testing.T) {
	cn := &connection.Conn{Config: db.Config{Name: "prod"}, Status: connection.StatusIdle}
	var s widgets.ConnectState
	connects, cancels := 0, 0
	tt := ui.NewTester(func(c *ui.Context) {
		connect, cancel := widgets.ConnectBar(c, cn, s)
		if connect {
			connects++
		}
		if cancel {
			cancels++
		}
	}, 600, 80)
	tt.Frame()
	if !tt.HasText("Not connected.") {
		t.Fatalf("idle: %q", tt.Texts())
	}
	testutil.Settled(t, tt, "Connect") // it fades in
	tt.Click("Connect")
	if connects != 1 || cancels != 0 {
		t.Fatalf("Connect: %d connects, %d cancels", connects, cancels)
	}

	cn.Status, cn.Err = connection.StatusFailed, "password authentication failed"
	s.Default = true
	tt.Frame()
	if !tt.HasText("Not connected: password authentication failed") || !tt.HasText("Retry (↵)") {
		t.Fatalf("failed: %q", tt.Texts())
	}

	cn.Status = connection.StatusConnecting
	s = widgets.ConnectState{Connecting: true, Cancellable: true, Since: time.Now(), Default: true}
	tt.Frame()
	if !tt.HasText("Connecting to prod…") || tt.HasText("Cancel") {
		t.Fatalf("connecting at once: %q", tt.Texts())
	}
	s.Since = time.Now().Add(-time.Second)
	testutil.Settled(t, tt, "Cancel")
	tt.Click("Cancel")
	if cancels != 1 || connects != 1 {
		t.Fatalf("Cancel: %d connects, %d cancels", connects, cancels)
	}
}

// Offline, a network engine's bar says so, Connect still there; a file's
// does not.
func TestConnectBarShowsOffline(t *testing.T) {
	cn := &connection.Conn{Config: db.Config{Name: "prod", Engine: db.Postgres}, Status: connection.StatusIdle}
	s := widgets.ConnectState{Offline: true}
	tt := ui.NewTester(func(c *ui.Context) { widgets.ConnectBar(c, cn, s) }, 600, 80)
	tt.Frame()
	if !tt.HasText("Offline: Connect works again once the computer is online.") {
		t.Fatalf("offline: %q", tt.Texts())
	}
	testutil.Settled(t, tt, "Connect")

	cn.Config.Engine = db.SQLite
	tt.Frame()
	if !tt.HasText("Not connected.") {
		t.Fatalf("a file offline: %q", tt.Texts())
	}
}

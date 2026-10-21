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

// The bar has the button that goes on, the toolbar's: Connect, Retry after
// a failure, Cancel once connecting lasts; and says what its connection is
// at, with why it failed.
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
	if err := tt.Click("Connect"); err != nil {
		t.Fatal(err)
	}
	if connects != 1 || cancels != 0 {
		t.Fatalf("Connect: %d connects, %d cancels", connects, cancels)
	}

	cn.Status, cn.Err = connection.StatusFailed, "password authentication failed"
	s.Default = true
	tt.Frame()
	if !tt.HasText("Not connected: password authentication failed") || !tt.HasText("Retry") || !tt.HasText(widgets.KeyLabel("↵")) {
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
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
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

// The toolbar's button changes in place as the connection does, never its
// width: Connect, still Connect while connecting, Cancel once that lasts,
// Disconnect once connected, Retry after a failure beside a warning named
// with why.
func TestConnectStatus(t *testing.T) {
	cn := &connection.Conn{Config: db.Config{Name: "prod", Engine: db.Postgres}, Status: connection.StatusIdle}
	var s widgets.ConnectState
	var asked []widgets.ConnectAction
	// In a row, as a toolbar's, where it takes its own width.
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Children(func() {
			if a := widgets.ConnectStatus(c, cn, s); a != widgets.NoConnectAction {
				asked = append(asked, a)
			}
		})
	}, 600, 40)
	// As wide as a toolbar button showing its widest label.
	widest := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Children(func() { widgets.ToolButton(c, widgets.IconUnplug, "Disconnect", "") })
	}, 600, 40)
	widest.Frame()
	r, _ := widest.Find("Disconnect")
	width := r.W
	wide := func(label string) {
		t.Helper()
		if r, ok := tt.Find(label); !ok || r.W != width {
			t.Errorf("%s is %v wide (%v), want %v", label, r.W, ok, width)
		}
	}
	click := func(label string, want widgets.ConnectAction) {
		t.Helper()
		wide(label)
		asked = nil
		if err := tt.Click(label); err != nil || len(asked) != 1 || asked[0] != want {
			t.Fatalf("%s: %v, asked %v", label, err, asked)
		}
	}
	tt.Frame()
	click("Connect", widgets.ConnectAsked)

	cn.Status, cn.Err = connection.StatusFailed, "password authentication failed\nmore detail"
	tt.Frame()
	if _, ok := tt.Find("Connection failed: password authentication failed"); !ok {
		t.Fatalf("failed: %q", tt.Texts())
	}
	click("Retry", widgets.ConnectAsked)

	cn.Status = connection.StatusConnecting
	s = widgets.ConnectState{Connecting: true, Cancellable: true, Since: time.Now()}
	tt.Frame()
	wide("Connect")
	asked = nil
	if err := tt.Click("Connect"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Connect") || tt.HasText("Cancel") || len(asked) != 0 {
		t.Fatalf("connecting at once: asked %v, %q", asked, tt.Texts())
	}
	s.Since = time.Now().Add(-time.Second)
	testutil.Settled(t, tt, "Cancel")
	click("Cancel", widgets.CancelAsked)

	cn.Status, s = connection.StatusConnected, widgets.ConnectState{}
	testutil.Settled(t, tt, "Disconnect")
	click("Disconnect", widgets.DisconnectAsked)
}

package widgets

import (
	"time"

	"dgopher/internal/connection"

	"github.com/egoist/mygo/ui"
)

// ConnectState is what a ConnectBar shows beyond its connection's status.
type ConnectState struct {
	// Connecting is set while the connection connects, or what waits for
	// it opens.
	Connecting bool
	// Cancellable offers Cancel once connecting has gone on since Since.
	Cancellable bool
	Since       time.Time
	// Default makes the button the view's own: in focus as it appears,
	// its label naming ↵.
	Default bool
	// Offline is set while the system says the computer is offline;
	// Connect stays, as the system may be wrong.
	Offline bool
}

// ConnectBar says that cn is not connected, or why it failed, with the
// button that connects it, Connect or Retry; while connecting, it says so,
// with Cancel once that lasts. It reports which was clicked.
func ConnectBar(c *ui.Context, cn *connection.Conn, s ConnectState) (connect, cancel bool) {
	t := c.Theme()
	failed := cn.Status == connection.StatusFailed && !s.Connecting
	row := ui.Row(c.Key("connect-bar")).Padding(6, 12).Gap(10)
	if failed {
		row.Background(t.Danger.Alpha(0.14))
	} else {
		row.Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
	}
	AppearBar(row).Children(func() {
		label, button := "Not connected.", "Connect"
		if failed {
			ui.Icon(c, IconAlert).TextColor(t.Danger).FontSize(14)
			label, button = "Not connected: "+FirstLine(cn.Err), "Retry"
		}
		if s.Offline && !failed && !cn.Config.Engine.IsFile() {
			label = "Offline: Connect works again once the computer is online."
		}
		if s.Connecting {
			label = "Connecting to " + cn.Config.Name + "…"
		}
		ui.Text(c, label).Grow(1).Shrink(1).SingleLine()
		// Cancel is a button of its own, fading in as Connect fades out:
		// Connect, in focus, would carry the focus to it, and a second ↵
		// would cancel the connect it began.
		if s.Connecting && s.Cancellable && Lasted(c, s.Since) {
			cancel = Appear(ui.Button(c.Key("cancel"), "Cancel")).Clicked()
			return
		}
		if s.Default {
			button += " (↵)"
		}
		b := Appear(MorphButton(c.Key("connect"), button)).Disabled(s.Connecting)
		if s.Default {
			b.AutoFocus()
		}
		connect = b.Clicked()
	})
	return connect, cancel
}

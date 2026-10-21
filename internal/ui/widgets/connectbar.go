package widgets

import (
	"strings"
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

// ConnectBar is the bar of a view waiting for cn, as a toolbar: the
// button an editor's toolbar has first, which connects it, Retry after a
// failure, Details beside it, Cancel once connecting lasts, then what the
// connection is at. It reports which was clicked.
func ConnectBar(c *ui.Context, cn *connection.Conn, s ConnectState) (asked ConnectAction) {
	t := c.Theme()
	failed := cn.Status == connection.StatusFailed && !s.Connecting
	// A toolbar's look, its button in one, its message beside it: in the
	// toolbar, a long message, as why it failed, would go out of sight
	// into its overflow menu.
	ui.Row(c.Key("connect-bar")).Padding(4, 8).Gap(4).AlignItems(ui.Center).FontSize(ToolFontSize).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Toolbar(c, func() {
			if a := ConnectStatus(c, cn, s); a != DisconnectAsked {
				asked = a
			}
			// The key the view takes for the button, while it does.
			if s.Default && !s.Connecting {
				Keycap(c, "↵")
			}
		}).Label("Connection").Shrink(0)
		ui.Divider(c).Margin(0, 4)
		label, color := "Not connected.", PaletteOf(c).Muted
		switch {
		case s.Connecting:
			label = "Connecting to " + cn.Config.Name + "…"
		case failed:
			label, color = "Not connected: "+FailureText(cn), t.Danger
		case s.Offline && !cn.Config.Engine.IsFile():
			label = "Offline: Connect works again once the computer is online."
		}
		ui.Text(c, label).TextColor(color).Grow(1).Shrink(1).SingleLine().Tooltip(label)
	})
	return asked
}

// ConnectAction is what a ConnectStatus button asked.
type ConnectAction int

const (
	NoConnectAction ConnectAction = iota
	ConnectAsked
	CancelAsked
	DisconnectAsked
	// ConnectDetails asks for the whole error of a failed connection.
	ConnectDetails
)

// FailureText is a failed connection's reason in a line: the hint of its
// kind of failure, else the error's first line.
func FailureText(cn *connection.Conn) string {
	if h := cn.ErrKind.Hint(); h != "" {
		return h
	}
	return FirstLine(cn.Err)
}

// ConnectStatus is the connection's button of a toolbar, one that changes
// in place as the connection does, never its width, so that nothing beside
// it moves: Connect, with a spinner while connecting, then Cancel once
// that lasts; Disconnect once connected; Retry after a failure, before a
// warning whose tooltip says why and Details, which asks for the whole
// error.
func ConnectStatus(c *ui.Context, cn *connection.Conn, s ConnectState) ConnectAction {
	// Cancel is a button of its own: Connect, in focus, would carry the
	// focus to it, and a second ↵ would cancel the connect it began. A
	// Default Connect or Retry takes the focus as it appears.
	button := func(ic *ui.SVG, label, tip string, a ConnectAction) ConnectAction {
		key := "connect"
		if a == CancelAsked {
			key = "cancel"
		}
		b := connectButton(c.Key(key), ic, label, tip)
		if s.Default && a == ConnectAsked {
			b.AutoFocus()
		}
		if b.Clicked() {
			return a
		}
		return NoConnectAction
	}
	switch {
	case cn.Status == connection.StatusConnected && !s.Connecting:
		return button(IconUnplug, "Disconnect", "Disconnect from "+cn.Config.Name, DisconnectAsked)
	case s.Connecting && s.Cancellable && Lasted(c, s.Since):
		return button(IconX, "Cancel", "Stop connecting to "+cn.Config.Name, CancelAsked)
	case s.Connecting:
		connectButton(c.Key("connect"), nil, "Connect", "Connecting to "+cn.Config.Name+"…").Disabled(true)
		return NoConnectAction
	case cn.Status == connection.StatusFailed:
		// The warning after the button, which stays where Connect was.
		why := "Connection failed: " + FailureText(cn)
		tip := why
		if !strings.HasSuffix(tip, ".") && !strings.HasSuffix(tip, "?") {
			tip += "."
		}
		a := button(IconRefresh, "Retry", tip+" Connect again.", ConnectAsked)
		ui.Text(c, FailureText(cn)).FontSize(12).TextColor(c.Theme().Danger).SingleLine().Shrink(1).MinWidth(0).Label(why).Tooltip(cn.Err)
		if ToolButton(c.Key("details"), IconInfo, "Details", "Show why the connection failed").Clicked() {
			a = ConnectDetails
		}
		return a
	}
	tip := "Connect to " + cn.Config.Name
	if s.Offline && !cn.Config.Engine.IsFile() {
		tip = "Offline: Connect works again once the computer is online."
	}
	return button(IconPlug, "Connect", tip, ConnectAsked)
}

// connectButton is a ToolButton, a spinner for a nil icon, as wide as its
// widest label, Disconnect, whatever it shows: what it shows is centered
// over that, which takes the room unseen.
func connectButton(c *ui.Context, ic *ui.SVG, label, tip string) ui.Element {
	b := ui.Button(c, "").Label(label).Tooltip(tip)
	b.Children(func() {
		ui.Row(c).Gap(5).Invisible().Children(func() {
			ui.Box(c).Size(ToolIconSize, ToolIconSize)
			ui.Text(c, "Disconnect").FontSize(ToolFontSize)
		})
		ui.Row(c).Absolute().Left(0).Right(0).Top(0).Bottom(0).Gap(5).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			if ic == nil {
				// A spinner takes more than its font size: in the icon's room.
				ui.Box(c).Size(ToolIconSize, ToolIconSize).Center().Children(func() { ui.Spinner(c).FontSize(9.5) })
			} else {
				ui.Icon(c, ic).FontSize(ToolIconSize).ContentTransition(ui.ContentTransition{})
			}
			ui.Text(c, label).FontSize(ToolFontSize).ContentTransition(ui.ContentTransition{})
		})
	})
	return b
}

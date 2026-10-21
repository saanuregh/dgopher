package app

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"dgopher/internal/redact"
	"dgopher/internal/ui/widgets"
)

// notesKept is how many notifications the session keeps, the newest.
const notesKept = 200

// notification is an alert, toast or logged line the session saw, listed
// behind the title bar's bell.
type notification struct {
	at      time.Time
	isError bool
	title   string
	hint    string // plain-language cause, may be ""
	detail  string
	actions []widgets.ErrorAction
	seq     int  // numbers the session's notifications from 1
	seen    bool // an error shown in the list or by Show Latest Error
}

// note records n, on the main thread.
func (a *App) note(n notification) {
	a.noteSeq++
	n.seq = a.noteSeq
	if n.at.IsZero() {
		n.at = time.Now()
	}
	a.notes = append(a.notes, n)
	if over := len(a.notes) - notesKept; over > 0 {
		a.notes = append(a.notes[:0], a.notes[over:]...)
	}
}

// unread counts the errors not seen yet.
func (a *App) unread() int {
	n := 0
	for _, note := range a.notes {
		if note.isError && !note.seen {
			n++
		}
	}
	return n
}

// noteWriter turns the standard logger's lines into error notifications,
// redacted; Write runs on any goroutine and only posts to the main thread.
type noteWriter struct{ a *App }

func (w noteWriter) Write(p []byte) (int, error) {
	line := redact.Secrets(strings.TrimRight(string(p), "\n"))
	at := time.Now()
	w.a.Post(func() { w.a.note(notification{at: at, isError: true, title: "Logged", detail: line}) })
	return len(p), nil
}

// captureLog sends the standard logger's lines to stderr and the
// notifications; restore puts the logger's output back.
func (a *App) captureLog() (restore func()) {
	prev := log.Writer()
	log.SetOutput(io.MultiWriter(os.Stderr, noteWriter{a}))
	return func() { log.SetOutput(prev) }
}

// notificationsButton is the title bar's bell, with the count of unread
// errors, opening the list of notifications.
func (a *App) notificationsButton(c *ui.Context) {
	bell := widgets.IconButton(c, widgets.IconBell, "Notifications")
	if bell.Clicked() {
		a.toggleNotifications(false)
	}
	if a.notesShown {
		for i := range a.notes {
			a.notes[i].seen = true
		}
	}
	if unread := a.unread(); unread > 0 {
		n := fmt.Sprint(unread)
		if unread > 99 {
			n = "99+"
		}
		ui.Badge(c, n)
	}
	panel := ui.Popover(c, bell, &a.notesShown, func() { a.notificationsList(c) })
	if a.notesShown {
		a.notificationKeys(panel)
	}
}

// notificationKeys moves the highlighted row with the arrows and shows it
// whole with Enter; the popover closes itself with Escape.
func (a *App) notificationKeys(panel ui.Element) {
	step := 0
	if panel.OverlayShortcut(0, ui.KeyDown) {
		step = 1
	} else if panel.OverlayShortcut(0, ui.KeyUp) {
		step = -1
	}
	if step != 0 && len(a.notes) > 0 {
		// Rows show newest first: the last note is row 0.
		row := -1
		for i, n := range a.notes {
			if n.seq == a.noteCursor {
				row = len(a.notes) - 1 - i
			}
		}
		row = min(max(row+step, 0), len(a.notes)-1)
		a.noteCursor = a.notes[len(a.notes)-1-row].seq
	}
	if a.noteCursor != 0 && panel.OverlayShortcut(0, ui.KeyEnter) {
		a.noteOpen, a.noteDetails = a.noteCursor, false
	}
}

// notificationsList lists the notifications, newest first; clicking one
// shows it whole.
func (a *App) notificationsList(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	list := ui.Column(c).Width(420).Gap(4)
	if a.notesByKey {
		// Opened by its key, the list takes the focus, so that the arrows
		// left by the focused view reach it.
		list.Focusable().FocusRing(false).AutoFocus()
	}
	list.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Padding(0, 0, 4, 8).Children(func() {
			ui.Text(c, "Notifications").Bold()
			ui.Spacer(c)
			if len(a.notes) > 0 && ui.Button(c, "Clear All").Clicked() {
				a.notes, a.noteOpen, a.noteCursor = nil, 0, 0
			}
		})
		if len(a.notes) == 0 {
			ui.Text(c, "No notifications").TextColor(pal.Muted).Padding(8, 0)
			return
		}
		ui.Scroll(c).MaxHeight(420).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				for i := len(a.notes) - 1; i >= 0; i-- {
					a.notificationRow(c.Key(a.notes[i].seq), i)
				}
			})
		})
	})
}

func (a *App) notificationRow(c *ui.Context, i int) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	n := a.notes[i]
	highlighted := a.noteCursor == n.seq
	if a.noteOpen == n.seq {
		card := ui.Column(c).Padding(8).Radius(6).Background(pal.Hover)
		if highlighted {
			card.Border(1, t.Accent).ScrollIntoView()
		}
		card.Children(func() {
			r := widgets.ErrorReport{Title: n.title, Hint: n.hint, Detail: n.detail, Actions: n.actions}
			widgets.ErrorCard(c, r, &a.noteDetails, a.WriteClipboard)
		})
		return
	}
	row := ui.ButtonBase(c).Label(n.title).Padding(6, 8).Radius(6).Justify(ui.Start)
	if row.Hovered() || highlighted {
		row.Background(pal.Hover)
	}
	if highlighted {
		row.ScrollIntoView()
	}
	row.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, n.at.Format("15:04:05")).FontSize(11.5).TextColor(pal.Muted)
			if n.isError {
				ui.Icon(c, widgets.IconAlert).FontSize(13).TextColor(t.Danger)
			} else {
				ui.Icon(c, widgets.IconInfo).FontSize(13).TextColor(pal.Muted)
			}
			ui.Column(c).Shrink(1).Children(func() {
				ui.Text(c, n.title).SingleLine()
				if d := widgets.FirstLine(n.detail); d != "" {
					ui.Text(c, d).FontSize(11.5).TextColor(pal.Muted).SingleLine()
				}
			})
		})
	})
	if row.Clicked() {
		a.noteOpen, a.noteDetails, a.noteCursor = n.seq, false, n.seq
	}
}

// showLatestError shows the newest error in the error dialog, without
// noting it again, and counts it seen.
func (a *App) showLatestError() {
	for i := len(a.notes) - 1; i >= 0; i-- {
		if n := &a.notes[i]; n.isError {
			a.showAlert(widgets.ErrorReport{Title: n.title, Hint: n.hint, Detail: n.detail, Actions: n.actions})
			n.seen = true
			return
		}
	}
	a.Toast("No errors in this session", "", nil)
}

// toggleNotifications opens the notifications, with the focus in the list
// when byKey, or closes them.
func (a *App) toggleNotifications(byKey bool) {
	a.notesShown = !a.notesShown
	a.noteOpen, a.noteCursor, a.notesByKey = 0, 0, byKey && a.notesShown
}

func (a *App) toggleNotificationsByKey() { a.toggleNotifications(true) }

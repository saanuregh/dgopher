package app

import (
	"time"

	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"
)

// alerting is a notice as the system tells it: the seam tests replace.
type alerting struct {
	dataview.Notice
	// window is the window the notice is of, whose taskbar button flashes.
	window         *window
	desktop, sound bool
	// onClick runs on the UI thread once the notification is clicked.
	onClick func()
}

// Notify tells of n while it is not in sight, as the settings choose: long
// work once it took the seconds set, work at risk at once.
func (a *App) Notify(n dataview.Notice) {
	alert := a.settings.NotifyLongWork
	if n.AtRisk {
		alert = a.settings.NotifyAtRisk
	}
	if a.deliverNotice == nil || !alert.On() {
		return
	}
	if !n.AtRisk && time.Since(n.Started) < time.Duration(a.settings.NotifyAfter)*time.Second {
		return
	}
	owner := a.windowOfTab(n.Tab)
	if a.inSight(owner) {
		return
	}
	if owner == nil {
		owner = a.focusedWindow()
	}
	a.deliverNotice(alerting{
		Notice: n, window: owner, desktop: alert.Desktop(), sound: alert.Sound(),
		onClick: func() { a.showNoticed(n, owner) },
	})
}

// inSight reports whether what a notice is of shows already: the window
// of its tab, or, for app-wide work (owner nil), any window, has the focus.
func (a *App) inSight(owner *window) bool {
	if a.windowFocused == nil {
		return false
	}
	if owner != nil {
		return a.windowFocused(owner)
	}
	for _, w := range a.windows {
		if a.windowFocused(w) {
			return true
		}
	}
	return false
}

// showNoticed brings forward what a clicked notice is of: its tab, in the
// window holding it now, or the window it was told in, then runs its Show.
// The window is raised even when it was the last focused, as the system
// does not bring the app forward on a click (Linux, Windows).
func (a *App) showNoticed(n dataview.Notice, owner *window) {
	if n.Tab != nil && a.ActivateTab(func(t widgets.Tab) bool { return t == n.Tab }) {
		owner = a.windowOfTab(n.Tab)
	}
	a.raiseWindow(owner)
	if n.Show != nil {
		n.Show()
	}
}

// windowOfTab is the window holding t, nil for none.
func (a *App) windowOfTab(t widgets.Tab) *window {
	if t == nil {
		return nil
	}
	for w, each := range a.everyTab() {
		if each == t {
			return w
		}
	}
	return nil
}

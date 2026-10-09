package app

import (
	"time"

	"dgopher/internal/settings"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/redis"
	"dgopher/internal/ui/widgets"
)

// The app is the host of the feature packages: what they need of it, as
// their Host interfaces say.

func (a *App) Settings() *settings.Settings { return &a.settings }
func (a *App) Now() time.Time               { return a.now }
func (a *App) FocusWant() *string           { return &a.focusWant }
func (a *App) Dialogs() *dataview.Dialogs   { return &a.data }

func (a *App) Toast(text, action string, run func()) {
	a.toast = &pendingToast{text: text, action: action, run: run}
}

func (a *App) Notify(started time.Time, title, body string, show func()) {
	focused := a.windowFocused == nil || a.windowFocused()
	if a.notify == nil || !notifyWanted(time.Since(started), a.settings.NotifyAfter, focused) {
		return
	}
	a.notify(title, body, func() {
		a.Post(func() {
			if show != nil {
				show()
			}
		})
	})
}

// notifyWanted reports whether the end of work that took took is told by
// a system notification: when it took notifyAfter seconds or more, and
// the user is not looking at the window, where it shows already.
func notifyWanted(took time.Duration, notifyAfter int, focused bool) bool {
	return notifyAfter > 0 && !focused && took >= time.Duration(notifyAfter)*time.Second
}

func (a *App) AskDiscard(title, reason string, onDiscard func()) {
	a.closing = &closeRequest{open: true, title: title, reason: reason, onClose: onDiscard}
}

func (a *App) AskPending(n int, apply, discard, cancel func()) {
	a.pending = &pendingAsk{open: true, n: n, apply: apply, discard: discard, cancel: cancel}
}

func (a *App) ActivateTab(match func(widgets.Tab) bool) bool {
	_, ok := a.findTab(match)
	return ok
}

func (a *App) QueryDialogs() *query.Dialogs { return &a.queries }

var (
	_ dataview.Host = (*App)(nil)
	_ query.Host    = (*App)(nil)
	_ redis.Host    = (*App)(nil)
)

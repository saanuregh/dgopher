package app

import (
	"slices"
	"testing"

	"dgopher/internal/settings"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// The Notifications page chooses, for long work and for work at risk,
// how each tells; long work's time hides while it tells nothing. The
// Results page no longer has the setting.
func TestNotificationsPage(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	a.settingsOpen = true
	a.settingsPage = slices.Index(settingsPages, "Notifications")
	testutil.WaitFor(t, tt, "the page", func() bool { return tt.HasText("When work is at risk") && tt.HasText("Long work takes") })
	testutil.Snapshot(t, tt, "settings-notifications")

	if err := tt.Click("Notifications with sound"); err != nil { // long work's, the first
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Off"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the time to hide", func() bool { return !tt.HasText("Long work takes") })
	if a.settings.NotifyLongWork != settings.AlertOff || a.settings.NotifyAtRisk != settings.AlertBoth {
		t.Fatalf("long work %q, at risk %q", a.settings.NotifyLongWork, a.settings.NotifyAtRisk)
	}
	if err := tt.Click("Notifications with sound"); err != nil { // now at risk's
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Sound only"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if b := startApp(t, a.st); b.settings.NotifyLongWork != settings.AlertOff || b.settings.NotifyAtRisk != settings.AlertSound {
		t.Fatalf("saved %q, %q", b.settings.NotifyLongWork, b.settings.NotifyAtRisk)
	}

	a.settingsPage = slices.Index(settingsPages, "Results")
	tt.Frame()
	if tt.HasText("seconds, 0 for never") {
		t.Fatal("the Results page still sets notifications")
	}
}

package app

import (
	"fmt"
	"log"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"dgopher/internal/keymap"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/widgets"
)

// Two toasts asked for in one frame both show: the second does not
// replace the first.
func TestTwoToastsInOneFrameBothShow(t *testing.T) {
	a := newBareApp(t)
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.Toast("First toast", "", nil)
	a.Toast("Second toast", "", nil)
	tt.Frame()
	if !tt.HasText("First toast") || !tt.HasText("Second toast") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

// Errors count as unread, toasts do not; opening the list shows every
// notification and clears the count.
func TestErrorsCountUnreadAndOpeningClears(t *testing.T) {
	a := newBareApp(t)
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.ShowError("First problem", "first detail")
	a.ShowError("Second problem", "second detail")
	a.Toast("Saved the thing", "", nil)
	a.alerts = nil // the dialog would cover the title bar
	tt.Frame()
	if a.unread() != 2 || !slices.Contains(tt.Texts(), "2") {
		t.Fatalf("unread %d; texts %q", a.unread(), tt.Texts())
	}
	if err := tt.Click("Notifications"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the list", func() bool { return tt.HasText("No notifications") || tt.HasText("Clear All") })
	for _, s := range []string{"First problem", "Second problem", "Saved the thing", "first detail"} {
		if !testutil.HasTextContaining(tt, s) {
			t.Fatalf("no %q in %q", s, tt.Texts())
		}
	}
	if a.unread() != 0 || slices.Contains(tt.Texts(), "2") {
		t.Fatalf("after opening: unread %d; texts %q", a.unread(), tt.Texts())
	}
	testutil.Snapshot(t, tt, "notifications")
}

// Only the newest notifications are kept.
func TestNotificationsKeepNewest200(t *testing.T) {
	a := newBareApp(t)
	for i := range 201 {
		a.note(notification{title: fmt.Sprint("note ", i)})
	}
	if len(a.notes) != 200 || a.notes[0].title != "note 1" || a.notes[199].title != "note 200" {
		t.Fatalf("%d notes, first %q", len(a.notes), a.notes[0].title)
	}
}

// A line of the standard logger becomes an error notification, its
// secrets hidden.
func TestLogLineBecomesRedactedNotification(t *testing.T) {
	a := newBareApp(t)
	restore := a.captureLog()
	t.Cleanup(restore)
	log.Print("ALTER USER app WITH PASSWORD 'hunter2'")
	a.drain()
	if len(a.notes) != 1 {
		t.Fatalf("%d notes", len(a.notes))
	}
	n := a.notes[0]
	if !n.isError || !strings.Contains(n.detail, "ALTER USER app") || strings.Contains(n.detail, "hunter2") || a.unread() != 1 {
		t.Fatalf("note %+v, unread %d", n, a.unread())
	}
}

// A notification keeps its error's hint, which shows as it is opened.
func TestNotificationKeepsHint(t *testing.T) {
	a := newBareApp(t)
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.ShowReport(widgets.ErrorReport{Title: "Some problem", Hint: "What it means.", Detail: "the raw text"})
	a.alerts = nil // the dialog would cover the title bar
	if len(a.notes) != 1 || a.notes[0].hint != "What it means." {
		t.Fatalf("notes %+v", a.notes)
	}
	tt.Frame()
	if err := tt.Click("Notifications"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the list", func() bool { return tt.HasText("Some problem") })
	if err := tt.Click("Some problem"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the note open", func() bool { return tt.HasText("What it means.") })
	if err := tt.Click("Details"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the full text under Details", func() bool { return tt.HasText("the raw text") })
}

// A background panic, which is both logged and shown, is one notification.
func TestBackgroundPanicNotedOnce(t *testing.T) {
	a := newBareApp(t)
	t.Cleanup(a.captureLog())
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.Background(func() func() { panic("boom") })
	testutil.WaitFor(t, tt, "the error", func() bool { return a.frontAlert() != nil })
	tt.Frame()
	tt.Frame()
	errs := 0
	for _, n := range a.notes {
		if n.isError {
			errs++
		}
	}
	if errs != 1 {
		t.Fatalf("%d error notifications: %+v", errs, a.notes)
	}
}

// Show Latest Error shows the newest error in a dialog without noting it
// again; with no error yet it says so in a toast.
func TestShowLatestErrorOpensDialog(t *testing.T) {
	a := newBareApp(t)
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.ShowError("First problem", "first detail")
	a.ShowError("Second problem", "second detail")
	a.Toast("Saved the thing", "", nil)
	a.alerts = nil
	notes := len(a.notes)
	a.menuAction(keymap.ShowLatestError)()
	tt.Frame()
	if len(a.alerts) != 1 || a.alerts[0].report.Title != "Second problem" {
		t.Fatalf("alerts %v", a.alerts)
	}
	if len(a.notes) != notes {
		t.Fatalf("notes %d, were %d", len(a.notes), notes)
	}
	if !testutil.HasTextContaining(tt, "second detail") {
		t.Fatalf("texts %q", tt.Texts())
	}

	b := newBareApp(t)
	bt := ui.NewTester(b.windowView(b.main), 1200, 760)
	b.menuAction(keymap.ShowLatestError)()
	bt.Frame()
	if len(b.alerts) != 0 || !bt.HasText("No errors in this session") {
		t.Fatalf("alerts %d; texts %q", len(b.alerts), bt.Texts())
	}
}

// The notifications' key opens the list with the keys in it: Down
// highlights the newest, Enter shows it whole, Escape closes the list.
func TestNotificationsByKeyboard(t *testing.T) {
	a := newBareApp(t)
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.ShowError("First problem", "first detail")
	a.ShowError("Second problem", "second detail")
	a.alerts = nil
	tt.Frame()
	tt.Key(ui.Cmd|ui.Shift, ui.KeyN)
	testutil.WaitFor(t, tt, "the list", func() bool { return tt.HasText("Clear All") })
	if a.unread() != 0 {
		t.Fatalf("unread %d", a.unread())
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if newest := a.notes[len(a.notes)-1].seq; a.noteOpen != newest {
		t.Fatalf("open %d, want the newest %d", a.noteOpen, newest)
	}
	testutil.Snapshot(t, tt, "notifications-keyboard")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()
	if a.notesShown || tt.HasText("Clear All") {
		t.Fatalf("still open: texts %q", tt.Texts())
	}
}

// Showing the latest error again does not count another error as read.
func TestShowLatestErrorCountsItOnce(t *testing.T) {
	a := newBareApp(t)
	a.ShowError("First problem", "first detail")
	a.ShowError("Second problem", "second detail")
	a.alerts = nil
	a.showLatestError()
	a.alerts = nil
	a.showLatestError()
	if a.unread() != 1 {
		t.Fatalf("unread %d, want 1: only the second was shown", a.unread())
	}
}

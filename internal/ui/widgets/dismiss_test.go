package widgets_test

import (
	"testing"

	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// form is a dialog with one input, and work that may run.
type form struct {
	open    bool
	name    string
	running bool
	dismiss widgets.DismissGuard
}

func (f *form) view(c *ui.Context) {
	ui.Column(c).Fill()
	dlg := ui.Modal(c, &f.open, func() { ui.Text(c, "The form") })
	f.dismiss.Keep(c, dlg, &f.open, f.name, f.running)
}

func openForm(t *testing.T) (*form, *ui.Tester) {
	t.Helper()
	f := &form{open: true}
	tt := ui.NewTester(f.view, 600, 400)
	tt.Frame()
	return f, tt
}

// Escape or a click outside closes a dialog left as it opened.
func TestDismissGuardUntouchedCloses(t *testing.T) {
	f, tt := openForm(t)
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if f.open {
		t.Fatal("an untouched dialog stayed open")
	}
	// Opened again, it compares with what it opens with now.
	f.open, f.name = true, "reopened"
	tt.Frame()
	f.name = "typed, then taken back"
	tt.Frame()
	f.name = "reopened"
	tt.ClickAt(5, 5)
	tt.Frame()
	if f.open {
		t.Fatal("a dialog changed back stayed open")
	}
}

// An edited dialog asks before its edits go; keeping them keeps it open.
func TestDismissGuardEditedAsks(t *testing.T) {
	f, tt := openForm(t)
	f.name = "orders"
	tt.Frame()
	tt.ClickAt(5, 5)
	tt.Frame()
	if !f.open || !tt.HasText("Discard your changes?") {
		t.Fatalf("closed without asking: open %v, %q", f.open, tt.Texts())
	}
	if err := tt.Click("Keep Editing"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !f.open || tt.HasText("Discard your changes?") {
		t.Fatalf("keep editing: open %v, %q", f.open, tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Key(0, ui.KeyEscape) // on the question: keep editing
	tt.Frame()
	if !f.open || tt.HasText("Discard your changes?") {
		t.Fatalf("Escape on the question: open %v, %q", f.open, tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if err := tt.Click("Discard"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if f.open {
		t.Fatal("discarded, still open")
	}
}

// A dialog whose work runs stays open, without asking.
func TestDismissGuardRunningStays(t *testing.T) {
	f, tt := openForm(t)
	f.running = true
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.ClickAt(5, 5)
	tt.Frame()
	if !f.open || tt.HasText("Discard your changes?") {
		t.Fatalf("running: open %v, %q", f.open, tt.Texts())
	}
	f.running = false
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if f.open {
		t.Fatal("done, still open")
	}
}

// What the dialog's work ran on is no edit to discard.
func TestDismissGuardUsed(t *testing.T) {
	f, tt := openForm(t)
	f.name = "orders"
	tt.Frame()
	f.dismiss.Used() // as a run starts
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if f.open {
		t.Fatalf("asked about what ran: %q", tt.Texts())
	}
}

// A slice changed in place is an edit.
func TestDismissGuardInPlace(t *testing.T) {
	f := &struct {
		open    bool
		tags    []string
		dismiss widgets.DismissGuard
	}{open: true, tags: []string{"a"}}
	tt := ui.NewTester(func(c *ui.Context) {
		dlg := ui.Modal(c, &f.open, func() { ui.Text(c, "Tags") })
		f.dismiss.Keep(c, dlg, &f.open, f.tags, false)
	}, 600, 400)
	tt.Frame()
	f.tags[0] = "b"
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if !f.open || !tt.HasText("Discard your changes?") {
		t.Fatal("an edit in place went unseen")
	}
}

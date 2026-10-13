package widgets_test

import (
	"testing"
	"time"

	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Work shows only once it has gone on a moment: what ends sooner would
// only flash.
func TestLasted(t *testing.T) {
	since := time.Now()
	shown := false
	tt := ui.NewTester(func(c *ui.Context) { shown = widgets.Lasted(c, since) }, 200, 100)
	tt.Frame()
	if shown {
		t.Fatal("shown at once")
	}
	since = time.Now().Add(-widgets.ShowAfter)
	tt.Frame()
	if !shown {
		t.Fatal("not shown once it lasted")
	}
}

// A morphing button takes the room the plain one does, for the same
// label: what follows it stays where it was.
func TestMorphButtonsMatchPlain(t *testing.T) {
	at := func(button func(c *ui.Context) ui.Element) ui.Rect {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Row(c).Padding(10).Children(func() {
				button(c)
				ui.Text(c, "after")
			})
		}, 300, 100)
		tt.Frame()
		next, _ := tt.Find("after")
		return next
	}
	for _, c := range []struct {
		name         string
		plain, morph func(c *ui.Context) ui.Element
	}{
		{"button", func(c *ui.Context) ui.Element { return ui.Button(c, "Close") }, func(c *ui.Context) ui.Element { return widgets.MorphButton(c, "Close") }},
		{"primary", func(c *ui.Context) ui.Element { return ui.PrimaryButton(c, "Close") }, func(c *ui.Context) ui.Element { return widgets.MorphPrimaryButton(c, "Close") }},
	} {
		if plain, morph := at(c.plain), at(c.morph); plain != morph {
			t.Errorf("%s: what follows at %v, after the plain one at %v", c.name, morph, plain)
		}
	}
}

// A dialog's first button stops its job while it runs, else closes the
// dialog, in one place: Cancel while running or before the job, Close once
// done.
func TestDismissButton(t *testing.T) {
	running, done := false, false
	stopped, closed := 0, 0
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Padding(10).Children(func() {
			widgets.DismissButton(c, running, done, func() { stopped++ }, func() { closed++ })
		})
	}, 300, 100)
	tt.Frame()
	if err := tt.Click("Cancel"); err != nil || closed != 1 || stopped != 0 {
		t.Fatalf("before the job: %v, closed %d, stopped %d", err, closed, stopped)
	}
	running = true
	tt.Frame()
	if err := tt.Click("Cancel"); err != nil || stopped != 1 || closed != 1 {
		t.Fatalf("while it runs: %v, closed %d, stopped %d", err, closed, stopped)
	}
	running, done = false, true
	tt.Frame()
	if err := tt.Click("Close"); err != nil || closed != 2 || stopped != 1 {
		t.Fatalf("after it: %v, closed %d, stopped %d", err, closed, stopped)
	}
	// Running with nothing to stop it, Cancel does nothing.
	tt2 := ui.NewTester(func(c *ui.Context) { widgets.DismissButton(c, true, true, nil, func() { closed++ }) }, 300, 100)
	tt2.Frame()
	if err := tt2.Click("Cancel"); err != nil || closed != 2 {
		t.Fatalf("with no stop: %v, closed %d", err, closed)
	}
}

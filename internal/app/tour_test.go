package app

import (
	"strings"
	"testing"
	"time"

	"dgopher/internal/keymap"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// The start page offers the tour, which goes from stop to stop, shows the
// sidebar at its stop, ends with Esc or Done, and is not offered after.
func TestTour(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	if err := tt.Click("Take the Tour"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the first stop", func() bool { return tt.HasText("Welcome to DGopher") })
	if err := tt.Click("Next"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("Projects and connections") || a.tourParts[tourSidebar].W == 0 {
		t.Fatalf("the sidebar's stop: %q, parts %+v", tt.Texts(), a.tourParts)
	}
	testutil.Snapshot(t, tt, "tour-sidebar")
	// Keys show as they are set on this system, never as macOS's alone.
	if open := keymap.Hint("Open Table", keymap.OpenTable); !strings.Contains(tourStops()[1].text, open) {
		t.Fatalf("the sidebar's stop names Open Table's key otherwise than %q: %q", open, tourStops()[1].text)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.touring || !a.settings.TourDone || tt.HasText("Take the Tour") {
		t.Fatalf("Esc left the tour %v, done %v", a.touring, a.settings.TourDone)
	}

	a.startTour()
	for range len(tourStops()) - 1 {
		tt.Frame()
		if err := tt.Click("Next"); err != nil {
			t.Fatal(err)
		}
	}
	tt.Frame()
	if err := tt.Click("Done"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.touring {
		t.Fatal("Done left the tour on")
	}
}

// Every button of a stop is inside its card, at each stop: one with a
// button of its own, as Open the Palette, has four to fit.
func TestTourButtonsStayInTheCard(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	a.startTour()
	for i, stop := range tourStops() {
		a.tourStop = i
		tt.Frame()
		tt.Advance(time.Second) // what comes in, come
		tt.Frame()
		card, ok := tt.Access(stop.title)
		if !ok {
			t.Fatalf("stop %d shows no card named %q", i+1, stop.title)
		}
		for _, button := range []string{"End Tour", stop.action, "Back", "Next", "Done"} {
			r, ok := tt.Find(button)
			if button == "" || !ok {
				continue
			}
			if right, edge := r.X+r.W, card.Bounds.X+card.Bounds.W; right > edge || r.X < card.Bounds.X {
				t.Errorf("stop %d, %q: the button %q is at %v, past its card at %v", i+1, stop.title, button, r, card.Bounds)
			}
		}
	}
}

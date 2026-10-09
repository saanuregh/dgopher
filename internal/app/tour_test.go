package app

import (
	"testing"

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

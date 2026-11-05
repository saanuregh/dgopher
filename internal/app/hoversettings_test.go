package app

import (
	"testing"
	"time"

	"dgopher/internal/settings"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Hover cards show everywhere after the default rest unless set
// otherwise; a surface turned off is off alone.
func TestHoverSettings(t *testing.T) {
	h := hoverSettings(settings.Default().HoverCards)
	if h.Off || h.Delay != widgets.DefaultHoverDelay || !h.Shows(widgets.CardGrid) {
		t.Fatalf("default %+v", h)
	}
	h = hoverSettings(settings.HoverCardPrefs{DelayMs: 250, Hidden: []string{widgets.CardGrid}})
	if h.Delay != 250*time.Millisecond || h.Shows(widgets.CardGrid) || !h.Shows(widgets.CardEditor) {
		t.Fatalf("set %+v", h)
	}
	if hoverSettings(settings.HoverCardPrefs{Off: true}).Shows(widgets.CardEditor) {
		t.Fatal("off shows")
	}
}

// The wait's number input keeps its − and + buttons clear of the text
// after it. The setting is drawn alone, so that its stepper is the only
// one Find sees.
func TestHoverDelayInputFits(t *testing.T) {
	a := newBareApp(t)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Form(c, func() { a.hoverCardsSetting(c) })
	}, 640, 200)
	stepperClearOf(t, tt, "ms before a card shows")
	testutil.Snapshot(t, tt, "settings-hover-cards")
}

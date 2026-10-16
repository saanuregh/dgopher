package app

import (
	"testing"
	"time"

	"dgopher/internal/settings"
	"dgopher/internal/ui/widgets"
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

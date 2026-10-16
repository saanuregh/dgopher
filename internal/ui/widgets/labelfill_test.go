package widgets

import (
	"testing"

	"dgopher/internal/db"

	"github.com/egoist/mygo/ui"
)

// White text on an environment's label reads as WCAG AA asks, in the
// environment's own hue.
func TestLabelFillReads(t *testing.T) {
	for _, e := range []db.Environment{db.Development, db.Staging, db.Production} {
		col := EnvironmentColor(e)
		got := LabelFill(col)
		if r := contrastWithWhite(got); r < 4.5 {
			t.Errorf("%s: %v on white reads %.2f:1", e.Label(), got, r)
		}
		if contrastWithWhite(col) >= 4.5 && got != col {
			t.Errorf("%s: %v already reads, changed to %v", e.Label(), col, got)
		}
	}
	if got := LabelFill(ui.Hex("#dc2626")); got.R <= got.G || got.R <= got.B {
		t.Errorf("production's red became %v", got)
	}
}

// Production's and staging's names read on the sidebar, dark or light.
func TestLabelInkReads(t *testing.T) {
	for _, bg := range []ui.Color{darkPalette.Sidebar, lightPalette.Sidebar} {
		for _, e := range []db.Environment{db.Staging, db.Production} {
			if r := contrast(LabelInk(EnvironmentColor(e), bg), bg); r < 4.5 {
				t.Errorf("%s on %v reads %.2f:1", e.Label(), bg, r)
			}
		}
	}
}

package dataview

import (
	"testing"

	"dgopher/internal/settings"

	"github.com/egoist/mygo/ui"
)

// rowPitch is how far apart the rows showing a and b are.
func rowPitch(t *testing.T, tt *ui.Tester, a, b string) float32 {
	t.Helper()
	tt.Frame()
	ra, okA := tt.Find(a)
	rb, okB := tt.Find(b)
	if !okA || !okB {
		t.Fatalf("%q or %q does not show: %q", a, b, tt.Texts())
	}
	return rb.Y - ra.Y
}

// A grid's rows are as dense as DBeaver's: 22 high at the default text
// size, and as much taller as the text is zoomed.
func TestGridRowsAreCompact(t *testing.T) {
	a, tt, _, _ := openNumbers(t, 5, 100)
	if got := rowPitch(t, tt, "row 1", "row 2"); got != 22 {
		t.Errorf("rows are %v apart at the default size, want 22", got)
	}
	a.Settings().GridFont = settings.MaxGridFont
	if got, want := rowPitch(t, tt, "row 1", "row 2"), RowHeight(settings.MaxGridFont); got != want {
		t.Errorf("rows are %v apart at the largest size, want %v", got, want)
	}
}

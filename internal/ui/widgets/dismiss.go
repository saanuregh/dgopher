package widgets

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// DismissGuard keeps a dialog open when Escape or a click outside closed
// it while its work runs, which its own Stop ends, or after the user
// changed what it asks for, asking then whether to discard the changes.
type DismissGuard struct {
	start        string // the inputs as the dialog opened, printed
	seen, asking bool
}

// Used takes what the dialog holds now as what it opened with, as when its
// work ran on it: closing it then loses nothing.
func (g *DismissGuard) Used() { g.seen = false }

// Edited reports whether inputs differ from what the dialog opened with:
// what the dialog loads itself, read once it lands, is Used only when they
// do not.
func (g *DismissGuard) Edited(inputs any) bool { return g.seen && fmt.Sprint(inputs) != g.start }

// Keep runs after the dialog, before what its closing does: dialog is
// what ui.Modal or ui.DialogBase returned for open; inputs are the values
// the user enters in it, compared with those it opened with as fmt prints
// them: a slice or map changed in place counts, and a pointer in them
// compares by address; running is whether its work runs.
func (g *DismissGuard) Keep(c *ui.Context, dialog ui.Element, open *bool, inputs any, running bool) {
	if !*open && !g.asking && !dialog.Dismissed() {
		g.seen = false // the next open starts again
		return
	}
	now := fmt.Sprint(inputs)
	if !g.seen {
		g.start, g.seen = now, true
	}
	if dialog.Dismissed() {
		switch {
		case running:
			*open = true
		case now != g.start:
			*open, g.asking = true, true
		}
	}
	if g.asking && ui.AlertDialog(c, &g.asking, "Discard your changes?", "What you entered here is lost.", "Discard", "Keep Editing") == 0 {
		*open = false
	}
}

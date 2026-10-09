package keymap

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// Every default key reads back as written, and none is another's where
// both work.
func TestDefaults(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Commands {
		if seen[c.ID] {
			t.Errorf("%s twice", c.ID)
		}
		seen[c.ID] = true
		for _, ch := range c.Default {
			back, err := ParseChord(ch.String())
			if err != nil || back != ch {
				t.Errorf("%s: %s read back as %v, %v", c.ID, ch, back, err)
			}
			if err := Allowed(c.ID, ch); err != nil {
				t.Errorf("%s: %v", c.ID, err)
			}
		}
	}
}

func TestUse(t *testing.T) {
	defer Use(nil)
	errs := Use(map[string][]string{Format: {"Alt+Shift+F"}, Explain: {}, "nothing": {"Cmd+Q"}, Find: {"Cmd+Nope"}})
	if len(errs) != 2 {
		t.Fatalf("errors %v", errs)
	}
	if got := Chords(Format); len(got) != 1 || got[0] != (Chord{ui.Alt | ui.Shift, ui.KeyF}) || !Changed(Format) {
		t.Fatalf("format %v", got)
	}
	if len(Chords(Explain)) != 0 || !Is(Run, ui.Cmd, ui.KeyEnter) {
		t.Fatal("explain kept its key, or run lost its")
	}
	if ch, _ := Primary(Format); ch.Accelerator() != "Alt+Shift+f" {
		t.Fatalf("accelerator %q", ch.Accelerator())
	}
	if ch, _ := ParseChord("cmd+shift+enter"); ch != (Chord{ui.Cmd | ui.Shift, ui.KeyEnter}) {
		t.Fatalf("parsed %v", ch)
	}
}

// A key that types, or moves in a list, or is the window's own, is no
// command's; the default keys keep how they are written.
func TestAllowed(t *testing.T) {
	for _, ch := range []Chord{{0, ui.KeyA}, {ui.Shift, ui.KeyA}, {0, ui.KeyEnter}, {0, ui.KeyDown}, {ui.Cmd, ui.Key3}, {ui.Cmd, ui.KeyK}} {
		if Allowed(Format, ch) == nil {
			t.Errorf("%s allowed", ch)
		}
	}
	if Allowed(Format, Chord{0, ui.KeyF9}) != nil || Allowed(Format, Chord{ui.Alt, ui.KeyQ}) != nil || Allowed(EditValue, Chord{ui.Shift, ui.KeyEnter}) != nil {
		t.Error("a key free refused")
	}
	if Allowed(Copy, Chord{0, ui.KeyF2}) == nil {
		t.Error("the grid's F2 taken")
	}
	got := Written(NextTab, []Chord{{ui.Ctrl, ui.KeyTab}, {ui.Alt, ui.KeyPageDown}})
	if got[0] != "Ctrl+Tab" || got[1] != "Alt+PageDown" {
		t.Errorf("written %q", got)
	}
	if !SameKeys([]Chord{{0, ui.KeyF1}, {0, ui.KeyF2}}, []Chord{{0, ui.KeyF2}, {0, ui.KeyF1}}) {
		t.Error("the same keys in another order differ")
	}
	defer Use(nil)
	if Use(map[string][]string{Find: {"Cmd+Nope"}}); len(Chords(Find)) != 1 {
		t.Error("a key misspelled took the command's own")
	}
}

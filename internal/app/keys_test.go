package app

import (
	"slices"
	"testing"

	"dgopher/internal/keymap"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// A key recorded for a command is kept in the settings and works; one
// another command has is refused; Reset gives the command its own back.
func TestKeysEditor(t *testing.T) {
	a := newTestApp(t)
	defer keymap.Use(nil)
	tt := ui.NewTester(a.view, 1280, 800)
	a.keys = &keysEditor{open: true, recording: keymap.Format}
	testutil.WaitFor(t, tt, "the recorder", func() bool { return testutil.HasTextContaining(tt, "Press a key") })
	tt.Frame()
	tt.Key(ui.Alt|ui.Shift, ui.KeyF)
	tt.Frame()
	if got := a.settings.Keys[keymap.Format]; !slices.Equal(got, []string{"Cmd+Shift+F", "Alt+Shift+F"}) || !keymap.Is(keymap.Format, ui.Alt|ui.Shift, ui.KeyF) {
		t.Fatalf("format's keys %q, recording %q", got, a.keys.recording)
	}

	a.keys.recording = keymap.Explain
	tt.Frame()
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	if a.keys.err == "" || keymap.Is(keymap.Explain, ui.Cmd, ui.KeyK) {
		t.Fatalf("explain took the palette's key: %q", a.keys.err)
	}
	testutil.Snapshot(t, tt, "keys-editor")

	a.keys.filter = "format"
	tt.Frame()
	if err := tt.Click("Reset"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, kept := a.settings.Keys[keymap.Format]; kept || keymap.Changed(keymap.Format) {
		t.Fatalf("format not reset: %q", a.settings.Keys)
	}
}

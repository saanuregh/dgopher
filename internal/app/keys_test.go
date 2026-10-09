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

// A dialog does what it is for with its key from any of its fields, and
// the key changed in the settings does it in its place.
func TestDialogKey(t *testing.T) {
	a := newTestApp(t)
	defer keymap.Use(nil)
	tt := ui.NewTester(a.view, 1000, 700)
	var renamed []string
	ask := func() {
		a.renaming = &renameForm{open: true, title: "Rename", name: "new", rename: func(n string) error { renamed = append(renamed, n); return nil }}
		tt.Frame()
		tt.Frame()
	}
	ask()
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()
	if len(renamed) != 1 || renamed[0] != "new" {
		t.Fatalf("renamed %q", renamed)
	}
	keymap.Use(map[string][]string{keymap.ConfirmDialog: {"Alt+Enter"}})
	ask()
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()
	tt.Key(ui.Alt, ui.KeyEnter)
	tt.Frame()
	if len(renamed) != 2 {
		t.Fatalf("with the key changed, renamed %q", renamed)
	}
}

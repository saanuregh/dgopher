package app

import (
	"slices"
	"strings"

	"dgopher/internal/keymap"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// keysEditor changes the keys of the app's commands.
type keysEditor struct {
	open   bool
	filter string
	// recording is the command whose next key pressed is added to its
	// keys, "" for none.
	recording string
	err       string
}

// setKeys keeps a command's keys in the settings, none of it when they
// are its own, and puts them to use: in the window, and the menu bar.
func (a *App) setKeys(id string, chords []keymap.Chord) {
	cmd, _ := keymap.Lookup(id)
	if keymap.SameKeys(chords, cmd.Default) {
		delete(a.settings.Keys, id)
	} else {
		if a.settings.Keys == nil {
			a.settings.Keys = map[string][]string{}
		}
		a.settings.Keys[id] = keymap.Written(id, chords)
	}
	keymap.Use(a.settings.Keys)
	a.SaveSettings()
	mygo.App.SetMenu(buildMenu(a))
}

// record takes a key pressed for the command being recorded: Esc stops,
// a key another command has is refused.
func (k *keysEditor) record(a *App, ev ui.InputEvent) bool {
	if ev.Kind != ui.InputKeyDown || k.recording == "" {
		return false
	}
	if ev.Key == ui.KeyEscape && ev.Mods == 0 {
		k.recording = ""
		return true
	}
	if ev.Key == ui.KeyUnknown {
		return true // a modifier, held for the key to come
	}
	ch := keymap.Chord{Mods: ev.Mods, Key: ev.Key}
	if err := keymap.Allowed(k.recording, ch); err != nil {
		k.err = widgets.Capitalize(err.Error()) + "."
		return true
	}
	if chords := keymap.Chords(k.recording); !slices.Contains(chords, ch) {
		a.setKeys(k.recording, append(slices.Clone(chords), ch))
	}
	k.recording, k.err = "", ""
	return true
}

func (a *App) keysView(c *ui.Context) {
	k := a.keys
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Modal(c, &k.open, func() {
		ui.Column(c).Width(720).Height(600).Gap(10).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Keyboard Shortcuts").FontSize(16).Bold()
				ui.Spacer(c)
				ui.TextInput(c, &k.filter).Placeholder("Search commands or keys").Width(260).Label("Search commands")
			})
			if k.err != "" {
				ui.Text(c, k.err).TextColor(th.Danger).FontSize(12.5)
			}
			filter := strings.ToLower(strings.TrimSpace(k.filter))
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Gap(4).PaddingX(2).Children(func() {
					for _, scope := range keymap.Scopes {
						ui.Text(c, string(scope)).Bold().FontSize(12).TextColor(pal.Muted).Padding(8, 0, 2, 0)
						for _, cmd := range keymap.Commands {
							if cmd.Scope != scope || filter != "" && !strings.Contains(strings.ToLower(cmd.Title+" "+keymap.Label(cmd.ID)), filter) {
								continue
							}
							k.commandRow(a, c, cmd)
						}
					}
				})
			})
			ui.Text(c, "Add records the next key pressed; Esc stops. A key works where its section says, and once only there.").
				FontSize(12).TextColor(pal.Muted)
		})
	})
	if !k.open {
		a.keys = nil
	}
}

func (k *keysEditor) commandRow(a *App, c *ui.Context, cmd keymap.Command) {
	pal := widgets.PaletteOf(c)
	ui.Row(c.Key("key-"+cmd.ID)).Gap(8).AlignItems(ui.Center).Padding(3, 0).Children(func() {
		title := ui.Text(c, cmd.Title).FontSize(12.5).Grow(1).Shrink(1)
		if keymap.Changed(cmd.ID) {
			title.Bold()
		}
		chords := keymap.Chords(cmd.ID)
		for i, ch := range chords {
			ui.Row(c).Gap(2).AlignItems(ui.Center).Padding(1, 2, 1, 6).Radius(4).Background(pal.Hover).Children(func() {
				ui.Text(c, ch.Label()).Font(widgets.MonoFont).FontSize(11.5)
				if widgets.IconButton(c, widgets.IconX, "Remove "+ch.Label()).Clicked() {
					a.setKeys(cmd.ID, slices.Delete(slices.Clone(chords), i, i+1))
				}
			})
		}
		if k.recording == cmd.ID {
			box := ui.Box(c).Focusable().Padding(2, 8).Radius(4).Border(1, c.Theme().Accent).Label("Press the key for " + cmd.Title).
				HandleInput(func(ev ui.InputEvent) bool { return k.record(a, ev) })
			box.Children(func() { ui.Text(c, "Press a key…").FontSize(11.5).TextColor(pal.Muted) })
			if !box.Focused() {
				box.Focus()
			}
		} else if ui.Button(c, "Add").Tooltip("Record a key for this command").Clicked() {
			k.recording, k.err = cmd.ID, ""
		}
		if keymap.Changed(cmd.ID) && ui.Button(c, "Reset").Tooltip("Back to its own keys").Clicked() {
			k.err = ""
			for _, ch := range cmd.Default {
				if err := keymap.Allowed(cmd.ID, ch); err != nil {
					k.err = widgets.Capitalize(err.Error()) + "."
				}
			}
			if k.err == "" {
				a.setKeys(cmd.ID, cmd.Default)
			}
		}
	})
}

package app

import (
	"errors"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/keymap"
	"dgopher/internal/store"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// keysEditor changes the keys of the app's actions.
type keysEditor struct {
	open   bool
	filter string
	// recording is the action whose next key pressed is added to its
	// keys, "" for none; first is the first of two keys pressed for it,
	// waiting for the second, or ↵ to keep it alone.
	recording string
	first     keymap.Chord
	hasFirst  bool
	err       string
}

// keymapFile is what the app knows of keymap.json: when it was last
// read, as its time on disk says, and what of it did not read.
type keymapFile struct {
	modTime   time.Time
	checkedAt time.Time
	errs      []string
	// openWanted asks the next frame to open the file in the system's
	// editor, which needs the frame's context.
	openWanted bool
}

// loadKeymap reads keymap.json and puts its keys to use. Keys the
// settings once kept move there the first time, and leave the settings.
func (a *App) loadKeymap() {
	if len(a.settings.Keys) > 0 {
		if _, err := a.st.ReadFile(keymap.FileName); errors.Is(err, store.ErrNotFound) {
			overrides, errs := keymap.FromLegacy(a.settings.Keys)
			for _, err := range errs {
				log.Println("settings: keys:", err)
			}
			if err := a.st.WriteFile(keymap.FileName, keymap.FormatFile(overrides)); err != nil {
				log.Println("keymap:", err)
				keymap.Use(overrides)
				return
			}
			a.settings.Keys = nil
			a.SaveSettings()
		}
	}
	a.readKeymap()
}

// readKeymap reads keymap.json again, and keeps what did not read.
func (a *App) readKeymap() {
	f := &a.keymapFile
	f.errs = nil
	if p, err := a.st.Path(keymap.FileName); err == nil {
		if info, err := os.Stat(p); err == nil {
			f.modTime = info.ModTime()
		} else {
			f.modTime = time.Time{}
		}
	}
	data, err := a.st.ReadFile(keymap.FileName)
	switch {
	case errors.Is(err, store.ErrNotFound):
		keymap.Use(nil)
		return
	case err != nil:
		f.errs = []string{err.Error()}
		return // the keys in use stay
	}
	overrides, errs := keymap.Parse(data)
	for _, err := range errs {
		f.errs = append(f.errs, err.Error())
	}
	if len(overrides) == 0 && len(errs) == 1 && strings.Contains(errs[0].Error(), "does not read") {
		return // a file being written, or broken: the keys in use stay
	}
	keymap.Use(overrides)
}

// watchKeymap reads keymap.json again when it changed on disk, as an
// editor saved it, looking at most once a second; what did not read
// shows in a toast.
func (a *App) watchKeymap(c *ui.Context) {
	f := &a.keymapFile
	now := c.Now()
	if now.Sub(f.checkedAt) < time.Second {
		return
	}
	f.checkedAt = now
	p, err := a.st.Path(keymap.FileName)
	if err != nil {
		return
	}
	var mod time.Time
	if info, err := os.Stat(p); err == nil {
		mod = info.ModTime()
	}
	if mod.Equal(f.modTime) {
		return
	}
	a.readKeymap()
	setMenuBar(a)
	c.Invalidate()
	if len(f.errs) > 0 {
		a.Toast(keymap.FileName+": "+f.errs[0], "", nil)
	} else {
		a.Toast("Keys read again from "+keymap.FileName, "", nil)
	}
}

// setBindings gives an action exactly these keys, keeps them in
// keymap.json, and puts them to use: in the window, and the menu bar. A
// file with what did not read is not written over: it would lose that.
func (a *App) setBindings(id string, bindings []keymap.Binding) {
	if a.refuseKeymapWrite() {
		return
	}
	a.saveKeymap(keymap.SetBindings(id, bindings))
}

// refuseKeymapWrite says so and reports true while keymap.json holds
// what did not read, which writing it again would drop.
func (a *App) refuseKeymapWrite() bool {
	if len(a.keymapFile.errs) == 0 {
		return false
	}
	a.ShowError("keymap.json has mistakes", "Writing the keys there now would drop what did not read: "+a.keymapFile.errs[0]+"\n\nFix the file first; Open keymap.json shows it.")
	return true
}

// saveKeymap writes the user's keys to keymap.json.
func (a *App) saveKeymap(overrides []keymap.Override) {
	if err := a.st.WriteFile(keymap.FileName, keymap.FormatFile(overrides)); err != nil {
		a.ShowError("Could not save the keys", err.Error())
	}
	a.readKeymap()
	setMenuBar(a)
}

// openKeymap opens keymap.json in the system's editor for JSON, written
// first with the keys in use when there is none yet.
func (a *App) openKeymap() {
	if _, err := a.st.ReadFile(keymap.FileName); errors.Is(err, store.ErrNotFound) {
		a.saveKeymap(keymap.Overrides())
	}
	a.keymapFile.openWanted = true
}

// openKeymapNow opens keymap.json as a frame asked.
func (a *App) openKeymapNow(c *ui.Context) {
	if !a.keymapFile.openWanted {
		return
	}
	a.keymapFile.openWanted = false
	p, err := a.st.Path(keymap.FileName)
	if err != nil {
		return
	}
	abs, _ := filepath.Abs(p)
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	c.OpenURLThen(u.String(), func(err error) {
		if err != nil {
			a.ShowError("Could not open "+keymap.FileName, err.Error()+"\n\nIt is "+abs)
		}
	})
}

// record takes a key pressed for the action being recorded: Esc stops; a
// second key makes two in a row, ↵ keeps the first alone; a key another
// action has is refused.
func (k *keysEditor) record(a *App, ev ui.InputEvent) bool {
	if ev.Kind != ui.InputKeyDown || k.recording == "" {
		return false
	}
	if ev.Key == ui.KeyEscape && ev.Mods == 0 {
		k.recording, k.hasFirst = "", false
		return true
	}
	if ev.Key == ui.KeyUnknown {
		return true // a modifier, held for the key to come
	}
	ch := keymap.Chord{Mods: ev.Mods, Key: ev.Key}
	var ks keymap.Keystroke
	switch {
	case !k.hasFirst:
		k.first, k.hasFirst, k.err = ch, true, ""
		return true
	case ev.Key == ui.KeyEnter && ev.Mods == 0:
		ks = keymap.Keystroke{First: k.first}
	default:
		ks = keymap.Keystroke{First: k.first, Then: ch}
	}
	k.hasFirst = false
	if err := keymap.Allowed(k.recording, ks); err != nil {
		k.err = widgets.Sentence(err.Error())
		return true
	}
	bindings := keymap.Bindings(k.recording)
	if !slices.ContainsFunc(bindings, func(b keymap.Binding) bool { return b.Keys == ks }) {
		cmd, _ := keymap.Lookup(k.recording)
		b := keymap.Binding{Keys: ks, Scope: cmd.Scope}
		if cmd.TakesArg {
			b.Arg = 1 // the first; keymap.json gives others
		}
		a.setBindings(k.recording, append(slices.Clone(bindings), b))
	}
	k.recording, k.err = "", ""
	return true
}

func (a *App) keysView(c *ui.Context) {
	k := a.keys
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	widgets.Modal(c, &k.open, func() {
		ui.Column(c).Width(760).Height(620).Gap(10).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Customize Keyboard Shortcuts").FontSize(16).Bold()
				ui.Spacer(c)
				if ui.Button(c, keymap.Hint("Open keymap.json", keymap.OpenKeymap)).Tooltip("The keys you set, as Zed keeps them: contexts, keys of two, actions").Clicked() {
					a.openKeymap()
				}
				ui.TextInput(c, &k.filter).Placeholder("Search actions or keys").Width(240).Label("Search actions").AutoFocus()
			})
			if k.err != "" {
				ui.Text(c, k.err).TextColor(th.Danger).FontSize(12.5)
			}
			for _, e := range a.keymapFile.errs {
				ui.Text(c, keymap.FileName+": "+e).TextColor(th.Warning).FontSize(12)
			}
			filter := strings.ToLower(strings.TrimSpace(k.filter))
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Gap(4).PaddingX(2).Children(func() {
					for _, scope := range keymap.Scopes {
						ui.Text(c, scope.Label()+" · "+string(scope)).Bold().FontSize(12).TextColor(pal.Muted).Padding(8, 0, 2, 0)
						for _, cmd := range keymap.Commands {
							if cmd.Scope != scope || filter != "" && !strings.Contains(strings.ToLower(cmd.Title+" "+cmd.ID+" "+keymap.Label(cmd.ID)), filter) {
								continue
							}
							k.commandRow(a, c, cmd)
						}
					}
				})
			})
			ui.Text(c, "Add records the next key, then a second for two in a row, or ↵ to keep one; Esc stops. Changes are written to "+keymap.FileName+", whose comments they do not keep.").
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
		ui.Column(c).Grow(1).Shrink(1).Children(func() {
			title := ui.Text(c, cmd.Title).FontSize(12.5)
			if keymap.Changed(cmd.ID) {
				title.Bold()
			}
			ui.Text(c, cmd.ID).Font(widgets.MonoFont).FontSize(10.5).TextColor(pal.Muted)
		})
		bindings := keymap.Bindings(cmd.ID)
		for i, b := range bindings {
			label := b.Keys.Label()
			if b.Arg != 0 {
				label += " → " + strconv.Itoa(b.Arg)
			}
			if b.Scope != cmd.Scope {
				label += " (" + b.Scope.Label() + ")"
			}
			ui.Row(c).Gap(2).AlignItems(ui.Center).Padding(1, 2, 1, 6).Radius(4).Background(pal.Hover).Children(func() {
				ui.Text(c, label).Font(widgets.MonoFont).FontSize(11.5)
				if widgets.IconButton(c, widgets.IconX, "Remove "+label).Clicked() {
					a.setBindings(cmd.ID, slices.Delete(slices.Clone(bindings), i, i+1))
				}
			})
		}
		if k.recording == cmd.ID {
			box := widgets.Appear(ui.Box(c.Key("recording")).Focusable().Padding(2, 8).Radius(4).Border(1, c.Theme().Accent).Label("Press the key for " + cmd.Title)).
				HandleInput(func(ev ui.InputEvent) bool { return k.record(a, ev) })
			box.Children(func() {
				text := "Press a key…"
				if k.hasFirst {
					text = k.first.Label() + " — a second key, or ↵"
				}
				ui.Text(c, text).FontSize(11.5).TextColor(pal.Muted)
			})
			if !box.Focused() {
				box.Focus()
			}
		} else if widgets.Appear(ui.Button(c.Key("add"), "Add")).Tooltip("Record a key for this action").Clicked() {
			k.recording, k.err, k.hasFirst = cmd.ID, "", false
		}
		if keymap.Changed(cmd.ID) && widgets.Appear(ui.Button(c.Key("reset"), "Reset")).Tooltip("Back to its own keys").Clicked() {
			k.err = ""
			for _, b := range cmd.Default {
				if slices.Contains(keymap.Bindings(cmd.ID), b) {
					continue
				}
				if err := keymap.Allowed(cmd.ID, b.Keys); err != nil {
					k.err = widgets.Sentence(err.Error())
				}
			}
			if k.err == "" && !a.refuseKeymapWrite() {
				a.saveKeymap(keymap.Reset(cmd.ID))
			}
		}
	})
}

package app

import (
	"strings"

	"dgopher/internal/keymap"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// shortcutRows are the keys of a scope as the list shows them: its
// commands' as they are set, then those that cannot be changed; those
// whose keys or text hold find, ignoring case.
func shortcutRows(scope keymap.Scope, find string) [][2]string {
	var out [][2]string
	find = strings.ToLower(strings.TrimSpace(find))
	add := func(keys, does string) {
		if strings.Contains(strings.ToLower(keys), find) || strings.Contains(strings.ToLower(does), find) {
			out = append(out, [2]string{keys, does})
		}
	}
	for _, cmd := range keymap.Commands {
		if label := keymap.Label(cmd.ID); cmd.Scope == scope && label != "" {
			add(label, cmd.Title)
		}
	}
	for _, f := range keymap.Fixed {
		if f.Scope == scope {
			add(widgets.KeyLabel(f.Keys), f.Does)
		}
	}
	return out
}

func (a *App) shortcutsView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	open := a.shortcutsOpen
	widgets.Dialog(c, &open, widgets.DialogLarge, func(panel ui.Element) {
		_, h := c.Size()
		panel.Height(min(680, h-60)).Label("Keyboard shortcuts")
		widgets.DialogHeader(c, func() {
			widgets.DialogTitle(c, "Keyboard Shortcuts").Grow(1)
			ui.Text(c, "Change them in Settings · "+keymap.Label(keymap.ShortcutsList)+" or Esc to close").FontSize(widgets.TextSmall).TextColor(pal.Muted)
		}).AlignItems(ui.Baseline)
		ui.Row(c).Padding(10, 20, 0, 20).Children(func() {
			widgets.SearchBox(c, &a.shortcutsFind, "Find a key or a command", 0).AutoFocus()
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Grid(c).Columns(2).GapX(28).GapY(18).Padding(16, 20).Children(func() {
				for _, scope := range keymap.Scopes {
					rows := shortcutRows(scope, a.shortcutsFind)
					if len(rows) == 0 {
						continue
					}
					ui.Column(c).Gap(6).Children(func() {
						ui.Text(c, scope.Label()).Bold().TextColor(pal.Muted).FontSize(widgets.TextSmall)
						for _, k := range rows {
							ui.Row(c).Gap(10).AlignItems(ui.Start).Children(func() {
								ui.Text(c, k[0]).Font(widgets.MonoFont).FontSize(widgets.TextMeta).Padding(2, 6).Radius(widgets.RadiusSmall).Background(pal.Hover).MinWidth(64)
								ui.Text(c, widgets.KeyLabel(k[1])).FontSize(widgets.TextBody).Grow(1).Shrink(1)
							})
						}
					})
				}
			})
		})
	})
	if !open {
		a.shortcutsFind = ""
	}
	a.shortcutsOpen = open
}

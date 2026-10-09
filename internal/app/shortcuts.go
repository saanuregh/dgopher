package app

import (
	"dgopher/internal/keymap"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// shortcutRows are the keys of a scope as the list shows them: its
// commands' as they are set, then those that cannot be changed.
func shortcutRows(scope keymap.Scope) [][2]string {
	var out [][2]string
	for _, cmd := range keymap.Commands {
		if label := keymap.Label(cmd.ID); cmd.Scope == scope && label != "" {
			out = append(out, [2]string{label, cmd.Title})
		}
	}
	for _, f := range keymap.Fixed {
		if f.Scope == scope {
			out = append(out, [2]string{widgets.KeyLabel(f.Keys), f.Does})
		}
	}
	return out
}

func (a *App) shortcutsView(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	open := a.shortcutsOpen
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(widgets.Backdrop)
		_, h := c.Size()
		panel.Width(760).Height(min(680, h-60)).Radius(12).Background(th.Background).Border(1, th.Border).Clip().Label("Keyboard shortcuts")
		ui.Row(c).Padding(14, 20).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, "Keyboard Shortcuts").FontSize(16).Bold().Grow(1)
			ui.Text(c, "Change them in Settings · "+keymap.Label(keymap.ShortcutsList)+" or Esc to close").FontSize(12).TextColor(pal.Muted)
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Grid(c).Columns(2).GapX(28).GapY(18).Padding(16, 20).Children(func() {
				for _, scope := range keymap.Scopes {
					ui.Column(c).Gap(6).Children(func() {
						ui.Text(c, string(scope)).Bold().TextColor(pal.Muted).FontSize(12)
						for _, k := range shortcutRows(scope) {
							ui.Row(c).Gap(10).AlignItems(ui.Start).Children(func() {
								ui.Text(c, k[0]).Font(widgets.MonoFont).FontSize(11.5).Padding(2, 6).Radius(4).Background(pal.Hover).MinWidth(64)
								ui.Text(c, widgets.KeyLabel(k[1])).FontSize(12.5).Grow(1).Shrink(1)
							})
						}
					})
				}
			})
		})
	})
	a.shortcutsOpen = open
}

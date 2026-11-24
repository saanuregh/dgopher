package app

import (
	"dgopher/internal/changelog"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// Changelog is CHANGELOG.md, which the main package embeds and sets
// before Run.
var Changelog string

// appVersion is the running version, "" when unknown. Run sets it to the
// bundle's; tests set their own.
var appVersion = func() string { return "" }

// checkWhatsNew records the running version as seen, first opening its
// release notes when it is newer than the version that ran before. A first
// install, which had no settings file, and a downgrade show nothing; so
// does a launch whose start page offers the tour, which comes first. It
// runs after the workspaces are restored: with tabs, the start page does
// not show.
func (a *App) checkWhatsNew(firstInstall bool) {
	v := appVersion()
	notes := changelog.Section(Changelog, v)
	if v == "" || notes == "" || a.settings.LastSeenVersion == v {
		return
	}
	tourBanner := !a.settings.TourDone && a.main.tabCount() == 0
	if !firstInstall && !tourBanner &&
		(a.settings.LastSeenVersion == "" || changelog.Newer(v, a.settings.LastSeenVersion)) {
		a.whatsNewOpen, a.whatsNewNotes = true, notes
	}
	a.settings.LastSeenVersion = v
	a.SaveSettings()
}

// openWhatsNew shows the running version's release notes, when it has any.
func (a *App) openWhatsNew() {
	a.whatsNewNotes = changelog.Section(Changelog, appVersion())
	a.whatsNewOpen = a.whatsNewNotes != ""
}

func (a *App) whatsNewView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	v := appVersion()
	open := a.whatsNewOpen
	widgets.Dialog(c, &open, widgets.DialogMedium, func(panel ui.Element) {
		_, h := c.Size()
		panel.MaxHeight(min(680, h-60)).Label("What's New in DGopher")
		widgets.DialogHeader(c, func() {
			widgets.DialogTitle(c, "What's New in DGopher "+v).Grow(1)
			ui.Text(c, "Esc to close").FontSize(widgets.TextSmall).TextColor(pal.Muted)
		}).AlignItems(ui.Baseline)
		ui.Scroll(c).Shrink(1).Children(func() {
			widgets.DialogBody(c, func() {
				markdown.View(c, a.whatsNewNotes, &markdown.Options{WrapCode: true}).FillWidth()
			}).FillWidth()
		})
		widgets.DialogActions(c, func() {
			ui.Spacer(c)
			if ui.Button(c, "Close").Clicked() {
				open = false
			}
		})
	})
	a.whatsNewOpen = open
}

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
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	v := appVersion()
	open := a.whatsNewOpen
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		widgets.DialogLook(c, backdrop, panel)
		_, h := c.Size()
		panel.Width(760).MaxHeight(min(680, h-60)).Radius(12).Background(th.Background).Border(1, th.Border).Clip().Label("What's New in DGopher")
		ui.Row(c).Padding(14, 20).AlignItems(ui.Baseline).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, "What's New in DGopher "+v).FontSize(16).Bold().Grow(1)
			ui.Text(c, "Esc to close").FontSize(12).TextColor(pal.Muted)
		})
		ui.Scroll(c).Shrink(1).Children(func() {
			ui.Column(c).Padding(16, 20).FillWidth().Children(func() {
				markdown.View(c, a.whatsNewNotes, &markdown.Options{WrapCode: true}).FillWidth()
			})
		})
		ui.Row(c).Padding(10, 20).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
			ui.Spacer(c)
			if ui.Button(c, "Close").Clicked() {
				open = false
			}
		})
	})
	a.whatsNewOpen = open
}

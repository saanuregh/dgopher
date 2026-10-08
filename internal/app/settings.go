package app

import (
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var themeLabels = []string{"System", "Light", "Dark"}

func themeIndex(s string) int {
	switch s {
	case "light":
		return 1
	case "dark":
		return 2
	}
	return 0
}

// applyTheme follows the theme the user chose.
func applyTheme(s string) {
	switch s {
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

func (a *App) settingsView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	open := a.settingsOpen
	theme := themeIndex(a.settings.Theme)
	font := float64(a.settings.EditorFont)
	page := float64(a.settings.PageSize)
	notifyAfter := float64(a.settings.NotifyAfter)
	ui.Modal(c, &open, func() {
		ui.Column(c).Width(520).Gap(14).Children(func() {
			ui.Text(c, "Settings").FontSize(16).Bold()
			ui.Form(c, func() {
				ui.Field(c, "Appearance", func() {
					if ui.Segmented(c, &theme, themeLabels...).Changed() {
						a.settings.Theme = []string{"system", "light", "dark"}[theme]
						applyTheme(a.settings.Theme)
						a.SaveSettings()
					}
				})
				ui.Field(c, "Editor font size", func() {
					if ui.NumberInput(c, &font, 9, 24, 1).Changed() {
						a.settings.EditorFont = float32(font)
						a.SaveSettings()
					}
				})
				ui.Field(c, "Rows per page", func() {
					if ui.NumberInput(c, &page, 50, 10000, 50).Changed() {
						a.settings.PageSize = int(page)
						a.SaveSettings()
					}
				}).Description("Results load this many rows at a time as you scroll.")
				ui.Field(c, "Statements end", func() {
					blank := !a.settings.SemicolonOnly
					if ui.Checkbox(c, &blank, "At a blank line, as well as at ;").Changed() {
						a.settings.SemicolonOnly = !blank
						a.SaveSettings()
					}
				}).Description("As in DBeaver: ⌘↵ runs the statement around the caret, up to the blank lines around it.")
				ui.Field(c, "On error in a script", func() {
					cont := 0
					if a.settings.ContinueOnError {
						cont = 1
					}
					if ui.Segmented(c, &cont, "Stop", "Continue").Changed() {
						a.settings.ContinueOnError = cont == 1
						a.SaveSettings()
					}
				})
				ui.Field(c, "Notifications", func() {
					ui.Row(c).Gap(8).Children(func() {
						if ui.NumberInput(c, &notifyAfter, 0, 3600, 5).Label("Seconds").Changed() {
							a.settings.NotifyAfter = int(notifyAfter)
							a.SaveSettings()
						}
						ui.Text(c, "seconds, 0 for never").TextColor(pal.Muted)
					})
				}).Description("A statement, script, export or import that takes this long tells the system when it ends, if DGopher is in the background then.")
				ui.Field(c, "Data", func() {
					ui.Column(c).Gap(6).Children(func() {
						ui.Text(c, a.st.Dir()).Font(widgets.MonoFont).FontSize(12).Selectable()
						ui.Text(c, "App settings, the destinations you trusted, and SSH host keys. Everything else is in each project's folder: "+project.File+" and queries to share, and "+project.LocalDir+"/ (ignored by Git) for history, open files and the audit log.").FontSize(12).TextColor(pal.Muted)
						keychain := "Passwords are kept in the system keychain."
						if !a.st.Secrets().Available() {
							keychain = "No system keychain is available: passwords are never saved, and asked on connect."
						}
						ui.Text(c, keychain).FontSize(12).TextColor(pal.Muted)
						ui.Row(c).Gap(8).Children(func() {
							if ui.Button(c, "Show in Folder").Clicked() {
								mygo.Shell.OpenPath(a.st.Dir())
							}
						})
					})
				})
			})
			ui.Row(c).Justify(ui.End).Children(func() {
				if ui.PrimaryButton(c, "Done").Clicked() {
					open = false
				}
			})
		})
	})
	a.settingsOpen = open
}

package app

import (
	"cmp"
	"log"
	"os"
	"slices"
	"strings"

	"dgopher/internal/keymap"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// appearances are the appearances the app offers: following the system,
// light or dark, then the themes by their names.
var appearances = []string{"System", "Light", "Dark"}

// openSettings opens the settings, the themes read again.
func (a *App) openSettings() {
	a.loadThemes()
	a.settingsOpen = true
}

// appearanceOf is the label of the setting's appearance.
func appearanceOf(s string) string {
	switch s {
	case "system", "":
		return "System"
	case "light":
		return "Light"
	case "dark":
		return "Dark"
	}
	return s
}

// loadThemes reads the user's themes, the built-in ones first; a file
// that does not read is logged.
func (a *App) loadThemes() {
	themes, errs := widgets.LoadThemes(widgets.ThemesDir(a.st.Dir()))
	for _, err := range errs {
		log.Println("themes:", err)
	}
	a.themes = themes
}

// applyTheme follows the appearance the user chose: the system's, light,
// dark, or a theme, in its own light or dark.
func (a *App) applyTheme() {
	s := a.settings.Theme
	source := map[string]mygo.ThemeSource{"light": mygo.ThemeLight, "dark": mygo.ThemeDark}[s]
	var theme *widgets.Theme
	if i := slices.IndexFunc(a.themes, func(t widgets.Theme) bool { return t.Name == s }); i >= 0 {
		theme = &a.themes[i]
		source = mygo.ThemeLight
		if theme.Base == "dark" {
			source = mygo.ThemeDark
		}
	} else if source == "" {
		source = mygo.ThemeSystem // as for a theme taken away since
	}
	widgets.UseTheme(theme)
	mygo.Theme.SetSource(source)
	widgets.MonoFont = cmp.Or(strings.TrimSpace(a.settings.EditorFontFamily), widgets.DefaultMonoFont)
}

// monoFamilies and uiFamilies are families to suggest: the app cannot
// list the system's.
var (
	monoFamilies = []string{"JetBrains Mono", "Fira Code", "SF Mono", "Menlo", "Monaco", "Consolas", "Cascadia Code",
		"Source Code Pro", "IBM Plex Mono", "Ubuntu Mono", "DejaVu Sans Mono", "Hack"}
	uiFamilies = []string{"Inter", "SF Pro Text", "Helvetica Neue", "Segoe UI", "Roboto", "Noto Sans", "Ubuntu", "Cantarell", "IBM Plex Sans"}
)

func (a *App) settingsView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	open := a.settingsOpen
	appearance := appearanceOf(a.settings.Theme)
	choices := slices.Clone(appearances)
	for _, t := range a.themes {
		choices = append(choices, t.Name)
	}
	font := float64(a.settings.EditorFont)
	page := float64(a.settings.PageSize)
	notifyAfter := float64(a.settings.NotifyAfter)
	ui.Modal(c, &open, func() {
		ui.Column(c).Width(520).Gap(14).Children(func() {
			ui.Text(c, "Settings").FontSize(16).Bold()
			ui.Form(c, func() {
				ui.Field(c, "Appearance", func() {
					ui.Row(c).Gap(8).Children(func() {
						if ui.Select(c, &appearance, choices).Label("Appearance").Width(220).Changed() {
							a.settings.Theme = appearance
							if i := slices.Index(appearances, appearance); i >= 0 {
								a.settings.Theme = []string{"system", "light", "dark"}[i]
							}
							a.applyTheme()
							a.SaveSettings()
						}
						if ui.Button(c, "Themes Folder").Tooltip("Add a theme as a JSON file there: see the documentation").Clicked() {
							dir := widgets.ThemesDir(a.st.Dir())
							if err := os.MkdirAll(dir, 0o755); err != nil {
								a.ShowError("Could not make the themes folder", err.Error())
							} else {
								mygo.Shell.OpenPath(dir)
							}
						}
					})
				}).Description("A theme is light or dark. The themes folder's are read as Settings opens.")
				ui.Field(c, "Fonts", func() {
					ui.Row(c).Gap(8).Children(func() {
						if ui.Autocomplete(c, &a.settings.EditorFontFamily, monoFamilies).Placeholder("Monospace, the system's").Label("Editor font").Width(220).Changed() {
							a.applyTheme()
							a.settingsDirty = true
						}
						if ui.Autocomplete(c, &a.settings.UIFontFamily, uiFamilies).Placeholder("The system's").Label("Interface font").Width(200).Changed() {
							a.settingsDirty = true
						}
					})
				}).Description("A family the system lacks shows in its own font.")
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
				}).Description("As in DBeaver: " + keymap.Hint("running a statement", keymap.Run) + " runs the one around the caret, up to the blank lines around it.")
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
				ui.Field(c, "Sensitive values", func() {
					hide := !a.settings.ShowSensitive
					if ui.Checkbox(c, &hide, "Hide the values of columns that look sensitive").Changed() {
						a.settings.ShowSensitive = !hide
						a.SaveSettings()
					}
				}).Description("Passwords, tokens, keys and card numbers show as •••••• in the grids. Any column's values can be hidden or shown from its header's menu.")
				ui.Field(c, "Notifications", func() {
					ui.Row(c).Gap(8).Children(func() {
						if ui.NumberInput(c, &notifyAfter, 0, 3600, 5).Label("Seconds").Changed() {
							a.settings.NotifyAfter = int(notifyAfter)
							a.SaveSettings()
						}
						ui.Text(c, "seconds, 0 for never").TextColor(pal.Muted)
					})
				}).Description("A statement, script, export or import that takes this long tells the system when it ends, if DGopher is in the background then.")
				ui.Field(c, "Editor keys", func() {
					if ui.Checkbox(c, &a.settings.Vim, "Vim key bindings").Changed() {
						a.SaveSettings()
					}
				}).Description("Normal, insert and visual modes, motions, operators, text objects, . and :w.")
				ui.Field(c, "Keys", func() {
					if ui.Button(c, "Keyboard Shortcuts…").Clicked() {
						open = false
						a.keys = &keysEditor{open: true}
					}
				}).Description("Change the keys of the app's commands.")
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

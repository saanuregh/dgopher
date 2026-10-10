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
	// Light and dark are themes of their base alone: the app shows them
	// itself, as a system that stays dark would not.
	var theme *widgets.Theme
	if i := slices.IndexFunc(a.themes, func(t widgets.Theme) bool { return t.Name == s }); i >= 0 {
		theme = &a.themes[i]
		source = mygo.ThemeLight
		if theme.Base == "dark" {
			source = mygo.ThemeDark
		}
	} else if source != "" {
		theme = &widgets.Theme{Base: s}
	} else {
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

// The pages of the settings, as their tabs name them.
var settingsPages = []string{"Appearance", "Editor", "Results", "Safety", "Storage"}

func (a *App) settingsView(c *ui.Context) {
	th := c.Theme()
	open := a.settingsOpen
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(widgets.Backdrop)
		_, winH := c.Size()
		panel.Width(640).Height(min(620, winH-40)).Radius(12).Background(th.Background).Border(1, th.Border).
			Shadow(0, 12, 40, 0, ui.RGBA(0, 0, 0, 0.3)).Role(ui.RoleDialog).Label("Settings")
		ui.Row(c).Padding(16, 20, 8, 20).Children(func() {
			ui.Text(c, "Settings").FontSize(16).Bold()
		})
		ui.Row(c).Padding(0, 20).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Tabs(c, &a.settingsPage, settingsPages...).Label("Settings pages")
		})
		ui.Scroll(c).Grow(1).Shrink(1).Children(func() {
			ui.Form(c, func() {
				switch settingsPages[a.settingsPage] {
				case "Appearance":
					a.appearancePage(c)
				case "Editor":
					a.editorPage(c, &open)
				case "Results":
					a.resultsPage(c)
				case "Safety":
					a.safetyPage(c)
				case "Storage":
					a.storagePage(c)
				}
			}).Padding(16, 20)
		})
		ui.Row(c).Padding(12, 20).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Justify(ui.End).Children(func() {
			if widgets.Activated(c, ui.PrimaryButton(c, "Done")) {
				open = false
			}
		})
	})
	a.settingsOpen = open
}

// appearancePage chooses the theme and the fonts.
func (a *App) appearancePage(c *ui.Context) {
	appearance := appearanceOf(a.settings.Theme)
	choices := slices.Clone(appearances)
	for _, t := range a.themes {
		choices = append(choices, t.Name)
	}
	ui.Field(c, "Theme", func() {
		ui.Row(c).Gap(8).Children(func() {
			if ui.Select(c, &appearance, choices).Label("Theme").Width(240).Changed() {
				a.settings.Theme = appearance
				if i := slices.Index(appearances, appearance); i >= 0 {
					a.settings.Theme = []string{"system", "light", "dark"}[i]
				}
				a.applyTheme()
				a.SaveSettings()
			}
			if ui.Button(c, "Themes Folder").Tooltip("Add a theme as a JSON file there: see the documentation").Clicked() {
				if dir, err := a.makeThemesFolder(); err != nil {
					a.ShowError("Could not make the themes folder", err.Error())
				} else {
					mygo.Shell.OpenPath(dir)
				}
			}
		})
	}).Description("System follows the system's appearance. A theme is light or dark; the themes folder's are read as Settings opens.")
	ui.Field(c, "Editor font", func() {
		if ui.Autocomplete(c, &a.settings.EditorFontFamily, monoFamilies).Placeholder("Monospace, the system's").Label("Editor font").Width(240).Changed() {
			a.applyTheme()
			a.settingsDirty = true
		}
	})
	font := float64(a.settings.EditorFont)
	ui.Field(c, "Editor font size", func() {
		if ui.NumberInput(c, &font, 9, 24, 1).Changed() {
			a.settings.EditorFont = float32(font)
			a.SaveSettings()
		}
	})
	ui.Field(c, "Interface font", func() {
		if ui.Autocomplete(c, &a.settings.UIFontFamily, uiFamilies).Placeholder("The system's").Label("Interface font").Width(240).Changed() {
			a.settingsDirty = true
		}
	}).Description("A family the system lacks shows in its own font.")
}

// makeThemesFolder makes the folder of the user's themes, when it is
// not there yet, the user's alone as the rest of the config.
func (a *App) makeThemesFolder() (string, error) {
	dir := widgets.ThemesDir(a.st.Dir())
	return dir, os.MkdirAll(dir, 0o700)
}

// editorPage sets how the SQL editor runs statements and takes keys.
func (a *App) editorPage(c *ui.Context, open *bool) {
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
	ui.Field(c, "Vim", func() {
		if ui.Checkbox(c, &a.settings.Vim, "Vim key bindings").Changed() {
			a.SaveSettings()
		}
	}).Description("Normal, insert and visual modes, motions, operators, text objects, . and :w.")
	ui.Field(c, "Shortcuts", func() {
		if ui.Button(c, "Customize Keyboard Shortcuts…").Clicked() {
			*open = false
			a.keys = &keysEditor{open: true}
		}
	}).Description("Change the keys of the app's commands.")
}

// resultsPage sets how results read and tell they are done.
func (a *App) resultsPage(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	page := float64(a.settings.PageSize)
	ui.Field(c, "Rows per page", func() {
		if ui.NumberInput(c, &page, 50, 10000, 50).Changed() {
			a.settings.PageSize = int(page)
			a.SaveSettings()
		}
	}).Description("Results load this many rows at a time as you scroll.")
	notifyAfter := float64(a.settings.NotifyAfter)
	ui.Field(c, "Notifications", func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.NumberInput(c, &notifyAfter, 0, 3600, 5).Label("Seconds").Changed() {
				a.settings.NotifyAfter = int(notifyAfter)
				a.SaveSettings()
			}
			ui.Text(c, "seconds, 0 for never").TextColor(pal.Muted)
		})
	}).Description("A statement, script, export or import that takes this long tells the system when it ends, if DGopher is in the background then.")
}

// safetyPage sets what asks before it changes rows, and what values hide.
func (a *App) safetyPage(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	limit := float64(a.settings.ChangeLimit)
	ui.Field(c, "Large changes", func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.NumberInput(c, &limit, 0, 1_000_000, 100).Label("Rows a change makes without asking").Changed() {
				a.settings.ChangeLimit = int(limit)
				a.SaveSettings()
			}
			ui.Text(c, "rows, 0 for no limit").TextColor(pal.Muted)
		})
	}).Description("On staging and production in auto-commit, an UPDATE, DELETE, MERGE, REPLACE or upsert that changes more rows than this asks before it commits; until then it is held in a transaction.")
	ui.Field(c, "Sensitive values", func() {
		hide := !a.settings.ShowSensitive
		if ui.Checkbox(c, &hide, "Hide the values of columns that look sensitive").Changed() {
			a.settings.ShowSensitive = !hide
			a.SaveSettings()
		}
	}).Description("Passwords, tokens, keys and card numbers show as •••••• in the grids. Any column's values can be hidden or shown from its header's menu.")
}

// storagePage says where the app keeps what it keeps.
func (a *App) storagePage(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	ui.Field(c, "Folder", func() {
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, a.st.Dir()).Font(widgets.MonoFont).FontSize(12).Selectable()
			ui.Text(c, "App settings, the destinations you trusted, and SSH host keys. Everything else is in each project's folder: "+project.File+" and queries to share, and "+project.LocalDir+"/ (ignored by Git) for history, open files and the audit log.").FontSize(12).TextColor(pal.Muted)
			ui.Row(c).Children(func() {
				if ui.Button(c, "Show in Folder").Clicked() {
					mygo.Shell.OpenPath(a.st.Dir())
				}
			})
		})
	})
	keychain := "Passwords are kept in the system keychain."
	if !a.st.Secrets().Available() {
		keychain = "No system keychain is available: passwords are never saved, and asked on connect."
	}
	ui.Field(c, "Passwords", func() {
		ui.Text(c, keychain).FontSize(12.5)
	})
}

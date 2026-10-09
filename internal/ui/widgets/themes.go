package widgets

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"dgopher/internal/sqltext"

	"github.com/egoist/mygo/ui"
)

// Theme is a look of the app: the light or dark one, with colors of its
// own in place of some of theirs, by their names (ThemeColors). A user's
// is a file of the themes folder, as the built-in ones are written.
type Theme struct {
	Name   string            `json:"name"`
	Base   string            `json:"base"` // "light" or "dark"
	Colors map[string]string `json:"colors"`
}

// themeColor sets a color of the app's theme, or of the palette.
type themeColor func(t *ui.Theme, p *Palette, c ui.Color)

// themeColors are the colors a theme may set, by their names.
var themeColors = map[string]themeColor{
	"background":       func(t *ui.Theme, _ *Palette, c ui.Color) { t.Background = c },
	"surface":          func(t *ui.Theme, _ *Palette, c ui.Color) { t.Surface = c },
	"surfaceHover":     func(t *ui.Theme, _ *Palette, c ui.Color) { t.SurfaceHover = c },
	"surfacePressed":   func(t *ui.Theme, _ *Palette, c ui.Color) { t.SurfacePressed = c },
	"border":           func(t *ui.Theme, _ *Palette, c ui.Color) { t.Border = c },
	"text":             func(t *ui.Theme, _ *Palette, c ui.Color) { t.Text = c },
	"textMuted":        func(t *ui.Theme, _ *Palette, c ui.Color) { t.TextMuted = c },
	"accent":           func(t *ui.Theme, _ *Palette, c ui.Color) { t.Accent = c },
	"accentText":       func(t *ui.Theme, _ *Palette, c ui.Color) { t.AccentText = c },
	"danger":           func(t *ui.Theme, _ *Palette, c ui.Color) { t.Danger = c },
	"warning":          func(t *ui.Theme, _ *Palette, c ui.Color) { t.Warning = c },
	"success":          func(t *ui.Theme, _ *Palette, c ui.Color) { t.Success = c },
	"selection":        func(t *ui.Theme, _ *Palette, c ui.Color) { t.Selection = c },
	"focus":            func(t *ui.Theme, _ *Palette, c ui.Color) { t.Focus = c },
	"sidebar":          func(_ *ui.Theme, p *Palette, c ui.Color) { p.Sidebar = c },
	"editor":           func(_ *ui.Theme, p *Palette, c ui.Color) { p.EditorBg = c },
	"currentStatement": func(_ *ui.Theme, p *Palette, c ui.Color) { p.CurrentStatement = c },
	"gutter":           func(_ *ui.Theme, p *Palette, c ui.Color) { p.Gutter = c },
	"lineNumber":       func(_ *ui.Theme, p *Palette, c ui.Color) { p.LineNumber = c },
	"gridHeader":       func(_ *ui.Theme, p *Palette, c ui.Color) { p.GridHeader = c },
	"gridLine":         func(_ *ui.Theme, p *Palette, c ui.Color) { p.GridLine = c },
	"cellSelected":     func(_ *ui.Theme, p *Palette, c ui.Color) { p.CellSelected = c },
	"null":             func(_ *ui.Theme, p *Palette, c ui.Color) { p.Null = c },
	"modified":         func(_ *ui.Theme, p *Palette, c ui.Color) { p.Modified = c },
	"inserted":         func(_ *ui.Theme, p *Palette, c ui.Color) { p.Inserted = c },
	"deleted":          func(_ *ui.Theme, p *Palette, c ui.Color) { p.Deleted = c },
	"muted":            func(_ *ui.Theme, p *Palette, c ui.Color) { p.Muted = c },
	"hover":            func(_ *ui.Theme, p *Palette, c ui.Color) { p.Hover = c },
	"keyword":          syntaxColor(sqltext.Keyword),
	"string":           syntaxColor(sqltext.String),
	"number":           syntaxColor(sqltext.Number),
	"comment":          syntaxColor(sqltext.Comment),
	"quotedIdentifier": syntaxColor(sqltext.QuotedIdent),
	"parameter":        syntaxColor(sqltext.Param),
	"operator":         syntaxColor(sqltext.Operator),
}

func syntaxColor(k sqltext.Kind) themeColor {
	return func(_ *ui.Theme, p *Palette, c ui.Color) { p.Syntax[k] = c }
}

var hexColor = regexp.MustCompile(`^#([0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

// Check says what of a theme is wrong, as a file written by hand.
func (t Theme) Check() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("the theme has no name")
	}
	if t.Base != "light" && t.Base != "dark" {
		return fmt.Errorf("the base of %s is %q: light or dark", t.Name, t.Base)
	}
	for name, value := range t.Colors {
		if themeColors[name] == nil {
			return fmt.Errorf("%s: %q is no color a theme sets", t.Name, name)
		}
		if !hexColor.MatchString(value) {
			return fmt.Errorf("%s: %s is %q, not a color as #rrggbb or #rrggbbaa", t.Name, name, value)
		}
	}
	return nil
}

// BuiltinThemes are the themes the app comes with.
var BuiltinThemes = []Theme{
	{Name: "Nord", Base: "dark", Colors: map[string]string{
		"background": "#2e3440", "surface": "#3b4252", "surfaceHover": "#434c5e", "surfacePressed": "#4c566a",
		"border": "#4c566a", "text": "#eceff4", "textMuted": "#a3acbd", "accent": "#88c0d0", "accentText": "#2e3440",
		"danger": "#bf616a", "warning": "#ebcb8b", "success": "#a3be8c", "selection": "#5e81ac66", "focus": "#88c0d0",
		"sidebar": "#2b303b", "editor": "#2e3440", "gutter": "#2e3440", "lineNumber": "#616e88", "currentStatement": "#353c4a",
		"gridHeader": "#3b4252", "muted": "#a3acbd", "null": "#616e88",
		"keyword": "#81a1c1", "string": "#a3be8c", "number": "#b48ead", "comment": "#616e88",
		"quotedIdentifier": "#8fbcbb", "parameter": "#d08770", "operator": "#d8dee9",
	}},
	{Name: "Dracula", Base: "dark", Colors: map[string]string{
		"background": "#282a36", "surface": "#343746", "surfaceHover": "#3e4152", "surfacePressed": "#44475a",
		"border": "#44475a", "text": "#f8f8f2", "textMuted": "#9ea8c7", "accent": "#bd93f9", "accentText": "#282a36",
		"danger": "#ff5555", "warning": "#f1fa8c", "success": "#50fa7b", "selection": "#44475acc", "focus": "#bd93f9",
		"sidebar": "#21222c", "editor": "#282a36", "gutter": "#282a36", "lineNumber": "#6272a4", "currentStatement": "#2f3242",
		"gridHeader": "#343746", "muted": "#9ea8c7", "null": "#6272a4",
		"keyword": "#ff79c6", "string": "#f1fa8c", "number": "#bd93f9", "comment": "#6272a4",
		"quotedIdentifier": "#8be9fd", "parameter": "#ffb86c", "operator": "#f8f8f2",
	}},
	{Name: "Solarized Light", Base: "light", Colors: map[string]string{
		"background": "#fdf6e3", "surface": "#eee8d5", "surfaceHover": "#e6dfca", "surfacePressed": "#ddd6c1",
		"border": "#d6cdb5", "text": "#586e75", "textMuted": "#839496", "accent": "#268bd2", "accentText": "#fdf6e3",
		"danger": "#dc322f", "warning": "#b58900", "success": "#859900", "selection": "#268bd233", "focus": "#268bd2",
		"sidebar": "#eee8d5", "editor": "#fdf6e3", "gutter": "#eee8d5", "lineNumber": "#93a1a1", "currentStatement": "#f5eedb",
		"gridHeader": "#eee8d5", "muted": "#839496", "null": "#93a1a1",
		"keyword": "#859900", "string": "#2aa198", "number": "#d33682", "comment": "#93a1a1",
		"quotedIdentifier": "#268bd2", "parameter": "#cb4b16", "operator": "#657b83",
	}},
}

// ThemesDir is the folder of the user's themes, in the app's data folder.
func ThemesDir(dataDir string) string { return filepath.Join(dataDir, "themes") }

// LoadThemes reads the user's themes from their folder, after the
// built-in ones: one of a built-in's name takes its place. A file that
// does not read is reported, and left out.
func LoadThemes(dir string) ([]Theme, []error) {
	out := slices.Clone(BuiltinThemes)
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(files)
	var errs []error
	for _, f := range files {
		data, err := os.ReadFile(f)
		var t Theme
		if err == nil {
			err = json.Unmarshal(data, &t)
		}
		if err == nil {
			err = t.Check()
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(f), err))
			continue
		}
		if i := slices.IndexFunc(out, func(o Theme) bool { return o.Name == t.Name }); i >= 0 {
			out[i] = t
		} else {
			out = append(out, t)
		}
	}
	return out, errs
}

// look is the theme in use, as the app's theme and the palette made of
// the base's with its colors; nil to follow the system's appearance.
var look *struct {
	theme ui.Theme
	pal   Palette
}

// root is the theme the window's root uses, kept for the frame.
var root ui.Theme

// UseTheme puts a theme to use, over the light or dark one as its Base
// says; nil for none.
func UseTheme(t *Theme) {
	if t == nil {
		look = nil
		return
	}
	pal, th := lightPalette, *ui.LightTheme()
	if t.Base == "dark" {
		pal, th = darkPalette, *ui.DarkTheme()
	}
	pal.Syntax = maps.Clone(pal.Syntax)
	for name, value := range t.Colors {
		themeColors[name](&th, &pal, ui.Hex(value))
	}
	// Hovered and pressed, as the accent the theme gave.
	if _, ok := t.Colors["accent"]; ok {
		th.AccentHover, th.AccentPressed = th.Accent.Mix(th.Text, 0.12), th.Accent.Mix(th.Text, 0.24)
	}
	look = &struct {
		theme ui.Theme
		pal   Palette
	}{th, pal}
}

// ApplyTheme sets the window's theme for the frame: the theme in use,
// whatever the window's darkness, which a system's dark appearance may
// keep though a light one was asked for, as on Linux; with the interface
// font, "" for the system's.
func ApplyTheme(c *ui.Context, font string) {
	root = *c.Theme()
	if look != nil {
		root = look.theme
	}
	root.Font = font
	c.SetTheme(&root)
}

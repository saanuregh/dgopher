package widgets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/sqltext"

	"github.com/egoist/mygo/ui"
)

// The built-in themes read as a user's file would, and set colors the
// app has.
func TestBuiltinThemes(t *testing.T) {
	for _, th := range BuiltinThemes {
		if err := th.Check(); err != nil {
			t.Error(err)
		}
	}
}

// A theme file of the themes folder is read, one of a built-in's name in
// its place; a broken one is reported and left out.
func TestLoadThemes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("mine.json", `{"name": "Mine", "base": "light", "colors": {"accent": "#ff0000", "keyword": "#00ff00"}}`)
	write("nord.json", `{"name": "Nord", "base": "dark", "colors": {"accent": "#123456"}}`)
	write("bad.json", `{"name": "Bad", "base": "light", "colors": {"accent": "red"}}`)
	write("worse.json", `{"name": "Worse", "base": "sepia"}`)
	themes, errs := LoadThemes(dir)
	if len(errs) != 2 || !strings.Contains(errs[0].Error(), "bad.json") {
		t.Fatalf("errors %v", errs)
	}
	names := map[string]Theme{}
	for _, th := range themes {
		names[th.Name] = th
	}
	if _, ok := names["Mine"]; !ok || names["Nord"].Colors["accent"] != "#123456" || len(themes) != len(BuiltinThemes)+1 {
		t.Fatalf("themes %+v", themes)
	}

	mine := names["Mine"]
	UseTheme(&mine)
	defer UseTheme(nil)
	var keyword ui.Color
	var accent ui.Color
	tt := ui.NewTester(func(c *ui.Context) {
		ApplyTheme(c, "")
		accent, keyword = c.Theme().Accent, PaletteOf(c).Syntax[sqltext.Keyword]
	}, 100, 100)
	tt.Frame()
	if HexColor(accent) != "#ff0000" || HexColor(keyword) != "#00ff00" {
		t.Fatalf("accent %s, keyword %s", HexColor(accent), HexColor(keyword))
	}
	if HexColor(lightPalette.Syntax[sqltext.Keyword]) == "#00ff00" {
		t.Fatal("the theme changed the light palette")
	}
}

// A light theme shows light though the window stays dark, as a dark
// desktop keeps a window on Linux.
func TestLightThemeOnDarkWindow(t *testing.T) {
	defer UseTheme(nil)
	for _, theme := range []*Theme{{Base: "light"}, &BuiltinThemes[2]} {
		UseTheme(theme)
		var dark bool
		var pal *Palette
		tt := ui.NewTester(func(c *ui.Context) {
			ApplyTheme(c, "")
			dark, pal = c.Theme().Dark, PaletteOf(c)
			ui.Text(c, "x")
		}, 200, 100)
		tt.SetDark(true)
		tt.Frame()
		if dark || pal.EditorBg == darkPalette.EditorBg {
			t.Errorf("%q showed dark", theme.Name)
		}
	}
}

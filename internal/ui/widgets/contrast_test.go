package widgets

import (
	"testing"

	"dgopher/internal/sqltext"

	"github.com/egoist/mygo/ui"
)

// The light and dark themes the app starts with keep their text readable:
// 4.5 to 1 against what it is drawn on, as WCAG's AA asks of text, and 3
// to 1 for line numbers, which are not read as text is.
func TestBuiltInThemesMeetContrast(t *testing.T) {
	defer UseTheme(nil)
	UseTheme(nil)
	for _, dark := range []bool{false, true} {
		var th ui.Theme
		var pal Palette
		tt := ui.NewTester(func(c *ui.Context) {
			ApplyTheme(c, "")
			th, pal = *c.Theme(), *PaletteOf(c)
			ui.Text(c, "x")
		}, 200, 100)
		tt.SetDark(dark)
		tt.Frame()
		name := "light"
		if dark {
			name = "dark"
		}
		// What text is drawn on: the window, a panel, the sidebar, the
		// editor and its current statement, the grid's header.
		grounds := map[string]ui.Color{
			"background": th.Background, "surface": th.Surface, "sidebar": pal.Sidebar,
			"editor": pal.EditorBg, "current statement": pal.CurrentStatement, "grid header": pal.GridHeader,
		}
		everywhere := map[string]ui.Color{
			"text": th.Text, "muted text": th.TextMuted, "palette muted": pal.Muted,
			"danger": th.Danger, "success": th.Success, "warning": th.Warning, "accent": th.Accent,
		}
		for what, col := range everywhere {
			for where, bg := range grounds {
				if r := contrast(col, bg); r < 4.5 {
					t.Errorf("%s: %s %s on %s %s is %.2f to 1, under 4.5", name, what, HexColor(col), where, HexColor(bg), r)
				}
			}
		}
		// In the editor and the grid only.
		work := map[string]ui.Color{"editor": pal.EditorBg, "current statement": pal.CurrentStatement, "background": th.Background}
		code := map[string]ui.Color{"NULL": pal.Null}
		for kind, col := range pal.Syntax {
			code["syntax "+kindName(kind)] = col
		}
		for what, col := range code {
			for where, bg := range work {
				if r := contrast(col, bg); r < 4.5 {
					t.Errorf("%s: %s %s on %s %s is %.2f to 1, under 4.5", name, what, HexColor(col), where, HexColor(bg), r)
				}
			}
		}
		if r := contrast(pal.LineNumber, pal.EditorBg); r < 3 {
			t.Errorf("%s: line numbers %s on the editor are %.2f to 1, under 3", name, HexColor(pal.LineNumber), r)
		}
		// Text on a filled button.
		if r := contrast(th.AccentText, th.Accent); r < 4.5 {
			t.Errorf("%s: text %s on the accent %s is %.2f to 1, under 4.5", name, HexColor(th.AccentText), HexColor(th.Accent), r)
		}
		if r := contrast(th.AccentText, th.AccentHover); r < 4.5 {
			t.Errorf("%s: text %s on the hovered accent %s is %.2f to 1, under 4.5", name, HexColor(th.AccentText), HexColor(th.AccentHover), r)
		}
		if r := contrast(OnDanger(&th), th.Danger); r < 4.5 {
			t.Errorf("%s: text %s on danger %s is %.2f to 1, under 4.5", name, HexColor(OnDanger(&th)), HexColor(th.Danger), r)
		}
	}
}

func kindName(k sqltext.Kind) string {
	switch k {
	case sqltext.Keyword:
		return "keyword"
	case sqltext.String:
		return "string"
	case sqltext.Number:
		return "number"
	case sqltext.Comment:
		return "comment"
	case sqltext.QuotedIdent:
		return "quoted identifier"
	case sqltext.Param:
		return "parameter"
	case sqltext.Operator:
		return "operator"
	}
	return "other"
}

// A theme that names an accent and no text for it gets text that reads
// on that accent: white on a dark one, dark on a light one.
func TestThemeAccentTextFollowsTheAccent(t *testing.T) {
	defer UseTheme(nil)
	for accent, want := range map[string]string{"#7c3aed": "#ffffff", "#a5f3fc": "#1a1918"} {
		UseTheme(&Theme{Name: "mine", Base: "dark", Colors: map[string]string{"accent": accent}})
		if got := HexColor(look.theme.AccentText); got != want {
			t.Errorf("text on the accent %s is %s, want %s", accent, got, want)
		}
	}
}

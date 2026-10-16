package editor

import (
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	code "github.com/egoist/mygo/ui/editor"
)

// themeFrom is the editor's theme of the app's: the palette's editor and
// SQL colors, the theme's text, caret and selection. Kinds of tokens the
// palette does not color are the text's.
func themeFrom(t *ui.Theme, pal *widgets.Palette) *code.Theme {
	ct := code.DefaultTheme(t)
	ct.Background = pal.EditorBg
	ct.LineNumber = pal.LineNumber
	ct.Cursor = t.Accent
	ct.Selection = t.Selection
	ct.Error = t.Danger
	for k := range ct.Tokens {
		ct.Tokens[k] = code.TokenStyle{Color: t.Text}
	}
	for kind, color := range pal.Syntax {
		if k := tokenKind(kind); k != code.TokenText {
			ct.Tokens[k].Color = color
		}
	}
	for k, kind := range borrowedColors {
		if color, ok := pal.Syntax[kind]; ok {
			ct.Tokens[k].Color = color
		}
	}
	return ct
}

// borrowedColors are the kinds of tokens SQL has none of, colored as a
// SQL kind: JSON's true, false and null as keywords, and its brackets and
// commas as operators.
var borrowedColors = map[code.TokenKind]sqltext.Kind{
	code.TokenConstant:    sqltext.Keyword,
	code.TokenPunctuation: sqltext.Operator,
}

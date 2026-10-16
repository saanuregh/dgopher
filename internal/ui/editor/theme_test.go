package editor

import (
	"testing"

	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	code "github.com/egoist/mygo/ui/editor"
)

// The editor's colors are the palette's and the theme's; kinds the
// palette does not color are the text's, not GitHub's.
func TestThemeFromPalette(t *testing.T) {
	th := ui.DarkTheme()
	pal := &widgets.Palette{
		EditorBg:   ui.Hex("#101010"),
		LineNumber: ui.Hex("#202020"),
		Syntax: map[sqltext.Kind]ui.Color{
			sqltext.Keyword:     ui.Hex("#300000"),
			sqltext.String:      ui.Hex("#003000"),
			sqltext.Number:      ui.Hex("#000030"),
			sqltext.Comment:     ui.Hex("#303000"),
			sqltext.QuotedIdent: ui.Hex("#003030"),
			sqltext.Param:       ui.Hex("#300030"),
			sqltext.Operator:    ui.Hex("#303030"),
		},
	}
	ct := themeFrom(th, pal)
	for name, c := range map[string][2]ui.Color{
		"background":  {ct.Background, pal.EditorBg},
		"line number": {ct.LineNumber, pal.LineNumber},
		"text":        {ct.Text, th.Text},
		"cursor":      {ct.Cursor, th.Accent},
		"selection":   {ct.Selection, th.Selection},
		"error":       {ct.Error, th.Danger},
		"function":    {ct.Tokens[code.TokenFunction].Color, th.Text},
		"type":        {ct.Tokens[code.TokenType].Color, th.Text},
		"constant":    {ct.Tokens[code.TokenConstant].Color, pal.Syntax[sqltext.Keyword]},
		"punctuation": {ct.Tokens[code.TokenPunctuation].Color, pal.Syntax[sqltext.Operator]},
	} {
		if c[0] != c[1] {
			t.Errorf("%s is %v, want %v", name, c[0], c[1])
		}
	}
	for kind, want := range pal.Syntax {
		if got := ct.Tokens[tokenKind(kind)].Color; got != want {
			t.Errorf("sqltext kind %v is %v, want %v", kind, got, want)
		}
	}
}

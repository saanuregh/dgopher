package app

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestFontFamilySuggestions(t *testing.T) {
	saved := systemFontFamilies
	t.Cleanup(func() { systemFontFamilies = saved })

	systemFontFamilies = func() []ui.FontFamily {
		return []ui.FontFamily{{Name: "Iosevka", Monospaced: true}, {Name: "Lato"}}
	}
	mono, all := fontFamilySuggestions()
	if !slices.Equal(mono, []string{"Iosevka"}) {
		t.Errorf("editor suggestions = %v, want [Iosevka]", mono)
	}
	if !slices.Equal(all, []string{"Iosevka", "Lato"}) {
		t.Errorf("interface suggestions = %v, want [Iosevka Lato]", all)
	}

	systemFontFamilies = func() []ui.FontFamily { return []ui.FontFamily{{Name: "Lato"}} }
	if mono, _ = fontFamilySuggestions(); !slices.Equal(mono, fallbackMonoFamilies) {
		t.Errorf("with no monospaced family the editor got %v, want the fallback", mono)
	}

	systemFontFamilies = func() []ui.FontFamily { return nil }
	mono, all = fontFamilySuggestions()
	if !slices.Equal(mono, fallbackMonoFamilies) || !slices.Equal(all, fallbackUIFamilies) {
		t.Errorf("with no system families got %v and %v, want the fallbacks", mono, all)
	}
}

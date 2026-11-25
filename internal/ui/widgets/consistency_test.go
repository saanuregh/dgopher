package widgets

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// eachSourceLine calls fn with every line of the app's source that is not
// a test's, and its path from internal/.
func eachSourceLine(t *testing.T, fn func(rel string, n int, line string)) {
	t.Helper()
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(src), "\n") {
			fn(filepath.ToSlash(rel), i+1, line)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A tinted fill takes its strength by name (tints.go): a notice reads as
// strongly in one view as in another. Stronger fills, as a chart's or a
// plan's bars, are drawings, not tints.
func TestTintsAreNamed(t *testing.T) {
	raw := regexp.MustCompile(`Background\([^()]*(Danger|Warning|Success|Accent|col)\.Alpha\(0\.[0-2][0-9]*\)\)`)
	eachSourceLine(t, func(rel string, n int, line string) {
		if rel != "ui/widgets/tints.go" && raw.MatchString(line) {
			t.Errorf("%s:%d: a tint's strength as a number: %s", rel, n, strings.TrimSpace(line))
		}
	})
}

// A bar, which is a row with a rule under or over it, takes its padding
// by name (sizes.go): a toolbar's, a bar's, a section's or a status
// line's. Two bars pad their sides unevenly for what they hold, and are
// named here.
func TestBarsUseNamedPaddings(t *testing.T) {
	raw := regexp.MustCompile(`Padding\([0-9][0-9, ]*\).*BorderWidth\((0, 0, 1, 0|1, 0, 0, 0)\)`)
	uneven := map[string]bool{
		"ui/query/query.go": true, // the Output's headline: flush with the log under it
		"ui/chat/view.go":   true, // the chat's composer: more room under its buttons
	}
	found := map[string]int{}
	eachSourceLine(t, func(rel string, n int, line string) {
		if !raw.MatchString(line) {
			return
		}
		if found[rel]++; !uneven[rel] || found[rel] > 1 {
			t.Errorf("%s:%d: a bar's padding as numbers: %s", rel, n, strings.TrimSpace(line))
		}
	})
}

// A button drawn by hand is one the shared widgets do not cover, as a
// tab, a card or a chart's bar: each file's are counted here, so that a
// new one is either built from a widget (IconButton, RowButton, Pill,
// StatusButton, SmallIconButton) or added on purpose.
func TestHandBuiltButtonsAreCounted(t *testing.T) {
	want := map[string]int{
		"app/layout.go":           6, // a tab, its close button, a start page's cards, the status bar's chips
		"app/navfoot.go":          1, // the sidebar's foot's chip of the memory used, an icon before its figure
		"app/titlebar.go":         1, // a window button
		"app/txsafety.go":         1, // the title bar's chip of open transactions
		"ui/chat/modelpicker.go":  0,
		"ui/chat/view.go":         6, // a suggestion, a reply's parts and buttons, Send
		"ui/dataview/jsontree.go": 2, // a JSON tree's fold and key
		"ui/query/notebook.go":    1, // a Markdown cell, pressed to edit it
		"ui/query/plan.go":        3, // a plan's advice, step and flame bar
	}
	got := map[string]int{}
	eachSourceLine(t, func(rel string, _ int, line string) {
		if !strings.HasPrefix(rel, "ui/widgets/") {
			got[rel] += strings.Count(line, "ui.ButtonBase(")
		}
	})
	for rel, n := range got {
		if n != want[rel] {
			t.Errorf("%s builds %d buttons by hand, %d are counted", rel, n, want[rel])
		}
	}
}

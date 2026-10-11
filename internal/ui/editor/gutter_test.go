package editor

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"dgopher/internal/sqltext"

	"github.com/egoist/mygo/ui"
)

const gutterFontSize = 13

// gutterText is a text of 100,000 lines: names, which the highlighting
// leaves uncolored, as the text area's colored runs cost far more than the
// gutter, and blocks that fold near its start and around line 50,000.
func gutterText() string {
	block := "select a,\n  b\nfrom t;\n"
	names := strings.Repeat("abc;\n", 49_990)
	return block + block + names + strings.Repeat(block, 10) + names
}

// gutterNumbers is the gutter's text of line numbers in the last frame.
func gutterNumbers(t *testing.T, tt *ui.Tester) []string {
	t.Helper()
	for _, s := range tt.Texts() {
		lines := strings.Split(s, "\n")
		if _, err := strconv.Atoi(lines[0]); err == nil {
			return lines
		}
	}
	t.Fatalf("no line numbers in %d texts", len(tt.Texts()))
	return nil
}

// The gutter draws the lines in sight, not the whole text's, each at its
// line; a marker clicked in a scrolled view folds the block on its line,
// and folded, the numbers are the text's lines that show.
func TestGutterOnlyVisible(t *testing.T) {
	e := &Editor{Text: gutterText(), Dialect: sqltext.Postgres}
	tt := ui.NewTester(func(c *ui.Context) { e.View(c, gutterFontSize) }, 800, 600)
	tt.Frame()
	tt.Frame()
	if got := gutterNumbers(t, tt); len(got) > 200 {
		t.Fatalf("the gutter has %d lines", len(got))
	}

	lh := float32(gutterFontSize * LineHeight)
	e.fold(block{9, 21}) // a fold above the view
	e.Scroll.Y = 8 + 50_000*lh
	tt.Frame()
	tt.Frame()
	numbers := gutterNumbers(t, tt)
	if len(numbers) > 200 {
		t.Fatalf("scrolled, the gutter has %d lines", len(numbers))
	}
	want := e.lineNumbers()
	wantLines := strings.Split(want, "\n")
	if !slices.Equal(numbers, wantLines[e.gutterTop:e.gutterTop+len(numbers)]) {
		t.Fatalf("view lines from %d numbered %v…", e.gutterTop, numbers[:3])
	}
	view := int((e.Scroll.Y-8)/lh) + 3 // a line in sight
	if n, _ := strconv.Atoi(numbers[view-e.gutterTop]); n < 50_000 || n > 50_010 {
		t.Fatalf("view line %d numbered %d", view, n)
	}
	// The block that starts on a view line in sight: its marker folds it.
	_, at := e.foldMarkers()
	for ; at[view] == (block{}); view++ {
	}
	b := at[view]
	y := 8 + float32(view)*lh - e.Scroll.Y + lh/2
	tt.ClickAt(e.gutterWidth()-e.markersWidth()/2, y)
	tt.Frame()
	if !slices.Contains(e.folds, b) {
		t.Fatalf("clicked on view line %d, folds %v, want %v", view, e.folds, b)
	}
	fresh := &Editor{Text: e.Text, Dialect: sqltext.Postgres}
	fresh.folds, fresh.foldedText = slices.Clone(e.folds), e.Text
	fresh.syncFolds()
	numbers = gutterNumbers(t, tt)
	if all := strings.Split(fresh.lineNumbers(), "\n"); !slices.Equal(numbers, all[e.gutterTop:e.gutterTop+len(numbers)]) {
		t.Fatalf("folded, view lines from %d numbered %v…", e.gutterTop, numbers[:3])
	}
}

// BenchmarkIdleFrame100k is one frame of a 100,000-line editor that
// nothing changed.
func BenchmarkIdleFrame100k(b *testing.B) {
	e := &Editor{Text: gutterText(), Dialect: sqltext.Postgres}
	tt := ui.NewTester(func(c *ui.Context) { e.View(c, gutterFontSize) }, 800, 600)
	tt.Frame()
	for b.Loop() {
		tt.Frame()
	}
}

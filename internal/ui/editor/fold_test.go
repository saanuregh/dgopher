package editor

import (
	"slices"
	"testing"

	"dgopher/internal/sqltext"
)

const folded = "select a,\n  b\nfrom t;\n\nselect (\n  1\n) x;"

// The blocks that fold run from their first line's end to their last's:
// a statement and its group in parentheses over the same lines are one.
func TestBlocks(t *testing.T) {
	if got := blocks(folded, sqltext.Postgres); !slices.Equal(got, []block{{9, 21}, {31, 40}}) {
		t.Fatalf("blocks %v", got)
	}
}

// A folded block shows as its first line and a mark; places map between
// the text and what shows, both ways, and edits move the folds after.
func TestFolding(t *testing.T) {
	tx := []rune(folded)
	f := folding{}.add(block{9, 21})
	shown := string(f.shown(tx))
	if shown != "select a,"+foldMark+"\n\nselect (\n  1\n) x;" {
		t.Fatalf("shown %q", shown)
	}
	mark := len([]rune(foldMark))
	for _, c := range []struct{ text, shown int }{{0, 0}, {9, 9}, {15, 9}, {21, 9 + mark}, {23, 9 + mark + 2}} {
		if got := f.toShown(c.text); got != c.shown {
			t.Errorf("text %d shows at %d, want %d", c.text, got, c.shown)
		}
	}
	if f.toText(9+mark, false) != 21 || f.toText(10, false) != 9 || f.toText(10, true) != 21 || f.toText(9, true) != 9 {
		t.Error("shown places map to the wrong runes")
	}
	if !f.hides(15) || f.hides(9) || f.hides(21) {
		t.Error("hides")
	}
	if got := f.edited(0, 0, 3); len(got) != 1 || got[0] != (block{12, 24}) {
		t.Errorf("an edit before: %v", got)
	}
	if got := f.edited(10, 12, 0); len(got) != 0 {
		t.Errorf("an edit inside: %v", got)
	}
	if got := f.add(block{0, 30}); len(got) != 1 || got[0] != (block{0, 30}) {
		t.Errorf("an outer fold: %v", got)
	}
}

// Typed over a fold's mark, the folded lines go with it; typed after it,
// the text gets it after the block.
func TestTypedFolded(t *testing.T) {
	e := &Editor{Text: folded, Dialect: sqltext.Postgres}
	e.folds, e.foldedText = folding{}.add(block{9, 21}), folded
	e.syncFolds()
	mark := len([]rune(foldMark))
	view := []rune(e.shown)
	e.shown = string(view[:9]) + string(view[9+mark:]) // the mark deleted
	e.syncFolds()
	if e.Text != "select a,\n\nselect (\n  1\n) x;" || e.Folded() {
		t.Fatalf("after deleting the mark: %q, folds %v", e.Text, e.folds)
	}

	e = &Editor{Text: folded, Dialect: sqltext.Postgres}
	e.folds, e.foldedText = folding{}.add(block{9, 21}), folded
	e.syncFolds()
	view = []rune(e.shown)
	e.shown = string(view[:9+mark]) + "!" + string(view[9+mark:])
	e.syncFolds()
	if e.Text != folded[:21]+"!"+folded[21:] || !e.Folded() {
		t.Fatalf("after typing past the mark: %q", e.Text)
	}
}

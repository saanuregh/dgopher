package editor

import "testing"

// editorAt is an editor of a text with its caret at a rune.
func editorAt(text string, caret int) *Editor {
	e := &Editor{Text: text, SelStart: caret, SelEnd: caret}
	e.cursorText = text
	return e
}

func TestCursorEdits(t *testing.T) {
	// A caret added where one is, as down then up, types once.
	e := editorAt("ab\ncd\n", 0)
	e.AddCursor(1)
	e.AddCursor(-1)
	if len(e.cursors) != 1 {
		t.Fatalf("cursors %v", e.cursors)
	}
	e.TypeAtCarets(0, 0, "x")
	if e.Text != "xab\nxcd\n" {
		t.Fatalf("typed %q", e.Text)
	}

	// A change that runs off the text at one caret leaves it in place.
	e = editorAt("ab\ncd", 0)
	e.AddCursor(1) // the primary at line 2, a caret at 0
	e.atCursors([]rune(e.Text), e.primary(), span{2, 3}, nil)
	if e.Text != "abcd" || len(e.cursors) != 1 || e.cursors[0] != (span{0, 0}) {
		t.Fatalf("after a backspace: %q, cursors %v", e.Text, e.cursors)
	}

	// A cursor over the primary takes it in: the primary stays a caret.
	e = editorAt("abcdef", 3)
	e.cursors = []span{{0, 5}}
	e.mergeCursors()
	if len(e.cursors) != 0 {
		t.Fatalf("overlapping cursors kept: %v", e.cursors)
	}
}

func TestSelectNext(t *testing.T) {
	// From a word, its next whole occurrence, not inside another word.
	e := editorAt("id, valid, id", 0)
	e.SelectNext()
	e.SelectNext()
	if e.primary() != (span{11, 13}) || len(e.cursors) != 1 || e.cursors[0] != (span{0, 2}) {
		t.Fatalf("primary %v, cursors %v", e.primary(), e.cursors)
	}
	// Round to the start, each occurrence once.
	e = editorAt("a x a x a", 4)
	e.SelectNext()
	e.SelectNext()
	e.SelectNext()
	e.SelectNext()
	if len(e.cursors) != 2 {
		t.Fatalf("after wrapping, cursors %v", e.cursors)
	}
	e = editorAt("id, valid, id", 0)
	e.SelectAll()
	e.TypeAtCarets(e.primary().start, e.primary().end, "Z")
	if e.Text != "Z, valid, Z" {
		t.Fatalf("every whole id: %q", e.Text)
	}
}

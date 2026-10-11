package editor

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestOverlay(t *testing.T) {
	base := []ui.TextRange{{Start: 0, End: 6}, {Start: 10, End: 14}}
	top := []ui.TextRange{{Start: 4, End: 11, Weight: 700}}
	got := overlay(base, top)
	want := []ui.TextRange{{Start: 0, End: 4}, {Start: 4, End: 11, Weight: 700}, {Start: 11, End: 14}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v", got)
		}
	}
}

func TestFindAll(t *testing.T) {
	text := "SELECT éa FROM t;\nselect b FROM u;\n-- Select me"
	got := FindAll(text, "select")
	if len(got) != 3 || got[0] != 0 || got[1] != 18 || got[2] != 38 {
		t.Errorf("matches %v, want [0 18 38]", got)
	}
	if FindAll(text, "") != nil || FindAll(text, "nothing") != nil {
		t.Error("an empty or absent query matched")
	}
}

// A letter whose lower case has another byte length, as İ, does not move
// the matches after it.
func TestFindAllAfterChangingCase(t *testing.T) {
	text := "-- İstanbul\nselect 1"
	if got := FindAll(text, "select"); len(got) != 1 || got[0] != 12 {
		t.Errorf("matches %v, want [12]", got)
	}
	if got := FindAll(text, "istanbul"); len(got) != 1 || got[0] != 3 {
		t.Errorf("matches %v, want [3]", got)
	}
}

func TestReplaceAt(t *testing.T) {
	text := "SELECT é FROM t; select 1"
	got := ReplaceAt(text, FindAll(text, "select"), 6, "SELECT DISTINCT")
	if want := "SELECT DISTINCT é FROM t; SELECT DISTINCT 1"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := ReplaceAt(text, nil, 6, "x"); got != text {
		t.Errorf("no matches changed the text: %q", got)
	}
}

func TestLineCol(t *testing.T) {
	e := &Editor{Text: "ab\nécd\n\nx"}
	for _, c := range []struct{ at, line, col int }{
		{0, 0, 0}, {1, 0, 1}, {2, 0, 2}, {3, 1, 0}, {5, 1, 2}, {7, 2, 0}, {8, 3, 0}, {9, 3, 1}, {50, 3, 1},
	} {
		if line, col := e.LineCol(c.at); line != c.line || col != c.col {
			t.Errorf("LineCol(%d) = %d, %d, want %d, %d", c.at, line, col, c.line, c.col)
		}
	}
}

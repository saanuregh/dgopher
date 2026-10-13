package editor

import (
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/keymap"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	code "github.com/egoist/mygo/ui/editor"
)

// editorTest is an editor in a window without a screen, with views built
// before and after it.
type editorTest struct {
	e             *Editor
	tt            *ui.Tester
	before, after func(c *ui.Context)
}

func newEditorTest(t *testing.T, text string) *editorTest {
	t.Helper()
	et := &editorTest{e: New(text, sqltext.Postgres)}
	et.tt = ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			if et.before != nil {
				et.before(c)
			}
			et.e.View(c, 13)
			if et.after != nil {
				et.after(c)
			}
		})
	}, 800, 400)
	et.e.WantFocus = true
	et.frames()
	if !et.e.HasFocus() || et.e.WantFocus {
		t.Fatal("the editor did not take the focus")
	}
	return et
}

func (et *editorTest) frames() {
	et.tt.Frame()
	et.tt.Frame()
}

func TestTextAndSelection(t *testing.T) {
	e := New("ab\nécd\n\nx", sqltext.Generic)
	for _, c := range []struct{ at, line, col int }{
		{0, 0, 0}, {1, 0, 1}, {2, 0, 2}, {3, 1, 0}, {5, 1, 1}, {7, 1, 3}, {8, 2, 0}, {9, 3, 0}, {10, 3, 1}, {50, 3, 1},
	} {
		if line, col := e.LineCol(c.at); line != c.line || col != c.col {
			t.Errorf("LineCol(%d) = %d, %d, want %d, %d", c.at, line, col, c.line, c.col)
		}
	}
	e.Select(5, 3)
	if s, end := e.Selection(); s != 3 || end != 5 || e.Caret() != 3 || e.SelectedText() != "é" {
		t.Fatalf("selection %d-%d, caret %d, text %q", s, end, e.Caret(), e.SelectedText())
	}
	e.Replace(3, 5, "日本")
	if e.Text() != "ab\n日本cd\n\nx" || e.Caret() != 9 {
		t.Fatalf("replaced: %q, caret %d", e.Text(), e.Caret())
	}
	e.SetText("select 1")
	if e.Text() != "select 1" {
		t.Fatalf("set: %q", e.Text())
	}
	e.State.Run(code.Undo)
	e.State.Run(code.Undo)
	if e.Text() != "ab\nécd\n\nx" {
		t.Fatalf("two undos: %q", e.Text())
	}
	var zero Editor
	if zero.Text() != "" || zero.FileText() != "" || !zero.Typing() {
		t.Fatal("a zero editor is not empty")
	}
}

// A text read with CRLF line endings is edited with LF ones and saved
// with its own.
func TestFileTextKeepsCRLF(t *testing.T) {
	e := New("select 1;\r\nselect 2;\r\n", sqltext.Generic)
	if e.Text() != "select 1;\nselect 2;\n" {
		t.Fatalf("text %q", e.Text())
	}
	e.Replace(0, 0, "-- é\n")
	if got := e.FileText(); got != "-- é\r\nselect 1;\r\nselect 2;\r\n" {
		t.Fatalf("file text %q", got)
	}
	lf := New("a\nb", sqltext.Generic)
	if lf.FileText() != "a\nb" {
		t.Fatalf("LF file text %q", lf.FileText())
	}
}

// In Vim's normal mode letters are commands, with a block caret; i types
// until Esc, which goes back to normal mode; u undoes in Vim's history.
// In Vim's normal mode no input method composes in the text: what one
// commits reaches Vim, which leaves the text as it is.
func TestVimNormalKeepsInputMethodsOut(t *testing.T) {
	et := newEditorTest(t, "select 1")
	e := et.e
	e.Vim = NewVim()
	e.Select(0, 0)
	et.frames()
	et.tt.Compose("に", 1)
	et.frames()
	et.tt.Type("に")
	et.frames()
	if e.Text() != "select 1" || e.Vim.Mode != VimNormal {
		t.Fatalf("composed in normal mode: %q, mode %v", e.Text(), e.Vim.Mode)
	}
	if _, ok := et.tt.TextCaret(); !ok {
		t.Fatal("the window's text input is off in normal mode: keys would type nothing")
	}
	// Text typed right after i, before a frame, is typed, not lost.
	et.tt.Type("i")
	et.tt.Type("q")
	et.frames()
	if e.Text() != "qselect 1" {
		t.Fatalf("typed right after i: %q", e.Text())
	}
	et.tt.Compose("に", 1)
	et.frames()
	et.tt.Type("に")
	et.frames()
	if e.Text() != "qにselect 1" {
		t.Fatalf("composed in insert mode: %q", e.Text())
	}
}

func TestVimBlockCaretAndInsert(t *testing.T) {
	et := newEditorTest(t, "select é, b")
	e := et.e
	e.Vim = NewVim()
	e.Select(0, 0)
	et.frames()
	if !e.State.BlockCaret || e.Typing() {
		t.Fatal("normal mode without a block caret, or typing")
	}
	et.tt.Type("w")
	et.frames()
	if e.Text() != "select é, b" || e.Caret() != 7 {
		t.Fatalf("w: %q, caret %d", e.Text(), e.Caret())
	}
	et.tt.Type("l")
	et.frames()
	if e.Caret() != 9 {
		t.Fatalf("l over é: caret %d", e.Caret())
	}
	et.tt.Type("i")
	et.frames()
	if e.Vim.Mode != VimInsert || e.State.BlockCaret || !e.Typing() {
		t.Fatalf("i: mode %v, block caret %v", e.Vim.Mode, e.State.BlockCaret)
	}
	et.tt.Type("x")
	et.frames()
	if e.Text() != "select éx, b" {
		t.Fatalf("typed: %q", e.Text())
	}
	et.tt.Key(0, ui.KeyEscape)
	et.frames()
	if e.Vim.Mode != VimNormal || !e.State.BlockCaret || e.Caret() != 9 {
		t.Fatalf("Esc: mode %v, block caret %v, caret %d", e.Vim.Mode, e.State.BlockCaret, e.Caret())
	}
	et.tt.Type("u")
	et.frames()
	if e.Text() != "select é, b" {
		t.Fatalf("u: %q", e.Text())
	}
	// Keys that would edit behind Vim's back do nothing.
	et.tt.Key(0, ui.KeyTab)
	et.tt.Key(ui.Shift, ui.KeyEnter)
	et.frames()
	if e.Text() != "select é, b" {
		t.Fatalf("Tab and Shift+Enter in normal mode: %q", e.Text())
	}
	// A click moves Vim's caret.
	et.tt.Type("$")
	et.frames()
	e.Select(2, 2)
	et.frames()
	et.tt.Type("x")
	et.frames()
	if e.Text() != "seect é, b" {
		t.Fatalf("x after the caret moved: %q", e.Text())
	}
}

// Vim's Control keys win over the app's and the editor's while the editor
// has the keys, outside insert mode; half a page is half the lines shown.
func TestVimCtrlKeysWin(t *testing.T) {
	et := newEditorTest(t, strings.Repeat("select 1;\n", 200))
	e := et.e
	e.Vim = NewVim()
	e.Select(0, 0)
	app := 0
	et.after = func(c *ui.Context) {
		if c.Shortcut(ui.Ctrl, ui.KeyD) {
			app++
		}
	}
	et.frames()
	first, last := e.State.VisibleLines()
	et.tt.Key(ui.Ctrl, ui.KeyD)
	et.frames()
	line, _ := e.LineCol(e.Caret())
	if want := (last - first + 1) / 2; line != want || app != 0 || e.SelectedText() != "" {
		t.Fatalf("Ctrl+D: line %d, want %d; the app's ran %d times, selected %q", line, want, app, e.SelectedText())
	}
	et.tt.Key(ui.Ctrl, ui.KeyU)
	et.frames()
	if line, _ := e.LineCol(e.Caret()); line != 0 {
		t.Fatalf("Ctrl+U: line %d", line)
	}
	et.tt.Type("xu")
	et.frames()
	et.tt.Key(ui.Ctrl, ui.KeyR)
	et.frames()
	if !strings.HasPrefix(e.Text(), "elect 1;") {
		t.Fatalf("Ctrl+R: %q", e.Text()[:10])
	}
	et.tt.Type("i")
	et.frames()
	et.tt.Key(ui.Ctrl, ui.KeyD)
	et.frames()
	if app != 1 {
		t.Fatalf("in insert mode, the app's Ctrl+D ran %d times", app)
	}
	et.tt.Key(ui.Ctrl, ui.KeyBracketLeft)
	et.frames()
	if e.Vim.Mode != VimNormal {
		t.Fatalf("Ctrl+[: mode %v", e.Vim.Mode)
	}
}

// A ▶ shows on each statement's line in view, named by RunTip, and a
// click runs its statement and gives the editor back the keys.
func TestRunButtons(t *testing.T) {
	et := newEditorTest(t, strings.Repeat("select 1;\n", 100))
	e := et.e
	var ran []int
	other, focusOther := "", true
	var otherField ui.Element
	// The run gives the keys to another element, as a run may.
	e.Run = func(line int) {
		ran = append(ran, line)
		otherField.Focus()
	}
	e.RunTip = "Run the statement (F5)"
	e.RunLines = []int{0, 2, 90}
	et.before = func(c *ui.Context) {
		otherField = ui.TextInput(c, &other).Label("other")
		if focusOther {
			otherField.Focus()
			focusOther = false
		}
	}
	et.frames()
	count := func() int {
		n := 0
		for _, s := range et.tt.Texts() {
			if s == e.RunTip {
				n++
			}
		}
		return n
	}
	if n := count(); n != 2 || !et.tt.Focused("other") {
		t.Fatalf("%d run buttons in view, want 2; the field has the focus: %v", n, et.tt.Focused("other"))
	}
	r, _ := et.tt.Find(e.RunTip)
	et.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	for deadline := time.Now().Add(3 * time.Second); count() < 3 && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		et.tt.Frame()
	}
	if count() != 3 {
		t.Fatalf("no tooltip %q: %q", e.RunTip, et.tt.Texts())
	}
	et.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2+2*r.H)
	et.frames()
	if !slices.Equal(ran, []int{2}) || !e.HasFocus() {
		t.Fatalf("ran %v, the editor has the focus: %v", ran, e.HasFocus())
	}
	e.Run = nil
	et.frames()
	if e.State.GutterButtons != nil || count() != 0 {
		t.Fatal("run buttons without Run")
	}
}

// The current statement's band shows only while the owner sets
// HasCurrent.
func TestCurrentBand(t *testing.T) {
	et := newEditorTest(t, "select 1;\nselect\n  2;\n")
	e := et.e
	var pal *widgets.Palette
	et.after = func(c *ui.Context) { pal = widgets.PaletteOf(c) }
	e.CurrentLines = [2]int{1, 2}
	et.frames()
	if e.State.LineBackgrounds != nil {
		t.Fatal("a band without HasCurrent")
	}
	e.HasCurrent = true
	et.frames()
	want := []code.LineBackground{{First: 1, Last: 2, Color: pal.CurrentStatement}}
	if !slices.Equal(e.State.LineBackgrounds, want) {
		t.Fatalf("band %v, want %v", e.State.LineBackgrounds, want)
	}
	e.HasCurrent = false
	et.frames()
	if e.State.LineBackgrounds != nil {
		t.Fatal("the band stayed")
	}
}

// Problems are the editor's diagnostics, errors, at least a character
// wide; ProblemAt finds one at either end.
func TestProblemsToDiagnostics(t *testing.T) {
	et := newEditorTest(t, "select é from t")
	e := et.e
	e.Problems = []Problem{{Start: 14, End: 15, Message: "no table t"}, {Start: 7, End: 7, Message: "no column é", Fix: "e"}}
	et.frames()
	want := []code.Diagnostic{
		{Start: 7, End: 9, Severity: code.SeverityError, Message: "no column é"},
		{Start: 14, End: 15, Severity: code.SeverityError, Message: "no table t"},
	}
	if got := e.State.Diagnostics(); !slices.Equal(got, want) {
		t.Fatalf("diagnostics %v, want %v", got, want)
	}
	for at, want := range map[int]string{6: "", 7: "no column é", 13: "", 14: "no table t", 15: "no table t", 16: ""} {
		p, ok := e.ProblemAt(at)
		if ok != (want != "") || p.Message != want {
			t.Errorf("ProblemAt(%d) = %q, %v", at, p.Message, ok)
		}
	}
	e.Problems = nil
	et.frames()
	if got := e.State.Diagnostics(); len(got) != 0 {
		t.Fatalf("diagnostics left: %v", got)
	}
}

// KeyHook takes input before Vim and the editor.
func TestKeyHookFirst(t *testing.T) {
	et := newEditorTest(t, "select a")
	e := et.e
	var hooked []string
	e.KeyHook = func(ev ui.InputEvent) bool {
		switch {
		case ev.Kind == ui.InputText && ev.Text == "x":
			hooked = append(hooked, "x")
			return true
		case ev.Kind == ui.InputKeyDown && ev.Key == ui.KeyTab:
			hooked = append(hooked, "Tab")
			return true
		}
		return false
	}
	e.Select(0, 0)
	et.frames()
	et.tt.Key(0, ui.KeyTab)
	et.tt.Type("y")
	et.frames()
	if e.Text() != "yselect a" {
		t.Fatalf("text %q", e.Text())
	}
	e.Vim = NewVim()
	et.frames()
	et.tt.Type("x")
	et.frames()
	if e.Text() != "yselect a" || e.Vim.Status() != "" || !slices.Equal(hooked, []string{"Tab", "x"}) {
		t.Fatalf("text %q, Vim %q, hooked %v", e.Text(), e.Vim.Status(), hooked)
	}
}

// The editor's keys of the keymap are the user's: a key given there
// selects the next occurrence.
func TestBindingsFromKeymap(t *testing.T) {
	et := newEditorTest(t, "abc abc")
	e := et.e
	t.Cleanup(func() { keymap.Use(nil) })
	if errs := keymap.Use(map[string][]string{keymap.SelectNext: {"Alt+N"}}); len(errs) > 0 {
		t.Fatal(errs)
	}
	e.Select(1, 1)
	et.frames()
	et.tt.Key(ui.Alt, ui.KeyN)
	et.frames()
	if s, end := e.Selection(); s != 0 || end != 3 {
		t.Fatalf("Alt+N selected %d-%d", s, end)
	}
	et.tt.Key(ui.Alt, ui.KeyN)
	et.frames()
	if n := len(e.State.Selections()); n != 2 {
		t.Fatalf("Alt+N again: %d selections", n)
	}
	// The editor's own key for the command no longer runs it.
	e.Select(1, 1)
	et.frames()
	for _, b := range code.DefaultBindings() {
		if b.Command == code.SelectNext && b.ThenKey == 0 {
			et.tt.Key(b.Mods, b.Key)
		}
	}
	et.frames()
	if s, end := e.Selection(); s != 1 || end != 1 || len(e.State.Selections()) != 1 {
		t.Fatalf("the default key still selects next: %d-%d", s, end)
	}
}

// A text set and a caret put in it before the next frame are colored:
// the editor may lay out the caret's line before the highlighter hears of
// the edit, and must not keep the old text's tokens for it.
func TestSelectAfterSetTextHighlights(t *testing.T) {
	et := newEditorTest(t, "")
	var keyword ui.Color
	et.before = func(c *ui.Context) { keyword = widgets.PaletteOf(c).Syntax[sqltext.Keyword] }
	et.e.SetText("SELECT * FROM invoices")
	et.e.Select(22, 22)
	et.frames()
	img := et.tt.Image()
	b := img.Bounds()
	near := func(a uint32, b uint8) bool { return max(int(a>>8), int(b))-min(int(a>>8), int(b)) <= 8 }
	for y := b.Min.Y; y < b.Min.Y+40; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if r, g, bl, _ := img.At(x, y).RGBA(); near(r, keyword.R) && near(g, keyword.G) && near(bl, keyword.B) {
				return
			}
		}
	}
	t.Fatal("no keyword colored on the first line")
}

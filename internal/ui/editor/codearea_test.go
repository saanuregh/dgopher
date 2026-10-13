package editor

import (
	"testing"

	"dgopher/internal/sqltext"

	"github.com/egoist/mygo/ui"
	code "github.com/egoist/mygo/ui/editor"
)

// codeAreaTest is a code area bound to a string, in a window without a
// screen, counting the frames that reported a change.
type codeAreaTest struct {
	area    *CodeArea
	text    string
	changes int
	tt      *ui.Tester
}

func newCodeAreaTest(text string, area *CodeArea) *codeAreaTest {
	at := &codeAreaTest{area: area, text: text}
	at.tt = ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			el, changed := at.area.View(c, &at.text)
			el.Grow(1)
			if changed {
				at.changes++
			}
		})
	}, 600, 300)
	at.frames()
	return at
}

func (at *codeAreaTest) frames() {
	at.tt.Frame()
	at.tt.Frame()
}

// The owner's string is the text: changed outside, the area shows it with
// no step to undo; typed in, it changes, once for each edit.
func TestCodeAreaBindsString(t *testing.T) {
	at := newCodeAreaTest("one", &CodeArea{Label: "Value", AutoFocus: true})
	b := at.area.state.Buffer
	if b.String() != "one" || at.changes != 0 {
		t.Fatalf("shown %q, %d changes", b.String(), at.changes)
	}
	at.text = "twé"
	at.frames()
	if b.String() != "twé" || at.changes != 0 || b.CanUndo() {
		t.Fatalf("outside change: %q, %d changes, can undo %v", b.String(), at.changes, b.CanUndo())
	}
	at.area.state.Select(4, 4)
	at.frames()
	at.tt.Type("x")
	at.frames()
	if at.text != "twéx" || at.changes != 1 {
		t.Fatalf("typed: %q, %d changes", at.text, at.changes)
	}
	at.tt.Type("y")
	at.frames()
	if at.text != "twéxy" || at.changes != 2 {
		t.Fatalf("typed again: %q, %d changes", at.text, at.changes)
	}
	at.area.state.Run(code.Undo)
	at.frames()
	if at.text != "twé" || b.CanUndo() {
		t.Fatalf("undone: %q, can undo %v", at.text, b.CanUndo())
	}
	// A shorter text from outside keeps the caret in it.
	at.text = "é"
	at.frames()
	if c := at.area.state.Cursor(); c > len("é") {
		t.Fatalf("caret %d past the text", c)
	}
	// Lines read with \r\n are written back with them.
	at.text = "a\r\nb"
	at.frames()
	at.area.state.Select(4, 4)
	at.tt.Type("c")
	at.frames()
	if at.text != "a\r\nbc" {
		t.Fatalf("CRLF typed: %q", at.text)
	}
	changes := at.changes
	at.area.ReadOnly = true
	at.frames()
	at.tt.Type("z")
	at.frames()
	if at.text != "a\r\nbc" || at.changes != changes {
		t.Fatalf("read only typed: %q, %d changes", at.text, at.changes-changes)
	}
}

// A disabled area takes no focus and so no typing.
func TestCodeAreaDisabled(t *testing.T) {
	at := newCodeAreaTest("one", &CodeArea{Label: "Value", AutoFocus: true, Disabled: true})
	at.tt.ClickAt(100, 20)
	at.frames()
	at.tt.Type("x")
	at.frames()
	if at.text != "one" || at.changes != 0 || at.area.state.Focused() {
		t.Fatalf("disabled: %q, %d changes, focused %v", at.text, at.changes, at.area.state.Focused())
	}
}

func TestCodeAreaLanguage(t *testing.T) {
	at := newCodeAreaTest("select `a`", &CodeArea{Language: LanguageSQL, Dialect: sqltext.MySQL})
	lang := at.area.state.Buffer.Language()
	if lang == nil || lang.Name != "SQL" {
		t.Fatalf("SQL: %v", lang)
	}
	if h, ok := lang.Highlighter(at.area.state.Buffer.Snapshot(), nil).(*sqlHighlighter); !ok || h.dialect != sqltext.MySQL {
		t.Fatal("not highlighted in MySQL's dialect")
	}
	at.area.Dialect = sqltext.Postgres
	at.frames()
	lang = at.area.state.Buffer.Language()
	if h, ok := lang.Highlighter(at.area.state.Buffer.Snapshot(), nil).(*sqlHighlighter); !ok || h.dialect != sqltext.Postgres {
		t.Fatal("the dialect changed, not the highlighter")
	}

	json := code.LanguageNamed("JSON")
	if json == nil {
		t.Fatal("no JSON language")
	}
	at = newCodeAreaTest("not json", &CodeArea{Language: LanguageJSON})
	if at.area.state.Buffer.Language() != json {
		t.Fatal("JSON area not JSON")
	}

	at = newCodeAreaTest(" {\"a\": [1, 2]}\n", &CodeArea{Language: LanguageDetected})
	if at.area.state.Buffer.Language() != json {
		t.Fatal("JSON text not detected")
	}
	at.text = "hello"
	at.frames()
	if at.area.state.Buffer.Language() != nil {
		t.Fatal("plain text detected as JSON")
	}
	at.text = "[1]"
	at.frames()
	if at.area.state.Buffer.Language() != json {
		t.Fatal("JSON set from outside not detected")
	}
}

// A value is data, typed as it is: no bracket or quote closes itself,
// Enter adds no indentation, and an edit keeps the line endings of the
// lines it did not touch, mixed or not.
func TestCodeAreaTypesData(t *testing.T) {
	at := newCodeAreaTest("  a", &CodeArea{Label: "Value", Language: LanguageJSON, AutoFocus: true})
	at.area.state.Select(3, 3)
	at.frames()
	at.tt.Key(0, ui.KeyEnter)
	at.frames()
	at.tt.Type("(")
	at.frames()
	at.tt.Type(`"`)
	at.frames()
	if at.text != "  a\n(\"" {
		t.Fatalf("typed: %q", at.text)
	}

	at = newCodeAreaTest("a\r\nb\nc", &CodeArea{Label: "Value", AutoFocus: true})
	at.area.state.Select(5, 5)
	at.frames()
	at.tt.Type("d")
	at.frames()
	if at.text != "a\r\nb\ncd" {
		t.Fatalf("mixed line endings after an edit: %q", at.text)
	}
	at.tt.Key(0, ui.KeyEnter)
	at.frames()
	if at.text != "a\r\nb\ncd\r\n" {
		t.Fatalf("a new line in a text with \\r\\n: %q", at.text)
	}
}

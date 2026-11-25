package widgets

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// A prompt is answered by one of its buttons, the last with Enter, or
// dismissed by Esc; a click outside it answers nothing, and it stays. It
// is one of the app's dialogs, as wide as the small ones.
func TestPromptAnswers(t *testing.T) {
	open, answer := true, -1
	tt := ui.NewTester(func(c *ui.Context) {
		ApplyTheme(c, "")
		if got := Prompt(c, &open, "Discard your changes?", "What you entered here is lost.", "Discard", "Keep Editing"); got != -1 {
			answer = got
		}
	}, 900, 600)
	shown := func() {
		t.Helper()
		open, answer = true, -1
		tt.Frame()
		tt.Advance(time.Second)
		tt.Frame()
	}

	shown()
	panel, ok := tt.Access("Discard your changes?")
	if !ok || panel.Bounds.W != DialogSmall+2*(DialogPad+1) {
		t.Errorf("the prompt's panel is %v wide (found %v), want a small dialog's", panel.Bounds.W, ok)
	}
	if !tt.HasText("What you entered here is lost.") {
		t.Errorf("the prompt's message does not show: %q", tt.Texts())
	}
	tt.ClickAt(5, 5)
	tt.Frame()
	if !open || answer != -1 {
		t.Errorf("a click outside: open %v, answer %d; want it open and unanswered", open, answer)
	}

	shown()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if open || answer != PromptDismissed {
		t.Errorf("Esc: open %v, answer %d; want it closed and dismissed", open, answer)
	}

	shown()
	if err := tt.Click("Discard"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if open || answer != 0 {
		t.Errorf("Discard: open %v, answer %d; want it closed and 0", open, answer)
	}

	shown()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if open || answer != 1 {
		t.Errorf("Enter: open %v, answer %d; want it closed and its last button", open, answer)
	}
}

package widgets_test

import (
	"testing"

	"dgopher/internal/testutil"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

const cardDetail = "dial tcp 10.0.0.5:5432: connect: connection refused\nis the server running?"

// Copy hands over the full text, every line of it, not the first.
func TestErrorCardCopiesFullText(t *testing.T) {
	r := widgets.ErrorReport{Title: "Could not connect", Hint: "The server refused the connection.", Detail: cardDetail}
	var open bool
	tt := ui.NewTester(func(c *ui.Context) {
		widgets.ErrorCard(c, r, &open, c.WriteClipboard)
	}, 600, 400)
	testutil.Settled(t, tt, "Copy")
	if err := tt.Click("Copy"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); got != cardDetail {
		t.Fatalf("clipboard: %q", got)
	}
}

// Clicking an action runs it and reports which one it was.
func TestErrorCardRunsAction(t *testing.T) {
	retries, edits := 0, 0
	r := widgets.ErrorReport{Title: "Could not connect", Detail: cardDetail, Actions: []widgets.ErrorAction{
		{Label: "Edit connection", Run: func() { edits++ }},
		{Label: "Retry", Run: func() { retries++ }},
	}}
	var open bool
	clicked := false
	tt := ui.NewTester(func(c *ui.Context) {
		if widgets.ErrorCard(c, r, &open, nil) {
			clicked = true
		}
	}, 600, 400)
	testutil.Settled(t, tt, "Retry")
	if err := tt.Click("Retry"); err != nil {
		t.Fatal(err)
	}
	if !clicked || retries != 1 || edits != 0 {
		t.Fatalf("Retry: clicked %v, %d retries, %d edits", clicked, retries, edits)
	}
}

// Without a hint the full text shows at once; with one it waits behind a
// closed Details disclosure.
func TestErrorCardOpensDetailsWithoutHint(t *testing.T) {
	r := widgets.ErrorReport{Title: "Could not connect", Detail: cardDetail}
	var open bool
	tt := ui.NewTester(func(c *ui.Context) {
		widgets.ErrorCard(c, r, &open, nil)
	}, 600, 400)
	tt.Frame()
	if !testutil.HasTextContaining(tt, "connection refused") {
		t.Fatalf("no hint: %q", tt.Texts())
	}
	testutil.Snapshot(t, tt, "errorcard-no-hint")

	r.Hint = "The server refused the connection."
	tt.Frame()
	if testutil.HasTextContaining(tt, "connection refused") || !tt.HasText("Details") {
		t.Fatalf("hint, closed: %q", tt.Texts())
	}
	if err := tt.Click("Details"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the details", func() bool { return testutil.HasTextContaining(tt, "connection refused") })
	if !open {
		t.Fatal("Details did not open the caller's disclosure")
	}
	testutil.Snapshot(t, tt, "errorcard-hint-open")
}

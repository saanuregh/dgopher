package widgets

import (
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"

	"github.com/egoist/mygo/ui"
)

// A request's notes show under its reasons, and its extra button, left of
// the others, closes it and answers with its own callback only.
func TestConfirmNotesAndExtra(t *testing.T) {
	cn := &connection.Conn{Config: db.Config{Name: "shop-prod", Env: db.Production}}
	confirmed, extra := false, false
	r := &ConfirmRequest{Open: true, Conn: cn, Title: "Run 1 statement on shop-prod?", Reasons: []string{"UPDATE on production"},
		Notes: []string{"It changes 3 rows of orders, counted just now."}, Preview: "UPDATE orders SET a = 1",
		Action: "Run", OnConfirm: func() { confirmed = true }, Extra: "Preview Changes", OnExtra: func() { extra = true }}
	tt := ui.NewTester(func(c *ui.Context) {
		ApplyTheme(c, "")
		if r.Open {
			ConfirmView(c, r)
		}
	}, 1000, 700)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if !tt.HasText("It changes 3 rows of orders, counted just now.") {
		t.Fatalf("the note does not show: %q", tt.Texts())
	}
	more, ok := tt.Access("Preview Changes")
	cancel, ok2 := tt.Access("Cancel")
	if !ok || !ok2 || more.Bounds.X >= cancel.Bounds.X {
		t.Errorf("the extra button is at %v and Cancel at %v (found %v, %v); want it left of Cancel", more.Bounds, cancel.Bounds, ok, ok2)
	}
	if err := tt.Click("Preview Changes"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !extra || confirmed || r.Open || !r.Answered {
		t.Errorf("extra %v, confirmed %v, open %v, answered %v; want only the extra answer, closed", extra, confirmed, r.Open, r.Answered)
	}
}

package widgets_test

import (
	"testing"
	"time"

	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// An unavailable button keeps the keyboard's focus, which shows why it is
// unavailable, and does nothing when clicked; once available, a click
// runs it.
func TestUnavailableShowsWhyToTheKeyboard(t *testing.T) {
	why := "Connect to a database first"
	ran := false
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Padding(10).Children(func() {
			if widgets.Unavailable(ui.Button(c, "Run"), why).Clicked() && why == "" {
				ran = true
			}
		})
	}, 400, 300)
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Advance(2 * time.Second)
	tt.Frame()
	if !tt.HasText(why) {
		t.Fatalf("the focused button shows no reason: %q", tt.Texts())
	}
	if err := tt.Click("Run"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ran {
		t.Fatal("an unavailable button ran")
	}
	why = ""
	tt.Frame()
	if err := tt.Click("Run"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !ran {
		t.Fatal("the available button did not run")
	}
}

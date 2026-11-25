package widgets_test

import (
	"cmp"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// A button that cannot be pressed says why: it is made unavailable with
// Unavailable, Clicked or ActivatedUnless, which give it a reason as its
// tooltip and leave it to the keyboard's focus, not disabled, which
// tells the user nothing.
func TestUnavailableButtonsSayWhy(t *testing.T) {
	disabled := regexp.MustCompile(`Button\w*\(.*\.Disabled\(`)
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(src), "\n") {
			if disabled.MatchString(line) {
				t.Errorf("%s:%d: a disabled button gives no reason: %s", filepath.ToSlash(rel), i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Clicked and When make a button unavailable in one line: the click is
// not reported while a reason holds, and the first reason that holds is
// the one shown.
func TestClickedIgnoresAnUnavailableButton(t *testing.T) {
	busy, empty, ran := true, true, 0
	tt := ui.NewTester(func(c *ui.Context) {
		why := cmp.Or(widgets.When(busy, "The copy is running"), widgets.When(empty, "Choose a table first"))
		if widgets.Clicked(ui.Button(c, "Copy"), why) {
			ran++
		}
	}, 400, 300)
	tt.Frame()
	for _, step := range []struct {
		busy, empty bool
		want        int
	}{{true, true, 0}, {false, true, 0}, {false, false, 1}} {
		busy, empty = step.busy, step.empty
		tt.Frame()
		if err := tt.Click("Copy"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if ran != step.want {
			t.Fatalf("busy %v, empty %v: ran %d times, want %d", busy, empty, ran, step.want)
		}
		info, _ := tt.Access("Copy")
		if info.Disabled != (busy || empty) || !info.Focusable {
			t.Errorf("busy %v, empty %v: disabled %v, focusable %v", busy, empty, info.Disabled, info.Focusable)
		}
	}
}

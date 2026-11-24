package widgets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// A dialog's content is as wide as the width it is given, whichever of
// the four, and in a window narrower than that it leaves the window's
// edges their room.
func TestDialogWidths(t *testing.T) {
	const window = 1300
	for _, width := range []float32{DialogSmall, DialogMedium, DialogLarge, DialogWide} {
		open := true
		tt := ui.NewTester(func(c *ui.Context) {
			ApplyTheme(c, "")
			Modal(c, &open, width, func() {
				ui.Box(c).FillWidth().Height(10).Label("content")
			})
		}, window, 700)
		// Come in: a dialog grows into its place.
		tt.Frame()
		tt.Advance(time.Second)
		tt.Frame()
		content, ok := tt.Access("content")
		if !ok || content.Bounds.W != width {
			t.Errorf("a dialog of %v: its content is %v wide (found %v)", width, content.Bounds.W, ok)
		}
		tt.SetSize(500, 700)
		tt.Frame()
		content, _ = tt.Access("content")
		if left, right := content.Bounds.X, 500-content.Bounds.X-content.Bounds.W; content.Bounds.W >= width || left < 20 || right < 20 {
			t.Errorf("a dialog of %v in a window of 500: content %v wide, %v and %v from its edges", width, content.Bounds.W, left, right)
		}
	}
}

// Every dialog's panel is built here, by Dialog, Picker or Prompt: one
// built elsewhere, or the toolkit's own alert, would have a border, a
// corner and a fit to the window of its own.
func TestDialogsShareOneShell(t *testing.T) {
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
		here := filepath.Dir(path) == filepath.Join(root, "ui", "widgets")
		for i, line := range strings.Split(string(src), "\n") {
			if !here && strings.Contains(line, "ui.DialogBase(") {
				t.Errorf("%s:%d: a dialog's panel built outside widgets: %s", filepath.ToSlash(rel), i+1, strings.TrimSpace(line))
			}
			if strings.Contains(line, "ui.AlertDialog(") {
				t.Errorf("%s:%d: the toolkit's alert, in place of Prompt: %s", filepath.ToSlash(rel), i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

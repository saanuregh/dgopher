package widgets

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Text with no size of its own is the body's on every system, in the light
// look and the dark one: the toolkit's own differs from one system to the
// next.
func TestUnsizedTextIsThirteen(t *testing.T) {
	for _, dark := range []bool{false, true} {
		tt := ui.NewTester(func(c *ui.Context) {
			ApplyTheme(c, "")
			ui.Column(c).Children(func() {
				ui.Text(c, "unsized")
				ui.Text(c, "sized").FontSize(TextBody)
				ui.Button(c, "pressed")
			})
		}, 300, 200)
		tt.SetDark(dark)
		tt.Frame()
		unsized, okU := tt.Find("unsized")
		sized, okS := tt.Find("sized")
		if !okU || !okS || unsized.H != sized.H {
			t.Errorf("dark %v: unsized text is %v high, text of the body's size %v", dark, unsized.H, sized.H)
		}
	}
}

// A size written as a number is one the scale does not know: text, icons
// and corners take theirs by name (sizes.go), so that the same kind of
// thing is the same size everywhere. Radii up to 3 shape small dots and
// bars.
func TestSizesAreNamed(t *testing.T) {
	fontSize := regexp.MustCompile(`FontSize\(\s*[0-9]`)
	radius := regexp.MustCompile(`\bRadius\(\s*([0-9.]+)\s*\)`)
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "sizes.go" {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(src), "\n") {
			if fontSize.MatchString(line) {
				t.Errorf("%s:%d: a font size as a number: %s", filepath.ToSlash(rel), i+1, strings.TrimSpace(line))
			}
			for _, m := range radius.FindAllStringSubmatch(line, -1) {
				if r, _ := strconv.ParseFloat(m[1], 64); r > 3 {
					t.Errorf("%s:%d: a corner as a number: %s", filepath.ToSlash(rel), i+1, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

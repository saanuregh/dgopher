package widgets

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// ShowAfter is how long work runs before it shows: what ends sooner would
// only flash.
const ShowAfter = 300 * time.Millisecond

// MinShown is how long a sign of work, once shown, stays: less would
// read as a flicker.
const MinShown = 400 * time.Millisecond

// Lasted reports whether work begun at since has run ShowAfter, asking for
// a frame then when it has not yet.
func Lasted(c *ui.Context, since time.Time) bool {
	left := ShowAfter - time.Since(since)
	if left > 0 {
		c.After(left)
		return false
	}
	return true
}

// ActivityBar is the app's one sign of work going on: a thin bar, moving,
// along the top of what holds it.
func ActivityBar(c *ui.Context) {
	ui.Progress(c, -1).Height(2).Radius(0).Label("Working")
}

// Skeleton stands where rows being read will show, once the read has
// lasted ShowAfter since since: faint bars in the shape of a table, a
// light passing over them, so the wait shows where its result will be.
// The light only paints, from the clock; under reduced motion the bars
// keep still.
func Skeleton(c *ui.Context, since time.Time) {
	if !Lasted(c, since) {
		ui.Box(c).Grow(1)
		return
	}
	t := c.Theme()
	base, lit := t.Text.Alpha(0.05), t.Text.Alpha(0.11)
	still := c.Preferences().ReduceMotion
	ui.Box(c).Grow(1).FillWidth().Label("Reading the rows").Draw(func(p *ui.Painter, r ui.Rect) {
		// Where the light is along the width, from before the left edge
		// to past the right one.
		at := float32(-1e9)
		if !still {
			p.AnimationFrameRate(pulseFrameRate)
			phase := float32(p.Now().UnixMilli()%skeletonSweep) / skeletonSweep
			at = r.X - 0.3*r.W + phase*1.6*r.W
		}
		const rowH, barH = 28, 10
		y := r.Y + 10
		for i := 0; y+rowH <= r.Y+r.H; i++ {
			x := r.X + 12
			for j, part := range skeletonCols {
				w := part * (r.W - 24)
				fill := skeletonFill[(i+j)%len(skeletonFill)]
				if i == 0 {
					fill = 0.45 // a column's name
				}
				bar := ui.Rect{X: x, Y: y + (rowH-barH)/2, W: max((w-16)*fill, 4), H: barH}
				d := (bar.X + bar.W/2 - at) / (0.22 * r.W)
				light := max(0, 1-d*d)
				col := base.Mix(lit, light)
				if i == 0 {
					col = col.Mix(lit, 0.5) // the header, a step stronger
				}
				p.Fill(bar, col, barH/2)
				x += w
			}
			y += rowH
		}
	})
}

// skeletonSweep is how long, in milliseconds, the light takes to cross.
const skeletonSweep = 1400

// skeletonCols are the widths of the columns, as parts of the width;
// skeletonFill, how much of its cell a bar fills, varied from cell to cell
// as values are.
var (
	skeletonCols = []float32{0.14, 0.30, 0.24, 0.32}
	skeletonFill = []float32{0.9, 0.55, 0.75, 0.4, 0.65, 0.85, 0.5}
)

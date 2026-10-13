package widgets

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// ShowAfter is how long work runs before it shows: what ends sooner would
// only flash.
const ShowAfter = 300 * time.Millisecond

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

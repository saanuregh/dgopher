package dataview

import (
	"testing"
	"time"
)

// Work still going on as a test ends is waited for, and what it posts is
// run, as the app runs it before it quits: left unrun, a session the work
// brought stayed open, and its file with it, past the end of the tests.
func TestFakeHostSettlesBackgroundWorkAtCleanup(t *testing.T) {
	ran := false
	t.Run("a test that ends with work going on", func(t *testing.T) {
		h := NewFakeHost(t)
		h.Background(func() func() {
			time.Sleep(100 * time.Millisecond)
			return func() { ran = true }
		})
	})
	if !ran {
		t.Error("the work's result, posted after the test ended, was never run")
	}
}

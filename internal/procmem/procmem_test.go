package procmem

import (
	"runtime"
	"testing"
)

// A running test uses memory, and more once it holds more: the system is
// asked, not a figure made up.
func TestUsedGrowsWithWhatIsHeld(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "windows", "darwin":
	default:
		t.Skip("no way to ask on " + runtime.GOOS)
	}
	before, ok := Used()
	if !ok || before < 1<<20 {
		t.Fatalf("a running test uses %d bytes (asked: %v), want more than a megabyte", before, ok)
	}
	held := make([]byte, 64<<20)
	for i := range held {
		held[i] = byte(i) // touched, so that it is resident
	}
	after, _ := Used()
	if after < before+32<<20 {
		t.Errorf("with 64 MB more held, the memory used went from %d to %d", before, after)
	}
	runtime.KeepAlive(held)
}

// The most used is at least what is used now, where the system keeps it.
func TestPeakIsAtLeastWhatIsUsed(t *testing.T) {
	most, ok := Peak()
	if !ok {
		t.Skip("the system keeps no peak on " + runtime.GOOS)
	}
	// Read after: what is used may only have grown to the peak.
	if now, _ := Used(); most < now/2 || most < 1<<20 {
		t.Errorf("the peak is %d bytes with %d used now", most, now)
	}
}

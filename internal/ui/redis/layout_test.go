package redis

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// The panel under the keys keeps its height in a taller window: the keys
// take the room, not an empty console.
func TestPanelKeepsItsHeightInATallWindow(t *testing.T) {
	f := newFakeRedis(t, stringKey("k", "v", -1))
	_, tt, _, _ := openFake(t, f, db.Development)
	heights := map[int]float32{}
	for _, h := range []int{700, 1100} {
		tt.SetSize(1200, h)
		tt.Frame()
		tt.Frame()
		panels, ok := tt.Access("Panels")
		if !ok {
			t.Fatalf("no panel bar at %d px: %q", h, tt.Texts())
		}
		heights[h] = float32(h) - panels.Bounds.Y
	}
	if heights[700] != heights[1100] {
		t.Errorf("the panel is %v high in a 700 px window and %v in a 1100 px one", heights[700], heights[1100])
	}
}

// The type filter sits beside the pattern, as both say which keys to
// list, and the count of keys with the button that reads more of them.
func TestTypeFilterIsBesideThePattern(t *testing.T) {
	f := newFakeRedis(t, stringKey("k", "v", -1))
	_, tt, _, _ := openFake(t, f, db.Development)
	tt.Frame()
	typ, okT := tt.Access("Type")
	var pattern, count struct{ y, h float32 }
	for _, n := range tt.AccessAll() {
		switch {
		case strings.HasPrefix(n.Name, "Pattern"):
			pattern.y, pattern.h = n.Bounds.Y, n.Bounds.H
		case strings.HasSuffix(n.Name, " keys"):
			count.y, count.h = n.Bounds.Y, n.Bounds.H
		}
	}
	if !okT || pattern.h == 0 {
		t.Fatalf("the type filter shows %v, the pattern box is %v high", okT, pattern.h)
	}
	if mid := typ.Bounds.Y + typ.Bounds.H/2; mid < pattern.y || mid > pattern.y+pattern.h {
		t.Errorf("the type filter at %v is not in the row of the pattern at %v", typ.Bounds, pattern)
	}
	testutil.Snapshot(t, tt, "redis-browser")
	if count.h == 0 || count.y < typ.Bounds.Y+200 {
		t.Errorf("the count of keys at %v is not under the keys", count)
	}
}

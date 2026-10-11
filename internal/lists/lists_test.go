package lists

import (
	"slices"
	"strconv"
	"testing"
)

func TestMap(t *testing.T) {
	got := Map([]int{3, 1, 2}, strconv.Itoa)
	if want := []string{"3", "1", "2"}; !slices.Equal(got, want) {
		t.Fatalf("Map = %q, want %q", got, want)
	}
	for _, in := range [][]int{nil, {}} {
		if got := Map(in, strconv.Itoa); got == nil || len(got) != 0 {
			t.Fatalf("Map(%#v) = %#v, want a non-nil empty slice", in, got)
		}
	}
}

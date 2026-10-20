package panes

import "testing"

// Splits put groups where they were asked; neighbours are found across
// splits; a group taken out gives its place and the focus to the one
// beside.
func TestSplitNeighborRemove(t *testing.T) {
	l := New()
	a := l.Focused()
	b := l.Split(a, Right) // a | b
	c := l.Split(b, Down)  // a | (b / c)
	d := l.Split(a, Left)  // (d | a) | (b / c)
	if got := l.Groups(); len(got) != 4 || got[0] != d || got[1] != a || got[2] != b || got[3] != c {
		t.Fatalf("groups %v", ids(got))
	}
	if l.Focused() != a {
		t.Fatal("a split took the focus")
	}
	for _, tc := range []struct {
		from *Group
		d    Direction
		want *Group
	}{
		{a, Right, b}, {a, Left, d}, {d, Left, nil}, {c, Up, b}, {b, Down, c}, {c, Left, a}, {b, Up, nil}, {d, Right, a},
	} {
		if got := l.Neighbor(tc.from, tc.d); got != tc.want {
			t.Errorf("neighbour of %d toward %d: %v, want %v", tc.from.ID, tc.d, idOf(got), idOf(tc.want))
		}
	}
	l.Focus(b)
	if next := l.Remove(b); next != c || l.Focused() != c {
		t.Fatalf("after b: focus %v", idOf(l.Focused()))
	}
	if got := l.Groups(); len(got) != 3 || l.Neighbor(a, Right) != c {
		t.Fatalf("groups %v", ids(got))
	}
	l.Focus(d)
	l.Remove(d)
	if l.Focused() != a {
		t.Fatalf("after d: focus %v", idOf(l.Focused()))
	}
	l.Remove(a)
	if l.Remove(c) != nil || l.Len() != 1 || l.Focused() != c {
		t.Fatal("the last group went")
	}
}

func TestSaveRestore(t *testing.T) {
	l := New()
	b := l.Split(l.Focused(), Right)
	l.Split(b, Down)
	l.Root().Percent = 30
	r := Restore(l.Save())
	if r.Len() != 3 || r.Root().Percent != 30 || r.Root().Then.Axis != Column {
		t.Fatalf("restored %+v", r.Save())
	}
	if Restore(&Saved{First: &Saved{}}).Len() != 1 {
		t.Error("a broken tree made more than one group")
	}
	ids := map[int]bool{}
	for _, g := range r.Groups() {
		if ids[g.ID] {
			t.Error("two groups share an ID")
		}
		ids[g.ID] = true
	}
}

func ids(groups []*Group) []int {
	out := make([]int, len(groups))
	for i, g := range groups {
		out[i] = g.ID
	}
	return out
}

func idOf(g *Group) any {
	if g == nil {
		return nil
	}
	return g.ID
}

// The group beside one is the nearest of the node it shares a split with,
// whichever has the focus.
func TestBeside(t *testing.T) {
	l := New()
	a := l.Focused()
	b := l.Split(a, Right)
	c := l.Split(b, Right)
	l.Focus(a)
	if got := l.Beside(c); got != b {
		t.Fatalf("beside c: group %d, want b", l.Index(got))
	}
	if got := l.Beside(a); got != b {
		t.Fatalf("beside a: group %d, want b (nearest of b|c from the left)", l.Index(got))
	}
}

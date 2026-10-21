package tablist

import (
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

type tab string

func (t *tab) Title() string                { return string(*t) }
func (t *tab) Connection() *connection.Conn { return nil }
func (t *tab) View(*ui.Context)             {}
func (t *tab) CloseReason() string          { return "" }
func (t *tab) Close()                       {}

// list is a list of tabs named by the letters of names, the last in front.
func list(names string) (*List, map[string]widgets.Tab) {
	l, by := &List{}, map[string]widgets.Tab{}
	for _, r := range names {
		t := tab(string(r))
		by[string(r)] = &t
		l.Add(&t)
	}
	return l, by
}

// order names the tabs, pinned ones with a *, the one in front in [ ].
func order(l *List) string {
	var out []string
	for _, t := range l.All() {
		name := t.Title()
		if l.Pinned(t) {
			name += "*"
		}
		if t == l.Active() {
			name = "[" + name + "]"
		}
		out = append(out, name)
	}
	return strings.Join(out, " ")
}

func check(t *testing.T, l *List, want string) {
	t.Helper()
	if got := order(l); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestAddSelectAndCycle(t *testing.T) {
	l, by := list("abc")
	check(t, l, "a b [c]")
	l.Next()
	check(t, l, "[a] b c")
	l.Previous()
	check(t, l, "a b [c]")
	l.SelectAt(9)
	check(t, l, "a b [c]")
	if !l.Select(by["b"]) || l.Select(&[]tab{"x"}[0]) {
		t.Fatal("Select reports a tab not in the list")
	}
	check(t, l, "a [b] c")
	d := tab("d")
	l.Append(&d)
	check(t, l, "a [b] c d")
	empty := &List{}
	empty.Append(&d)
	check(t, empty, "[d]")
}

// Pinned tabs come first, in the order pinned; unpinned, a tab goes first
// of the rest. The tab in front stays in front.
func TestPinKeepsGroups(t *testing.T) {
	l, by := list("abcd")
	l.SetPinned(by["c"], true)
	check(t, l, "c* a b [d]")
	l.SetPinned(by["b"], true)
	check(t, l, "c* b* a [d]")
	l.SetPinned(by["c"], false)
	check(t, l, "b* c a [d]")
	if l.PinnedCount() != 1 {
		t.Fatalf("pinned %d", l.PinnedCount())
	}
}

// A tab moves within its group, pinned or not.
func TestMoveWithinGroup(t *testing.T) {
	l, by := list("abcd")
	l.SetPinned(by["a"], true)
	l.Move(by["d"], 0)
	check(t, l, "a* [d] b c")
	l.Move(by["a"], 3)
	check(t, l, "a* [d] b c")
	l.Move(by["b"], 9)
	check(t, l, "a* [d] c b")
}

// Removing the tab in front brings the next forward, the last's the one
// before; a pin goes with its tab.
func TestRemove(t *testing.T) {
	l, by := list("abcd")
	l.Select(by["b"])
	if at, ok := l.Remove(by["b"]); !ok || at != 1 {
		t.Fatalf("removed at %d, %v", at, ok)
	}
	check(t, l, "a [c] d")
	l.Select(by["d"])
	l.Remove(by["d"])
	check(t, l, "a [c]")
	l.Remove(by["a"])
	check(t, l, "[c]")
	l.SetPinned(by["c"], true)
	l.Remove(by["c"])
	if _, ok := l.Remove(by["c"]); ok || l.Len() != 0 || l.Active() != nil {
		t.Fatal("removed twice, or a tab left in front")
	}
	l.Add(by["c"])
	check(t, l, "[c]")
}

// A tab put in the place of another takes its place, pin and front.
func TestReplace(t *testing.T) {
	l, by := list("abc")
	l.SetPinned(by["b"], true)
	l.Select(by["b"])
	x, y := tab("x"), tab("y")
	if !l.Replace(by["b"], &x) || !l.Replace(by["c"], &y) {
		t.Fatal("not replaced")
	}
	check(t, l, "[x*] a y")
	if l.Replace(by["b"], &y) {
		t.Fatal("replaced a tab not in the list")
	}
}

// A tab put back at a place stays within its group, in front.
func TestInsert(t *testing.T) {
	l, by := list("abc")
	l.SetPinned(by["a"], true)
	x, y := tab("x"), tab("y")
	l.Insert(&x, 2, true)
	check(t, l, "a* [x*] b c")
	l.Insert(&y, 0, false)
	check(t, l, "a* x* [y] b c")
}

// TakeAll empties the list, its tabs in order.
func TestTakeAll(t *testing.T) {
	l, by := list("ab")
	l.SetPinned(by["b"], true)
	got := l.TakeAll()
	if len(got) != 2 || got[0] != by["b"] || l.Len() != 0 || l.Active() != nil || l.Pinned(by["b"]) {
		t.Fatalf("took %v, left %s", got, order(l))
	}
}

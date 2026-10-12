// Package tablist keeps a window's tabs: their order, the pinned ones, the
// one in front and the one beside it. Only its methods change them, so
// that every change keeps the rules List states.
package tablist

import (
	"iter"
	"slices"

	"dgopher/internal/ui/widgets"
)

// List is a window's tabs. The pinned ones come first, in the order
// pinned, and a tab moves within its group; the tab in front stays in
// front as others move; the tab beside it is never the one in front, and
// a pin or a place beside goes with its tab.
type List struct {
	tabs   []widgets.Tab
	active int
	pinned map[widgets.Tab]bool
	// side is shown beside the tab in front, left of it when sideLeft is
	// set; nil for none.
	side     widgets.Tab
	sideLeft bool
}

func (l *List) Len() int { return len(l.tabs) }

func (l *List) At(i int) widgets.Tab { return l.tabs[i] }

// Index is a tab's place, -1 for one not in the list.
func (l *List) Index(t widgets.Tab) int { return slices.Index(l.tabs, t) }

func (l *List) Contains(t widgets.Tab) bool { return slices.Contains(l.tabs, t) }

// All yields the tabs with their places as they were when it was called:
// a change while it runs moves nothing it yields.
func (l *List) All() iter.Seq2[int, widgets.Tab] { return slices.All(slices.Clone(l.tabs)) }

// Tabs returns the tabs, a copy.
func (l *List) Tabs() []widgets.Tab { return slices.Clone(l.tabs) }

// Active is the tab in front, nil for none.
func (l *List) Active() widgets.Tab {
	if l.active >= 0 && l.active < len(l.tabs) {
		return l.tabs[l.active]
	}
	return nil
}

// ActiveIndex is the place of the tab in front.
func (l *List) ActiveIndex() int { return l.active }

// Select brings a tab in front, and reports whether the list holds it.
func (l *List) Select(t widgets.Tab) bool {
	i := l.Index(t)
	if i >= 0 {
		l.SelectAt(i)
	}
	return i >= 0
}

// SelectAt brings the tab at i in front, i held within the list.
func (l *List) SelectAt(i int) {
	l.active = max(0, min(i, len(l.tabs)-1))
	l.settle()
}

// Next brings the next tab in front, the first after the last.
func (l *List) Next() {
	if len(l.tabs) > 0 {
		l.SelectAt((l.active + 1) % len(l.tabs))
	}
}

// Previous brings the tab before in front, the last before the first.
func (l *List) Previous() {
	if len(l.tabs) > 0 {
		l.SelectAt((l.active + len(l.tabs) - 1) % len(l.tabs))
	}
}

// Add puts a tab last, in front.
func (l *List) Add(t widgets.Tab) {
	l.tabs = append(l.tabs, t)
	l.SelectAt(len(l.tabs) - 1)
}

// Append puts a tab last, behind the one in front: in front only of a list
// that was empty.
func (l *List) Append(t widgets.Tab) { l.tabs = append(l.tabs, t) }

// Insert puts a tab at i, pinned or not, in front; i is held within its
// group.
func (l *List) Insert(t widgets.Tab, i int, pinned bool) {
	n := l.PinnedCount()
	lo, hi := n, len(l.tabs)
	if pinned {
		lo, hi = 0, n
		l.pin(t)
	}
	i = max(lo, min(i, hi))
	l.tabs = slices.Insert(l.tabs, i, t)
	l.SelectAt(i)
}

// Remove takes a tab out, and reports the place it had. The tab after it
// comes in front when it was, else the one before when it was last.
func (l *List) Remove(t widgets.Tab) (int, bool) {
	i := l.Index(t)
	if i < 0 {
		return 0, false
	}
	l.tabs = slices.Delete(l.tabs, i, i+1)
	delete(l.pinned, t)
	if l.active >= len(l.tabs) || l.active > i {
		l.active--
	}
	if l.side == t {
		l.side = nil
	}
	l.SelectAt(l.active)
	return i, true
}

// Replace puts next in the place of old, pinned, in front or beside as
// old was, and reports whether the list held old.
func (l *List) Replace(old, next widgets.Tab) bool {
	i := l.Index(old)
	if i < 0 {
		return false
	}
	l.tabs[i] = next
	if l.pinned[old] {
		delete(l.pinned, old)
		l.pin(next)
	}
	if l.side == old {
		l.side = next
	}
	return true
}

// TakeAll empties the list, and returns its tabs in order.
func (l *List) TakeAll() []widgets.Tab {
	tabs := l.tabs
	*l = List{}
	return tabs
}

// Pinned reports whether a tab is pinned.
func (l *List) Pinned(t widgets.Tab) bool { return l.pinned[t] }

// PinnedCount is how many tabs are pinned: the first ones.
func (l *List) PinnedCount() int {
	n := 0
	for _, t := range l.tabs {
		if l.pinned[t] {
			n++
		}
	}
	return n
}

// Move puts a tab at i, held within its group, pinned or not.
func (l *List) Move(t widgets.Tab, i int) {
	at := l.Index(t)
	if at < 0 {
		return
	}
	front := l.Active()
	n := l.PinnedCount()
	lo, hi := n, len(l.tabs)-1
	if l.pinned[t] {
		lo, hi = 0, n-1
	}
	l.tabs = slices.Insert(slices.Delete(l.tabs, at, at+1), max(lo, min(i, hi)), t)
	l.active = max(0, l.Index(front))
}

// SetPinned pins a tab, after the others pinned, or unpins it, first of
// the rest.
func (l *List) SetPinned(t widgets.Tab, on bool) {
	if l.pinned[t] == on || !l.Contains(t) {
		return
	}
	n := l.PinnedCount()
	if on {
		l.pin(t)
		l.Move(t, n)
		return
	}
	delete(l.pinned, t)
	l.Move(t, n-1)
}

func (l *List) pin(t widgets.Tab) {
	if l.pinned == nil {
		l.pinned = map[widgets.Tab]bool{}
	}
	l.pinned[t] = true
}

// Side is the tab shown beside the one in front, nil for none.
func (l *List) Side() widgets.Tab { return l.side }

// SideLeft reports whether the tab beside shows left of the one in front.
func (l *List) SideLeft() bool { return l.sideLeft }

// SetSide shows a tab of the list beside the one in front, left of it when
// left is set: not the one in front, which leaves the split as it is. Nil
// ends the split.
func (l *List) SetSide(t widgets.Tab, left bool) {
	switch {
	case t == nil:
		l.side = nil
	case t != l.Active() && l.Contains(t):
		l.side, l.sideLeft = t, left
	}
}

// BringSide brings the tab beside in front, each staying where it is: the
// one in front before goes beside it.
func (l *List) BringSide() {
	i := l.Index(l.side)
	if i < 0 {
		return
	}
	l.side, l.active, l.sideLeft = l.Active(), i, !l.sideLeft
}

// settle ends the split when the tab beside came in front.
func (l *List) settle() {
	if l.side != nil && l.side == l.Active() {
		l.side = nil
	}
}

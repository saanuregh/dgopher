// Package panes is a window's editor groups: a tree of splits whose
// leaves are groups of tabs, each with its own tab bar, one of them
// focused. Only its methods change the tree, so that it stays whole: a
// split has two children, and the last group never goes.
package panes

import (
	"slices"

	"dgopher/internal/ui/tablist"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Group is a group of tabs, with the state of its tab bar.
type Group struct {
	Tabs tablist.List
	// TabScroll is its tab bar's, whose overflow shows the list of tabs;
	// RevealTab scrolls the tab in front into view once.
	TabScroll ui.ScrollState
	RevealTab bool
	// Dropping is the tab dragged over the group, whose drop zones are up:
	// a zone, taking the drag from the content under it, keeps them up.
	Dropping widgets.Tab
	// HadKeys is whether the keys were in it in the last frame: the keys
	// coming into it make it the focused group, their staying does not,
	// as they stay a frame in one a command focused another from.
	HadKeys bool
	// InView is the tab it showed in the window's last frame.
	InView widgets.Tab
	// ID tells the group from the others in the frame's keys.
	ID int
}

// Axis is how a split lays its two children out.
type Axis uint8

const (
	Row    Axis = iota // side by side
	Column             // one above the other
)

// Direction is where a split puts a new group, or where a neighbour is.
type Direction uint8

const (
	Right Direction = iota
	Left
	Down
	Up
)

func (d Direction) axis() Axis {
	if d == Right || d == Left {
		return Row
	}
	return Column
}

// forward reports whether the direction goes to the second child of a
// split: right, or down.
func (d Direction) forward() bool { return d == Right || d == Down }

// Node is a group, or a split of two nodes.
type Node struct {
	Group *Group // a leaf's; nil for a split
	Axis  Axis
	// Percent is the first child's share of the split, as
	// ui.SplitPercent keeps it.
	Percent     float32
	First, Then *Node
	parent      *Node
}

// Leaf reports whether the node is a group.
func (n *Node) Leaf() bool { return n.Group != nil }

// Layout is a window's groups.
type Layout struct {
	root    *Node
	focused *Group
	nextID  int
	// groups are Groups, made again once the tree changed (nil): the
	// window asks for them many times a frame.
	groups []*Group
}

// New is a layout of one group, empty.
func New() *Layout {
	g := &Group{ID: 1}
	return &Layout{root: &Node{Group: g}, focused: g, nextID: 2}
}

// Root is the tree's top: the one group, or the split of them all.
func (l *Layout) Root() *Node { return l.root }

// Focused is the group the window's keys go to.
func (l *Layout) Focused() *Group { return l.focused }

// Focus gives a group of the layout the window's keys.
func (l *Layout) Focus(g *Group) {
	if l.node(g) != nil {
		l.focused = g
	}
}

// Groups are the groups, left to right and top to bottom: the layout's
// own list, not to be changed.
func (l *Layout) Groups() []*Group {
	if l.groups != nil {
		return l.groups
	}
	var out []*Group
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.Leaf() {
			out = append(out, n.Group)
			return
		}
		walk(n.First)
		walk(n.Then)
	}
	walk(l.root)
	l.groups = out
	return out
}

// Len is how many groups there are.
func (l *Layout) Len() int { return len(l.Groups()) }

// Index is a group's place among Groups, -1 for none.
func (l *Layout) Index(g *Group) int { return slices.Index(l.Groups(), g) }

// GroupOf is the group holding a tab, nil for none.
func (l *Layout) GroupOf(t widgets.Tab) *Group {
	for _, g := range l.Groups() {
		if g.Tabs.Contains(t) {
			return g
		}
	}
	return nil
}

// Tabs are every group's tabs, group after group.
func (l *Layout) Tabs() []widgets.Tab {
	var out []widgets.Tab
	for _, g := range l.Groups() {
		out = append(out, g.Tabs.Tabs()...)
	}
	return out
}

func (l *Layout) node(g *Group) *Node {
	var found *Node
	var walk func(n *Node)
	walk = func(n *Node) {
		switch {
		case found != nil:
		case n.Leaf():
			if n.Group == g {
				found = n
			}
		default:
			walk(n.First)
			walk(n.Then)
		}
	}
	walk(l.root)
	return found
}

// Split puts a new group, empty, beside g in direction d, sharing g's
// place half and half, and returns it. It does not take the focus.
func (l *Layout) Split(g *Group, d Direction) *Group {
	n := l.node(g)
	if n == nil {
		return nil
	}
	ng := &Group{ID: l.nextID}
	l.nextID++
	old := &Node{Group: n.Group}
	added := &Node{Group: ng}
	n.Group, n.Axis, n.Percent = nil, d.axis(), 50
	l.groups = nil
	n.First, n.Then = old, added
	if !d.forward() {
		n.First, n.Then = added, old
	}
	old.parent, added.parent = n, n
	return ng
}

// Beside is the group nearest g in the node beside it, which takes g's
// place as Remove takes g out; nil for the last group.
func (l *Layout) Beside(g *Group) *Group {
	n := l.node(g)
	if n == nil || n.parent == nil {
		return nil
	}
	return nearest(sibling(n))
}

// sibling is the node beside n, a split's child, and the side of it that
// faces n.
func sibling(n *Node) (*Node, Direction) {
	p := n.parent
	other, side := p.First, Right
	if other == n {
		other, side = p.Then, Left
	}
	if p.Axis == Column {
		side += Down - Right
	}
	return other, side
}

// Remove takes a group out, its place going to the node beside it, and
// returns the group that has the focus after: the nearest of the node
// beside when the group had it. The last group stays: it returns nil.
func (l *Layout) Remove(g *Group) *Group {
	n := l.node(g)
	if n == nil || n.parent == nil {
		return nil
	}
	p := n.parent
	other, side := sibling(n)
	// The sibling takes the parent's place.
	l.groups = nil
	*p = Node{Group: other.Group, Axis: other.Axis, Percent: other.Percent, First: other.First, Then: other.Then, parent: p.parent}
	if !p.Leaf() {
		p.First.parent, p.Then.parent = p, p
	}
	if l.focused == g {
		l.focused = nearest(p, side)
	}
	return l.focused
}

// nearest is the group of n at its side: its first from the left or the
// top, its last from the right or the bottom.
func nearest(n *Node, side Direction) *Group {
	for !n.Leaf() {
		if n.Axis == side.axis() && side.forward() {
			n = n.Then
		} else {
			n = n.First
		}
	}
	return n.Group
}

// Neighbor is the group next to g in direction d, nil for none.
func (l *Layout) Neighbor(g *Group, d Direction) *Group {
	n := l.node(g)
	for n != nil && n.parent != nil {
		p := n.parent
		if p.Axis == d.axis() {
			if d.forward() && p.First == n {
				return nearest(p.Then, opposite(d))
			}
			if !d.forward() && p.Then == n {
				return nearest(p.First, opposite(d))
			}
		}
		n = p
	}
	return nil
}

func opposite(d Direction) Direction {
	switch d {
	case Right:
		return Left
	case Left:
		return Right
	case Down:
		return Up
	}
	return Down
}

// Next is the group after g, the first after the last.
func (l *Layout) Next(g *Group) *Group {
	groups := l.Groups()
	i := slices.Index(groups, g)
	return groups[(i+1)%len(groups)]
}

// Saved is a layout as a workspace keeps it: a split's axis, share and
// children, or a group by its place among the groups.
type Saved struct {
	Group   int     `json:"group,omitempty"`
	Axis    Axis    `json:"axis,omitempty"`
	Percent float32 `json:"percent,omitempty"`
	First   *Saved  `json:"first,omitempty"`
	Then    *Saved  `json:"then,omitempty"`
}

// Save is the layout's tree, its groups by their place among Groups.
func (l *Layout) Save() *Saved {
	groups := l.Groups()
	var walk func(n *Node) *Saved
	walk = func(n *Node) *Saved {
		if n.Leaf() {
			return &Saved{Group: slices.Index(groups, n.Group)}
		}
		return &Saved{Axis: n.Axis, Percent: n.Percent, First: walk(n.First), Then: walk(n.Then)}
	}
	return walk(l.root)
}

// Restore makes a layout of a saved tree, its groups empty: as many as
// the tree has leaves, in their order. A tree that does not hold
// together makes one group.
func Restore(s *Saved) *Layout {
	l := New()
	if s == nil {
		return l
	}
	l.nextID = 1
	var build func(s *Saved, depth int) *Node
	build = func(s *Saved, depth int) *Node {
		if s.First == nil || s.Then == nil || depth > 16 {
			g := &Group{ID: l.nextID}
			l.nextID++
			return &Node{Group: g}
		}
		n := &Node{Axis: s.Axis % 2, Percent: s.Percent, First: build(s.First, depth+1), Then: build(s.Then, depth+1)}
		if n.Percent <= 0 || n.Percent >= 100 {
			n.Percent = 50
		}
		n.First.parent, n.Then.parent = n, n
		return n
	}
	l.root = build(s, 0)
	l.focused = l.Groups()[0]
	return l
}

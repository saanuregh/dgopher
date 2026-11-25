package app

import (
	"math"
	"strings"
	"time"

	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// How a row of the navigator starts, in place of the outline's own
// indentation: a catalog goes deep, and every pixel before a name is one
// the name does not get.
const (
	navInset  = 4  // before the first level
	navIndent = 10 // a level
	navArrow  = 14 // the arrow's room
	navEdge   = 6  // after a row's last thing
)

// isHead tells a heading of the database tree: the one over a project's
// connections, a connection, or a group of a schema's objects, as its
// tables. A heading does not indent what it holds: its small capitals, or
// a connection's badge, already set it apart from the rows under it, so
// the level's room goes to the names. The files' pane has headings of its
// own, over one level.
func isHead(n navNode) bool {
	return n.kind == nodeConnections || n.conn != "" && (n.kind == nodeConn || n.kind == nodeFolder)
}

// navHeads counts the headings a row of the database tree is under, by
// what the row is: the levels it does not indent by. top is 1 when the
// connections have a heading over them, as one project's do. A note's are
// not told by the note, which is the same under a schema and under a
// table (noteStart).
func navHeads(n navNode) int {
	const top = 1 // the heading of the connections, over each project's
	switch {
	case n.kind == nodeProject, n.kind == nodeConnections:
		return 0
	case n.conn == "" || n.kind == nodeConn:
		return top // a folder of connections, or a connection
	}
	heads := top + 1 // and its connection
	switch n.kind {
	case nodeObject, nodeColumn:
		heads++ // Tables, or Views
	case nodeItem:
		heads++ // its kind's group
		if strings.HasPrefix(n.name, folderPartitions+"\x00") {
			heads++ // and, of a partition, the group of its table
		}
	case nodeFolder:
		if n.folder == folderPartitions {
			heads++ // its table's group
		}
	}
	return heads
}

// rowStart is where a row starts: the headings it is under, and whether
// the row that holds it is one.
type rowStart struct {
	heads     int
	underHead bool
}

// noteAt names a note of the database tree by where it is: a note says
// "Loading…" at any depth.
type noteAt struct {
	note  navNode
	depth int
}

// eachRowStart calls fn with each row of the database tree, top to
// bottom, and where it starts, as the rows above it tell.
func (n *navState) eachRowStart(fn func(node navNode, depth int, at rowStart)) {
	under := []rowStart{{}} // by depth: where the rows held at that depth start
	for i := range n.tree.Rows() {
		node, depth := n.tree.Item(i), n.tree.Depth(i)
		at := under[depth]
		fn(node, depth, at)
		under = append(under[:depth+1], rowStart{heads: at.heads + b2i(isHead(node)), underHead: isHead(node)})
	}
}

// noteStart is where a note of the database tree starts.
func (n *navState) noteStart(note navNode, depth int) rowStart {
	if !n.notesRead {
		n.notesRead = true
		if n.notes == nil {
			n.notes = map[noteAt]rowStart{}
		}
		clear(n.notes)
		n.eachRowStart(func(node navNode, depth int, at rowStart) {
			if node.kind == nodeInfo {
				n.notes[noteAt{node, depth}] = at
			}
		})
	}
	return n.notes[noteAt{note, depth}]
}

// navHeadingText is a heading's name: small capitals, quieter than the
// rows under it, read by its own name. A section of a pane, as Queries or
// Connections, is a step brighter and heavier than a group inside a
// connection, as Tables: the group reads as the connection's, where the
// two alike read as one level.
func navHeadingText(c *ui.Context, label string, section bool) ui.Element {
	color, weight := widgets.PaletteOf(c).Muted, 600
	if section {
		color, weight = color.Mix(c.Theme().Text, 0.45), 700
	}
	return ui.Text(c, strings.ToUpper(label)).Label(label).SingleLine().Grow(1).
		FontSize(widgets.TextMeta).FontWeight(weight).LetterSpacing(0.6).TextColor(color)
}

// rowAdd is the button on a row that makes another of what the row holds,
// as a query file under Queries or an editor on a database, and reports
// its click. It has room of its own before the row's count and fades in
// for the row pointed at or chosen, so the count keeps its place: one
// taking the other's place flickered as the pointer crossed the rows.
func rowAdd(c *ui.Context, row ui.Element, chosen bool, label string) bool {
	add := widgets.IconButton(c, widgets.IconPlus, label).Padding(1)
	shown := row.Hovered() || chosen
	add.Opacity(add.Animate("shown", map[bool]float32{true: 1}[shown], 120*time.Millisecond))
	return add.Clicked()
}

// quietHeading is what a heading in small capitals paints over the list's
// tint of the chosen row while it is the one chosen, Transparent
// otherwise: the sidebar's own color. The tint is made for an item; on a
// heading in small capitals it shouts, so a chosen heading, of the files'
// pane or a group of the database tree, shows by its brighter name, and
// by its button where it has one.
func (a *App) quietHeading(c *ui.Context, n navNode) ui.Color {
	switch n.kind {
	case nodeConnections, nodeQueries, nodeDashboards, nodeModels, nodeFolder:
		if at, ok := a.nav.paneOf(n).chosen(); ok && at == n {
			return widgets.PaletteOf(c).Sidebar
		}
	}
	return ui.Transparent
}

// navStart builds the start of a row, before what navRow shows of n: the
// row's height, its depth, and its arrow. A row is as deep as the rows
// above it that indent: a heading does not (isHead). A row that never
// opens, among rows that never do, as a file, a column or a note, takes
// no room for an arrow and starts under its parent's name, which under a
// heading is after the arrow's room; a connection that does not open
// keeps the room, to line up with those that do.
func (a *App) navStart(c *ui.Context, n navNode) {
	r := a.nav.start
	var at rowStart
	switch {
	case a.nav.startFiles:
	case n.kind == nodeInfo:
		at = a.nav.noteStart(n, r.Depth)
	default:
		at = rowStart{heads: navHeads(n), underHead: n.kind == nodeItem}
	}
	depth := r.Depth - at.heads
	indent := ui.Box(c).Width(navInset + float32(depth)*navIndent).Height(navRowHeight).Shrink(0)
	quiet := a.quietHeading(c, n)
	if quiet != ui.Transparent {
		indent.Background(quiet)
	}
	if depth > 0 && !inFilesPane(n) && n.projectDir == "" {
		// A faint line down from each arrow above, through the rows it
		// indents: in a catalog levels deep, what is in what reads at a
		// glance. The files' pane is one level, and needs none.
		line := c.Theme().Text.Alpha(0.09)
		indent.Draw(func(p *ui.Painter, rect ui.Rect) {
			for d := range depth {
				x := rect.X + navInset + float32(d)*navIndent + navArrow/2
				p.Fill(ui.Rect{X: float32(math.Round(float64(x))), Y: rect.Y, W: 1, H: rect.H}, line, 0)
			}
		})
	}
	switch {
	case r.Branch:
		arrow := r.Arrow().Size(navArrow, navRowHeight).Shrink(0).Center()
		if quiet != ui.Transparent {
			arrow.Background(quiet)
		}
		turn := arrow.Animate("open", map[bool]float32{true: 90}[r.Open], 150*time.Millisecond)
		arrow.Children(func() {
			ui.Icon(c, widgets.IconChevronRight).FontSize(widgets.IconSmall).TextColor(widgets.PaletteOf(c).Muted).Rotate(turn)
		})
	case at.underHead:
		ui.Box(c).Width(navArrow).Shrink(0)
	case n.kind == nodeQueryFile, n.kind == nodeDashboard, n.kind == nodeModel, n.kind == nodeColumn, n.kind == nodeInfo:
		ui.Box(c).Width(4).Shrink(0)
	default:
		ui.Box(c).Width(navArrow).Shrink(0)
	}
}

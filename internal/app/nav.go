package app

import (
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

type nodeKind uint8

const (
	nodeProject     nodeKind = iota
	nodeConnections          // the connections of a project
	nodeQueries              // the query files of a project
	nodeQueryFile
	nodeDashboards // the dashboards of a project
	nodeDashboard  // named by its file's path
	nodeModels     // the data models of a project
	nodeModel      // named by its file's path
	nodeConnFolder // a folder of a project's connections, named by its path
	nodeConn
	nodeDatabase
	nodeSchema
	nodeFolder // the tables, views or items of a kind of a schema, or a table's partitions
	nodeObject
	nodeColumn
	nodeItem // a routine, trigger, sequence, type and the like
	nodeInfo // loading, an error, or nothing there
)

// The folders of a schema besides those of its items, by their kind;
// partitions are a table's.
const (
	folderTables     = ""
	folderViews      = "views"
	folderPartitions = string(db.ItemPartition)
)

// navNode is an item of the navigator's tree.
type navNode struct {
	kind nodeKind
	// projectDir is the folder of a project's own nodes: the project, its
	// sections, their files, and their notes. The nodes of a connection
	// are named by its ID alone, unique across projects.
	projectDir string
	conn       string
	database   string
	schema     string
	// name is an object's, a table's for its partitions' folder, or, for
	// an item, its kind and label (itemName).
	name   string
	folder string // a folder's: folderTables, folderViews, or an item kind
	// files is set for a project's node in the pane of the projects'
	// files, which holds its query files, dashboards and data models;
	// the one of the database tree holds its connections.
	files bool
	// under is a note's: the node it is under, which tells it from the
	// same note elsewhere, as Loading… under two databases (noteUnder).
	under string
}

// navFiles is the navigator's pane of the projects' files, under the
// database tree: a tree of its own, with its own chosen row.
type navFiles struct {
	tree ui.OutlineState[navNode]
	row  int
	// chose is the row chosen as the last frame ended (navState.chose).
	chose int
}

// navState is the navigator: the tree of the connections and what their
// databases hold (tree, row, chose), and above it the pane of the
// projects' files (files). A node is in one of the two by its kind.
type navState struct {
	tree ui.OutlineState[navNode]
	row  int
	// files is the pane of the projects' files, and inFiles whether it,
	// not the database tree, had the keys last; sideH is the height the
	// two shared as last drawn.
	files   navFiles
	inFiles bool
	sideH   float32
	// start is the row an outline is building, as the outline tells it
	// before the row builds: how deep, and whether it opens (navStart).
	start ui.OutlineRow
	// notes are where the notes of the database tree start, read from
	// its rows once in a frame that shows one (noteStart).
	notes     map[noteAt]rowStart
	notesRead bool
	// shown is the tab in front the navigator last followed; reveal is
	// its node, until the tree shows it, and opened the nodes opened to
	// show it.
	shown  widgets.Tab
	reveal *navNode
	opened map[navNode]bool
	// chose is the row chosen as the last frame ended, which a choice
	// moved from.
	chose int
	// filter is the text typed in the navigator's box, and needle what
	// the tree last took of it, in lower case; openBefore are the nodes
	// open before the filter, which clearing it opens again, and
	// pickMatch asks for the first match to be chosen once the tree
	// shows the filter's rows (navfilter.go).
	filter, needle string
	openBefore     []navNode
	pickMatch      bool
	// holds says, for the frame, whether a node holds a match (navHolds).
	holds map[navNode]bool
	// hasKeys is whether the tree had the focus as it was last drawn.
	hasKeys bool
	// memory is the memory the app uses, as the sidebar's foot shows it.
	memory memoryUse
	// databasesKept are the databases last kept of each connection, by
	// its ID, a line each (keepKnownDatabases).
	databasesKept map[string]string
	// startFiles is whether the row an outline is building is of the
	// files' pane.
	startFiles bool
	// listed are the headings opened as they first showed: a project's
	// Connections and its Queries.
	listed map[navNode]bool
	// schemas are, by project, the schemas chosen to show of each
	// connection's databases (navschemas.go).
	schemas map[*project.Project]map[string][]string
	// openConns are the IDs of the connections open in the tree as it
	// last drew, and openConnsNow as it draws: one opened since connects.
	openConns, openConnsNow map[string]bool
	// errors are the errors of the notes that say one, by note, made
	// again as each frame lists the tree: a note's text is a line of its
	// error, so one whose error changes is a new note.
	errors map[navNode]navError
	// raised are the whole errors of the notes raised already, kept
	// across frames until the note's error is gone or changes.
	raised map[navNode]string
}

// navSpacing is the unit of the room the navigator's rows leave around
// their text (ui.Theme's Spacing), which makes a row about 24 high.
const navSpacing = 3

func (n *navState) init() {
	n.row, n.chose = -1, -1
	n.files.row, n.files.chose = -1, -1
	n.openConnsNow = map[string]bool{}
	n.tree.List.Selected = &n.row
	n.files.tree.List.Selected = &n.files.row
	// The rows start themselves (navStart), which knows what a row is: the
	// outline only says how deep it is and whether it opens.
	n.tree.Prefix = func(r ui.OutlineRow) { n.start, n.startFiles = r, false }
	n.files.tree.Prefix = func(r ui.OutlineRow) { n.start, n.startFiles = r, true }
}

// navPane is one of the navigator's two trees with its chosen row, for
// what does the same to either.
type navPane struct {
	tree  *ui.OutlineState[navNode]
	row   *int
	chose *int
}

// panes are the database tree and the pane of the projects' files, in
// that order.
func (n *navState) panes() [2]navPane {
	return [2]navPane{{&n.tree, &n.row, &n.chose}, {&n.files.tree, &n.files.row, &n.files.chose}}
}

// chosen is the pane's chosen node, if it has one.
func (p navPane) chosen() (navNode, bool) {
	if *p.row < 0 || *p.row >= p.tree.Rows() {
		return navNode{}, false
	}
	return p.tree.Item(*p.row), true
}

// inFilesPane reports whether a node shows in the pane of the projects'
// files: a section of them, a file, or a project's node there.
func inFilesPane(n navNode) bool {
	switch n.kind {
	case nodeQueries, nodeQueryFile, nodeDashboards, nodeDashboard, nodeModels, nodeModel:
		return true
	}
	return n.files
}

// paneOf is the pane a node shows in.
func (n *navState) paneOf(node navNode) navPane {
	if inFilesPane(node) {
		return n.panes()[1]
	}
	return n.panes()[0]
}

// isOpen reports whether a node shows its children, in its pane.
func (n *navState) isOpen(node navNode) bool { return n.paneOf(node).tree.Open.Has(node) }

// skipNotes keeps the choice off a note, as "Loading…" or "No tables",
// which nothing is done with: it goes on to the next row the way it moved,
// else back where it was; the next frame, asked at once, shows it. A note
// of an error is chosen, as Enter raises it.
func (a *App) skipNotes(c *ui.Context, pane navPane) {
	rows, r, was := pane.tree.Rows(), *pane.row, *pane.chose
	skipped := func(i int) bool {
		n := pane.tree.Item(i)
		_, isError := a.nav.errors[n]
		return n.kind == nodeInfo && !isError
	}
	if r >= 0 && r < rows && skipped(r) {
		step := 1
		if r < was {
			step = -1
		}
		to := -1
		if was >= 0 && was < rows && !skipped(was) {
			to = was
		}
		for i := r; i >= 0 && i < rows; i += step {
			if !skipped(i) {
				to = i
				break
			}
		}
		*pane.row = to
		if to >= 0 {
			pane.tree.List.ScrollIntoView(to)
		}
		c.Invalidate()
	}
	*pane.chose = *pane.row
}

// expand opens a node, in its pane; collapse closes it.
func (n *navState) expand(node navNode)   { n.paneOf(node).tree.Open.Add(node) }
func (n *navState) collapse(node navNode) { n.paneOf(node).tree.Open.Remove(node) }

// navRoots are the projects; one alone shows its sections, a level less
// deep, its name above the tree (sidebarBar).
func (a *App) navRoots() []navNode { return a.paneRoots(false) }

// navFileRoots are the roots of the pane of the projects' files: one
// project's sections, or several projects, each holding its own.
func (a *App) navFileRoots() []navNode { return a.paneRoots(true) }

// paneRoots are a pane's roots: what the one project holds there, the
// heading of its connections or its sections of files, or the projects
// when there are several.
func (a *App) paneRoots(files bool) []navNode {
	if len(a.projects) == 1 {
		p := a.projects[0]
		if !files {
			// One heading over the connections closes them all, which
			// brings the files up to it. It costs the tables no level: a
			// heading does not indent what it holds (isHead).
			conns := navNode{kind: nodeConnections, projectDir: p.Dir}
			a.openHeading(conns)
			return []navNode{conns}
		}
		return a.projectChildren(navNode{kind: nodeProject, projectDir: p.Dir, files: files})
	}
	roots := make([]navNode, len(a.projects))
	for i, p := range a.projects {
		roots[i] = navNode{kind: nodeProject, projectDir: p.Dir, files: files}
	}
	return roots
}

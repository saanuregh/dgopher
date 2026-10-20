package query

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"dgopher/internal/keymap"
	"dgopher/internal/project"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/editor"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// The notebook view shows an editor's SQL as cells, as Jupyter does: each
// cell runs on its own, shows its own results under it, and every cell
// runs on the editor's one session, as a notebook's cells run on one
// kernel, so that what one sets, as a temporary table or a transaction,
// the next sees. The file stays SQL that runs as a script: a cell starts
// at a line "-- %%", a Markdown cell at "-- %% [markdown]", its lines
// commented, as jupytext's percent format writes them. Results are not
// kept in the file: it is shared, and they may hold what should not be.

// cellMarker starts a cell in the file.
const cellMarker = "-- %%"

// NewNotebookText is a new notebook's text: one empty cell, its header
// naming the notebook view.
const NewNotebookText = "-- view: " + project.NotebookView + "\n\n" + cellMarker + "\n"

// markerLine is a line starting a cell, its tag after the marker.
var markerLine = regexp.MustCompile(`^--\s?%%(.*)$`)

// cellSource is a cell as the file holds it.
type cellSource struct {
	Markdown bool
	Text     string
	// Tag is what follows the marker, as a title, the Markdown tag and
	// jupytext's metadata, kept as the file has it.
	Tag string
}

// HasCells reports whether a text is a notebook's: a line of it starts a
// cell.
func HasCells(text string) bool {
	for line := range strings.Lines(text) {
		if markerLine.MatchString(strings.TrimRight(line, "\r\n")) {
			return true
		}
	}
	return false
}

// parseCells reads a notebook's text: what comes before the first cell,
// as the line naming the connection, and the cells. A text without cells
// is one cell for each statement, with the comments before it, after its
// header.
func parseCells(text string, d sqltext.Dialect, opts sqltext.SplitOptions) (preamble string, cells []cellSource) {
	if !HasCells(text) {
		return statementCells(text, d, opts)
	}
	var pre, body strings.Builder
	var cur *cellSource
	flush := func() {
		if cur != nil {
			cur.Text = cellText(body.String(), cur.Markdown)
			cells = append(cells, *cur)
		}
		body.Reset()
	}
	for line := range strings.Lines(text) {
		if m := markerLine.FindStringSubmatch(strings.TrimRight(line, "\r\n")); m != nil {
			flush()
			cur = &cellSource{Markdown: markdownTag(m[1]), Tag: strings.TrimSpace(m[1])}
			continue
		}
		if cur == nil {
			pre.WriteString(line)
		} else {
			body.WriteString(line)
		}
	}
	flush()
	return pre.String(), cells
}

// markdownTag reports whether a marker's tag makes a Markdown cell, as
// "[markdown]" or "[md]".
// markdownTagged matches the tag of a Markdown cell, as markdownTag finds
// it.
var markdownTagged = regexp.MustCompile(`(?i)\[(markdown|md)\]`)

// retag is a marker's tag for a cell of the other kind: its Markdown tag
// taken out, or put in after the title and before the metadata
// (key=value), as jupytext writes it.
func retag(tag string, md bool) string {
	fields := strings.Fields(markdownTagged.ReplaceAllString(tag, ""))
	if md {
		at := slices.IndexFunc(fields, func(f string) bool { return strings.Contains(f, "=") })
		if at < 0 {
			at = len(fields)
		}
		fields = slices.Insert(fields, at, "[markdown]")
	}
	return strings.Join(fields, " ")
}

func markdownTag(tag string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	return strings.Contains(tag, "[markdown]") || strings.Contains(tag, "[md]")
}

// cellText is a cell's text as the file holds it, without the blank lines
// after it, and a Markdown cell's lines without their comment.
func cellText(body string, md bool) string {
	body = strings.TrimRight(body, " \t\r\n")
	if !md {
		return body
	}
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		switch {
		case strings.HasPrefix(l, "-- "):
			l = l[3:]
		case strings.HasPrefix(l, "--"):
			l = l[2:]
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

// statementCells cuts a text without cells into a cell for each
// statement, the comments before a statement in its cell; the line
// naming the connection, and the blank lines after it, stay before them.
func statementCells(text string, d sqltext.Dialect, opts sqltext.SplitOptions) (string, []cellSource) {
	at := project.HeaderEnd(text)
	preamble, rest := text[:at], text[at:]
	stmts := sqltext.SplitWith(rest, d, opts)
	if len(stmts) <= 1 {
		return preamble, []cellSource{{Text: strings.TrimRight(rest, " \t\r\n")}}
	}
	var cells []cellSource
	from := 0
	for i := range stmts {
		to := len(rest)
		if i+1 < len(stmts) {
			// After the line where this statement ends: the comments
			// before the next go with it.
			to = stmts[i].End
			if nl := strings.IndexByte(rest[to:], '\n'); nl >= 0 {
				to += nl + 1
			} else {
				to = len(rest)
			}
		}
		cells = append(cells, cellSource{Text: strings.Trim(rest[from:to], "\r\n")})
		from = to
	}
	return preamble, cells
}

// joinCells writes a notebook's text: what comes before the cells, then
// each cell after its marker, a blank line between two.
func joinCells(preamble string, cells []cellSource) string {
	var b strings.Builder
	b.WriteString(preamble)
	for i, cl := range cells {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(cellMarker)
		tag := cl.Tag
		if markdownTag(tag) != cl.Markdown {
			tag = retag(tag, cl.Markdown) // its kind changed
		}
		if tag != "" {
			b.WriteString(" " + tag)
		}
		text := cl.Text
		if cl.Markdown {
			lines := strings.Split(text, "\n")
			for j, l := range lines {
				if l == "" {
					lines[j] = "--"
				} else {
					lines[j] = "-- " + l
				}
			}
			text = strings.Join(lines, "\n")
		}
		b.WriteString("\n")
		if cl.Text != "" {
			b.WriteString(text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// cell is a cell of the notebook view: SQL with its editor, or Markdown.
type cell struct {
	id       int
	markdown bool
	// tag is what follows its marker (cellSource.Tag).
	tag string
	// ed is a SQL cell's editor; md a Markdown cell's text, editing
	// whether it shows as text to edit rather than as it reads.
	ed      *editor.Editor
	md      string
	editing bool
	// hadFocus is whether its editor had the focus in the last frame: a
	// cell the focus comes to takes the keys, not one keeping it as
	// another asks for it.
	hadFocus bool
	// results, resultIdx and messages are what the cell's last run
	// brought, kept here while another cell's are the tab's (bindCell).
	results   []*result
	resultIdx int
	messages  []message
	// count is its last run's number among the notebook's runs, 0
	// before it ran; took how long that run took.
	count int
	took  time.Duration
}

// text is the cell's text, as the file holds it.
func (cl *cell) text() string {
	if cl.markdown {
		return cl.md
	}
	return cl.ed.Text()
}

// notebook is the state of the notebook view.
type notebook struct {
	preamble string
	cells    []*cell
	// focused is the cell the keys act on; bound the cell whose results
	// are the tab's; running the cell of the run going on.
	focused, bound, running *cell
	nextID                  int
	// runs counts the runs, numbering each cell's.
	runs int
	// joined is the text the cells last made, and scriptText the
	// script's as it was then: the script changing otherwise, as the
	// file did on disk, the cells are read from it again.
	joined, scriptText string
	// made are the cells' sources the joined text was made of.
	made []cellSource
	// queue are the cells left to run of Run All, the next once the
	// one running ends.
	queue []*cell
	// startedAt is when the cell running began; fromQueue says the run
	// asked is Run All's next, which leaves the rest to run.
	startedAt time.Time
	fromQueue bool
}

// Notebook reports whether the editor shows as a notebook.
func (q *Tab) Notebook() bool { return q.nb != nil }

// ToggleNotebook shows the editor as a notebook of cells, or as one
// script; its text, session and results stay.
func (q *Tab) ToggleNotebook() {
	if q.nb != nil {
		q.closeNotebook()
		return
	}
	if q.Running {
		q.note("The view changes once the run ends.", "", true)
		return
	}
	q.setHeaderView(true)
	nb := &notebook{}
	q.nb = nb
	nb.readAgain(q)
	// The results of the script go with the first cell.
	nb.bound = nb.firstSQL()
	if nb.bound == nil {
		nb.bound = q.addCell(len(nb.cells), false, "")
	}
	q.focusCell(nb.bound)
}

// closeNotebook shows the editor as one script again: the results of the
// cells but the one whose results are the tab's go.
func (q *Tab) closeNotebook() {
	nb := q.nb
	q.syncNotebook()
	var closers []func()
	for _, cl := range nb.cells {
		if cl != nb.bound {
			closers = append(closers, releaseResults(cl.results)...)
		}
	}
	q.nb = nil
	q.Editor = q.script
	q.setHeaderView(false)
	q.Editor.WantFocus = true
	q.closeLater(closers)
}

// setHeaderView has the file's header name the view it shows in, as one
// change undo takes back, the caret staying where it was in the rest.
func (q *Tab) setHeaderView(notebook bool) {
	old := q.script.Text()
	text := project.SetHeaderView(old, notebook)
	if text == old {
		return
	}
	pre := 0
	for pre < len(old) && pre < len(text) && old[pre] == text[pre] {
		pre++
	}
	suf := 0
	for suf < len(old)-pre && suf < len(text)-pre && old[len(old)-1-suf] == text[len(text)-1-suf] {
		suf++
	}
	start, end := q.script.Selection()
	q.script.Replace(pre, len(old)-suf, text[pre:len(text)-suf])
	shift := func(at int) int {
		if at >= len(old)-suf {
			return at + len(text) - len(old)
		}
		return at
	}
	q.script.Select(shift(start), shift(end))
}

// releaseResults lets results go, returning what closes their cursors.
func releaseResults(results []*result) []func() {
	var closers []func()
	for _, r := range results {
		if r.view != nil {
			closers = append(closers, r.view.Release())
		}
	}
	return closers
}

// closeLater closes cursors away from the main thread.
func (q *Tab) closeLater(closers []func()) {
	if len(closers) == 0 {
		return
	}
	go func() {
		defer dataview.RecoverBackground(q.a.Post, q.a.ShowError, nil)
		for _, close := range closers {
			close()
		}
	}()
}

// readAgain reads the cells from the script's text, keeping the cells
// whose text and kind stayed, with their results.
func (nb *notebook) readAgain(q *Tab) {
	text := q.script.Text()
	pre, sources := parseCells(text, q.script.Dialect, SplitOptions(q.a.Settings()))
	nb.preamble = pre
	old := nb.cells
	nb.cells = nil
	var closers []func()
	for _, src := range sources {
		// The first of the old cells of its kind and text not taken yet:
		// results stay with the SQL they came from, wherever it moved.
		i := slices.IndexFunc(old, func(cl *cell) bool {
			return cl != nil && cl.markdown == src.Markdown && cl.text() == src.Text
		})
		if i < 0 {
			nb.cells = append(nb.cells, q.newCell(src))
			continue
		}
		cl := old[i]
		old[i] = nil
		cl.tag = src.Tag
		nb.cells = append(nb.cells, cl)
	}
	old = slices.DeleteFunc(old, func(cl *cell) bool { return cl == nil })
	for _, cl := range old {
		closers = append(closers, q.dropCell(cl)...) // none of them kept
	}
	if len(nb.cells) == 0 {
		nb.cells = append(nb.cells, q.newCell(cellSource{}))
	}
	if !slices.Contains(nb.cells, nb.focused) {
		nb.focused = nil
		if cl := nb.firstSQL(); cl != nil {
			q.focusCell(cl)
		}
	}
	nb.made = nb.sources()
	nb.joined, nb.scriptText = joinCells(nb.preamble, nb.made), text
	q.closeLater(closers)
}

// dropCell lets a cell go: its results, and the tab's when they are its.
func (q *Tab) dropCell(cl *cell) []func() {
	nb := q.nb
	if nb.bound == cl {
		closers := releaseResults(q.results)
		q.results, q.resultIdx, q.messages, q.stale = nil, 0, nil, nil
		nb.bound = nil
		return closers
	}
	if nb.running == cl {
		nb.running = nil
	}
	nb.queue = slices.DeleteFunc(nb.queue, func(o *cell) bool { return o == cl })
	return releaseResults(cl.results)
}

// newCell makes a cell of a source.
func (q *Tab) newCell(src cellSource) *cell {
	nb := q.nb
	nb.nextID++
	cl := &cell{id: nb.nextID, markdown: src.Markdown, tag: src.Tag}
	if src.Markdown {
		cl.md = src.Text
	} else {
		cl.ed = q.newEditor(src.Text)
	}
	return cl
}

// addCell puts a new cell at i, focused, and returns it.
func (q *Tab) addCell(i int, md bool, text string) *cell {
	nb := q.nb
	cl := q.newCell(cellSource{Markdown: md, Text: text})
	i = max(0, min(i, len(nb.cells)))
	nb.cells = slices.Insert(nb.cells, i, cl)
	q.focusCell(cl)
	if md {
		cl.editing = true
	}
	return cl
}

// firstSQL is the first SQL cell, nil for none.
func (nb *notebook) firstSQL() *cell {
	for _, cl := range nb.cells {
		if !cl.markdown {
			return cl
		}
	}
	return nil
}

// index is a cell's place, -1 for one gone.
func (nb *notebook) index(cl *cell) int { return slices.Index(nb.cells, cl) }

// sources are the cells as the file holds them.
func (nb *notebook) sources() []cellSource {
	out := make([]cellSource, len(nb.cells))
	for i, cl := range nb.cells {
		out[i] = cellSource{Markdown: cl.markdown, Text: cl.text(), Tag: cl.tag}
	}
	return out
}

// syncNotebook keeps the script's text, which is the file's, as the cells
// make it, and the cells as the script's when it changed otherwise.
func (q *Tab) syncNotebook() {
	nb := q.nb
	if q.script.Text() != nb.scriptText {
		nb.readAgain(q)
		return
	}
	// The cells' texts are their editors' own, the same strings while
	// unchanged, which compare at once: the file's text is made again only
	// once a cell changed, not in every frame.
	src := nb.sources()
	if slices.Equal(src, nb.made) {
		return
	}
	nb.made = src
	if joined := joinCells(nb.preamble, src); joined != nb.joined {
		q.script.ChangeTo(joined)
		nb.joined, nb.scriptText = joined, q.script.Text()
	}
}

// focusCell gives a cell the keys: a SQL cell's editor becomes the tab's.
func (q *Tab) focusCell(cl *cell) {
	nb := q.nb
	nb.focused = cl
	if !cl.markdown {
		if q.Editor != cl.ed {
			q.Editor = cl.ed
			q.syncVim()
		}
		cl.ed.WantFocus = true
	}
}

// bindCell makes a cell's results the tab's, which a run replaces and
// adds to, keeping the cell's that were. Never during a run, whose
// results come to the tab's as it goes.
func (q *Tab) bindCell(cl *cell) {
	nb := q.nb
	if nb == nil || nb.bound == cl || q.Running {
		return
	}
	if b := nb.bound; b != nil {
		b.results, b.resultIdx, b.messages = q.results, q.resultIdx, q.messages
	}
	q.results, q.resultIdx, q.messages, q.stale = cl.results, cl.resultIdx, cl.messages, nil
	cl.results, cl.messages = nil, nil
	nb.bound = cl
}

// cellResults are a cell's results and the one shown.
func (q *Tab) cellResults(cl *cell) ([]*result, int, []message) {
	if q.nb.bound == cl {
		return q.results, q.resultIdx, q.messages
	}
	return cl.results, cl.resultIdx, cl.messages
}

// otherResults are the results of the cells but the one whose results
// are the tab's: none in the script view.
func (q *Tab) otherResults() []*result {
	if q.nb == nil {
		return nil
	}
	var out []*result
	for _, cl := range q.nb.cells {
		if cl != q.nb.bound {
			out = append(out, cl.results...)
		}
	}
	return out
}

// allResults are the tab's results and every cell's.
func (q *Tab) allResults() []*result {
	if q.nb == nil {
		return q.results
	}
	return append(slices.Clone(q.results), q.otherResults()...)
}

// cellOf is the cell a result is of, nil for none.
func (q *Tab) cellOf(r *result) *cell {
	if q.nb == nil {
		return nil
	}
	for _, cl := range q.nb.cells {
		if results, _, _ := q.cellResults(cl); slices.Contains(results, r) {
			return cl
		}
	}
	return nil
}

// runCell runs a SQL cell, all of it, as the tab's run, in place of what
// Run All had left to run.
func (q *Tab) runCell(cl *cell) {
	if cl == nil || cl.markdown || q.Running {
		return
	}
	q.nb.queue = nil
	q.startCell(cl)
}

func (q *Tab) startCell(cl *cell) {
	q.focusCell(cl)
	q.Run(RunScript)
}

// cellSkipped goes on to Run All's next cell after one with nothing to
// run, as a cell of comments alone.
func (q *Tab) cellSkipped() {
	q.chartSkipped()
	if nb := q.nb; nb != nil && nb.running != nil {
		nb.running = nil
		q.runNextCell()
	}
}

// runCells runs cells one after the other, each once the one before
// ended, stopping at an error unless errors are skipped.
func (q *Tab) runCells(cells []*cell) {
	nb := q.nb
	nb.queue = nil
	for _, cl := range cells {
		if !cl.markdown && strings.TrimSpace(cl.ed.Text()) != "" {
			nb.queue = append(nb.queue, cl)
		}
	}
	q.runNextCell()
}

// runNextCell runs the next cell of the queue.
func (q *Tab) runNextCell() {
	nb := q.nb
	if len(nb.queue) == 0 || q.Running {
		return
	}
	next := nb.queue[0]
	nb.queue = nb.queue[1:]
	nb.fromQueue = true
	q.startCell(next)
	nb.fromQueue = false // read by Run, which may have returned before
}

// runEnded takes what a run's end brings: the chart the AI chat asked
// for, and the notebook's next cell.
func (q *Tab) runEnded() {
	q.chartRunEnded()
	q.cellRunEnded()
}

// cellRunEnded numbers the cell whose run ended, then runs the next of
// Run All: not after an error, unless errors are skipped.
func (q *Tab) cellRunEnded() {
	nb := q.nb
	if nb == nil {
		return
	}
	cl := nb.running
	nb.running = nil
	if cl == nil {
		return
	}
	nb.runs++
	cl.count, cl.took = nb.runs, time.Since(nb.startedAt)
	failed := slices.ContainsFunc(q.results, func(r *result) bool { return r.err != "" })
	if failed && !q.a.Settings().ContinueOnError {
		nb.queue = nil
		return
	}
	q.runNextCell()
}

// moveCell moves the focused cell by d places.
func (q *Tab) moveCell(d int) {
	nb := q.nb
	i := nb.index(nb.focused)
	j := i + d
	if i < 0 || j < 0 || j >= len(nb.cells) {
		return
	}
	nb.cells[i], nb.cells[j] = nb.cells[j], nb.cells[i]
	q.focusCell(nb.focused)
}

// deleteCell takes a cell away, once its results' changes are settled;
// not while it runs. The last cell stays, emptied.
func (q *Tab) deleteCell(cl *cell) {
	nb := q.nb
	if cl == nil || q.Running && nb.running == cl {
		return
	}
	results, _, _ := q.cellResults(cl)
	q.settlePending(results, func() {
		i := nb.index(cl)
		if i < 0 {
			return
		}
		q.closeLater(q.dropCell(cl))
		nb.cells = slices.Delete(nb.cells, i, i+1)
		if len(nb.cells) == 0 {
			nb.cells = append(nb.cells, q.newCell(cellSource{}))
		}
		q.focusCell(nb.cells[min(i, len(nb.cells)-1)])
	})
}

// changeCell makes a cell SQL or Markdown, keeping its text.
func (q *Tab) changeCell(cl *cell, md bool) {
	nb := q.nb
	if cl == nil || cl.markdown == md || q.Running && nb.running == cl {
		return
	}
	i := nb.index(cl)
	if i < 0 {
		return
	}
	text := cl.text()
	q.closeLater(q.dropCell(cl))
	next := q.newCell(cellSource{Markdown: md, Text: text})
	nb.cells[i] = next
	q.focusCell(next)
}

// clearOutputs lets every cell's results go, once their changes are
// settled.
func (q *Tab) clearOutputs() {
	if q.Running {
		return
	}
	q.settlePending(q.allResults(), func() {
		var closers []func()
		for _, cl := range q.nb.cells {
			closers = append(closers, releaseResults(cl.results)...)
			cl.results, cl.messages, cl.resultIdx, cl.count = nil, nil, 0, 0
		}
		closers = append(closers, releaseResults(q.results)...)
		q.results, q.messages, q.resultIdx, q.stale = nil, nil, 0, nil
		q.nb.runs = 0
		q.closeLater(closers)
	})
}

// notebookKeys handles the keys of the notebook's cells.
func (q *Tab) notebookKeys(c *ui.Context) {
	nb := q.nb
	at := nb.index(nb.focused)
	switch {
	case q.Editor.HasFocus() && q.pressed(c, keymap.RunCellAdvance):
		cl := nb.focused
		q.runCell(cl)
		if i := nb.index(cl); i == len(nb.cells)-1 {
			q.addCell(i+1, false, "")
		} else if i >= 0 {
			q.focusCell(nb.cells[i+1])
		}
	case q.pressed(c, keymap.RunCell):
		q.runCell(nb.focused)
	case q.pressed(c, keymap.RunAllCells):
		q.runCells(nb.cells)
	case q.pressed(c, keymap.RunCellsAbove) && at >= 0:
		q.runCells(nb.cells[:at])
	case q.pressed(c, keymap.RunCellsBelow) && at >= 0:
		q.runCells(nb.cells[at:])
	case q.pressed(c, keymap.AddCellAbove):
		q.addCell(max(at, 0), false, "")
	case q.pressed(c, keymap.AddCellBelow):
		q.addCell(at+1, false, "")
	case q.pressed(c, keymap.DeleteCell):
		q.deleteCell(nb.focused)
	case q.pressed(c, keymap.MoveCellUp):
		q.moveCell(-1)
	case q.pressed(c, keymap.MoveCellDown):
		q.moveCell(1)
	case q.pressed(c, keymap.CellToSQL):
		q.changeCell(nb.focused, false)
	case q.pressed(c, keymap.CellToMarkdown):
		q.changeCell(nb.focused, true)
	case q.pressed(c, keymap.ClearOutputs):
		q.clearOutputs()
	case q.pressed(c, keymap.PreviousCell) && at > 0:
		q.focusCell(nb.cells[at-1])
	case q.pressed(c, keymap.NextCell) && at >= 0 && at < len(nb.cells)-1:
		q.focusCell(nb.cells[at+1])
	}
}

// notebookBar is the notebook's toolbar.
func (q *Tab) notebookBar(c *ui.Context) {
	th := c.Theme()
	nb := q.nb
	if q.Running && widgets.Lasted(c, q.StartedAt) {
		if widgets.LeadToolButton(c.Key("run"), widgets.IconStop, "Stop", "Stop the running cell (Esc); the cells after it do not run", th.Danger).Clicked() {
			nb.queue = nil
			if q.cancel != nil {
				q.cancel()
			}
		}
	} else if widgets.LeadToolButton(c.Key("run"), widgets.IconPlay, "Run Cell", keymap.Hint("Run the cell", keymap.RunCellAdvance), th.Success).Clicked() {
		q.runCell(nb.focused)
	}
	if widgets.ToolButton(c.Key("run-all"), widgets.IconLayers, "Run All", keymap.Hint("Run every cell, from the first", keymap.RunAllCells)).Clicked() {
		q.runCells(nb.cells)
	}
	if widgets.ToolButton(c.Key("add-sql"), widgets.IconPlus, "SQL", keymap.Hint("Add a SQL cell below", keymap.AddCellBelow)).Clicked() {
		q.addCell(nb.index(nb.focused)+1, false, "")
	}
	if widgets.ToolButton(c.Key("add-md"), widgets.IconText, "Markdown", "Add a Markdown cell below").Clicked() {
		q.addCell(nb.index(nb.focused)+1, true, "")
	}
	if widgets.ToolButton(c.Key("clear"), widgets.IconTrash, "Clear", keymap.Hint("Clear every cell's results", keymap.ClearOutputs)).Clicked() {
		q.clearOutputs()
	}
}

// notebookView shows the cells, one under the other.
func (q *Tab) notebookView(c *ui.Context) {
	q.syncNotebook()
	nb := q.nb
	pal := widgets.PaletteOf(c)
	ui.Scroll(c.Key("cells")).Grow(1).Children(func() {
		ui.Column(c).Padding(12, 16, 24, 8).Gap(10).Children(func() {
			for _, cl := range slices.Clone(nb.cells) {
				q.cellView(c, cl)
			}
			ui.Row(c).Gap(8).Padding(0, 0, 0, 52).Children(func() {
				if ui.Button(c, "+ SQL").Tooltip("Add a SQL cell at the end").Clicked() {
					q.addCell(len(nb.cells), false, "")
				}
				if ui.Button(c, "+ Markdown").Tooltip("Add a Markdown cell at the end").Clicked() {
					q.addCell(len(nb.cells), true, "")
				}
				ui.Spacer(c)
				ui.Text(c, keymap.Hint("Run a cell and go on", keymap.RunCellAdvance)).FontSize(12).TextColor(pal.Muted)
			})
		})
	})
}

// cellView shows a cell: its run's number and button, its SQL or
// Markdown, its actions, and under it what its last run brought.
func (q *Tab) cellView(c *ui.Context, cl *cell) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	nb := q.nb
	focused := nb.focused == cl
	running := q.Running && nb.running == cl
	queued := slices.Contains(nb.queue, cl)
	row := ui.Row(c.Key(fmt.Sprintf("cell-%d", cl.id))).Gap(8).AlignItems(ui.Start)
	row.Children(func() {
		// The gutter: the run's number, as Jupyter's [n], and Run.
		ui.Column(c).Width(44).Gap(4).AlignItems(ui.End).Padding(6, 0, 0, 0).Children(func() {
			label := "[ ]"
			switch {
			case running:
				label = "[*]"
			case queued:
				label = "[…]"
			case cl.count > 0:
				label = fmt.Sprintf("[%d]", cl.count)
			}
			if !cl.markdown {
				ui.Text(c, label).Font(widgets.MonoFont).FontSize(11.5).TextColor(pal.Muted)
				if running {
					if widgets.IconButton(c.Key("stop"), widgets.IconStop, "Stop the cell").Clicked() && q.cancel != nil {
						nb.queue = nil
						q.cancel()
					}
				} else if widgets.IconButton(c.Key("run"), widgets.IconPlay, "Run the cell").Disabled(q.Running).Clicked() {
					q.runCell(cl)
				}
			}
		})
		body := ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Radius(8).Border(1, th.Border).Clip()
		if focused {
			body.Border(1, th.Accent)
		}
		if running {
			// A line along its top, light sweeping on it: Run All is
			// followed from cell to cell.
			if !c.Preferences().ReduceMotion {
				c.AnimationFrame()
			}
			body.DrawOver(func(p *ui.Painter, r ui.Rect) {
				widgets.GlowLine(c, p, ui.Rect{X: r.X + 1, Y: r.Y + 1, W: r.W - 2, H: 2}, widgets.GlowTime(c)) // under the border
			})
		}
		body.Children(func() {
			if cl.markdown {
				q.markdownCell(c, cl)
			} else {
				q.sqlCell(c, cl)
			}
			if focused || row.Hovered() {
				q.cellActions(c, cl)
			}
		})
	})
}

// cellActions are a cell's actions, at its top right while it is focused
// or pointed at.
func (q *Tab) cellActions(c *ui.Context, cl *cell) {
	th := c.Theme()
	ui.Row(c.Key("actions")).Absolute().Top(3).Right(6).Gap(1).Padding(1, 2).Radius(6).Background(th.Background).Border(1, th.Border).Children(func() {
		if widgets.IconButton(c.Key("up"), widgets.IconArrowUp, "Move Cell Up").Tooltip(keymap.Hint("Move up", keymap.MoveCellUp)).Clicked() {
			q.focusCell(cl)
			q.moveCell(-1)
		}
		if widgets.IconButton(c.Key("down"), widgets.IconArrowDown, "Move Cell Down").Tooltip(keymap.Hint("Move down", keymap.MoveCellDown)).Clicked() {
			q.focusCell(cl)
			q.moveCell(1)
		}
		kind, tip := widgets.IconText, keymap.Hint("Change to Markdown", keymap.CellToMarkdown)
		if cl.markdown {
			kind, tip = widgets.IconCode, keymap.Hint("Change to SQL", keymap.CellToSQL)
		}
		if widgets.IconButton(c.Key("kind"), kind, "Change Cell Kind").Tooltip(tip).Clicked() {
			q.changeCell(cl, !cl.markdown)
		}
		if widgets.IconButton(c.Key("delete"), widgets.IconTrash, "Delete Cell").Tooltip(keymap.Hint("Delete the cell", keymap.DeleteCell)).Clicked() {
			q.deleteCell(cl)
		}
	})
}

// sqlCell shows a SQL cell's editor, as tall as its lines, and its
// results.
func (q *Tab) sqlCell(c *ui.Context, cl *cell) {
	a := q.a
	nb := q.nb
	ed := cl.ed
	font := a.Settings().EditorFont
	lines := strings.Count(ed.Text(), "\n") + 1
	height := float32(lines)*font*editor.LineHeight + 14
	focused := nb.focused == cl
	ed.SplitOptions = SplitOptions(a.Settings())
	q.tintLanded(c, ed)
	ed.Run = nil // the cell's own button runs it: no ▶ by each statement
	ed.HasShown = false
	ui.Column(c).Height(height).Children(func() {
		if focused && q.Editor == ed {
			_, _, ok := q.currentStatement()
			ed.HasCurrent = ok && ed.HasFocus()
			q.checkProblems()
			q.syncVim()
		} else {
			ed.HasCurrent = false
		}
		waits := ed.WantFocus && !a.KeysTo(q)
		ed.WantFocus = ed.WantFocus && !waits
		ed.View(c, font)
		ed.WantFocus = ed.WantFocus || waits
	})
	if ed.HasFocus() && !cl.hadFocus && nb.focused != cl {
		q.focusCell(cl)
	}
	cl.hadFocus = ed.HasFocus()
	if focused && q.Editor == ed {
		q.trackSnippet()
		q.completionKeys(c)
		q.problemBar(c)
		q.vimBar(c)
	}
	q.cellResultsView(c, cl)
}

// cellResultsView shows what a SQL cell's last run brought: its results,
// a result tab for each when there are several, and what ran without
// rows.
func (q *Tab) cellResultsView(c *ui.Context, cl *cell) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	nb := q.nb
	results, idx, messages := q.cellResults(cl)
	running := q.Running && nb.running == cl
	if len(results) == 0 && len(messages) == 0 && !running {
		return
	}
	ui.Column(c).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
		shown := 0
		for _, r := range results {
			if r.rowsMode || r.err != "" {
				shown++
			}
		}
		if shown > 1 {
			ui.Row(c).Padding(4, 8).Gap(2).ClipX().Children(func() {
				names := resultNames(results)
				for i, r := range results {
					if !r.rowsMode && r.err == "" {
						continue
					}
					label := names[i]
					if r.err != "" {
						label += " ⚠"
					}
					if widgets.IconPill(c, nil, label, i == idx).Tooltip(resultTip(r)).Clicked() {
						if nb.bound == cl {
							q.resultIdx = i
						} else {
							cl.resultIdx = i
						}
					}
				}
			})
		}
		// What ran without rows, a line each.
		for _, r := range results {
			if r.rowsMode || r.err != "" {
				continue
			}
			ui.Text(c, verbLabel(r.verb)+" · "+affectedLabel(r.affected)+" · "+widgets.FormatDuration(r.elapsed)).
				FontSize(12).TextColor(pal.Muted).Padding(4, 10)
		}
		r := resultAt(results, idx)
		switch {
		case r == nil && running && q.runReads:
			ui.Column(c).Height(120).Children(func() { widgets.Skeleton(c, q.StartedAt) })
		case r == nil:
			if n := len(messages); n > 0 && (messages[n-1].err || len(results) == 0) {
				m := messages[n-1]
				col := pal.Muted
				if m.err {
					col = th.Danger
				}
				ui.Text(c, m.text).FontSize(12).TextColor(col).Padding(4, 10)
			}
		case r.view == nil:
			q.errorPanel(c, r)
		case r.plan != nil:
			ui.Column(c).Height(360).Children(func() { q.planView(c, r) })
		default:
			ui.Column(c).Height(resultHeight(r)).Children(func() { r.view.View(c) })
		}
		if cl.count > 0 && !running {
			ui.Text(c, "took "+widgets.FormatDuration(cl.took)).FontSize(11).TextColor(pal.Muted).Padding(0, 10, 4, 10)
		}
	})
}

// resultHeight is how tall a cell's rows show: as many as there are, up
// to a screenful, under the viewer's bars.
func resultHeight(r *result) float32 {
	rows := len(r.view.Rows())
	return float32(min(max(rows, 3), 12))*26 + 96
}

// markdownCell shows a Markdown cell as it reads; focused and clicked, or
// new, as text to edit.
func (q *Tab) markdownCell(c *ui.Context, cl *cell) {
	pal := widgets.PaletteOf(c)
	nb := q.nb
	if cl.editing {
		ui.Column(c).Padding(6).Gap(6).Children(func() {
			area := ui.TextArea(c.Key("md"), &cl.md).Label("Markdown").Font(widgets.MonoFont).MinHeight(64).FillWidth()
			if nb.focused == cl && !area.Focused() && cl.md == "" {
				area.Focus()
			}
			if area.Focused() && !cl.hadFocus && nb.focused != cl {
				nb.focused = cl
			}
			cl.hadFocus = area.Focused()
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, "Markdown").FontSize(11.5).TextColor(pal.Muted)
				ui.Spacer(c)
				if ui.Button(c, "Done").Tooltip("Show it as it reads").Clicked() {
					cl.editing = false
				}
			})
		})
		return
	}
	view := ui.ButtonBase(c.Key("md-view")).Padding(8, 12).Label("Markdown cell: click to edit").AlignItems(ui.Stretch)
	view.Children(func() {
		ui.Column(c).Grow(1).AlignItems(ui.Start).Children(func() {
			if strings.TrimSpace(cl.md) == "" {
				ui.Text(c, "Empty Markdown cell: click to write").TextColor(pal.Muted).FontSize(12.5)
				return
			}
			markdown.View(c, cl.md, nil).FillWidth()
		})
	})
	if view.Clicked() {
		if nb.focused == cl {
			cl.editing = true
		}
		nb.focused = cl
	}
}

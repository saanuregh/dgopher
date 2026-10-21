package query

import (
	"slices"
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/sqltext"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// A notebook's text reads as its cells and writes back as it was; a
// script without cells reads as a cell for each statement, after its
// header.
func TestParseCells(t *testing.T) {
	text := "-- connection: lite\n\n-- %%\nSELECT 1;\n\n-- %% [markdown]\n-- # Orders\n--\n-- by day\n\n-- %%\nSELECT 2;\nSELECT 3;\n"
	pre, cells := parseCells(text, sqltext.SQLite, sqltext.SplitOptions{})
	want := []cellSource{{Text: "SELECT 1;"}, {Markdown: true, Text: "# Orders\n\nby day", Tag: "[markdown]"}, {Text: "SELECT 2;\nSELECT 3;"}}
	if pre != "-- connection: lite\n\n" || !slices.Equal(cells, want) {
		t.Fatalf("preamble %q, cells %+v", pre, cells)
	}
	if back := joinCells(pre, cells); back != text {
		t.Fatalf("written back:\n%s", back)
	}

	pre, cells = parseCells("-- connection: lite\n\n-- the first\nSELECT 1;\n\nSELECT 2;\n", sqltext.SQLite, sqltext.SplitOptions{})
	want = []cellSource{{Text: "-- the first\nSELECT 1;"}, {Text: "SELECT 2;"}}
	if pre != "-- connection: lite\n\n" || !slices.Equal(cells, want) {
		t.Fatalf("preamble %q, cells %+v", pre, cells)
	}
	if !HasCells("x\n--%% [md]\n") || HasCells("SELECT '-- %%';") {
		t.Error("cells found wrong")
	}
}

// Cells run on the editor's one session, each keeping its own results;
// Run All runs them in order; the file holds the cells as SQL.
func TestNotebookRuns(t *testing.T) {
	text := "-- %%\nCREATE TEMP TABLE n AS SELECT 41 AS answer;\n\n-- %% [markdown]\n-- # Notes\n\n-- %%\nSELECT answer + 1 AS next FROM n;\n"
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1100, 900, text)
	q.ToggleNotebook()
	tt.Frame()
	if !q.Notebook() || len(q.nb.cells) != 3 || !q.nb.cells[1].markdown {
		t.Fatalf("cells %d", len(q.nb.cells))
	}
	testutil.WaitFor(t, tt, "the Markdown", func() bool { return tt.HasText("Notes") })
	if q.nb.cells[0].ed.Run != nil {
		t.Fatal("a cell shows a run button by each statement besides its own")
	}
	q.runCells(q.nb.cells)
	testutil.WaitFor(t, tt, "both cells' runs", func() bool {
		return !q.Running && len(q.nb.queue) == 0 && q.nb.cells[2].count == 2
	})
	testutil.WaitFor(t, tt, "the second's rows", func() bool { return tt.HasText("42") })
	first, _, _ := q.cellResults(q.nb.cells[0])
	last, _, _ := q.cellResults(q.nb.cells[2])
	if len(first) != 1 || first[0].rowsMode || len(last) != 1 || !last[0].rowsMode {
		t.Fatalf("first %d results, last %d", len(first), len(last))
	}
	if q.nb.cells[0].count != 1 || !tt.HasText("[1]") || !tt.HasText("[2]") {
		t.Fatalf("counts %d %d", q.nb.cells[0].count, q.nb.cells[2].count)
	}
	testutil.Snapshot(t, tt, "notebook")

	// A cell typed into changes the file's text, the cells kept as SQL.
	q.nb.cells[2].ed.SetText("SELECT answer * 2 AS twice FROM n;")
	tt.Frame()
	if got := q.FileText(); !strings.Contains(got, "-- %%\nSELECT answer * 2 AS twice FROM n;\n") || !strings.Contains(got, "-- %% [markdown]\n-- # Notes") {
		t.Fatalf("file text:\n%s", got)
	}
	// Run in place: the first cell's results stay as they were.
	q.focusCell(q.nb.cells[2])
	testutil.WaitFor(t, tt, "the cell's focus", func() bool { return q.nb.cells[2].ed.HasFocus() })
	tt.Key(ui.Shift, ui.KeyEnter)
	testutil.WaitFor(t, tt, "the rerun", func() bool { return !q.Running && tt.HasText("82") })
	if len(q.nb.cells) != 4 || q.nb.focused != q.nb.cells[3] {
		t.Fatalf("no cell added after the last: %d cells", len(q.nb.cells))
	}
	if first, _, _ := q.cellResults(q.nb.cells[0]); len(first) != 1 {
		t.Fatal("the first cell lost its results")
	}

	// Back to the script: the text is the cells'.
	q.ToggleNotebook()
	tt.Frame()
	if q.Notebook() || q.Editor != q.script || !strings.Contains(q.Editor.Text(), "-- %%\nSELECT answer * 2") {
		t.Fatalf("script %q", q.Editor.Text())
	}
}

// The file changing on disk reads the cells again.
func TestNotebookReadsAgain(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT 1;\n\nSELECT 2;\n")
	q.ToggleNotebook()
	tt.Frame()
	if len(q.nb.cells) != 2 || q.FileText() != "-- view: notebook\n\nSELECT 1;\n\nSELECT 2;\n" {
		t.Fatalf("cells %d; the header does not name the view: %q", len(q.nb.cells), q.FileText())
	}
	q.setFileText("-- %%\nSELECT 3;\n")
	tt.Frame()
	if len(q.nb.cells) != 1 || q.nb.cells[0].ed.Text() != "SELECT 3;" {
		t.Fatalf("cells %d", len(q.nb.cells))
	}
}

// Run All goes past a cell with nothing to run; a run the user starts
// ends what Run All had left.
func TestNotebookRunAllSkipsComments(t *testing.T) {
	text := "-- %%\nSELECT 1 AS one;\n\n-- %%\n-- a note alone\n\n-- %%\nSELECT 333 AS three;\n"
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1100, 900, text)
	q.ToggleNotebook()
	tt.Frame()
	q.runCells(q.nb.cells)
	testutil.WaitFor(t, tt, "the last cell's run", func() bool { return !q.Running && q.nb.cells[2].count > 0 })
	if len(q.nb.queue) != 0 || q.nb.running != nil {
		t.Fatalf("queue %d left", len(q.nb.queue))
	}
}

// What follows a cell's marker, as a title or jupytext's metadata, stays
// as the file had it, Markdown tag rebuilt from the cell's kind.
func TestCellTagsKept(t *testing.T) {
	text := "-- %% Load users\nSELECT 1;\n\n-- %% [markdown] tags=[\"x\"]\n-- hi\n"
	pre, cells := parseCells(text, sqltext.SQLite, sqltext.SplitOptions{})
	if cells[0].Tag != "Load users" || !cells[1].Markdown || cells[1].Tag != `[markdown] tags=["x"]` {
		t.Fatalf("cells %+v", cells)
	}
	if back := joinCells(pre, cells); back != text {
		t.Fatalf("written back:\n%s", back)
	}
	// A cell changing kind: the tag follows, in jupytext's order.
	cells[0].Markdown, cells[1].Markdown = true, false
	if got := retag(cells[0].Tag, true) + " | " + retag(cells[1].Tag, false); got != `Load users [markdown] | tags=["x"]` {
		t.Fatalf("retagged %q", got)
	}
}

// The file read again keeps each cell's results with the SQL they came
// from: a cell put before the others takes none of theirs.
func TestReadAgainKeepsResultsWithTheirSQL(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1100, 800, "-- %%\nSELECT 42 AS answer;\n")
	q.ToggleNotebook()
	tt.Frame()
	ran := q.nb.cells[0]
	q.runCells(q.nb.cells)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && ran.count > 0 })
	q.setFileText("-- view: notebook\n\n-- %%\nSELECT 7 AS seven;\n\n-- %%\nSELECT 42 AS answer;\n")
	tt.Frame()
	if len(q.nb.cells) != 2 || q.nb.cells[1] != ran || q.nb.cells[0].count != 0 {
		t.Fatalf("cells %d; the run's cell moved to %d", len(q.nb.cells), q.nb.index(ran))
	}
}

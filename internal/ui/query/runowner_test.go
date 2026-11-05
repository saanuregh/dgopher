package query

import (
	"slices"
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/sqltext"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/editor"

	"github.com/egoist/mygo/ui"
)

// The file read again while a cell runs drops that cell: the run's
// results, coming after, go nowhere, and pinned results before them do
// not shift where the run's end looks for its own.
func TestNotebookReadAgainDuringRun(t *testing.T) {
	text := "-- %%\nSELECT 1 AS one;\n\n-- %%\nSELECT 2 AS two;\n"
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1100, 800, text)
	q.ToggleNotebook()
	tt.Frame()
	first, second := q.nb.cells[0], q.nb.cells[1]
	// Two runs of the first cell, both pinned.
	for range 2 {
		q.runCell(first)
		testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
		for _, r := range q.results {
			r.pinned = true
		}
	}
	if len(q.results) != 2 {
		t.Fatalf("pinned %d results", len(q.results))
	}
	q.runCell(first)
	if !q.Running {
		t.Fatal("the run did not start at once")
	}
	q.setFileText("-- view: notebook\n\n-- %%\nSELECT 11 AS eleven;\n\n-- %%\nSELECT 2 AS two;\n")
	if q.nb.index(first) >= 0 || q.nb.cells[1] != second {
		t.Fatalf("the first cell was kept: %d cells", len(q.nb.cells))
	}
	testutil.WaitFor(t, tt, "the run's end", func() bool { return !q.Running })
	if q.nb.running != nil {
		t.Fatal("the dropped cell still runs")
	}
	for _, cl := range q.nb.cells {
		if results, _, _ := q.cellResults(cl); slices.ContainsFunc(results, func(r *result) bool { return r.sql == "SELECT 1 AS one" }) {
			t.Fatalf("cell %d took the dropped cell's run", q.nb.index(cl))
		}
	}
	// The next run of a cell shows its own results alone.
	q.runCell(q.nb.cells[0])
	testutil.WaitFor(t, tt, "the new cell's run", func() bool { return !q.Running && q.nb.cells[0].count > 0 })
	if len(q.results) != 1 || q.results[0].sql != "SELECT 11 AS eleven" {
		t.Fatalf("results %d: %q", len(q.results), q.results[0].sql)
	}
}

// Run All stops at a cell that fails in this run, not at an error pinned
// from an earlier one.
func TestRunAllPastPinnedError(t *testing.T) {
	text := "-- %%\nSELECT * FROM missing;\n\n-- %%\nSELECT 2 AS two;\n"
	a, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1100, 800, text)
	a.Settings().ContinueOnError = false
	q.ToggleNotebook()
	tt.Frame()
	first, second := q.nb.cells[0], q.nb.cells[1]
	q.runCell(first)
	testutil.WaitFor(t, tt, "the failing run", func() bool { return !q.Running && first.count > 0 })
	if len(q.results) != 1 || q.results[0].err == "" {
		t.Fatalf("results %d", len(q.results))
	}
	q.results[0].pinned = true
	first.ed.SetText("SELECT 1 AS one;")
	tt.Frame()
	q.runCells(q.nb.cells)
	testutil.WaitFor(t, tt, "Run All's end", func() bool { return !q.Running && q.nb.running == nil && len(q.nb.queue) == 0 })
	if second.count == 0 {
		t.Fatal("Run All stopped at the pinned error of an earlier run")
	}
	// A cell failing now still stops it.
	first.ed.SetText("SELECT * FROM missing;")
	tt.Frame()
	ran := second.count
	q.runCells(q.nb.cells)
	testutil.WaitFor(t, tt, "Run All's end", func() bool { return !q.Running && q.nb.running == nil && len(q.nb.queue) == 0 })
	if second.count != ran {
		t.Fatal("Run All went past a cell failing now")
	}
}

// Esc the editor takes, as Vim's to leave insert mode, does not cancel the
// run; Esc the editor leaves does.
func TestCancelQueryWaitsForTheEditor(t *testing.T) {
	a := newFakeQueryHost(t)
	a.Settings().Vim = true
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := New(a, cn, "", "q", "select 1")
	q.Editor.Select(0, 0)
	a.AddTab(q)
	testutil.WaitFor(t, tt, "the editor's focus", func() bool { return q.Editor.HasFocus() && q.Editor.Vim != nil })
	step := func() {
		tt.Frame()
		tt.Frame()
	}
	tt.Type("i")
	step()
	if q.Editor.Vim.Mode != editor.VimInsert {
		t.Fatalf("mode %v", q.Editor.Vim.Mode)
	}
	cancelled := 0
	q.Running, q.cancel = true, func() { cancelled++ }
	t.Cleanup(func() { q.Running, q.cancel = false, nil })
	step()
	tt.Key(0, ui.KeyEscape)
	step()
	if cancelled != 0 || q.Editor.Vim.Mode != editor.VimNormal {
		t.Fatalf("Esc in insert mode: cancelled %d, mode %v", cancelled, q.Editor.Vim.Mode)
	}
	tt.Key(0, ui.KeyEscape)
	step()
	if cancelled != 1 {
		t.Fatalf("Esc in normal mode cancelled %d times", cancelled)
	}
}

// Stop shows that it stops until the run ends.
func TestStopShowsStopping(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1200, 760, "select 1")
	cancelled := 0
	q.Running, q.cancel = true, func() { cancelled++ }
	q.StartedAt = q.a.Now().Add(-10 * 1e9)
	tt.Frame()
	if err := tt.Click("Stop"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	if cancelled != 1 || !tt.HasText("Stopping…") {
		t.Fatalf("cancelled %d, shown %q", cancelled, tt.Texts())
	}
	q.Running, q.cancel = false, nil
	tt.Frame()
	tt.Frame()
	if !tt.HasText("Run") || tt.HasText("Stopping…") {
		t.Fatalf("after the run: %q", tt.Texts())
	}
}

// Nothing to run says so where the user looks, not only in Output.
func TestNothingToRunToasts(t *testing.T) {
	a, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "-- a note\n")
	q.Run(RunStatement)
	tt.Frame()
	if len(a.Toasts) == 0 || !strings.Contains(a.Toasts[len(a.Toasts)-1], "Nothing to run") {
		t.Fatalf("toasts %q", a.Toasts)
	}
}

// A USE is found by its word, not by its letters inside another.
func TestMayMoveDatabase(t *testing.T) {
	for sql, want := range map[string]bool{
		"SELECT cause FROM t":   false,
		"SELECT 'use' AS x":     false,
		"SELECT 1 -- use it":    false,
		"USE shop":              true,
		"use shop":              true,
		"SELECT 1;\nUSE shop":   true,
		"SELECT * FROM users":   false,
		"SELECT \"use\" FROM t": false,
	} {
		s := safety.Statement{SQL: sql}
		if got := mayMoveDatabase([]safety.Statement{s}, sqltext.TSQL); got != want {
			t.Errorf("%q: %v", sql, got)
		}
	}
}

// The results of a run whose cell was dropped go to no cell: neither the
// tab's nor, back from the script view, the first cell's.
func TestDroppedCellRunBindsNowhere(t *testing.T) {
	text := "-- %%\nSELECT 1 AS one;\n\n-- %%\nSELECT 2 AS two;\n"
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1100, 800, text)
	q.ToggleNotebook()
	tt.Frame()
	q.runCell(q.nb.cells[0])
	if !q.Running {
		t.Fatal("the run did not start at once")
	}
	q.setFileText("-- view: notebook\n\n-- %%\nSELECT 11 AS eleven;\n\n-- %%\nSELECT 2 AS two;\n")
	testutil.WaitFor(t, tt, "the run's end", func() bool { return !q.Running })
	if len(q.results) != 0 {
		t.Fatalf("the tab kept %d results of the dropped cell", len(q.results))
	}
	q.ToggleNotebook()
	tt.Frame()
	q.ToggleNotebook()
	tt.Frame()
	if results, _, _ := q.cellResults(q.nb.cells[0]); len(results) != 0 {
		t.Fatalf("the first cell took %q", results[0].sql)
	}
}

// Esc with the find bar open closes it before it cancels the run.
func TestCancelQueryAfterFindBar(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "select 1")
	testutil.WaitFor(t, tt, "the editor's focus", func() bool { return q.Editor.HasFocus() })
	q.Editor.State.Find("select")
	tt.Frame()
	if n, _ := q.Editor.State.FindMatches(); n != 1 {
		t.Fatalf("find bar: %d matches", n)
	}
	cancelled := 0
	q.Running, q.cancel = true, func() { cancelled++ }
	t.Cleanup(func() { q.Running, q.cancel = false, nil })
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()
	if n, _ := q.Editor.State.FindMatches(); cancelled != 0 || n != 0 {
		t.Fatalf("Esc with the find bar: cancelled %d, find bar open %v", cancelled, n != 0)
	}
	testutil.WaitFor(t, tt, "the editor's focus", func() bool { return q.Editor.HasFocus() })
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()
	if cancelled != 1 || q.Editor.SelectedText() != "select" {
		t.Fatalf("Esc after the find bar cancelled %d times, selection %q", cancelled, q.Editor.SelectedText())
	}
}

// Run All goes on to the next cells when the running one is read again
// from the file and dropped.
func TestRunAllPastDroppedCell(t *testing.T) {
	text := "-- %%\nSELECT 1 AS one;\n\n-- %%\nSELECT 2 AS two;\n\n-- %%\nSELECT 3 AS three;\n"
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1100, 800, text)
	q.ToggleNotebook()
	tt.Frame()
	q.runCells(q.nb.cells)
	if !q.Running {
		t.Fatal("Run All did not start at once")
	}
	q.setFileText("-- view: notebook\n\n-- %%\nSELECT 11 AS eleven;\n\n-- %%\nSELECT 2 AS two;\n\n-- %%\nSELECT 3 AS three;\n")
	testutil.WaitFor(t, tt, "Run All's end", func() bool { return !q.Running && q.nb.running == nil && len(q.nb.queue) == 0 })
	if q.nb.cells[1].count == 0 || q.nb.cells[2].count == 0 {
		t.Fatalf("Run All stopped at the dropped cell: counts %d %d", q.nb.cells[1].count, q.nb.cells[2].count)
	}
}

package query

import (
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// named is where the control of that name shows.
func named(t *testing.T, tt *ui.Tester, name string) ui.Rect {
	t.Helper()
	info, ok := tt.Access(name)
	if !ok {
		t.Fatalf("nothing is named %q", name)
	}
	return info.Bounds
}

// The editor's toolbar reads as a sentence: where the statement runs,
// then what runs it, then what changes the view. Production is named
// beside Run, not only at the far end of the window.
func TestEditorToolbarStartsWithTheConnection(t *testing.T) {
	_, tt, _, _ := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:", Env: db.Production}, 1000, 700, "SELECT 1")
	tt.Frame()
	order := []string{"Runs on lite, Production", "Disconnect", "Run", "Run Script", "Explain", "More ways to explain", "Format", "Commit", "Roll Back", "Notebook", "AI Chat"}
	at := float32(-1)
	for _, name := range order {
		r := named(t, tt, name)
		if r.X <= at {
			t.Errorf("%s at %v is not after what comes before it, at %v", name, r.X, at)
		}
		at = r.X
	}
	if !tt.HasText("Production") {
		t.Error("the environment is not written beside Run")
	}
	for _, gone := range []string{"Save As", "Analyze"} {
		if _, ok := tt.Access(gone); ok {
			t.Errorf("%s still takes room in the toolbar", gone)
		}
	}
	testutil.Snapshot(t, tt, "editor-toolbar-production")
}

// Commit and Roll Back keep their place in the toolbar under manual
// commit, unavailable until a transaction is open: nothing slides in over
// the editor when one opens.
func TestCommitAndRollBackKeepTheirSlots(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:", Commit: db.CommitManual}, 1000, 700,
		"create table t (id integer);\ninsert into t values (1);")
	tt.Frame()
	editor := named(t, tt, "SQL editor")
	commit, _ := tt.Access("Commit")
	if !commit.Disabled || !commit.Focusable {
		t.Fatalf("with no transaction, Commit is disabled %v, focusable %v", commit.Disabled, commit.Focusable)
	}
	q.Run(RunScript)
	testutil.WaitFor(t, tt, "the transaction", func() bool { return !q.Running && q.Tx == db.TxOpen })
	tt.Frame()
	tt.Frame()
	if after := named(t, tt, "SQL editor"); after != editor {
		t.Errorf("the editor moved from %v to %v as the transaction opened", editor, after)
	}
	open, _ := tt.Access("Commit")
	if open.Disabled || open.Bounds != commit.Bounds {
		t.Errorf("with a transaction open, Commit is disabled %v, at %v from %v", open.Disabled, open.Bounds, commit.Bounds)
	}
	testutil.Snapshot(t, tt, "editor-toolbar-transaction")
	if err := tt.Click("Roll Back"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the rollback", func() bool { return !q.Running && q.Tx == db.TxNone })
}

// A connection that commits each statement shows no Commit: it would
// never have anything to commit.
func TestAutoCommitShowsNoCommit(t *testing.T) {
	_, tt, _, _ := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT 1")
	tt.Frame()
	if _, ok := tt.Access("Commit"); ok {
		t.Error("Commit shows under auto-commit with no transaction open")
	}
}

// In a narrow pane, as one of a split, the toolbar gives up Format and
// Explain to its overflow menu before where the statement runs, Run, or
// the switches of the view.
func TestNarrowEditorKeepsTheConnectionInSight(t *testing.T) {
	_, tt, _, _ := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:", Env: db.Production}, 520, 700, "SELECT 1")
	tt.Frame()
	tt.Frame()
	for _, name := range []string{"Runs on lite, Production", "Run", "Commit", "Roll Back", "Notebook", "AI Chat"} {
		r := named(t, tt, name)
		if r.W == 0 || r.X < 0 || r.X+r.W > 520 {
			t.Errorf("%s at %v is not in the 520 px pane", name, r)
		}
	}
	testutil.Snapshot(t, tt, "editor-toolbar-narrow")
}

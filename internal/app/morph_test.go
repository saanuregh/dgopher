package app

import (
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// A job dialog's first button stays one button through the job: Cancel
// before it and while it runs, where it was, and Close once it ended; the
// main button is disabled while the job runs, not gone.
func TestJobDialogFooterMorphs(t *testing.T) {
	a, tt, cn, _ := liteApp(t, db.Development)
	path := writeSQL(t, "long.sql", "CREATE TABLE t (a int);\nBEGIN;\nINSERT INTO t VALUES (1);\n"+longSelect)
	a.startSQLFileRun(cn, "", path, "")
	x := a.sqlFile
	testutil.WaitFor(t, tt, "the survey", func() bool { return !x.reading })
	before, ok := tt.Find("Cancel")
	if !ok {
		t.Fatalf("no Cancel before the run: %q", tt.Texts())
	}
	a.confirmSQLFile(x)
	testutil.WaitFor(t, tt, "the long statement", func() bool { return x.done.Load() == 3 })
	if _, ok := tt.Find("Run"); !ok {
		t.Fatalf("the Run button vanished while the file runs: %q", tt.Texts())
	}
	running, ok := tt.Find("Cancel")
	if !ok || running.X != before.X {
		t.Fatalf("Cancel while running at %v (found %v), before the run at %v", running.X, ok, before.X)
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the dialog to read Close", func() bool {
		_, closing := tt.Find("Close")
		_, cancelling := tt.Find("Cancel")
		return x.finished && closing && !cancelling
	})
	if err := tt.Click("Close"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.sqlFile != nil {
		t.Fatal("Close left the dialog open")
	}
}

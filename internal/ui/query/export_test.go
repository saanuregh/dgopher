package query

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// Export From Query asks the statement's parameters, then exports its
// rows with them bound, run again in full.
func TestExportFromQueryBindsParameters(t *testing.T) {
	a := newFakeQueryHost(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	setup, _ := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	setup.SQL.Exec("CREATE TABLE n (i INTEGER, s TEXT)")
	setup.SQL.Exec("WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM r WHERE i < 1200) INSERT INTO n SELECT i, 'row ' || i FROM r")
	setup.Close()
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	folder := t.TempDir()
	a.Settings().Export.Folder = folder
	tt := ui.NewTester(a.view, 1100, 760)
	q := newEditor(t, a, tt, cn, "-- connection: lite\n\nSELECT * FROM n WHERE i <= :last")
	testutil.SetCaret(tt, &q.Editor, len([]rune(q.Editor.Text)))
	q.exportFromQuery()
	if a.dialogs.params == nil {
		t.Fatal("no parameters asked")
	}
	a.dialogs.params.fields[0].value = "10"
	a.dialogs.params.submit()
	tt.Frame()
	if err := tt.Click("Export"); err != nil {
		t.Fatal(err)
	}
	var files []string
	testutil.WaitFor(t, tt, "the export", func() bool {
		files, _ = filepath.Glob(filepath.Join(folder, "*.csv"))
		return len(files) == 1
	})
	testutil.WaitFor(t, tt, "the rows", func() bool {
		data, _ := os.ReadFile(files[0])
		return strings.Count(string(data), "\n") == 11
	})
	// The value is bound, not pasted into the statement, and the export
	// ran it again in full rather than writing the rows read.
	var ev *audit.Event
	testutil.WaitFor(t, tt, "the export's audit event", func() bool {
		for i := range a.Events {
			if a.Events[i].Kind == audit.KindExport {
				ev = &a.Events[i]
			}
		}
		return ev != nil
	})
	if strings.Contains(ev.Statement, "10") || strings.Contains(ev.Statement, ":last") || strings.HasSuffix(ev.Detail, "the rows read") || ev.Rows != 10 {
		t.Fatalf("export event %q, %q, %d rows", ev.Statement, ev.Detail, ev.Rows)
	}
}

// An editor's export runs again on its session, where it sees what the
// rows shown saw, as a temporary table; meanwhile the editor runs
// nothing there.
func TestExportFromQueryOnTheEditorsSession(t *testing.T) {
	a := newFakeQueryHost(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	folder := t.TempDir()
	a.Settings().Export.Folder = folder
	tt := ui.NewTester(a.view, 1100, 760)
	q := newEditor(t, a, tt, cn, "-- connection: lite\n\nCREATE TEMP TABLE mine AS SELECT 1 AS one UNION ALL SELECT 2;\n\nSELECT * FROM mine")
	testutil.SetCaret(tt, &q.Editor, 30)
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the temporary table", func() bool { return !q.Running && len(q.messages) > 0 })
	testutil.SetCaret(tt, &q.Editor, len([]rune(q.Editor.Text)))
	q.exportFromQuery()
	tt.Frame()
	if err := tt.Click("Export"); err != nil {
		t.Fatal(err)
	}
	if q.exports != 1 {
		t.Fatalf("the export does not hold the session: %d", q.exports)
	}
	q.Run(RunStatement)
	if !strings.Contains(a.Errors[len(a.Errors)-1], "An export is reading") {
		t.Fatalf("ran during the export: %q", a.Errors)
	}
	var files []string
	testutil.WaitFor(t, tt, "the export", func() bool {
		files, _ = filepath.Glob(filepath.Join(folder, "*.csv"))
		return len(files) == 1 && q.exports == 0
	})
	if data, _ := os.ReadFile(files[0]); string(data) != "one\n1\n2\n" {
		t.Fatalf("exported %q", data)
	}
}

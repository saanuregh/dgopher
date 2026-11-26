package query

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/params"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// previewDB makes a SQLite file holding t, of three named rows, and u,
// for an editor on env.
func previewDB(t *testing.T, env db.Environment) db.Config {
	t.Helper()
	file := filepath.Join(t.TempDir(), "preview.sqlite")
	os.WriteFile(file, nil, 0o600)
	setup, err := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()
	for _, s := range []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, a INTEGER)",
		"INSERT INTO t VALUES (1, 'ann', 0), (2, 'bob', 0), (3, 'cy', 0)",
		"CREATE TABLE u (id INTEGER)",
	} {
		if _, err := setup.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file, Env: env}
}

// namesIn reads the names of t, by id, from the file itself.
func namesIn(t *testing.T, cfg db.Config) []string {
	t.Helper()
	conn, err := db.Open(context.Background(), db.Config{Name: "check", Engine: db.SQLite, Database: cfg.Database}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rows, err := conn.SQL.Query("SELECT name FROM t ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	return names
}

// previewAtStart previews the editor's statement at its start, as ⌥⌘E
// does, and waits for the run to end.
func previewAtStart(t *testing.T, tt *ui.Tester, q *Tab) {
	t.Helper()
	testutil.SetCaret(tt, q.Editor, 0)
	q.Run(RunPreview)
	testutil.WaitFor(t, tt, "the preview", func() bool { return !q.Running })
	tt.Frame()
}

// shownPreview is the one result of a preview, failing the test without
// one.
func shownPreview(t *testing.T, q *Tab) *result {
	t.Helper()
	if len(q.results) != 1 || q.results[0].preview == nil || q.results[0].view == nil || q.results[0].err != "" {
		t.Fatalf("results %+v, messages %+v", q.results, q.messages)
	}
	return q.results[0]
}

// The preview's names of its bar's buttons.
const (
	previewRunLabel    = "Run Previewed Statement"
	previewCancelLabel = "Close Preview"
)

// Preview Changes of an UPDATE shows the rows it would change, each SET
// column's new value first, read-only, under a bar with their count; the
// table is as it was.
func TestPreviewUpdateShowsNewValues(t *testing.T) {
	cfg := previewDB(t, db.Development)
	_, tt, _, q := openEditor(t, cfg, 1000, 700, "UPDATE t SET name = upper(name) WHERE id <= 2")
	previewAtStart(t, tt, q)
	r := shownPreview(t, q)
	cols := r.view.Columns()
	if len(cols) == 0 || cols[0].Name != "name → new" {
		t.Fatalf("columns %+v", cols)
	}
	rows := r.view.Rows()
	if len(rows) != 2 || db.Display(rows[0][0]) != "ANN" || db.Display(rows[1][0]) != "BOB" {
		t.Fatalf("rows %v", rows)
	}
	if names := resultNames(q.results); names[0] != "Preview · UPDATE t" {
		t.Fatalf("result named %q", names[0])
	}
	if !testutil.HasTextContaining(tt, "2 rows of t would change. Nothing has run.") {
		t.Fatalf("no count bar: %q", tt.Texts())
	}
	if r.view.ReadOnly() != previewReadOnly {
		t.Fatalf("read-only %q", r.view.ReadOnly())
	}
	testutil.Snapshot(t, tt, "preview-update")
	if got := namesIn(t, cfg); !slices.Equal(got, []string{"ann", "bob", "cy"}) {
		t.Fatalf("the UPDATE ran: %q", got)
	}
}

// Preview Changes of a DELETE says the rows would be deleted, and tints
// every cell as a deleted row's.
func TestPreviewDeleteTintsRows(t *testing.T) {
	cfg := previewDB(t, db.Development)
	_, tt, _, q := openEditor(t, cfg, 1000, 700, "DELETE FROM t WHERE id = 3")
	previewAtStart(t, tt, q)
	r := shownPreview(t, q)
	if !testutil.HasTextContaining(tt, "1 row of t would be deleted. Nothing has run.") {
		t.Fatalf("no count bar: %q", tt.Texts())
	}
	pal := &widgets.Palette{Deleted: ui.RGBA(1, 2, 3, 1), Modified: ui.RGBA(4, 5, 6, 1)}
	if fill, ok := r.preview.tint(0, 1, pal); !ok || fill != pal.Deleted {
		t.Fatalf("tint %v %v", fill, ok)
	}
	testutil.Snapshot(t, tt, "preview-delete")
	if got := namesIn(t, cfg); len(got) != 3 {
		t.Fatalf("the DELETE ran: %q", got)
	}
}

// A statement the preview cannot read is refused in Output, as an error,
// with nothing sent.
func TestPreviewRefusesJoin(t *testing.T) {
	a, tt, _, q := openEditor(t, previewDB(t, db.Development), 1000, 700, "UPDATE t SET a = 1 FROM u")
	sent := len(a.Events)
	previewAtStart(t, tt, q)
	if len(q.results) != 0 {
		t.Fatalf("results %+v", q.results)
	}
	i := slices.IndexFunc(q.messages, func(m message) bool {
		return strings.HasPrefix(m.text, "Preview Changes reads one table's rows")
	})
	if i < 0 || !q.messages[i].err || !strings.HasSuffix(q.messages[i].text, " Nothing ran.") {
		t.Fatalf("messages %+v", q.messages)
	}
	if slices.ContainsFunc(a.Events[sent:], func(e audit.Event) bool { return e.Kind == audit.KindStatement }) {
		t.Fatalf("sent %+v", a.Events[sent:])
	}
	testutil.Snapshot(t, tt, "preview-refused")
}

// Once the statement is edited, the preview's Run is unavailable, and
// says why.
func TestPreviewRunDisabledAfterEdit(t *testing.T) {
	_, tt, _, q := openEditor(t, previewDB(t, db.Development), 1000, 700, "UPDATE t SET name = 'x' WHERE id = 1")
	previewAtStart(t, tt, q)
	shownPreview(t, q)
	if run, ok := tt.Access(previewRunLabel); !ok || run.Disabled {
		t.Fatalf("Run before the edit: %+v, %v", run, ok)
	}
	q.Editor.SetText("UPDATE t SET name = 'y' WHERE id = 1")
	tt.Frame()
	run, ok := tt.Access(previewRunLabel)
	if !ok || !run.Disabled || !strings.HasPrefix(run.Description, "The statement changed since this preview: preview it again (") {
		t.Fatalf("Run after the edit: %+v, %v", run, ok)
	}
	testutil.Snapshot(t, tt, "preview-run-disabled")
}

// The preview's Run runs the statement previewed, in place of the
// preview; Cancel closes the preview.
func TestPreviewRunRunsStatement(t *testing.T) {
	cfg := previewDB(t, db.Development)
	_, tt, _, q := openEditor(t, cfg, 1000, 700, "UPDATE t SET name = upper(name) WHERE id <= 2")
	previewAtStart(t, tt, q)
	shownPreview(t, q)
	if err := tt.Click(previewCancelLabel); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(q.results) != 0 {
		t.Fatalf("Cancel left %+v", q.results)
	}
	previewAtStart(t, tt, q)
	shownPreview(t, q)
	if err := tt.Click(previewRunLabel); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the UPDATE", func() bool { return !q.Running && len(q.results) == 1 && q.results[0].preview == nil })
	if r := q.results[0]; r.err != "" || r.affected != 2 {
		t.Fatalf("result %+v", r)
	}
	if got := namesIn(t, cfg); !slices.Equal(got, []string{"ANN", "BOB", "cy"}) {
		t.Fatalf("names %q", got)
	}
}

// Preview Changes in a production question stops the run and previews
// the statement instead: nothing changes.
func TestConfirmPreviewButton(t *testing.T) {
	cfg := previewDB(t, db.Production)
	a, tt, _, q := openEditor(t, cfg, 1000, 700, "UPDATE t SET name = upper(name) WHERE id <= 2")
	runAtStart(tt, q)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.Confirm != nil })
	r := a.Confirm
	if r.Extra != "Preview Changes" {
		t.Fatalf("extra %q", r.Extra)
	}
	a.Confirm = nil
	r.Open, r.Answered = false, true
	r.OnExtra()
	testutil.WaitFor(t, tt, "the preview", func() bool {
		return !q.Running && len(q.results) == 1 && q.results[0].preview != nil
	})
	if a.Confirm != nil {
		t.Fatalf("asked again: %q", a.Confirm.Reasons)
	}
	shownPreview(t, q)
	if got := namesIn(t, cfg); !slices.Equal(got, []string{"ann", "bob", "cy"}) {
		t.Fatalf("the UPDATE ran: %q", got)
	}
}

// A parameter in a SET value is given to the preview's rows and its
// count alike, so it previews rather than refusing.
func TestPreviewTakesSetParameter(t *testing.T) {
	cfg := previewDB(t, db.Development)
	_, tt, _, q := openEditor(t, cfg, 1000, 700, "UPDATE t SET name = :name WHERE id = :id")
	dataview.SaveParamValue(q.Conn.Project, "lite", ":name", params.Input{Value: "zed"})
	dataview.SaveParamValue(q.Conn.Project, "lite", ":id", params.Input{Value: "2", Kind: "number"})
	previewAtStart(t, tt, q)
	r := shownPreview(t, q)
	if rows := r.view.Rows(); len(rows) != 1 || db.Display(rows[0][0]) != "zed" {
		t.Fatalf("rows %v", rows)
	}
	if !testutil.HasTextContaining(tt, "1 row of t would change. Nothing has run.") {
		t.Fatalf("no count bar: %q", tt.Texts())
	}
}

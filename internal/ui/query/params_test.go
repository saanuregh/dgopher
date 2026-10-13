package query

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/params"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// The parameters asked close at Escape as they opened, and ask once a
// value was typed.
func TestParamsKeepEdits(t *testing.T) {
	a := newFakeQueryHost(t)
	file := filepath.Join(t.TempDir(), "p.sqlite")
	os.WriteFile(file, nil, 0o600)
	setup, _ := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	setup.SQL.Exec("CREATE TABLE n (i INTEGER)")
	setup.Close()
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1100, 760)
	q := newEditor(t, a, tt, cn, "-- connection: lite\n\nSELECT * FROM n WHERE i <= :last")
	ask := func() {
		q.withParams([]string{q.Editor.Text()}, func(map[string]params.Input) {})
		tt.Frame()
		if a.dialogs.params == nil {
			t.Fatal("no parameters asked")
		}
	}
	ask()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.dialogs.params != nil {
		t.Fatalf("untouched parameters ask: %q", tt.Texts())
	}
	ask()
	a.dialogs.params.fields[0].value = "10"
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.dialogs.params == nil || !tt.HasText("Discard your changes?") {
		t.Fatalf("typed parameters closed: %q", tt.Texts())
	}
}

// paramsEditor opens a connected editor of a SQLite file on text.
func paramsEditor(t *testing.T, text string) (*fakeQueryHost, *ui.Tester, *Tab) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "p.sqlite")
	os.WriteFile(file, nil, 0o600)
	a, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file}, 1100, 760, text)
	return a, tt, q
}

// A run takes the values kept for its connection without asking, and
// says which it ran with; those of another connection are not its own.
func TestParamsSavedSkipAsk(t *testing.T) {
	a, tt, q := paramsEditor(t, "SELECT :last")
	dataview.SaveParamValue(q.Conn.Project, "lite", ":last", params.Input{Value: "10", Kind: "number"})
	dataview.SaveParamValue(q.Conn.Project, "other", ":more", params.Input{Value: "1"})
	var got map[string]params.Input
	q.withParams([]string{q.Editor.Text()}, func(v map[string]params.Input) { got = v })
	tt.Frame()
	if a.dialogs.params != nil || got[":last"] != (params.Input{Value: "10", Kind: "number"}) {
		t.Fatalf("asked %v, ran with %v", a.dialogs.params != nil, got)
	}
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the run", func() bool {
		return len(q.messages) > 0 && strings.HasSuffix(q.messages[len(q.messages)-1].text, " · :last = 10")
	})
	q.withParams([]string{"SELECT :more"}, func(map[string]params.Input) {})
	if a.dialogs.params == nil {
		t.Fatal("another connection's value taken")
	}
}

// A run asks only for what has no value, and keeps what it is given for
// the next run.
func TestParamsAskOnlyUnset(t *testing.T) {
	a, tt, q := paramsEditor(t, "SELECT :a, :b")
	dataview.SaveParamValue(q.Conn.Project, "lite", ":a", params.Input{Value: "1"})
	var got map[string]params.Input
	q.withParams([]string{q.Editor.Text()}, func(v map[string]params.Input) { got = v })
	tt.Frame()
	f := a.dialogs.params
	if f == nil || len(f.fields) != 1 || f.fields[0].key != ":b" {
		t.Fatalf("asked %+v", f)
	}
	f.fields[0].value = "5"
	f.submit()
	if got[":a"].Value != "1" || got[":b"].Value != "5" {
		t.Fatalf("ran with %v", got)
	}
	if kept := dataview.ParamValues(q.Conn.Project, "lite"); kept[":b"].Value != "5" {
		t.Fatalf("kept %v", kept)
	}
}

// The bar shows the parameters of the editor's text; a value typed in it
// is kept, and runs take it without asking; one kept from elsewhere
// shows; a run takes what was typed just before it.
func TestParamsBar(t *testing.T) {
	a, tt, q := paramsEditor(t, "SELECT :id")
	if !tt.HasText("Parameters") || !tt.HasText(":id") {
		t.Fatalf("no bar: %q", tt.Texts())
	}
	if err := tt.Click("Value of :id"); err != nil {
		t.Fatal(err)
	}
	tt.Type("42")
	tt.Frame()
	if kept := dataview.ParamValues(q.Conn.Project, "lite"); len(kept) != 0 {
		t.Fatalf("kept while typing: %v", kept)
	}
	testutil.WaitFor(t, tt, "the value kept", func() bool { return dataview.ParamValues(q.Conn.Project, "lite")[":id"].Value == "42" })
	q.withParams([]string{q.Editor.Text()}, func(map[string]params.Input) {})
	tt.Frame()
	if a.dialogs.params != nil {
		t.Fatal("asked a value typed in the bar")
	}
	dataview.SaveParamValue(q.Conn.Project, "lite", ":id", params.Input{Value: "7"})
	tt.Frame()
	if e := q.paramsBarState.edits[":id"]; e.now.Value != "7" {
		t.Fatalf("the bar shows %+v, not the value kept from elsewhere", e)
	}
	testutil.Snapshot(t, tt, "params-bar")
	if err := tt.Click("Forget the value of :id"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the value forgotten", func() bool { return len(dataview.ParamValues(q.Conn.Project, "lite")) == 0 })
	if err := tt.Click("Value of :id"); err != nil {
		t.Fatal(err)
	}
	tt.Type("9")
	tt.Frame()
	var got map[string]params.Input
	q.withParams([]string{q.Editor.Text()}, func(v map[string]params.Input) { got = v })
	if a.dialogs.params != nil || got[":id"].Value != "9" {
		t.Fatalf("a run before typing stopped took %v", got)
	}
	q.Editor.SetText("SELECT 1")
	tt.Frame()
	if tt.HasText("Parameters") {
		t.Fatalf("a bar without parameters: %q", tt.Texts())
	}
}

// What was typed in the bar and not kept yet does not overwrite a value
// kept since in another editor, and follows a new ID of the connection.
func TestParamsBarPendingEdits(t *testing.T) {
	_, tt, q := paramsEditor(t, "SELECT :id")
	p := q.Conn.Project
	typed := func(v string) {
		t.Helper()
		if err := tt.Click("Value of :id"); err != nil {
			t.Fatal(err)
		}
		tt.Type(v)
		tt.Frame()
	}
	typed("42")
	dataview.SaveParamValue(p, "lite", ":id", params.Input{Value: "5", Kind: "auto"})
	var got map[string]params.Input
	q.withParams([]string{q.Editor.Text()}, func(v map[string]params.Input) { got = v })
	if got[":id"].Value != "5" || dataview.ParamValues(p, "lite")[":id"].Value != "5" {
		t.Fatalf("ran with %v, kept %v", got, dataview.ParamValues(p, "lite"))
	}
	tt.Frame()
	typed("7")
	if err := dataview.RenameConnection(p, "lite", "lite2"); err != nil {
		t.Fatal(err)
	}
	q.Conn.Config.ID = p.Prefix + "lite2"
	q.keepParamEdits()
	if kept := dataview.ParamValues(p, "lite2"); kept[":id"].Value != q.paramsBarState.edits[":id"].now.Value || kept[":id"].Value == "5" || len(dataview.ParamValues(p, "lite")) != 0 {
		t.Fatalf("after the new ID: %v, %v", kept, dataview.ParamValues(p, "lite"))
	}
}

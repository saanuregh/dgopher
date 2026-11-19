package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// fruitTable opens a project with a SQLite connection whose table fruit
// holds five rows, and returns the connection.
func fruitTable(t *testing.T, a *App, tt *ui.Tester, path string) *connection.Conn {
	t.Helper()
	projDir := filepath.Join(filepath.Dir(path), "proj")
	os.MkdirAll(projDir, 0o755)
	p, err := a.addProject(projDir)
	if err != nil {
		t.Fatal(err)
	}
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: path})
	a.saveProject(p)
	connectNow(t, a, tt, cn)
	for _, s := range []string{
		"CREATE TABLE IF NOT EXISTS fruit (id INTEGER PRIMARY KEY, label TEXT)",
		"INSERT OR IGNORE INTO fruit VALUES (1, 'apple'), (2, 'banana'), (3, 'cherry'), (4, 'durian'), (5, 'elderberry')",
	} {
		if _, err := cn.DB().SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return cn
}

var fruit = db.Object{Schema: "main", Name: "fruit", Kind: db.KindTable, Rows: -1, Bytes: -1}

// activeTable waits for the tab in front to be a table tab that read its
// rows, and returns it.
func activeTable(t *testing.T, a *App, tt *ui.Tester) *dataview.TableTab {
	t.Helper()
	var tb *dataview.TableTab
	testutil.WaitFor(t, tt, "the table's rows", func() bool {
		var ok bool
		tb, ok = a.ActiveTab().(*dataview.TableTab)
		return ok && !tb.Busy() && tt.HasText("label")
	})
	return tb
}

// above reports whether the text a shows above the text b.
func above(t *testing.T, tt *ui.Tester, a, b string) bool {
	t.Helper()
	ra, oka := tt.Find(a)
	rb, okb := tt.Find(b)
	if !oka || !okb {
		t.Fatalf("%q shown %v, %q shown %v: %q", a, oka, b, okb, tt.Texts())
	}
	return ra.Y < rb.Y
}

// A table tab comes back after a restart with its filter in the WHERE
// box, its sort, read on the server, and its chosen cell.
func TestTableTabRestoresFilterSortCursor(t *testing.T) {
	cfgDir := t.TempDir()
	st, _ := store.Open(cfgDir, store.MemorySecrets())
	a := startApp(t, st)
	tt := ui.NewTester(a.view, 1200, 760)
	cn := fruitTable(t, a, tt, writeSQL(t, "x.sqlite", ""))
	a.OpenTable(cn, "", fruit, dataview.PageData)
	tb := activeTable(t, a, tt)

	if err := tt.Click("Filter"); err != nil {
		t.Fatal(err)
	}
	tt.Type("id > 1")
	tt.Key(0, ui.KeyEnter)
	testutil.WaitFor(t, tt, "the filtered rows", func() bool { return !tb.Busy() && !tt.HasText("apple") })
	for range 2 { // ascending, then descending
		if err := tt.Click("label"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		testutil.WaitFor(t, tt, "the sorted rows", func() bool { return !tb.Busy() })
	}
	if !above(t, tt, "durian", "cherry") {
		t.Fatalf("not sorted descending: %q", tt.Texts())
	}
	if err := tt.Click("cherry"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	want := dataview.TableState{Filter: "id > 1", Sort: []dataview.SortKey{{Column: "label", Desc: true}}, Row: 2, Column: "label"}
	if got := tb.State(); !sameState(got, want) {
		t.Fatalf("state %+v, want %+v", got, want)
	}
	a.saveWorkspace(true)
	waitIdle(t, a, tt)

	st2, _ := store.Open(cfgDir, store.MemorySecrets())
	b := startApp(t, st2)
	tt2 := ui.NewTester(b.view, 1200, 760)
	tt2.Frame()
	w, ok := b.ActiveTab().(*waitingTab)
	if !ok || w.saved.Filter != "id > 1" || w.saved.Column != "label" || w.saved.Row != 2 || w.saved.Sort == "" {
		t.Fatalf("restored %T %+v", b.ActiveTab(), w)
	}
	connectNow(t, b, tt2, w.conn)
	restored := activeTable(t, b, tt2)
	if got := restored.State(); !sameState(got, want) {
		t.Fatalf("restored state %+v, want %+v", got, want)
	}
	if !boxShows(tt2, "id > 1") || tt2.HasText("apple") {
		t.Fatalf("the filter is not shown or not applied: %q", tt2.Texts())
	}
	if !above(t, tt2, "elderberry", "durian") || !above(t, tt2, "cherry", "banana") {
		t.Fatalf("not sorted descending: %q", tt2.Texts())
	}
	if s, _ := savedTabOf(restored); s.Filter != w.saved.Filter || s.Sort != w.saved.Sort || s.Row != 2 || s.Column != "label" {
		t.Fatalf("kept again as %+v, was %+v", s, w.saved)
	}
	waitIdle(t, b, tt2)
}

// boxShows reports whether a field, as the WHERE box, shows text.
func boxShows(tt *ui.Tester, text string) bool {
	for _, n := range tt.AccessAll() {
		if n.Value == text {
			return true
		}
	}
	return false
}

func sameState(a, b dataview.TableState) bool {
	if a.Filter != b.Filter || a.Row != b.Row || a.Column != b.Column || len(a.Sort) != len(b.Sort) {
		return false
	}
	for i := range a.Sort {
		if a.Sort[i] != b.Sort[i] {
			return false
		}
	}
	return true
}

// A workspace written before tabs kept their filter, sort and cell
// restores its table as it did: unfiltered, in the key's order.
func TestOldWorkspaceTableTabLoads(t *testing.T) {
	cfgDir := t.TempDir()
	st, _ := store.Open(cfgDir, store.MemorySecrets())
	a := startApp(t, st)
	tt := ui.NewTester(a.view, 1200, 760)
	fruitTable(t, a, tt, writeSQL(t, "x.sqlite", ""))
	old := `{"editors":[],"active":-1,"tabs":[{"kind":"table","connection":"lite","schema":"main","name":"fruit","objectKind":"table","page":0}],"order":["tabs/0"],"front":"tabs/0"}`
	if err := a.projects[0].Local.SaveJSON(workspaceFile, json.RawMessage(old)); err != nil {
		t.Fatal(err)
	}

	st2, _ := store.Open(cfgDir, store.MemorySecrets())
	b := startApp(t, st2)
	tt2 := ui.NewTester(b.view, 1200, 760)
	tt2.Frame()
	w, ok := b.ActiveTab().(*waitingTab)
	if !ok || w.saved.Name != "fruit" {
		t.Fatalf("restored %T", b.ActiveTab())
	}
	connectNow(t, b, tt2, w.conn)
	tb := activeTable(t, b, tt2)
	if got := tb.State(); !sameState(got, dataview.TableState{}) {
		t.Fatalf("state %+v", got)
	}
	if !tt2.HasText("apple") || !above(t, tt2, "apple", "elderberry") {
		t.Fatalf("rows %q", tt2.Texts())
	}
	waitIdle(t, b, tt2)
}

// A restored filter the table no longer allows stays in the WHERE box,
// and the tab says why it read no rows, offering to edit or clear it.
func TestRestoredBadFilterShowsBanner(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 760)
	path := writeSQL(t, "x.sqlite", "")
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: path})
	connectNow(t, a, tt, cn)
	for _, q := range []string{"CREATE TABLE fruit (id INTEGER PRIMARY KEY, label TEXT)", "INSERT INTO fruit VALUES (1, 'apple')"} {
		if _, err := cn.DB().SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s := savedTab{Kind: savedTable, Connection: cn.Config.ID, Schema: "main", Name: "fruit", ObjectKind: string(db.KindTable),
		Filter: "colour = 'red'", Sort: `[{"column":"gone","desc":true}]`, Row: 9, Column: "gone"}
	w := &waitingTab{a: a, conn: cn, saved: s}
	a.main.tabs().Add(w)
	testutil.WaitFor(t, tt, "the banner", func() bool { return tt.HasText("Edit Filter") })
	if !tt.HasText("Clear Filter") || !boxShows(tt, "colour = 'red'") || tt.HasText("apple") {
		t.Fatalf("texts %q", tt.Texts())
	}
	tb, ok := a.ActiveTab().(*dataview.TableTab)
	if !ok {
		t.Fatalf("in front %T", a.ActiveTab())
	}
	if got, _ := savedTabOf(tb); got.Filter != s.Filter {
		t.Fatalf("kept as %+v", got)
	}
	if err := tt.Click("Clear Filter"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the rows", func() bool { return tt.HasText("apple") })
	waitIdle(t, a, tt)
}

// A table opened from the sidebar takes the place of its waiting tab,
// whatever filter, sort and cell the waiting tab keeps.
func TestWaitingTableGivesWayWhateverItsState(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 760)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	connectNow(t, a, tt, cn)
	if _, err := cn.DB().SQL.Exec("CREATE TABLE fruit (id INTEGER PRIMARY KEY, label TEXT)"); err != nil {
		t.Fatal(err)
	}
	s := savedTab{Kind: savedTable, Connection: cn.Config.ID, Schema: "main", Name: "fruit", ObjectKind: string(db.KindTable),
		Filter: "id > 1", Sort: `[{"column":"label"}]`, Row: 3, Column: "label"}
	if !s.same(savedTab{Kind: savedTable, Connection: cn.Config.ID, Schema: "main", Name: "fruit"}) {
		t.Fatal("not the same table")
	}
	w := &waitingTab{a: a, conn: cn, saved: s}
	a.main.tabs().Append(&plainTab{"front"})
	a.main.tabs().Append(w)
	w.opening = true // as though it were on its way: the sidebar's tab comes first
	a.OpenTable(cn, "", fruit, dataview.PageData)
	tt.Frame()
	if _, ok := a.main.tabs().At(1).(*dataview.TableTab); !ok || a.main.tabs().Len() != 2 || a.holds(w) {
		t.Fatalf("%d tabs, second %T", a.main.tabs().Len(), a.main.tabs().At(1))
	}
	waitIdle(t, a, tt)
}

// A filter holding a credential, or naming a column whose values the
// table hides, is not kept in workspace.json: the tab comes back
// unfiltered. The tab itself keeps showing it.
func TestSavedTabLeavesOutSecretFilters(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 760)
	cn := fruitTable(t, a, tt, writeSQL(t, "x.sqlite", ""))
	cn.Project.HiddenValues = map[string][]string{"lite/main.fruit": {"label"}}
	a.OpenTable(cn, "", fruit, dataview.PageData)
	tb := activeTable(t, a, tt)
	for _, c := range []struct{ filter, kept string }{
		{"id > 1", "id > 1"},
		{"id IN (SELECT 1 WHERE 'postgres://bob:hunter2@db/x' <> '')", ""},
		{"label = 'apple'", ""},
		{`"label" <> 'apple'`, ""},
	} {
		if err := tt.Click("Filter"); err != nil {
			t.Fatal(err)
		}
		tt.Key(ui.Cmd, ui.KeyA)
		tt.Type(c.filter)
		tt.Key(0, ui.KeyEnter)
		testutil.WaitFor(t, tt, "the filtered rows", func() bool { return !tb.Busy() && tb.State().Filter == c.filter })
		if s, _ := savedTabOf(tb); s.Filter != c.kept {
			t.Errorf("filter %q kept as %q, want %q", c.filter, s.Filter, c.kept)
		}
	}
	waitIdle(t, a, tt)
}

// Reopen Closed Tab brings a table back with the filter, sort and cell it
// closed with.
func TestReopenTableKeepsFilterSortCursor(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 760)
	cn := fruitTable(t, a, tt, writeSQL(t, "x.sqlite", ""))
	a.OpenTable(cn, "", fruit, dataview.PageData)
	tb := activeTable(t, a, tt)
	if err := tt.Click("Filter"); err != nil {
		t.Fatal(err)
	}
	tt.Type("id > 1")
	tt.Key(0, ui.KeyEnter)
	testutil.WaitFor(t, tt, "the filtered rows", func() bool { return !tb.Busy() && !tt.HasText("apple") })
	if err := tt.Click("label"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	testutil.WaitFor(t, tt, "the sorted rows", func() bool { return !tb.Busy() })
	if err := tt.Click("cherry"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	want := tb.State()
	if want.Filter != "id > 1" || len(want.Sort) != 1 || want.Column != "label" || want.Row != 1 {
		t.Fatalf("state before closing %+v", want)
	}
	waitIdle(t, a, tt)
	a.closeTabOf(tb)
	tt.Frame()
	a.reopenTab()
	reopened := activeTable(t, a, tt)
	if reopened == tb {
		t.Fatal("the closed tab itself came back")
	}
	if got := reopened.State(); !sameState(got, want) {
		t.Fatalf("reopened with %+v, want %+v", got, want)
	}
	waitIdle(t, a, tt)
}

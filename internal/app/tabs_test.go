package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/datamodel"
	"dgopher/internal/db"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/modelview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/redis"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The tab kinds say what the tab bar, the status bar and the idle check
// ask of them: a method misnamed would fall back unnoticed.
var (
	_ widgets.Iconed           = (*dataview.TableTab)(nil)
	_ widgets.Iconed           = (*dataview.ERTab)(nil)
	_ widgets.Iconed           = (*redis.Tab)(nil)
	_ widgets.Iconed           = (*activityTab)(nil)
	_ widgets.Iconed           = (*usersTab)(nil)
	_ widgets.Iconed           = (*dashboard.Tab)(nil)
	_ widgets.Iconed           = (*modelview.Tab)(nil)
	_ widgets.Placed           = (*query.Tab)(nil)
	_ widgets.Placed           = (*dataview.TableTab)(nil)
	_ widgets.Placed           = (*dataview.ERTab)(nil)
	_ widgets.Placed           = (*dashboard.Tab)(nil)
	_ widgets.Placed           = (*modelview.Tab)(nil)
	_ widgets.ConnectionHolder = (*query.Tab)(nil)
	_ widgets.ConnectionHolder = (*dashboard.Tab)(nil)
	_ widgets.ShownNotifier    = (*query.Tab)(nil)
)

// plainTab is a tab with a title and nothing else.
type plainTab struct{ title string }

func (t *plainTab) Title() string                { return t.title }
func (t *plainTab) Connection() *connection.Conn { return nil }
func (t *plainTab) View(c *ui.Context)           { ui.Text(c, t.title) }
func (t *plainTab) CloseReason() string          { return "" }
func (t *plainTab) Close()                       {}

func plainTabs(names ...string) []widgets.Tab {
	out := make([]widgets.Tab, len(names))
	for i, n := range names {
		out[i] = &plainTab{n}
	}
	return out
}

func order(w *window) string {
	var names []string
	for _, t := range w.tabs.All() {
		n := t.Title()
		if w.tabs.Pinned(t) {
			n += "*"
		}
		names = append(names, n)
	}
	return strings.Join(names, " ")
}

// tabsApp is an app showing plain tabs a, b, c and d.
func tabsApp(t *testing.T) (*App, *ui.Tester) {
	t.Helper()
	a := newTestApp(t)
	for _, tb := range plainTabs("a", "b", "c", "d") {
		a.AddTab(tb)
	}
	a.tabs.SelectAt(0)
	tt := ui.NewTester(a.view, 1200, 700)
	tt.Frame()
	return a, tt
}

// dragTo drags the tab titled from onto the one titled to.
func dragTo(t *testing.T, tt *ui.Tester, from, to string) {
	t.Helper()
	f, ok1 := tt.Find(from)
	g, ok2 := tt.Find(to)
	if !ok1 || !ok2 {
		t.Fatalf("no tab %s or %s: %q", from, to, tt.Texts())
	}
	x, y := f.X+f.W/2, f.Y+f.H/2
	tt.Press(x, y)
	for i := 1; i <= 8; i++ {
		tt.Move(x+(g.X+g.W/2-x)*float32(i)/8, y)
		tt.Frame()
	}
	tt.Release(g.X+g.W/2, y)
	tt.Frame()
	tt.Frame()
}

// A tab dragged onto another takes its place, the tab in front staying.
func TestDragReordersTabs(t *testing.T) {
	a, tt := tabsApp(t)
	front := a.ActiveTab()
	dragTo(t, tt, "a", "c")
	if got := order(a.window); got != "b c a d" || a.ActiveTab() != front {
		t.Fatalf("dragged right: %s, front %s", got, a.ActiveTab().Title())
	}
	dragTo(t, tt, "d", "b")
	if got := order(a.window); got != "d b c a" {
		t.Fatalf("dragged left: %s", got)
	}
}

// menu chooses an item of a tab's menu.
func menu(t *testing.T, tt *ui.Tester, tab, item string) {
	t.Helper()
	if err := tt.RightClick(tab); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(item); err != nil {
		t.Fatalf("%s: %v", item, err)
	}
	tt.Frame()
	tt.Frame()
}

// Pins move a tab first, and keep it from Close to the Right, Close All
// and its key; its menu's Close closes it. Reopen opens what closed last.
func TestPinCloseAndReopen(t *testing.T) {
	a, tt := tabsApp(t)
	menu(t, tt, "c", "Pin")
	if got := order(a.window); got != "c* a b d" {
		t.Fatalf("pinned: %s", got)
	}
	menu(t, tt, "a", "Close to the Right")
	if got := order(a.window); got != "c* a" {
		t.Fatalf("closed to the right: %s", got)
	}
	a.tabs.SelectAt(0)
	a.closeActive()
	tt.Frame()
	if got := order(a.window); got != "c* a" || !testutil.HasTextContaining(tt, "is pinned") {
		t.Fatalf("the key closed a pinned tab: %s", got)
	}
	menu(t, tt, "a", "Close All")
	if got := order(a.window); got != "c*" {
		t.Fatalf("closed all: %s", got)
	}
	menu(t, tt, "c", "Close")
	if a.tabs.Len() != 0 {
		t.Fatalf("Close left %s", order(a.window))
	}
	// Plain tabs do not open again: nothing was kept.
	if len(a.closedTabs) != 0 {
		t.Fatalf("kept %d plain tabs", len(a.closedTabs))
	}
}

// Reopen Closed Tab opens the tabs closed, the last first, at their
// places; a file gone since says so.
func TestReopenClosedTab(t *testing.T) {
	a, tt, cn := idleApp(t, false)
	q := newAppEditor(t, a, tt, cn, "SELECT a FROM t")
	q.Editor.Text += "\n-- kept"
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "t", Kind: db.KindTable}, 0)
	testutil.WaitFor(t, tt, "the table", func() bool { return a.tabs.Len() == 2 })
	path := q.Path
	a.closeTabOf(q)
	a.closeTabOf(a.tabs.At(0))
	tt.Frame()
	if a.tabs.Len() != 0 || len(a.closedTabs) != 2 {
		t.Fatalf("%d tabs, %d kept", a.tabs.Len(), len(a.closedTabs))
	}
	a.reopenTab()
	a.reopenTab()
	testutil.WaitFor(t, tt, "both back", func() bool { return a.tabs.Len() == 2 })
	if r, ok := a.tabs.At(0).(*query.Tab); !ok || r.Path != path || !strings.Contains(r.Editor.Text, "-- kept") {
		t.Fatalf("first %T %s", a.tabs.At(0), a.tabs.At(0).Title())
	}
	if _, ok := a.tabs.At(1).(*dataview.TableTab); !ok {
		t.Fatalf("second %T", a.tabs.At(1))
	}
	a.closeTabOf(a.tabs.At(0))
	tt.Frame()
	os.Remove(path)
	a.reopenTab()
	tt.Frame()
	if a.tabs.Len() != 1 || !testutil.HasTextContaining(tt, "is gone") {
		t.Fatalf("a gone file: %d tabs, %q", a.tabs.Len(), tt.Texts())
	}
}

// The middle button closes a tab, but a pinned one; the others go on.
func TestMiddleClickClosesTab(t *testing.T) {
	a, tt := tabsApp(t)
	middle := ui.InputEvent{Kind: ui.InputPointerDown, Button: middleButton}
	if a.closeOnMiddlePress(a.window, a.tabs.At(1), ui.InputEvent{Kind: ui.InputPointerDown}) {
		t.Fatal("took the primary button")
	}
	if !a.closeOnMiddlePress(a.window, a.tabs.At(1), middle) {
		t.Fatal("did not take the middle button")
	}
	tt.Frame()
	if got := order(a.window); got != "a c d" {
		t.Fatalf("middle-clicked b: %s", got)
	}
	a.window.tabs.SetPinned(a.tabs.At(0), true)
	if a.closeOnMiddlePress(a.window, a.tabs.At(0), middle) {
		t.Fatal("closes a pinned tab")
	}
}

// Tabs that do not fit are listed by a button at the bar's end, which
// brings the one chosen to the front.
func TestTabOverflowList(t *testing.T) {
	a, tt := tabsApp(t)
	if _, ok := tt.Find("Open Tabs"); ok {
		t.Fatal("a list of four tabs that fit")
	}
	for i := range 30 {
		a.AddTab(&plainTab{title: "a tab with a long title " + string(rune('A'+i))})
	}
	a.tabs.SelectAt(0)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("Open Tabs"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	if err := tt.ChooseMenuItem("a tab with a long title Z"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.ActiveTab().Title() != "a tab with a long title Z" {
		t.Fatalf("in front: %s", a.ActiveTab().Title())
	}
}

// A pinned editor is pinned again, first, after a restart.
func TestPinnedEditorRestored(t *testing.T) {
	st, _ := store.Open(t.TempDir(), store.MemorySecrets())
	a := startApp(t, st)
	if _, err := a.addProject(newProjectDir(t, "p")); err != nil {
		t.Fatal(err)
	}
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	a.saveProject(a.projects[0])
	tt := ui.NewTester(a.view, 1000, 700)
	newAppEditor(t, a, tt, cn, "SELECT 1;")
	second := newAppEditor(t, a, tt, cn, "SELECT 2;")
	a.window.tabs.SetPinned(second, true)
	a.saveWorkspace(true)

	b := startApp(t, st)
	tt = ui.NewTester(b.view, 1000, 700)
	tt.Frame()
	if b.tabs.Len() != 2 || !b.main.tabs.Pinned(b.tabs.At(0)) || !strings.Contains(b.tabs.At(0).(*query.Tab).Editor.Text, "SELECT 2") || b.main.tabs.Pinned(b.tabs.At(1)) {
		t.Fatalf("restored: %s", order(b.main))
	}
}

// A query file shows under its name the connection its header names, an
// ID no connection has as it is, and nothing without a header; an editor's
// tab shows its connection, and its database when not the connection's.
func TestQueryFileShowsConnection(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	cn := addConn(a, db.Config{ID: "lite", Name: "Lite DB", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	os.MkdirAll(p.Queries, 0o755)
	for name, text := range map[string]string{
		"a.sql": "-- connection: lite\n\nSELECT 1",
		"b.sql": "SELECT 2",
		"c.sql": "-- connection: ghost\n\nSELECT 3",
	} {
		os.WriteFile(filepath.Join(p.Queries, name), []byte(text), 0o600)
	}
	tt := ui.NewTester(a.view, 1000, 700)
	a.ScanQueries(p, true)
	testutil.WaitFor(t, tt, "the files", func() bool { return len(p.Files) == 3 })
	a.nav.expand(navNode{kind: nodeQueries, projectDir: p.Dir})
	tt.Frame()
	if got := []string{p.FileConnections["a.sql"].ID, p.FileConnections["b.sql"].ID, p.FileConnections["c.sql"].ID}; strings.Join(got, ",") != "lite,,ghost" {
		t.Fatalf("headers %q", got)
	}
	if !tt.HasText("Lite DB") || !tt.HasText("ghost") {
		t.Fatalf("the sidebar: %q", tt.Texts())
	}
	q := newAppEditor(t, a, tt, cn, "SELECT 1")
	a.window.tabs.SetPinned(q, true)
	newAppEditor(t, a, tt, cn, "SELECT 2")
	testutil.Snapshot(t, tt, "tabs-sidebar-connections")
	if got := tabSubtitle(q); got != "Lite DB" {
		t.Fatalf("tab: %q", got)
	}
	q.Database = "other"
	if got := tabSubtitle(q); got != "Lite DB · other" {
		t.Fatalf("tab in another database: %q", got)
	}
	// A dashboard shows its panels' connections.
	addConn(a, db.Config{ID: "two", Name: "Two", Engine: db.SQLite, Database: writeSQL(t, "2.sqlite", "")})
	path := dashboard.File(p, "D")
	panel := func(conn string) dashboard.Panel {
		return dashboard.Panel{Title: conn, Connection: conn, SQL: "SELECT 1", View: dashboard.Views[0], Width: 1}
	}
	(&dashboard.Dashboard{Name: "D", Panels: []dashboard.Panel{panel("lite"), panel("two"), panel("lite")}}).Save(path, nil)
	a.openDashboard(p, path)
	// Problems first: which comes first follows the connections' states.
	if got := tabSubtitle(a.ActiveTab()); got != "Lite DB +1" && got != "Two +1" {
		t.Fatalf("dashboard: %q", got)
	}
	// A data model shows the connection it is read from.
	model := datamodel.File(p, "M")
	(&datamodel.Model{Name: "M", Engine: db.SQLite, Source: &datamodel.Source{Connection: "two"}}).Save(model, nil)
	a.openModel(p, model)
	if got := tabSubtitle(a.ActiveTab()); got != "Two" {
		t.Fatalf("model: %q", got)
	}
}

// lossyTab is a plain tab whose closing loses something.
type lossyTab struct{ plainTab }

func (t *lossyTab) CloseReason() string { return "unsaved" }

// Unpinning a tab from its pin, ahead of another pinned, moves it after
// them once the bar is drawn.
func TestUnpinFirstOfTwo(t *testing.T) {
	a, tt := tabsApp(t)
	a.window.tabs.SetPinned(a.tabs.At(0), true)
	a.window.tabs.SetPinned(a.tabs.At(1), true)
	tt.Frame()
	if err := tt.Click("Unpin a"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if got := order(a.window); got != "b* a c d" {
		t.Fatalf("unpinned: %s", got)
	}
}

// Close All asks once about what every tab closing loses, and closes
// them all as agreed, none when declined; with nothing lost, at once.
func TestCloseAllAsksOnce(t *testing.T) {
	a := newTestApp(t)
	for _, tb := range []widgets.Tab{&lossyTab{plainTab{"a"}}, &plainTab{"b"}, &lossyTab{plainTab{"c"}}, &plainTab{"d"}} {
		a.AddTab(tb)
	}
	tt := ui.NewTester(a.view, 1200, 700)
	all := func(int, widgets.Tab) bool { return true }
	a.closeTabs(a.window, all)
	tt.Frame()
	r := a.closing
	if r == nil || r.title != "Close 4 tabs?" || r.reason != "c: unsaved\n\na: unsaved" || order(a.window) != "a b c d" {
		t.Fatalf("question: %+v, %s", r, order(a.window))
	}
	a.closing = nil // declined
	tt.Frame()
	if order(a.window) != "a b c d" {
		t.Fatalf("declined: %s", order(a.window))
	}
	a.closeTabs(a.window, all)
	tt.Frame()
	a.closing.open = false
	a.closing.onClose()
	tt.Frame()
	if a.closing != nil || order(a.window) != "" {
		t.Fatalf("agreed: %+v, %s", a.closing, order(a.window))
	}
	for _, name := range []string{"e", "f"} {
		a.AddTab(&plainTab{name})
	}
	a.closeTabs(a.window, all)
	tt.Frame()
	if a.closing != nil || order(a.window) != "" {
		t.Fatalf("nothing lost: %+v, %s", a.closing, order(a.window))
	}
}

// An open transaction among the tabs closing is offered a commit, as
// quitting does.
func TestCloseAllOffersCommit(t *testing.T) {
	a, tt, q, file := txApp(t, 0)
	a.AddTab(&lossyTab{plainTab{"other"}})
	a.closeTabs(a.window, func(int, widgets.Tab) bool { return true })
	tt.Frame()
	r := a.closing
	if r == nil || len(r.txs) != 1 || r.txs[0] != q {
		t.Fatalf("question: %+v", r)
	}
	r.open = false
	a.commitThen(r.txs, r.onClose)
	testutil.WaitFor(t, tt, "the commit and the close", func() bool { return a.tabs.Len() == 0 })
	if got := rowsIn(t, file); got != "1" {
		t.Fatalf("rows after the commit: %q", got)
	}
}

// Switching a pinned editor's connection keeps it pinned, in its place.
func TestSwitchConnectionKeepsPin(t *testing.T) {
	a := newTestApp(t)
	one := addConn(a, db.Config{ID: "one", Name: "one", Engine: db.SQLite, Database: writeSQL(t, "1.sqlite", "")})
	addConn(a, db.Config{ID: "two", Name: "two", Engine: db.SQLite, Database: writeSQL(t, "2.sqlite", "")})
	tt := ui.NewTester(a.view, 1000, 700)
	first := newAppEditor(t, a, tt, one, "SELECT 1")
	second := newAppEditor(t, a, tt, one, "SELECT 2")
	a.window.tabs.SetPinned(first, true)
	a.window.tabs.SetPinned(second, true)
	a.SwitchConnection(first, "two")
	tt.Frame()
	q, ok := a.tabs.At(0).(*query.Tab)
	if !ok || q.Conn.Config.Name != "two" || !a.window.tabs.Pinned(q) || !a.window.tabs.Pinned(a.tabs.At(1)) {
		t.Fatalf("switched: %s", order(a.window))
	}
}

// A tab reopened that was open in another window, which comes forward,
// leaves that window's tabs where they are.
func TestReopenInAnotherWindow(t *testing.T) {
	a, _ := tabsApp(t)
	other := a.openWindow()
	for _, tb := range plainTabs("p", "q", "r") {
		other.tabs.Add(tb)
	}
	a.closedTabs = append(a.closedTabs, closedTab{title: "r", at: 0, open: func() string { a.window = other; return "" }})
	a.reopenTab()
	if got := order(other); got != "p q r" {
		t.Fatalf("the other window: %s", got)
	}
}

// A tab's places are where it runs, grouped by connection, problems
// first; its subtitle names one, its database, a count of databases, or
// the first connection and how many more; its tooltip lists them all.
func TestTabPlaces(t *testing.T) {
	conn := func(name, database string, st connection.Status) *connection.Conn {
		return &connection.Conn{Config: db.Config{Name: name, Database: database}, Status: st, Err: "password authentication failed"}
	}
	a := conn("A", "app", connection.StatusConnected)
	b := conn("B", "", connection.StatusIdle)
	c := conn("C", "", connection.StatusFailed)
	place := func(cn *connection.Conn, database string) connection.Place {
		return connection.Place{Conn: cn, Database: database}
	}
	for _, tc := range []struct {
		places          []connection.Place
		subtitle, first string
	}{
		{nil, "", ""},
		{[]connection.Place{place(a, "")}, "A", "A"},
		{[]connection.Place{place(a, "other")}, "A · other", "A"},
		{[]connection.Place{place(a, ""), place(a, "other"), place(a, "")}, "A · 2 databases", "A"},
		{[]connection.Place{place(a, ""), place(b, ""), place(c, "")}, "C +2", "C"},
		{[]connection.Place{place(a, ""), place(b, "x")}, "B · x +1", "B"},
	} {
		groups := byConnection(tc.places)
		if got := placesSubtitle(groups); got != tc.subtitle {
			t.Errorf("%v: subtitle %q, want %q", tc.places, got, tc.subtitle)
		}
		if len(groups) > 0 && groups[0].conn.Config.Name != tc.first {
			t.Errorf("%v: first %s, want %s", tc.places, groups[0].conn.Config.Name, tc.first)
		}
	}
	tip := placesTooltip(byConnection([]connection.Place{place(a, ""), place(a, "other"), place(c, "")}))
	for _, line := range []string{"C — failed: password authentication failed", "A · app — connected", "A · other — connected"} {
		if !strings.Contains(tip, line) {
			t.Errorf("tooltip %q lacks %q", tip, line)
		}
	}
	if strings.Index(tip, "C —") > strings.Index(tip, "A · app") {
		t.Errorf("tooltip not problems first: %q", tip)
	}
}

// A dashboard on five connections shows three chips in the status bar,
// a failed one first, and the others behind "+2 more"; an editor in
// another database names it there.
func TestStatusBarPlaces(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	var panels []dashboard.Panel
	for _, name := range []string{"one", "two", "three", "four"} {
		addConn(a, db.Config{ID: name, Name: name, Engine: db.SQLite, Database: writeSQL(t, name+".sqlite", "")})
		panels = append(panels, dashboard.Panel{Title: name, Connection: name, SQL: "SELECT 1", View: dashboard.ViewValue, Width: 1})
	}
	broken := addConn(a, db.Config{ID: "broken", Name: "broken", Engine: db.SQLite, Database: filepath.Join(t.TempDir(), "no", "such", "x.sqlite")})
	panels = append(panels, dashboard.Panel{Title: "broken", Connection: "broken", SQL: "SELECT 1", View: dashboard.ViewValue, Width: 1})
	path := dashboard.File(p, "Five")
	(&dashboard.Dashboard{Name: "Five", Panels: panels}).Save(path, nil)
	tt := ui.NewTester(a.view, 1400, 800)
	a.openDashboard(p, path)
	four := a.connByID(p.Prefix + "four")
	testutil.WaitFor(t, tt, "the connections", func() bool {
		return broken.Status == connection.StatusFailed && four.Status == connection.StatusConnected
	})
	tt.Frame()
	groups := byConnection(tabPlaces(a.ActiveTab()))
	if len(groups) != 5 || groups[0].conn != broken {
		t.Fatalf("groups: %d, first %s", len(groups), groups[0].conn.Config.Name)
	}
	testutil.Snapshot(t, tt, "status-bar-five-connections")
	if !tt.HasText("+2 more") {
		t.Fatalf("no more button: %q", tt.Texts())
	}
	if err := tt.Click("+2 more"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("broken — failed: "+widgets.FirstLine(broken.Err), "Retry"); err == nil {
		t.Fatal("the failed connection is behind +2 more")
	}
	tt.CloseMenu()
	if err := tt.Click("+2 more"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("four — connected", "Disconnect"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if four.Status != connection.StatusIdle {
		t.Fatalf("four: %v", four.Status)
	}
	cn := a.connByID(p.Prefix + "one")
	q := newAppEditor(t, a, tt, cn, "SELECT 1")
	q.Database = "other"
	tt.Frame()
	if !slices.Contains(tt.Texts(), "· other") {
		t.Fatalf("no database in the status bar: %q", tt.Texts())
	}
}

package app

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// filterApp is an app with a connected SQLite connection, shop, of the
// tables orders, order_items and customers, a connection never opened,
// archive, and the query files orders-by-day.sql and cleanup.sql.
func filterApp(t *testing.T) (*App, *ui.Tester, *connection.Conn) {
	t.Helper()
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "shop.sqlite")
	os.WriteFile(file, nil, 0o600)
	d, err := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"orders", "order_items", "customers"} {
		d.SQL.Exec("CREATE TABLE " + table + " (id INTEGER)")
	}
	d.Close()
	cn := addConn(a, db.Config{ID: "shop", Name: "shop", Engine: db.SQLite, Database: file})
	addConn(a, db.Config{ID: "archive", Name: "archive", Engine: db.SQLite, Database: ":memory:"})
	p := a.projects[0]
	os.MkdirAll(p.Queries, 0o755)
	for _, name := range []string{"orders-by-day.sql", "cleanup.sql"} {
		os.WriteFile(filepath.Join(p.Queries, name), []byte("-- connection: shop\nSELECT 1;"), 0o644)
	}
	a.ScanQueries(p, true)
	tt := ui.NewTester(a.view, 1200, 760)
	// Opened once, which reads its tables, then closed again: the filter
	// searches what is known.
	node := navNode{kind: nodeConn, conn: cn.Config.ID}
	a.nav.expand(node)
	testutil.WaitFor(t, tt, "the tables", func() bool {
		return cn.Status == connection.StatusConnected && len(cn.Objects[connection.SchemaKey{Schema: cn.DefaultSchema}]) == 3 && len(p.Files) == 2
	})
	a.nav.tree.Open.Remove(node)
	tt.Frame()
	return a, tt, cn
}

// navNames are the rows of the navigator's two panes, the database tree's
// then the files', by what each shows as its name.
func navNames(a *App) []string {
	var out []string
	for _, pane := range a.nav.panes() {
		for i := range pane.tree.Rows() {
			n := pane.tree.Item(i)
			if name := a.navName(n); name != "" {
				out = append(out, name)
			} else if n.kind != nodeInfo {
				out = append(out, fmt.Sprintf("(%d)", n.kind))
			}
		}
	}
	return out
}

// Text typed in the navigator's box leaves the rows whose names hold it,
// in any case, and those that hold such rows, opened down to them: a
// table shows under its connection though the connection was closed, and
// a query file under Queries. What is not known, as the tables of a
// connection never opened, is not read to be matched.
func TestNavigatorFilterShowsMatchesAndWhatHoldsThem(t *testing.T) {
	a, tt, _ := filterApp(t)
	if slices.Contains(navNames(a), "orders") {
		t.Fatalf("the tables show before anything opened them: %q", navNames(a))
	}
	a.nav.filter = "ORD"
	tt.Frame()
	tt.Frame()
	got := navNames(a)
	for _, want := range []string{"shop", "orders", "order_items", "orders-by-day"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s does not show for the filter: %q", want, got)
		}
	}
	for _, hidden := range []string{"customers", "cleanup", "archive"} {
		if slices.Contains(got, hidden) {
			t.Errorf("%s shows though it does not match: %q", hidden, got)
		}
	}
	if cn := a.connByID(a.projects[0].Prefix + "archive"); cn.Status != connection.StatusIdle {
		t.Errorf("the filter connected archive to search it: %v", cn.Status)
	}
	// The first match as the panes show them, the files' above the tree's.
	if n, _ := a.nav.panes()[1].chosen(); a.navName(n) != "orders-by-day" || !a.nav.inFiles {
		t.Errorf("the row chosen is %q, not the first match, the query file", a.navName(n))
	}
	testutil.Snapshot(t, tt, "navigator-filter")
	// A connection that matches shows all it holds.
	a.nav.filter = "shop"
	tt.Frame()
	tt.Frame()
	a.nav.expand(navNode{kind: nodeFolder, conn: a.projects[0].Prefix + "shop", schema: "main"})
	tt.Frame()
	if got := navNames(a); !slices.Contains(got, "customers") {
		t.Errorf("the matching connection's tables do not all show: %q", got)
	}
	// Nothing matches: the navigator says so, with the way out.
	a.nav.filter = "no such thing"
	tt.Frame()
	tt.Frame()
	if len(navNames(a)) != 0 || !testutil.HasTextContaining(tt, "No matches for") {
		t.Errorf("a filter matching nothing shows %q and the rows %q", tt.Texts(), navNames(a))
	}
}

// Under a filter a pane without a match gives its room to the other, and
// with no match at all the navigator says what it could not search, with
// the way out.
func TestFilterShowsOnlyThePanesWithMatches(t *testing.T) {
	a, tt, _ := filterApp(t)
	box, _ := tt.Find("Filter the navigator")
	frames := func() {
		tt.Frame()
		tt.Frame()
	}
	a.nav.filter = "cleanup"
	frames()
	if q, ok := tt.Find("Queries"); !ok || q.Y-(box.Y+box.H) > navRowHeight {
		t.Errorf("only a file matches, and Queries is at %v under the box at %v: an empty tree is above it", q, box)
	}
	a.nav.filter = "order_items"
	frames()
	if _, files := tt.Find("Queries"); files || !tt.HasText("order_items") {
		t.Errorf("only a table matches, and the navigator shows %q with the files' pane %v", navNames(a), files)
	}
	a.nav.filter = "no such thing"
	frames()
	if !testutil.HasTextContaining(tt, "1 connection not open") {
		t.Errorf("one of two connections is closed, and the navigator says %q", tt.Texts())
	}
	if err := tt.Click("Clear Filter"); err != nil {
		t.Fatal(err)
	}
	frames()
	_, files := tt.Find("Queries")
	if _, tree := tt.Find("Connections"); a.nav.filter != "" || !files || !tree {
		t.Errorf("Clear Filter left the filter %q and the rows %q", a.nav.filter, navNames(a))
	}
}

// A heading's count follows the filter, as how many of its rows show, and
// a heading over nothing has no count: a row of zeros says nothing.
func TestHeadingCountsFollowTheFilter(t *testing.T) {
	a, tt, _ := filterApp(t)
	has := func(text string) bool { _, ok := tt.Find(text); return ok }
	if !has("2") || has("0") {
		t.Errorf("two connections and two files, no dashboards: the counts are in %q", tt.Texts())
	}
	a.nav.filter = "ord"
	tt.Frame()
	tt.Frame()
	// One of two files and of two connections, two of three tables.
	if !has("1/2") || !has("2/3") {
		t.Errorf("the counts under the filter are in %q, want 1/2 and 2/3", tt.Texts())
	}
	// A connection that matches shows all its tables: their count alone.
	a.nav.filter = "shop"
	tt.Frame()
	tt.Frame()
	a.nav.expand(navNode{kind: nodeFolder, conn: a.projects[0].Prefix + "shop", schema: "main"})
	tt.Frame()
	if !has("3") || has("3/3") {
		t.Errorf("all three tables show, and the counts are in %q", tt.Texts())
	}
}

// Of the two panes' chosen rows, the one in the pane worked in has the
// tint and the other a fainter one: one row reads as the current.
func TestOnlyThePaneWorkedInHasTheFullTint(t *testing.T) {
	a, tt, cn := filterApp(t)
	a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: cn.DefaultSchema})
	tt.Frame()
	tint := func(text string) color.Color {
		t.Helper()
		tt.Move(-1, -1)
		tt.Frame()
		tt.Frame()
		r, ok := tt.Find(text)
		if !ok {
			t.Fatalf("%q does not show: %q", text, tt.Texts())
		}
		side, _ := tt.Access("Navigator")
		// The row's start, before its arrow or its dot: no text is there.
		return tt.Image().At(int(side.Bounds.X)+8, int(r.Y+r.H/2))
	}
	if err := tt.Click("cleanup.sql"); err != nil {
		t.Fatal(err)
	}
	full := tint("cleanup.sql")
	if err := tt.Click("customers"); err != nil {
		t.Fatal(err)
	}
	if file, table, plain := tint("cleanup.sql"), tint("customers"), tint("order_items"); file == full || file == plain || table != full {
		t.Errorf("the file's row is %v and the table's %v after the table was chosen: want a fainter tint than %v on the file, not none (%v), and the full one on the table", file, table, full, plain)
	}
}

// Clearing the filter leaves the tree as open as it was before it, with
// the row chosen meanwhile in view.
func TestNavigatorFilterClearedRestoresTheTree(t *testing.T) {
	a, tt, cn := filterApp(t)
	before := navNames(a)
	a.nav.filter = "order_it"
	tt.Frame()
	tt.Frame()
	if got := navNames(a); !slices.Contains(got, "order_items") || slices.Contains(got, "orders") {
		t.Fatalf("the filter shows %q", got)
	}
	chosen := navNode{kind: nodeObject, conn: cn.Config.ID, schema: cn.DefaultSchema, name: "order_items"}
	if a.nav.tree.Item(a.nav.row) != chosen {
		t.Fatalf("the match is not the row chosen: %+v", a.nav.tree.Item(a.nav.row))
	}
	a.nav.filter = ""
	tt.Frame()
	tt.Frame()
	tt.Frame()
	got := navNames(a)
	for _, name := range before {
		if !slices.Contains(got, name) {
			t.Errorf("%s was shown before the filter, and is not after it: %q", name, got)
		}
	}
	if a.nav.row < 0 || a.nav.row >= a.nav.tree.Rows() || a.nav.tree.Item(a.nav.row) != chosen {
		t.Errorf("the row chosen under the filter is not chosen after it: row %d of %q", a.nav.row, got)
	}
}

// The box answers to the keys: ⌘L goes to it from the navigator, ↵ opens
// the match chosen, ↓ goes on to the tree, Esc clears it.
func TestNavigatorFilterKeys(t *testing.T) {
	a, tt, _ := filterApp(t)
	a.focusWant = "nav"
	tt.Frame()
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyL)
	tt.Frame()
	tt.Frame()
	if !tt.Focused("Filter the navigator") {
		t.Fatalf("⌘L from the navigator left the focus elsewhere")
	}
	tt.Type("orders-by")
	tt.Frame()
	tt.Frame()
	if got := navNames(a); !slices.Contains(got, "orders-by-day") || slices.Contains(got, "shop") {
		t.Fatalf("typing filtered to %q", got)
	}
	tt.Key(0, ui.KeyEnter)
	testutil.WaitFor(t, tt, "the file's editor", func() bool {
		tb := a.ActiveTab()
		return tb != nil && tb.Title() == "orders-by-day.sql"
	})
	if err := tt.Click("Filter the navigator"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.nav.filter != "" {
		t.Errorf("Esc left the filter %q", a.nav.filter)
	}
	// ↓ from the empty box goes on to the pane under it, the files'.
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	tt.Frame()
	if !a.nav.hasKeys || !a.nav.inFiles {
		t.Errorf("↓ from the empty box left the keys in the files' pane: %v (a pane has them: %v)", a.nav.inFiles, a.nav.hasKeys)
	}
}

// The navigator is two panes: the project's files above, and below them
// the connections and what their databases hold, under one heading that
// closes them all.
func TestNavigatorHasTwoPanes(t *testing.T) {
	a, tt, cn := filterApp(t)
	tree, files := a.nav.panes()[0], a.nav.panes()[1]
	if first := tree.tree.Item(0); first != (navNode{kind: nodeConnections, projectDir: a.projects[0].Dir}) {
		t.Errorf("the tree starts with %+v, not the connections' heading", first)
	}
	if second := tree.tree.Item(1); second.kind != nodeConn {
		t.Errorf("under the heading is %+v, not a connection: it does not start open", second)
	}
	if first := files.tree.Item(0); first != (navNode{kind: nodeQueries, projectDir: a.projects[0].Dir}) {
		t.Errorf("the files' pane starts with %+v, not Queries", first)
	}
	db, okD := tt.Access("Navigator")
	fl, okF := tt.Access("Project files")
	if !okD || !okF || db.Bounds.Y < fl.Bounds.Y+fl.Bounds.H-1 {
		t.Fatalf("the files at %v (%v), the tree at %v (%v): not one under the other", fl.Bounds, okF, db.Bounds, okD)
	}
	// One level less: a table sits two levels under its connection.
	a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: cn.DefaultSchema})
	tt.Frame()
	tt.Frame()
	conn, _ := tt.Find("shop")
	table, ok := tt.Find("orders")
	if !ok || table.X-conn.X > 3*10+1 {
		t.Errorf("a table's name starts %v px right of its connection's (found %v), more than two levels", table.X-conn.X, ok)
	}
	testutil.Snapshot(t, tt, "navigator-two-panes")
}

// The files keep their place however many rows the tree has: they are
// above it, and a connection's tables scroll in their own pane.
func TestFilesPaneStaysInViewUnderManyTables(t *testing.T) {
	a, tt, cn := filterApp(t)
	before, ok := tt.Find("orders-by-day.sql")
	if !ok {
		t.Fatalf("the query files do not show: %q", tt.Texts())
	}
	a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: cn.DefaultSchema})
	tt.SetSize(1200, 270) // too short for the tree's rows
	tt.Frame()
	tt.Frame()
	after, ok := tt.Find("orders-by-day.sql")
	if !ok || after.Y != before.Y {
		t.Fatalf("with the tables open in a short window, the file is at %v (found %v), and was at %v", after, ok, before)
	}
	tree, _ := tt.Access("Navigator")
	if need := float32(a.nav.tree.Rows()) * navRowHeight; tree.Bounds.H >= need || tree.Bounds.Y+tree.Bounds.H > 270 {
		t.Errorf("the tree is at %v for rows %v tall in a window of 270: it does not scroll in what the files leave", tree.Bounds, need)
	}
}

// F5 reads the chosen connection's schema again from the navigator, as in
// DBeaver and pgAdmin: a table made elsewhere shows.
func TestNavigatorRefreshesOnF5(t *testing.T) {
	a, tt, cn := filterApp(t)
	node := navNode{kind: nodeConn, conn: cn.Config.ID}
	a.nav.expand(node)
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: cn.DefaultSchema})
	tt.Frame()
	d, err := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: cn.Config.Database}, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.SQL.Exec("CREATE TABLE refunds (id INTEGER)")
	d.Close()
	for i := range a.nav.tree.Rows() {
		if a.nav.tree.Item(i) == node {
			a.nav.row = i
		}
	}
	a.focusWant = "nav"
	tt.Frame()
	tt.Frame()
	tt.Key(0, ui.KeyF5)
	testutil.WaitFor(t, tt, "the new table", func() bool { return slices.Contains(navNames(a), "refunds") })
}

// A table dragged from the navigator into an editor writes its name
// there, quoted as Copy Name writes it, at the caret; a column's too.
func TestDraggedTableNameIsInsertedAtTheCaret(t *testing.T) {
	a, tt, cn := filterApp(t)
	a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: cn.DefaultSchema})
	q := newAppEditor(t, a, tt, cn, "SELECT * FROM ;")
	// The editor's text starts with the lines naming its connection.
	before := q.Editor.Text()
	at := strings.Index(before, "FROM ") + len("FROM ")
	testutil.SetCaret(tt, q.Editor, at)
	tt.Frame()
	dragTo(t, tt, "customers", "SQL editor")
	want := before[:at] + db.QualifiedName(cn.DB().Dialect, cn.DefaultSchema, "customers") + before[at:]
	if got := q.Editor.Text(); got != want {
		t.Fatalf("after the drag the editor holds %q, want %q", got, want)
	}
	// What the rows carry: a table its qualified name, a column its own.
	rows := map[string]any{}
	a.nav.expand(navNode{kind: nodeObject, conn: cn.Config.ID, schema: cn.DefaultSchema, name: "orders"})
	testutil.WaitFor(t, tt, "the columns", func() bool {
		return len(cn.Columns[connection.ObjectKey{Schema: cn.DefaultSchema, Name: "orders"}]) > 0
	})
	tt.Frame()
	for i := range a.nav.tree.Rows() {
		n := a.nav.tree.Item(i)
		rows[fmt.Sprint(n.kind, " ", a.navName(n))] = a.navDragRows([]int{i})
	}
	if got := rows[fmt.Sprint(nodeColumn, " id")]; got != (widgets.NameDrag{Text: cn.DB().Dialect.Quote("id")}) {
		t.Errorf("a column's row carries %#v", got)
	}
	if got := rows[fmt.Sprint(nodeFolder, " ")]; got != nil {
		t.Errorf("the Tables folder's row carries %#v, and should not drag", got)
	}
}

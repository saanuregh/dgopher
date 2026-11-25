package app

import (
	"math"
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// headsApp is filterApp with its connections, their tables and a table's
// columns open: a heading, what it holds, and a note under one.
func headsApp(t *testing.T) (*App, *ui.Tester) {
	t.Helper()
	a, tt, cn := filterApp(t)
	a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: cn.DefaultSchema})
	a.nav.expand(navNode{kind: nodeObject, conn: cn.Config.ID, schema: cn.DefaultSchema, name: "orders"})
	a.nav.expand(navNode{kind: nodeConn, conn: a.projects[0].Prefix + "archive"})
	testutil.WaitFor(t, tt, "the columns and the empty connection", func() bool {
		_, read := cn.Columns[connection.ObjectKey{Schema: cn.DefaultSchema, Name: "orders"}]
		return read && tt.HasText("No tables")
	})
	tt.Frame()
	return a, tt
}

// A heading of the database tree, a connection or a group as Tables, does
// not indent what it holds: a table's name starts a level after its
// connection's arrow, where it took three.
func TestHeadingsDoNotIndentWhatTheyHold(t *testing.T) {
	_, tt := headsApp(t)
	at := func(text string) float32 {
		t.Helper()
		r, ok := tt.Find(text)
		if !ok {
			t.Fatalf("%q does not show: %q", text, tt.Texts())
		}
		return r.X
	}
	group, table, column, note := at("Tables"), at("order_items"), at("id"), at("No tables")
	// The list's edge, the row's inset, an arrow and an icon, and no level.
	if table > 50 {
		t.Errorf("a table's name starts %v px into the sidebar under two headings, want 50 at most", table)
	}
	if table <= group {
		t.Errorf("a table's name starts at %v, its group's at %v: the table's icon has no room", table, group)
	}
	// A column is held by its table, which is no heading: a level in.
	if math.Abs(float64(column-group)) > 1 {
		t.Errorf("a column's name starts at %v, want under its table's icon, where the group's name starts, at %v", column, group)
	}
	// A note under a heading starts after the arrow's room, as a row there.
	if math.Abs(float64(note-group)) > 1 {
		t.Errorf("a connection's note starts at %v, want where a group's name does, at %v", note, group)
	}
}

// What a row tells of the headings it is under agrees with the rows above
// it, for every row but a note, which tells nothing.
func TestRowsKnowTheHeadingsAboveThem(t *testing.T) {
	a, _ := headsApp(t)
	rows, notes := 0, 0
	a.nav.eachRowStart(func(n navNode, depth int, at rowStart) {
		if n.kind == nodeInfo {
			notes++
			if got := a.nav.noteStart(n, depth); got != at {
				t.Errorf("the note %q at depth %d starts at %+v, want %+v", n.name, depth, got, at)
			}
			return
		}
		rows++
		if got := navHeads(n); got != at.heads {
			t.Errorf("%+v says it is under %d headings, and the rows above it say %d", n, got, at.heads)
		}
	})
	// Their heading, two connections, a group, three tables and a column;
	// one note.
	if rows != 8 || notes != 1 {
		t.Errorf("the tree has %d rows and %d notes, want 8 and 1", rows, notes)
	}
}

// A chosen group of the database tree shows as a chosen heading of the
// files' pane does, by its brighter name: the list's tint, made for an
// item, is not drawn behind small capitals.
func TestChosenGroupIsAsQuietAsAFilesHeading(t *testing.T) {
	a, tt := headsApp(t)
	row := func(text string) ui.Rect {
		t.Helper()
		r, ok := tt.Find(text)
		if !ok {
			t.Fatalf("%q does not show: %q", text, tt.Texts())
		}
		return r
	}
	// The color behind a row, in the room its name leaves at its end.
	behind := func(text string) any {
		r := row(text)
		return tt.Image().At(int(r.X+r.W)-10, int(r.Y+r.H/2))
	}
	choose := func(text string) {
		t.Helper()
		r := row(text)
		tt.Press(r.X+2, r.Y+r.H/2)
		tt.Release(r.X+2, r.Y+r.H/2)
		tt.Frame()
		tt.Advance(time.Second)
		tt.Frame()
	}
	plain := behind("Tables")
	choose("order_items")
	if behind("order_items") == plain {
		t.Fatal("a chosen table has no tint: the test reads the wrong place")
	}
	choose("Tables")
	if at, ok := a.nav.panes()[0].chosen(); !ok || at.kind != nodeFolder {
		t.Fatalf("the group is not the row chosen: %+v, %v", at, ok)
	}
	if got := behind("Tables"); got != plain {
		t.Errorf("behind the chosen group is %v, want the sidebar's own %v", got, plain)
	}
}

// The tree follows the files at once, however few they are, and opening
// its rows leaves the files where they are: they are not at the sidebar's
// foot under a long tree, nor moved by each table opened.
func TestTheTreeFollowsTheFiles(t *testing.T) {
	a, tt, cn := filterApp(t)
	tt.Frame()
	tt.Frame()
	y := func(text string) float32 {
		t.Helper()
		r, ok := tt.Find(text)
		if !ok {
			t.Fatalf("%q does not show: %q", text, tt.Texts())
		}
		return r.Y
	}
	queries := y("Queries")
	if box, _ := tt.Find("Filter the navigator"); queries-(box.Y+box.H) > navRowHeight {
		t.Errorf("Queries is %v under the filter's box, want right under it", queries-(box.Y+box.H))
	}
	// The files' last row, then the tree's first.
	if gap := y("Connections") - y("orders-by-day.sql"); gap > 2*navRowHeight {
		t.Errorf("Connections is %v under the files' last row, want within two rows", gap)
	}
	a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: cn.DefaultSchema})
	tt.Frame()
	tt.Frame()
	if !tt.HasText("order_items") {
		t.Fatalf("the tables do not show: %q", tt.Texts())
	}
	if got := y("Queries"); got != queries {
		t.Errorf("opening the tables moved Queries from %v to %v", queries, got)
	}
	// A file's heading closed, the tree comes up by its rows.
	conns := y("Connections")
	a.nav.files.tree.Open.Remove(navNode{kind: nodeQueries, projectDir: a.projects[0].Dir})
	tt.Frame()
	tt.Frame()
	if got := y("Connections"); conns-got != 2*navRowHeight {
		t.Errorf("Queries closed over two files, and Connections moved up %v, want two rows", conns-got)
	}
}

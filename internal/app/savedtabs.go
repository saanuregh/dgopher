package app

import (
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/redis"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The kinds of a connection's tabs a workspace keeps.
const (
	savedTable    = "table"
	savedDiagram  = "diagram"
	savedKeys     = "keys"
	savedActivity = "activity"
	savedUsers    = "users"
	savedSearch   = "search"
)

// savedTab is a tab of a connection kept from one run to the next: what
// it showed, not its state.
type savedTab struct {
	Kind string `json:"kind"`
	// Connection is the connection's ID; in workspace.json, without the
	// project's prefix.
	Connection string `json:"connection"`
	Database   string `json:"database,omitempty"`
	Schema     string `json:"schema,omitempty"`
	// Name, ObjectKind and Page are a table's.
	Name       string `json:"name,omitempty"`
	ObjectKind string `json:"objectKind,omitempty"`
	Page       int    `json:"page,omitempty"`
	Pinned     bool   `json:"pinned,omitempty"`
}

// savedTabOf is what keeps t, false for a tab that is not a connection's.
func savedTabOf(t widgets.Tab) (savedTab, bool) {
	switch t := t.(type) {
	case *waitingTab:
		s := t.saved
		s.Connection = t.conn.Config.ID // as it is now, after a change of ID
		return s, true
	case *dataview.TableTab:
		return savedTab{Kind: savedTable, Connection: t.Conn.Config.ID, Database: t.Database, Schema: t.Object.Schema,
			Name: t.Object.Name, ObjectKind: string(t.Object.Kind), Page: t.Page}, true
	case *dataview.ERTab:
		database, schema := t.Where()
		return savedTab{Kind: savedDiagram, Connection: t.Connection().Config.ID, Database: database, Schema: schema}, true
	case *redis.Tab:
		return savedTab{Kind: savedKeys, Connection: t.Connection().Config.ID}, true
	case *activityTab:
		return savedTab{Kind: savedActivity, Connection: t.conn.Config.ID}, true
	case *usersTab:
		return savedTab{Kind: savedUsers, Connection: t.conn.Config.ID}, true
	case *searchTab:
		return savedTab{Kind: savedSearch, Connection: t.conn.Config.ID, Database: t.database}, true
	}
	return savedTab{}, false
}

// same reports whether two saved tabs show the same thing, as the openers
// tell: whatever their page, pin or the kind of table.
func (s savedTab) same(o savedTab) bool {
	s.Page, s.Pinned, s.ObjectKind = 0, false, ""
	o.Page, o.Pinned, o.ObjectKind = 0, false, ""
	return s == o
}

// savedKind is what a kind of connection tab is: where it exists, how it
// opens, and how its waiting tab names and shows it.
type savedKind struct {
	on    func(db.Engine) bool
	open  func(a *App, cn *connection.Conn, s savedTab)
	icon  *ui.SVG
	title func(conn string, s savedTab) string
	// what names what the tab shows, as its waiting tab says.
	what func(s savedTab) string
}

var savedKinds = map[string]savedKind{
	savedTable: {
		on: db.Engine.IsSQL,
		open: func(a *App, cn *connection.Conn, s savedTab) {
			a.OpenTable(cn, s.Database, db.Object{Schema: s.Schema, Name: s.Name, Kind: db.ObjectKind(s.ObjectKind), Rows: -1, Bytes: -1}, s.Page)
		},
		icon:  widgets.IconTable,
		title: func(_ string, s savedTab) string { return s.Name },
		what: func(s savedTab) string {
			if s.Schema != "" {
				return s.Schema + "." + s.Name
			}
			return s.Name
		},
	},
	savedDiagram: {
		on:    db.Engine.IsSQL,
		open:  func(a *App, cn *connection.Conn, s savedTab) { dataview.OpenER(a, cn, s.Database, s.Schema) },
		icon:  widgets.IconSchema,
		title: func(_ string, s savedTab) string { return s.Schema + " · ER diagram" },
		what:  func(s savedTab) string { return "The ER diagram of " + s.Schema },
	},
	savedKeys: {
		on:    func(e db.Engine) bool { return e == db.Redis },
		open:  func(a *App, cn *connection.Conn, _ savedTab) { a.openRedis(cn) },
		icon:  widgets.IconKey,
		title: func(conn string, _ savedTab) string { return conn + " · keys" },
		what:  func(savedTab) string { return "The keys" },
	},
	savedActivity: {
		on:    func(e db.Engine) bool { return len(e.ActivityPages()) > 0 },
		open:  func(a *App, cn *connection.Conn, _ savedTab) { a.openActivity(cn) },
		icon:  widgets.IconClock,
		title: func(conn string, _ savedTab) string { return conn + " · activity" },
		what:  func(savedTab) string { return "The server's activity" },
	},
	savedUsers: {
		on:    usersSupported,
		open:  func(a *App, cn *connection.Conn, _ savedTab) { a.openUsers(cn) },
		icon:  widgets.IconUsers,
		title: func(conn string, _ savedTab) string { return conn + " · users" },
		what:  func(savedTab) string { return "The users" },
	},
	savedSearch: {
		on:   db.Engine.IsSQL,
		open: func(a *App, cn *connection.Conn, s savedTab) { a.openSearch(cn, s.Database) },
		icon: widgets.IconCode,
		title: func(conn string, s savedTab) string {
			if s.Database != "" {
				return conn + " · " + s.Database + " · search"
			}
			return conn + " · search"
		},
		what: func(savedTab) string { return "Search" },
	},
}

// availableOn reports whether cn's engine has the tab s.
func (s savedTab) availableOn(cn *connection.Conn) bool {
	k, ok := savedKinds[s.Kind]
	return ok && k.on(cn.Config.Engine)
}

// openSaved opens s on cn as the sidebar would; false when cn's engine
// lacks it.
func (a *App) openSaved(cn *connection.Conn, s savedTab) bool {
	if !s.availableOn(cn) {
		return false
	}
	savedKinds[s.Kind].open(a, cn, s)
	return true
}

// holds reports whether a window holds t.
func (a *App) holds(t widgets.Tab) bool {
	for _, o := range a.everyTab() {
		if o == t {
			return true
		}
	}
	return false
}

// waitingTab stands in for a connection's tab restored from the last run,
// until its connection opens: then the tab itself takes its place. It
// connects as the restored editors do, on its own only when the
// connection is set to auto-connect.
type waitingTab struct {
	a     *App
	conn  *connection.Conn
	saved savedTab
	// opening is set once the tab itself is on its way.
	opening bool
	// unavailable is set when the connection, changed since, no longer
	// has such a tab.
	unavailable bool
}

func (t *waitingTab) Title() string {
	return savedKinds[t.saved.Kind].title(t.conn.Config.Name, t.saved)
}

func (t *waitingTab) Icon() *ui.SVG { return savedKinds[t.saved.Kind].icon }

func (t *waitingTab) Connection() *connection.Conn { return t.conn }

func (t *waitingTab) Places() []connection.Place {
	return []connection.Place{t.conn.PlaceOf(t.saved.Database)}
}

// HoldsConnection is false: it has nothing open on its connection.
func (t *waitingTab) HoldsConnection(*connection.Conn) bool { return false }

func (t *waitingTab) CloseReason() string { return "" }

func (t *waitingTab) Close() {}

func (t *waitingTab) Shown() {
	if t.conn.Status == connection.StatusIdle && t.conn.Config.AutoConnect {
		t.a.ConnectInline(t.conn, func() { t.open() }) // not asked for: no dialog
	}
}

func (t *waitingTab) connect() {
	t.a.Connect(t.conn, func() { t.open() })
}

// open puts the tab itself in this one's place, once; this one closes
// when the tab was open already.
func (t *waitingTab) open() {
	if t.opening || t.unavailable {
		return
	}
	t.opening = true
	t.replace()
}

// replace opens the tab itself, unless this one was closed meanwhile.
func (t *waitingTab) replace() {
	if !t.a.holds(t) {
		return
	}
	if !t.a.openSaved(t.conn, t.saved) {
		t.opening, t.unavailable = false, true
		return
	}
	t.a.removeTab(t) // still there when the tab was open already, which shows instead
}

func (t *waitingTab) View(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	if t.conn.Status == connection.StatusConnected && !t.opening && !t.unavailable {
		// Connected elsewhere meanwhile, as from the sidebar: once.
		t.opening = true
		t.a.Post(t.replace)
	}
	if t.unavailable {
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, t.conn.Config.Name+" has no "+t.Title()+" now.").TextColor(pal.Muted)
		})
		return
	}
	ui.Column(c).Grow(1).Children(func() {
		connecting := t.conn.Status == connection.StatusConnecting || t.opening
		since, started := t.a.connectStarted(t.conn)
		// Its button is the tab's own, taking ↵, as a dialog's default does.
		asked := widgets.ConnectBar(c, t.conn, widgets.ConnectState{
			Connecting: connecting, Cancellable: started && !t.opening, Since: since, Default: true})
		switch {
		case asked == widgets.CancelAsked:
			t.a.CancelConnect(t.conn)
		case asked == widgets.ConnectDetails:
			t.a.ShowReport(t.a.connectionReport(t.conn, func(*connection.Conn) { t.connect() }))
		case asked == widgets.ConnectAsked || !connecting && t.a.KeysTo(t) && c.Shortcut(0, ui.KeyEnter):
			t.connect()
		}
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, savedKinds[t.saved.Kind].what(t.saved)+" will open once "+t.conn.Config.Name+" is connected.").TextColor(pal.Muted)
		})
	})
}

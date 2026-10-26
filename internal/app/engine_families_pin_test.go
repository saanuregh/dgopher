package app

// Characterization pins for the engine families: what each choice the
// app makes by the engine's family decides, for every engine and for an
// ID no build knows, and what each step of a connection's life does to a
// Redis connection and to a SQL one. When the code behind a choice moves,
// leave the pinned values alone.
//
// A site that opens the key browser is seen without a server: the
// connection is left connecting, so openRedis queues a waiter and opens
// no tab, while the SQL path opens an editor or the navigator node.

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/sshtunnel"
	"dgopher/internal/sshtunnel/sshtest"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// connectingConn adds a connection of engine e left connecting, which no
// attempt will finish: what connects it waits, and runs never.
func connectingConn(a *App, e db.Engine) *connection.Conn {
	cfg := db.Config{ID: "c-" + string(e), Name: "conn " + string(e), Engine: e, Host: "h"}
	if e.IsFile() {
		cfg.Host, cfg.Database = "", ":memory:"
	}
	cn := addConn(a, cfg)
	cn.Status = connection.StatusConnecting
	return cn
}

// openedOn says what opening cn did since tabs tabs were open: the tabs
// it added and the kind of the last, the waiters it queued, and whether
// its navigator node opened.
func openedOn(a *App, cn *connection.Conn, tabs int) string {
	kind := "none"
	if n := a.tabs().Len(); n > tabs {
		switch a.tabs().At(n - 1).(type) {
		case *query.Tab:
			kind = "editor"
		default:
			kind = fmt.Sprintf("%T", a.tabs().At(n-1))
		}
	}
	node := navNode{kind: nodeConn, conn: cn.Config.ID}
	return fmt.Sprintf("tabs+%d (%s) waiting=%d node open=%v", a.tabs().Len()-tabs, kind, len(cn.Waiters), a.nav.tree.Open.Has(node))
}

// The key browser opens instead of an editor for a key-value engine only
// (app.go NewQueryTab).
func TestPinNewQueryTabByEngine(t *testing.T) {
	pinValues(t, "new query tab", map[string]string{
		"postgres":   "tabs+1 (editor) waiting=1 node open=false",
		"mysql":      "tabs+1 (editor) waiting=1 node open=false",
		"clickhouse": "tabs+1 (editor) waiting=1 node open=false",
		"sqlite":     "tabs+1 (editor) waiting=1 node open=false",
		"duckdb":     "tabs+1 (editor) waiting=1 node open=false",
		"redis":      "tabs+0 (none) waiting=1 node open=false",
		"unknown":    "tabs+1 (editor) waiting=1 node open=false",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		for _, e := range pinEngines {
			cn := connectingConn(a, e)
			tabs := a.tabs().Len()
			a.NewQueryTab(cn, "", "")
			add(string(e), openedOn(a, cn, tabs))
		}
	})
}

// Activating a connection's node opens the key browser for Redis, and
// opens the node to connect it for the others (nav.go activate).
func TestPinActivateConnectionByEngine(t *testing.T) {
	pinValues(t, "activate connection", map[string]string{
		"postgres":   "tabs+0 (none) waiting=1 node open=true",
		"mysql":      "tabs+0 (none) waiting=1 node open=true",
		"clickhouse": "tabs+0 (none) waiting=1 node open=true",
		"sqlite":     "tabs+0 (none) waiting=1 node open=true",
		"duckdb":     "tabs+0 (none) waiting=1 node open=true",
		"redis":      "tabs+0 (none) waiting=1 node open=false",
		"unknown":    "tabs+0 (none) waiting=1 node open=true",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		for _, e := range pinEngines {
			cn := connectingConn(a, e)
			tabs := a.tabs().Len()
			a.activate(navNode{kind: nodeConn, conn: cn.Config.ID})
			add(string(e), openedOn(a, cn, tabs))
		}
	})
}

// A Redis connection's node has no children; the others' show their
// state (nav.go navChildren).
func TestPinConnectionNodeChildrenByEngine(t *testing.T) {
	pinValues(t, "connection node children", map[string]string{
		"postgres":   "[\"Connecting…\"]",
		"mysql":      "[\"Connecting…\"]",
		"clickhouse": "[\"Connecting…\"]",
		"sqlite":     "[\"Connecting…\"]",
		"duckdb":     "[\"Connecting…\"]",
		"redis":      "[]",
		"unknown":    "[\"Connecting…\"]",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		for _, e := range pinEngines {
			cn := connectingConn(a, e)
			node := navNode{kind: nodeConn, conn: cn.Config.ID}
			a.nav.tree.Open.Add(node)
			var names []string
			for _, n := range a.navChildren(node) {
				names = append(names, n.name)
			}
			add(string(e), fmt.Sprintf("%q", names))
		}
	})
}

// A connection's context menu offers the key browser for Redis and the
// SQL tools for the others; its Connect opens the key browser for Redis
// (nav.go navMenu).
func TestPinConnectionMenuByEngine(t *testing.T) {
	pinValues(t, "connection menu", map[string]string{
		"postgres menu":      "Connect | New SQL Editor | Run SQL File… | Search Objects… | Server Activity | Users and Privileges | Catalog Queries | - | Back Up… | Restore… | - | Edit Connection… | Duplicate | - | Delete Connection…",
		"postgres connect":   "tabs+0 (none) waiting=1 node open=true",
		"mysql menu":         "Connect | New SQL Editor | Run SQL File… | Search Objects… | Server Activity | Users and Privileges | Catalog Queries | - | Back Up… | Restore… | - | Edit Connection… | Duplicate | - | Delete Connection…",
		"mysql connect":      "tabs+0 (none) waiting=1 node open=true",
		"clickhouse menu":    "Connect | New SQL Editor | Run SQL File… | Search Objects… | Server Activity | Users and Privileges | Catalog Queries | - | Edit Connection… | Duplicate | - | Delete Connection…",
		"clickhouse connect": "tabs+0 (none) waiting=1 node open=true",
		"sqlite menu":        "Connect | New SQL Editor | Run SQL File… | Search Objects… | Catalog Queries | - | Back Up… | Restore… | - | Edit Connection… | Duplicate | - | Delete Connection…",
		"sqlite connect":     "tabs+0 (none) waiting=1 node open=true",
		"duckdb menu":        "Connect | New SQL Editor | Run SQL File… | Search Objects… | Catalog Queries | - | Back Up… | Restore… | - | Edit Connection… | Duplicate | - | Delete Connection…",
		"duckdb connect":     "tabs+0 (none) waiting=1 node open=true",
		"redis menu":         "Connect | Open Key Browser | Server Activity | - | Edit Connection… | Duplicate | - | Delete Connection…",
		"redis connect":      "tabs+0 (none) waiting=1 node open=false",
		"unknown menu":       "Connect | New SQL Editor | Run SQL File… | Search Objects… | Server Activity | Catalog Queries | - | Edit Connection… | Duplicate | - | Delete Connection…",
		"unknown connect":    "tabs+0 (none) waiting=1 node open=true",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			a := newTestApp(t)
			cn := connectingConn(a, e)
			tt := ui.NewTester(a.view, 1200, 800)
			tt.Frame()
			if err := tt.RightClick(cn.Config.Name); err != nil {
				t.Fatalf("%s: %v: %q", e, err, tt.Texts())
			}
			add(string(e)+" menu", strings.Join(tt.Menu(), " | "))
			tabs := a.tabs().Len()
			if err := tt.ChooseMenuItem("Connect"); err != nil {
				t.Fatalf("%s: %v", e, err)
			}
			add(string(e)+" connect", openedOn(a, cn, tabs))
		}
	})
}

// The palette's Open of a connection opens the key browser for Redis and
// an editor for the others (palette.go).
func TestPinPaletteOpenByEngine(t *testing.T) {
	pinValues(t, "palette open", map[string]string{
		"postgres":   "tabs+1 (editor) waiting=1 node open=false",
		"mysql":      "tabs+1 (editor) waiting=1 node open=false",
		"clickhouse": "tabs+1 (editor) waiting=1 node open=false",
		"sqlite":     "tabs+1 (editor) waiting=1 node open=false",
		"duckdb":     "tabs+1 (editor) waiting=1 node open=false",
		"redis":      "tabs+0 (none) waiting=1 node open=false",
		"unknown":    "tabs+1 (editor) waiting=1 node open=false",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		for _, e := range pinEngines {
			cn := connectingConn(a, e)
			i := slices.IndexFunc(a.paletteItems(false), func(it paletteItem) bool {
				return it.group == "Connection" && it.title == "Open "+cn.Config.Name
			})
			if i < 0 {
				t.Fatalf("%s: no palette item", e)
			}
			tabs := a.tabs().Len()
			a.paletteItems(false)[i].run()
			add(string(e), openedOn(a, cn, tabs))
		}
	})
}

// The welcome page's row of a connection opens the key browser for Redis
// and an editor for the others (layout.go welcome).
func TestPinWelcomeOpenByEngine(t *testing.T) {
	pinValues(t, "welcome open", map[string]string{
		"postgres":   "tabs+1 (editor) waiting=1 node open=false",
		"mysql":      "tabs+1 (editor) waiting=1 node open=false",
		"clickhouse": "tabs+1 (editor) waiting=1 node open=false",
		"sqlite":     "tabs+1 (editor) waiting=1 node open=false",
		"duckdb":     "tabs+1 (editor) waiting=1 node open=false",
		"redis":      "tabs+0 (none) waiting=1 node open=false",
		"unknown":    "tabs+1 (editor) waiting=1 node open=false",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			a := newTestApp(t)
			cn := connectingConn(a, e)
			tt := ui.NewTester(a.view, 1200, 800)
			tt.Frame()
			if err := tt.Click("Open " + cn.Config.Name); err != nil {
				t.Fatalf("%s: %v: %q", e, err, tt.Texts())
			}
			add(string(e), openedOn(a, cn, 0))
		}
	})
}

// Which saved tabs each engine has; the key browser's for Redis only
// (savedtabs.go savedKeys).
func TestPinSavedTabKindsByEngine(t *testing.T) {
	var kinds []string
	for k := range savedKinds {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	pinValues(t, "saved tab kinds", map[string]string{
		"postgres":   "activity diagram search table users",
		"mysql":      "activity diagram search table users",
		"clickhouse": "activity diagram search table users",
		"sqlite":     "diagram search table",
		"duckdb":     "diagram search table",
		"redis":      "activity keys",
		"unknown":    "diagram search table",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			var on []string
			for _, k := range kinds {
				if savedKinds[k].on(e) {
					on = append(on, k)
				}
			}
			add(string(e), strings.Join(on, " "))
		}
	})
}

// Stopping a row of the server's activity kills a Redis client, and
// cancels a query on the others (activity.go act). An engine without
// activity pages is given PostgreSQL's, to reach the choice.
func TestPinActivityStopByEngine(t *testing.T) {
	pinValues(t, "activity stop", map[string]string{
		"postgres":   "title=\"Cancel this query?\" preview=\"SELECT pg_cancel_backend(42)\"",
		"mysql":      "title=\"Cancel this query?\" preview=\"KILL QUERY 42\"",
		"clickhouse": "title=\"Cancel this query?\" preview=\"KILL QUERY WHERE query_id = 42 ASYNC\"",
		"sqlite":     "title=\"Cancel this query?\" preview=\"SELECT pg_cancel_backend(42)\"",
		"duckdb":     "title=\"Cancel this query?\" preview=\"SELECT pg_cancel_backend(42)\"",
		"redis":      "title=\"Disconnect client 42?\" preview=\"CLIENT KILL ID 42\"",
		"unknown":    "title=\"Cancel this query?\" preview=\"SELECT pg_cancel_backend(42)\"",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			a := newTestApp(t)
			cn := addConn(a, db.Config{ID: "c", Name: "c", Engine: e, Host: "h"})
			pages := e.ActivityPages()
			if len(pages) == 0 {
				pages = db.Postgres.ActivityPages()
			}
			at := &activityTab{a: a, conn: cn, pages: pages}
			at.act([]any{int64(42)}, false)
			if a.confirm == nil {
				add(string(e), fmt.Sprintf("no confirmation, alert %v", a.frontAlert() != nil))
				continue
			}
			add(string(e), fmt.Sprintf("title=%q preview=%q", a.confirm.Title, a.confirm.Preview))
		}
	})
}

// What the connection form keeps of a key separator and of the Redis
// topology, by engine and topology (connform.go config). The form knows
// no unknown engine: its label reads as PostgreSQL's.
func TestPinConnectionFormKeepsRedisFields(t *testing.T) {
	pinValues(t, "connection form redis fields", map[string]string{
		"postgres Single server sep \"/\"":   "engine=postgres separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"postgres Single server sep \":\"":   "engine=postgres separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"postgres Cluster sep \"/\"":         "engine=postgres separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"postgres Cluster sep \":\"":         "engine=postgres separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"postgres Sentinel sep \"/\"":        "engine=postgres separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"postgres Sentinel sep \":\"":        "engine=postgres separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"mysql Single server sep \"/\"":      "engine=mysql separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"mysql Single server sep \":\"":      "engine=mysql separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"mysql Cluster sep \"/\"":            "engine=mysql separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"mysql Cluster sep \":\"":            "engine=mysql separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"mysql Sentinel sep \"/\"":           "engine=mysql separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"mysql Sentinel sep \":\"":           "engine=mysql separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"clickhouse Single server sep \"/\"": "engine=clickhouse separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"clickhouse Single server sep \":\"": "engine=clickhouse separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"clickhouse Cluster sep \"/\"":       "engine=clickhouse separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"clickhouse Cluster sep \":\"":       "engine=clickhouse separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"clickhouse Sentinel sep \"/\"":      "engine=clickhouse separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"clickhouse Sentinel sep \":\"":      "engine=clickhouse separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"sqlite Single server sep \"/\"":     "engine=sqlite separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"sqlite Single server sep \":\"":     "engine=sqlite separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"sqlite Cluster sep \"/\"":           "engine=sqlite separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"sqlite Cluster sep \":\"":           "engine=sqlite separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"sqlite Sentinel sep \"/\"":          "engine=sqlite separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"sqlite Sentinel sep \":\"":          "engine=sqlite separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"duckdb Single server sep \"/\"":     "engine=duckdb separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"duckdb Single server sep \":\"":     "engine=duckdb separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"duckdb Cluster sep \"/\"":           "engine=duckdb separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"duckdb Cluster sep \":\"":           "engine=duckdb separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"duckdb Sentinel sep \"/\"":          "engine=duckdb separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"duckdb Sentinel sep \":\"":          "engine=duckdb separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"redis Single server sep \"/\"":      "engine=redis separator=\"/\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"redis Single server sep \":\"":      "engine=redis separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"redis Cluster sep \"/\"":            "engine=redis separator=\"/\" database=\"\" redis={Mode:cluster Nodes:h2:7000 Master: SentinelUser: SentinelPassword:}",
		"redis Cluster sep \":\"":            "engine=redis separator=\"\" database=\"\" redis={Mode:cluster Nodes:h2:7000 Master: SentinelUser: SentinelPassword:}",
		"redis Sentinel sep \"/\"":           "engine=redis separator=\"/\" database=\"3\" redis={Mode:sentinel Nodes:h2:7000 Master:m SentinelUser:su SentinelPassword:sp}",
		"redis Sentinel sep \":\"":           "engine=redis separator=\"\" database=\"3\" redis={Mode:sentinel Nodes:h2:7000 Master:m SentinelUser:su SentinelPassword:sp}",
		"unknown Single server sep \"/\"":    "engine=postgres separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"unknown Single server sep \":\"":    "engine=postgres separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"unknown Cluster sep \"/\"":          "engine=postgres separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"unknown Cluster sep \":\"":          "engine=postgres separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"unknown Sentinel sep \"/\"":         "engine=postgres separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
		"unknown Sentinel sep \":\"":         "engine=postgres separator=\"\" database=\"3\" redis={Mode: Nodes: Master: SentinelUser: SentinelPassword:}",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		for _, e := range pinEngines {
			for mode := range redisModes {
				for _, sep := range []string{"/", db.DefaultKeySeparator} {
					a.openConnForm(nil)
					f := a.connForm
					f.engine = e.Label()
					f.cfg.Name, f.cfg.Host, f.cfg.Database = "c", "h", "3"
					f.cfg.KeySeparator = sep
					f.redisMode = mode
					f.cfg.Redis = db.RedisConfig{Nodes: " h2:7000 ", Master: " m ", SentinelUser: "su", SentinelPassword: "sp"}
					cfg := f.config()
					add(fmt.Sprintf("%s %s sep %q", e, redisModeLabels[mode], sep),
						fmt.Sprintf("engine=%s separator=%q database=%q redis=%+v", cfg.Engine, cfg.KeySeparator, cfg.Database, cfg.Redis))
					a.connForm = nil
				}
			}
		}
	})
}

// The connection form shows the topology and key separator for Redis
// only, and the statement timeout for all but Redis (connform.go
// generalPage and optionsPage).
func TestPinConnectionFormRedisFieldsShown(t *testing.T) {
	labels := []string{"Topology", "Key separator", "Statement timeout"}
	pinValues(t, "connection form redis fields shown", map[string]string{
		"postgres":   "Statement timeout",
		"mysql":      "Statement timeout",
		"clickhouse": "Statement timeout",
		"sqlite":     "Statement timeout",
		"duckdb":     "Statement timeout",
		"redis":      "Topology, Key separator",
		"unknown":    "Statement timeout",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		tt := ui.NewTester(a.view, 1200, 1400)
		for _, e := range pinEngines {
			var shown []string
			for _, page := range []int{pageGeneral, pageOptions} {
				a.openConnForm(nil)
				a.connForm.engine = e.Label()
				a.connForm.page = page
				tt.Frame()
				for _, l := range labels {
					if tt.HasText(l) {
						shown = append(shown, l)
					}
				}
				a.connForm = nil
				tt.Frame()
			}
			add(string(e), strings.Join(shown, ", "))
		}
	})
}

// Test Connection opens each engine with its family's opener
// (connform.go runTest, db.OpenStore): a server engine without a host
// stops at Validate in its own opener, which another opener's engine check
// would stop first.
func TestPinConnectionTestOpensByEngine(t *testing.T) {
	pinValues(t, "connection test", map[string]string{
		"postgres":   "ok=false a host is required",
		"mysql":      "ok=false a host is required",
		"clickhouse": "ok=false a host is required",
		"sqlite":     "ok=true Connected to SQLite",
		"duckdb":     "ok=true Connected to DuckDB",
		"redis":      "ok=false a host is required",
		"unknown":    "ok=false unknown engine \"unknown\"; a host is required",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		tt := ui.NewTester(a.view, 1200, 800)
		for _, e := range pinEngines {
			a.openConnForm(nil)
			f := a.connForm
			cfg := db.Config{Name: "c", Engine: e}
			if e.IsFile() {
				cfg.Database = ":memory:"
			}
			a.runTest(f, cfg)
			testutil.WaitFor(t, tt, "the test", func() bool { return !f.testing })
			result := f.testResult
			if f.testOK {
				// Without the version and the time taken.
				result = strings.Fields(result)[0] + " " + strings.Fields(result)[1] + " " + strings.Fields(result)[2]
			}
			add(string(e), fmt.Sprintf("ok=%v %s", f.testOK, result))
			a.connForm = nil
		}
	})
}

// Where a connection goes, and whether that is another machine: a Redis
// connection's other nodes count, others' do not (connform.go destination
// and remote).
func TestPinDestinationByEngine(t *testing.T) {
	pinValues(t, "destination", map[string]string{
		"postgres":   "destination=\"127.0.0.1:7000\" remote=false",
		"mysql":      "destination=\"127.0.0.1:7000\" remote=false",
		"clickhouse": "destination=\"127.0.0.1:7000\" remote=false",
		"sqlite":     "destination=\"127.0.0.1:7000\" remote=false",
		"duckdb":     "destination=\"127.0.0.1:7000\" remote=false",
		"redis":      "destination=\"127.0.0.1:7000, far.example.com:7001\" remote=true",
		"unknown":    "destination=\"127.0.0.1:7000\" remote=false",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			cfg := db.Config{Engine: e, Host: "127.0.0.1", Port: 7000, Redis: db.RedisConfig{Nodes: " far.example.com:7001 "}}
			add(string(e), fmt.Sprintf("destination=%q remote=%v", destination(cfg), remote(&cfg)))
		}
	})
}

// Connecting opens each engine with its family's opener (app.go open,
// db.OpenStore): a server engine without a host stops at Validate in its
// own opener, which another opener's engine check would stop first. The
// connect is audited either way.
func TestPinConnectOpensByEngine(t *testing.T) {
	pinValues(t, "connect", map[string]string{
		"postgres":   "status=failed err=\"a host is required\" sql=false redis=false audited=true",
		"mysql":      "status=failed err=\"a host is required\" sql=false redis=false audited=true",
		"clickhouse": "status=failed err=\"a host is required\" sql=false redis=false audited=true",
		"sqlite":     "status=connected err=\"\" sql=true redis=false audited=true",
		"duckdb":     "status=connected err=\"\" sql=true redis=false audited=true",
		"redis":      "status=failed err=\"a host is required\" sql=false redis=false audited=true",
		"unknown":    "status=failed err=\"unknown engine \\\"unknown\\\"; a host is required\" sql=false redis=false audited=true",
	}, func(add func(key, got string)) {
		a := newTestApp(t)
		tt := ui.NewTester(a.view, 1200, 800)
		for _, e := range pinEngines {
			cfg := db.Config{ID: "o-" + string(e), Name: "open " + string(e), Engine: e}
			if e.IsFile() {
				cfg.Database = ":memory:"
			}
			cn := addConn(a, cfg)
			a.open(cn, cn.Config, false)
			testutil.WaitFor(t, tt, "the connect", func() bool { return cn.Status != connection.StatusConnecting })
			audited := slices.ContainsFunc(auditEvents(t, a), func(ev audit.Event) bool {
				return ev.Kind == audit.KindConnect && ev.ConnectionID == cn.Config.ID
			})
			add(string(e), fmt.Sprintf("status=%s err=%q sql=%v redis=%v audited=%v", statusName(cn.Status), cn.Err, cn.DB() != nil, cn.KV() != nil, audited))
			a.closeConnection(cn)
		}
	})
}

// statusName names a connection's status.
func statusName(s connection.Status) string {
	switch s {
	case connection.StatusIdle:
		return "idle"
	case connection.StatusConnecting:
		return "connecting"
	case connection.StatusConnected:
		return "connected"
	case connection.StatusFailed:
		return "failed"
	}
	return fmt.Sprint(int(s))
}

// liteConn adds a connection to a new SQLite file of the test, connected.
func liteConn(t *testing.T, a *App, tt *ui.Tester) (*connection.Conn, string) {
	t.Helper()
	file := writeSQL(t, "families.sqlite", "")
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	connectNow(t, a, tt, cn)
	return cn, file
}

// lastEvent is the newest audit event of kind for cn, nil for none.
func lastEvent(t *testing.T, a *App, cn *connection.Conn, kind string) *audit.Event {
	t.Helper()
	for _, e := range auditEvents(t, a) {
		if e.Kind == kind && e.ConnectionID == cn.Config.ID {
			return &e
		}
	}
	return nil
}

// countEvents counts the audit events of kind for cn.
func countEvents(t *testing.T, a *App, cn *connection.Conn, kind string) int {
	t.Helper()
	n := 0
	for _, e := range auditEvents(t, a) {
		if e.Kind == kind && e.ConnectionID == cn.Config.ID {
			n++
		}
	}
	return n
}

// sqlClosed reports whether a pool was closed.
func sqlClosed(d *db.DB) bool { return d.SQL.Ping() != nil }

// The connect of a SQLite file is audited with the file and the version,
// and no TLS state; the pool is the connection's, and its schemas load.
func TestPinSQLConnectState(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	cn, file := liteConn(t, a, tt)
	ev := lastEvent(t, a, cn, audit.KindConnect)
	if cn.Version == "" || ev == nil || ev.Detail != "file "+file+", server "+widgets.FirstLine(cn.Version) || ev.Error != "" {
		t.Fatalf("version %q, connect event %+v", cn.Version, ev)
	}
	if cn.DB() == nil || cn.KV() != nil || cn.HeldAt.IsZero() || tlsStateOf(cn.Store) != "" {
		t.Fatalf("db %v kv %v held at %v tls %q", cn.DB() != nil, cn.KV() != nil, cn.HeldAt, tlsStateOf(cn.Store))
	}
	testutil.WaitFor(t, tt, "the schemas", func() bool { return len(cn.Schemas[""]) > 0 })
}

// A disconnect audits once, with no detail, clears the connection and
// closes its pool; a connection never connected audits no disconnect.
func TestPinSQLDisconnect(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	cn, _ := liteConn(t, a, tt)
	pool, gen := cn.DB(), cn.Generation
	a.disconnect(cn)
	if cn.DB() != nil || cn.KV() != nil || cn.Status != connection.StatusIdle || cn.Err != "" || cn.Generation != gen+1 {
		t.Fatalf("after the disconnect: db %v kv %v status %s err %q generation %d", cn.DB() != nil, cn.KV() != nil, statusName(cn.Status), cn.Err, cn.Generation)
	}
	ev := lastEvent(t, a, cn, audit.KindDisconnect)
	if ev == nil || ev.Detail != "" || countEvents(t, a, cn, audit.KindDisconnect) != 1 {
		t.Fatalf("disconnect events: %+v", auditEvents(t, a))
	}
	testutil.WaitFor(t, tt, "the pool to close", func() bool { return sqlClosed(pool) })
	a.disconnect(cn)
	never := addConn(a, db.Config{ID: "never", Name: "never", Engine: db.SQLite, Database: ":memory:"})
	a.disconnect(never)
	if countEvents(t, a, cn, audit.KindDisconnect) != 1 || countEvents(t, a, never, audit.KindDisconnect) != 0 {
		t.Fatalf("a disconnect without a pool audited: %+v", auditEvents(t, a))
	}
}

// Idle time counts from the pool's last use when it is later than the
// last hold: a SQL connection held long ago but used since stays.
func TestPinSQLIdleCountsPoolUse(t *testing.T) {
	idleAfter(t, time.Hour)
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	cn, _ := liteConn(t, a, tt)
	cn.HeldAt = time.Now().Add(-2 * time.Hour)
	for range 3 {
		tt.Frame()
	}
	if cn.Status != connection.StatusConnected {
		t.Fatalf("disconnected though its pool was used since: %v", cn.Status)
	}
}

// Quitting closes each connection's pool.
func TestPinSQLShutdownCloses(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	cn, _ := liteConn(t, a, tt)
	pool := cn.DB()
	a.shutdown()
	if !sqlClosed(pool) {
		t.Fatal("the pool is open after the quit")
	}
}

// waitPosted waits, drawing no frame of the app, until background work
// has posted its result, which stays unapplied until the app draws.
func waitPosted(t *testing.T, a *App) {
	t.Helper()
	blank := ui.NewTester(func(*ui.Context) {}, 10, 10)
	testutil.WaitFor(t, blank, "the result to be posted", func() bool {
		a.queueMu.Lock()
		defer a.queueMu.Unlock()
		return len(a.queue) > 0
	})
}

// settle draws the app until its background work is done and applied.
func settle(t *testing.T, a *App, tt *ui.Tester) {
	t.Helper()
	testutil.WaitFor(t, tt, "the background work", func() bool { return a.working.Load() == 0 })
}

// A SQL connect whose result comes after a disconnect is not applied,
// nor audited.
func TestPinSQLLateConnectNotAudited(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	cn := addConn(a, db.Config{ID: "late", Name: "late", Engine: db.SQLite, Database: writeSQL(t, "late.sqlite", "")})
	settle(t, a, tt)
	a.open(cn, cn.Config, false)
	waitPosted(t, a)
	if cn.Status != connection.StatusConnecting {
		t.Fatalf("the connect was applied before the disconnect: %s", statusName(cn.Status))
	}
	a.disconnect(cn)
	settle(t, a, tt)
	if cn.Status != connection.StatusIdle || cn.DB() != nil || cn.KV() != nil || countEvents(t, a, cn, audit.KindConnect) != 0 {
		t.Fatalf("a late connect was applied: status %v, db %v, events %+v", cn.Status, cn.DB() != nil, auditEvents(t, a))
	}
}

// redisConfig is the Redis test server's, from compose.yaml.
func redisConfig() db.Config {
	return db.Config{ID: "rd", Name: "rd", Engine: db.Redis, Host: "127.0.0.1", Port: 16379}
}

// redisConn adds a connection to the Redis test server, connected.
func redisConn(t *testing.T, a *App, tt *ui.Tester) *connection.Conn {
	t.Helper()
	cn := addConn(a, redisConfig())
	connectNow(t, a, tt, cn)
	return cn
}

// kvClosed reports whether a Redis client was closed: it sends nothing
// more.
func kvClosed(kv *db.KV) bool {
	_, err := kv.Do(context.Background(), []string{"PING"})
	return err != nil
}

// The connect of a Redis server is audited with its address, the
// version and the TLS state; the client is the connection's, and no
// schema is read.
func TestPinIntegrationRedisConnectState(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	cn := redisConn(t, a, tt)
	ev := lastEvent(t, a, cn, audit.KindConnect)
	want := "@127.0.0.1:16379, tls disable, server " + widgets.FirstLine(cn.Version) + ", no TLS"
	if cn.Version == "" || ev == nil || ev.Detail != want || ev.Error != "" {
		t.Fatalf("version %q, connect event %+v, want detail %q", cn.Version, ev, want)
	}
	if cn.KV() == nil || cn.DB() != nil || cn.HeldAt.IsZero() || tlsStateOf(cn.Store) != "no TLS" || cn.KV().RouteDone() != nil {
		t.Fatalf("kv %v db %v held at %v tls %q", cn.KV() != nil, cn.DB() != nil, cn.HeldAt, tlsStateOf(cn.Store))
	}
	settle(t, a, tt)
	if len(cn.Schemas) != 0 || len(cn.Loading) != 0 {
		t.Fatalf("schemas read for Redis: %v, loading %v", cn.Schemas, cn.Loading)
	}
}

// A Redis disconnect audits once, with no detail, clears the connection
// and closes its client.
func TestPinIntegrationRedisDisconnect(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	cn := redisConn(t, a, tt)
	kv, gen := cn.KV(), cn.Generation
	a.disconnect(cn)
	if cn.DB() != nil || cn.KV() != nil || cn.Status != connection.StatusIdle || cn.Err != "" || cn.Generation != gen+1 {
		t.Fatalf("after the disconnect: db %v kv %v status %s err %q generation %d", cn.DB() != nil, cn.KV() != nil, statusName(cn.Status), cn.Err, cn.Generation)
	}
	ev := lastEvent(t, a, cn, audit.KindDisconnect)
	if ev == nil || ev.Detail != "" || countEvents(t, a, cn, audit.KindDisconnect) != 1 {
		t.Fatalf("disconnect events: %+v", auditEvents(t, a))
	}
	testutil.WaitFor(t, tt, "the client to close", func() bool { return kvClosed(kv) })
}

// A Redis connection's idle time counts from its last hold only: a
// command sent just now does not keep it, as a SQL pool's use does.
// Disconnected, it is audited and told, and its client closes.
func TestPinIntegrationRedisIdleCountsHoldOnly(t *testing.T) {
	testutil.Integration(t)
	idleAfter(t, time.Hour)
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	cn := redisConn(t, a, tt)
	kv := cn.KV()
	cn.HeldAt = time.Now().Add(-2 * time.Hour)
	if _, err := kv.Do(context.Background(), []string{"PING"}); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if cn.Status != connection.StatusIdle || cn.KV() != nil {
		t.Fatalf("still connected: %v", cn.Status)
	}
	ev := lastEvent(t, a, cn, audit.KindDisconnect)
	if ev == nil || ev.Detail != "idle for 2 hours" {
		t.Fatalf("disconnect event %+v", ev)
	}
	testutil.WaitFor(t, tt, "the notice", func() bool { return tt.HasText("Disconnected rd after 2 hours idle") })
	testutil.WaitFor(t, tt, "the client to close", func() bool { return kvClosed(kv) })
}

// A Redis connection through an SSH server fails when the server goes:
// audited as the tunnel closing, cleared, its client closed, and the
// user offered to connect again.
func TestPinIntegrationRedisTunnelLoss(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	s := sshtest.Start(t, "secret", nil)
	a.appKnownHosts = filepath.Join(t.TempDir(), "known_hosts")
	if err := sshtunnel.Trust(a.appKnownHosts, s.Addr, s.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	cfg := redisConfig()
	cfg.SSH = db.SSHConfig{Enabled: true, Host: s.Host, Port: s.Port, User: "tester", Password: "secret"}
	cn := addConn(a, cfg)
	tt := ui.NewTester(a.view, 1000, 700)
	connectNow(t, a, tt, cn)
	kv := cn.KV()
	if kv == nil || kv.RouteDone() == nil {
		t.Fatal("connected without the tunnel")
	}
	notesBefore := len(a.notes)
	s.Stop()
	testutil.WaitFor(t, tt, "the connection to fail", func() bool { return cn.Status == connection.StatusFailed })
	if cn.ErrKind != db.ProblemTunnelClosed || !strings.Contains(cn.Err, "SSH tunnel closed") || cn.KV() != nil || cn.DB() != nil {
		t.Fatalf("kind %v, err %q, kv %v", cn.ErrKind, cn.Err, cn.KV() != nil)
	}
	ev := lastEvent(t, a, cn, audit.KindDisconnect)
	if ev == nil || ev.Detail != "SSH tunnel closed" {
		t.Fatalf("disconnect event %+v", ev)
	}
	added := a.notes[notesBefore:]
	if len(added) != 1 || added[0].title != "Lost rd" {
		t.Fatalf("notes added: %+v", added)
	}
	testutil.WaitFor(t, tt, "the client to close", func() bool { return kvClosed(kv) })
}

// A Redis connection renamed while connected sends under its new name.
func TestPinIntegrationRedisTrafficFollowsRename(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	cn := redisConn(t, a, tt)
	cn.Config.Name, cn.Config.Env = "ledger", db.Production
	relabel(cn)
	if _, err := cn.KV().Do(context.Background(), []string{"ECHO", "families-relabel"}); err != nil {
		t.Fatal(err)
	}
	var sent *audit.Event
	testutil.WaitFor(t, tt, "the traffic", func() bool {
		events, _ := a.projects[0].Audit.Read(0, true)
		for _, e := range events {
			if e.Kind == audit.KindTraffic && strings.Contains(e.Statement, "families-relabel") {
				sent = &e
				return true
			}
		}
		return false
	})
	if sent.Connection != "ledger" || sent.Environment != string(db.Production) {
		t.Fatalf("%+v", sent)
	}
}

// Quitting closes a Redis connection's client.
func TestPinIntegrationRedisShutdownCloses(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	cn := redisConn(t, a, tt)
	kv := cn.KV()
	a.shutdown()
	if !kvClosed(kv) {
		t.Fatal("the client is open after the quit")
	}
}

// redisClients counts the clients the Redis test server holds that DGopher
// named, through a client of its own named apart.
func redisClients(t *testing.T) func() int {
	t.Helper()
	ctx := context.Background()
	probe, err := db.OpenRedis(ctx, redisConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { probe.Close() })
	if _, err := probe.Do(ctx, []string{"CLIENT", "SETNAME", "families-probe"}); err != nil {
		t.Fatal(err)
	}
	return func() int {
		list, err := probe.Do(ctx, []string{"CLIENT", "LIST"})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(fmt.Sprint(list), " name=dgopher ")
	}
}

// A Redis connect whose result comes after a disconnect is not applied
// nor audited, and the client it opened closes.
func TestPinIntegrationRedisLateConnectDropped(t *testing.T) {
	testutil.Integration(t)
	clients := redisClients(t)
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	cn := addConn(a, redisConfig())
	settle(t, a, tt)
	before := clients()
	a.open(cn, cn.Config, false)
	waitPosted(t, a)
	if n := clients(); n != before+1 {
		t.Fatalf("the connect holds %d clients, %d before", n, before)
	}
	a.disconnect(cn)
	settle(t, a, tt)
	if cn.Status != connection.StatusIdle || cn.DB() != nil || cn.KV() != nil || countEvents(t, a, cn, audit.KindConnect) != 0 {
		t.Fatalf("a late connect was applied: status %v, kv %v, events %+v", cn.Status, cn.KV() != nil, auditEvents(t, a))
	}
	testutil.WaitFor(t, tt, "the late client to close", func() bool { return clients() <= before })
}

// Test Connection reaches a Redis server through OpenRedis.
func TestPinIntegrationRedisConnectionTest(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 800)
	a.openConnForm(nil)
	f := a.connForm
	a.runTest(f, redisConfig())
	testutil.WaitFor(t, tt, "the test", func() bool { return !f.testing })
	if !f.testOK || !strings.HasPrefix(f.testResult, "Connected to Redis ") {
		t.Fatalf("ok %v: %q", f.testOK, f.testResult)
	}
}

package app

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/testutil/mongoserver"
	"dgopher/internal/ui/mongo"

	"github.com/egoist/mygo/ui"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// The MongoDB tabs are kept only for a document engine.
func TestMongoSavedKindsOnlyOnDocuments(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	for _, kind := range []string{savedCollection, savedConsole} {
		if a.openSaved(cn, savedTab{Kind: kind, Connection: cn.Config.ID, Database: "shop", Name: "orders"}) {
			t.Errorf("%s opened on SQLite", kind)
		}
	}
}

// A collection's tab is kept with its filter, and a console with its
// database; each opens again from what was kept, from the workspace and
// after it was closed.
func TestMongoSavedTabsRestore(t *testing.T) {
	cfg, err := db.ParseURL(mongoserver.Primary(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ID, cfg.Name = "mg", "catalog"
	cfg.Port = max(cfg.Port, cfg.DefaultPort())
	c, err := net.DialTimeout("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), time.Second)
	if err != nil {
		t.Skipf("no MongoDB server at %s:%d", cfg.Host, cfg.Port)
	}
	c.Close()
	const database = "dgopher_app_saved_tabs"
	d, err := db.OpenMongo(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	defer d.Client.Database(database).Drop(context.Background())
	if _, err := d.Client.Database(database).Collection("orders").InsertOne(context.Background(), bson.D{{Key: "status", Value: "paid"}}); err != nil {
		t.Fatal(err)
	}

	a := newTestApp(t)
	cn := addConn(a, cfg)
	tt := ui.NewTester(a.view, 1200, 760)
	for _, s := range []savedTab{
		{Kind: savedCollection, Connection: cn.Config.ID, Database: database, Name: "orders", Filter: `{ status: "paid" }`},
		{Kind: savedConsole, Connection: cn.Config.ID, Database: database},
	} {
		if !a.openSaved(cn, s) {
			t.Fatalf("%s refused", s.Kind)
		}
		testutil.WaitFor(t, tt, s.Kind+" to open", func() bool {
			ct, ok := a.ActiveTab().(*mongo.CollectionTab)
			if s.Kind == savedCollection {
				return ok && !ct.Busy() && ct.Filter() == s.Filter
			}
			_, ok = a.ActiveTab().(*mongo.ConsoleTab)
			return ok
		})
		got, ok := savedTabOf(a.ActiveTab())
		if !ok || got != s {
			t.Fatalf("%s kept as %+v, %v", s.Kind, got, ok)
		}
		// Closed, it opens again as it was.
		reopen := a.reopener(a.ActiveTab())
		if reopen == nil {
			t.Fatalf("%s does not reopen", s.Kind)
		}
		a.closeTabOf(a.ActiveTab())
		tt.Frame()
		if why := reopen(); why != "" {
			t.Fatal(why)
		}
		testutil.WaitFor(t, tt, s.Kind+" to open again", func() bool {
			got, ok := savedTabOf(a.ActiveTab())
			return ok && got == s
		})
	}
}

// A collection's filter that holds a secret is not kept, read as the
// console's parser reads it: a quote inside a regular expression starts
// no string that would hide the password after it.
func TestKeptMongoFilter(t *testing.T) {
	for filter, want := range map[string]string{
		`{ status: "paid" }`:                        `{ status: "paid" }`,
		`{ name: /O'Brien/ }`:                       `{ name: /O'Brien/ }`,
		`{ name: /O'Brien/, password: "s3cretpw" }`: "",
		`{ note: "it's", password: "s3cretpw" }`:    "",
		`{ uri: "mongodb://u:s3cretpw@h/db" }`:      "",
	} {
		if got := keptMongoFilter(filter); got != want {
			t.Errorf("%s kept as %q, want %q", filter, got, want)
		}
	}
}

package app

import (
	"context"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/testutil/mongoserver"
	"dgopher/internal/ui/mongo"

	"github.com/egoist/mygo/ui"
	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// navTexts are the window's texts, in order, joined by |.
func navTexts(tt *ui.Tester) string { return strings.Join(tt.Texts(), "|") }

// A document connection lists its databases with their size, each its
// Collections and Views with their counts, a time series with its note,
// and a collection its indexes; a collection opens its documents. Read
// from a catalog the test gives, as a connection that read it would hold.
func TestNavigatorListsDocumentCatalog(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "mg", Name: "catalog", Engine: db.MongoDB, Host: "127.0.0.1", Port: 1})
	cn.Status = connection.StatusConnected
	cn.HeldAt = time.Now().Add(time.Hour) // connected without a server: not idle
	one := int32(1)
	cat := a.docCatalogOf(cn)
	cat.databases, cat.dbsRead = []db.DocDatabase{{Name: "admin", SizeOnDisk: 40960}, {Name: "shop", SizeOnDisk: 412 << 20}, {Name: "spare", Empty: true}}, true
	cat.colls["shop"] = []db.DocCollection{
		{Name: "customers", Type: "collection"}, {Name: "metrics", Type: "timeseries"}, {Name: "orders", Type: "collection"},
		{Name: "paid_orders", Type: "view", ViewOn: "orders"},
	}
	cat.indexes[docKey{"shop", "orders"}] = []db.DocIndex{
		{Name: "_id_", Keys: bson.D{{Key: "_id", Value: int32(1)}}, Unique: true},
		{Name: "status_1_createdAt_-1", Keys: bson.D{{Key: "status", Value: int32(1)}, {Key: "createdAt", Value: int32(-1)}}, ExpireAfter: &one},
	}
	tt := ui.NewTester(a.view, 1200, 760)
	conn := navNode{kind: nodeConn, conn: cn.Config.ID}
	shop := navNode{kind: nodeDocDatabase, conn: cn.Config.ID, database: "shop"}
	colls := navNode{kind: nodeDocFolder, conn: cn.Config.ID, database: "shop", folder: docCollections}
	orders := navNode{kind: nodeDocCollection, conn: cn.Config.ID, database: "shop", name: "orders"}
	a.nav.expand(conn)
	tt.Frame()
	if got := navTexts(tt); !strings.Contains(got, "admin|Open Console on admin|40.0 KB|shop|Open Console on shop|412.0 MB|spare|Open Console on spare|empty") {
		t.Fatalf("databases: %s", got)
	}
	if slices.Contains(tt.Texts(), "customers") {
		t.Fatal("the collections show under a closed database")
	}
	a.nav.expand(shop)
	tt.Frame()
	if got := navTexts(tt); !strings.Contains(got, "Collections|3|Views|1") {
		t.Fatalf("folders: %s", got)
	}
	a.nav.expand(colls)
	a.nav.expand(navNode{kind: nodeDocFolder, conn: cn.Config.ID, database: "shop", folder: docViews})
	a.nav.expand(orders)
	tt.Frame()
	got := navTexts(tt)
	for _, want := range []string{"customers|metrics|time series|orders|_id_|unique|status_1_createdAt_-1|Views|1|paid_orders|on orders"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tree %s, want %s", got, want)
		}
	}
	if ix, _ := a.docIndex(cn, navNode{kind: nodeDocIndex, conn: cn.Config.ID, database: "shop", name: "orders\x00status_1_createdAt_-1"}); docIndexDetail(ix) != "{ status: 1, createdAt: -1 }, expires after 1s" {
		t.Errorf("index detail %q", docIndexDetail(ix))
	}
	// An index is a leaf; a view has no indexes to open.
	if a.navChildren(navNode{kind: nodeDocCollection, conn: cn.Config.ID, database: "shop", name: "paid_orders"}) != nil {
		t.Error("a view has children")
	}
	a.activate(orders)
	tt.Frame()
	if ct, ok := a.ActiveTab().(*mongo.CollectionTab); !ok || ct.Title() != "shop.orders" {
		t.Fatalf("activating orders opened %T", a.ActiveTab())
	}
	// A failure says so under its node, and Refresh forgets it.
	cat.errs[docKey{database: "spare"}] = "not authorized on spare"
	spare := navNode{kind: nodeDocDatabase, conn: cn.Config.ID, database: "spare"}
	if kids := a.navChildren(spare); len(kids) != 1 || kids[0].name != "Failed: not authorized on spare" {
		t.Fatalf("failure: %+v", kids)
	}
	a.refresh(cn)
	if a.docCatalogOf(cn).dbsRead {
		t.Error("Refresh kept the catalog")
	}
}

// Refresh during a read drops what that read returns, and a read started
// after it is kept: what was read before the refresh may be stale. The
// reads are the test's, so no server is needed.
func TestNavigatorDocRefreshDropsReadsStartedBefore(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "mg", Name: "catalog", Engine: db.MongoDB, Host: "127.0.0.1", Port: 1})
	cn.Status = connection.StatusConnected
	cn.HeldAt = time.Now().Add(time.Hour)
	cn.Store = &db.Doc{}                 // never sent anything: the reads below are the test's
	t.Cleanup(func() { cn.Store = nil }) // before the app's shutdown, which would close it
	tt := ui.NewTester(a.view, 1200, 760)
	shop := navNode{kind: nodeDocDatabase, conn: cn.Config.ID, database: "shop"}
	orders := navNode{kind: nodeDocCollection, conn: cn.Config.ID, database: "shop", name: "orders"}
	read := func(release chan struct{}, colls []db.DocCollection, idx []db.DocIndex) func(context.Context, *db.Doc) (func(*docCatalog), error) {
		return func(context.Context, *db.Doc) (func(*docCatalog), error) {
			<-release
			return func(cat *docCatalog) {
				if colls != nil {
					cat.colls["shop"] = colls
				} else {
					cat.indexes[docKey{"shop", "orders"}] = idx
				}
			}, nil
		}
	}
	for _, c := range []struct {
		name     string
		key      docKey
		refresh  navNode
		old, new func(chan struct{}) func(context.Context, *db.Doc) (func(*docCatalog), error)
		kept     func(*docCatalog) string
	}{
		{"collections", docKey{database: "shop"}, shop,
			func(r chan struct{}) func(context.Context, *db.Doc) (func(*docCatalog), error) {
				return read(r, []db.DocCollection{{Name: "before"}}, nil)
			},
			func(r chan struct{}) func(context.Context, *db.Doc) (func(*docCatalog), error) {
				return read(r, []db.DocCollection{{Name: "after"}}, nil)
			},
			func(cat *docCatalog) string {
				if colls, ok := cat.colls["shop"]; ok && len(colls) == 1 {
					return colls[0].Name
				}
				return ""
			}},
		{"indexes", docKey{"shop", "orders"}, orders,
			func(r chan struct{}) func(context.Context, *db.Doc) (func(*docCatalog), error) {
				return read(r, nil, []db.DocIndex{{Name: "before"}})
			},
			func(r chan struct{}) func(context.Context, *db.Doc) (func(*docCatalog), error) {
				return read(r, nil, []db.DocIndex{{Name: "after"}})
			},
			func(cat *docCatalog) string {
				if idx, ok := cat.indexes[docKey{"shop", "orders"}]; ok && len(idx) == 1 {
					return idx[0].Name
				}
				return ""
			}},
	} {
		before, after := make(chan struct{}), make(chan struct{})
		a.loadDoc(cn, c.key, c.old(before))
		a.refreshDocNode(cn, c.refresh)
		a.loadDoc(cn, c.key, c.new(after))
		cat := a.docCatalogOf(cn)
		if !cat.loading[c.key] {
			t.Fatalf("%s: no read started after the refresh", c.name)
		}
		close(before)
		testutil.WaitFor(t, tt, c.name+": the read from before the refresh", func() bool { return a.working.Load() == 1 })
		if got := c.kept(cat); got != "" || !cat.loading[c.key] {
			t.Fatalf("%s: kept %q from before the refresh; loading %v", c.name, got, cat.loading[c.key])
		}
		close(after)
		testutil.WaitFor(t, tt, c.name+": the read from after the refresh", func() bool { return a.working.Load() == 0 })
		if got := c.kept(cat); got != "after" || cat.loading[c.key] {
			t.Fatalf("%s: kept %q, loading %v; want the read from after the refresh", c.name, got, cat.loading[c.key])
		}
	}
}

// Against a server: the databases, collections and indexes are read as
// their nodes open; the context menu's console opens on the database.
func TestMongoNavigatorFromServer(t *testing.T) {
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
	const database = "dgopher_app_navigator"
	ctx := context.Background()
	d, err := db.OpenMongo(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	dbase := d.Client.Database(database)
	dbase.Drop(ctx)
	defer dbase.Drop(context.Background())
	if _, err := dbase.Collection("orders").InsertOne(ctx, bson.D{{Key: "status", Value: "paid"}, {Key: "at", Value: bson.NewDateTimeFromTime(time.Now())}}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbase.Collection("orders").Indexes().CreateOne(ctx, mongodriver.IndexModel{Keys: bson.D{{Key: "status", Value: 1}, {Key: "at", Value: -1}}}); err != nil {
		t.Fatal(err)
	}
	if err := dbase.CreateCollection(ctx, "metrics", options.CreateCollection().SetTimeSeriesOptions(options.TimeSeries().SetTimeField("at"))); err != nil {
		t.Fatal(err)
	}
	if err := dbase.CreateView(ctx, "paid_orders", "orders", bson.A{bson.D{{Key: "$match", Value: bson.D{{Key: "status", Value: "paid"}}}}}); err != nil {
		t.Fatal(err)
	}

	a := newTestApp(t)
	cn := addConn(a, cfg)
	tt := ui.NewTester(a.view, 1200, 760)
	conn := navNode{kind: nodeConn, conn: cn.Config.ID}
	a.nav.expand(conn)
	testutil.WaitFor(t, tt, "the connection", func() bool { return cn.Status == connection.StatusConnected })
	testutil.WaitFor(t, tt, "the databases", func() bool { return slices.Contains(tt.Texts(), database) })
	if !slices.Contains(tt.Texts(), "admin") {
		t.Errorf("admin is not listed: %q", tt.Texts())
	}
	shop := navNode{kind: nodeDocDatabase, conn: cn.Config.ID, database: database}
	a.nav.expand(shop)
	testutil.WaitFor(t, tt, "the folders", func() bool { return slices.Contains(tt.Texts(), "Views") })
	a.nav.expand(navNode{kind: nodeDocFolder, conn: cn.Config.ID, database: database, folder: docCollections})
	a.nav.expand(navNode{kind: nodeDocFolder, conn: cn.Config.ID, database: database, folder: docViews})
	a.nav.expand(navNode{kind: nodeDocCollection, conn: cn.Config.ID, database: database, name: "orders"})
	testutil.WaitFor(t, tt, "the indexes", func() bool { return slices.Contains(tt.Texts(), "status_1_at_-1") })
	got := navTexts(tt)
	if !strings.Contains(got, "metrics|time series|orders|_id_|status_1_at_-1") || !strings.Contains(got, "paid_orders|on orders") {
		t.Fatalf("tree %s", got)
	}
	testutil.Snapshot(t, tt, "mongo-navigator")

	if err := tt.Click("Open Console on " + database); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the console", func() bool {
		con, ok := a.ActiveTab().(*mongo.ConsoleTab)
		return ok && con.Database == database
	})
}

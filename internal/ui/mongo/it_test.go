package mongo

import (
	"context"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/testutil/mongoserver"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// testHost is the data view's fake, with the connection status the
// console's bar asks of the app.
type testHost struct{ *dataview.FakeHost }

func (testHost) Online() bool                { return true }
func (testHost) Disconnect(*connection.Conn) {}

// serverConfig is the connection to a MongoDB test server; the test
// skips when nothing listens there.
func serverConfig(t *testing.T, uri string) db.Config {
	t.Helper()
	cfg, err := db.ParseURL(uri)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ID, cfg.Name, cfg.Env = "mg", "catalog", db.Development
	cfg.Port = max(cfg.Port, cfg.DefaultPort())
	c, err := net.DialTimeout("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), time.Second)
	if err != nil {
		t.Skipf("no MongoDB server at %s:%d", cfg.Host, cfg.Port)
	}
	c.Close()
	return cfg
}

// seed makes a database of the test's own holding coll's docs, dropped
// when the test ends.
func seed(t *testing.T, cfg db.Config, coll string, docs []any) string {
	t.Helper()
	ctx := context.Background()
	d, err := db.OpenMongo(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	name := "dgopher_ui_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	database := d.Client.Database(name)
	if err := database.Drop(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Drop(context.Background()) })
	if len(docs) > 0 {
		if _, err := database.Collection(coll).InsertMany(ctx, docs); err != nil {
			t.Fatal(err)
		}
	}
	return name
}

func orders(n int) []any {
	docs := make([]any, n)
	for i := range n {
		status := "open"
		if i%3 == 0 {
			status = "paid"
		}
		docs[i] = bson.D{{Key: "_id", Value: int32(i)}, {Key: "customer", Value: bson.D{{Key: "name", Value: "Customer " + strconv.Itoa(i)}, {Key: "country", Value: "GB"}}},
			{Key: "items", Value: bson.A{bson.D{{Key: "sku", Value: "KB-01"}, {Key: "qty", Value: int32(1)}}}},
			{Key: "total", Value: float64(i) + 0.5}, {Key: "status", Value: status},
			{Key: "createdAt", Value: bson.NewDateTimeFromTime(time.Date(2026, 11, 26, 18, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Minute))}}
	}
	return docs
}

// openCollection opens a collection tab of cfg's server.
func openCollection(t *testing.T, cfg db.Config, database, coll string) (testHost, *ui.Tester, *CollectionTab) {
	t.Helper()
	h := testHost{dataview.NewFakeHost(t)}
	cn := h.AddConn(cfg)
	tt := ui.NewTester(h.View, 1360, 820)
	h.Connect(cn, func() { h.AddTab(NewCollection(h, cn, database, coll)) })
	testutil.WaitFor(t, tt, "the collection tab", func() bool { return len(h.Tabs) == 1 })
	ct := h.Tabs[0].(*CollectionTab)
	testutil.WaitFor(t, tt, "the first documents", func() bool { return !ct.loading && (len(ct.read) > 0 || ct.loadErr != "") })
	if ct.loadErr != "" {
		t.Fatal(ct.loadErr)
	}
	return h, tt, ct
}

// The documents come a page at a time, counted, and a filter that does
// not read says why without asking the server.
func TestCollectionBrowse(t *testing.T) {
	cfg := serverConfig(t, mongoserver.Primary(t))
	database := seed(t, cfg, "orders", orders(450))
	_, tt, ct := openCollection(t, cfg, database, "orders")
	testutil.WaitFor(t, tt, "the count", func() bool { return ct.total == 450 })
	if len(ct.read) != pageSize || ct.done {
		t.Fatalf("first page: %d documents, done %v", len(ct.read), ct.done)
	}
	if !slices.Contains(tt.Texts(), "200 of 450 documents") {
		t.Errorf("status: %q", tt.Texts())
	}
	if err := tt.Click("Load More"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the second page", func() bool { return len(ct.read) == 400 })
	if err := tt.Click("Fetch All"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "every document", func() bool { return ct.done && len(ct.read) == 450 })

	finds := ct.readGen
	ct.FilterIn = `{ status: }`
	ct.Find()
	tt.Frame()
	if ct.readGen != finds || !strings.HasPrefix(ct.inputErr, "Filter: 1:11: expected a value") || !slices.Contains(tt.Texts(), ct.inputErr) {
		t.Fatalf("a filter that does not read: %q, finds %d → %d", ct.inputErr, finds, ct.readGen)
	}
	ct.FilterIn, ct.SortIn, ct.FieldsIn = `{ status: "paid" }`, "{ total: -1 }", "{ total: 1, status: 1 }"
	ct.Find()
	testutil.WaitFor(t, tt, "the documents found", func() bool { return !ct.loading && ct.total == 150 })
	if ct.inputErr != "" || ct.read[0].Lookup("_id").Int32() != 447 || len(ct.view.cols) != 3 {
		t.Fatalf("found %d documents, first %v, columns %+v", len(ct.read), ct.read[0], ct.view.cols)
	}
	for mode, name := range map[int]string{showJSON: "mongo-collection-json", showTree: "mongo-collection-tree"} {
		ct.view.mode = mode
		tt.Frame()
		testutil.Snapshot(t, tt, name)
	}
}

// Edits wait as pending changes, tinted; Save asks once, with every
// statement, the count and whether they apply in one transaction, and a
// change that no longer matches rolls back the others.
func TestCollectionSave(t *testing.T) {
	cfg := serverConfig(t, mongoserver.Primary(t))
	database := seed(t, cfg, "orders", orders(6))
	h, tt, ct := openCollection(t, cfg, database, "orders")

	ct.openFieldEdit(1, "status")
	if ct.field.text != `"open"` {
		t.Fatalf("the field's text %q", ct.field.text)
	}
	ct.field.text = `"refunded"`
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	testutil.Snapshot(t, tt, "mongo-field")
	ct.keepField(false)
	ct.openInsert(nil)
	ct.insert.text = `{ status: "draft", total: 0, items: [] }`
	ct.keepInsert()
	ct.view.grid.SelRow = 3
	ct.deleteChosen()
	ct.showDetail(1)
	tt.Frame()
	if got := ct.edits.summary(); got != "3 changes: 1 edited, 1 new, 1 deleted" || !slices.Contains(tt.Texts(), got) {
		t.Fatalf("pending %q; texts %q", got, tt.Texts())
	}
	if !slices.Contains(tt.Texts(), "was \"open\"") {
		t.Errorf("the document beside the rows does not say what changed: %q", tt.Texts())
	}
	testutil.Snapshot(t, tt, "mongo-collection")
	ct.detailJSON = true
	tt.Frame()
	testutil.Snapshot(t, tt, "mongo-collection-detail-json")
	ct.detailJSON = false

	if err := tt.Click("Save…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	req := h.Confirm
	if req == nil {
		t.Fatalf("Save did not ask; errors %q", h.Errors)
	}
	if req.Title != "Save 3 changes to "+database+".orders?" || !slices.Contains(req.Notes, atomicNote) {
		t.Fatalf("asked %q, notes %q", req.Title, req.Notes)
	}
	lines := strings.Split(req.Preview, "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], `db.orders.updateOne({ _id: 1 }, { $set: { status: "refunded" } })`) ||
		!strings.HasPrefix(lines[1], `db.orders.insertOne({ _id: ObjectId("`) || lines[2] != `db.orders.deleteOne({ _id: 3 })` {
		t.Fatalf("statements:\n%s", req.Preview)
	}
	tt.Advance(time.Second) // the question comes in
	tt.Frame()
	testutil.Snapshot(t, tt, "mongo-save")
	h.Confirm = nil
	req.OnConfirm()
	testutil.WaitFor(t, tt, "the documents saved and read again", func() bool { return !ct.saving && !ct.loading && ct.edits.count() == 0 && len(ct.read) == 6 })
	if len(h.Errors) > 0 {
		t.Fatal(h.Errors)
	}
	var saved int
	for _, e := range h.Events {
		if e.Kind == audit.KindMongo && e.Rows == 1 && e.Err == nil && strings.Contains(e.Statement, "One(") {
			saved++
		}
	}
	if saved != 3 {
		t.Errorf("%d saves in the audit log, want 3: %+v", saved, h.Events)
	}
	d := ct.conn.Doc()
	ctx := context.Background()
	for filter, want := range map[string]int64{`{ status: "refunded" }`: 1, `{ status: "draft" }`: 1, `{ _id: 3 }`: 0} {
		f, _ := parseDocument(filter, "")
		if n, err := d.Count(ctx, database, "orders", f); err != nil || n != want {
			t.Errorf("%s: %d, %v; want %d", filter, n, err, want)
		}
	}

	// A document deleted behind the tab's back: its change matches none,
	// and the change before it rolls back with it.
	ct.openFieldEdit(0, "total")
	ct.field.text = "1.0"
	ct.keepField(false)
	ct.openFieldEdit(4, "status")
	ct.field.text = `"gone"`
	ct.keepField(false)
	if _, err := d.Client.Database(database).Collection("orders").DeleteOne(ctx, bson.D{{Key: "_id", Value: ct.read[4].Lookup("_id")}}); err != nil {
		t.Fatal(err)
	}
	ct.Save()
	h.Confirm.OnConfirm()
	h.Confirm = nil
	testutil.WaitFor(t, tt, "the save to fail", func() bool { return !ct.saving && len(h.Errors) > 0 })
	if !strings.Contains(h.Errors[0], "nothing was saved") || ct.edits.count() != 2 {
		t.Fatalf("errors %q, %d pending", h.Errors, ct.edits.count())
	}
	f, _ := parseDocument("{ total: 1.0 }", "")
	if n, _ := d.Count(ctx, database, "orders", f); n != 0 {
		t.Error("the change before the failure stayed")
	}
}

// A field named with a dot or a $ saves by its name, and the fields the
// edit does not name stay: those a FIELDS projection hid, and those
// changed by someone else since the documents were read.
func TestCollectionSaveFieldsByName(t *testing.T) {
	cfg := serverConfig(t, mongoserver.Primary(t))
	for name, fields := range map[string]string{"all fields": "", "projected": "{ other: 0 }"} {
		t.Run(name, func(t *testing.T) {
			database := seed(t, cfg, "named", []any{bson.D{{Key: "_id", Value: int32(1)}, {Key: "a.b", Value: int32(1)},
				{Key: "$p", Value: int32(1)}, {Key: "n", Value: int32(1)}, {Key: "other", Value: "kept"}}})
			h, tt, ct := openCollection(t, cfg, database, "named")
			if fields != "" {
				ct.FieldsIn = fields
				ct.Find()
				testutil.WaitFor(t, tt, "the documents found", func() bool { return !ct.loading })
				if ct.read[0].Lookup("other").Type != 0 {
					t.Fatalf("the projection did not hide other: %v", ct.read[0])
				}
			}
			d := ct.conn.Doc()
			ctx := context.Background()
			coll := d.Client.Database(database).Collection("named")
			if _, err := coll.UpdateOne(ctx, bson.D{{Key: "_id", Value: int32(1)}}, bson.D{{Key: "$set", Value: bson.D{{Key: "later", Value: "theirs"}}}}); err != nil {
				t.Fatal(err)
			}
			doc := elementsOf(ct.read[0])
			for i := range doc {
				switch doc[i].Key {
				case "a.b":
					doc[i].Value = int32(2)
				case "$p":
					doc[i].Value = "v"
				}
			}
			raw, _ := rawOf(slices.DeleteFunc(doc, func(e bson.E) bool { return e.Key == "n" }))
			ct.edits.edit(ct.read, 0, raw)
			ct.Save()
			tt.Frame()
			req := h.Confirm
			if req == nil {
				t.Fatalf("Save did not ask; errors %q", h.Errors)
			}
			if !strings.Contains(req.Preview, `{ $replaceWith: { $setField: { field: "a.b", input: "$$ROOT", value: { $literal: 2 } } } }`) ||
				strings.Contains(req.Preview, "replaceOne") {
				t.Fatalf("statements:\n%s", req.Preview)
			}
			tt.Advance(time.Second)
			tt.Frame()
			testutil.Snapshot(t, tt, "mongo-save-fields-by-name-"+strings.ReplaceAll(name, " ", "-"))
			h.Confirm = nil
			req.OnConfirm()
			testutil.WaitFor(t, tt, "the documents saved", func() bool { return !ct.saving && !ct.loading && ct.edits.count() == 0 })
			if len(h.Errors) > 0 {
				t.Fatal(h.Errors)
			}
			if !slices.ContainsFunc(h.Events, func(e audit.Event) bool {
				return e.Kind == audit.KindMongo && e.Err == nil && strings.Contains(e.Statement, "$setField")
			}) {
				t.Errorf("the audit log does not hold the statement saved: %+v", h.Events)
			}
			var got bson.D
			if err := coll.FindOne(ctx, bson.D{}).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if b, _ := bson.MarshalExtJSON(got, false, false); string(b) != `{"_id":1,"a.b":2,"$p":"v","other":"kept","later":"theirs"}` {
				t.Errorf("saved %s", b)
			}
		})
	}
}

// Without transactions, the question says the changes apply one by one,
// and after a failure those before it stay saved, the rest pending.
func TestCollectionSaveOneByOne(t *testing.T) {
	cfg := serverConfig(t, mongoserver.Standalone(t))
	database := seed(t, cfg, "orders", orders(4))
	h, tt, ct := openCollection(t, cfg, database, "orders")
	d := ct.conn.Doc()
	ctx := context.Background()
	ct.openFieldEdit(0, "status")
	ct.field.text = `"first"`
	ct.keepField(false)
	ct.openFieldEdit(2, "status")
	ct.field.text = `"second"`
	ct.keepField(false)
	if _, err := d.Client.Database(database).Collection("orders").DeleteOne(ctx, bson.D{{Key: "_id", Value: int32(2)}}); err != nil {
		t.Fatal(err)
	}
	ct.Save()
	req := h.Confirm
	if req == nil || !slices.Contains(req.Notes, oneByOneNote) {
		t.Fatalf("asked %+v", req)
	}
	h.Confirm = nil
	req.OnConfirm()
	testutil.WaitFor(t, tt, "the save to stop", func() bool { return !ct.saving && len(h.Errors) > 0 })
	if !strings.HasPrefix(h.Errors[0], "1 of 2 changes saved") || ct.edits.count() != 1 || ct.read[0].Lookup("status").StringValue() != "first" {
		t.Fatalf("errors %q, %d pending, first %v", h.Errors, ct.edits.count(), ct.read[0])
	}
}

// On a read-only connection the documents show without their edit tools,
// and a change that reaches Save is refused before anything is sent.
func TestCollectionReadOnly(t *testing.T) {
	cfg := serverConfig(t, mongoserver.Primary(t))
	cfg.ReadOnly = true
	database := seed(t, cfg, "orders", orders(3))
	h, tt, ct := openCollection(t, cfg, database, "orders")
	if ct.readOnly() == "" {
		t.Fatal("the documents of a read-only connection are editable")
	}
	doc := elementsOf(ct.read[0])
	doc = append(doc, bson.E{Key: "note", Value: "x"})
	raw, _ := rawOf(doc)
	ct.edits.edit(ct.read, 0, raw)
	ct.Save()
	tt.Frame()
	if h.Confirm != nil || len(h.Errors) != 1 || !strings.Contains(h.Errors[0], "Not allowed: catalog is read-only: updateOne writes") {
		t.Fatalf("confirm %v, errors %q", h.Confirm, h.Errors)
	}
	if !slices.ContainsFunc(h.Events, func(e audit.Event) bool {
		return e.Kind == audit.KindBlocked && strings.Contains(e.Statement, "updateOne")
	}) {
		t.Errorf("no blocked event: %+v", h.Events)
	}
}

// openConsole opens a console on database of cfg's server.
func openConsole(t *testing.T, cfg db.Config, database string) (testHost, *ui.Tester, *ConsoleTab) {
	t.Helper()
	h := testHost{dataview.NewFakeHost(t)}
	cn := h.AddConn(cfg)
	tt := ui.NewTester(h.View, 1360, 820)
	h.Connect(cn, func() { h.AddTab(NewConsole(h, cn, database)) })
	testutil.WaitFor(t, tt, "the console", func() bool { return len(h.Tabs) == 1 })
	return h, tt, h.Tabs[0].(*ConsoleTab)
}

// A script runs in order and stops at a statement the policy refuses:
// those after it do not run.
func TestConsoleScriptStopsAtBlocked(t *testing.T) {
	cfg := serverConfig(t, mongoserver.Primary(t))
	cfg.ReadOnly = true
	database := seed(t, cfg, "orders", orders(5))
	h, tt, con := openConsole(t, cfg, database)
	con.Editor.SetText("db.orders.find({ total: { $lt: 3 } })\ndb.orders.insertOne({ total: 99 })\ndb.orders.countDocuments()")
	con.RunScript()
	testutil.WaitFor(t, tt, "the script to end", func() bool { return !con.running })
	if len(con.results) != 1 || len(con.results[0].docs) != 3 {
		t.Fatalf("results %+v; output %+v", con.results, con.output)
	}
	out := con.output
	if len(out) != 3 || !strings.HasPrefix(out[1].text, "Not allowed: catalog is read-only: insertOne writes") || out[2].text != "The 1 statement after it did not run." {
		t.Fatalf("output %+v", out)
	}
	testutil.Snapshot(t, tt, "mongo-console-output")
	if con.shown != len(con.results) || !slices.Contains(tt.Texts(), out[1].text) {
		t.Errorf("the refusal is not shown: %q", tt.Texts())
	}
	if !slices.ContainsFunc(h.Events, func(e audit.Event) bool { return e.Kind == audit.KindBlocked }) {
		t.Errorf("no blocked event: %+v", h.Events)
	}
	f, _ := parseDocument("{ total: 99 }", "")
	if n, _ := con.conn.Doc().Count(context.Background(), database, "orders", f); n != 0 {
		t.Error("the refused insert reached the server")
	}
}

// Statements of a script each have a result: documents to page through,
// replies; use switches the database; a change of many on production asks
// with the documents it matches.
func TestConsoleResults(t *testing.T) {
	cfg := serverConfig(t, mongoserver.Primary(t))
	database := seed(t, cfg, "orders", orders(260))
	h, tt, con := openConsole(t, cfg, "admin")
	var opened string
	con.OpenCollection = func(database, coll string) { opened = database + "." + coll }
	con.Editor.SetText("use " + database + "\nshow collections\n" +
		"db.orders.find({ status: \"paid\" }).sort({ createdAt: -1 })\n" +
		"db.orders.aggregate([{ $group: { _id: \"$status\", n: { $sum: 1 } } }, { $sort: { _id: 1 } }])\n" +
		"db.orders.countDocuments({ status: \"open\" })")
	con.RunScript()
	testutil.WaitFor(t, tt, "the script to end", func() bool { return !con.running })
	if con.Database != database || len(con.results) != 5 {
		t.Fatalf("database %s, %d results; output %+v", con.Database, len(con.results), con.output)
	}
	var titles []string
	for _, r := range con.results {
		titles = append(titles, r.title())
	}
	if got := strings.Join(titles, ", "); got != "use "+database+", show collections · 1, orders.find · 87, orders.aggregate · 2, orders.countDocuments" {
		t.Errorf("titles %s", got)
	}
	if n := con.results[4].reply.Lookup("countDocuments").AsInt64(); n != 173 {
		t.Errorf("count %d", n)
	}
	con.shown = 2
	tt.Frame()
	if err := tt.Click("Open in " + database + ".orders"); err != nil {
		t.Fatal(err)
	}
	if opened != database+".orders" {
		t.Errorf("opened %q", opened)
	}
	tt.Frame() // the note comes once the bar's width is known
	testutil.Snapshot(t, tt, "mongo-console")
	narrow := ui.NewTester(h.View, 720, 820)
	narrow.Frame()
	narrow.Frame()
	if slices.Contains(narrow.Texts(), readOnlyNote) {
		t.Error("the read-only note shows where there is no room for it")
	}
	testutil.Snapshot(t, narrow, "mongo-console-narrow")
	con.shown = 3
	tt.Frame()
	testutil.Snapshot(t, tt, "mongo-console-reply")

	// A cursor of more than a page reads on.
	con.Editor.SetText("db.orders.find()")
	con.Run()
	testutil.WaitFor(t, tt, "the find", func() bool { return !con.running && len(con.results) == 1 })
	r := con.results[0]
	if len(r.docs) != pageSize || r.done {
		t.Fatalf("first page %d, done %v", len(r.docs), r.done)
	}
	tt.Frame()
	if !slices.Contains(tt.Texts(), readOnlyNote) {
		t.Error("the read-only note does not show beside one result")
	}
	if err := tt.Click("Fetch All"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "every document", func() bool { return r.done && len(r.docs) == 260 })

	con.conn.Config.Env = db.Production
	con.Editor.SetText(`db.orders.deleteMany({ status: "paid" })`)
	con.Run()
	testutil.WaitFor(t, tt, "the question", func() bool { return h.Confirm != nil })
	if h.Confirm.Title != "Run deleteMany on catalog?" || !slices.Contains(h.Confirm.Notes, "It matches 87 documents of orders, counted just now.") {
		t.Fatalf("asked %q %q", h.Confirm.Title, h.Confirm.Notes)
	}
	h.Confirm.OnCancel()
	h.Confirm = nil
	if con.running {
		t.Error("cancelled, the run goes on")
	}
}

// typedHost keeps the typed runs the console records.
type typedHost struct {
	testHost
	typed []audit.TypedRun
}

func (h *typedHost) RecordTyped(cfg db.Config, r audit.TypedRun) {
	h.typed = append(h.typed, r)
	h.testHost.RecordTyped(cfg, r)
}

// What the console runs is recorded as typed, as the Redis console's
// commands are, and kept in the query history, as written: the audit log
// hides its secrets.
func TestConsoleRecordsTyped(t *testing.T) {
	cfg := serverConfig(t, mongoserver.Primary(t))
	database := seed(t, cfg, "orders", orders(3))
	h := &typedHost{testHost: testHost{dataview.NewFakeHost(t)}}
	cn := h.AddConn(cfg)
	tt := ui.NewTester(h.View, 1360, 820)
	h.Connect(cn, func() { h.AddTab(NewConsole(h, cn, database)) })
	testutil.WaitFor(t, tt, "the console", func() bool { return len(h.Tabs) == 1 })
	con := h.Tabs[0].(*ConsoleTab)
	con.Editor.SetText("db.orders.countDocuments({})")
	con.Run()
	testutil.WaitFor(t, tt, "the statement", func() bool { return !con.running })
	if len(h.typed) != 1 || !h.typed[0].History || h.typed[0].Sent != "db.orders.countDocuments({})" || h.typed[0].Database != database {
		t.Fatalf("typed runs %+v", h.typed)
	}
	if e := h.Events[len(h.Events)-1]; e.Kind != audit.KindMongo || e.Origin != audit.OriginTyped {
		t.Errorf("recorded %+v", e)
	}
}

// Refresh, Run and closing the tab during Fetch All close the cursor a
// Fetch is reading, on another goroutine: the cursor stops that Fetch
// first. Run with -race.
func TestCursorClosedDuringFetchAll(t *testing.T) {
	cfg := serverConfig(t, mongoserver.Primary(t))
	database := seed(t, cfg, "orders", orders(3000))
	_, tt, ct := openCollection(t, cfg, database, "orders")
	ct.FetchAll()
	ct.Find() // Refresh while a page is read
	testutil.WaitFor(t, tt, "the documents found again", func() bool { return !ct.loading && ct.loadErr == "" && len(ct.read) == pageSize })
	ct.FetchAll()
	ct.Close()

	_, tt, con := openConsole(t, cfg, database)
	con.Editor.SetText("db.orders.find()")
	con.Run()
	testutil.WaitFor(t, tt, "the find", func() bool { return !con.running && len(con.results) == 1 })
	con.loadMore(con.results[0], true)
	con.Run() // again, while Fetch All reads
	testutil.WaitFor(t, tt, "the find again", func() bool { return !con.running && len(con.results) == 1 && len(con.results[0].docs) == pageSize })
	con.loadMore(con.results[0], true)
	con.Close()
}

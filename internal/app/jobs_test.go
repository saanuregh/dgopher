package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// liteApp is an app with a SQLite file connected.
func liteApp(t *testing.T, env db.Environment) (*App, *ui.Tester, *connection.Conn, string) {
	t.Helper()
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "jobs.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file, Env: env})
	tt := ui.NewTester(a.view, 1200, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	return a, tt, cn, file
}

func writeSQL(t *testing.T, name, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// auditEvents reads the audit log of the app's first project.
func auditEvents(t *testing.T, a *App) []audit.Event {
	t.Helper()
	events, err := a.projects[0].Audit.Read(0)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// rollbackAudited finds the ROLLBACK the app ran for a file that left its
// transaction open.
func rollbackAudited(t *testing.T, a *App, line string) *audit.Event {
	t.Helper()
	for _, e := range auditEvents(t, a) {
		if e.Kind == audit.KindStatement && e.Statement == "ROLLBACK" && strings.Contains(e.Detail, line) {
			return &e
		}
	}
	return nil
}

// A file that begins a transaction and never commits it is rolled back
// on purpose, audited, and reported as failed with the BEGIN's line.
func TestSQLFileOpenTransactionRolledBackAndReported(t *testing.T) {
	a, tt, cn, _ := liteApp(t, db.Development)
	path := writeSQL(t, "open.sql", "CREATE TABLE t (a int);\nBEGIN;\nINSERT INTO t VALUES (1);\nINSERT INTO t VALUES (2);\n")
	x := runFile(t, a, tt, cn, path, nil)
	if x.oneTx {
		t.Fatal("a file with its own BEGIN ran in the app's transaction")
	}
	if !strings.Contains(x.err, "BEGIN at line 2 was never committed; its changes were rolled back") || len(x.failures) != 0 {
		t.Fatalf("the end of the file: %q, %q", x.err, x.failures)
	}
	var n int
	if err := cn.DB.SQL.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d rows stayed (%v)", n, err)
	}
	if e := rollbackAudited(t, a, "line 2"); e == nil || e.Error != "" {
		t.Fatalf("the rollback was not audited: %+v", e)
	}
	failed := false
	for _, e := range auditEvents(t, a) {
		if e.Kind == audit.KindScript && strings.Contains(e.Error, "never committed") {
			failed = true
		}
	}
	if !failed {
		t.Fatal("the file was audited as a success")
	}

	// Stopped at an error inside the transaction: the same.
	path = writeSQL(t, "fails.sql", "CREATE TABLE u (a int);\nBEGIN;\nINSERT INTO u VALUES (1);\nINSERT INTO missing VALUES (1);\n")
	x = runFile(t, a, tt, cn, path, nil)
	if !strings.Contains(x.err, "stopped at the first error") || !strings.Contains(x.err, "BEGIN at line 2 was never committed; its changes were rolled back") {
		t.Fatalf("a failing file: %q", x.err)
	}
	if err := cn.DB.SQL.QueryRow(`SELECT count(*) FROM u`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d rows stayed (%v)", n, err)
	}

	// DuckDB's one connection, which every session shares.
	duck := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	a.Connect(duck, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return duck.Status == connection.StatusConnected })
	x = runFile(t, a, tt, duck, writeSQL(t, "duck.sql", "CREATE TABLE d (a int);\nBEGIN;\nINSERT INTO d VALUES (1);\n"), nil)
	if !strings.Contains(x.err, "BEGIN at line 2 was never committed; its changes were rolled back") {
		t.Fatalf("DuckDB: %q", x.err)
	}
	if err := duck.DB.SQL.QueryRow(`SELECT count(*) FROM d`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("DuckDB: %d rows stayed (%v)", n, err)
	}
}

// The same on the servers, whose drivers report the transaction.
func TestIntegrationSQLFileOpenTransaction(t *testing.T) {
	testutil.Integration(t)
	for _, c := range []struct {
		cfg   db.Config
		begin string
	}{
		{testutil.PGConfig(), "BEGIN"},
		{db.Config{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dgopher", Database: "shop"}, "START TRANSACTION"},
	} {
		t.Run(string(c.cfg.Engine), func(t *testing.T) {
			a := newTestApp(t)
			cn := addConn(a, c.cfg)
			tt := ui.NewTester(a.view, 1200, 800)
			a.Connect(cn, nil)
			testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
			cn.DB.SQL.Exec(`DROP TABLE IF EXISTS it_open_tx`)
			defer cn.DB.SQL.Exec(`DROP TABLE IF EXISTS it_open_tx`)
			path := writeSQL(t, "open.sql", "CREATE TABLE it_open_tx (a int);\n"+c.begin+";\nINSERT INTO it_open_tx VALUES (1);\n")
			x := runFile(t, a, tt, cn, path, nil)
			if !strings.Contains(x.err, c.begin+" at line 2 was never committed; its changes were rolled back") {
				t.Fatalf("the end of the file: %q, %q", x.err, x.failures)
			}
			var n int
			if err := cn.DB.SQL.QueryRow(`SELECT count(*) FROM it_open_tx`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("%d rows stayed (%v)", n, err)
			}
			if e := rollbackAudited(t, a, "line 2"); e == nil || e.Error != "" {
				t.Fatalf("the rollback: %+v", e)
			}
		})
	}
}

// On production, a file that writes asks for the connection's name; one
// that only reads runs without asking.
func TestSQLFileProductionNeedsTypedName(t *testing.T) {
	a, tt, cn, _ := liteApp(t, db.Production)
	path := writeSQL(t, "writes.sql", "CREATE TABLE p (a int);\nINSERT INTO p VALUES (1);\n")
	a.startSQLFileRun(cn, "", path, "")
	x := a.sqlFile
	testutil.WaitFor(t, tt, "the survey", func() bool { return !x.reading })
	a.confirmSQLFile(x)
	if a.confirm == nil || !a.confirm.TypeName {
		t.Fatalf("a writing file on production: %+v", a.confirm)
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "run-sql-file-production")
	a.confirm.OnConfirm()
	a.confirm = nil
	testutil.WaitFor(t, tt, "the run", func() bool { return !x.running })
	var n int
	if err := cn.DB.SQL.QueryRow(`SELECT count(*) FROM p`).Scan(&n); x.err != "" || err != nil || n != 1 {
		t.Fatalf("the run: %q, %d rows (%v)", x.err, n, err)
	}

	path = writeSQL(t, "reads.sql", "SELECT count(*) FROM p;\n")
	a.startSQLFileRun(cn, "", path, "")
	x = a.sqlFile
	testutil.WaitFor(t, tt, "the survey", func() bool { return !x.reading })
	a.confirmSQLFile(x)
	if a.confirm != nil {
		t.Fatalf("a reading file asked: %+v", a.confirm)
	}
	testutil.WaitFor(t, tt, "the run", func() bool { return x.finished })
	if x.err != "" {
		t.Fatal(x.err)
	}
}

// The survey follows the transactions a MySQL file begins and ends itself:
// a statement inside one that commits it implicitly, as CREATE INDEX does,
// makes the run ask first, by its line and the BEGIN's. It reads only the
// file: no server is needed.
func TestSQLFileSurveyWarnsImplicitCommit(t *testing.T) {
	my := &db.Config{ID: "my", Name: "my", Engine: db.MySQL, Env: db.Development}
	pg := &db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Env: db.Development}
	for _, c := range []struct {
		cfg  *db.Config
		text string
		want string // the reason, "" for none
	}{
		{my, "BEGIN;\nINSERT INTO t VALUES (1);\nCREATE INDEX i ON t (a);\nINSERT INTO t VALUES (2);\nCOMMIT;\n",
			"line 3: CREATE commits the transaction begun at line 1"},
		{my, "START TRANSACTION;\nSAVEPOINT s;\nINSERT INTO t VALUES (1);\nROLLBACK TO SAVEPOINT s;\n/*!50000 ALTER TABLE t ADD COLUMN c INT */;\nCOMMIT;\n",
			"line 5: ALTER commits the transaction begun at line 1"},
		{my, "BEGIN;\nINSERT INTO t VALUES (1);\nBEGIN;\nINSERT INTO t VALUES (2);\nCOMMIT;\n",
			"line 3: BEGIN commits the transaction begun at line 1"},
		{my, "INSERT INTO t VALUES (1);\nCREATE INDEX i ON t (a);\n", ""},
		{my, "BEGIN;\nINSERT INTO t VALUES (1);\nCOMMIT;\nCREATE INDEX i ON t (a);\n", ""},
		{my, "BEGIN;\nINSERT INTO t VALUES (1);\nROLLBACK;\nALTER TABLE t ADD COLUMN c INT;\n", ""},
		{my, "BEGIN;\nCREATE TEMPORARY TABLE x (a INT);\nCOMMIT;\n", ""},
		{pg, "BEGIN;\nINSERT INTO t VALUES (1);\nCREATE INDEX i ON t (a);\nCOMMIT;\n", ""},
	} {
		path := writeSQL(t, "file.sql", c.text)
		var progress atomic.Int64
		sum, err := surveySQLFile(context.Background(), c.cfg, path, &progress)
		if err != nil {
			t.Fatalf("%q: %v", c.text, err)
		}
		i := slices.IndexFunc(sum.verdict.Reasons, func(r string) bool { return strings.Contains(r, "commits the transaction begun at") })
		switch {
		case c.want == "" && (i >= 0 || sum.verdict.Confirm):
			t.Errorf("%s %q warned: %+v", c.cfg.Engine, c.text, sum.verdict)
		case c.want != "" && (i < 0 || !strings.HasPrefix(sum.verdict.Reasons[i], c.want) || !sum.verdict.Confirm):
			t.Errorf("%s %q: %+v, want %q", c.cfg.Engine, c.text, sum.verdict, c.want)
		}
	}
	// Many are named up to a few, and counted past them.
	path := writeSQL(t, "many.sql", strings.Repeat("BEGIN;\nINSERT INTO t VALUES (1);\nALTER TABLE t ADD COLUMN c INT;\nCOMMIT;\n", 7))
	var progress atomic.Int64
	sum, err := surveySQLFile(context.Background(), my, path, &progress)
	if err != nil {
		t.Fatal(err)
	}
	if r := sum.verdict.Reasons; len(r) != 6 || !strings.HasPrefix(r[4], "line 19: ") || r[5] != "2 more statements commit a transaction the file began" {
		t.Fatalf("reasons %q", r)
	}
}

// One transaction is the default wherever the engine holds schema changes
// in one and the file has no transaction control of its own.
func TestSQLFileOneTransactionDefault(t *testing.T) {
	a, tt, lite, _ := liteApp(t, db.Development)
	plain := writeSQL(t, "plain.sql", "CREATE TABLE d (id integer primary key);\nINSERT INTO d VALUES (1);\nINSERT INTO d VALUES (1);\n")
	own := writeSQL(t, "own.sql", "BEGIN;\nCREATE TABLE e (a int);\nCOMMIT;\n")
	for _, c := range []struct {
		engine db.Engine
		path   string
		oneTx  bool
	}{
		{db.SQLite, plain, true}, {db.Postgres, plain, true}, {db.DuckDB, plain, true},
		{db.MySQL, plain, false}, {db.ClickHouse, plain, false}, {db.SQLite, own, false},
	} {
		cn := lite
		if c.engine != db.SQLite {
			cn = addConn(a, db.Config{ID: string(c.engine), Name: string(c.engine), Engine: c.engine})
		}
		a.startSQLFileRun(cn, "", c.path, "")
		x := a.sqlFile
		testutil.WaitFor(t, tt, "the survey", func() bool { return !x.reading })
		if x.err != "" || x.oneTx != c.oneTx {
			t.Errorf("%s %s: one transaction %v, want %v (%q)", c.engine, filepath.Base(c.path), x.oneTx, c.oneTx, x.err)
		}
		x.open = false
		tt.Frame()
	}

	// As it is by default: the failing row leaves nothing of the file.
	x := runFile(t, a, tt, lite, plain, nil)
	if !strings.Contains(x.err, "nothing of the file stays") {
		t.Fatalf("all or nothing: %q", x.err)
	}
	if err := lite.DB.SQL.QueryRow(`SELECT count(*) FROM d`).Scan(new(int)); err == nil {
		t.Fatal("the failed file left its table")
	}
}

// longSelect runs for minutes on SQLite, unless it is cancelled.
const longSelect = "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 5000000000) SELECT count(*) FROM c;\n"

// startLongFile runs a file that begins a transaction, inserts a row into
// t, then runs longSelect, and returns once that runs.
func startLongFile(t *testing.T, a *App, tt *ui.Tester, cn *connection.Conn) *sqlFileRun {
	t.Helper()
	path := writeSQL(t, "long.sql", "CREATE TABLE t (a int);\nBEGIN;\nINSERT INTO t VALUES (1);\n"+longSelect)
	a.startSQLFileRun(cn, "", path, "")
	x := a.sqlFile
	testutil.WaitFor(t, tt, "the survey", func() bool { return !x.reading })
	a.confirmSQLFile(x)
	if a.confirm != nil {
		t.Fatalf("asked: %+v", a.confirm)
	}
	testutil.WaitFor(t, tt, "the long statement", func() bool { return x.done.Load() == 3 })
	return x
}

// Quitting lists the work running, with what stopping it loses; once
// agreed, the quit stops the work and waits for it before closing the
// connections.
func TestQuitListsRunningJobs(t *testing.T) {
	a, tt, cn, file := liteApp(t, db.Development)
	x := startLongFile(t, a, tt, cn)
	quit := false
	if a.requestQuit(func() { quit = true }) || a.closing == nil {
		t.Fatal("quit without asking about the file running")
	}
	if r := a.closing.reason; !strings.Contains(r, "long.sql") || !strings.Contains(r, "stopping it") {
		t.Fatalf("the reason: %q", r)
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "quit-running-job")
	r := a.closing
	r.open = false
	r.onClose()
	if !quit {
		t.Fatal("did not quit")
	}
	a.shutdown() // as quitting does, once agreed
	if jobs := a.runningJobs(); len(jobs) != 0 {
		t.Fatalf("%d jobs still run after the quit", len(jobs))
	}
	testutil.WaitFor(t, tt, "the file's end", func() bool { return !x.running })
	if !strings.Contains(x.err, "BEGIN at line 2 was never committed; its changes were rolled back") {
		t.Fatalf("the file: %q", x.err)
	}
	// The quit closed the project's state: read its audit log again.
	p, _, err := project.Load(a.projects[0].Dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	events, err := p.Audit.Read(0)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(events, func(e audit.Event) bool {
		return e.Kind == audit.KindStatement && e.Statement == "ROLLBACK" && strings.Contains(e.Detail, "line 2")
	}); i < 0 || events[i].Error != "" {
		t.Fatalf("the rollback in %+v", events)
	}
	if rowsIn(t, file) != "0" {
		t.Fatal("the file's transaction was not rolled back")
	}
}

// Disconnecting lists the work running on the connection, then stops it
// and closes the pool once it ended, its transaction rolled back.
func TestDisconnectCancelsJobs(t *testing.T) {
	a, tt, cn, file := liteApp(t, db.Development)
	x := startLongFile(t, a, tt, cn)
	pool := cn.DB
	a.requestDisconnect("Disconnect lite?", []*connection.Conn{cn}, func() { a.disconnect(cn) })
	if a.closing == nil || !strings.Contains(a.closing.reason, "long.sql") {
		t.Fatalf("disconnected without asking: %+v", a.closing)
	}
	r := a.closing
	r.open = false
	r.onClose()
	testutil.WaitFor(t, tt, "the file's end", func() bool { return !x.running })
	if !strings.Contains(x.err, "cancelled") || !strings.Contains(x.err, "BEGIN at line 2 was never committed; its changes were rolled back") {
		t.Fatalf("the file: %q", x.err)
	}
	if e := rollbackAudited(t, a, "line 2"); e == nil || e.Error != "" {
		t.Fatalf("the rollback: %+v", e)
	}
	testutil.WaitFor(t, tt, "the pool to close", func() bool { return pool.SQL.Ping() != nil })
	if rowsIn(t, file) != "0" {
		t.Fatal("the file's transaction was not rolled back")
	}

	// Deleting a connection asks about its work too.
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	_, cancel := context.WithCancel(context.Background())
	finish := a.StartJob(cn, "Importing rows.csv into t: stopping it rolls the import back.", cancel)
	a.askDeleteConn(cn)
	if a.closing == nil || !strings.Contains(a.closing.reason, "Importing rows.csv") {
		t.Fatalf("deleting: %+v", a.closing)
	}
	a.closing = nil
	finish()
	if len(a.losses(cn)) != 0 {
		t.Fatalf("a finished job is still listed: %q", a.losses(cn))
	}
}

// Disconnecting closes the pool only once the jobs it stopped have ended,
// for them to roll back what they left open.
func TestDisconnectWaitsForJobs(t *testing.T) {
	a, tt, cn, _ := liteApp(t, db.Development)
	pool := cn.DB
	cancelled := make(chan struct{})
	finish := a.StartJob(cn, "Waiting: stopping it loses nothing.", func() { close(cancelled) })
	a.disconnect(cn)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnecting did not stop the job")
	}
	for range 5 {
		tt.Frame()
		time.Sleep(20 * time.Millisecond)
		if err := pool.SQL.Ping(); err != nil {
			t.Fatalf("the pool closed under the running job: %v", err)
		}
	}
	finish()
	testutil.WaitFor(t, tt, "the pool to close", func() bool { return pool.SQL.Ping() != nil })
}

// A job that does not end when stopped holds a disconnect or a quit up
// only for jobStopWait.
func TestStopJobsTimesOut(t *testing.T) {
	old := jobStopWait
	jobStopWait = 50 * time.Millisecond
	t.Cleanup(func() { jobStopWait = old })
	a, _, cn, _ := liteApp(t, db.Development)
	finish := a.StartJob(cn, "Stuck: stopping it loses nothing.", func() {})
	defer finish()
	start := time.Now()
	stopJobs(a.runningJobs())
	if took := time.Since(start); took < jobStopWait || took > 2*time.Second {
		t.Fatalf("stopping took %s", took)
	}
}

// A panic in background work shows an error, its secrets hidden, and the
// app goes on; a job's UI ends as it would have, and the job is no longer
// running.
func TestBackgroundPanicRecovered(t *testing.T) {
	a, tt, cn, _ := liteApp(t, db.Development)
	stopped := false
	_, cancel := context.WithCancel(context.Background())
	dataview.RunJob(a, cn, "Panicking: stopping it loses nothing.", cancel, func() { stopped = true }, func() func() {
		panic("dial postgres://u:hunter2@h/db failed\nsecond line")
	})
	testutil.WaitFor(t, tt, "the error", func() bool { return a.alert != nil && stopped })
	al := a.alert
	if al.title != "Something went wrong" || !strings.Contains(al.message, "A background task stopped on an internal error: dial postgres://u:") ||
		strings.Contains(al.message, "hunter2") || strings.Contains(al.message, "second line") || !strings.Contains(al.message, "The app kept running") {
		t.Fatalf("the error: %q: %q", al.title, al.message)
	}
	if jobs := a.runningJobs(); len(jobs) != 0 {
		t.Fatalf("%d jobs still run", len(jobs))
	}
	a.alert = nil

	// So does the fake host of the feature packages' tests.
	h := dataview.NewFakeHost(t)
	ht := ui.NewTester(h.View, 800, 600)
	h.Background(func() func() { panic("boom") })
	testutil.WaitFor(t, ht, "the fake host's error", func() bool { return len(h.Errors) > 0 })
	if !strings.HasPrefix(h.Errors[0], "Something went wrong: A background task stopped on an internal error: boom.") {
		t.Fatalf("the fake host: %q", h.Errors)
	}
}

// A panic in background work started with a reset shows the error and
// runs the reset, so what waits on the work does not stay busy; work that
// returns runs what it returns and not the reset.
func TestBackgroundResetOnPanic(t *testing.T) {
	a := newBareApp(t)
	tt := ui.NewTester(a.view, 800, 600)
	reset := false
	dataview.BackgroundResetOnPanic(a, func() { reset = true }, func() func() { panic("boom") })
	testutil.WaitFor(t, tt, "the error and the reset", func() bool { return a.alert != nil && reset })
	if !strings.Contains(a.alert.message, "boom") {
		t.Fatalf("the error: %q", a.alert.message)
	}
	a.alert = nil

	reset, applied := false, false
	dataview.BackgroundResetOnPanic(a, func() { reset = true }, func() func() { return func() { applied = true } })
	testutil.WaitFor(t, tt, "the apply", func() bool { return applied })
	if reset || a.alert != nil {
		t.Fatalf("reset %v, alert %v", reset, a.alert)
	}
}

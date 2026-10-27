package query

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/sqltext"
	"dgopher/internal/testutil"
	"dgopher/internal/testutil/optionalserver"
	"dgopher/internal/ui/dataview"
)

var sqlserverEditorConfig = db.Config{ID: "mssql", Name: "mssql", Engine: db.SQLServer, Host: "127.0.0.1", Port: 11433, User: "sa", Password: "Dgopher-1", Database: "shop"}

// A T-SQL batch shows each of its results, and the messages PRINT sends.
func TestIntegrationSQLServerBatch(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "sqlserver")
	_, tt, _, q := openEditor(t, sqlserverEditorConfig, 1000, 700, "PRINT 'hello from the batch'\nSELECT 1 AS a\nSELECT 2 AS b, 3 AS c")
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && len(q.results) > 0 })
	if len(q.results) != 2 || q.results[0].err != "" || q.results[1].view == nil || q.results[1].tableWhy == "" {
		t.Fatalf("results %d: %+v", len(q.results), q.results)
	}
	if !slices.ContainsFunc(q.messages, func(m message) bool { return m.text == "hello from the batch" }) {
		t.Fatalf("messages %+v", q.messages)
	}
}

// Explain shows SQL Server's estimated plan, Explain Analyze the plan as
// the statement ran, with the statement's rows; after either, and after
// an Explain Analyze cancelled as it ran, the session runs statements
// again: a write that follows is made, and returns no plan.
func TestIntegrationSQLServerExplain(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "sqlserver")
	ctx := context.Background()
	d, err := db.Open(ctx, sqlserverEditorConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, s := range []string{"DROP TABLE IF EXISTS dbo.explained_ui", "CREATE TABLE dbo.explained_ui (n INT)"} {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	defer d.SQL.Exec("DROP TABLE IF EXISTS dbo.explained_ui")
	_, tt, _, q := openEditor(t, sqlserverEditorConfig, 1200, 800, "")
	written := 0
	write := func(after string) {
		t.Helper()
		written++
		sql := fmt.Sprintf("INSERT INTO dbo.explained_ui VALUES (%d)", written)
		q.Editor.SetText(sql)
		q.Run(RunStatement)
		testutil.WaitFor(t, tt, "the write", func() bool { return !q.Running && len(q.results) > 0 && q.results[0].sql == sql })
		var n int
		d.SQL.QueryRow("SELECT COUNT(*) FROM dbo.explained_ui").Scan(&n)
		if r := q.results[0]; r.err != "" || r.plan != nil || len(q.results) != 1 || n != written {
			t.Fatalf("after %s: error %q, plan %v, %d results, %d rows written, want %d", after, r.err, r.plan != nil, len(q.results), n, written)
		}
	}

	q.Editor.SetText("SELECT name FROM sys.objects WHERE object_id = 3\nSELECT COUNT(*) AS n FROM sys.objects")
	q.Run(RunExplain)
	testutil.WaitFor(t, tt, "the plan", func() bool { return !q.Running && q.current() != nil && q.current().stmt.Analysis.Verb == "SELECT" })
	r := q.current()
	if r.err != "" || r.plan == nil || r.plan.Analyzed || r.plan.Root.Op != "Batch" || len(r.plan.Root.Children) != 2 || len(q.results) != 1 {
		t.Fatalf("explain: error %q, plan %+v, %d results", r.err, r.plan, len(q.results))
	}
	if !tt.HasText("Batch") {
		t.Fatalf("the plan is not shown: %q", tt.Texts())
	}
	write("Explain")

	q.Editor.SetText("SELECT name FROM sys.objects WHERE object_id = 3")
	q.Run(RunExplainAnalyze)
	testutil.WaitFor(t, tt, "the plan", func() bool { return !q.Running && q.current() != nil && q.current().stmt.Analysis.Verb == "SELECT" })
	r = q.current()
	if r.err != "" || r.plan == nil || !r.plan.Analyzed || len(q.results) != 2 || q.results[0].plan != nil || q.results[1] != r {
		t.Fatalf("explain analyze: error %q, plan %+v, %d results", r.err, r.plan, len(q.results))
	}
	write("Explain Analyze")

	q.Editor.SetText("SELECT COUNT_BIG(*) FROM sys.all_objects a CROSS JOIN sys.all_objects b CROSS JOIN sys.all_objects c")
	q.Run(RunExplainAnalyze)
	testutil.WaitFor(t, tt, "the run", func() bool { return q.Running })
	time.Sleep(300 * time.Millisecond)
	q.cancel()
	testutil.WaitFor(t, tt, "the cancel", func() bool { return !q.Running && q.current() != nil })
	if r := q.current(); !strings.Contains(strings.ToLower(r.err), "cancel") {
		t.Fatalf("the cancelled explain: %q", r.err)
	}
	write("a cancelled Explain Analyze")
}

// Under manual commit a T-SQL batch holding UPDATE STATISTICS among
// writes runs in the transaction the app opens, after asking; one holding
// KILL among writes, which then opens none, asks first.
func TestIntegrationSQLServerManualCommitBatch(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "sqlserver")
	cfg := sqlserverEditorConfig
	cfg.Commit = db.CommitManual
	d := serverWith(t, cfg, []string{"DROP TABLE IF EXISTS dbo.rv_ms_manual", "CREATE TABLE dbo.rv_ms_manual (id INT PRIMARY KEY, v INT)",
		"INSERT INTO dbo.rv_ms_manual VALUES (1, 1)"}, "DROP TABLE IF EXISTS dbo.rv_ms_manual")
	a, tt, _, q := openEditor(t, cfg, 1200, 760, "UPDATE STATISTICS dbo.rv_ms_manual\nUPDATE dbo.rv_ms_manual SET v = 2 WHERE id = 1")
	t.Cleanup(func() { q.sess.Close() })
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.Confirm != nil })
	if !slices.ContainsFunc(a.Confirm.Reasons, func(r string) bool { return strings.HasPrefix(r, "UPDATE STATISTICS takes effect at once") }) {
		t.Fatalf("reasons %q", a.Confirm.Reasons)
	}
	a.Confirm.Open = false
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
	began := slices.ContainsFunc(a.Events, func(e audit.Event) bool { return e.Purpose == purposeManualCommit })
	if !began || q.Tx != db.TxOpen {
		t.Fatalf("manual BEGIN %v, tx %v; messages %+v", began, q.Tx, q.messages)
	}
	q.endTx(false)
	testutil.WaitFor(t, tt, "the rollback", func() bool { return !q.Running })
	var v int
	if err := d.SQL.QueryRow("SELECT v FROM dbo.rv_ms_manual WHERE id = 1").Scan(&v); err != nil || v != 1 {
		t.Fatalf("after Roll Back: v %d (%v)", v, err)
	}

	q.Editor.SetText("INSERT INTO dbo.rv_ms_manual VALUES (2, 2)\nKILL 32767")
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.Confirm != nil })
	if !slices.ContainsFunc(a.Confirm.Reasons, func(r string) bool { return strings.HasPrefix(r, "KILL cannot run in a transaction") }) {
		t.Fatalf("reasons %q", a.Confirm.Reasons)
	}
	a.Confirm.Open, a.Confirm = false, nil
	tt.Frame()
	var n int
	if err := d.SQL.QueryRow("SELECT count(*) FROM dbo.rv_ms_manual").Scan(&n); err != nil || n != 1 || q.Running {
		t.Fatalf("ran without agreeing: %d rows (%v), running %v", n, err, q.Running)
	}
}

// On staging, a T-SQL batch that changes rows and commits them itself
// asks before it runs: the change limit's count would come after its
// COMMIT.
func TestIntegrationSQLServerBatchCommitsBeforeCount(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "sqlserver")
	cfg := sqlserverEditorConfig
	cfg.Env = db.Staging
	d := serverWith(t, cfg, []string{"DROP TABLE IF EXISTS dbo.rv_ms_guard", "CREATE TABLE dbo.rv_ms_guard (id INT PRIMARY KEY, v INT)",
		"INSERT INTO dbo.rv_ms_guard VALUES (1, 0), (2, 0), (3, 0)"}, "DROP TABLE IF EXISTS dbo.rv_ms_guard")
	a, tt, _, q := openEditor(t, cfg, 1200, 760, "UPDATE dbo.rv_ms_guard SET v = 1 WHERE id > 0\nCOMMIT")
	a.Settings().ChangeLimit = 2
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.Confirm != nil || !q.Running })
	var sum int
	if err := d.SQL.QueryRow("SELECT sum(v) FROM dbo.rv_ms_guard").Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if a.Confirm == nil || !slices.ContainsFunc(a.Confirm.Reasons, func(r string) bool { return strings.Contains(r, "its own transaction") }) || sum != 0 {
		t.Fatalf("asked %+v; sum %d", a.Confirm, sum)
	}
}

// A batch whose USE moved the session to another database says so, and
// the rows read there are not edited as the editor's database's table;
// once back, they are.
func TestIntegrationSQLServerUseMovesSession(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "sqlserver")
	cfg := sqlserverEditorConfig
	serverWith(t, cfg, []string{"DROP TABLE IF EXISTS dbo.rv_ms_moved", "CREATE TABLE dbo.rv_ms_moved (id INT PRIMARY KEY)",
		"INSERT INTO dbo.rv_ms_moved VALUES (1)",
		"IF OBJECT_ID('master.dbo.rv_ms_moved') IS NULL EXEC master.sys.sp_executesql N'CREATE TABLE dbo.rv_ms_moved (id INT PRIMARY KEY)'"},
		"DROP TABLE IF EXISTS dbo.rv_ms_moved", "DROP TABLE IF EXISTS master.dbo.rv_ms_moved")
	_, tt, _, q := openEditor(t, cfg, 1200, 760, "")
	t.Cleanup(func() { q.sess.Close() })
	read := func(use string) string {
		t.Helper()
		q.Editor.SetText(use)
		runWith(t, tt, q, "USE", RunStatement)
		q.Editor.SetText("SELECT * FROM dbo.rv_ms_moved")
		runWith(t, tt, q, "SELECT", RunStatement)
		testutil.WaitFor(t, tt, "the result", func() bool {
			return len(q.results) == 1 && q.results[0].view != nil && q.results[0].view.ReadOnly() != dataview.ReadingColumns
		})
		return q.results[0].view.ReadOnly()
	}
	if why := read("USE master"); !strings.Contains(why, "master") {
		t.Fatalf("rows of master's table edit as shop's: %q", why)
	}
	if !slices.ContainsFunc(q.messages, func(m message) bool { return strings.Contains(m.text, "works in master") }) {
		t.Fatalf("messages %+v", q.messages)
	}
	if why := read("USE shop"); why != "" {
		t.Fatalf("back in shop, rows not edited: %q", why)
	}
}

// Where the change limit counts a change's rows, a T-SQL batch that makes
// several changes, or begins or ends its own transaction, asks first
// instead; one change keeps being counted, and other engines' statements
// are as before. A batch that moves the session and then reads shows its
// rows.
func TestSQLServerBatchGuardAndRows(t *testing.T) {
	staging := db.Config{Name: "ms", Engine: db.SQLServer, Env: db.Staging}
	for _, c := range []struct {
		cfg   db.Config
		limit int
		sql   string
		want  string
	}{
		{staging, 2, "INSERT INTO t VALUES (1)\nUPDATE t SET a = 1 WHERE id > 0", "several changes"},
		{staging, 2, "UPDATE t SET a = 1 WHERE id > 0\nUPDATE t SET b = 1 WHERE id > 0", "several changes"},
		{staging, 2, "UPDATE t SET a = 1 WHERE id > 0\nCOMMIT", "its own transaction"},
		{staging, 2, "UPDATE t SET a = 1 WHERE id > 0\nSELECT 1", ""},
		{staging, 0, "INSERT INTO t VALUES (1)\nUPDATE t SET a = 1 WHERE id > 0", ""},
		{db.Config{Name: "ms", Engine: db.SQLServer, Env: db.Development}, 2, "INSERT INTO t VALUES (1)\nUPDATE t SET a = 1 WHERE id > 0", ""},
		{db.Config{Name: "pg", Engine: db.Postgres, Env: db.Staging}, 2, "UPDATE t SET a = 1 WHERE id > 0", ""},
	} {
		s := safety.Analyze(&c.cfg, []string{c.sql})[0]
		got := uncountedReason(c.cfg, c.limit, s, db.LexDialect(c.cfg.Engine))
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("%s %q, limit %d: %q, want %q", c.cfg.Env, c.sql, c.limit, got, c.want)
		}
	}
	for sql, want := range map[string]bool{"USE master\nSELECT 1 AS a": true, "SET NOCOUNT ON\nSELECT 1 AS a": true, "USE master": false,
		"USE master\nUPDATE t SET a = 1 WHERE id = 1": false} {
		if got := wantsRows(safety.Analyze(&staging, []string{sql})[0], sqltext.TSQL); got != want {
			t.Errorf("rows of %q: %v, want %v", sql, got, want)
		}
	}
	pg := db.Config{Engine: db.Postgres}
	if wantsRows(safety.Analyze(&pg, []string{"SET search_path = s"})[0], sqltext.Postgres) {
		t.Error("PostgreSQL's SET returns rows")
	}
}

// On staging, a batch of an INSERT and an UPDATE of many rows asks before
// it runs, and runs nothing when declined; a batch that moves to master
// and reads shows the rows.
func TestIntegrationSQLServerSeveralChangesAsk(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "sqlserver")
	cfg := sqlserverEditorConfig
	cfg.Env = db.Staging
	d := serverWith(t, cfg, []string{"DROP TABLE IF EXISTS dbo.rv_ms_several", "CREATE TABLE dbo.rv_ms_several (id INT PRIMARY KEY, v INT)",
		"INSERT INTO dbo.rv_ms_several VALUES (1, 0), (2, 0), (3, 0)"}, "DROP TABLE IF EXISTS dbo.rv_ms_several")
	a, tt, _, q := openEditor(t, cfg, 1200, 760, "INSERT INTO dbo.rv_ms_several VALUES (4, 0)\nUPDATE dbo.rv_ms_several SET v = 1 WHERE id > 0")
	a.Settings().ChangeLimit = 2
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.Confirm != nil || !q.Running })
	var n, sum int
	if err := d.SQL.QueryRow("SELECT count(*), sum(v) FROM dbo.rv_ms_several").Scan(&n, &sum); err != nil {
		t.Fatal(err)
	}
	if a.Confirm == nil || !slices.ContainsFunc(a.Confirm.Reasons, func(r string) bool { return strings.Contains(r, "several changes") }) || n != 3 || sum != 0 {
		t.Fatalf("asked %+v; %d rows, sum %d", a.Confirm, n, sum)
	}
	a.Confirm.Open, a.Confirm = false, nil
	tt.Frame()

	q.Editor.SetText("USE master\nSELECT 1 AS a")
	runWith(t, tt, q, "USE", RunStatement)
	if len(q.results) != 1 || q.results[0].view == nil || len(q.results[0].view.Rows()) != 1 {
		t.Fatalf("results %+v; messages %+v", q.results, q.messages)
	}
}

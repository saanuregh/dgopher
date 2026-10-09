package query

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/safety"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/editor"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

func TestAutocomplete(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := newFakeQueryHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "")

	tt.Frame()
	tt.Type("SELECT * FROM shop.cu")
	tt.Frame()
	t.Logf("text %q focus %v open %v items %d", q.Editor.Text, q.Editor.HasFocus, q.ac.open, len(q.ac.items))
	testutil.WaitFor(t, tt, "table suggestions", func() bool { return q.ac.open && len(q.ac.items) > 0 })
	if q.ac.items[0].label != "customers" {
		t.Fatalf("suggestions %+v", q.ac.items)
	}
	testutil.Snapshot(t, tt, "autocomplete-table")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !strings.HasSuffix(q.Editor.Text, "shop.customers") || q.ac.open {
		t.Fatalf("after accepting: %q open %v", q.Editor.Text, q.ac.open)
	}
	tt.Type(" c WHERE c.em")
	testutil.WaitFor(t, tt, "column suggestions", func() bool { return q.ac.open && len(q.ac.items) > 0 && q.ac.items[0].label == "email" })
	testutil.Snapshot(t, tt, "autocomplete-column")
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if !strings.HasSuffix(q.Editor.Text, "c.email") {
		t.Fatalf("after accepting a column: %q", q.Editor.Text)
	}
}

func TestChart(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := newFakeQueryHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.view, 1360, 820)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	// The kinds a chart picks are TestChartDetect's; these are its looks.
	for _, c := range []struct{ name, sql string }{
		{"chart-bar", "SELECT country, count(*) AS customers, sum(o.total) / 100 AS revenue_hundreds FROM shop.customers c JOIN shop.orders o ON o.customer_id = c.id GROUP BY 1 ORDER BY 1"},
		{"chart-line", "SELECT date_trunc('day', placed_at) AS day, count(*) AS orders, avg(total) AS avg_total FROM shop.orders GROUP BY 1 ORDER BY 1"},
		{"chart-pie", "SELECT status, sum(total) AS revenue FROM shop.orders GROUP BY 1"},
	} {
		q := newEditor(t, a, tt, cn, c.sql)
		testutil.SetCaret(tt, &q.Editor, 3)
		q.Run(RunStatement)
		testutil.WaitFor(t, tt, "result", func() bool { return !q.Running && len(q.results) == 1 })
		r := q.results[0]
		if r.err != "" {
			t.Fatal(r.err)
		}
		q.editorH = 120
		tt.Frame()
		if err := tt.Click("Chart"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if !tt.HasText("Series") {
			t.Fatalf("%s: no chart", c.name)
		}
		if c.name == "chart-pie" {
			if err := tt.Click("Pie"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			testutil.Snapshot(t, tt, c.name)
			continue
		}
		tt.Move(900, 560)
		tt.Frame()
		testutil.Snapshot(t, tt, c.name)
	}
}

// A transaction outlives the results of its session: running a statement
// while an earlier result still has rows to read must not lose it.
func TestTransactionSurvivesNewRun(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	for _, cfg := range []db.Config{
		testutil.PGConfig(),
		{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop", Env: db.Production},
	} {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			cfg.Env = db.Production // manual commit
			ctx := context.Background()
			d, err := db.Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			table := db.QualifiedName(d.Dialect, "shop", "tx_probe")
			for _, q := range []string{"DROP TABLE IF EXISTS " + table, "CREATE TABLE " + table + " (id INT PRIMARY KEY, v INT)"} {
				if _, err := d.SQL.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			for i := range 3000 {
				d.SQL.Exec(fmt.Sprintf("INSERT INTO %s VALUES (%d, 0)", table, i))
			}
			d.Close()
			a := newFakeQueryHost(t)
			cn := a.AddConn(cfg)
			tt := ui.NewTester(a.view, 1200, 760)
			q := newEditor(t, a, tt, cn, "")
			run := func(sql string) {
				q.execute(safety.Analyze(&cn.Config, []string{sql}), !strings.HasPrefix(sql, "SELECT"))
				testutil.WaitFor(t, tt, sql, func() bool { return !q.Running })
			}
			run("UPDATE " + table + " SET v = 1 WHERE id = 7")
			if q.Tx != db.TxOpen {
				t.Fatalf("tx %v after the update", q.Tx)
			}
			run("SELECT * FROM " + table) // more rows than a page: the cursor stays open
			if r := q.results[0]; r.view.Done() {
				t.Fatal("the result was read whole; the test needs an open cursor")
			}
			run("SELECT v FROM " + table + " WHERE id = 7")
			if q.Tx != db.TxOpen || db.Display(q.results[0].view.Rows()[0][0]) != "1" {
				t.Fatalf("after a new run: tx %v, v %v, messages %+v", q.Tx, q.results[0].view.Rows(), q.messages)
			}
			q.endTx(false)
			testutil.WaitFor(t, tt, "rollback", func() bool { return !q.Running && q.Tx == db.TxNone })
		})
	}
}

func TestFindInEditor(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := New(a, cn, "", "q", "SELECT éa FROM t;\nselect b FROM u;\n-- Select me")
	a.AddTab(q)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Frame()
	tt.Type("select")
	tt.Frame()
	tt.Frame()
	if len(q.find.Matches) != 3 || q.find.Matches[1] != 18 {
		t.Fatalf("matches %v", q.find.Matches)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	if q.find.Current != 1 || q.Editor.SelStart != 18 || q.Editor.SelEnd != 24 {
		t.Fatalf("current %d, selection %d-%d", q.find.Current, q.Editor.SelStart, q.Editor.SelEnd)
	}
	testutil.Snapshot(t, tt, "find")
}

func TestReplaceInEditor(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := New(a, cn, "", "q", "select a FROM t;\nselect b FROM u;\nselect c")
	a.AddTab(q)
	tt.Frame()
	tt.Key(ui.Cmd|ui.Alt, ui.KeyF)
	tt.Frame()
	tt.Type("select")
	tt.Frame()
	if !q.find.Replacing || len(q.find.Matches) != 3 {
		t.Fatalf("replacing %v, matches %v", q.find.Replacing, q.find.Matches)
	}
	q.find.Replacement = "SELECT DISTINCT"
	tt.Frame()
	// A replacement holding the query is not found again: the next match
	// is the second statement's.
	if err := tt.Click("Replace"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if q.Editor.Text != "SELECT DISTINCT a FROM t;\nselect b FROM u;\nselect c" || q.find.Current != 1 || q.Editor.SelStart != 26 {
		t.Fatalf("after Replace: %q, current %d, selection at %d", q.Editor.Text, q.find.Current, q.Editor.SelStart)
	}
	testutil.Snapshot(t, tt, "replace")
	before := q.Editor.Text
	if err := tt.Click("Replace All"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// Finding ignores case, so the statement replaced already matches too.
	if q.Editor.Text != "SELECT DISTINCT DISTINCT a FROM t;\nSELECT DISTINCT b FROM u;\nSELECT DISTINCT c" || a.Toasts[len(a.Toasts)-1] != "Replaced 3 matches" {
		t.Fatalf("after Replace All: %q, toasts %v", q.Editor.Text, a.Toasts)
	}
	a.ToastRun()
	tt.Frame()
	if q.Editor.Text != before {
		t.Fatalf("undo left %q", q.Editor.Text)
	}
}

func TestExplain(t *testing.T) {
	testutil.Integration(t)
	file := t.TempDir() + "/e.sqlite"
	os.WriteFile(file, nil, 0o600)
	for _, cfg := range []db.Config{
		testutil.PGConfig(),
		{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"},
		{ID: "ch", Name: "ch", Engine: db.ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher"},
		{ID: "sq", Name: "sq", Engine: db.SQLite, Database: file},
	} {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			a := newFakeQueryHost(t)
			cn := a.AddConn(cfg)
			tt := ui.NewTester(a.view, 1000, 700)
			q := newEditor(t, a, tt, cn, "SELECT 1 + 1 AS two;")
			testutil.SetCaret(tt, &q.Editor, 3)
			q.Run(RunExplain)
			testutil.WaitFor(t, tt, "plan", func() bool { return !q.Running && len(q.results) == 1 })
			if r := q.results[0]; r.err != "" || len(r.view.Rows()) == 0 {
				t.Fatalf("explain: %q %v", r.err, r.view.Rows())
			}
		})
	}
}

// A file runs only on the connection its header names: on another, the
// editor refuses to run, says why, and offers to switch.
func TestFileRunsOnlyOnItsConnection(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "alpha", Name: "Alpha", Engine: db.SQLite, Database: ":memory:"})
	a.AddConn(db.Config{ID: "beta", Name: "Beta", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "-- connection: beta\n\nSELECT 1 AS one;")
	tt.Frame()
	testutil.Snapshot(t, tt, "header-another-connection")
	testutil.SetCaret(tt, &q.Editor, 25)
	q.Run(RunStatement)
	tt.Frame()
	if len(q.results) != 0 || len(a.Errors) != 1 || !strings.Contains(a.Errors[0], "names the connection beta") {
		t.Fatalf("ran on another connection: results %d, errors %q", len(q.results), a.Errors)
	}
	q.exportFromQuery()
	if len(a.Errors) != 2 {
		t.Fatalf("exported from another connection: %q", a.Errors)
	}
	if err := tt.Click("Switch to beta"); err != nil {
		t.Fatal(err)
	}
	if len(a.switched) != 1 || a.switched[0] != "beta" {
		t.Fatalf("switched %v", a.switched)
	}

	q.Editor.Text = "-- connection: gamma\n\nSELECT 1 AS one;"
	tt.Frame()
	if !tt.HasText("which is not a SQL connection of") || tt.HasText("Switch to gamma") {
		t.Fatalf("a missing connection: %q", tt.Texts())
	}

	q.Editor.Text = "-- connection: alpha\n\nSELECT 1 AS one;"
	tt.Frame()
	if tt.HasText("This file names") {
		t.Fatal("the bar shows for the editor's own connection")
	}
	testutil.SetCaret(tt, &q.Editor, 25)
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && len(q.results) == 1 })
}

// Typing a header line completes the connections of the project.
func TestCompleteHeaderConnection(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "alpha", Name: "Alpha", Engine: db.SQLite, Database: ":memory:"})
	a.AddConn(db.Config{ID: "billing-prod", Name: "Billing", Engine: db.SQLite, Database: ":memory:", Env: db.Production})
	a.AddConn(db.Config{ID: "cache", Name: "Cache", Engine: db.Redis, Host: "localhost"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "-- connection: \n\nSELECT 1;")
	testutil.SetCaret(tt, &q.Editor, 15)
	tt.Type("b")
	tt.Frame()
	if !q.ac.open || len(q.ac.items) != 1 || q.ac.items[0].label != "billing-prod" {
		t.Fatalf("suggestions %+v", q.ac.items)
	}
	tt.Type("il")
	tt.Frame()
	if !q.ac.open || !q.Editor.HasFocus {
		t.Fatalf("typing the name: popup %v, focus %v", q.ac.open, q.Editor.HasFocus)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !strings.HasPrefix(q.Editor.Text, "-- connection: billing-prod\n") {
		t.Fatalf("after accepting: %q", q.Editor.Text)
	}
}

// The end of a run asks for a notification: what ran and where, and the
// first error of a failed script, never its SQL.
func TestRunNotifies(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "Lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "SELECT 'secret' AS s;\nSELECT * FROM missing;")
	q.Run(RunScript)
	testutil.WaitFor(t, tt, "the script", func() bool { return !q.Running && len(a.Notified) == 1 })
	n := a.Notified[0]
	if !strings.HasPrefix(n, "Script failed: query on Lite") || !strings.Contains(n, "no such table") || strings.Contains(n, "secret") {
		t.Fatalf("notified %q", n)
	}
}

// A command's key changed in the settings opens it in place of its own.
func TestChangedKey(t *testing.T) {
	defer keymap.Use(nil)
	if errs := keymap.Use(map[string][]string{keymap.Find: {"Alt+F"}}); errs != nil {
		t.Fatal(errs)
	}
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := New(a, cn, "", "q", "SELECT 1")
	a.AddTab(q)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Frame()
	if q.find.Open {
		t.Fatal("the key taken away still finds")
	}
	tt.Key(ui.Alt, ui.KeyF)
	tt.Frame()
	if !q.find.Open {
		t.Fatal("the key given does not find")
	}
}

// With Vim's keys, the editor moves and changes in normal mode, types in
// insert mode, and undoes and redoes; the app's commands keep their keys.
func TestVimInEditor(t *testing.T) {
	a := newFakeQueryHost(t)
	a.Settings().Vim = true
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := New(a, cn, "", "q", "select a, b from t")
	q.Editor.PendingSel = &[2]int{0, 0}
	a.AddTab(q)
	testutil.WaitFor(t, tt, "the editor's focus", func() bool { return q.Editor.HasFocus && q.Editor.Vim != nil })
	press := func(s string) {
		t.Helper()
		tt.Type(s)
		tt.Frame()
		tt.Frame()
	}
	press("w")
	if q.Editor.SelEnd != 7 {
		t.Fatalf("w: caret at %d", q.Editor.SelEnd)
	}
	press("cwx")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()
	if q.Editor.Text != "select x, b from t" || q.Editor.Vim.Mode != editor.VimNormal {
		t.Fatalf("cw: %q, mode %v", q.Editor.Text, q.Editor.Vim.Mode)
	}
	press("u")
	if q.Editor.Text != "select a, b from t" {
		t.Fatalf("u: %q", q.Editor.Text)
	}
	tt.Key(ui.Ctrl, ui.KeyR)
	tt.Frame()
	tt.Frame()
	if q.Editor.Text != "select x, b from t" {
		t.Fatalf("Ctrl+R: %q", q.Editor.Text)
	}
	testutil.Snapshot(t, tt, "vim-normal")
	press("$")
	if !testutil.HasTextContaining(tt, "-- NORMAL --") {
		t.Fatalf("no mode shown: %q", tt.Texts())
	}
	tt.Key(ui.Cmd, ui.KeyEnter)
	testutil.WaitFor(t, tt, "the run", func() bool { return len(q.messages) > 0 && !q.Running })
	if !strings.Contains(q.messages[len(q.messages)-1].text, "no such table") {
		t.Fatalf("ran as %+v", q.messages)
	}
	tt.Frame()
	if said := tt.Announcements(); !slices.ContainsFunc(said, func(s string) bool { return strings.Contains(s, "no such table") }) {
		t.Fatalf("the run's end not announced: %q", said)
	}
	a.Settings().Vim = false
	tt.Frame()
	if q.Editor.Vim != nil {
		t.Fatal("Vim kept after the setting went off")
	}
}

// Several carets: added on the lines below, they type and delete alike;
// every occurrence of a word selected is typed over at once; arrows move
// them all, and Esc leaves the editor's own.
func TestCursors(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := New(a, cn, "", "q", "select a;\nselect b;\nselect c;")
	q.Editor.PendingSel = &[2]int{0, 0}
	a.AddTab(q)
	testutil.WaitFor(t, tt, "the editor's focus", func() bool { return q.Editor.HasFocus })
	step := func() {
		tt.Frame()
		tt.Frame()
	}
	tt.Key(ui.Cmd|ui.Alt, ui.KeyDown)
	step()
	tt.Key(ui.Cmd|ui.Alt, ui.KeyDown)
	step()
	tt.Type("x")
	step()
	if q.Editor.Text != "xselect a;\nxselect b;\nxselect c;" {
		t.Fatalf("typed at the carets: %q", q.Editor.Text)
	}
	tt.Key(0, ui.KeyBackspace)
	step()
	if q.Editor.Text != "select a;\nselect b;\nselect c;" {
		t.Fatalf("deleted at the carets: %q", q.Editor.Text)
	}
	tt.Key(0, ui.KeyEscape)
	step()
	if q.Editor.HasCursors() {
		t.Fatal("Esc kept the carets")
	}

	tt.Key(ui.Cmd|ui.Shift, ui.KeyL)
	step()
	tt.Type("SELECT")
	step()
	if q.Editor.Text != "SELECT a;\nSELECT b;\nSELECT c;" {
		t.Fatalf("typed over the word: %q", q.Editor.Text)
	}
	tt.Key(0, ui.KeyRight)
	step()
	tt.Type("_")
	step()
	if q.Editor.Text != "SELECT _a;\nSELECT _b;\nSELECT _c;" {
		t.Fatalf("moved and typed: %q", q.Editor.Text)
	}
	testutil.Snapshot(t, tt, "cursors")
	tt.Key(0, ui.KeyEscape)
	step()
	tt.Type("!")
	step()
	if strings.Count(q.Editor.Text, "!") != 1 {
		t.Fatalf("after Esc, typed at %q", q.Editor.Text)
	}
}

// A block folds to its first line and a mark, the text whole beneath;
// typing before it moves it, a selection set inside it opens it, and a
// text the app changes opens every fold.
func TestFolding(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	text := "select a,\n  b\nfrom t;\n\nselect 2;"
	q := New(a, cn, "", "q", text)
	q.Editor.PendingSel = &[2]int{0, 0}
	a.AddTab(q)
	testutil.WaitFor(t, tt, "the editor's focus", func() bool { return q.Editor.HasFocus })
	step := func() {
		tt.Frame()
		tt.Frame()
	}
	tt.Key(ui.Cmd|ui.Alt, ui.KeyBracketLeft)
	step()
	if !q.Editor.Folded() || q.Editor.Text != text || q.Editor.ViewLine(len([]rune(text))) != 2 {
		t.Fatalf("folded %v, text %q, last line shown at %d", q.Editor.Folded(), q.Editor.Text, q.Editor.ViewLine(len([]rune(text))))
	}
	testutil.Snapshot(t, tt, "folded")
	tt.Type("x")
	step()
	if q.Editor.Text != "x"+text || !q.Editor.Folded() {
		t.Fatalf("typed before the fold: %q, folded %v", q.Editor.Text, q.Editor.Folded())
	}
	q.Editor.PendingSel = &[2]int{14, 14}
	step()
	if q.Editor.Folded() || q.Editor.SelEnd != 14 {
		t.Fatalf("a selection inside left it folded: %v, caret %d", q.Editor.Folded(), q.Editor.SelEnd)
	}
	// The caret after the block stays there as it folds and opens.
	end := len([]rune(q.Editor.Text))
	q.Editor.PendingSel = &[2]int{end, end}
	step()
	q.Editor.FoldAll()
	step()
	q.Editor.UnfoldAll()
	step()
	tt.Type("Z")
	step()
	if !strings.HasSuffix(q.Editor.Text, "select 2;Z") {
		t.Fatalf("typed after folding and opening: %q", q.Editor.Text)
	}
	q.Editor.FoldAll()
	step()
	q.Editor.Text = "select 1;"
	step()
	if q.Editor.Folded() {
		t.Fatal("the app's text kept the folds")
	}
}

// On staging, an UPDATE changing more rows than the limit holds them in a
// transaction and asks: Cancel rolls it back, Commit keeps it.
func TestLargeChangeAsks(t *testing.T) {
	a := newFakeQueryHost(t)
	a.Settings().ChangeLimit = 2
	file := filepath.Join(t.TempDir(), "big.sqlite")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file, Env: db.Staging})
	tt := ui.NewTester(a.view, 1000, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	for _, s := range []string{`CREATE TABLE t (id INTEGER PRIMARY KEY, a INTEGER)`, `INSERT INTO t VALUES (1, 0), (2, 0), (3, 0)`} {
		if _, err := cn.DB.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	sum := func() int {
		var n int
		cn.DB.SQL.QueryRow(`SELECT sum(a) FROM t`).Scan(&n)
		return n
	}
	q := New(a, cn, "", "q", "UPDATE t SET a = 1 WHERE id > 0")
	a.AddTab(q)
	tt.Frame()
	for _, commit := range []bool{false, true} {
		a.Confirm = nil
		q.Run(RunStatement)
		testutil.WaitFor(t, tt, "the question", func() bool { return a.Confirm != nil })
		if !strings.Contains(strings.Join(a.Confirm.Reasons, " "), "3 rows") {
			t.Fatalf("asked %+v", a.Confirm)
		}
		if commit {
			a.Confirm.OnConfirm()
		} else {
			a.Confirm.OnCancel()
		}
		a.Confirm.Open = false
		testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
		if want := map[bool]int{false: 0, true: 3}[commit]; sum() != want {
			t.Fatalf("committed %v: sum %d", commit, sum())
		}
	}

	// Within the limit, it commits without asking.
	a.Confirm = nil
	q.Editor.Text = "UPDATE t SET a = 2 WHERE id = 1"
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
	if a.Confirm != nil || sum() != 4 {
		t.Fatalf("a small change asked %v, or did not commit: sum %d", a.Confirm != nil, sum())
	}

	// An upsert changes rows already there, and asks; an INSERT only adds.
	a.Confirm = nil
	q.Editor.Text = "INSERT INTO t VALUES (1, 5), (2, 5), (3, 5) ON CONFLICT (id) DO UPDATE SET a = excluded.a"
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.Confirm != nil })
	a.Confirm.OnCancel()
	a.Confirm.Open = false
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
	if sum() != 4 {
		t.Fatalf("the declined upsert committed: sum %d", sum())
	}
	a.Confirm = nil
	q.Editor.Text = "INSERT INTO t VALUES (4, 0), (5, 0), (6, 0)"
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
	if a.Confirm != nil {
		t.Fatal("an INSERT asked")
	}

	// One connection every session shares is not guarded: the guard's
	// transaction would be every tab's.
	mem := a.AddConn(db.Config{ID: "mem", Name: "mem", Engine: db.SQLite, Database: ":memory:", Env: db.Staging})
	a.Connect(mem, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return mem.Status == connection.StatusConnected })
	if _, err := mem.DB.SQL.Exec(`CREATE TABLE t (a INTEGER); INSERT INTO t VALUES (0), (0), (0)`); err != nil {
		t.Fatal(err)
	}
	m := New(a, mem, "", "m", "UPDATE t SET a = 1 WHERE a = 0")
	a.AddTab(m)
	m.Run(RunStatement)
	testutil.WaitFor(t, tt, "the run", func() bool { return !m.Running })
	if a.Confirm != nil {
		t.Fatal("a shared connection's change asked")
	}
}

// On MySQL, a change to a table that keeps no transactions, as MyISAM's,
// is not held: it could not roll back; an InnoDB table's is.
func TestIntegrationLargeChangeMySQL(t *testing.T) {
	testutil.Integration(t)
	a := newFakeQueryHost(t)
	a.Settings().ChangeLimit = 2
	cn := a.AddConn(db.Config{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher",
		Database: "shop", Env: db.Staging})
	tt := ui.NewTester(a.view, 1000, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	for _, engine := range []string{"MyISAM", "InnoDB"} {
		for _, s := range []string{`DROP TABLE IF EXISTS it_guard`, `CREATE TABLE it_guard (id INT PRIMARY KEY, a INT) ENGINE=` + engine,
			`INSERT INTO it_guard VALUES (1, 0), (2, 0), (3, 0)`} {
			if _, err := cn.DB.SQL.Exec(s); err != nil {
				t.Fatal(s, err)
			}
		}
		a.Confirm = nil
		q := New(a, cn, "", "q", "UPDATE it_guard SET a = 1 WHERE id > 0")
		a.AddTab(q)
		q.Run(RunStatement)
		testutil.WaitFor(t, tt, "the run or its question", func() bool { return a.Confirm != nil || len(q.messages) > 0 && !q.Running })
		if (a.Confirm != nil) != (engine == "InnoDB") {
			t.Fatalf("%s: asked %v", engine, a.Confirm != nil)
		}
		if a.Confirm != nil {
			a.Confirm.OnCancel()
			a.Confirm.Open = false
			testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
		}
		var sum int
		cn.DB.SQL.QueryRow(`SELECT sum(a) FROM it_guard`).Scan(&sum)
		if want := map[string]int{"MyISAM": 3, "InnoDB": 0}[engine]; sum != want {
			t.Fatalf("%s: sum %d", engine, sum)
		}
	}
	cn.DB.SQL.Exec(`DROP TABLE IF EXISTS it_guard`)
}

// The commit key commits the open transaction; rolling back has no key
// until one is set.
func TestTransactionKeys(t *testing.T) {
	a := newFakeQueryHost(t)
	path := filepath.Join(t.TempDir(), "tx.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil { // an empty SQLite database
		t.Fatal(err)
	}
	cfg := db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: path, Env: db.Production}
	cn := a.AddConn(cfg)
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "")
	run := func(sql string) {
		q.execute(safety.Analyze(&cn.Config, []string{sql}), true)
		testutil.WaitFor(t, tt, sql, func() bool { return !q.Running })
	}
	run("CREATE TABLE t (id INTEGER PRIMARY KEY)")
	run("INSERT INTO t VALUES (1)")
	if q.Tx != db.TxOpen {
		t.Fatalf("tx %v after the insert", q.Tx)
	}
	if keymap.First(keymap.Rollback) != "" {
		t.Fatal("rolling back has a key by default")
	}
	tt.Key(ui.Cmd|ui.Alt|ui.Shift, ui.KeyEnter)
	testutil.WaitFor(t, tt, "the commit", func() bool { return !q.Running && q.Tx == db.TxNone })
	if !slices.ContainsFunc(q.messages, func(m message) bool { return strings.Contains(m.text, "ommit") }) {
		t.Fatalf("messages %+v", q.messages)
	}
}

// The palette moves between the result tabs, and closes the one shown.
func TestResultCommands(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "SELECT 1;\nSELECT 2;")
	q.Run(RunScript)
	testutil.WaitFor(t, tt, "two results", func() bool { return !q.Running && len(q.results) == 2 })
	run := func(title string) {
		t.Helper()
		i := slices.IndexFunc(q.Commands(), func(c widgets.Command) bool { return c.Title == title })
		if i < 0 {
			t.Fatalf("no %s", title)
		}
		q.Commands()[i].Run()
		tt.Frame()
	}
	from := q.resultIdx
	run("Next Result")
	if q.resultIdx == from {
		t.Fatalf("Next Result stayed on %d", from)
	}
	run("Next Result")
	if q.resultIdx != from {
		t.Fatalf("Next Result did not come round to %d: %d", from, q.resultIdx)
	}
	run("Close Result")
	testutil.WaitFor(t, tt, "one result", func() bool { return len(q.results) == 1 })
}

// The palette moves the editor to another schema, as its switcher does.
func TestSwitchSchemaCommand(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "")
	testutil.WaitFor(t, tt, "the first schemas", func() bool { _, ok := cn.Schemas[""]; return ok })
	if _, err := cn.DB.SQL.Exec(`CREATE SCHEMA s2`); err != nil {
		t.Fatal(err)
	}
	connection.LoadSchemas(a, cn, "", nil)
	testutil.WaitFor(t, tt, "the schemas", func() bool { return slices.Contains(cn.Schemas[""], "s2") })
	i := slices.IndexFunc(q.Commands(), func(c widgets.Command) bool { return c.Title == "Use Schema s2" })
	if i < 0 {
		t.Fatal("no Use Schema s2")
	}
	q.Commands()[i].Run()
	testutil.WaitFor(t, tt, "the switch", func() bool { return !q.Running && q.schema == "s2" })
}

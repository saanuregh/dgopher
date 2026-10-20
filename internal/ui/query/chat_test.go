package query

import (
	"slices"
	"strings"
	"testing"
	"time"

	agent "github.com/saanuregh/agent-cli-connector"
	"github.com/saanuregh/agent-cli-connector/agenttest"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/chat"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The editor's chat opens beside it with the keys, which the editor's own
// keys then leave alone; what it is sent carries the statement at the
// caret, and what it inserts replaces the selection. The key again hides
// it, the keys going back to the editor.
func TestEditorChat(t *testing.T) {
	saved := chat.NewProvider
	t.Cleanup(func() { chat.NewProvider = saved })
	got := make(chan string, 1)
	chat.NewProvider = func(string) agent.Provider {
		return agenttest.New(func(turn *agenttest.Turn) error {
			got <- turn.Input().PlainText()
			turn.Text("```sql\nSELECT 2 AS two;\n```")
			return nil
		})
	}
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1400, 800, "SELECT 1;\n\nSELECT 41 + 1;\n")
	q.Editor.Select(14, 14)
	q.ToggleChat()
	testutil.WaitFor(t, tt, "the chat's focus", func() bool { return q.ChatOpen() && q.chat.focused })
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()
	if q.Running || len(q.results) != 0 {
		t.Fatal("the editor's key ran a statement from the chat")
	}
	tt.Type("what is this")
	tt.Key(0, ui.KeyEnter)
	var sent string
	testutil.WaitFor(t, tt, "the message", func() bool {
		select {
		case sent = <-got:
			return true
		default:
			return false
		}
	})
	if !strings.Contains(sent, "Statement at the caret, line 3:\n```sql\nSELECT 41 + 1\n") || !strings.HasSuffix(sent, "what is this") {
		t.Fatalf("sent:\n%s", sent)
	}
	testutil.WaitFor(t, tt, "the reply", func() bool { return tt.HasText("Insert") })
	q.Editor.Select(0, 9)
	tt.Frame()
	if err := tt.Click("Replace Selection"); err != nil {
		t.Fatal(err)
	}
	if text := q.Editor.Text(); text != "SELECT 2 AS two;\n\nSELECT 41 + 1;\n" {
		t.Fatalf("text %q", text)
	}
	testutil.WaitFor(t, tt, "the editor's focus", func() bool { return q.Editor.HasFocus() })
	q.ToggleChat()
	testutil.WaitFor(t, tt, "the chat's focus", func() bool { return q.chat.focused })
	q.ToggleChat()
	testutil.WaitFor(t, tt, "the editor's focus", func() bool { return !q.ChatOpen() && q.Editor.HasFocus() })
	if q.Chat() == nil {
		t.Fatal("hiding the chat ended it")
	}
}

// A chart the chat asks for goes at the end of the script and runs
// there, its rows drawn as asked, the run the app's in the audit log; in
// the notebook view, it is a new cell's.
func TestEditorChart(t *testing.T) {
	h, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1400, 900, "SELECT 1;")
	sql := "SELECT 'a' AS k, 1 AS n UNION ALL SELECT 'b', 2"
	got := make(chan chat.ChartResult, 1)
	if why := q.ChartSQL(sql, dataview.ChartSettings{Kind: "Pie", X: "k", Series: []string{"n"}}, func(r chat.ChartResult) { got <- r }); why != "" {
		t.Fatal(why)
	}
	var res chat.ChartResult
	testutil.WaitFor(t, tt, "the chart", func() bool {
		select {
		case res = <-got:
			return true
		default:
			return false
		}
	})
	tt.Frame()
	if res.Err != "" || len(res.Rows) != 2 || q.Editor.Text() != "SELECT 1;\n\n"+sql+"\n" {
		t.Fatalf("result %+v, text %q", res, q.Editor.Text())
	}
	if !tt.HasText("Series") || !testutil.HasTextContaining(tt, "%") {
		t.Fatalf("no pie: %q", tt.Texts())
	}
	i := slices.IndexFunc(h.Events, func(e audit.Event) bool { return strings.Contains(e.Statement, "UNION ALL") })
	if i < 0 || h.Events[i].Origin != audit.OriginApp || h.Events[i].Purpose == "" {
		t.Fatalf("audited %+v", h.Events)
	}

	q.ToggleNotebook()
	tt.Frame()
	n := len(q.nb.cells)
	if why := q.ChartSQL("SELECT 3 AS n", dataview.ChartSettings{Kind: "Bar"}, func(r chat.ChartResult) { got <- r }); why != "" {
		t.Fatal(why)
	}
	testutil.WaitFor(t, tt, "the cell's chart", func() bool {
		select {
		case res = <-got:
			return true
		default:
			return false
		}
	})
	if res.Err != "" || len(q.nb.cells) != n+1 || q.nb.focused.ed.Text() != "SELECT 3 AS n" {
		t.Fatalf("result %+v, %d cells", res, len(q.nb.cells))
	}
}

// The chat opens at its width at once, never wider for a frame.
func TestChatOpensAtItsWidth(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1600, 800, "SELECT 1;")
	q.ToggleChat()
	for i := range 3 {
		tt.Frame()
		if r, ok := tt.Find("Ask about this editor"); ok && r.X < 1600-chatWidth-2 {
			t.Fatalf("frame %d: the chat starts at x %v, wider than %d", i, r.X, chatWidth)
		}
	}
	if r, ok := tt.Find("Ask about this editor"); !ok || r.X > 1600-chatWidth+40 {
		t.Fatalf("the chat did not open: %v %v", r, ok)
	}
}

// A chart the editor could not run is refused at once, the editor left as
// it was: the chart's tool hears why rather than waiting.
func TestEditorChartRefused(t *testing.T) {
	h, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1200, 800, "-- connection: other\n\nSELECT 1;\n")
	h.AddConn(db.Config{ID: "other", Name: "other", Engine: db.SQLite, Database: ":memory:"})
	tt.Frame()
	called := false
	why := q.ChartSQL("SELECT 2 AS n", dataview.ChartSettings{Kind: "Bar"}, func(chat.ChartResult) { called = true })
	if why == "" || !strings.Contains(why, "names the connection other") || called || q.chartAsk != nil {
		t.Fatalf("why %q, called %v", why, called)
	}
	if q.Editor.Text() != "-- connection: other\n\nSELECT 1;\n" || len(h.Errors) != 0 {
		t.Fatalf("text %q, errors %q", q.Editor.Text(), h.Errors)
	}
}

// A chart whose run never started is not taken for the user's next run:
// that one is the user's, audited as typed, and the chart hears it did
// not run.
func TestChartNotTakenByNextRun(t *testing.T) {
	h, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1200, 800, "SELECT 1 AS one;")
	got := make(chan chat.ChartResult, 1)
	// A parameter the user is asked for, and does not give.
	if why := q.ChartSQL("SELECT :n AS n", dataview.ChartSettings{Kind: "Bar"}, func(r chat.ChartResult) { got <- r }); why != "" {
		t.Fatal(why)
	}
	tt.Frame()
	if q.Running || q.chartAsk == nil {
		t.Fatalf("running %v, asked %v", q.Running, q.chartAsk != nil)
	}
	q.Editor.Select(0, 0)
	q.Run(RunStatement)
	select {
	case r := <-got:
		if r.Err == "" {
			t.Fatalf("the chart got the user's run: %+v", r)
		}
	default:
		t.Fatal("the chart was not told")
	}
	testutil.WaitFor(t, tt, "the user's run", func() bool { return !q.Running && len(q.results) > 0 })
	for _, e := range h.Events {
		if strings.Contains(e.Statement, "SELECT 1 AS one") && (e.Origin != audit.OriginTyped || e.Purpose != "") {
			t.Fatalf("the user's run audited as %q, %q", e.Origin, e.Purpose)
		}
	}
}

// A key pressed while the chart's run goes on leaves the chart its run.
func TestChartRunNotCancelledByRefusedRun(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1200, 800, "SELECT 1;")
	got := make(chan chat.ChartResult, 1)
	sql := "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 300000) SELECT count(*) AS c FROM n"
	if why := q.ChartSQL(sql, dataview.ChartSettings{Kind: "Bar"}, func(r chat.ChartResult) { got <- r }); why != "" {
		t.Fatal(why)
	}
	if !q.Running {
		t.Fatal("the chart's run is not going")
	}
	q.Run(RunStatement) // refused: a run goes on
	var res chat.ChartResult
	testutil.WaitFor(t, tt, "the chart", func() bool {
		select {
		case res = <-got:
			return true
		default:
			return false
		}
	})
	if res.Err != "" || len(res.Rows) != 1 {
		t.Fatalf("chart %+v", res)
	}
}

// The lines the chat's SQL lands on are tinted a moment, then the tint
// goes, asking for no frames once done.
func TestLandedTint(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1200, 800, "SELECT 1;\n")
	q.InsertSQL("SELECT 2\nFROM t;")
	tt.Advance(100 * time.Millisecond)
	tt.Frame()
	if q.landedIn != q.Editor || q.landedLines != [2]int{1, 2} || q.Editor.LandedStrength <= 0 {
		t.Fatalf("tinted %v lines %v at %v", q.landedIn == q.Editor, q.landedLines, q.Editor.LandedStrength)
	}
	tt.Advance(2 * time.Second)
	tt.Frame()
	if q.Editor.LandedStrength != 0 || q.flash != (widgets.Flash{}) {
		t.Fatal("the tint did not end")
	}
	q.ToggleNotebook()
	tt.Frame()
	if why := q.ChartSQL("SELECT 3 AS n", dataview.ChartSettings{Kind: "Bar"}, func(chat.ChartResult) {}); why != "" {
		t.Fatal(why)
	}
	if q.landedIn != q.nb.focused.ed || q.landedLines != [2]int{0, 0} {
		t.Fatal("the new cell's lines are not tinted")
	}
}

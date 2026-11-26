package query

import (
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/ui/editor"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// toolbar is the editor's bar, read as a sentence: where its statements
// run, what runs them, then what ends a transaction and what changes the
// view. Only what runs them gives way to the overflow menu in a narrow
// pane: where a statement runs and how its transaction ends stay in
// sight.
func (q *Tab) toolbar(c *ui.Context) {
	a := q.a
	t := c.Theme()
	ui.Row(c.Key("editor-bar")).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Toolbar(c, func() {
			q.runsOn(c)
			state := widgets.ConnectState{Connecting: q.Conn.Status == connection.StatusConnecting, Offline: !a.Online(), Compact: true}
			switch widgets.ConnectStatus(c, q.Conn, state) {
			case widgets.ConnectAsked:
				a.Connect(q.Conn, nil)
			case widgets.DisconnectAsked:
				a.Disconnect(q.Conn)
			case widgets.ConnectDetails:
				a.ShowReport(widgets.ConnectionReport(q.Conn, func() { a.Connect(q.Conn, nil) }, nil))
			}
		}).Label("Connection").Padding(4, 0, 4, 8).FontSize(widgets.TextBody).Shrink(0)
		ui.Divider(c).Margin(8, 6)
		widgets.Bar(c, "Editor", func() { q.runTools(c) }).Grow(1).Padding(4, 0).BorderWidth(0, 0, 0, 0)
		ui.Toolbar(c, func() {
			if q.InTx() || cfgManual(q.Conn) {
				q.EndButtons(c, &q.Conn.Config, q.busyWhy(), q.endOpenTx)
				ui.Divider(c).Margin(0, 4)
			}
			tip := "Show the editor as a notebook of cells, each with its results"
			if q.nb != nil {
				tip = "Show the editor as one script"
			}
			if widgets.ToggleIconButton(c.Key("view"), widgets.IconNotebook, "Notebook", q.nb != nil).Tooltip(keymap.Hint(tip, keymap.ToggleNotebook)).Clicked() {
				q.ToggleNotebook()
			}
			// The chat opens at the right: its switch is there too.
			tip = "Ask an AI agent about this editor"
			if q.ChatOpen() {
				tip = "Hide the AI chat"
			}
			if widgets.ToggleIconButton(c.Key("chat"), widgets.IconSparkles, "AI Chat", q.ChatOpen()).Tooltip(keymap.Hint(tip, keymap.ToggleChat)).Clicked() {
				if q.ChatOpen() {
					q.hideChat()
				} else {
					q.ToggleChat()
				}
			}
		}).Label("View").Padding(4, 8, 4, 4).FontSize(widgets.TextBody).Shrink(0)
	})
}

// runsOn names the connection the editor's statements run on, before the
// button that runs them: on staging and production in the environment's
// color, with its name, where a development connection takes a tint of
// its own color. It is no menu: an editor's file names its connection.
func (q *Tab) runsOn(c *ui.Context) {
	cfg := q.Conn.Config
	guarded := cfg.Env == db.Staging || cfg.Env == db.Production
	fill, ink := widgets.EnvironmentLabel(cfg.Env, widgets.SafetyColor(&cfg), c.Theme().Background)
	label, tip := "Runs on "+cfg.Name, "Statements of this editor run on "+cfg.Name
	if guarded {
		label += ", " + cfg.Env.Label()
		tip += ", a " + cfg.Env.Label() + " connection"
	}
	ui.Row(c.Key("runs-on")).Gap(6).Padding(3, 8).Radius(widgets.RadiusSmall).Background(fill).AlignItems(ui.Center).Shrink(0).
		Label(label).Tooltip(tip).Children(func() {
		if guarded {
			ui.Text(c, cfg.Env.Label()).FontSize(widgets.TextMeta).Bold().TextColor(ink)
		}
		ui.Text(c, cfg.Name).FontSize(widgets.TextSmall).FontWeight(600).TextColor(ink).SingleLine().MaxWidth(180)
	})
}

// runTools are the buttons that run the editor's text, the most used
// first: the toolbar's overflow menu takes them from the last.
func (q *Tab) runTools(c *ui.Context) {
	// One button: Run turns to Stop, in place, once a run lasts; a quick
	// one leaves it as it is. A notebook has its own.
	switch {
	case q.nb != nil:
		q.notebookBar(c)
	case q.Running && widgets.Lasted(c, q.StartedAt):
		q.stopButton(c, "Stop the running statement")
	default:
		if widgets.LeadToolButton(c.Key("run"), widgets.IconPlay, "Run", keymap.Hint("Run statement", keymap.Run), c.Theme().Success).Clicked() {
			q.Run(RunStatement)
		}
	}
	if q.nb == nil && widgets.ToolButton(c.Key("run-script"), widgets.IconLayers, "Run Script", keymap.Hint("Run all statements", keymap.RunScript)).Clicked() {
		q.Run(RunScript)
	}
	// One control: Explain, and beside it its other way, the plan measured
	// by a run.
	explain := ui.SplitButton(c.Key("explain"), "") // its label is built below, with its icon
	explain.Button.Label("Explain").Tooltip(keymap.Hint("Explain plan", keymap.Explain)).Children(func() {
		ui.Row(c).Gap(5).Children(func() {
			ui.Icon(c, widgets.IconCode).FontSize(widgets.IconMedium)
			ui.Text(c, "Explain").FontSize(widgets.TextBody)
		})
	})
	if explain.Button.Clicked() {
		q.Run(RunExplain)
	}
	explain.Arrow.Label("More ways to explain").Tooltip("More ways to explain").Menu(func(m *ui.Menu) {
		if keymap.Item(m.Item("Explain Plan"), keymap.Explain).Chosen() {
			q.Run(RunExplain)
		}
		if keymap.Item(m.Item("Explain Analyze"), keymap.ExplainAnalyze).Chosen() {
			q.Run(RunExplainAnalyze)
		}
		m.Separator()
		if keymap.Item(m.Item("Preview Changes"), keymap.PreviewChanges).Chosen() {
			q.Run(RunPreview)
		}
	})
	if widgets.ToolButton(c.Key("format"), widgets.IconWand, "Format", keymap.Hint("Format SQL", keymap.Format)).Clicked() {
		q.format()
	}
	// The database and the schema come once connected: at the end, where
	// their coming moves no button, least of all Run under the pointer
	// that pressed it to connect.
	ui.Spacer(c)
	q.switcherView(c)
}

// cfgManual reports whether the connection leaves its transactions to
// end by hand.
func cfgManual(cn *connection.Conn) bool {
	return cn.Config.Engine.Transactions() && cn.Config.ManualCommit()
}

// takeName lets an editor take a name dragged onto it, as a table's or a
// column's from the navigator: written over the selection, at the caret,
// and the editor takes the keys to go on typing. The caret, not the
// pointer, says where: the editor has no way from a point to a place in
// its text.
func (q *Tab) takeName(c *ui.Context, el ui.Element, ed *editor.Editor) {
	if _, over := ui.DragOver[widgets.NameDrag](el); over {
		el.Border(1, c.Theme().Accent)
	}
	d, ok := ui.Drop[widgets.NameDrag](el)
	if !ok || d.Text == "" {
		return
	}
	start, end := ed.Selection()
	ed.Replace(start, end, d.Text)
	ed.WantFocus = true
}

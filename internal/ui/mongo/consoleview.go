package mongo

import (
	"fmt"
	"slices"
	"strconv"

	"dgopher/internal/connection"
	"dgopher/internal/keymap"
	"dgopher/internal/mongosh"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// View builds the console: its bar, the editor, and the results of the
// last run under it.
func (t *ConsoleTab) View(c *ui.Context) {
	a := t.a
	if a.KeysTo(t) {
		if keymap.Pressed(c, keymap.Run) {
			t.Run()
		}
		if keymap.Pressed(c, keymap.RunScript) {
			t.RunScript()
		}
	}
	if t.conn.Doc() != nil && t.collsOf != t.Database {
		t.readNames()
	}
	ui.Column(c).Grow(1).Children(func() {
		t.toolbar(c)
		ui.SplitVerticalPercent(c, &t.editorPct, func() {
			ui.Column(c).Fill().Children(func() {
				t.syncRunLines()
				t.Editor.View(c, a.Settings().EditorFont)
			})
		}, func() {
			ui.Column(c).Fill().Children(func() { t.resultsView(c) })
		}).Grow(1)
	})
}

// syncRunLines puts a ▶ before each statement of the text, found again
// only when the text changes.
func (t *ConsoleTab) syncRunLines() {
	if text := t.Editor.Text(); text != t.linesText || t.runLines == nil {
		t.linesText, t.stmts, t.runLines = text, statementsOf(text, 0), []int{}
		for _, s := range t.stmts {
			line, _ := t.Editor.LineCol(s.from)
			t.runLines = append(t.runLines, line)
		}
	}
	stmts := t.stmts
	t.Editor.RunLines = t.runLines
	t.Editor.RunTip = keymap.Hint("Run the statement", keymap.Run)
	t.Editor.Run = func(line int) {
		for _, s := range stmts {
			if l, _ := t.Editor.LineCol(s.from); l == line {
				t.run([]statement{s})
				return
			}
		}
	}
}

// toolbar is the console's bar, as the SQL editor's: where statements
// run, what runs them, and the database they run on.
func (t *ConsoleTab) toolbar(c *ui.Context) {
	a := t.a
	th := c.Theme()
	ui.Row(c.Key("console-bar")).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
		ui.Toolbar(c, func() {
			widgets.RunsOn(c, &t.conn.Config, "console")
			state := widgets.ConnectState{Connecting: t.conn.Status == connection.StatusConnecting, Offline: !a.Online(), Compact: true}
			switch widgets.ConnectStatus(c, t.conn, state) {
			case widgets.ConnectAsked:
				a.Connect(t.conn, nil)
			case widgets.DisconnectAsked:
				a.Disconnect(t.conn)
			case widgets.ConnectDetails:
				a.ShowReport(widgets.ConnectionReport(t.conn, func() { a.Connect(t.conn, nil) }, nil))
			}
		}).Label("Connection").Padding(4, 0, 4, 8).FontSize(widgets.TextBody).Shrink(0)
		ui.Divider(c).Margin(8, 6)
		widgets.Bar(c, "Console", func() {
			if t.running && widgets.Lasted(c, t.runStart) {
				if widgets.TimedToolButton(c.Key("run"), widgets.IconStop, "Stop", "Stop the running statement", th.Danger, t.runStart).Clicked() {
					t.Stop()
				}
			} else if widgets.LeadToolButton(c.Key("run"), widgets.IconPlay, "Run", keymap.Hint("Run the statement at the caret, or those selected", keymap.Run), th.Success).Clicked() {
				t.Run()
			}
			if widgets.ToolButton(c.Key("run-script"), widgets.IconLayers, "Run Script", keymap.Hint("Run all statements", keymap.RunScript)).Clicked() {
				t.RunScript()
			}
			ui.Spacer(c)
			if t.conn.Doc() != nil && len(t.databases) > 0 {
				database := t.Database
				options := t.databases
				if !slices.Contains(options, database) {
					options = append([]string{database}, options...) // a database not made yet, as use makes it
				}
				if ui.Select(c, &database, options).Label("Database").Tooltip("The database statements run on, as use switches it").
					FontSize(widgets.TextBody).Changed() {
					t.SwitchDatabase(database)
				}
			}
		}).Grow(1).Padding(4, 0).BorderWidth(0, 0, 0, 0)
	})
}

// resultsView is a pill per result and the output, and the one chosen.
func (t *ConsoleTab) resultsView(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	var r *consoleResult
	if t.shown < len(t.results) {
		r = t.results[t.shown]
	}
	// The pills scroll in what the bar leaves them; the switch and Open
	// in keep their place, and the note shows only where there is room
	// for it besides the pills, else in Open in's tooltip.
	hasDocs := r != nil && (r.docs != nil || r.cursor != nil)
	openIn := ""
	if hasDocs && findCollection(r.op) != "" && t.OpenCollection != nil {
		openIn = "Open in " + r.database + "." + findCollection(r.op)
	}
	pillsW := float32(0)
	for _, res := range t.results {
		pillsW += pillWidth(res.title())
	}
	pillsW += pillWidth("Output")
	note := hasDocs && t.resultBarW-pillsW-modeSwitchWidth-pillWidth(openIn)-readOnlyNoteWidth > 0
	ui.Row(c.Key("result-bar")).Padding(widgets.PadToolbarY, widgets.PadToolbarX).Gap(4).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).
		Draw(func(_ *ui.Painter, rect ui.Rect) { t.resultBarW = rect.W }).Children(func() {
		ui.ScrollHorizontal(c.Key("result-pills")).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(2).Children(func() {
				for i, res := range t.results {
					if widgets.Pill(c.Key("result"+strconv.Itoa(i)), res.title(), i == t.shown).Tooltip(widgets.OneLine(res.op.Text, 200)).Clicked() {
						t.shown = i
					}
				}
				if widgets.Pill(c.Key("output"), "Output", r == nil).Tooltip("What ran, and errors").Clicked() {
					t.shown = len(t.results)
				}
			})
		})
		if r == nil || r.docs == nil && r.cursor == nil && r.reply == nil {
			return
		}
		r.view.modeButtons(c)
		if note {
			ui.Text(c, readOnlyNote).FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine().Shrink(0)
		}
		if openIn != "" {
			coll := findCollection(r.op)
			if widgets.ToolButton(c.Key("open-in"), widgets.IconArrowRight, openIn, "Browse and edit "+coll+"'s documents: these are read-only").Shrink(0).Clicked() {
				t.OpenCollection(r.database, coll)
			}
		}
	})
	if r == nil {
		t.outputView(c)
		return
	}
	docs := r.docs
	if docs == nil && r.cursor == nil {
		// A reply is one document, shown as a tree first.
		if r.gen == 0 {
			r.view.mode, r.gen = showTree, 1
		}
		docs = []bson.Raw{}
		if r.reply != nil {
			docs = []bson.Raw{r.reply}
		}
	}
	r.view.set(docs, r.gen)
	roots := func() []treeRoot {
		if r.docs == nil && r.cursor == nil {
			return []treeRoot{{doc: r.reply}}
		}
		return plainRoots(r.docs)
	}
	if r.reply == nil && r.docs == nil && r.cursor == nil {
		ui.Text(c, r.summary()).FontSize(widgets.TextSmall).TextColor(pal.Muted).Padding(10)
		ui.Spacer(c)
	} else {
		r.view.body(c, t.a, roots)
	}
	if r.err != "" {
		widgets.ErrorBanner(c, widgets.ErrorReport{Title: "Could not read more documents", Detail: r.err}, t.a.ShowReport)
	}
	if r.docs == nil && r.cursor == nil {
		return
	}
	ui.Row(c.Key("result-status")).Padding(widgets.PadStatusY, widgets.PadStatusX).Gap(6).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
		ui.Spacer(c)
		status := widgets.Count(len(r.docs), "document")
		if !r.done {
			status += " loaded"
		}
		ui.Text(c, fmt.Sprintf("%s in %s", status, widgets.FormatDuration(r.elapsed))).FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine()
		if r.done {
			return
		}
		busy := widgets.When(r.loading, "The documents are being read")
		if widgets.Clicked(widgets.StatusButton(c.Key("load-more"), "Load More").Tooltip(keymap.Hint("Read the next documents", keymap.FetchNext)), busy) {
			t.loadMore(r, false)
		}
		if widgets.Clicked(widgets.StatusButton(c.Key("fetch-all"), "Fetch All").Tooltip(keymap.Hint("Read every document", keymap.FetchAll)), busy) {
			t.loadMore(r, true)
		}
	})
	if keymap.ListPressed(c, &r.view.grid.List, keymap.FetchNext) {
		t.loadMore(r, false)
	}
	if keymap.ListPressed(c, &r.view.grid.List, keymap.FetchAll) {
		t.loadMore(r, true)
	}
}

// readOnlyNote says why a console's documents are not edited.
const readOnlyNote = "Read-only: open the collection to edit documents"

// The widths the result bar's parts take, roughly, to tell whether the
// note fits: a pill's or a button's from its label.
const (
	modeSwitchWidth   = 90
	readOnlyNoteWidth = 300
)

func pillWidth(label string) float32 {
	if label == "" {
		return 0
	}
	return float32(len([]rune(label))*7 + 28)
}

// findCollection is the collection a find reads, which Open in opens; ""
// for another statement.
func findCollection(op mongosh.Op) string {
	if op.Method == "find" && op.Collection != "" {
		return op.Collection
	}
	return ""
}

// outputView lists what ran and what failed.
func (t *ConsoleTab) outputView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	ui.Scroll(c).Grow(1).FillWidth().Padding(8, 12).Children(func() {
		if len(t.output) == 0 {
			ui.Text(c, "Write a statement, as db.orders.find({ status: \"paid\" }), and run it with "+widgets.KeyLabel(keymap.Hint("", keymap.Run))+".").
				FontSize(widgets.TextSmall).TextColor(pal.Muted)
			return
		}
		for i, l := range t.output {
			txt := ui.Text(c.Key("line"+strconv.Itoa(i)), l.text).Font(widgets.MonoFont).FontSize(widgets.TextSmall).Selectable()
			if l.err {
				txt.TextColor(c.Theme().Danger)
			}
		}
		if t.running {
			ui.Text(c, "Running…").FontSize(widgets.TextSmall).TextColor(pal.Muted)
		}
	})
}

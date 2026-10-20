package query

import (
	"slices"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/lists"
	"dgopher/internal/ui/chat"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/editor"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// editorChat is an editor's AI chat, beside it: open is whether it
// shows, focused whether it had the keys in the last frame, width the
// width it and the editor shared then.
type editorChat struct {
	*chat.Chat
	open    bool
	focused bool
	width   float32
}

// The chat's width, unless the user drags it to another, and the least
// it and the editor keep.
const (
	chatWidth     = 400
	chatMinWidth  = 300
	editorMinWide = 320
)

// ChatOpen reports whether the editor's AI chat shows.
func (q *Tab) ChatOpen() bool { return q.chat.Chat != nil && q.chat.open }

// Chat is the editor's AI chat, nil before it first opened.
func (q *Tab) Chat() *chat.Chat { return q.chat.Chat }

// ChatState is the editor's chat as a workspace keeps it: its agent, its
// session, to continue, and whether it shows; "" provider for none.
func (q *Tab) ChatState() (provider, session string, open bool) {
	if q.chat.Chat == nil {
		return "", "", false
	}
	return q.chat.Provider(), q.chat.SessionID(), q.chat.open
}

// RestoreChat brings back the editor's chat as a workspace kept it: its
// conversation shows again, and continues with the next message.
func (q *Tab) RestoreChat(provider, session string, open bool) {
	if !slices.ContainsFunc(chat.Providers, func(p chat.ProviderInfo) bool { return p.Name == provider }) {
		return
	}
	q.newChat(provider)
	q.chat.Resume(session)
	q.chat.open = open
}

// newChat makes the editor's chat, with an agent.
func (q *Tab) newChat(provider string) {
	dir := q.a.ChatDir()
	if p := q.Conn.Project; p != nil && p.Dir != "" {
		dir = p.Dir
	}
	q.chat = editorChat{Chat: chat.New(q.a, q, provider, dir)}
	q.chat.OnHide = q.hideChat
}

// ToggleChat shows the editor's AI chat with the keys in its message
// field; showing with the keys, it hides it, the keys going back to the
// editor.
func (q *Tab) ToggleChat() {
	if q.chat.Chat == nil {
		provider := q.a.Settings().ChatProvider
		if provider == "" {
			provider = chat.Providers[0].Name
		}
		q.newChat(provider)
	}
	switch {
	case !q.chat.open:
		q.chat.open = true
		q.chat.Focus()
	case !q.chat.focused:
		q.chat.Focus()
	default:
		q.hideChat()
	}
}

// hideChat hides the editor's chat, which keeps its conversation, the
// keys going back to the editor.
func (q *Tab) hideChat() {
	q.chat.open, q.chat.focused = false, false
	q.Editor.WantFocus = true
}

// EditorContext is what the editor shows, for its chat.
func (q *Tab) EditorContext() chat.EditorContext {
	e := chat.EditorContext{File: q.Title(), Database: q.Database, Text: q.FileText()}
	text := q.Editor.Text()
	lines := func(start, end int) (int, int) {
		first, _ := q.Editor.LineCol(start)
		last, _ := q.Editor.LineCol(max(start, end-1))
		return first + 1, last + 1
	}
	if start, end := q.Editor.Selection(); start != end {
		e.Selection = text[start:end]
		e.From, e.To = lines(start, end)
	} else if start, end, ok := q.currentStatement(); ok {
		e.Statement = text[start:end]
		e.From, e.To = lines(start, end)
	}
	if n := len(q.messages); n > 0 && q.messages[n-1].err {
		e.LastError = q.messages[n-1].text
	}
	return e
}

// InsertSQL puts SQL in place of the selection, or at the caret, as a
// change undo takes back, with the keys in the editor to run it.
func (q *Tab) InsertSQL(sql string) {
	start, end := q.Editor.Selection()
	q.Editor.Replace(start, end, sql)
	q.Editor.WantFocus = true
	q.landed(q.Editor, start, start+len(sql))
}

// landed tints a moment the lines of ed where the chat's SQL went, bytes
// from to to.
func (q *Tab) landed(ed *editor.Editor, from, to int) {
	first, _ := ed.LineCol(from)
	last, _ := ed.LineCol(max(from, to-1))
	q.landedIn, q.landedLines = ed, [2]int{first, last}
	q.flash.Start(q.a.Now())
}

// tintLanded has ed tint the lines where the chat's SQL landed, fading,
// when they are its.
func (q *Tab) tintLanded(c *ui.Context, ed *editor.Editor) {
	ed.LandedStrength = 0
	if ed == q.landedIn {
		ed.Landed, ed.LandedStrength = q.landedLines, q.flash.Strength(c)
	}
}

// chartAsk is a chart the AI chat asked for: the next run's rows drawn
// as settings say, which done is told of.
type chartAsk struct {
	settings dataview.ChartSettings
	done     func(chat.ChartResult)
}

// chartPurpose is why the audit log says a chart's read ran.
const chartPurpose = "a chart the AI chat asked for, as the user allowed"

// ChartSQL puts a read at the end of the script, selected, or in a new
// cell after the focused one, runs it as Run does, and shows its rows as
// a chart drawn as settings say. It returns why it does not, "" when it
// does.
func (q *Tab) ChartSQL(sql string, settings dataview.ChartSettings, done func(chat.ChartResult)) string {
	switch {
	case q.Running:
		return "a statement is running in the editor: try once it ends"
	case q.InTx():
		return "a transaction is open in the editor: the chart would show its uncommitted changes"
	case q.resultsWork() != "":
		return q.resultsWork()
	case q.anotherConnection() != "":
		return q.anotherConnection()
	case q.pendingCount() > 0:
		return "the editor's results have changes not applied, which the run would ask about: apply or discard them first"
	}
	q.chartSkipped() // a chart asked before whose run never came
	sql = strings.TrimSpace(sql)
	if nb := q.nb; nb != nil {
		at := len(nb.cells)
		if f := nb.focused; f != nil {
			at = nb.index(f) + 1
		}
		cl := q.addCell(at, false, sql)
		cl.ed.Select(0, 0)
		q.focusCell(cl)
		q.landed(cl.ed, 0, len(sql))
	} else {
		text := q.script.Text()
		sep := "\n\n"
		switch {
		case strings.TrimSpace(text) == "":
			sep = ""
		case strings.HasSuffix(text, "\n\n"):
			sep = ""
		case strings.HasSuffix(text, "\n"):
			sep = "\n"
		}
		start := len(text) + len(sep)
		q.script.Replace(len(text), len(text), sep+sql+"\n")
		q.script.Select(start, start+len(sql))
		q.landed(q.script, start, start+len(sql))
	}
	q.chartAsk = &chartAsk{settings: settings, done: done}
	q.chartRun = true
	q.Run(RunStatement)
	q.chartRun = false
	return ""
}

// chartRunEnded charts the rows of the run that ended, as the AI chat
// asked, and tells it of them.
func (q *Tab) chartRunEnded() {
	ask := q.chartAsk
	if ask == nil {
		return
	}
	q.chartAsk = nil
	var res chat.ChartResult
	for _, r := range q.results {
		if r.err != "" {
			res.Err = r.err
			break
		}
		if r.rowsMode && r.view != nil {
			r.view.ShowChart(ask.settings)
			res.Columns = lists.Map(r.view.Columns(), func(c db.ColumnInfo) string { return c.Name })
			res.Rows = r.view.Rows()
			q.showResults()
			break
		}
	}
	if res.Err == "" && res.Columns == nil {
		res.Err = "the statement returned no rows"
	}
	ask.done(res)
}

// chartSkipped tells the AI chat that the run it asked for a chart did
// not happen.
func (q *Tab) chartSkipped() {
	if ask := q.chartAsk; ask != nil {
		q.chartAsk = nil
		ask.done(chat.ChartResult{Err: "the statement did not run"})
	}
}

// chatPanel splits the tab: body, and the chat beside it when it shows,
// as wide as it was last dragged to, whatever the window's width. The
// frame around them is there either way, for its width to be known as
// the chat opens.
func (q *Tab) chatPanel(c *ui.Context, body func()) {
	open := q.ChatOpen()
	frame := ui.Column(c.Key("chat-frame")).Grow(1).MinWidth(0)
	frame.Children(func() {
		total := q.chat.width
		if !open || total <= 0 {
			q.chat.focused = false
			body()
			if open {
				c.Invalidate() // the chat, once the width is known
			}
			return
		}
		l := q.a.Layout()
		width := l.ChatWidth
		if width <= 0 {
			width = chatWidth
		}
		width = max(min(width, total-1-editorMinWide), min(chatMinWidth, total/2))
		editor := total - 1 - width
		th := c.Theme()
		split := ui.Split(c.Key("chat-split"), &editor, func() {
			ui.Column(c).Fill().Children(body)
		}, func() {
			panel := ui.Column(c.Key("chat")).Fill().Background(widgets.PaletteOf(c).Sidebar).BorderWidth(0, 0, 0, 1).BorderColor(th.Border)
			panel.Children(func() {
				q.chat.View(c, q.a.KeysTo(q) && q.chat.focused)
			})
			q.chat.focused = panel.FocusWithin()
		}).Grow(1)
		if split.Changed() {
			l.ChatWidth = total - 1 - editor
		}
	})
	if w := frame.Bounds().W; w != q.chat.width {
		q.chat.width = w
		c.Invalidate()
	}
}

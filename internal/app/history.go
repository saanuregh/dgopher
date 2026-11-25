package app

import (
	"cmp"
	"strings"

	"dgopher/internal/project"
	"dgopher/internal/store"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The tabs of the history dialog.
const (
	historyQueries = iota // what was typed and ran, to run again
	historyAudit          // the audit log, every action
)

// historyState is the dialog of a project's past queries and audit log.
type historyState struct {
	open             bool
	project          *project.Project
	tab              int
	entries          []store.HistoryEntry
	filter           string
	list             ui.ListState
	row              int
	err              string
	queriesRequested bool
	// audit is the audit log's tab, read when it first shows.
	audit *auditState
}

// openHistory opens the query history of the current project.
func (a *App) openHistory() {
	p := a.currentProject()
	if p == nil || p.Err != "" {
		a.ShowError("Which project?", "Every project keeps its own query history: choose one in the sidebar first.")
		return
	}
	a.openHistoryOf(p)
}

// openAudit opens the audit log of the current project.
func (a *App) openAudit() {
	p := a.currentProject()
	if p == nil {
		a.ShowError("Which project?", "Every project keeps its own audit log: choose one in the sidebar first.")
		return
	}
	a.openAuditOf(p)
}

func (a *App) openHistoryOf(p *project.Project) { a.openHistoryTab(p, historyQueries) }

func (a *App) openAuditOf(p *project.Project) { a.openHistoryTab(p, historyAudit) }

func (a *App) openHistoryTab(p *project.Project, tab int) {
	h := &historyState{open: true, project: p, row: -1}
	h.list.Selected = &h.row
	a.history = h
	if tab == historyAudit {
		h.showAudit(a)
	} else {
		h.showQueries(a)
	}
}

// showQueries turns to the queries' tab, reading them the first time.
func (h *historyState) showQueries(a *App) {
	h.tab = historyQueries
	if h.queriesRequested {
		return
	}
	h.queriesRequested = true
	if h.project.Err != "" {
		h.err = "The project could not be read: " + h.project.Err
		return
	}
	st := h.project.Local
	a.Background(func() func() {
		entries, err := st.History(2000)
		return func() {
			h.entries = entries
			if err != nil {
				h.err = err.Error()
			}
		}
	})
}

// showAudit turns to the audit log's tab, reading the log the first time.
func (h *historyState) showAudit(a *App) {
	h.tab = historyAudit
	if h.audit == nil {
		h.audit = a.readAudit(h.project)
	}
}

func (a *App) historyView(c *ui.Context) {
	h := a.history
	t := c.Theme()
	widgets.Dialog(c, &h.open, widgets.DialogWide, func(panel ui.Element) {
		_, wh := c.Size()
		panel.Height(min(760, wh-50)).Label("History")
		widgets.DialogHeader(c, func() {
			ui.Icon(c, widgets.IconHistory).FontSize(widgets.IconLarge).TextColor(t.Accent)
			widgets.DialogTitle(c, "History · "+h.project.Name).SingleLine().Shrink(1)
			ui.Spacer(c)
			if ui.Segmented(c, &h.tab, "Queries", "Audit Log").Label("Show").Changed() {
				if h.tab == historyAudit {
					h.showAudit(a)
				} else {
					h.showQueries(a)
				}
			}
		})
		if h.tab == historyAudit {
			a.auditTab(c, h)
		} else {
			a.queriesTab(c, h)
		}
	})
	if !h.open {
		a.history = nil
	}
}

func (a *App) queriesTab(c *ui.Context, h *historyState) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	var shown []store.HistoryEntry
	f := strings.ToLower(h.filter)
	for _, e := range h.entries {
		if f == "" || strings.Contains(strings.ToLower(e.SQL), f) || strings.Contains(strings.ToLower(e.Connection), f) {
			shown = append(shown, e)
		}
	}
	ui.Row(c).Padding(8, widgets.DialogPad).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		widgets.SearchBox(c, &h.filter, "Filter by SQL or connection", 0).AutoFocus()
	})
	if h.err != "" {
		ui.Text(c, h.err).TextColor(t.Danger).Padding(12)
	}
	var list ui.Element
	widgets.ChosenRows(c, func() {
		list = ui.List(c, &h.list, len(shown), func(i int) {
			e := shown[i]
			ui.Column(c).Padding(8, widgets.DialogPad).Gap(3).Children(func() {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, e.Time.Local().Format("Jan 2 15:04:05")).FontSize(widgets.TextMeta).TextColor(pal.Muted)
					ui.Text(c, e.Connection).FontSize(widgets.TextMeta).Bold()
					ui.Text(c, widgets.FormatDuration(e.Duration)).FontSize(widgets.TextMeta).TextColor(pal.Muted)
					if e.Error != "" {
						ui.Text(c, "failed").FontSize(widgets.TextMeta).TextColor(t.Danger).Tooltip(e.Error)
					}
				})
				ui.Text(c, widgets.OneLine(e.SQL, 220)).Font(widgets.MonoFont).FontSize(widgets.TextSmall).SingleLine()
			})
		}).Grow(1).Label("History").Dividers(1, t.Border)
	})
	if list.Submitted() && h.row >= 0 && h.row < len(shown) {
		e := shown[h.row]
		a.openStatement(h, e.ConnectionID, e.Database, e.SQL)
	}
	widgets.DialogActions(c, func() {
		ui.Text(c, "Enter opens the query in a new editor of its connection. Clearing the history leaves the audit log as it is.").FontSize(widgets.TextSmall).TextColor(pal.Muted).Grow(1).Shrink(1)
		var seq int64
		if h.row >= 0 && h.row < len(shown) {
			seq = shown[h.row].AuditSeq
		}
		show := ui.Button(c, "Show in Audit Log").Tooltip("The audit log's entry of this run: who ran it, where, and what was sent")
		if widgets.Clicked(show, cmp.Or(widgets.When(h.project.Audit == nil, "The project keeps no audit log"), widgets.When(seq == 0, "The audit log has no entry of this run"))) {
			h.showAudit(a)
			h.audit.choose(seq)
		}
		if widgets.Clicked(ui.Button(c, "Clear History…"), widgets.When(h.project.Local == nil, "The project keeps no history")) {
			a.closing = &closeRequest{open: true, title: "Clear the query history?", reason: "Every saved query is forgotten. The audit log keeps them.", action: "Clear", onClose: func() {
				h.project.Local.ClearHistory()
				h.entries = nil
			}}
		}
		if ui.Button(c, "Close").Clicked() {
			h.open = false
		}
	})
}

// openStatement opens a statement in a new editor of its connection, or
// copies it when the connection is gone or runs no SQL.
func (a *App) openStatement(h *historyState, connectionID, database, text string) {
	cn := a.connByID(connectionID)
	if cn == nil || !cn.Config.Engine.IsSQL() {
		a.WriteClipboard(text)
		a.Toast("Copied the statement", "", nil)
		return
	}
	h.open = false
	a.NewQueryTab(cn, database, text+";\n")
}

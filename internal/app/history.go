package app

import (
	"strings"

	"dgopher/internal/project"
	"dgopher/internal/store"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// historyState is the dialog of past queries.
type historyState struct {
	open    bool
	project *project.Project
	entries []store.HistoryEntry
	filter  string
	list    ui.ListState
	row     int
	err     string
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

func (a *App) openHistoryOf(p *project.Project) {
	h := &historyState{open: true, project: p, row: -1}
	h.list.Selected = &h.row
	a.history = h
	st := p.Local
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

func (a *App) historyView(c *ui.Context) {
	h := a.history
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	var shown []store.HistoryEntry
	f := strings.ToLower(h.filter)
	for _, e := range h.entries {
		if f == "" || strings.Contains(strings.ToLower(e.SQL), f) || strings.Contains(strings.ToLower(e.Connection), f) {
			shown = append(shown, e)
		}
	}
	ui.DialogBase(c, &h.open, func(backdrop, panel ui.Element) {
		backdrop.Background(widgets.Backdrop)
		panel.Width(820).Height(560).Radius(12).Background(t.Background).Border(1, t.Border).Clip().Label("Query history")
		ui.Row(c).Padding(12, 16).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "Query History · "+h.project.Name).FontSize(15).Bold().SingleLine().Shrink(1)
			widgets.SearchBox(c, &h.filter, "Filter by SQL or connection", 0).AutoFocus()
		})
		if h.err != "" {
			ui.Text(c, h.err).TextColor(t.Danger).Padding(12)
		}
		open := func(e store.HistoryEntry) {
			if cn := a.connByID(e.ConnectionID); cn != nil {
				h.open = false
				if cn.Config.Engine.IsSQL() {
					a.NewQueryTab(cn, e.Database, e.SQL+";\n")
				} else {
					a.WriteClipboard(e.SQL)
				}
			} else {
				a.WriteClipboard(e.SQL)
			}
		}
		list := ui.List(c, &h.list, len(shown), func(i int) {
			e := shown[i]
			ui.Column(c).Padding(8, 16).Gap(3).Children(func() {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, e.Time.Format("Jan 2 15:04:05")).FontSize(11).TextColor(pal.Muted)
					ui.Text(c, e.Connection).FontSize(11).Bold()
					ui.Text(c, widgets.FormatDuration(e.Duration)).FontSize(11).TextColor(pal.Muted)
					if e.Error != "" {
						ui.Text(c, "failed").FontSize(11).TextColor(t.Danger).Tooltip(e.Error)
					}
				})
				ui.Text(c, widgets.OneLine(e.SQL, 220)).Font(widgets.MonoFont).FontSize(12).SingleLine()
			})
		}).Grow(1).Label("History").Dividers(1, t.Border)
		if list.Submitted() && h.row >= 0 && h.row < len(shown) {
			open(shown[h.row])
		}
		ui.Row(c).Padding(10, 16).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "Enter opens the query in a new editor of its connection.").FontSize(12).TextColor(pal.Muted).Grow(1)
			if ui.Button(c, "Clear History…").Clicked() {
				a.closing = &closeRequest{open: true, title: "Clear the query history?", reason: "Every saved query is forgotten.", onClose: func() {
					h.project.Local.ClearHistory()
					h.entries = nil
				}}
			}
			if ui.Button(c, "Close").Clicked() {
				h.open = false
			}
		})
	})
	if !h.open {
		a.history = nil
	}
}

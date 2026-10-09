package dataview

import (
	"dgopher/internal/keymap"
	"dgopher/internal/ui/widgets"
)

// Commands are what the palette offers on the rows.
func (v *Viewer) Commands() []widgets.Command {
	chart, panel := "Show Chart", "Show Value Panel"
	if v.showChart {
		chart = "Hide Chart"
	}
	if v.grid.ShowValue {
		panel = "Hide Value Panel"
	}
	cmds := []widgets.Command{
		{Title: "Refresh Rows", Key: keymap.Refresh, Icon: widgets.IconRefresh, Run: v.RequestReload},
		{Title: "Export Rows…", Icon: widgets.IconDownload, Run: func() { OpenExport(v.a, v.exportSource()) }},
		{Title: chart, Key: keymap.ToggleChart, Icon: widgets.IconLayers, Run: func() { v.showChart = !v.showChart }},
		{Title: panel, Key: keymap.ValuePanel, Icon: widgets.IconView, Run: func() { v.grid.ShowValue = !v.grid.ShowValue }},
	}
	if !v.done && !v.loading {
		cmds = append(cmds, widgets.Command{Title: "Fetch All Rows", Key: keymap.FetchAll, Icon: widgets.IconDownload, Run: v.FetchAll})
	}
	if v.grid.edits.count() > 0 && !v.applying {
		cmds = append(cmds,
			widgets.Command{Title: "Review Changes…", Detail: "and apply them", Key: keymap.Apply, Icon: widgets.IconCheck, Run: func() { v.Review(nil) }},
			widgets.Command{Title: "Discard Changes", Key: keymap.Discard, Icon: widgets.IconUndo, Run: func() {
				if !v.applying {
					v.discard()
				}
			}})
	}
	return cmds
}

// Commands are what the palette offers on the tab: the rows', on the Data
// page.
func (t *TableTab) Commands() []widgets.Command {
	if t.Page != PageData {
		return nil
	}
	return t.view.Commands()
}

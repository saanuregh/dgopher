package query

import (
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/ui/widgets"
)

// Commands are what the palette offers on the editor: running and
// explaining, formatting, the open transaction, and the result shown.
func (q *Tab) Commands() []widgets.Command {
	cmds := []widgets.Command{
		{Title: "Run Statement", Key: keymap.Run, Icon: widgets.IconPlay, Run: func() { q.Run(RunStatement) }},
		{Title: "Run in New Tab", Key: keymap.RunInNewTab, Icon: widgets.IconPlay, Run: func() { q.Run(RunNewTab) }},
		{Title: "Run Script", Key: keymap.RunScript, Icon: widgets.IconPlay, Run: func() { q.Run(RunScript) }},
		{Title: "Explain Plan", Key: keymap.Explain, Icon: widgets.IconLayers, Run: func() { q.Run(RunExplain) }},
		{Title: "Explain Analyze", Detail: "runs the statement", Key: keymap.ExplainAnalyze, Icon: widgets.IconLayers, Run: func() { q.Run(RunExplainAnalyze) }},
		{Title: "Format SQL", Key: keymap.Format, Icon: widgets.IconWand, Run: q.format},
		{Title: "Go to Statement…", Key: keymap.GoToStatement, Icon: widgets.IconSearch, Run: q.openOutline},
		{Title: "Export from Query…", Icon: widgets.IconDownload, Run: q.exportFromQuery},
		{Title: "Save As…", Key: keymap.SaveAs, Icon: widgets.IconSave, Run: func() { q.a.SaveSQLFile(q, true) }},
	}
	if q.Tx != db.TxNone && !q.Busy() {
		if q.Tx == db.TxOpen {
			cmds = append(cmds, widgets.Command{Title: "Commit", Detail: "the open transaction", Key: keymap.Commit, Icon: widgets.IconCheck, Run: func() { q.endOpenTx(true) }})
		}
		cmds = append(cmds, widgets.Command{Title: "Roll Back", Detail: "the open transaction", Key: keymap.Rollback, Icon: widgets.IconUndo, Run: func() { q.endOpenTx(false) }})
	}
	if r := q.current(); r != nil && r.view != nil {
		cmds = append(cmds, r.view.Commands()...)
	}
	return cmds
}

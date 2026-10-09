package query

import (
	"cmp"
	"slices"

	"dgopher/internal/connection"
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
	cmds = append(cmds, q.switchCommands()...)
	cmds = append(cmds, q.resultCommands()...)
	if r := q.current(); r != nil && r.view != nil {
		cmds = append(cmds, r.view.Commands()...)
	}
	return cmds
}

// switchCommands move the editor to another database or schema, as its
// switcher's menus do.
func (q *Tab) switchCommands() []widgets.Command {
	cn := q.Conn
	if cn.Status != connection.StatusConnected || cn.DB == nil {
		return nil
	}
	var cmds []widgets.Command
	if cn.Config.Engine == db.Postgres && len(cn.Databases) > 1 {
		current := cmp.Or(q.Database, cn.Config.Database)
		for _, name := range cn.Databases {
			if name != current {
				cmds = append(cmds, widgets.Command{Title: "Use Database " + name, Detail: "in this editor", Icon: widgets.IconDatabase, Run: func() { q.switchDatabase(name) }})
			}
		}
	}
	if schemaStatement(cn.Config.Engine, cn.DB.Dialect, "") != "" {
		current := q.currentSchema()
		for _, name := range cn.Schemas[q.Database] {
			if name != current {
				cmds = append(cmds, widgets.Command{Title: "Use Schema " + name, Detail: "in this editor", Icon: widgets.IconSchema, Run: func() { q.switchSchema(name) }})
			}
		}
	}
	return cmds
}

// resultCommands move between the result tabs, and pin or close the one
// shown, as their pills do.
func (q *Tab) resultCommands() []widgets.Command {
	var shown []int // the results with a pill
	for i, r := range q.results {
		if r.rowsMode || r.err != "" {
			shown = append(shown, i)
		}
	}
	var cmds []widgets.Command
	if len(shown) > 1 {
		step := func(by int) func() {
			return func() {
				n := len(shown)
				switch at := slices.Index(shown, q.resultIdx); {
				case at >= 0:
					q.resultIdx = shown[(at+by+n)%n]
				case by > 0: // from the messages
					q.resultIdx = shown[0]
				default:
					q.resultIdx = shown[n-1]
				}
			}
		}
		cmds = append(cmds,
			widgets.Command{Title: "Next Result", Icon: widgets.IconNext, Run: step(1)},
			widgets.Command{Title: "Previous Result", Icon: widgets.IconPrev, Run: step(-1)})
	}
	r := q.current()
	if r == nil {
		return cmds
	}
	pin := "Pin Result"
	if r.pinned {
		pin = "Unpin Result"
	}
	cmds = append(cmds, widgets.Command{Title: pin, Icon: widgets.IconLock, Run: func() { r.pinned = !r.pinned }})
	if !q.Running && (r.view == nil || !r.view.Applying() && !r.view.Counting()) {
		cmds = append(cmds, widgets.Command{Title: "Close Result", Icon: widgets.IconX, Run: func() {
			q.a.Post(func() { q.settlePending([]*result{r}, func() { q.closeResult(r) }) })
		}})
	}
	return cmds
}

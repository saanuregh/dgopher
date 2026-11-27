package mongo

import (
	"dgopher/internal/keymap"
	"dgopher/internal/ui/widgets"
)

// Commands are what the palette offers on a collection: finding, the
// pending changes, and reading more.
func (t *CollectionTab) Commands() []widgets.Command {
	cmds := []widgets.Command{
		{Title: "Find Again", Detail: t.Title(), Key: keymap.Refresh, Icon: widgets.IconRefresh, Run: t.Find},
	}
	if t.readOnly() == "" {
		cmds = append(cmds, widgets.Command{Title: "Insert Document…", Detail: t.Title(), Key: keymap.AddRow, Icon: widgets.IconPlus, Run: func() { t.openInsert(nil) }})
	}
	if t.edits.count() > 0 {
		cmds = append(cmds,
			widgets.Command{Title: "Save Changes…", Detail: t.edits.summary(), Key: keymap.Apply, Icon: widgets.IconSave, Run: t.Save},
			widgets.Command{Title: "Discard Changes", Detail: t.edits.summary(), Key: keymap.Discard, Icon: widgets.IconUndo, Run: t.Discard})
	}
	if !t.done && t.cursor != nil {
		cmds = append(cmds,
			widgets.Command{Title: "Load More Documents", Key: keymap.FetchNext, Icon: widgets.IconDownload, Run: t.LoadMore},
			widgets.Command{Title: "Fetch All Documents", Key: keymap.FetchAll, Icon: widgets.IconDownload, Run: t.FetchAll})
	}
	return cmds
}

// Commands are what the palette offers on the console.
func (t *ConsoleTab) Commands() []widgets.Command {
	cmds := []widgets.Command{
		{Title: "Run Statement", Key: keymap.Run, Icon: widgets.IconPlay, Run: t.Run},
		{Title: "Run Script", Key: keymap.RunScript, Icon: widgets.IconPlay, Run: t.RunScript},
	}
	if t.running {
		cmds = append(cmds, widgets.Command{Title: "Stop", Detail: "the running statement", Icon: widgets.IconStop, Run: t.Stop})
	}
	return cmds
}

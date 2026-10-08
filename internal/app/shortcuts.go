package app

import (
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// shortcutGroups are the keys of the app, by where they work.
var shortcutGroups = []struct {
	title string
	keys  [][2]string
}{
	{"Anywhere", [][2]string{
		{"⌘K", "Command palette: every command, connection, table and snippet"},
		{"⌘P", "Open a table by name"},
		{"⌘T", "New SQL editor on the current connection"},
		{"⌘N", "New connection"},
		{"⌘1 … ⌘9", "Go to tab 1 … 9 (⌘9: the last)"},
		{"Ctrl+Tab", "Next tab (Ctrl+Shift+Tab: previous)"},
		{"⌘W", "Close tab"},
		{"⌘0", "Focus the navigator"},
		{"⌘L", "Focus the filter of the current view"},
		{"⌘B", "Show or hide the sidebar"},
		{"⌘Y", "Query history"},
		{"⌘⇧A", "Audit log"},
		{"⌘⇧O", "Add an existing folder as a project"},
		{"⌘/", "This list"},
	}},
	{"Navigator", [][2]string{
		{"↑ ↓", "Move; type a name's first letters to jump to it"},
		{"→ ←", "Open or close"},
		{"↵", "Connect, or open a table's data"},
		{"⌘↵", "New editor with SELECT * of the table"},
	}},
	{"SQL editor", [][2]string{
		{"⌘↵  or  F5", "Run the statement at the caret (blank lines and ; end it), or the selection"},
		{"⌘\\", "Run it into a new result tab, keeping the results shown"},
		{"⌘⇧↵  or  Alt+X", "Run the whole script"},
		{"⌘E", "Explain the statement"},
		{"⌘⇧F", "Format the SQL"},
		{"Ctrl+Space", "Complete: tables, columns, keywords"},
		{"⌘F", "Find in the editor (↵ next, ⇧↵ previous)"},
		{"⌘⌥F", "Replace in the editor (↵ in its field replaces the match)"},
		{"⌘J", "Between the editor and its results"},
		{"⌘S", "Save the file now (it also saves as you type)"},
		{"Esc", "Cancel the running statement"},
	}},
	{"Results and table data", [][2]string{
		{"↑ ↓ ← →", "Move between cells"},
		{"↵  or  F2", "Edit the cell"},
		{"Delete", "Mark the rows for deletion"},
		{"⌘C", "Copy the cell, or the rows chosen"},
		{"⌘⇧C", "Advanced Copy: delimiter, column names, quoting, row numbers"},
		{"⌘V", "Paste cells over the chosen one, as pending changes"},
		{"⌘⇧V", "Advanced Paste: delimiter, header row, as new rows"},
		{"⇧↵", "Open the value editor"},
		{"Esc", "Revert the chosen cell's change"},
		{"⌘Z  /  ⌘⇧Z", "Undo or redo a pending change"},
		{"Alt+Insert", "Add a row (⌘⌥Insert: duplicate the chosen one)"},
		{"Alt+Delete", "Mark the chosen rows for deletion"},
		{"⌘D", "Copy the value of the row above (⌘⌥D: below)"},
		{"⌘G  /  ⌘⇧G", "Go to a row by number, or a column by name"},
		{"⌘↑  /  ⌘↓", "First or last row"},
		{"⌘2", "Order by the chosen column: ascending, descending, none"},
		{"Tab  /  ⌘`", "Record view of the chosen row / next presentation"},
		{"Alt+Space", "Follow the cell's foreign key"},
		{"⌘⌥N  /  ⌘⇧=", "Fetch the next page / every row"},
		{"⌘S", "Review and apply the pending changes; in an editor, then save the file"},
		{"⌘R", "Discard the pending changes; without any, read the rows again"},
		{"F5", "Read a table's rows again (in an editor, F5 runs the statement)"},
	}},
}

func (a *App) shortcutsView(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	open := a.shortcutsOpen
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		_, h := c.Size()
		panel.Width(760).Height(min(680, h-60)).Radius(12).Background(th.Background).Border(1, th.Border).Clip().Label("Keyboard shortcuts")
		ui.Row(c).Padding(14, 20).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, "Keyboard Shortcuts").FontSize(16).Bold().Grow(1)
			ui.Text(c, widgets.KeyLabel("⌘/ or Esc to close")).FontSize(12).TextColor(pal.Muted)
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Grid(c).Columns(2).GapX(28).GapY(18).Padding(16, 20).Children(func() {
				for _, g := range shortcutGroups {
					ui.Column(c).Gap(6).Children(func() {
						ui.Text(c, g.title).Bold().TextColor(pal.Muted).FontSize(12)
						for _, k := range g.keys {
							ui.Row(c).Gap(10).AlignItems(ui.Start).Children(func() {
								ui.Text(c, widgets.KeyLabel(k[0])).Font(widgets.MonoFont).FontSize(11.5).Padding(2, 6).Radius(4).Background(pal.Hover).MinWidth(64)
								ui.Text(c, widgets.KeyLabel(k[1])).FontSize(12.5).Grow(1).Shrink(1)
							})
						}
					})
				}
			})
		})
	})
	a.shortcutsOpen = open
}

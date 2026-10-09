package keymap

// The commands with keys, by their IDs, which the settings keep.
const (
	Palette        = "palette"
	OpenTable      = "openTable"
	NewEditor      = "newEditor"
	NewConnection  = "newConnection"
	OpenScript     = "openScript"
	AddFolder      = "addFolder"
	CloseTab       = "closeTab"
	History        = "history"
	AuditLog       = "auditLog"
	Settings       = "settings"
	ToggleSidebar  = "toggleSidebar"
	FocusNavigator = "focusNavigator"
	FocusFilter    = "focusFilter"
	ShortcutsList  = "shortcuts"
	NextTab        = "nextTab"
	PreviousTab    = "previousTab"

	NavigatorSelect  = "navigator.select"
	NavigatorRefresh = "navigator.refresh"

	Run            = "editor.run"
	RunInNewTab    = "editor.runInNewTab"
	RunScript      = "editor.runScript"
	Explain        = "editor.explain"
	ExplainAnalyze = "editor.explainAnalyze"
	Format         = "editor.format"
	Complete       = "editor.complete"
	Find           = "editor.find"
	Replace        = "editor.replace"
	GoToStatement  = "editor.goToStatement"
	GoToDefinition = "editor.goToDefinition"
	FindUsages     = "editor.findUsages"
	QuickFix       = "editor.quickFix"
	Rename         = "editor.rename"
	CursorAbove    = "editor.cursorAbove"
	CursorBelow    = "editor.cursorBelow"
	SelectNext     = "editor.selectNext"
	SelectAll      = "editor.selectAll"
	ToggleResults  = "editor.toggleResults"
	Save           = "editor.save"
	SaveAs         = "editor.saveAs"

	Copy             = "grid.copy"
	AdvancedCopy     = "grid.advancedCopy"
	Paste            = "grid.paste"
	AdvancedPaste    = "grid.advancedPaste"
	EditValue        = "grid.editValue"
	DeleteRows       = "grid.deleteRows"
	AddRow           = "grid.addRow"
	DuplicateRow     = "grid.duplicateRow"
	CopyAbove        = "grid.copyAbove"
	CopyBelow        = "grid.copyBelow"
	Undo             = "grid.undo"
	Redo             = "grid.redo"
	GoToRow          = "grid.goToRow"
	GoToColumn       = "grid.goToColumn"
	FirstRow         = "grid.firstRow"
	LastRow          = "grid.lastRow"
	SortColumn       = "grid.sortColumn"
	DistinctValues   = "grid.distinctValues"
	NextPresentation = "grid.nextPresentation"
	FollowKey        = "grid.followKey"
	PickReference    = "grid.pickReference"
	ValuePanel       = "grid.valuePanel"
	FetchNext        = "grid.fetchNext"
	FetchAll         = "grid.fetchAll"
	Apply            = "grid.apply"
	Discard          = "grid.discard"
	Refresh          = "grid.refresh"
)

// command makes a command of its keys as the settings write them, which
// it keeps as they are written: on Linux, Ctrl and Cmd are one key.
func command(id string, scope Scope, title string, keys ...string) Command {
	cmd := Command{ID: id, Scope: scope, Title: title, written: keys}
	for _, k := range keys {
		ch, err := ParseChord(k)
		if err != nil {
			panic(id + ": " + err.Error())
		}
		cmd.Default = append(cmd.Default, ch)
	}
	return cmd
}

// Commands are the commands with keys, in the order lists show them.
var Commands = []Command{
	command(Palette, Global, "Command palette: every command, connection, table and snippet", "Cmd+K"),
	command(OpenTable, Global, "Open a table by name", "Cmd+P"),
	command(NewEditor, Global, "New SQL editor on the current connection", "Cmd+T"),
	command(NewConnection, Global, "New connection", "Cmd+N"),
	command(OpenScript, Global, "Open a SQL script", "Cmd+O"),
	command(AddFolder, Global, "Add an existing folder as a project"),
	command(CloseTab, Global, "Close the tab", "Cmd+W"),
	command(History, Global, "Query history", "Cmd+Y"),
	command(AuditLog, Global, "Audit log", "Cmd+Shift+A"),
	command(Settings, Global, "Settings", "Cmd+,"),
	command(ToggleSidebar, Global, "Show or hide the sidebar", "Cmd+B"),
	command(FocusNavigator, Global, "Focus the navigator", "Cmd+0"),
	command(FocusFilter, Global, "Focus the filter of the current view", "Cmd+L"),
	command(ShortcutsList, Global, "The list of keys", "Cmd+/"),
	command(NextTab, Global, "Next tab", "Ctrl+Tab"),
	command(PreviousTab, Global, "Previous tab", "Ctrl+Shift+Tab"),

	command(NavigatorSelect, Navigator, "New editor with SELECT * of the table", "Cmd+Enter"),
	command(NavigatorRefresh, Navigator, "Read the schema again", "Cmd+R"),

	command(Run, Editor, "Run the statement at the caret (blank lines and ; end it), or the selection", "Cmd+Enter", "F5"),
	command(RunInNewTab, Editor, "Run it into a new result tab, keeping the results shown", "Cmd+\\"),
	command(RunScript, Editor, "Run the whole script", "Cmd+Shift+Enter", "Alt+X"),
	command(Explain, Editor, "Explain the statement", "Cmd+E"),
	command(ExplainAnalyze, Editor, "Explain and analyze the statement, running it", "Cmd+Shift+E"),
	command(Format, Editor, "Format the SQL", "Cmd+Shift+F"),
	command(Complete, Editor, "Complete: tables, columns, keywords", "Ctrl+Space"),
	command(Find, Editor, "Find in the editor (↵ next, ⇧↵ previous)", "Cmd+F"),
	command(Replace, Editor, "Replace in the editor", "Cmd+Alt+F"),
	command(GoToStatement, Editor, "Go to a statement of the script", "Cmd+Shift+O"),
	command(GoToDefinition, Editor, "Go to the definition of the name at the caret", "F12"),
	command(FindUsages, Editor, "Find the usages of the name at the caret", "Shift+F12"),
	command(QuickFix, Editor, "Fix the problem at the caret", "Alt+Enter"),
	command(Rename, Editor, "Rename the name at the caret, where it is used", "F2"),
	command(CursorAbove, Editor, "Add a caret on the line above", "Cmd+Alt+Up"),
	command(CursorBelow, Editor, "Add a caret on the line below", "Cmd+Alt+Down"),
	command(SelectNext, Editor, "Select the word, then the next time it occurs, with a caret at each", "Cmd+D"),
	command(SelectAll, Editor, "Select every time the word occurs, with a caret at each", "Cmd+Shift+L"),
	command(ToggleResults, Editor, "Between the editor and its results", "Cmd+J"),
	command(Save, Editor, "Save the file now (it also saves as you type)", "Cmd+S"),
	command(SaveAs, Editor, "Save the editor as a file", "Cmd+Shift+S"),

	command(Copy, Grid, "Copy the cell, or the rows chosen", "Cmd+C"),
	command(AdvancedCopy, Grid, "Advanced Copy: delimiter, column names, quoting, row numbers", "Cmd+Shift+C"),
	command(Paste, Grid, "Paste cells over the chosen one, as pending changes", "Cmd+V"),
	command(AdvancedPaste, Grid, "Advanced Paste: delimiter, header row, as new rows", "Cmd+Shift+V"),
	command(EditValue, Grid, "Open the value editor", "Shift+Enter"),
	command(DeleteRows, Grid, "Mark the chosen rows for deletion", "Delete", "Cmd+Backspace", "Alt+Delete"),
	command(AddRow, Grid, "Add a row", "Alt+Insert"),
	command(DuplicateRow, Grid, "Duplicate the chosen row", "Cmd+Alt+Insert"),
	command(CopyAbove, Grid, "Copy the value of the row above", "Cmd+D"),
	command(CopyBelow, Grid, "Copy the value of the row below", "Cmd+Alt+D"),
	command(Undo, Grid, "Undo a pending change", "Cmd+Z"),
	command(Redo, Grid, "Redo a pending change", "Cmd+Shift+Z"),
	command(GoToRow, Grid, "Go to a row by number", "Cmd+G"),
	command(GoToColumn, Grid, "Go to a column by name", "Cmd+Shift+G"),
	command(FirstRow, Grid, "First row", "Cmd+Up"),
	command(LastRow, Grid, "Last row", "Cmd+Down"),
	command(SortColumn, Grid, "Order by the chosen column: ascending, descending, none", "Cmd+2"),
	command(DistinctValues, Grid, "The chosen column's distinct values, to filter by", "Cmd+F11"),
	command(NextPresentation, Grid, "Next presentation: grid, record, text, JSON, chart", "Cmd+`"),
	command(FollowKey, Grid, "Follow the cell's foreign key", "Alt+Space"),
	command(PickReference, Grid, "Choose the cell's value from the table its foreign key points at", "Alt+Down"),
	command(ValuePanel, Grid, "Show or hide the value panel", "F7"),
	command(FetchNext, Grid, "Fetch the next page of rows", "Cmd+Alt+N"),
	command(FetchAll, Grid, "Fetch every row", "Cmd+Shift+="),
	command(Apply, Grid, "Review and apply the pending changes", "Cmd+S"),
	command(Discard, Grid, "Discard the pending changes; without any, read the rows again", "Cmd+R"),
	command(Refresh, Grid, "Read a table's rows again", "F5"),
}

// Fixed are keys that are not commands, and cannot be changed: they move
// within a list or a form, as everywhere.
var Fixed = []struct {
	Scope      Scope
	Keys, Does string
}{
	{Global, "⌘1 … ⌘9", "Go to tab 1 … 9 (⌘9: the last)"},
	{Navigator, "↑ ↓", "Move; type a name's first letters to jump to it"},
	{Navigator, "→ ←", "Open or close"},
	{Navigator, "↵", "Connect, or open a table's data"},
	{Editor, "Esc", "Cancel the running statement"},
	{Grid, "↑ ↓ ← →", "Move between cells"},
	{Grid, "↵  or  F2", "Edit the cell"},
	{Grid, "Esc", "Revert the chosen cell's change"},
	{Grid, "Tab", "Record view of the chosen row"},
}

package keymap

import "fmt"

// The actions with keys, by the names keymap.json gives them.
const (
	Palette             = "command_palette::Toggle"
	ConfirmDialog       = "menu::Confirm"
	OpenTable           = "file_finder::Toggle"
	NewEditor           = "workspace::NewEditor"
	NewNotebook         = "workspace::NewNotebook"
	NewConnection       = "workspace::NewConnection"
	OpenScript          = "workspace::Open"
	AddFolder           = "workspace::AddFolderToProject"
	CloseTab            = "pane::CloseActiveItem"
	ReopenTab           = "pane::ReopenClosedItem"
	History             = "workspace::QueryHistory"
	AuditLog            = "workspace::AuditLog"
	ShowLatestError     = "workspace::ShowLatestError"
	ToggleNotifications = "workspace::ToggleNotifications"
	Settings            = "app::OpenSettings"
	OpenKeymap          = "app::OpenKeymap"
	ToggleSidebar       = "workspace::ToggleLeftDock"
	FullScreen          = "app::ToggleFullScreen"
	Quit                = "app::Quit"
	FocusNavigator      = "workspace::FocusNavigator"
	FocusFilter         = "workspace::FocusFilter"
	ShortcutsList       = "app::ShowKeyBindings"
	NextTab             = "pane::ActivateNextItem"
	PreviousTab         = "pane::ActivatePreviousItem"
	ActivateTab         = "pane::ActivateItem"
	LastTab             = "pane::ActivateLastItem"

	SplitRight     = "pane::SplitRight"
	SplitLeft      = "pane::SplitLeft"
	SplitDown      = "pane::SplitDown"
	SplitUp        = "pane::SplitUp"
	MoveTabRight   = "pane::MoveItemRight"
	MoveTabLeft    = "pane::MoveItemLeft"
	MoveTabDown    = "pane::MoveItemDown"
	MoveTabUp      = "pane::MoveItemUp"
	FocusPaneRight = "workspace::ActivatePaneRight"
	FocusPaneLeft  = "workspace::ActivatePaneLeft"
	FocusPaneDown  = "workspace::ActivatePaneDown"
	FocusPaneUp    = "workspace::ActivatePaneUp"
	ActivatePane   = "workspace::ActivatePane"
	NextPane       = "workspace::ActivateNextPane"
	ClosePane      = "pane::ClosePane"
	JoinPanes      = "workspace::JoinAllPanes"

	ToggleChat = "chat::Toggle"
	ChatStop   = "chat::Stop"

	NavigatorSelect    = "navigator::NewSelect"
	NavigatorRefresh   = "navigator::Refresh"
	NavigatorCopyName  = "navigator::CopyName"
	NavigatorRename    = "navigator::Rename"
	NavigatorStructure = "navigator::Structure"
	NavigatorDrop      = "navigator::Drop"

	Run             = "editor::Run"
	RunInNewTab     = "editor::RunInNewTab"
	RunScript       = "editor::RunScript"
	Explain         = "editor::Explain"
	ExplainAnalyze  = "editor::ExplainAnalyze"
	Format          = "editor::Format"
	Complete        = "editor::ShowCompletions"
	Find            = "editor::Find"
	Replace         = "editor::Replace"
	GoToStatement   = "editor::GoToStatement"
	GoToDefinition  = "editor::GoToDefinition"
	ShowHover       = "editor::Hover"
	FindUsages      = "editor::FindAllReferences"
	QuickFix        = "editor::QuickFix"
	Rename          = "editor::Rename"
	CursorAbove     = "editor::AddSelectionAbove"
	CursorBelow     = "editor::AddSelectionBelow"
	SelectNext      = "editor::SelectNext"
	SelectAll       = "editor::SelectAllMatches"
	Fold            = "editor::Fold"
	Unfold          = "editor::UnfoldLines"
	FoldAll         = "editor::FoldAll"
	UnfoldAll       = "editor::UnfoldAll"
	Comment         = "editor::ToggleComments"
	BlockComment    = "editor::ToggleBlockComment"
	ToggleResults   = "editor::ToggleResults"
	FocusResults    = "editor::FocusResults"
	MaximizeResults = "editor::MaximizeResults"
	Save            = "editor::Save"
	SaveAs          = "editor::SaveAs"
	Commit          = "editor::Commit"
	Rollback        = "editor::Rollback"
	ToggleNotebook  = "editor::ToggleNotebook"

	RunCell        = "notebook::RunCell"
	RunCellAdvance = "notebook::RunCellAndAdvance"
	RunAllCells    = "notebook::RunAll"
	RunCellsAbove  = "notebook::RunAbove"
	RunCellsBelow  = "notebook::RunBelow"
	AddCellAbove   = "notebook::AddCellAbove"
	AddCellBelow   = "notebook::AddCellBelow"
	DeleteCell     = "notebook::DeleteCell"
	MoveCellUp     = "notebook::MoveCellUp"
	MoveCellDown   = "notebook::MoveCellDown"
	CellToSQL      = "notebook::ChangeToSql"
	CellToMarkdown = "notebook::ChangeToMarkdown"
	ClearOutputs   = "notebook::ClearOutputs"
	PreviousCell   = "notebook::SelectPreviousCell"
	NextCell       = "notebook::SelectNextCell"

	Copy             = "grid::Copy"
	AdvancedCopy     = "grid::AdvancedCopy"
	Paste            = "grid::Paste"
	AdvancedPaste    = "grid::AdvancedPaste"
	EditValue        = "grid::EditValue"
	DeleteRows       = "grid::DeleteRows"
	AddRow           = "grid::AddRow"
	DuplicateRow     = "grid::DuplicateRow"
	CopyAbove        = "grid::CopyAbove"
	CopyBelow        = "grid::CopyBelow"
	Undo             = "grid::Undo"
	Redo             = "grid::Redo"
	GoToRow          = "grid::GoToRow"
	GoToColumn       = "grid::GoToColumn"
	FirstRow         = "grid::FirstRow"
	LastRow          = "grid::LastRow"
	ExtendToFirstRow = "grid::ExtendToFirstRow"
	ExtendToLastRow  = "grid::ExtendToLastRow"
	SelectRow        = "grid::SelectRow"
	SelectColumn     = "grid::SelectColumn"
	SelectMatching   = "grid::SelectMatching"
	SelectChanged    = "grid::SelectChanged"
	InvertSelection  = "grid::InvertSelection"
	SortColumn       = "grid::SortColumn"
	DistinctValues   = "grid::DistinctValues"
	NextPresentation = "grid::NextPresentation"
	FollowKey        = "grid::FollowKey"
	PickReference    = "grid::PickReference"
	ValuePanel       = "grid::ToggleValuePanel"
	FetchNext        = "grid::FetchNext"
	FetchAll         = "grid::FetchAll"
	Apply            = "grid::Apply"
	Discard          = "grid::Discard"
	Refresh          = "grid::Refresh"
	ToggleChart      = "grid::ToggleChart"
)

// command makes an action of its keys as keymap.json writes them.
func command(id string, scope Scope, title string, keys ...string) Command {
	cmd := Command{ID: id, Scope: scope, Title: title, written: keys}
	for _, k := range keys {
		cmd.Default = append(cmd.Default, Binding{Keys: mustKeys(id, k), Scope: scope})
	}
	return cmd
}

// numbered makes an action taking a number of its keys: the keys before
// the number, and the number, from 1 to n.
func numbered(id string, scope Scope, title, before string, n int) Command {
	cmd := Command{ID: id, Scope: scope, Title: title, TakesArg: true}
	for i := 1; i <= n; i++ {
		cmd.Default = append(cmd.Default, Binding{Keys: mustKeys(id, fmt.Sprint(before, i)), Scope: scope, Arg: i})
	}
	return cmd
}

func mustKeys(id, s string) Keystroke {
	k, err := ParseKeystroke(s)
	if err != nil {
		panic(id + ": " + err.Error())
	}
	return k
}

// Commands are the actions with keys, in the order lists show them.
var Commands = []Command{
	command(Palette, Global, "Command palette: every command, connection, table and snippet", "cmd-shift-p"),
	command(OpenTable, Global, "Open a table or a query file by name", "cmd-p"),
	command(NewEditor, Global, "New SQL editor on the current connection", "cmd-t"),
	command(NewNotebook, Global, "New SQL notebook on the current connection", "cmd-k cmd-t"),
	command(NewConnection, Global, "New connection", "cmd-n"),
	command(OpenScript, Global, "Open a SQL script", "cmd-o"),
	command(AddFolder, Global, "Add an existing folder as a project"),
	command(CloseTab, Global, "Close the tab", "cmd-w"),
	command(ReopenTab, Global, "Reopen the tab closed last", "cmd-shift-t"),
	command(History, Global, "Query history", "cmd-y"),
	command(AuditLog, Global, "Audit log", "cmd-shift-a"),
	command(ShowLatestError, Global, "Show the latest error", "cmd-k cmd-e"),
	command(ToggleNotifications, Global, "Notifications: every error and message of this session", "cmd-shift-n"),
	command(Settings, Global, "Settings", "cmd-,"),
	command(OpenKeymap, Global, "Open keymap.json, the keys you set", "cmd-k cmd-s"),
	command(ToggleSidebar, Global, "Show or hide the sidebar", "cmd-b"),
	command(FullScreen, Global, "Enter or leave full screen", "f11"),
	command(Quit, Global, "Quit DGopher, asking first about what it would lose", "cmd-q"),
	command(FocusNavigator, Global, "Focus the navigator", "cmd-0"),
	command(FocusFilter, Global, "Focus the filter of the current view", "cmd-l"),
	command(ShortcutsList, Global, "The list of keys", "f1"),
	command(NextTab, Global, "Next tab of the group", "ctrl-tab"),
	command(PreviousTab, Global, "Previous tab of the group", "ctrl-shift-tab"),
	numbered(ActivateTab, Global, "Go to tab n of the group", "cmd-", 8),
	command(LastTab, Global, "Go to the last tab of the group", "cmd-9"),
	command(ToggleChat, Global, "The editor's AI chat: show it, or hide it", "cmd-k cmd-i"),

	command(SplitRight, Global, "Split right: the tab in a new group beside", "cmd-k right", "cmd-\\"),
	command(SplitLeft, Global, "Split left: the tab in a new group beside", "cmd-k left"),
	command(SplitDown, Global, "Split down: the tab in a new group below", "cmd-k down"),
	command(SplitUp, Global, "Split up: the tab in a new group above", "cmd-k up"),
	command(MoveTabRight, Global, "Move the tab to the group to the right, made when there is none", "cmd-k shift-right"),
	command(MoveTabLeft, Global, "Move the tab to the group to the left, made when there is none", "cmd-k shift-left"),
	command(MoveTabDown, Global, "Move the tab to the group below, made when there is none", "cmd-k shift-down"),
	command(MoveTabUp, Global, "Move the tab to the group above, made when there is none", "cmd-k shift-up"),
	command(FocusPaneRight, Global, "Focus the group to the right", "cmd-k cmd-right"),
	command(FocusPaneLeft, Global, "Focus the group to the left", "cmd-k cmd-left"),
	command(FocusPaneDown, Global, "Focus the group below", "cmd-k cmd-down"),
	command(FocusPaneUp, Global, "Focus the group above", "cmd-k cmd-up"),
	numbered(ActivatePane, Global, "Focus group n", "cmd-k cmd-", 9),
	command(NextPane, Global, "Focus the next group"),
	command(ClosePane, Global, "Close the group: its tabs move to the one beside", "cmd-k cmd-w"),
	command(JoinPanes, Global, "Join every group into one"),

	command(ConfirmDialog, Dialog, "Do what a dialog is for: its main button, from any of its fields", "cmd-enter"),

	command(NavigatorSelect, Navigator, "New editor with SELECT * of the table", "cmd-enter"),
	command(NavigatorRefresh, Navigator, "Read the schema again", "cmd-r"),
	command(NavigatorCopyName, Navigator, "Copy the table's name, quoted", "cmd-c"),
	command(NavigatorRename, Navigator, "Rename the table", "f2"),
	command(NavigatorStructure, Navigator, "The table's structure", "f4"),
	command(NavigatorDrop, Navigator, "A DROP of the table, in a new editor to review and run", "delete"),

	command(Run, Editor, "Run the statement at the caret (blank lines and ; end it), or the selection", "cmd-enter", "f5"),
	command(RunInNewTab, Editor, "Run it into a new result tab, keeping the results shown", "cmd-alt-\\"),
	command(RunScript, Editor, "Run the whole script; in a notebook, the whole cell", "cmd-shift-enter", "alt-x"),
	command(Explain, Editor, "Explain the statement", "cmd-e"),
	command(ExplainAnalyze, Editor, "Explain and analyze the statement, running it", "cmd-shift-e"),
	command(Format, Editor, "Format the SQL", "cmd-shift-f"),
	command(Complete, Editor, "Complete: tables, columns, keywords", "ctrl-space"),
	command(Find, Editor, "Find in the editor (↵ next, ⇧↵ previous)", "cmd-f"),
	command(Replace, Editor, "Replace in the editor", "cmd-alt-f"),
	command(GoToStatement, Editor, "Go to a statement of the script", "cmd-shift-o"),
	command(GoToDefinition, Editor, "Go to the definition of the name at the caret", "f12"),
	command(ShowHover, Editor, "Show the card of the name at the caret", "cmd-i"),
	command(FindUsages, Editor, "Find the usages of the name at the caret", "shift-f12"),
	command(QuickFix, Editor, "Fix the problem at the caret", "alt-enter"),
	command(Rename, Editor, "Rename the name at the caret, where it is used", "f2"),
	command(CursorAbove, Editor, "Add a caret on the line above", "cmd-alt-up"),
	command(CursorBelow, Editor, "Add a caret on the line below", "cmd-alt-down"),
	command(SelectNext, Editor, "Select the word, then the next time it occurs, with a caret at each", "cmd-d"),
	command(SelectAll, Editor, "Select every time the word occurs, with a caret at each", "cmd-shift-l"),
	command(Fold, Editor, "Fold the block the caret is in: a statement, a group in parentheses, a comment", "cmd-alt-["),
	command(Unfold, Editor, "Open the fold the caret is on", "cmd-alt-]"),
	command(FoldAll, Editor, "Fold every block", "cmd-alt-shift-["),
	command(UnfoldAll, Editor, "Open every fold", "cmd-alt-shift-]"),
	command(Comment, Editor, "Comment or uncomment the lines", "cmd-/"),
	command(BlockComment, Editor, "Put the selection in a block comment, or take it out", "cmd-alt-/"),
	command(ToggleResults, Editor, "Show or hide the results", "cmd-j"),
	command(FocusResults, Editor, "Between the editor and its results", "cmd-shift-j"),
	command(MaximizeResults, Editor, "Maximize the results over the editor, or restore them"),
	command(Save, Editor, "Save the file now (it also saves as you type)", "cmd-s"),
	command(SaveAs, Editor, "Save the editor as a file", "cmd-shift-s"),
	command(Commit, Editor, "Commit the open transaction, an editor's or a table's", "cmd-alt-shift-enter"),
	// Rolling back loses the transaction's work: no key unless one is set.
	command(Rollback, Editor, "Roll back the open transaction, an editor's or a table's"),
	command(ToggleNotebook, Editor, "Show the editor as a notebook of cells, or as one script", "cmd-k cmd-n"),

	command(RunCellAdvance, Notebook, "Run the cell and go to the next, made when there is none", "shift-enter"),
	command(RunCell, Notebook, "Run the cell, staying in it", "cmd-alt-enter"),
	command(RunAllCells, Notebook, "Run every cell, from the first", "cmd-k cmd-enter"),
	command(RunCellsAbove, Notebook, "Run the cells above the one with the caret"),
	command(RunCellsBelow, Notebook, "Run the cell with the caret and those below"),
	command(AddCellAbove, Notebook, "Add a SQL cell above", "cmd-k cmd-a"),
	command(AddCellBelow, Notebook, "Add a SQL cell below", "cmd-k cmd-b"),
	command(DeleteCell, Notebook, "Delete the cell", "cmd-k cmd-d"),
	command(MoveCellUp, Notebook, "Move the cell up", "alt-shift-up"),
	command(MoveCellDown, Notebook, "Move the cell down", "alt-shift-down"),
	command(CellToSQL, Notebook, "Change the cell to SQL", "cmd-k cmd-y"),
	command(CellToMarkdown, Notebook, "Change the cell to Markdown", "cmd-k cmd-m"),
	command(ClearOutputs, Notebook, "Clear every cell's results"),
	command(PreviousCell, Notebook, "Go to the cell above", "alt-pageup"),
	command(NextCell, Notebook, "Go to the cell below", "alt-pagedown"),

	command(ChatStop, Chat, "Stop the reply", "cmd-."),

	command(Copy, Grid, "Copy the cell, or the rows chosen", "cmd-c"),
	command(AdvancedCopy, Grid, "Advanced Copy: delimiter, column names, quoting, row numbers", "cmd-shift-c"),
	command(Paste, Grid, "Paste cells over the chosen one, as pending changes", "cmd-v"),
	command(AdvancedPaste, Grid, "Advanced Paste: delimiter, header row, as new rows", "cmd-shift-v"),
	command(EditValue, Grid, "Open the value editor", "shift-enter"),
	command(DeleteRows, Grid, "Mark the chosen rows for deletion; in the Redis key tree, delete the key", "delete", "cmd-backspace", "alt-delete"),
	command(AddRow, Grid, "Add a row", "alt-insert"),
	command(DuplicateRow, Grid, "Duplicate the chosen row", "cmd-alt-insert"),
	command(CopyAbove, Grid, "Copy the value of the row above", "cmd-d"),
	command(CopyBelow, Grid, "Copy the value of the row below", "cmd-alt-d"),
	command(Undo, Grid, "Undo a pending change", "cmd-z"),
	command(Redo, Grid, "Redo a pending change", "cmd-shift-z"),
	command(GoToRow, Grid, "Go to a row by number", "cmd-g"),
	command(GoToColumn, Grid, "Go to a column by name", "cmd-shift-g"),
	command(FirstRow, Grid, "First row", "cmd-up"),
	command(LastRow, Grid, "Last row", "cmd-down"),
	command(ExtendToFirstRow, Grid, "Choose the rows up to the first", "cmd-shift-up"),
	command(ExtendToLastRow, Grid, "Choose the rows down to the last", "cmd-shift-down"),
	command(SelectRow, Grid, "Choose the whole rows of the chosen cells", "alt-shift-space"),
	command(SelectColumn, Grid, "Choose the whole columns of the chosen cells", "cmd-shift-space"),
	command(SelectMatching, Grid, "Choose the rows whose value in the column is the cell's"),
	command(SelectChanged, Grid, "Choose the rows with pending changes"),
	command(InvertSelection, Grid, "Choose the rows not chosen, and none of those that were"),
	command(SortColumn, Grid, "Order by the chosen column: ascending, descending, none", "cmd-alt-s"),
	command(DistinctValues, Grid, "The chosen column's distinct values, to filter by", "cmd-f11"),
	command(NextPresentation, Grid, "Next presentation: grid, record, text", "cmd-`"),
	command(FollowKey, Grid, "Follow the cell's foreign key", "alt-space"),
	command(PickReference, Grid, "Choose the cell's value from the table its foreign key points at", "alt-down"),
	command(ValuePanel, Grid, "Show or hide the value panel", "f7"),
	command(FetchNext, Grid, "Fetch the next page of rows", "cmd-alt-n"),
	command(FetchAll, Grid, "Fetch every row", "cmd-shift-="),
	command(Apply, Grid, "Review and apply the pending changes", "cmd-s"),
	command(Discard, Grid, "Discard the pending changes; without any, read the rows again", "cmd-r"),
	command(Refresh, Grid, "Read a table's rows again; scan the Redis keys, or run a dashboard's panels, again", "f5"),
	command(ToggleChart, Grid, "Show or hide the chart of the rows"),
}

// Fixed are keys that are not actions, and cannot be changed: they move
// within a list or a form, as everywhere.
var Fixed = []struct {
	Scope      Scope
	Keys, Does string
}{
	{Global, "⇧F10  or  Menu", "Open the menu of what has the focus"},
	{Global, "Esc", "Forget the first of two keys pressed"},
	{Dialog, "Esc", "Close the dialog"},
	{Navigator, "↑ ↓", "Move; type a name's first letters to jump to it"},
	{Navigator, "→ ←", "Open or close"},
	{Navigator, "↵", "Connect, or open a table's data"},
	{Editor, "Esc", "Cancel the running statement"},
	{Chat, "↵", "Send; ⇧↵ for a new line"},
	{Grid, "↑ ↓ ← →", "Move between cells"},
	{Grid, "↵  or  F2", "Edit the cell"},
	{Grid, "Esc", "Revert the chosen cell's change"},
	{Grid, "Tab", "Record view of the chosen row"},
}

// legacyIDs are the actions by the IDs the settings once kept their keys
// by.
var legacyIDs = map[string]string{
	"palette": Palette, "dialog.confirm": ConfirmDialog, "openTable": OpenTable, "newEditor": NewEditor,
	"newConnection": NewConnection, "openScript": OpenScript, "addFolder": AddFolder, "closeTab": CloseTab,
	"reopenTab": ReopenTab, "history": History, "auditLog": AuditLog, "settings": Settings,
	"toggleSidebar": ToggleSidebar, "fullScreen": FullScreen, "quit": Quit, "focusNavigator": FocusNavigator,
	"focusFilter": FocusFilter, "shortcuts": ShortcutsList, "nextTab": NextTab, "previousTab": PreviousTab,
	"navigator.select": NavigatorSelect, "navigator.refresh": NavigatorRefresh, "navigator.copyName": NavigatorCopyName,
	"navigator.rename": NavigatorRename, "navigator.structure": NavigatorStructure, "navigator.drop": NavigatorDrop,
	"editor.run": Run, "editor.runInNewTab": RunInNewTab, "editor.runScript": RunScript, "editor.explain": Explain,
	"editor.explainAnalyze": ExplainAnalyze, "editor.format": Format, "editor.complete": Complete, "editor.find": Find,
	"editor.replace": Replace, "editor.goToStatement": GoToStatement, "editor.goToDefinition": GoToDefinition,
	"editor.showHover": ShowHover, "editor.findUsages": FindUsages, "editor.quickFix": QuickFix, "editor.rename": Rename,
	"editor.cursorAbove": CursorAbove, "editor.cursorBelow": CursorBelow, "editor.selectNext": SelectNext,
	"editor.selectAll": SelectAll, "editor.fold": Fold, "editor.unfold": Unfold, "editor.foldAll": FoldAll,
	"editor.unfoldAll": UnfoldAll, "editor.comment": Comment, "editor.blockComment": BlockComment,
	"editor.toggleResults": ToggleResults, "editor.focusResults": FocusResults, "editor.maximizeResults": MaximizeResults,
	"editor.save": Save, "editor.saveAs": SaveAs, "editor.commit": Commit, "editor.rollback": Rollback,
	"grid.copy": Copy, "grid.advancedCopy": AdvancedCopy, "grid.paste": Paste, "grid.advancedPaste": AdvancedPaste,
	"grid.editValue": EditValue, "grid.deleteRows": DeleteRows, "grid.addRow": AddRow, "grid.duplicateRow": DuplicateRow,
	"grid.copyAbove": CopyAbove, "grid.copyBelow": CopyBelow, "grid.undo": Undo, "grid.redo": Redo,
	"grid.goToRow": GoToRow, "grid.goToColumn": GoToColumn, "grid.firstRow": FirstRow, "grid.lastRow": LastRow,
	"grid.extendToFirstRow": ExtendToFirstRow, "grid.extendToLastRow": ExtendToLastRow, "grid.selectRow": SelectRow,
	"grid.selectColumn": SelectColumn, "grid.selectMatching": SelectMatching, "grid.selectChanged": SelectChanged,
	"grid.invertSelection": InvertSelection, "grid.sortColumn": SortColumn, "grid.distinctValues": DistinctValues,
	"grid.nextPresentation": NextPresentation, "grid.followKey": FollowKey, "grid.pickReference": PickReference,
	"grid.valuePanel": ValuePanel, "grid.fetchNext": FetchNext, "grid.fetchAll": FetchAll, "grid.apply": Apply,
	"grid.discard": Discard, "grid.refresh": Refresh, "grid.toggleChart": ToggleChart,
}

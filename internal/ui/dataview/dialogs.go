package dataview

import (
	"github.com/egoist/mygo/ui"
)

// DialogsView draws the data view's open dialogs, but for the export
// dialog, which the app draws under its own.
func DialogsView(a Host, c *ui.Context) {
	d := a.Dialogs()
	if d.clip != nil {
		clipView(a, c)
	}
	if d.sqlShown != nil {
		sqlDialogView(a, c)
	}
	if d.valueEdit != nil {
		valueEditorView(a, c)
	}
	if d.goTo != nil {
		goToView(a, c)
	}
	if d.filterPrompt != nil {
		filterPromptView(a, c)
	}
	if d.distinct != nil {
		distinctView(a, c)
	}
	if d.keyForm != nil {
		keyFormView(a, c)
	}
	if d.refPicker != nil {
		refPickerView(a, c)
	}
	if d.builder != nil {
		queryBuilderView(a, c)
	}
}

// ExportOpen reports whether the export dialog is open.
func ExportOpen(a Host) bool { return a.Dialogs().export != nil }

// Dialogs are the data view's dialogs, one of each at a time.
type Dialogs struct {
	export       *exportState
	clip         *clipForm
	sqlShown     *sqlDialog
	valueEdit    *valueEditor
	goTo         *goToForm
	filterPrompt *filterPrompt
	distinct     *distinctForm
	keyForm      *keyForm
	refPicker    *refPicker
	builder      *queryBuilder
	// pinned are the rows kept to compare another result with.
	pinned *rowSnapshot
}

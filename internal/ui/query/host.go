package query

import (
	"dgopher/internal/project"
	"dgopher/internal/ui/dataview"
)

// Host is what the query tab needs of the app: the data view's
// host, for its results, and its own dialogs and files.
type Host interface {
	dataview.Host
	QueryDialogs() *Dialogs
	// AskSnippet asks a name for the editor's selection, kept as a
	// snippet of its project.
	AskSnippet(q *Tab)
	SaveSQLFile(q *Tab, saveAs bool)
	// ScanQueries lists the project's query files again: now, or when
	// they last changed a while ago.
	ScanQueries(p *project.Project, now bool)
}

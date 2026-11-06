package app

import (
	"log"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// MyGo's recent documents, which tests replace: they need a running app.
var (
	addRecentDocument    = mygo.App.AddRecentDocument
	recentDocuments      = mygo.App.RecentDocuments
	clearRecentDocuments = mygo.App.ClearRecentDocuments
)

// addRecent records a file the user opened with MyGo's recent documents.
func addRecent(path string) {
	if err := addRecentDocument(path); err != nil {
		log.Println("recent documents:", err)
	}
}

// recentMenu is the title bar's Open Recent: the system's menu bar has
// MyGo's own.
func (a *App) recentMenu(m *ui.Menu) {
	paths := recentDocuments()
	for _, p := range paths {
		if m.Item(filepath.Base(p)).Chosen() {
			a.openDropped([]string{p})
		}
	}
	if len(paths) > 0 {
		m.Separator()
	}
	if m.Item("Clear Recent").Disabled(len(paths) == 0).Chosen() {
		if err := clearRecentDocuments(); err != nil {
			log.Println("recent documents:", err)
		}
	}
}

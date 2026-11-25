package app

import (
	"dgopher/internal/keymap"
	"dgopher/internal/project"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// projectMenu is the context menu of a project's own nodes.
func (a *App) projectMenu(m *ui.Menu, n navNode) {
	p := a.projectByDir(n.projectDir)
	if p == nil {
		return
	}
	if n.kind == nodeConnFolder {
		if p.Err == "" {
			a.connFolderMenu(m, p, n)
		}
		return
	}
	if p.Err == "" {
		// The sidebar lists what is there: the menu makes more.
		for _, kind := range []nodeKind{nodeConnections, nodeDashboards, nodeModels, nodeQueries} {
			if label, run := a.sectionNew(p, kind); label != "" && m.Item(label).Chosen() {
				run()
			}
		}
		if (n.kind == nodeProject || n.kind == nodeConnections) && m.Item("New Folder…").Chosen() {
			a.askNewFolder(p, "", nil)
		}
		if m.Item("Import from DBeaver…").Chosen() {
			a.openDBeaverImport(p)
		}
		if rename, del := a.fileActions(p, n); rename != nil {
			if keymap.Item(m.Item("Rename…"), keymap.NavigatorRename).Chosen() {
				rename()
			}
			if n.kind == nodeQueryFile && m.Item("Duplicate").Chosen() {
				a.duplicateQueryFile(p, n.name)
			}
			if keymap.Item(m.Item("Delete…"), keymap.NavigatorDrop).Chosen() {
				del()
			}
		}
		m.Separator()
		if m.Item("Query History").Chosen() {
			a.openHistoryOf(p)
		}
		if m.Item("Audit Log").Chosen() {
			a.openAuditOf(p)
		}
	}
	if m.Item("Show in Files").Chosen() {
		mygo.Shell.OpenPath(p.Dir)
	}
	if n.kind == nodeProject {
		m.Separator()
		if m.Item("Reload").Chosen() {
			a.reloadProject(p)
		}
		if m.Item("Remove from Sidebar…").Chosen() {
			a.removeProject(p)
		}
	}
}

// addMenu lists what the sidebar's + makes.
func (a *App) addMenu(m *ui.Menu) {
	if len(a.usableProjects()) > 0 && m.Item("New Connection…").Chosen() {
		a.openConnForm(nil)
	}
	// What a project holds, in the project in use: a folder of
	// connections at its top level, and a dashboard or a data model, whose
	// headings show only once there is one.
	if p := a.currentProject(); p != nil && p.Err == "" {
		if m.Item("New Folder…").Chosen() {
			a.askNewFolder(p, "", nil)
		}
		for _, kind := range []nodeKind{nodeDashboards, nodeModels} {
			if label, run := a.sectionNew(p, kind); label != "" && m.Item(label).Chosen() {
				run()
			}
		}
	}
	if len(a.usableProjects()) > 0 && m.Item("Import from DBeaver…").Chosen() {
		a.openDBeaverImport(nil)
	}
	if m.Item("New Project…").Chosen() {
		a.openNewProject(nil)
	}
	if m.Item("Add Existing Folder…").Chosen() {
		a.addExistingProject()
	}
}

// selectProject opens a project in the sidebar and chooses it.
func (a *App) selectProject(p *project.Project) {
	for _, files := range []bool{false, true} {
		n := navNode{kind: nodeProject, projectDir: p.Dir, files: files}
		a.nav.expand(n)
		pane := a.nav.paneOf(n)
		for i := 0; i < pane.tree.Rows(); i++ {
			if pane.tree.Item(i) == n {
				*pane.row = i
				break
			}
		}
	}
}

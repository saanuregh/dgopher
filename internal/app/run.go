package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"dgopher/internal/store"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// keychainService is the service the app's secrets are kept under in the
// system keychain.
const keychainService = "DGopher"

// Run opens the app window and runs until it quits. args are the
// command-line arguments after the program name.
func Run(args []string) error {
	mygo.App.SetName("DGopher")
	if !mygo.App.RequestSingleInstanceLock() {
		return nil // the running app comes to the front
	}
	dir, err := store.DefaultDir()
	if err != nil {
		return err
	}
	st, err := store.Open(dir, store.KeyringSecrets(keychainService))
	if err != nil {
		return err
	}
	a := newApp(st)
	// dgopher <folder> opens a project folder, as a repository's.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if _, err := a.addProject(args[0]); err != nil {
			fmt.Fprintln(os.Stderr, "dgopher:", err)
		}
	}
	applyTheme(a.settings.Theme)
	mygo.App.SetMenu(buildMenu(a))
	mygo.App.OnWindowAllClosed(func() { mygo.App.Quit() })
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     "DGopher",
			Width:     1280,
			Height:    820,
			MinWidth:  760,
			MinHeight: 480,
			StateKey:  "main",
			Content:   ui.View(a.view),
		})
		a.win = win
		// Quitting asks first about what it would lose, as an open
		// transaction: the close waits for the answer.
		// Listeners run on the main thread, as the app's frames do.
		win.OnClose(func(e *mygo.CloseEvent) {
			if !a.requestQuit(func() { win.Close() }) {
				e.PreventDefault()
				win.Invalidate()
			}
		})
		mygo.App.OnSecondInstance(func(args []string, wd string) {
			win.Show()
			win.Focus()
			if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
				dir := args[0]
				if !filepath.IsAbs(dir) {
					dir = filepath.Join(wd, dir)
				}
				a.Post(func() {
					if _, err := a.addProject(dir); err != nil {
						a.ShowError("Could not add the project", err.Error())
					}
				})
			}
		})
	})
	mygo.App.OnQuit(func() {
		a.SaveSettings()
		a.saveWorkspace(true)
		for _, t := range a.tabs {
			t.Close()
		}
		for _, cn := range a.conns {
			if cn.DB != nil {
				cn.DB.Close()
			}
			if cn.KV != nil {
				cn.KV.Close()
			}
		}
	})
	return mygo.App.Run()
}

// buildMenu builds the menu bar, whose items act on the window's app.
func buildMenu(a *App) *mygo.Menu {
	do := func(fn func()) func(*mygo.MenuItem, *mygo.Window) {
		return func(*mygo.MenuItem, *mygo.Window) { a.Post(fn) }
	}
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu, Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			mygo.Separator(),
			{Label: "Settings…", Accelerator: "CmdOrCtrl+,", Click: do(func() { a.settingsOpen = true })},
			mygo.Separator(),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Label: "File", Submenu: []*mygo.MenuItem{
			{Label: "New Connection…", Accelerator: "CmdOrCtrl+N", Click: do(func() { a.openConnForm(nil) })},
			{Label: "New SQL Editor", Accelerator: "CmdOrCtrl+T", Click: do(func() {
				if cn := a.activeConn(); cn != nil {
					a.NewQueryTab(cn, "", "")
				} else {
					a.openPalette(false)
				}
			})},
			{Label: "Open SQL Script…", Accelerator: "CmdOrCtrl+O", Click: do(func() {
				if cn := a.activeConn(); cn != nil && cn.Config.Engine.IsSQL() {
					a.openSQLFile(cn)
				} else {
					a.ShowError("Choose a connection first", "A script opens in an editor of the connection chosen in the navigator.")
				}
			})},
			mygo.Separator(),
			{Label: "New Project…", Click: do(func() { a.openNewProject(nil) })},
			{Label: "Add Existing Folder…", Accelerator: "CmdOrCtrl+Shift+O", Click: do(a.addExistingProject)},
			mygo.Separator(),
			{Label: "Close Tab", Accelerator: "CmdOrCtrl+W", Click: do(func() {
				if len(a.tabs) > 0 {
					a.closeTab(a.active)
				}
			})},
			mygo.Separator(),
			{Label: "Settings…", Hidden: runtime.GOOS == "darwin", Click: do(func() { a.settingsOpen = true })},
			{Role: mygo.RoleQuit, Hidden: runtime.GOOS == "darwin"},
		}},
		{Role: mygo.RoleEditMenu},
		{Label: "View", Submenu: []*mygo.MenuItem{
			{Label: "Command Palette", Accelerator: "CmdOrCtrl+K", Click: do(func() { a.openPalette(false) })},
			{Label: "Open Table…", Accelerator: "CmdOrCtrl+P", Click: do(func() { a.openPalette(true) })},
			{Label: "Query History", Accelerator: "CmdOrCtrl+Y", Click: do(func() { a.openHistory() })},
			{Label: "Audit Log", Accelerator: "CmdOrCtrl+Shift+A", Click: do(a.openAudit)},
			{Label: "Toggle Sidebar", Accelerator: "CmdOrCtrl+B", Click: do(func() { a.sidebarHidden = !a.sidebarHidden })},
			mygo.Separator(),
			{Role: mygo.RoleToggleFullScreen},
			{Role: mygo.RoleToggleDevTools},
		}},
		{Role: mygo.RoleWindowMenu},
	})
}

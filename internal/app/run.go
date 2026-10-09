package app

import (
	"errors"
	"fmt"
	"log"
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
		a.windowFocused = win.IsFocused
		if mygo.NotificationsSupported() {
			a.notify = func(title, body string, onClick func()) {
				n := mygo.NewNotification(mygo.NotificationOptions{Title: title, Body: body})
				n.OnClick(func() {
					win.Show()
					win.Focus()
					onClick()
				})
				// On macOS the first one waits for the user to allow them.
				go func() {
					if err := n.Show(); err != nil && !errors.Is(err, mygo.ErrNotificationsDenied) {
						log.Println("notification:", err)
					}
				}()
			}
		}
		// What the window shows needs no notification left behind.
		mygo.App.OnDidBecomeActive(mygo.ClearNotifications)
		// Quitting asks first about what it would lose, as an open
		// transaction: the quit waits for the answer, shown in the window.
		// Listeners run on the main thread, as the app's frames do.
		mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
			if !a.requestQuit(mygo.App.Quit) {
				e.PreventDefault()
				win.Show()
				win.Focus()
				win.Invalidate()
				return
			}
			a.quitting = true
		})
		win.OnClose(func(e *mygo.CloseEvent) {
			switch {
			case a.quitting:
			case runtime.GOOS == "darwin":
				// A Mac app outlives its window: hiding it keeps the tabs,
				// connections and transactions, and the Dock icon shows it
				// again.
				e.PreventDefault()
				win.Hide()
			case !a.requestQuit(win.Close):
				e.PreventDefault()
				win.Invalidate()
			}
		})
		mygo.App.OnActivate(func(hasVisibleWindows bool) {
			if !hasVisibleWindows {
				win.Show()
				win.Focus()
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
			{Label: "Run SQL File…", Click: do(func() {
				if cn := a.activeConn(); cn != nil && cn.Config.Engine.IsSQL() {
					a.openSQLFileRun(cn, "")
				} else {
					a.ShowError("Choose a connection first", "A file runs on the connection chosen in the navigator.")
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
		windowMenu(),
	})
}

// windowMenu is the Window menu. Elsewhere than on macOS the default one
// has Close Window on CmdOrCtrl+W, the key of Close Tab: two items on one
// key leave the toolkit to choose, and closing the window quits.
func windowMenu() *mygo.MenuItem {
	if runtime.GOOS == "darwin" {
		return &mygo.MenuItem{Role: mygo.RoleWindowMenu}
	}
	return &mygo.MenuItem{Label: "Window", Submenu: []*mygo.MenuItem{{Role: mygo.RoleMinimize}}}
}

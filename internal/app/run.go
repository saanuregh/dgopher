package app

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"dgopher/internal/keymap"
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
	a.loadThemes()
	a.applyTheme()
	mygo.App.SetMenu(buildMenu(a))
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     "DGopher",
			Width:     1280,
			Height:    820,
			MinWidth:  760,
			MinHeight: 480,
			StateKey:  "main",
			Content:   ui.View(a.windowView(a.main)),
		})
		a.main.native = win
		a.windowFocused = a.anyFocused
		// The dialogs move to the window gaining the focus.
		win.OnFocus(func() { a.lastFocused = a.main; a.invalidate() })
		a.makeWindow = func(w *window) {
			w.native = mygo.NewWindow(mygo.WindowOptions{
				Title:     "DGopher",
				Width:     1000,
				Height:    720,
				MinWidth:  560,
				MinHeight: 360,
				Content:   ui.View(a.windowView(w)),
			})
			w.native.OnFocus(func() { a.lastFocused = w; a.invalidate() })
			// Closed, its tabs go back to the main window.
			w.native.OnClose(func(*mygo.CloseEvent) {
				if !a.quitting {
					w.native = nil // closing already
					a.closeWindow(w)
				}
			})
		}
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
			default:
				// Closing the main window quits, the other windows with it:
				// quitting asks first about what it would lose, in every
				// window.
				e.PreventDefault()
				mygo.App.Quit()
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
		for _, t := range a.everyTab() {
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

// accelerator is a command's first key as the menu bar takes it, "" for
// none.
func accelerator(id string) string {
	if ch, ok := keymap.Primary(id); ok {
		return ch.Accelerator()
	}
	return ""
}

// menuCommands are the commands of the menu bar that have keys: the menu
// takes the first key of each, the window the others.
var menuCommands = []string{keymap.Settings, keymap.NewConnection, keymap.NewEditor, keymap.OpenScript, keymap.AddFolder,
	keymap.CloseTab, keymap.Palette, keymap.OpenTable, keymap.History, keymap.AuditLog, keymap.ToggleSidebar}

// menuAction is what a command of the menu bar does.
func (a *App) menuAction(id string) func() {
	switch id {
	case keymap.Settings:
		return a.openSettings
	case keymap.NewConnection:
		return func() { a.openConnForm(nil) }
	case keymap.NewEditor:
		return func() {
			if cn := a.activeConn(); cn != nil {
				a.NewQueryTab(cn, "", "")
			} else {
				a.openPalette(false)
			}
		}
	case keymap.OpenScript:
		return func() {
			if cn := a.activeConn(); cn != nil && cn.Config.Engine.IsSQL() {
				a.openSQLFile(cn)
			} else {
				a.ShowError("Choose a connection first", "A script opens in an editor of the connection chosen in the navigator.")
			}
		}
	case keymap.AddFolder:
		return a.addExistingProject
	case keymap.CloseTab:
		return func() {
			if len(a.tabs) > 0 {
				a.closeTab(a.active)
			}
		}
	case keymap.Palette:
		return func() { a.openPalette(false) }
	case keymap.OpenTable:
		return func() { a.openPalette(true) }
	case keymap.History:
		return a.openHistory
	case keymap.AuditLog:
		return a.openAudit
	case keymap.ToggleSidebar:
		return func() { a.sidebarHidden = !a.sidebarHidden }
	}
	panic("no menu command " + id)
}

// buildMenu builds the menu bar, whose items act on the window's app: the
// commands' keys as they are set, built again once they change.
func buildMenu(a *App) *mygo.Menu {
	do := func(fn func()) func(*mygo.MenuItem, *mygo.Window) {
		return func(*mygo.MenuItem, *mygo.Window) { a.Post(fn) }
	}
	item := func(label, id string) *mygo.MenuItem {
		return &mygo.MenuItem{Label: label, Accelerator: accelerator(id), Click: do(a.menuAction(id))}
	}
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu, Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			mygo.Separator(),
			item("Settings…", keymap.Settings),
			mygo.Separator(),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Label: "File", Submenu: []*mygo.MenuItem{
			item("New Connection…", keymap.NewConnection),
			item("New SQL Editor", keymap.NewEditor),
			item("Open SQL Script…", keymap.OpenScript),
			{Label: "Run SQL File…", Click: do(func() {
				if cn := a.activeConn(); cn != nil && cn.Config.Engine.IsSQL() {
					a.openSQLFileRun(cn, "")
				} else {
					a.ShowError("Choose a connection first", "A file runs on the connection chosen in the navigator.")
				}
			})},
			mygo.Separator(),
			{Label: "New Project…", Click: do(func() { a.openNewProject(nil) })},
			item("Add Existing Folder…", keymap.AddFolder),
			mygo.Separator(),
			item("Close Tab", keymap.CloseTab),
			mygo.Separator(),
			{Label: "Settings…", Hidden: runtime.GOOS == "darwin", Click: do(a.menuAction(keymap.Settings))},
			{Role: mygo.RoleQuit, Hidden: runtime.GOOS == "darwin"},
		}},
		{Role: mygo.RoleEditMenu},
		{Label: "View", Submenu: []*mygo.MenuItem{
			item("Command Palette", keymap.Palette),
			item("Open Table…", keymap.OpenTable),
			item("Query History", keymap.History),
			item("Audit Log", keymap.AuditLog),
			item("Toggle Sidebar", keymap.ToggleSidebar),
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

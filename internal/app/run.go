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
	"dgopher/internal/state"
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
	// The layout without a file of its own lasts the run: never a reason
	// not to start.
	if dir, err := store.DefaultDataDir(); err != nil {
		log.Println("ui state:", err)
	} else if ui, err := state.OpenUI(dir); err != nil {
		log.Println("ui state:", err)
	} else {
		a.useUIState(ui)
	}
	// dgopher <folder> opens a project folder, as a repository's.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if _, err := a.addProject(args[0]); err != nil {
			fmt.Fprintln(os.Stderr, "dgopher:", err)
		}
	}
	a.loadThemes()
	a.applyTheme()
	setMenuBar(a)
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:          "DGopher",
			TitleBarStyle:  mygo.TitleBarHidden,
			TitleBarHeight: titleBarHeight,
			Width:          1280,
			Height:         820,
			MinWidth:       760,
			MinHeight:      480,
			StateKey:       "main",
			Content:        ui.View(a.windowView(a.main)),
		})
		a.main.native = win
		a.windowFocused = a.anyFocused
		// The dialogs move to the window gaining the focus.
		win.OnFocus(func() { a.lastFocused = a.main; a.invalidate() })
		a.makeWindow = func(w *window) {
			w.native = mygo.NewWindow(mygo.WindowOptions{
				Title:          "DGopher",
				TitleBarStyle:  mygo.TitleBarHidden,
				TitleBarHeight: titleBarHeight,
				Width:          1000,
				Height:         720,
				MinWidth:       560,
				MinHeight:      360,
				Content:        ui.View(a.windowView(w)),
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
	mygo.App.OnQuit(a.shutdown)
	return mygo.App.Run()
}

// shutdown ends what the app holds as it quits, once the user agreed to
// what quitting loses: it saves the settings and the workspace, closes the
// tabs, stops the work running and waits for it to roll back what it left
// open, then closes the connections and the projects' state files.
func (a *App) shutdown() {
	a.SaveSettings()
	a.saveWorkspace(true)
	for _, t := range a.everyTab() {
		t.Close()
	}
	stopJobs(a.runningJobs())
	for _, cn := range a.conns {
		if cn.DB != nil {
			cn.DB.Close()
		}
		if cn.KV != nil {
			cn.KV.Close()
		}
	}
	for _, p := range a.projects {
		p.Close()
	}
}

// accelerator is a command's first key as the menu bar takes it, "" for
// none.
func accelerator(id string) string {
	if ch, ok := keymap.Primary(id); ok {
		return ch.Accelerator()
	}
	return ""
}

// menuCommands are the commands of the menus that have keys: the system's
// menu bar takes the first key of each, where there is one, and the window
// the others.
var menuCommands = []string{keymap.Settings, keymap.NewConnection, keymap.NewEditor, keymap.OpenScript, keymap.AddFolder,
	keymap.CloseTab, keymap.Palette, keymap.OpenTable, keymap.History, keymap.AuditLog, keymap.ToggleSidebar, keymap.FullScreen, keymap.Quit}

// menuAction is what a command of the menu bar does.
func (a *App) menuAction(id string) func() {
	switch id {
	case keymap.Settings:
		return a.openSettings
	case keymap.FullScreen:
		return func() {
			if w := a.window.native; w != nil {
				w.SetFullScreen(!w.IsFullScreen())
			}
		}
	case keymap.Quit:
		return mygo.App.Quit // which asks first, in OnBeforeQuit
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
		return func() { a.layout.SidebarHidden = !a.layout.SidebarHidden }
	}
	panic("no menu command " + id)
}

// nativeMenuBar is whether the app has the system's menu bar: on macOS,
// at the top of the screen, where its apps keep one. Elsewhere the menu is
// the title bar's (titleBar), drawn by the app as on macOS.
const nativeMenuBar = runtime.GOOS == "darwin"

// setMenuBar gives the app the system's menu bar where it has one, with the
// commands' keys as they are set.
func setMenuBar(a *App) {
	if nativeMenuBar {
		mygo.App.SetMenu(buildMenu(a))
	}
}

// menuGroup is a menu of the app's, which the menu bar and the title
// bar's menu both show.
type menuGroup struct {
	label string
	items []menuEntry
}

// menuEntry is an item of a menuGroup; one without a label separates
// groups of items.
type menuEntry struct {
	label string
	// id is the keymap command it runs, whose key it shows; "" for none.
	id  string
	run func()
}

// appMenus are the app's menus: what File, View and Help hold.
func (a *App) appMenus() []menuGroup {
	cmd := func(label, id string) menuEntry { return menuEntry{label: label, id: id, run: a.menuAction(id)} }
	return []menuGroup{
		{"File", []menuEntry{
			cmd("New Connection…", keymap.NewConnection),
			cmd("New SQL Editor", keymap.NewEditor),
			cmd("Open SQL Script…", keymap.OpenScript),
			{label: "Run SQL File…", run: func() {
				if cn := a.activeConn(); cn != nil && cn.Config.Engine.IsSQL() {
					a.openSQLFileRun(cn, "")
				} else {
					a.ShowError("Choose a connection first", "A file runs on the connection chosen in the navigator.")
				}
			}},
			{},
			{label: "New Project…", run: func() { a.openNewProject(nil) }},
			cmd("Add Existing Folder…", keymap.AddFolder),
			{},
			cmd("Close Tab", keymap.CloseTab),
		}},
		{"View", []menuEntry{
			cmd("Command Palette", keymap.Palette),
			cmd("Open Table…", keymap.OpenTable),
			cmd("Query History", keymap.History),
			cmd("Audit Log", keymap.AuditLog),
			cmd("Toggle Sidebar", keymap.ToggleSidebar),
			{},
			cmd("Toggle Full Screen", keymap.FullScreen),
		}},
		// The list of keys shows no key: its key toggles it, which the
		// window handles, Escape closing it as well.
		{"Help", []menuEntry{
			{label: "Keyboard Shortcuts", run: func() { a.shortcutsOpen = true }},
			{label: "Customize Keyboard Shortcuts…", run: func() { a.keys = &keysEditor{open: true} }},
			{},
			{label: "Take the Tour", run: a.startTour},
		}},
	}
}

// buildMenu builds the system's menu bar of macOS, whose items act on the
// window's app: the commands' keys as they are set, built again once they
// change.
func buildMenu(a *App) *mygo.Menu {
	native := func(g menuGroup) []*mygo.MenuItem {
		var out []*mygo.MenuItem
		for _, e := range g.items {
			if e.label == "" {
				out = append(out, mygo.Separator())
				continue
			}
			run := e.run
			out = append(out, &mygo.MenuItem{Label: e.label, Accelerator: accelerator(e.id),
				Click: func(*mygo.MenuItem, *mygo.Window) { a.Post(run) }})
		}
		return out
	}
	menus := a.appMenus()
	file, view, help := menus[0], menus[1], menus[2]
	settings := &mygo.MenuItem{Label: "Settings…", Accelerator: accelerator(keymap.Settings),
		Click: func(*mygo.MenuItem, *mygo.Window) { a.Post(a.openSettings) }}
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu, Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			mygo.Separator(),
			settings,
			mygo.Separator(),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit, Accelerator: accelerator(keymap.Quit)},
		}},
		{Label: file.label, Submenu: native(file)},
		{Role: mygo.RoleEditMenu},
		{Label: view.label, Submenu: native(view)},
		{Role: mygo.RoleWindowMenu},
		{Label: help.label, Submenu: native(help)},
	})
}

package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/datamodel"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/modelview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// reasonTab is a tab without a connection whose closing loses what
// reason says, as a data model with a table form not saved.
type reasonTab struct {
	plainTab
	reason string
}

func (t *reasonTab) CloseReason() string { return t.reason }

// Quitting asks about a tab without a connection that closing would
// lose work of, as a data model's unsaved table.
func TestQuitAsksAboutTabsWithoutConnection(t *testing.T) {
	a := newTestApp(t)
	a.AddTab(&reasonTab{plainTab{"orders model"}, "The table's changes are not saved to the model. Closing discards them."})
	quit := false
	if a.requestQuit(func() { quit = true }) || a.closing == nil {
		t.Fatalf("quit without asking: %+v", a.closing)
	}
	r := a.closing
	if r.title != "Quit DGopher?" || r.action != "Quit" || !strings.Contains(r.reason, "orders model: The table's changes") {
		t.Fatalf("the quit prompt: %+v", r)
	}
	r.onClose()
	if !quit {
		t.Fatal("agreeing did not quit")
	}
}

// Quitting asks about what closing an editor loses even when
// disconnecting it would lose nothing, as text that conflicts with its
// file on disk, and says it closes the tabs.
func TestQuitAsksAboutCloseOnlyLosses(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newAppEditor(t, a, tt, cn, "SELECT 1")
	q.Path, q.Saved, q.DiskConflict = filepath.Join(t.TempDir(), "x.sql"), "SELECT 2", "changed on disk"
	if q.DisconnectReason() != "" || q.CloseReason() == "" {
		t.Fatalf("the fixture: disconnect %q, close %q", q.DisconnectReason(), q.CloseReason())
	}
	quit := false
	if a.requestQuit(func() { quit = true }) || quit || a.closing == nil {
		t.Fatalf("quit without asking: quit %v, %+v", quit, a.closing)
	}
	if r := a.closing.reason; !strings.Contains(r, "changed on disk") || !strings.Contains(r, "Quitting closes") {
		t.Fatalf("the quit prompt: %q", r)
	}
}

// A quit asked while another question is open replaces it: the quit is
// not dropped, and the other question counts as cancelled.
func TestQuitWhileAnotherQuestionIsOpen(t *testing.T) {
	a := newTestApp(t)
	a.AddTab(&reasonTab{plainTab{"m"}, "Unsaved."})
	discarded := false
	a.AskDiscard("Discard the changes?", "They go.", "Discard", func() { discarded = true })
	if a.requestQuit(func() {}) || a.closing == nil || a.closing.title != "Quit DGopher?" {
		t.Fatalf("the quit was dropped: %+v", a.closing)
	}
	// A second quit keeps the quit prompt.
	r := a.closing
	if a.requestQuit(func() {}) || a.closing != r {
		t.Fatalf("a second quit: %+v", a.closing)
	}
	if discarded {
		t.Fatal("the replaced question ran")
	}
}

// projectViews opens a dashboard and a data model of a project.
func projectViews(t *testing.T, a *App, p *project.Project) (*dashboard.Tab, *modelview.Tab) {
	t.Helper()
	dpath := dashboard.File(p, "Sales")
	if _, err := (&dashboard.Dashboard{Name: "Sales", Panels: []dashboard.Panel{}}).Save(dpath, nil); err != nil {
		t.Fatal(err)
	}
	mpath := datamodel.File(p, "Shop")
	if _, err := (&datamodel.Model{Name: "Shop", Engine: db.SQLite, Tables: []db.TableDesign{}}).Save(mpath, nil); err != nil {
		t.Fatal(err)
	}
	a.openDashboard(p, dpath)
	a.openModel(p, mpath)
	var d *dashboard.Tab
	var m *modelview.Tab
	for _, tb := range a.everyTab() {
		switch tb := tb.(type) {
		case *dashboard.Tab:
			d = tb
		case *modelview.Tab:
			m = tb
		}
	}
	if d == nil || m == nil {
		t.Fatalf("opened dashboard %v, model %v", d, m)
	}
	return d, m
}

// tabsOfProject lists the dashboards and models of p still open.
func tabsOfProject(a *App, p *project.Project) []widgets.Tab {
	var out []widgets.Tab
	for _, tb := range a.everyTab() {
		switch tb := tb.(type) {
		case *dashboard.Tab:
			if tb.Project == p {
				out = append(out, tb)
			}
		case *modelview.Tab:
			if tb.Project == p {
				out = append(out, tb)
			}
		}
	}
	return out
}

// Removing a project always asks, and closes its dashboards and models
// with the tabs of its connections.
func TestRemoveProjectClosesItsDashboardsAndModels(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	projectViews(t, a, p)
	a.removeProject(p)
	if a.closing == nil {
		t.Fatalf("removed without asking: %d projects", len(a.projects))
	}
	r := a.closing
	if r.title != "Remove "+p.Name+" from the sidebar?" || r.action != "Remove" || !strings.Contains(r.reason, "folder is not touched") {
		t.Fatalf("the prompt: %+v", r)
	}
	r.onClose()
	if len(a.projects) != 0 {
		t.Fatalf("%d projects after removing", len(a.projects))
	}
	if left := tabsOfProject(a, p); len(left) > 0 {
		t.Fatalf("%d tabs of the removed project stay open", len(left))
	}
}

// Reloading a project binds its dashboards and models to the project
// read again: none keeps the closed one.
func TestReloadProjectRebindsDashboardsAndModels(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	projectViews(t, a, p)
	a.reloadProject(p)
	if a.closing != nil {
		a.closing.onClose()
	}
	if left := tabsOfProject(a, p); len(left) > 0 {
		t.Fatalf("%d tabs keep the closed project", len(left))
	}
	if len(a.projects) != 1 || a.projects[0] == p {
		t.Fatalf("projects after reloading: %v", a.projects)
	}
	if got := len(tabsOfProject(a, a.projects[0])); got != 2 {
		t.Fatalf("%d dashboards and models open again", got)
	}
}

// Reloading a project asks about what closing its tabs loses, worded as
// a reload.
func TestReloadProjectAsksAboutTabsWithoutConnection(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	projectViews(t, a, p)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newAppEditor(t, a, tt, cn, "SELECT 1")
	q.Path, q.Saved, q.DiskConflict = filepath.Join(t.TempDir(), "x.sql"), "SELECT 2", "changed on disk"
	a.reloadProject(p)
	if a.closing == nil || a.closing.title != "Reload "+p.Name+"?" || !strings.Contains(a.closing.reason, "changed on disk") ||
		!strings.Contains(a.closing.reason, "Reloading closes") {
		t.Fatalf("the reload prompt: %+v", a.closing)
	}
}

// Deleting a connection with an open transaction offers to commit it.
func TestDeleteConnectionOffersCommit(t *testing.T) {
	a, tt, q, _ := txApp(t, 0)
	a.askDeleteConn(q.Conn)
	if a.closing == nil || len(a.closing.txs) != 1 || a.closing.txs[0] != q {
		t.Fatalf("the delete prompt: %+v", a.closing)
	}
	tt.Frame()
	if texts := strings.Join(tt.Texts(), "|"); !strings.HasSuffix(texts, "Cancel|Roll Back|Commit") {
		t.Fatalf("the dialog's buttons: %s", texts)
	}
}

// Deleting a connection of a project that may not be written says so
// before asking.
func TestDeleteConnectionChecksProjectFirst(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(cn.Project)
	if err := os.WriteFile(filepath.Join(cn.Project.Dir, project.File), []byte(`{"connections": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a.askDeleteConn(cn)
	if a.closing != nil || len(a.alerts) != 1 || !strings.Contains(a.alerts[0].report.Title, "Could not delete lite") {
		t.Fatalf("asked %v, alerts %d", a.closing != nil, len(a.alerts))
	}
}

// The middle button on a pinned tab says why it stays, as the close key
// does.
func TestMiddleClickOnPinnedTabSaysWhy(t *testing.T) {
	a, _ := tabsApp(t)
	a.window.tabs().SetPinned(a.tabs().At(0), true)
	middle := ui.InputEvent{Kind: ui.InputPointerDown, Button: middleButton}
	if !a.closeOnMiddlePress(a.window, a.tabs().At(0), middle) {
		t.Fatal("the press went on")
	}
	if !slices.ContainsFunc(a.toasts, func(p pendingToast) bool { return strings.Contains(p.text, "a is pinned") }) {
		t.Fatalf("toasts %+v", a.toasts)
	}
	if a.tabs().Len() != 4 {
		t.Fatalf("%d tabs", a.tabs().Len())
	}
}

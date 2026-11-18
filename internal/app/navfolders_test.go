package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// folderApp is an app of one project with connections named names, and
// the tester drawing it.
func folderApp(t *testing.T, names ...string) (*App, *ui.Tester, *project.Project, []*connection.Conn) {
	t.Helper()
	a := newBareApp(t)
	p, err := a.addProject(newProjectDir(t, "shop"))
	if err != nil {
		t.Fatal(err)
	}
	var conns []*connection.Conn
	for _, name := range names {
		conns = append(conns, a.addConn(p, db.Config{Name: name, Engine: db.SQLite, Database: ":memory:"}))
	}
	tt := ui.NewTester(a.view, 1200, 760)
	tt.Frame()
	return a, tt, p, conns
}

// navLabels are the navigator's rows as their kinds and names, in order.
func navLabels(a *App) []string {
	var out []string
	for i := range a.nav.tree.Rows() {
		n := a.nav.tree.Item(i)
		switch n.kind {
		case nodeConnFolder:
			out = append(out, "folder "+n.name)
		case nodeConn:
			out = append(out, "conn "+a.connByID(n.conn).Config.Name)
		}
	}
	return out
}

func projectFile(t *testing.T, p *project.Project) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(p.Dir, project.File))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func chooseOn(t *testing.T, tt *ui.Tester, row string, item ...string) {
	t.Helper()
	if err := tt.RightClick(row); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	if err := tt.ChooseMenuItem(item...); err != nil {
		t.Fatalf("%v; menu %q", err, tt.Menu())
	}
	tt.Frame()
}

// Folders are made, filled, renamed and deleted from the navigator's
// menus, and kept in dgopher.json; a connection is never deleted with
// its folder.
func TestConnectionFolders(t *testing.T) {
	a, tt, p, conns := folderApp(t, "zeta", "Alpha", "mid")
	zeta, alpha := conns[0], conns[1]
	if got := navLabels(a); !slices.Equal(got, []string{"conn Alpha", "conn mid", "conn zeta"}) {
		t.Fatalf("top level by name: %q", got)
	}

	chooseOn(t, tt, "Connections", "New Folder…")
	if a.renaming == nil || a.renaming.action != "Create" {
		t.Fatalf("no new folder form: %+v", a.renaming)
	}
	for _, bad := range []string{" ", "a/b"} {
		if err := a.renaming.rename(bad); err == nil {
			t.Errorf("folder %q was made", bad)
		}
	}
	if err := a.renaming.rename("Prod"); err != nil {
		t.Fatal(err)
	}
	a.renaming = nil
	a.askNewFolder(p, "", nil)
	if err := a.renaming.rename("prod"); err == nil {
		t.Error("a second Prod, by case, was made")
	}
	a.renaming = nil
	tt.Frame()
	if got := navLabels(a); !slices.Equal(got, []string{"folder Prod", "conn Alpha", "conn mid", "conn zeta"}) {
		t.Fatalf("folders first: %q", got)
	}

	chooseOn(t, tt, "Prod", "New Folder…")
	if err := a.renaming.rename("EU"); err != nil {
		t.Fatal(err)
	}
	a.renaming = nil
	tt.Frame()
	chooseOn(t, tt, "zeta", "Move to Folder", "Prod/EU")
	if zeta.Config.Folder != "Prod/EU" {
		t.Fatalf("moved to %q", zeta.Config.Folder)
	}
	// New Folder… on a connection makes it beside it, and moves it in.
	chooseOn(t, tt, "Alpha", "New Folder…")
	if err := a.renaming.rename("Mine"); err != nil {
		t.Fatal(err)
	}
	a.renaming = nil
	if alpha.Config.Folder != "Mine" {
		t.Fatalf("Alpha in %q", alpha.Config.Folder)
	}
	tt.Frame()
	if got := navLabels(a); !slices.Equal(got, []string{"folder Mine", "conn Alpha", "folder Prod", "folder Prod/EU", "conn zeta", "conn mid"}) {
		t.Fatalf("tree: %q", got)
	}
	testutil.Snapshot(t, tt, "connection-folders")
	file := projectFile(t, p)
	for _, want := range []string{`"folder": "Prod/EU"`, `"Prod/EU": {}`, `"Mine": {}`} {
		if !strings.Contains(file, want) {
			t.Errorf("the file lacks %s:\n%s", want, file)
		}
	}

	chooseOn(t, tt, "Prod", "Rename…")
	if err := a.renaming.rename("Live"); err != nil {
		t.Fatal(err)
	}
	a.renaming = nil
	if zeta.Config.Folder != "Live/EU" || !slices.Equal(p.Folders, []string{"Live", "Live/EU", "Mine"}) {
		t.Fatalf("renamed: zeta in %q, folders %q", zeta.Config.Folder, p.Folders)
	}
	if file := projectFile(t, p); strings.Contains(file, "Prod") {
		t.Errorf("the file keeps Prod:\n%s", file)
	}

	// Deleted, with its subfolder; its connections go to its parent.
	tt.Frame()
	chooseOn(t, tt, "Live", "Delete…")
	if a.closing == nil || !strings.Contains(a.closing.reason, "will not be deleted") {
		t.Fatalf("no question: %+v", a.closing)
	}
	a.closing.onClose()
	a.closing = nil
	if zeta.Config.Folder != "" || !slices.Equal(p.Folders, []string{"Mine"}) || len(a.projectConns(p)) != 3 {
		t.Fatalf("deleted: zeta in %q, folders %q, %d connections", zeta.Config.Folder, p.Folders, len(a.projectConns(p)))
	}

	// A save refused by a changed file leaves the folders as they were.
	os.WriteFile(filepath.Join(p.Dir, project.File), []byte(projectFile(t, p)+" "), 0o644)
	a.moveConnsToFolder(p, []string{zeta.Config.ID}, "Mine")
	if zeta.Config.Folder != "" {
		t.Errorf("moved though not saved: %q", zeta.Config.Folder)
	}
	a.askNewFolder(p, "", nil)
	if err := a.renaming.rename("Other"); err == nil || slices.Contains(p.Folders, "Other") {
		t.Errorf("made though not saved: %v, %q", err, p.Folders)
	}
}

// A folder is muted until a connection in it, at any depth, is
// connected.
func TestConnectionFolderConnected(t *testing.T) {
	a, _, p, conns := folderApp(t, "deep")
	p.Folders = []string{"A", "A/B"}
	conns[0].Config.Folder = "A/B"
	if a.folderConnected(p, "A") {
		t.Fatal("connected while idle")
	}
	conns[0].Status = connection.StatusConnected
	if !a.folderConnected(p, "A") || !a.folderConnected(p, "A/B") {
		t.Fatal("not connected")
	}
}

// Connections and folders are dragged onto folders and onto the
// Connections section; never into another project, nor a folder into
// itself.
func TestDragConnectionToFolder(t *testing.T) {
	a, tt, p, conns := folderApp(t, "orders")
	cn := conns[0]
	other, err := a.addProject(newProjectDir(t, "else"))
	if err != nil {
		t.Fatal(err)
	}
	other.Folders = []string{"Theirs"}
	p.Folders = []string{"Live", "Live/EU", "Mine"}
	a.saveProject(p)
	a.saveProject(other)
	a.nav.expand(navNode{kind: nodeProject, projectDir: p.Dir})
	a.nav.expand(navNode{kind: nodeProject, projectDir: other.Dir})
	a.nav.expand(navNode{kind: nodeConnections, projectDir: other.Dir})
	a.expandFolder(p, "Live/EU")
	tt.Frame()

	dragTo(t, tt, "orders", "Mine")
	if cn.Config.Folder != "Mine" {
		t.Fatalf("dropped on Mine: %q", cn.Config.Folder)
	}
	if !strings.Contains(projectFile(t, p), `"folder": "Mine"`) {
		t.Errorf("not saved:\n%s", projectFile(t, p))
	}
	dragTo(t, tt, "orders", "Theirs")
	if cn.Config.Folder != "Mine" || cn.Project != p {
		t.Fatalf("dropped in another project: %q", cn.Config.Folder)
	}
	// A folder goes into another, with what it holds.
	dragTo(t, tt, "Mine", "EU")
	if cn.Config.Folder != "Live/EU/Mine" || !slices.Contains(p.Folders, "Live/EU/Mine") || slices.Contains(p.Folders, "Mine") {
		t.Fatalf("folder moved: %q, folders %q", cn.Config.Folder, p.Folders)
	}
	if !slices.Contains(navLabels(a), "conn orders") {
		t.Fatalf("the folder moved closed: %q", navLabels(a))
	}
	// Not into itself, nor a folder in it.
	dragTo(t, tt, "Live", "EU")
	if !slices.Equal(p.Folders, []string{"Live", "Live/EU", "Live/EU/Mine"}) {
		t.Fatalf("moved into itself: %q", p.Folders)
	}
	// The section takes it to the top level: the first Connections is
	// shop's, listed first.
	dragTo(t, tt, "orders", "Connections")
	if cn.Config.Folder != "" {
		t.Fatalf("dropped on the section: %q", cn.Config.Folder)
	}
}

// The connection form chooses a connection's folder: preset from the
// folder a new one is made in, reset when its project changes.
func TestConnectionFormFolder(t *testing.T) {
	a, tt, p, conns := folderApp(t, "lite")
	cn := conns[0]
	p.Folders = []string{"Prod"}
	a.saveProject(p)

	a.openConnForm(cn)
	tt.Frame()
	if !tt.HasText("Folder") {
		t.Fatalf("no Folder field: %q", tt.Texts())
	}
	tall := ui.NewTester(a.view, 1200, 1000)
	tall.Frame()
	tall.Advance(time.Second) // the dialog comes in
	testutil.Snapshot(t, tall, "connection-form-folder")
	a.connForm.cfg.Folder = "Prod"
	a.saveConnForm(a.connForm, false)
	if cn.Config.Folder != "Prod" || !strings.Contains(projectFile(t, p), `"folder": "Prod"`) {
		t.Fatalf("saved in %q:\n%s", cn.Config.Folder, projectFile(t, p))
	}
	a.connForm = nil

	a.newConnIn(p, "Prod")
	if a.connForm == nil || a.connForm.cfg.Folder != "Prod" {
		t.Fatalf("new connection form: %+v", a.connForm)
	}
	other, err := a.addProject(newProjectDir(t, "else"))
	if err != nil {
		t.Fatal(err)
	}
	a.connForm.project = other
	tt.Frame()
	if a.connForm.cfg.Folder != "" {
		t.Errorf("kept Prod in another project: %q", a.connForm.cfg.Folder)
	}
}

// A press on a row's button that moves a little still clicks it, though
// the rows drag.
func TestNavigatorButtonClicksThoughRowsDrag(t *testing.T) {
	a, tt, _, _ := folderApp(t, "orders")
	r, ok := tt.Find("New Connection…")
	if !ok {
		t.Fatalf("no button: %q", tt.Texts())
	}
	x, y := float32(r.X+r.W/2), float32(r.Y+r.H/2)
	tt.Move(x, y)
	tt.Frame()
	tt.Press(x, y)
	tt.Move(x+3, y+3)
	tt.Move(x+6, y+5)
	tt.Release(x+6, y+5)
	tt.Frame()
	if a.connForm == nil {
		t.Fatal("the button did not click")
	}
}

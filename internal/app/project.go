package app

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// loadProjects lists the projects of the last run, those that fail too.
func (a *App) loadProjects() {
	for _, dir := range a.settings.Projects {
		p, pc, err := project.Load(dir, false)
		if err != nil {
			log.Println("project:", err)
			p.Err = err.Error()
		}
		a.adoptProject(p, pc, len(a.projects))
	}
}

// addProject lists a project folder in the sidebar, creating its
// dgopher.json when it has none; a folder listed already is selected.
func (a *App) addProject(dir string) (*project.Project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if p := a.projectByDir(dir); p != nil {
		a.selectProject(p)
		return p, nil
	}
	p, pc, err := project.Load(dir, true)
	if err != nil {
		return nil, err
	}
	a.adoptProject(p, pc, len(a.projects))
	a.settings.Projects = append(a.settings.Projects, dir)
	a.SaveSettings()
	a.restoreWorkspace(p)
	a.selectProject(p)
	return p, nil
}

func (a *App) projectByDir(dir string) *project.Project {
	for _, p := range a.projects {
		if p.Dir == dir {
			return p
		}
	}
	return nil
}

// ProjectConfigs are the configurations of the project's connections, as
// its file keeps them.
func (a *App) ProjectConfigs(p *project.Project) []db.Config {
	var out []db.Config
	for _, cn := range a.projectConns(p) {
		out = append(out, cn.Config)
	}
	return out
}

func (a *App) projectConns(p *project.Project) []*connection.Conn {
	var out []*connection.Conn
	for _, cn := range a.conns {
		if cn.Project == p {
			out = append(out, cn)
		}
	}
	return out
}

// adoptProject puts a read project in the app at index i.
func (a *App) adoptProject(p *project.Project, pc project.Config, i int) {
	a.projects = slices.Insert(a.projects, i, p)
	if p.Err != "" {
		return
	}
	a.auditMu.Lock()
	if p.Audit != nil {
		a.auditLogs[p.Prefix] = p.Audit
	}
	a.auditMu.Unlock()
	for _, cfg := range pc.Connections {
		if cfg.ID == "" {
			cfg.ID = project.Slug(cfg.Name)
		}
		// Whatever a file says, it holds no secrets, and an environment
		// it misspells is production.
		cfg.Password, cfg.SSH.Password, cfg.SSH.KeyPassphrase = "", "", ""
		cfg.Env = db.NormalizeEnvironment(cfg.Env)
		cfg.ID = p.Prefix + cfg.ID
		if cfg.Engine.IsFile() {
			cfg.Database = p.ResolvePath(cfg.Database)
		}
		cn := &connection.Conn{Config: cfg, Project: p}
		cn.Reset()
		a.conns = append(a.conns, cn)
	}
	a.ScanQueries(p, true)
}

// removeProject takes a project off the sidebar, once its tabs may close.
// Its folder is not touched.
func (a *App) removeProject(p *project.Project) {
	a.requestDisconnect("Remove "+p.Name+" from the sidebar?", a.projectConns(p), func() { a.dropProject(p) })
}

// dropProject forgets a project: its connections, tabs and audit log.
func (a *App) dropProject(p *project.Project) int {
	if p.Err == "" {
		a.saveWorkspaceOf(p)
	}
	for i := len(a.conns) - 1; i >= 0; i-- {
		if a.conns[i].Project == p {
			a.disconnect(a.conns[i])
			a.conns = slices.Delete(a.conns, i, i+1)
		}
	}
	a.auditMu.Lock()
	delete(a.auditLogs, p.Prefix)
	a.auditMu.Unlock()
	p.Close()
	i := slices.Index(a.projects, p)
	if i >= 0 {
		a.projects = slices.Delete(a.projects, i, i+1)
	}
	a.settings.Projects = slices.DeleteFunc(a.settings.Projects, func(d string) bool { return d == p.Dir })
	a.SaveSettings()
	return i
}

// reloadProject reads a project's folder again, as after a git pull,
// once its tabs may close.
func (a *App) reloadProject(p *project.Project) {
	a.requestDisconnect("Reload "+p.Name+"?", a.projectConns(p), func() {
		i := a.dropProject(p)
		q, pc, err := project.Load(p.Dir, false)
		if err != nil {
			q.Err = err.Error()
		}
		a.adoptProject(q, pc, max(i, 0))
		a.settings.Projects = slices.Insert(a.settings.Projects, min(max(i, 0), len(a.settings.Projects)), p.Dir)
		a.SaveSettings()
		if err == nil {
			a.restoreWorkspace(q)
		}
	})
}

// currentProject is the project of the connection in front or chosen in
// the sidebar, else of the project chosen in the sidebar, else the only
// one; nil when there is none to choose.
func (a *App) currentProject() *project.Project {
	if cn := a.activeConn(); cn != nil {
		return cn.Project
	}
	if a.nav.row >= 0 && a.nav.row < a.nav.tree.Rows() {
		if p := a.projectByDir(a.nav.tree.Item(a.nav.row).projectDir); p != nil {
			return p
		}
	}
	if len(a.projects) == 1 {
		return a.projects[0]
	}
	return nil
}

// withProject runs then with the current project. Without one, it asks
// for a new project when there is none, or for a choice in the sidebar.
func (a *App) withProject(then func(*project.Project)) {
	if p := a.currentProject(); p != nil && p.Err == "" {
		then(p)
		return
	}
	if len(a.projects) == 0 {
		a.openNewProject(then)
		return
	}
	a.ShowError("Which project?", "Choose a project in the sidebar first: every connection belongs to one.")
}

// saveProject writes a project's connections and snippets back to its
// file, in an order that changes only when they do.
func (a *App) saveProject(p *project.Project) {
	if err := p.Save(a.ProjectConfigs(p)); err != nil {
		a.ShowError("Could not save "+project.File+" of "+p.Name, err.Error())
	}
}

// uniqueConnID returns an ID for a new connection of a project.
func (a *App) uniqueConnID(p *project.Project, name string) string {
	base := p.Prefix + project.Slug(name)
	id := base
	for n := 2; a.connByID(id) != nil; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

// addConn adds a connection the user made to a project. The user chose
// where it goes, so it is trusted: only connections that arrive in the
// file from someone else are asked about. It returns nil, having said
// why, when the project's file may not be written.
func (a *App) addConn(p *project.Project, cfg db.Config) *connection.Conn {
	if err := p.Writable(); err != nil {
		a.ShowError("Could not save "+project.File+" of "+p.Name, err.Error())
		return nil
	}
	cfg.ID = a.uniqueConnID(p, cfg.Name)
	cn := &connection.Conn{Config: cfg, Project: p}
	cn.Reset()
	a.conns = append(a.conns, cn)
	a.trust(cn)
	a.saveProject(p)
	return cn
}

// ScanQueries lists the .sql files of the project, every few seconds.
func (a *App) ScanQueries(p *project.Project, now bool) {
	if p.Err != "" || !now && time.Since(p.Scanned) < 3*time.Second {
		return
	}
	p.Scanned = time.Now()
	root := p.Queries
	a.Background(func() func() {
		var files []string
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".sql") {
				rel, _ := filepath.Rel(root, path)
				files = append(files, filepath.ToSlash(rel))
			}
			return nil
		})
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		sort.Strings(files)
		return func() {
			if pl := a.palette; pl != nil && !slices.Equal(p.Files, files) {
				// Files added since the palette opened are offered too.
				defer func() { pl.items, pl.shownQ = a.paletteItems(pl.tables), "\x00" }()
			}
			p.Files = files
			p.ScanErr = ""
			if err != nil {
				p.ScanErr = err.Error()
			}
		}
	})
}

// openQueryFile opens a .sql file of a project in an editor of the
// connection its header names, or of the active one of the project; then,
// when set, goes on with the editor.
func (a *App) openQueryFile(p *project.Project, rel string, then func(*query.Tab)) {
	path := filepath.Join(p.Queries, filepath.FromSlash(rel))
	if t, ok := a.findTab(func(t widgets.Tab) bool { q, ok := t.(*query.Tab); return ok && q.Path == path }); ok {
		if then != nil {
			then(t.(*query.Tab))
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		a.ShowError("Could not open "+rel, err.Error())
		return
	}
	text := string(data)
	open := func(cn *connection.Conn) {
		q := a.openFileTab(cn, "", path, text)
		if then != nil {
			then(q)
		}
	}
	cn := a.activeConn()
	if cn != nil && cn.Project != p {
		cn = nil
	}
	id := project.HeaderConnection(text)
	if named := a.connByID(p.Prefix + id); id != "" && named != nil && named.Config.Engine.IsSQL() {
		a.Connect(named, func() { open(named) })
		return
	}
	if cn != nil && !cn.Config.Engine.IsSQL() {
		cn = nil
	}
	if id != "" {
		// The file names a connection the project lacks. Its editor runs
		// nothing until the line names one of the project's, so it opens on
		// any of them, unconnected, for the line to be fixed.
		if cn == nil {
			cn = a.firstSQLConn(p)
		}
		if cn == nil {
			a.ShowError("No connection "+id, rel+" names the connection "+id+", which "+project.File+" of "+p.Name+" does not have. Add a connection to "+p.Name+" to open it.")
			return
		}
		open(cn)
		return
	}
	if cn == nil {
		// Never a guess, such as the project's first connection: it may be
		// production.
		a.ShowError("Which connection?", "Add a line such as\n\n-- connection: "+a.firstProjectSlug(p)+"\n\nat the top of "+rel+", or choose a connection of "+p.Name+" in the sidebar first.")
		return
	}
	a.Connect(cn, func() { open(cn) })
}

// OpenQueryFile opens a query file of a project at a rune offset.
func (a *App) OpenQueryFile(path string, at int) {
	for _, p := range a.projects {
		if rel, err := filepath.Rel(p.Queries, path); err == nil && !strings.HasPrefix(rel, "..") {
			a.openQueryFile(p, filepath.ToSlash(rel), func(q *query.Tab) { q.GoTo(at) })
			return
		}
	}
	a.ShowError("Could not open "+filepath.Base(path), "It is in no project's queries folder.")
}

// firstSQLConn is the first connection of a project an editor can use,
// nil for none.
func (a *App) firstSQLConn(p *project.Project) *connection.Conn {
	for _, cn := range a.projectConns(p) {
		if cn.Config.Engine.IsSQL() {
			return cn
		}
	}
	return nil
}

// SwitchConnection reopens an editor's file on another connection of its
// project, in the editor's place, once the user agrees to what closing
// the editor loses. Running connects it.
func (a *App) SwitchConnection(q *query.Tab, id string) {
	cn := a.connByID(q.Conn.Project.Prefix + id)
	if cn == nil || !cn.Config.Engine.IsSQL() {
		return
	}
	a.endTab(q, "Switch "+q.Title()+" to "+cn.Config.Name+"?", func(w *window, at int) {
		// Closing saved the editor's text, unless the user chose to lose
		// it to the file on disk: the file is what reopens, or the text
		// of an editor whose file was never written.
		text := q.Editor.Text
		if q.Path != "" {
			switch data, err := os.ReadFile(q.Path); {
			case err == nil:
				text = string(data)
			case !errors.Is(err, os.ErrNotExist):
				a.ShowError("Could not open "+q.Name, err.Error())
				return
			}
		}
		nq := query.New(a, cn, "", q.Name, text)
		nq.Path, nq.Saved = q.Path, text
		w.tabs = slices.Insert(w.tabs, at, widgets.Tab(nq))
		w.active, a.window = at, w
	})
}

// openFileTab opens an editor on a file whose text was just read or
// written.
func (a *App) openFileTab(cn *connection.Conn, database, path, text string) *query.Tab {
	q := query.New(a, cn, database, filepath.Base(path), text)
	q.Path, q.Saved = path, text
	a.AddTab(q)
	return q
}

func (a *App) firstProjectSlug(p *project.Project) string {
	for _, cn := range a.projectConns(p) {
		return strings.TrimPrefix(cn.Config.ID, p.Prefix)
	}
	return "my-database"
}

// newQueryFile opens an editor on a new file of the connection's project,
// query-N.sql, holding text after the connection's header; then, when
// set, goes on with the editor.
func (a *App) newQueryFile(cn *connection.Conn, database, text string, then func(*query.Tab)) {
	p := cn.Project
	a.Connect(cn, func() {
		if err := os.MkdirAll(p.Queries, 0o755); err != nil {
			a.ShowError("Could not create the queries folder", err.Error())
			return
		}
		content := connection.HeaderLine(cn) + text
		var path string
		for n := 1; ; n++ {
			path = filepath.Join(p.Queries, fmt.Sprintf("query-%d.sql", n))
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			if err == nil {
				_, err = f.WriteString(content)
				err = errors.Join(err, f.Close())
			}
			if err != nil {
				a.ShowError("Could not create the query file", err.Error())
				return
			}
			break
		}
		a.ScanQueries(p, true)
		q := a.openFileTab(cn, database, path, content)
		q.Created = content
		if then != nil {
			then(q)
		}
	})
}

// addExistingProject asks for a folder to list as a project.
func (a *App) addExistingProject() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Add Existing Folder", Directory: true,
			Message: "A folder, such as a Git repository. Its " + project.File + " is created if it has none."})
		if err != nil || len(paths) == 0 {
			return
		}
		a.Post(func() {
			if _, err := a.addProject(paths[0]); err != nil {
				a.ShowError("Could not add the project", err.Error())
			}
		})
	}()
}

// newProjectForm asks for a new project's name and the folder to make it
// in.
type newProjectForm struct {
	open   bool
	name   string
	parent string
	err    string
	then   func(*project.Project) // what asked for a project, to go on with it
}

func defaultProjectsFolder() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "dgopher-projects")
}

func (a *App) openNewProject(then func(*project.Project)) {
	a.newProject = &newProjectForm{open: true, parent: defaultProjectsFolder(), then: then}
}

// createProject makes the folder of a new project and lists it.
func (a *App) createProject(f *newProjectForm) {
	name := strings.TrimSpace(f.name)
	switch {
	case name == "" || name == "." || name == "..":
		f.err = "A name is required."
		return
	case strings.ContainsAny(name, `/\:*?"<>|`):
		f.err = `A name may not hold / \ : * ? " < > or |.`
		return
	case strings.TrimSpace(f.parent) == "":
		f.err = "Choose where to create it."
		return
	}
	dir := filepath.Join(db.ExpandPath(strings.TrimSpace(f.parent)), name)
	if _, err := os.Stat(dir); err == nil {
		f.err = dir + " exists already: use Add Existing Folder to list it."
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "queries"), 0o755); err != nil {
		f.err = err.Error()
		return
	}
	p, err := a.addProject(dir)
	if err != nil {
		f.err = err.Error()
		return
	}
	f.open = false
	a.toast = &pendingToast{text: "Created " + dir}
	if f.then != nil {
		f.then(p)
	}
}

func (a *App) newProjectView(c *ui.Context) {
	f := a.newProject
	pal := widgets.PaletteOf(c)
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(520).Gap(12).Children(func() {
			ui.Text(c, "New Project").FontSize(15).Bold()
			ui.Text(c, "A project is a folder to keep in Git: its "+project.File+" lists connections without passwords, and its queries are .sql files.").FontSize(12).TextColor(pal.Muted)
			var submit bool
			ui.Form(c, func() {
				ui.Field(c, "Name", func() {
					submit = ui.TextInput(c, &f.name).AutoFocus().Placeholder("billing").Submitted()
				})
				ui.Field(c, "Create in", func() {
					ui.Row(c).Gap(6).Children(func() {
						ui.TextInput(c, &f.parent).Grow(1).Font(widgets.MonoFont).FontSize(12.5)
						if ui.Button(c, "Choose…").Clicked() {
							go func() {
								paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Create the Project In", Directory: true, CreateDirectories: true})
								if err != nil || len(paths) == 0 {
									return
								}
								a.Post(func() { f.parent = paths[0] })
							}()
						}
					})
				}).Description(filepath.Join(db.ExpandPath(strings.TrimSpace(f.parent)), strings.TrimSpace(f.name)))
			})
			if f.err != "" {
				ui.Text(c, f.err).FontSize(12).TextColor(c.Theme().Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if widgets.Activated(c, ui.PrimaryButton(c, "Create Project")) || submit {
					a.createProject(f)
				}
			})
		})
	})
	if !f.open {
		a.newProject = nil
	}
}

// renameForm asks for a query file's new name.
// renameForm asks a new name for something: a query file, a table, a
// column.
type renameForm struct {
	open  bool
	title string
	name  string
	err   string
	// action is the button's, "" for Rename: the form names what is new
	// too.
	action string
	// rename renames it to name, or says why not, which keeps the form
	// open.
	rename func(name string) error
}

func (a *App) askRename(p *project.Project, path string) {
	a.renaming = &renameForm{open: true, title: "Rename " + filepath.Base(path), name: filepath.Base(path),
		rename: func(name string) error { return a.renameQueryFile(p, path, name) }}
}

// renameQueryFile renames a query file and the editors open on it.
func (a *App) renameQueryFile(p *project.Project, path, name string) error {
	name = strings.TrimSpace(name)
	if !strings.EqualFold(filepath.Ext(name), ".sql") {
		name += ".sql"
	}
	if name == ".sql" || strings.ContainsAny(name, `/\:*?"<>|`) {
		return errors.New(`A name may not be empty, nor hold / \ : * ? " < > or |.`)
	}
	to := filepath.Join(filepath.Dir(path), name)
	if to == path {
		return nil
	}
	if _, err := os.Lstat(to); err == nil {
		return errors.New(name + " exists already.")
	}
	for _, t := range a.everyTab() {
		if q, ok := t.(*query.Tab); ok && q.Path == path {
			q.Flush(false)
		}
	}
	if err := os.Rename(path, to); err != nil {
		return err
	}
	for _, t := range a.everyTab() {
		if q, ok := t.(*query.Tab); ok && q.Path == path {
			q.Path, q.Name, q.Created = to, name, ""
		}
	}
	a.ScanQueries(p, true)
	return nil
}

func (a *App) renameView(c *ui.Context) {
	f := a.renaming
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(420).Gap(10).Children(func() {
			ui.Text(c, f.title).FontSize(15).Bold()
			submit := ui.TextInput(c, &f.name).AutoFocus().Label("Name").Font(widgets.MonoFont).Submitted()
			if f.err != "" {
				ui.Text(c, f.err).FontSize(12).TextColor(c.Theme().Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				action := f.action
				if action == "" {
					action = "Rename"
				}
				if widgets.Activated(c, ui.PrimaryButton(c, action)) || submit {
					if err := f.rename(f.name); err != nil {
						f.err = err.Error()
					} else {
						f.open = false
					}
				}
			})
		})
	})
	if !f.open {
		a.renaming = nil
	}
}

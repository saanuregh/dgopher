package app

import (
	"cmp"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/dbeaver"
	"dgopher/internal/lists"
	"dgopher/internal/project"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// dbeaverImport is the dialog importing the connections DBeaver saved
// into a project.
type dbeaverImport struct {
	open       bool
	dir        string   // the DBeaver folder read
	dirs       []string // the folders offered: those found, and one chosen
	project    *project.Project
	projectSel string // its label in the project choice
	entries    []dbeaver.Entry
	from       []string // each entry's DBeaver project
	// fromSeveral is whether the entries come from more than one DBeaver
	// project, so each row names its own.
	fromSeveral bool
	chosen      []bool
	// envs are the environments chosen here, by label, for the remote
	// servers DBeaver left on its default type: "" until chosen.
	envs []string
	// passwords is the user's consent to keep DBeaver's saved passwords
	// in the keychain; off until given.
	passwords bool
	loading   bool
	reads     int // only the latest read's result is kept
	err       string
	dismiss   widgets.DismissGuard
}

// openDBeaverImport opens the dialog on DBeaver's own folder, into p, or
// the current project when p is nil.
func (a *App) openDBeaverImport(p *project.Project) {
	if p == nil || p.Err != "" {
		p = a.currentProject()
	}
	if p == nil || p.Err != "" {
		if ps := a.usableProjects(); len(ps) > 0 {
			p = ps[0]
		}
	}
	if p == nil {
		a.toast("Add a project first: connections are kept in one.", "", nil)
		return
	}
	d := &dbeaverImport{open: true, project: p, projectSel: a.projectLabel(p), dirs: dbeaver.DataDirs()}
	a.dbeaverImport = d
	if len(d.dirs) > 0 {
		a.readDBeaver(d, d.dirs[0])
	} else {
		d.err = "DBeaver's folder was not found: choose it."
	}
}

// readDBeaver reads the connections of every DBeaver project under dir.
func (a *App) readDBeaver(d *dbeaverImport, dir string) {
	d.dir, d.loading, d.err = dir, true, ""
	if !slices.Contains(d.dirs, dir) {
		d.dirs = append(d.dirs, dir)
	}
	d.reads++
	n := d.reads
	a.Background(func() func() {
		var entries []dbeaver.Entry
		var from, problems []string
		projects, err := dbeaver.Projects(dir)
		for _, p := range projects {
			es, err := dbeaver.Read(p)
			if err != nil {
				problems = append(problems, p.Name+": "+err.Error())
				continue
			}
			entries = append(entries, es...)
			for range es {
				from = append(from, p.Name)
			}
		}
		return func() {
			if d.reads != n || a.dbeaverImport != d {
				return
			}
			d.loading = false
			d.entries, d.from = entries, from
			d.envs = make([]string, len(entries))
			d.fromSeveral = len(slices.Compact(slices.Clone(from))) > 1
			switch {
			case err != nil:
				d.err = err.Error()
			case len(projects) == 0:
				d.err = "No DBeaver connections in " + dir + "."
			default:
				d.err = strings.Join(problems, "\n")
			}
			a.chooseDBeaverDefaults(d)
		}
	})
}

// chooseDBeaverDefaults ticks every entry that can be imported, is not
// in the project already, and needs no environment chosen.
func (a *App) chooseDBeaverDefaults(d *dbeaverImport) {
	d.chosen = make([]bool, len(d.entries))
	for i := range d.entries {
		d.chosen[i] = d.entries[i].Skip == "" && !d.needsEnv(i)
	}
	a.untickDBeaverDuplicates(d)
}

// untickDBeaverDuplicates unticks the entries the project has already,
// keeping the other ticks, as when the project changes.
func (a *App) untickDBeaverDuplicates(d *dbeaverImport) {
	for i := range d.entries {
		if a.dbeaverDuplicate(d, i) != "" {
			d.chosen[i] = false
		}
	}
}

// needsEnv reports whether the entry is a server elsewhere whose
// environment DBeaver left at its default, and none is chosen here yet:
// as the connection form, import does not take Development, which asks
// nothing before a write, for one.
func (d *dbeaverImport) needsEnv(i int) bool {
	return d.asksEnv(i) && d.envs[i] == ""
}

// asksEnv reports whether the entry's environment is chosen here: a
// server elsewhere left on DBeaver's default type, which can be imported.
func (d *dbeaverImport) asksEnv(i int) bool {
	e := &d.entries[i]
	return e.Skip == "" && !e.EnvChosen && remote(&e.Config)
}

// chooseDBeaverEnv sets an entry's environment from its label, with the
// environment's own commit default, and ticks it.
func (d *dbeaverImport) chooseDBeaverEnv(i int, label string) {
	for _, env := range db.Environments() {
		if env.Label() == label {
			d.envs[i] = label
			d.entries[i].Config.Env, d.entries[i].Config.Commit = env, db.CommitDefault
			d.chosen[i] = true
		}
	}
}

// dbeaverDuplicate says which connection of the project the entry is,
// "" when none.
func (a *App) dbeaverDuplicate(d *dbeaverImport, i int) string {
	for _, cn := range a.projectConns(d.project) {
		if sameTarget(&cn.Config, &d.entries[i].Config) {
			return cn.Config.Name
		}
	}
	return ""
}

// sameTarget reports whether two connections reach the same database as
// the same user, through the same SSH server: the same file, for a file
// engine.
func sameTarget(x, y *db.Config) bool {
	if x.Engine != y.Engine {
		return false
	}
	if x.Engine.IsFile() {
		return filepath.Clean(x.Database) == filepath.Clean(y.Database)
	}
	return strings.EqualFold(destination(*x), destination(*y)) && x.Database == y.Database && x.User == y.User
}

// chooseDBeaverFolder asks for a DBeaver folder, off the UI thread.
func (a *App) chooseDBeaverFolder(d *dbeaverImport) {
	go func() {
		defer dataview.RecoverBackground(a.Post, a.ShowReport, nil)
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Choose DBeaver's Folder", Directory: true})
		a.Post(func() {
			if a.dbeaverImport != d {
				return
			}
			if err != nil {
				d.err = err.Error()
				return
			}
			if len(paths) > 0 {
				a.readDBeaver(d, paths[0])
			}
		})
	}()
}

func (d *dbeaverImport) count() int {
	n := 0
	for i, on := range d.chosen {
		if on && d.entries[i].Skip == "" {
			n++
		}
	}
	return n
}

// withSecrets reports whether a chosen entry has a saved password.
func (d *dbeaverImport) withSecrets() bool {
	for i, on := range d.chosen {
		if on && d.entries[i].HasSecrets() {
			return true
		}
	}
	return false
}

func (a *App) dbeaverImportView(c *ui.Context) {
	d := a.dbeaverImport
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	_, winH := c.Size()
	keychain := a.st.Secrets().Available()
	dlg := widgets.Modal(c, &d.open, func() {
		ui.Column(c).Width(760).MaxHeight(winH - 80).Gap(12).Children(func() {
			ui.Text(c, "Import from DBeaver").FontSize(widgets.TextTitle).Bold()
			ui.Form(c, func() {
				ui.Field(c, "DBeaver folder", func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						dir := d.dir
						if ui.Select(c, &dir, d.dirs).Label("DBeaver folder").Grow(1).Shrink(1).Changed() {
							a.readDBeaver(d, dir)
						}
						if ui.Button(c, "Choose…").Tooltip("A DBeaverData, workspace6 or project folder").Clicked() {
							a.chooseDBeaverFolder(d)
						}
					})
				})
				ui.Field(c, "Into project", func() {
					if ui.Select(c, &d.projectSel, a.projectLabels()).Label("Into project").Changed() {
						if p := a.projectByLabel(d.projectSel); p != nil {
							d.project = p
							a.untickDBeaverDuplicates(d)
						}
					}
				})
			})
			switch {
			case d.loading:
				ui.Text(c, "Reading DBeaver's connections…").TextColor(pal.Muted)
			case len(d.entries) > 0:
				ui.Scroll(c).MaxHeight(360).Border(1, th.Border).Radius(widgets.RadiusMedium).Children(func() {
					ui.Column(c).Padding(4, 0).Children(func() {
						dbeaverHeader(c)
						for i := range d.entries {
							a.dbeaverRow(c, d, i)
						}
					})
				})
			}
			if d.err != "" {
				ui.Text(c, d.err).TextColor(th.Danger).Selectable()
			}
			tip := "DBeaver keeps them encrypted with a key written in its source. They go to the system keychain, never to " + project.File + "."
			switch {
			case !keychain:
				tip = "No system keychain is available: each connection asks for its password."
			case !d.withSecrets():
				tip = "DBeaver saved no password for the connections chosen."
			}
			ui.Checkbox(c, &d.passwords, "Import saved passwords into the keychain").Disabled(!keychain || !d.withSecrets()).Tooltip(tip)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					d.open = false
				}
				n := d.count()
				b := ui.PrimaryButton(c.Key("import"), fmt.Sprintf("Import %d", n)).Disabled(n == 0 || d.loading)
				if n == 0 {
					b.Tooltip("Choose the connections to import")
				}
				if widgets.Activated(c, b) {
					a.importDBeaver(d)
				}
			})
		})
	})
	// Only the consent counts as typed: the ticks start as the read left them.
	d.dismiss.Keep(c, dlg, &d.open, d.passwords, false)
	if !d.open {
		a.dbeaverImport = nil
	}
}

// dbeaverHeader names dbeaverRow's columns, in the same widths.
func dbeaverHeader(c *ui.Context) {
	muted := widgets.PaletteOf(c).Muted
	label := func(s string, width float32) {
		ui.Text(c, s).FontSize(widgets.TextMeta).TextColor(muted).SingleLine().Width(width)
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(3, 10).Children(func() {
		// The checkbox and the colour bar.
		ui.Box(c).Width(16)
		ui.Box(c).Width(4)
		ui.Text(c, "Name").FontSize(widgets.TextMeta).TextColor(muted).SingleLine().Grow(1).Shrink(1)
		label("Engine", 90)
		label("Where", 200)
		label("Environment", 140)
		label("Status", 100)
	})
}

// dbeaverRow is one connection: its choice, name, engine, where it goes,
// and why it is not chosen, with the detail on hover.
func (a *App) dbeaverRow(c *ui.Context, d *dbeaverImport, i int) {
	e := &d.entries[i]
	pal := widgets.PaletteOf(c)
	dup := ""
	if e.Skip == "" {
		dup = a.dbeaverDuplicate(d, i)
	}
	c = c.Key("dbeaver-" + d.from[i] + "/" + e.ID)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(3, 10).Children(func() {
		needsEnv := d.needsEnv(i)
		cb := ui.Checkbox(c, &d.chosen[i], "").Label("Import " + e.Config.Name).Disabled(e.Skip != "" || needsEnv)
		if needsEnv {
			cb.Tooltip("Choose its environment first")
		}
		ui.Box(c).Size(4, 16).Radius(2).Background(widgets.EnvColor(&e.Config))
		name := e.Config.Name
		if e.Folder != "" {
			name = e.Folder + "/" + name
		}
		if d.fromSeveral {
			name = d.from[i] + ": " + name
		}
		ui.Text(c, name).SingleLine().Grow(1).Shrink(1)
		if e.Config.Engine != "" {
			ui.Text(c, e.Config.Engine.Label()).FontSize(widgets.TextSmall).TextColor(widgets.EngineColor(e.Config.Engine)).SingleLine().Width(90)
		} else {
			ui.Text(c, e.Driver).FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine().Width(90)
		}
		ui.Text(c, dbeaverWhere(&e.Config)).FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine().Width(200)
		if d.asksEnv(i) {
			// DBeaver's default type says nothing of the server: chosen here.
			env := cmp.Or(d.envs[i], chooseEnv)
			if ui.Select(c.Key("env"), &env, dbeaverEnvLabels).Label("Environment of "+e.Config.Name).
				FontSize(widgets.TextSmall).Width(140).Height(22).Padding(0, 8).Changed() {
				d.chooseDBeaverEnv(i, env)
			}
		} else {
			ui.Text(c, e.Config.Env.Label()).FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine().Width(140)
		}
		switch {
		case e.Skip != "":
			ui.Text(c, "Not supported").FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine().Width(100).Tooltip(e.Skip)
		case dup != "":
			ui.Text(c, "Already here").FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine().Width(100).Tooltip("The project has it as " + dup)
		case len(e.Notes) > 0:
			ui.Text(c, "Partly").FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine().Width(100).Tooltip(strings.Join(e.Notes, "\n"))
		default:
			ui.Box(c).Width(100)
		}
	})
}

// dbeaverFolder is the project's folder a connection in DBeaver's folder
// path goes in: each name the project's folder of that name whatever its
// case, as folders that differ by case alone are not kept apart, and the
// path stopping before a name that cannot be a folder's.
func dbeaverFolder(p *project.Project, path string) string {
	folder := ""
	for name := range strings.SplitSeq(path, "/") {
		name, err := project.CleanFolderName(name)
		if err != nil {
			break
		}
		next := folderPath(folder, name)
		if i := slices.IndexFunc(p.Folders, func(f string) bool { return strings.EqualFold(f, next) }); i >= 0 {
			next = p.Folders[i]
		}
		folder = next
	}
	return folder
}

// chooseEnv is the environment select's label until one is chosen.
const chooseEnv = "Choose…"

// dbeaverEnvLabels are the choices of a row's environment.
var dbeaverEnvLabels = append([]string{chooseEnv}, lists.Map(db.Environments(), db.Environment.Label)...)

// dbeaverWhere is where a connection goes, as a row shows it.
func dbeaverWhere(cfg *db.Config) string {
	switch {
	case cfg.Engine.IsFile():
		return filepath.Base(cfg.Database)
	case cfg.Host == "":
		return ""
	}
	return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
}

// importDBeaver adds the chosen connections to the project, with their
// passwords in the keychain when the user agreed.
func (a *App) importDBeaver(d *dbeaverImport) {
	if err := d.project.Writable(); err != nil {
		d.err = "Could not save " + project.File + ": " + err.Error()
		return
	}
	keychain := a.st.Secrets().Available()
	added := 0
	var retype []string
	for i := range d.entries {
		e := &d.entries[i]
		if !d.chosen[i] || e.Skip != "" {
			continue
		}
		cfg, secret := e.Config, e.Config
		cfg.Password, cfg.SSH.Password, cfg.SSH.KeyPassphrase, cfg.Redis.SentinelPassword, cfg.Proxy.Password = "", "", "", "", ""
		keep := d.passwords && keychain
		if secret.Password != "" && !keep && !cfg.Engine.IsFile() {
			// DBeaver had it: asked for, rather than failing to log in
			// without one.
			cfg.AskPassword = true
		}
		if !keep && (secret.SSH.Password != "" || secret.SSH.KeyPassphrase != "" || secret.Proxy.Password != "") {
			retype = append(retype, cfg.Name) // only the database's password is asked for
		}
		if keep {
			// Kept first, under the ID addConn gives, as the form does: a
			// connection is never saved without its passwords.
			secret.ID = a.uniqueConnID(d.project, cfg.Name)
			if err := a.saveSecrets(&secret, true, nil); err != nil {
				d.err = "Could not save the passwords of " + cfg.Name + " in the keychain: " + err.Error()
				return
			}
		}
		cfg.Folder = dbeaverFolder(d.project, e.Folder)
		cn := a.addConn(d.project, cfg)
		if cn == nil {
			return // it said why
		}
		a.expandFolder(d.project, cfg.Folder)
		d.chosen[i] = false
		a.Record(&cn.Config, audit.Event{Kind: audit.KindConfig, Detail: "connection added from DBeaver: " + cn.Config.Env.Label() + ", " + connectionSummary(&cn.Config)})
		added++
	}
	d.open = false
	noun := "connections"
	if added == 1 {
		noun = "connection"
	}
	text := fmt.Sprintf("Imported %d %s from DBeaver into %s.", added, noun, d.project.Name)
	if len(retype) > 0 {
		text += " Type the SSH or proxy password of " + strings.Join(retype, ", ") + " in its settings."
	}
	a.toast(text, "", nil)
}

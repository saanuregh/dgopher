package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/datamodel"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/store"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/modelview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"
)

// connIDProblem says why id cannot be cn's ID, "" when it can: an ID is
// written as a generated one is, and no other connection of the project
// has it.
func (a *App) connIDProblem(cn *connection.Conn, id string) string {
	switch {
	case id == "":
		return "An ID is needed: query files name the connection by it."
	case project.Slug(id) != id:
		return "An ID has lower-case letters, digits and dashes only, as " + project.Slug(id) + "."
	}
	if other := a.connByID(cn.Project.Prefix + id); other != nil && other != cn {
		return other.Config.Name + " has that ID."
	}
	return ""
}

// idUses counts the files of a project that name a connection by id:
// query files, dashboards and data models.
func idUses(p *project.Project, id string) (queries, dashboards, models int) {
	walkQueries(p, func(_, text string) {
		if project.HeaderConnection(text) == id {
			queries++
		}
	})
	if paths, err := dashboard.List(p); err == nil {
		for _, path := range paths {
			if d, err := dashboard.Load(path); err == nil && slices.ContainsFunc(d.Panels, func(x dashboard.Panel) bool { return x.Connection == id }) {
				dashboards++
			}
		}
	}
	if paths, err := datamodel.List(p); err == nil {
		for _, path := range paths {
			if m, err := datamodel.Load(path); err == nil && m.Source != nil && m.Source.Connection == id {
				models++
			}
		}
	}
	return queries, dashboards, models
}

// usesList names counts of files as a sentence does: "2 query files and a
// dashboard"; "" for none.
func usesList(first string, queries, dashboards, models int) string {
	var files []string
	if first != "" {
		files = append(files, first)
	}
	for _, n := range []struct {
		n    int
		what string
	}{{queries, "query file"}, {dashboards, "dashboard"}, {models, "data model"}} {
		if n.n > 0 {
			files = append(files, widgets.Count(n.n, n.what))
		}
	}
	switch n := len(files); n {
	case 0:
		return ""
	case 1:
		return files[0]
	case 2:
		return files[0] + " and " + files[1]
	default:
		return strings.Join(files[:n-1], ", ") + ", and " + files[n-1]
	}
}

// askRenameConn asks before cn's ID changes from old to id, saying what it
// rewrites, then renames it and goes on with then, after the question
// closed: then may ask one of its own. A rename that did not happen shows
// why, and then does not run.
func (a *App) askRenameConn(cn *connection.Conn, old, id string, then func()) {
	p := cn.Project
	q, d, m := idUses(p, old)
	list := usesList(project.File, q, d, m)
	reason := "This rewrites " + list + " to name " + id + ". Teammates are asked whether to trust it again once they pull."
	if a.hasSecrets(&cn.Config) {
		reason = "This rewrites " + list + " to name " + id + ", and moves its saved password with it. " +
			"Teammates are asked for its password and whether to trust it again once they pull."
	}
	q, d, m = idUses(p, id)
	if named := usesList("", q, d, m); named != "" {
		// Left from a connection deleted, as after a pull: they run here.
		reason += " " + strings.ToUpper(named[:1]) + named[1:] + " already name " + id + ", and will run on this connection."
	}
	reason += " Its query history and audit log keep " + old + "."
	name := cn.Config.Name
	a.closing = &closeRequest{open: true, title: "Change the ID of " + name + " to " + id + "?", reason: reason, action: "Change ID", onClose: func() {
		err := a.renameConn(cn, id)
		if cn.Config.ID != p.Prefix+id {
			a.ShowError("Could not change the ID of "+name, err.Error())
			return
		}
		if err != nil {
			a.ShowError("Changed the ID of "+name+" to "+id+", not everywhere", err.Error())
		}
		a.Post(then)
	}}
}

// renameConn gives cn the ID id, without the project's prefix, and moves
// what names it by its old one: dgopher.json and its tables' settings,
// the query files, open editors (their unsaved text kept), dashboards and
// data models, the keychain's secrets and the trust the user gave it. The
// query history and the audit log keep the old ID, as written.
//
// Nothing changes when the secrets cannot be copied or dgopher.json
// cannot be written, as after a pull changed it: what was done is undone.
// Files that cannot be rewritten after that are reported, the rest done.
func (a *App) renameConn(cn *connection.Conn, id string) error {
	p := cn.Project
	old := strings.TrimPrefix(cn.Config.ID, p.Prefix)
	if id == old {
		return nil
	}
	if why := a.connIDProblem(cn, id); why != "" {
		return errors.New(why)
	}
	// Their files are rewritten and read again, which would lose what they
	// did not save: a dashboard saves its edited titles, as closing does; a
	// model's table changes wait for the user.
	for _, t := range a.everyTab() {
		switch t := t.(type) {
		case *dashboard.Tab:
			if inProject(p, t.Path) && !t.SaveEdits() {
				return fmt.Errorf("could not save the titles edited in %s: save or close it first", t.Title())
			}
		case *modelview.Tab:
			if inProject(p, t.Path) && t.CloseReason() != "" {
				return fmt.Errorf("save or close %s first: changing the ID rewrites its file", t.Title())
			}
		}
	}

	was := cn.Config
	renamed := was
	renamed.ID = p.Prefix + id
	copied, err := a.copySecrets(&was, &renamed)
	if err != nil {
		return fmt.Errorf("could not copy its secrets in the keychain: %w", err)
	}
	cn.Config.ID = renamed.ID
	localErr := dataview.RenameConnection(p, old, id)
	if err := p.Save(a.ProjectConfigs(p)); err != nil {
		cn.Config.ID = was.ID
		dataview.RenameConnection(p, id, old)
		for _, k := range copied {
			a.st.Secrets().Delete(k)
		}
		return fmt.Errorf("could not save %s: %w", project.File, err)
	}
	a.deleteSecrets(&was)
	a.moveTrust(&was, &renamed, p.Dir)
	relabel(cn)

	errs := []error{localErr}
	errs = append(errs, a.renameInQueries(p, old, id)...)
	errs = append(errs, a.renameInDashboards(p, old, id)...)
	a.saveWorkspace(true)
	a.ScanQueries(p, true)
	a.Record(&cn.Config, audit.Event{Kind: audit.KindConfig, Detail: "connection ID changed from " + old + " to " + id})
	return errors.Join(errs...)
}

// hasSecrets reports whether the keychain keeps a secret of cfg, under
// its key or its key of before.
func (a *App) hasSecrets(cfg *db.Config) bool {
	sec := a.st.Secrets()
	if !sec.Available() {
		return false
	}
	return slices.ContainsFunc(secretNames, func(what string) bool {
		_, err := sec.Get(secretKey(cfg, what))
		_, errOld := sec.Get(legacySecretKey(cfg, what))
		return err == nil || errOld == nil
	})
}

// copySecrets copies the keychain's secrets of from to the keys of to,
// each to its like: one kept under the key of before (legacySecretKey)
// goes under to's key of before, and so still asks to be typed once. It
// returns the keys it wrote; those are removed again when one cannot be
// copied.
func (a *App) copySecrets(from, to *db.Config) ([]string, error) {
	sec := a.st.Secrets()
	if !sec.Available() {
		return nil, nil
	}
	var wrote []string
	for _, what := range secretNames {
		keys := [][2]string{{secretKey(from, what), secretKey(to, what)}}
		if old := legacySecretKey(from, what); old != keys[0][0] {
			keys = append(keys, [2]string{old, legacySecretKey(to, what)})
		}
		for _, k := range keys {
			v, err := sec.Get(k[0])
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err == nil {
				err = sec.Set(k[1], v)
			}
			if err != nil {
				for _, w := range wrote {
					sec.Delete(w)
				}
				return nil, err
			}
			wrote = append(wrote, k[1])
		}
	}
	return wrote, nil
}

// moveTrust carries the trust the user gave a connection over to its new
// ID: trusted, it stays trusted; asking first, it still asks.
func (a *App) moveTrust(was, renamed *db.Config, projectDir string) {
	changed := false
	if i := slices.Index(a.settings.TrustedShared, sharedFingerprint(was, projectDir)); i >= 0 {
		a.settings.TrustedShared[i] = sharedFingerprint(renamed, projectDir)
		changed = true
	}
	if d, ok := a.settings.TrustedDetails[trustKeyOf(projectDir, was.ID)]; ok {
		delete(a.settings.TrustedDetails, trustKeyOf(projectDir, was.ID))
		a.settings.TrustedDetails[trustKeyOf(projectDir, renamed.ID)] = d
		changed = true
	}
	if changed {
		a.SaveSettings()
	}
}

// walkQueries calls fn with each query file of a project and its text, as
// the sidebar lists them (ScanQueries): a link to a file elsewhere is not
// the project's to rewrite, and is left.
func walkQueries(p *project.Project, fn func(path, text string)) []error {
	var errs []error
	filepath.WalkDir(p.Queries, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		case d.IsDir() && strings.HasPrefix(d.Name(), ".") && path != p.Queries:
			return filepath.SkipDir
		case d.Type().IsRegular() && strings.EqualFold(filepath.Ext(path), ".sql"):
			if data, err := os.ReadFile(path); err != nil {
				errs = append(errs, err)
			} else {
				fn(path, string(data))
			}
		}
		return nil
	})
	return errs
}

func inProject(p *project.Project, path string) bool {
	return strings.HasPrefix(path, p.Dir+string(filepath.Separator))
}

// renameInQueries points the query files whose header names old at new,
// and the open editors of those files: their unsaved text keeps, and
// their caret stays where it was in it.
func (a *App) renameInQueries(p *project.Project, old, new string) []error {
	var errs []error
	rewritten := map[string]bool{}
	errs = append(errs, walkQueries(p, func(path, text string) {
		if renamed, ok := project.RenameHeaderConnection(text, old, new); ok {
			if err := os.WriteFile(path, []byte(renamed), 0o644); err != nil {
				errs = append(errs, err)
				return
			}
			rewritten[path] = true
		}
	})...)
	for _, t := range a.everyTab() {
		q, ok := t.(*query.Tab)
		if !ok || !rewritten[q.Path] {
			continue
		}
		text, _ := project.RenameHeaderConnection(q.Editor.Text, old, new)
		shift := caretShift(q.Editor.Text, text)
		q.Editor.PendingSel = &[2]int{shift(q.Editor.SelStart), shift(q.Editor.SelEnd)}
		q.Editor.Text = text
		q.Saved, _ = project.RenameHeaderConnection(q.Saved, old, new)
		q.Created, _ = project.RenameHeaderConnection(q.Created, old, new)
	}
	return errs
}

// caretShift returns where a rune offset of before goes in after, which
// replaced one run of it: offsets past the run move by the change in
// length.
func caretShift(before, after string) func(int) int {
	b, c := []rune(before), []rune(after)
	at := 0
	for at < len(b) && at < len(c) && b[at] == c[at] {
		at++
	}
	d := len(c) - len(b)
	return func(i int) int {
		if i > at {
			return max(at, i+d)
		}
		return i
	}
}

// renameInDashboards points the dashboards' panels and the data models
// that name old at new, and reads their open tabs again; one whose file
// does not read is reported, as it keeps old.
func (a *App) renameInDashboards(p *project.Project, old, new string) []error {
	var errs []error
	rewritten := map[string]bool{}
	paths, err := dashboard.List(p)
	errs = append(errs, err)
	for _, path := range paths {
		d, was, err := dashboard.LoadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(path), err))
			continue
		}
		changed := false
		for i := range d.Panels {
			if d.Panels[i].Connection == old {
				d.Panels[i].Connection, changed = new, true
			}
		}
		if !changed {
			continue
		}
		if _, err := d.Save(path, was); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(path), err))
		} else {
			rewritten[path] = true
		}
	}
	paths, err = datamodel.List(p)
	errs = append(errs, err)
	for _, path := range paths {
		m, was, err := datamodel.LoadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(path), err))
			continue
		}
		if m.Source == nil || m.Source.Connection != old {
			continue
		}
		m.Source.Connection = new
		if _, err := m.Save(path, was); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(path), err))
		} else {
			rewritten[path] = true
		}
	}
	for _, t := range a.everyTab() {
		switch t := t.(type) {
		case *dashboard.Tab:
			if rewritten[t.Path] {
				t.Reload()
			}
		case *modelview.Tab:
			if rewritten[t.Path] {
				t.Reload()
			}
		}
	}
	return errs
}

package app

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"
)

// identityDismissedFile keeps in a project's state, by connection ID, the
// server whose mismatch notice was let go, as its engine and version: the
// same server is not told of again, another version is.
const identityDismissedFile = "engine-identity-dismissed.json"

// identityNoticeTime is how long the mismatch notice shows: a choice
// takes longer to make than an Undo.
const identityNoticeTime = 15 * time.Second

// identifiedServer is what a connection's server is, as db.Identify reads
// it; no engine when it could not tell.
type identifiedServer struct {
	engine  db.Engine
	version string
}

// identifyStore reads, off the main thread, what an open store's server
// is: nothing for a store that is not SQL, or a server that did not say.
func identifyStore(ctx context.Context, s db.Store) identifiedServer {
	d, ok := s.(*db.DB)
	if !ok {
		return identifiedServer{}
	}
	e, v, err := d.Identify(ctx)
	if err != nil {
		return identifiedServer{}
	}
	return identifiedServer{engine: e, version: v}
}

// label names the server, with its version's first two parts.
func (s identifiedServer) label() string {
	parts := strings.SplitN(s.version, ".", 3)
	if s.version == "" {
		return s.engine.Label()
	}
	return s.engine.Label() + " " + strings.Join(parts[:min(2, len(parts))], ".")
}

// mismatches reports whether the server is known to be another engine
// than set.
func (s identifiedServer) mismatches(set db.Engine) bool {
	return s.engine != "" && s.engine != set
}

// shownVersion is the version to show of a server that says read: a
// flavor's own, as identified, for a connection set to that flavor, whose
// server says its parent's version, if any.
func (s identifiedServer) shownVersion(set db.Engine, read string) string {
	if s.engine == set && set.Base() != set && s.version != "" {
		return s.version
	}
	return read
}

// key is how a dismissal keeps the server.
func (s identifiedServer) key() string { return string(s.engine) + " " + s.version }

// identifiedConn is a connection whose server identifiedServer found.
type identifiedConn struct {
	conn   *connection.Conn
	server identifiedServer
}

// offerIdentifiedEngine tells, once cn connected, that its server is
// another engine than it is set to, with a switch to that engine, unless
// the notice for this server was let go before. Nothing changes unless
// the switch is clicked, and not then if cn has since been set to another
// engine or server.
func (a *App) offerIdentifiedEngine(cn *connection.Conn, s identifiedServer) {
	if !s.mismatches(cn.Config.Engine) || a.identityDismissed(cn, s) {
		return
	}
	text := cn.Config.Name + ": this server is " + s.label() + ", but the connection is set to " + cn.Config.Engine.Label() + "."
	// Kept by the project and ID, as a notification outlives the
	// connection it was about.
	p, id, name, set, at := cn.Project, cn.Config.ID, cn.Config.Name, cn.Config.Engine, destination(cn.Config)
	use := func() {
		i := slices.IndexFunc(a.conns, func(x *connection.Conn) bool { return x.Project == p && x.Config.ID == id })
		if i < 0 || a.conns[i].Config.Engine != set || destination(a.conns[i].Config) != at {
			a.toast(name+" has changed since: connect to it again to check its server.", "", nil)
			return
		}
		a.useIdentifiedEngine(a.conns[i], s.engine)
	}
	action := "Use " + s.engine.Label()
	// Only a choice not to be asked again is kept: a toast that closed on
	// its own was perhaps never read.
	dontAsk := func() { a.dismissIdentity(cn, s) }
	a.toasts = append(a.toasts, pendingToast{id: "engine-identity " + id, text: text, action: action, run: use, life: identityNoticeTime})
	a.note(notification{title: text, actions: []widgets.ErrorAction{{Label: action, Run: use}, {Label: "Don't Ask Again", Run: dontAsk}}})
}

// identityDismissed reports whether the notice for cn's server s was let
// go before.
func (a *App) identityDismissed(cn *connection.Conn, s identifiedServer) bool {
	var kept map[string]string
	if cn.Project == nil || cn.Project.Local == nil || cn.Project.Local.LoadJSON(identityDismissedFile, &kept) != nil {
		return false
	}
	return kept[localConnID(cn)] == s.key()
}

// dismissIdentity keeps that the notice for cn's server s was let go.
func (a *App) dismissIdentity(cn *connection.Conn, s identifiedServer) {
	if cn.Project == nil || cn.Project.Local == nil {
		return
	}
	var kept map[string]string
	if cn.Project.Local.LoadJSON(identityDismissedFile, &kept) != nil || kept == nil {
		kept = map[string]string{}
	}
	kept[localConnID(cn)] = s.key()
	if err := cn.Project.Local.SaveJSON(identityDismissedFile, kept); err != nil {
		a.ShowError("Could not keep the choice", err.Error())
	}
}

// localConnID is cn's ID in its project's files and state, as
// dataview.RenameConnection keys them.
func localConnID(cn *connection.Conn) string {
	return strings.TrimPrefix(cn.Config.ID, cn.Project.Prefix)
}

// renameIdentityDismissal moves the dismissal kept for the connection
// old of p to id, as renameConn does the connection's other state.
func renameIdentityDismissal(p *project.Project, old, id string) error {
	var kept map[string]string
	if p.Local == nil || p.Local.LoadJSON(identityDismissedFile, &kept) != nil || kept[old] == "" {
		return nil
	}
	kept[id] = kept[old]
	delete(kept, old)
	return p.Local.SaveJSON(identityDismissedFile, kept)
}

// useIdentifiedEngine sets cn to the engine its server is, saved as an
// edit in the connection form is: an open connection connects again,
// once the user agrees to what that loses. A form open already is left
// as it is, and one missing a password the keychain could not give stays
// open for it.
func (a *App) useIdentifiedEngine(cn *connection.Conn, e db.Engine) {
	if cn.Config.Engine == e {
		return
	}
	if a.connForm != nil && a.connForm.open {
		a.toast("Close the connection form first, then use "+e.Label()+" again.", "", nil)
		return
	}
	connected := cn.Status == connection.StatusConnected
	a.openConnForm(cn)
	f := a.connForm
	if f == nil || f.editing != cn {
		return
	}
	setFormEngine(f, e)
	if len(f.unread) > 0 {
		f.err = "Couldn't read a saved password of " + cn.Config.Name + " from the keychain: enter it, then save."
		return
	}
	// The environment is the connection's, kept as it was.
	f.envChosen = true
	a.saveConnForm(f, connected)
}

// setFormEngine sets the form to engine e, of the same family, keeping
// where it connects: a port left to the old engine's default is written
// out, as e's may differ.
func setFormEngine(f *connForm, e db.Engine) {
	old := engineByLabel(f.engine)
	if strings.TrimSpace(f.port) == "" && old.DefaultPort() > 0 {
		f.port = strconv.Itoa(old.DefaultPort())
	}
	f.engine, f.preset = e.Label(), nil
}

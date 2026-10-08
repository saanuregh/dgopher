package app

import (
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/safety"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// AskConfirm asks before running something: at once, or after the
// requests already asked, which a new one never replaces. The caller may
// set the returned request's other answers.
func (a *App) AskConfirm(cn *connection.Conn, v safety.Verdict, title, action, preview string, onConfirm func()) *widgets.ConfirmRequest {
	cfg := cn.Config
	confirmed := func() {
		detail := title + " — " + strings.Join(v.Reasons, "; ")
		if v.TypeName {
			detail += " (confirmed by typing the connection's name)"
		}
		a.Record(&cfg, audit.Event{Kind: audit.KindConfirm, Statement: preview, Detail: detail})
		onConfirm()
	}
	r := &widgets.ConfirmRequest{Open: true, Conn: cn, Title: title, Reasons: v.Reasons, Preview: preview,
		TypeName: v.TypeName, Action: action, OnConfirm: confirmed}
	if a.confirm != nil {
		a.confirmQueue = append(a.confirmQueue, r)
	} else {
		a.confirm = r
	}
	return r
}

// asking reports whether a request matching ok is shown or waiting.
func (a *App) asking(ok func(*widgets.ConfirmRequest) bool) bool {
	if a.confirm != nil && ok(a.confirm) {
		return true
	}
	return slices.ContainsFunc(a.confirmQueue, ok)
}

// RecordBlocked notes in the audit log what the safety policy refused.
func (a *App) RecordBlocked(cn *connection.Conn, why, what string) {
	a.Record(&cn.Config, audit.Event{Kind: audit.KindBlocked, Statement: what, Detail: why})
}

type passwordPrompt struct {
	open     bool
	conn     *connection.Conn // nil when the form's Test asks
	name     string           // what the password is for
	where    string
	action   string
	password string
	onSubmit func(string)
	onCancel func()
	answered bool
}

type hostKeyRequest struct {
	open              bool
	host, fingerprint string
	onTrust           func()
}

// dialogs builds the window's dialogs.
func (a *App) dialogs(c *ui.Context) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	if a.connForm != nil {
		a.connFormView(c)
		if !a.connForm.open {
			a.connForm = nil
		}
	}
	if r := a.confirm; r != nil {
		widgets.ConfirmView(c, r)
		if !r.Open {
			if !r.Answered && r.OnCancel != nil {
				r.OnCancel()
			}
			a.confirm = nil
			if len(a.confirmQueue) > 0 {
				a.confirm, a.confirmQueue = a.confirmQueue[0], a.confirmQueue[1:]
			}
		}
	}
	if p := a.prompt; p != nil {
		ui.Modal(c, &p.open, func() {
			ui.Column(c).Width(360).Gap(12).Children(func() {
				ui.Text(c, "Password for "+p.name).FontSize(15).Bold()
				ui.Text(c, p.where).TextColor(pal.Muted)
				submit := ui.TextInput(c, &p.password).Password().AutoFocus().Label("Password").Submitted()
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, "Cancel").Clicked() {
						p.open = false
					}
					if ui.PrimaryButton(c, p.action).Clicked() || submit {
						p.open, p.answered = false, true
						p.onSubmit(p.password)
					}
				})
			})
		})
		if !p.open {
			// Escape and a click outside close it too: all but submitting cancel.
			if !p.answered {
				p.onCancel()
			}
			a.prompt = nil
			if len(a.promptQueue) > 0 {
				a.prompt, a.promptQueue = a.promptQueue[0], a.promptQueue[1:]
			}
		}
	}
	if h := a.hostKey; h != nil {
		ui.Modal(c, &h.open, func() {
			ui.Column(c).Width(460).Gap(12).Children(func() {
				ui.Text(c, "Trust this SSH server?").FontSize(15).Bold()
				ui.Text(c, h.host+" has not been seen before. Compare its fingerprint with the one its administrator gives, then trust it to connect.")
				ui.Text(c, h.fingerprint).Font(widgets.MonoFont).FontSize(12).Padding(8).Radius(6).Background(pal.EditorBg).Selectable()
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, "Cancel").Clicked() {
						h.open = false
					}
					if ui.PrimaryButton(c, "Trust and Connect").Clicked() {
						h.open = false
						h.onTrust()
					}
				})
			})
		})
		if !h.open {
			a.hostKey = nil
		}
	}
	if p := a.pending; p != nil {
		title := fmt.Sprintf("Apply %d pending change%s first?", p.n, widgets.Plural(p.n))
		switch ui.AlertDialog(c, &p.open, title, "Reading the rows again shows them as the database has them: the changes not applied yet are written first, or dropped.", "Cancel", "Discard", "Apply…") {
		case 0:
			p.cancel()
		case 1:
			p.discard()
		case 2:
			p.apply()
		}
		// An answer may ask again, as for the next result's changes:
		// that question stays.
		if !p.open && a.pending == p {
			a.pending = nil
		}
	}
	if r := a.closing; r != nil {
		if len(r.txs) > 0 {
			switch ui.AlertDialog(c, &r.open, r.title, r.reason, "Cancel", "Roll Back", "Commit") {
			case 1:
				r.onClose()
			case 2:
				a.commitThen(r.txs, r.onClose)
			}
		} else if ui.AlertDialog(c, &r.open, r.title, r.reason, "Cancel", "Close") == 1 {
			r.onClose()
		}
		if !r.open {
			a.closing = nil
		}
	}
	if al := a.alert; al != nil {
		ui.Modal(c, &al.open, func() {
			ui.Column(c).Width(480).Gap(12).Children(func() {
				ui.Row(c).Gap(8).Children(func() {
					ui.Icon(c, widgets.IconAlert).TextColor(t.Danger).FontSize(16)
					ui.Text(c, al.title).FontSize(15).Bold()
				})
				ui.Scroll(c).MaxHeight(300).Children(func() {
					ui.Text(c, al.message).Selectable()
				})
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, "Copy").Clicked() {
						a.WriteClipboard(al.message)
					}
					if ui.PrimaryButton(c, "OK").Clicked() || c.Shortcut(0, ui.KeyEnter) {
						al.open = false
					}
				})
			})
		})
		if !al.open {
			a.alert = nil
		}
	}
	if a.palette != nil {
		a.paletteView(c)
	}
	if a.settingsOpen {
		a.settingsView(c)
	}
}

// askDeleteConn asks before deleting a saved connection.
func (a *App) askDeleteConn(cn *connection.Conn) {
	reason := "The connection's settings and saved passwords are removed. Its database is not touched."
	if lost := a.losses(cn); len(lost) > 0 {
		reason += "\n\nIts tabs close, losing:\n" + strings.Join(lost, "\n")
	}
	a.closing = &closeRequest{open: true, title: "Delete " + cn.Config.Name + "?",
		reason: reason,
		onClose: func() {
			if err := cn.Project.Writable(); err != nil {
				a.ShowError("Could not delete "+cn.Config.Name, err.Error())
				return
			}
			a.disconnect(cn)
			for i, x := range a.conns {
				if x == cn {
					a.conns = append(a.conns[:i], a.conns[i+1:]...)
					break
				}
			}
			a.deleteSecrets(&cn.Config)
			a.saveProject(cn.Project)
			a.Record(&cn.Config, audit.Event{Kind: audit.KindConfig, Detail: "connection deleted"})
		}}
}

func (a *App) duplicateConn(cn *connection.Conn) {
	if err := cn.Project.Writable(); err != nil {
		a.ShowError("Could not duplicate "+cn.Config.Name, err.Error())
		return
	}
	cfg := cn.Config
	if sourceOf(&cfg) == sourceKeychain {
		a.loadSecrets(&cfg)
	} else {
		loaded := cfg
		a.loadSecrets(&loaded)
		cfg.SSH.Password, cfg.SSH.KeyPassphrase = loaded.SSH.Password, loaded.SSH.KeyPassphrase
	}
	cfg.Name += " copy"
	cfg.ID = a.uniqueConnID(cn.Project, cfg.Name)
	if err := a.saveSecrets(&cfg, !cfg.AskPassword); err != nil {
		a.ShowError("Could not save the password in the keychain", err.Error())
		return
	}
	cfg.Password, cfg.SSH.Password, cfg.SSH.KeyPassphrase = "", "", ""
	dup := &connection.Conn{Config: cfg, Project: cn.Project}
	dup.Reset()
	a.conns = append(a.conns, dup)
	if a.trusted(cn) {
		a.trust(dup)
	}
	a.saveProject(cn.Project)
}

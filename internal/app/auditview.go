package app

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// auditState is the audit log's tab of the history dialog.
type auditState struct {
	log      *audit.Log
	file     string // the state file the log is in
	events   []audit.Event
	read     bool
	err      string
	filter   string
	kind     string
	origin   int // one of auditOrigins
	list     ui.ListState
	row      int
	verified *audit.Verification
	checking bool
	// want is the entry to choose once the log is read; 0 for none.
	want int64
	// missing says the entry asked for is not among those read.
	missing string
	// traffic shows every statement and command as sent, read with them.
	traffic bool
}

var auditKinds = []string{"All kinds", audit.KindStatement, audit.KindEdit, audit.KindCommand, audit.KindScript, audit.KindImport, audit.KindExport,
	audit.KindBackup, audit.KindRestore,
	audit.KindKill, audit.KindConfirm, audit.KindBlocked, audit.KindConnect, audit.KindDisconnect, audit.KindTrust, audit.KindConfig}

// auditOrigins are the choices of who wrote a statement, and
// auditOriginValues the Origin each stands for.
var (
	auditOrigins      = []string{"Anyone", "Typed", "App"}
	auditOriginValues = []string{"", audit.OriginTyped, audit.OriginApp}
)

const auditShown = 5000

// readAudit reads the newest entries of a project's audit log.
func (a *App) readAudit(p *project.Project) *auditState {
	v := &auditState{log: p.Audit, kind: auditKinds[0], row: -1}
	v.list.Selected = &v.row
	if p.Audit == nil {
		v.err = "The audit log could not be opened: " + p.AuditErr
		if p.Err != "" {
			v.err = "The project could not be read: " + p.Err
		}
		return v
	}
	v.file = p.Local.Path()
	v.reload(a)
	return v
}

// reload reads the newest entries again, with the traffic or without.
func (v *auditState) reload(a *App) {
	l, traffic := v.log, v.traffic
	a.Background(func() func() {
		events, err := l.Read(auditShown, traffic)
		return func() {
			if traffic != v.traffic {
				return // asked again since
			}
			v.events, v.read, v.err = events, true, ""
			if err != nil {
				v.err = err.Error()
			}
			if v.want != 0 {
				v.choose(v.want)
				v.want = 0
			}
		}
	})
}

// choose shows every entry and chooses the one numbered seq, once read.
func (v *auditState) choose(seq int64) {
	v.filter, v.kind, v.origin, v.missing = "", auditKinds[0], 0, ""
	if !v.read {
		v.want = seq
		return
	}
	for i, e := range v.events {
		if e.Seq == seq {
			v.row = i
			v.list.ScrollIntoView(i)
			return
		}
	}
	v.missing = fmt.Sprintf("Entry %d is not among the newest %d.", seq, len(v.events))
}

func (v *auditState) shown() []audit.Event {
	f := strings.ToLower(strings.TrimSpace(v.filter))
	origin := auditOriginValues[v.origin]
	var out []audit.Event
	for _, e := range v.events {
		if v.kind != auditKinds[0] && e.Kind != v.kind {
			continue
		}
		if origin != "" && e.Origin != origin {
			continue
		}
		if f != "" && !strings.Contains(strings.ToLower(e.Connection+" "+e.Statement+" "+e.Typed+" "+e.Purpose+" "+e.Detail+" "+e.Error+" "+e.User), f) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// auditCount says how many of the entries read show: the newest
// auditShown are read, of what may be more.
func auditCount(shown, read int) string {
	switch {
	case shown < read:
		return fmt.Sprintf("%d of the newest %d entries", shown, read)
	case read == auditShown:
		return fmt.Sprintf("The newest %d entries", read)
	case read == 1:
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", read)
}

func kindColor(c *ui.Context, kind string) ui.Color {
	th := c.Theme()
	switch kind {
	case audit.KindBlocked:
		return th.Danger
	case audit.KindConfirm, audit.KindKill, audit.KindTrust:
		return th.Warning
	case audit.KindEdit, audit.KindImport:
		return th.Accent
	}
	return widgets.PaletteOf(c).Muted
}

func (a *App) auditTab(c *ui.Context, h *historyState) {
	v := h.audit
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	shown := v.shown()
	ui.Row(c).Padding(8, 16).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
		ui.Icon(c, widgets.IconShield).FontSize(16).TextColor(th.Accent)
		widgets.SearchBox(c, &v.filter, "Filter by statement, connection, user or error", 0).AutoFocus()
		ui.Select(c, &v.kind, auditKinds).Label("Kind")
		if ui.Checkbox(c, &v.traffic, "Traffic").Disabled(v.log == nil).
			Tooltip("Every statement and command as it went to a server, the app's own reads of schemas and its probes too. Hidden by default: there is a lot of it.").Changed() {
			v.reload(a)
		}
		ui.Segmented(c, &v.origin, auditOrigins...).Label("Written by").
			Tooltip("Typed: what a person wrote, in an editor, the console, a file or a dashboard. App: what the app sent for what was asked of it.")
	})
	ui.Row(c).Padding(8, 16).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
		ui.Text(c, auditCount(len(shown), len(v.events))).FontSize(12).TextColor(pal.Muted)
		ui.Spacer(c)
		switch {
		case v.checking:
		case v.verified != nil && v.verified.Intact():
			ui.Text(c, fmt.Sprintf("✓ Chain intact: %d entries", v.verified.Entries)).FontSize(12).TextColor(th.Success).Bold().
				Tooltip("No entry was changed, removed, inserted or moved, and the log starts at its first entry. A cut-off end, or a log rewritten whole, shows only against a copy of the head kept elsewhere.")
		case v.verified != nil:
			ui.Text(c, fmt.Sprintf("✗ Entry %d: %s", v.verified.Broken, v.verified.Reason)).FontSize(12).TextColor(th.Danger).Bold()
		}
		if v.log != nil && ui.Button(c, "Verify Integrity").Disabled(v.checking).Clicked() {
			v.checking = true
			l := v.log
			dataview.BackgroundResetOnPanic(a, func() { v.checking = false }, func() func() {
				res, err := l.Verify()
				return func() {
					v.checking = false
					if err != nil {
						v.err = err.Error()
						return
					}
					v.verified = &res
				}
			})
		}
		if v.log != nil && ui.Button(c, "Copy Head").Tooltip("The last entry's number and hash: keep them elsewhere, as in a ticket, to show later that the log was not rewritten or cut short").Clicked() {
			seq, hash := v.log.Head()
			a.WriteClipboard(fmt.Sprintf("dgopher audit head: entry %d, sha256 %s", seq, hash))
			c.Toast("Copied the audit head")
		}
		if v.log != nil && ui.Button(c, "Export…").Clicked() {
			a.exportAudit(v.log)
		}
		if v.log != nil && ui.Button(c, "Show File").Clicked() {
			mygo.Shell.ShowItemInFolder(v.file)
		}
	})
	if msg := cmp.Or(v.err, v.missing); msg != "" {
		ui.Text(c, msg).TextColor(th.Danger).Padding(10, 16).Selectable()
	}
	cols := []ui.TableColumn{
		{Title: "Time", Width: 150}, {Title: "Kind", Width: 90}, {Title: "By", Width: 50}, {Title: "Connection", Width: 170},
		{Title: "What", MinWidth: 200}, {Title: "Rows", Width: 70, Align: ui.End}, {Title: "Took", Width: 70, Align: ui.End},
	}
	dense := *th
	dense.Spacing = 2.5
	c.SetTheme(&dense)
	table := ui.Table(c, &v.list, cols, len(shown), func(r, col int) {
		e := shown[r]
		chosen := r == v.row
		switch col {
		case 0:
			ui.Text(c, e.Time.Local().Format("Jan 02 15:04:05")).FontSize(12).Font(widgets.MonoFont).SingleLine()
		case 1:
			ui.Text(c, e.Kind).FontSize(12).Bold().TextColor(widgets.RowColor(c, kindColor(c, e.Kind), chosen)).SingleLine()
		case 2:
			if e.Origin != "" {
				ui.Text(c, e.Origin).FontSize(12).TextColor(widgets.RowColor(c, pal.Muted, chosen)).SingleLine()
			}
		case 3:
			ui.Row(c).Gap(6).Children(func() {
				if e.Environment != "" {
					ui.Box(c).Size(7, 7).Radius(4).Background(widgets.EnvironmentColor(dbEnv(e.Environment)))
				}
				ui.Text(c, e.Connection).FontSize(12.5).SingleLine()
			})
		case 4:
			what := cmp.Or(e.Statement, e.Detail)
			txt := ui.Text(c, widgets.OneLine(what, 200)).FontSize(12).Font(widgets.MonoFont).SingleLine()
			switch {
			case e.Error != "":
				txt.TextColor(widgets.RowColor(c, th.Danger, chosen))
			case e.Kind == audit.KindTraffic:
				txt.TextColor(widgets.RowColor(c, pal.Muted, chosen))
			}
		case 5:
			if e.Rows > 0 {
				ui.Text(c, fmt.Sprint(e.Rows)).FontSize(12)
			}
		case 6:
			if e.DurationMS > 0 {
				ui.Text(c, widgets.FormatDuration(time.Duration(e.DurationMS)*time.Millisecond)).FontSize(12)
			}
		}
	}).Grow(1).Label("Audit entries")
	c.SetTheme(th)
	if table.Submitted() && v.row >= 0 && v.row < len(shown) {
		if e := shown[v.row]; cmp.Or(e.Typed, e.Statement) != "" {
			a.openStatement(h, e.ConnectionID, e.Database, cmp.Or(e.Typed, e.Statement))
		}
	}
	if v.row >= 0 && v.row < len(shown) {
		e := shown[v.row]
		ui.Column(c).Padding(10, 16).Gap(4).MaxHeight(220).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
			ui.Scroll(c).Children(func() {
				ui.Column(c).Gap(4).Children(func() {
					ui.Text(c, fmt.Sprintf("#%d · %s · %s · %s", e.Seq, e.Time.Local().Format(time.RFC3339), e.User, e.Kind)).FontSize(12).TextColor(pal.Muted).Selectable()
					if e.Purpose != "" {
						ui.Text(c, "Sent by the app "+e.Purpose).FontSize(12.5).Selectable()
					}
					if e.Typed != "" {
						ui.Text(c, "Typed").FontSize(11).Bold().TextColor(pal.Muted)
						ui.Text(c, e.Typed).Font(widgets.MonoFont).FontSize(12).Selectable()
						ui.Text(c, "Sent").FontSize(11).Bold().TextColor(pal.Muted)
					}
					if e.Statement != "" {
						ui.Text(c, e.Statement).Font(widgets.MonoFont).FontSize(12).Selectable()
					}
					if e.Params != "" {
						ui.Text(c, "Parameters: "+e.Params).Font(widgets.MonoFont).FontSize(12).Selectable()
					}
					if e.Detail != "" {
						ui.Text(c, e.Detail).FontSize(12.5).Selectable()
					}
					if e.Error != "" {
						ui.Text(c, e.Error).FontSize(12.5).TextColor(th.Danger).Selectable()
					}
					ui.Text(c, "hash "+e.Hash+"  ←  "+e.Prev).Font(widgets.MonoFont).FontSize(10.5).TextColor(pal.Muted).Selectable()
				})
			})
		})
	}
	ui.Row(c).Padding(10, 16).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
		ui.Text(c, "Each entry carries the hash of the one before it: an entry changed, removed or inserted breaks the chain. To catch a cut-off end or a rewrite, keep a copy of the head elsewhere. Secrets are redacted. Enter opens a statement in a new editor.").
			FontSize(12).TextColor(pal.Muted).Grow(1).Shrink(1)
		if ui.Button(c, "Close").Clicked() {
			h.open = false
		}
	})
}

// exportAudit writes the audit log, one JSON entry per line, to a file
// the user chooses.
func (a *App) exportAudit(l *audit.Log) {
	go func() {
		defer dataview.RecoverBackground(a.Post, a.ShowError, nil)
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "Export the Audit Log",
			DefaultPath: "dgopher-audit-" + time.Now().Format("20060102") + ".jsonl"})
		if err != nil || path == "" {
			return
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err == nil {
			err = l.Export(f)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		a.Post(func() {
			if err != nil {
				a.ShowError("Could not export the audit log", err.Error())
				return
			}
			a.toast = &pendingToast{text: "Exported the audit log to " + filepath.Base(path)}
		})
	}()
}

func dbEnv(s string) db.Environment { return db.Environment(s) }

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// auditState is the viewer of the audit log.
type auditState struct {
	open     bool
	project  string
	log      *audit.Log
	file     string // the state file the log is in
	events   []audit.Event
	err      string
	filter   string
	kind     string
	list     ui.ListState
	row      int
	verified *audit.Verification
	checking bool
}

var auditKinds = []string{"All kinds", audit.KindStatement, audit.KindEdit, audit.KindCommand, audit.KindScript, audit.KindImport, audit.KindExport,
	audit.KindBackup, audit.KindRestore,
	audit.KindKill, audit.KindConfirm, audit.KindBlocked, audit.KindConnect, audit.KindDisconnect, audit.KindTrust, audit.KindConfig}

const auditShown = 5000

// openAudit opens the audit log of the current project.
func (a *App) openAudit() {
	p := a.currentProject()
	if p == nil {
		a.ShowError("Which project?", "Every project keeps its own audit log: choose one in the sidebar first.")
		return
	}
	a.openAuditOf(p)
}

func (a *App) openAuditOf(p *project.Project) {
	v := &auditState{open: true, project: p.Name, log: p.Audit, kind: auditKinds[0], row: -1}
	v.list.Selected = &v.row
	a.auditView = v
	if p.Audit == nil {
		v.err = "The audit log could not be opened: " + p.AuditErr
		if p.Err != "" {
			v.err = "The project could not be read: " + p.Err
		}
		return
	}
	v.file = p.Local.Path()
	l := p.Audit
	a.Background(func() func() {
		events, err := l.Read(auditShown)
		return func() {
			v.events = events
			if err != nil {
				v.err = err.Error()
			}
		}
	})
}

func (v *auditState) shown() []audit.Event {
	f := strings.ToLower(strings.TrimSpace(v.filter))
	var out []audit.Event
	for _, e := range v.events {
		if v.kind != auditKinds[0] && e.Kind != v.kind {
			continue
		}
		if f != "" && !strings.Contains(strings.ToLower(e.Connection+" "+e.Statement+" "+e.Detail+" "+e.Error+" "+e.User), f) {
			continue
		}
		out = append(out, e)
	}
	return out
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

func (a *App) auditViewer(c *ui.Context) {
	v := a.auditView
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	shown := v.shown()
	ui.DialogBase(c, &v.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		w, h := c.Size()
		panel.Width(min(1100, w-60)).Height(min(760, h-50)).Radius(12).Background(th.Background).Border(1, th.Border).Clip().Label("Audit log")
		ui.Row(c).Padding(12, 16).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Icon(c, widgets.IconShield).FontSize(16).TextColor(th.Accent)
			ui.Text(c, "Audit Log · "+v.project).FontSize(15).Bold().SingleLine().Shrink(1)
			widgets.SearchBox(c, &v.filter, "Filter by statement, connection, user or error", 0)
			ui.Select(c, &v.kind, auditKinds).Label("Kind")
		})
		ui.Row(c).Padding(8, 16).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, fmt.Sprintf("%d of the newest %d entries", len(shown), len(v.events))).FontSize(12).TextColor(pal.Muted)
			ui.Spacer(c)
			switch {
			case v.checking:
				ui.Spinner(c).Size(14, 14)
			case v.verified != nil && v.verified.Intact():
				ui.Text(c, fmt.Sprintf("✓ Chain intact: %d entries", v.verified.Entries)).FontSize(12).TextColor(th.Success).Bold().
					Tooltip("No entry was changed, removed, inserted or moved, and the log starts at its first entry. A cut-off end, or a log rewritten whole, shows only against a copy of the head kept elsewhere.")
			case v.verified != nil:
				ui.Text(c, fmt.Sprintf("✗ Entry %d: %s", v.verified.Broken, v.verified.Reason)).FontSize(12).TextColor(th.Danger).Bold()
			}
			if v.log != nil && ui.Button(c, "Verify Integrity").Disabled(v.checking).Clicked() {
				v.checking = true
				l := v.log
				a.Background(func() func() {
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
		if v.err != "" {
			ui.Text(c, v.err).TextColor(th.Danger).Padding(10, 16).Selectable()
		}
		cols := []ui.TableColumn{
			{Title: "Time", Width: 150}, {Title: "Kind", Width: 90}, {Title: "Connection", Width: 170},
			{Title: "What", MinWidth: 200}, {Title: "Rows", Width: 70, Align: ui.End}, {Title: "Took", Width: 70, Align: ui.End},
		}
		dense := *th
		dense.Spacing = 2.5
		c.SetTheme(&dense)
		ui.Table(c, &v.list, cols, len(shown), func(r, col int) {
			e := shown[r]
			switch col {
			case 0:
				ui.Text(c, e.Time.Local().Format("Jan 02 15:04:05")).FontSize(12).Font(widgets.MonoFont).SingleLine()
			case 1:
				ui.Text(c, e.Kind).FontSize(12).Bold().TextColor(kindColor(c, e.Kind)).SingleLine()
			case 2:
				ui.Row(c).Gap(6).Children(func() {
					if e.Environment != "" {
						ui.Box(c).Size(7, 7).Radius(4).Background(widgets.EnvironmentColor(dbEnv(e.Environment)))
					}
					ui.Text(c, e.Connection).FontSize(12.5).SingleLine()
				})
			case 3:
				what := e.Statement
				if what == "" {
					what = e.Detail
				}
				txt := ui.Text(c, widgets.OneLine(what, 200)).FontSize(12).Font(widgets.MonoFont).SingleLine()
				if e.Error != "" {
					txt.TextColor(th.Danger)
				}
			case 4:
				if e.Rows > 0 {
					ui.Text(c, fmt.Sprint(e.Rows)).FontSize(12)
				}
			case 5:
				if e.DurationMS > 0 {
					ui.Text(c, widgets.FormatDuration(time.Duration(e.DurationMS)*time.Millisecond)).FontSize(12)
				}
			}
		}).Grow(1).Label("Audit entries")
		c.SetTheme(th)
		if v.row >= 0 && v.row < len(shown) {
			e := shown[v.row]
			ui.Column(c).Padding(10, 16).Gap(4).MaxHeight(220).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
				ui.Scroll(c).Children(func() {
					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, fmt.Sprintf("#%d · %s · %s · %s", e.Seq, e.Time.Local().Format(time.RFC3339), e.User, e.Kind)).FontSize(12).TextColor(pal.Muted).Selectable()
						if e.Statement != "" {
							ui.Text(c, e.Statement).Font(widgets.MonoFont).FontSize(12).Selectable()
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
			ui.Text(c, "Each entry carries the hash of the one before it: an entry changed, removed or inserted breaks the chain. To catch a cut-off end or a rewrite, keep a copy of the head elsewhere. Secrets are redacted.").
				FontSize(12).TextColor(pal.Muted).Grow(1).Shrink(1)
			if ui.Button(c, "Close").Clicked() {
				v.open = false
			}
		})
	})
	if !v.open {
		a.auditView = nil
	}
}

// exportAudit writes the audit log, one JSON entry per line, to a file
// the user chooses.
func (a *App) exportAudit(l *audit.Log) {
	go func() {
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

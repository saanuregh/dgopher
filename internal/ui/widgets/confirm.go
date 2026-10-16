package widgets

import (
	"strings"

	"dgopher/internal/connection"

	"github.com/egoist/mygo/ui"
)

// OneRowEach is how grid edits guard against changing more than they
// say, which a request shows as a note rather than a risk.
const OneRowEach = "Each statement must change exactly one row, or every change is rolled back."

// ConfirmRequest asks before running something the safety policy flags.
type ConfirmRequest struct {
	Open      bool
	Conn      *connection.Conn
	Title     string
	Reasons   []string
	Preview   string          // the statements, as they will run
	Details   []ConfirmDetail // what the user agrees to, as where a password goes
	TypeName  bool
	Typed     string
	Action    string
	OnConfirm func()
	OnCancel  func()
	// OnSkip and OnRunAll, when set, offer to skip this statement of a
	// script, or to run the rest without asking again.
	OnSkip   func()
	OnRunAll func()
	// Trust marks the question whether to connect to a destination.
	Trust bool
	// Harmless marks a request by which nothing is lost, as connecting or
	// watching a server: its button is the accent's, not red, which stays
	// for what changes or deletes.
	Harmless bool
	// Answered is set by the buttons: closed otherwise, the request was
	// cancelled.
	Answered bool
}

// ConfirmDetail is a row of what a request asks the user to agree to.
type ConfirmDetail struct {
	Label, Value string
	// Changed marks a row that differs from what the user agreed to last.
	Changed bool
}

// ConfirmView draws a request as a dialog, in the colors of the
// connection's environment; its buttons answer it and close it.
func ConfirmView(c *ui.Context, r *ConfirmRequest) {
	t := c.Theme()
	pal := PaletteOf(c)
	envCol := SafetyColor(&r.Conn.Config)
	ui.DialogBase(c, &r.Open, func(backdrop, panel ui.Element) {
		DialogLook(c, backdrop, panel)
		panel.Width(560).MaxHeightPercent(90).Radius(12).Background(t.Background).Border(1, t.Border).Clip().Role(ui.RoleAlertDialog).Label(r.Title)
		fill, ink := EnvironmentLabel(r.Conn.Config.Env, envCol, t.Background)
		ui.Row(c).Padding(10, 16).Gap(8).Background(fill).BorderWidth(3, 0, 0, 0).BorderColor(envCol).Children(func() {
			ui.Icon(c, IconShield).TextColor(ink).FontSize(14)
			ui.Text(c, r.Conn.Config.Name+" · "+r.Conn.Config.Env.Label()).Bold().TextColor(ink)
			if r.Conn.Config.ReadOnly {
				ui.Text(c, "read-only").TextColor(ink)
			}
		})
		ui.Column(c).Padding(16, 20, 20, 20).Gap(12).Children(func() {
			ui.Text(c, r.Title).FontSize(16).Bold()
			// What could go wrong in one callout; how the app guards
			// against it, a note under it, without the warning's color.
			var risks, notes []string
			for _, reason := range r.Reasons {
				if reason == OneRowEach {
					notes = append(notes, reason)
				} else {
					risks = append(risks, reason)
				}
			}
			if len(risks) > 0 {
				ui.Column(c).Gap(6).Padding(10, 12).Radius(8).Background(t.Warning.Alpha(0.10)).Border(1, t.Warning.Alpha(0.3)).Children(func() {
					for _, reason := range risks {
						ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
							ui.Icon(c, IconAlert).TextColor(t.Warning).FontSize(14)
							ui.Text(c, reason).Grow(1).Shrink(1)
						})
					}
				})
			}
			for _, note := range notes {
				ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
					ui.Icon(c, IconInfo).TextColor(pal.Muted).FontSize(14)
					ui.Text(c, note).TextColor(pal.Muted).Grow(1).Shrink(1)
				})
			}
			if len(r.Details) > 0 {
				ui.Scroll(c).MaxHeight(300).Radius(8).Border(1, t.Border).Children(func() {
					ui.Column(c).Padding(10).Gap(6).Children(func() {
						for _, d := range r.Details {
							detailRow(c, d)
						}
					})
				})
			}
			if r.Preview != "" {
				ui.Scroll(c).MaxHeight(220).Radius(8).Background(pal.EditorBg).Border(1, t.Border).Children(func() {
					ui.Text(c, r.Preview).Font(MonoFont).FontSize(12).Padding(10).Selectable()
				})
			}
			ready := true
			if r.TypeName {
				ready = strings.TrimSpace(r.Typed) == r.Conn.Config.Name
				name := r.Conn.Config.Name
				ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
					ui.Text(c, "Type").TextColor(pal.Muted)
					// Not selectable: copied and pasted, it would not be typed.
					ui.Text(c, name).Font(MonoFont).FontSize(12.5).Padding(1, 6).Radius(4).Background(pal.Hover)
					ui.Text(c, "to confirm.").TextColor(pal.Muted)
				})
				// No placeholder: the name in it would look typed already.
				// The check says it matches before the button does.
				in := ui.TextInput(c, &r.Typed).AutoFocus().Label("Connection name")
				if ready {
					in.DrawOver(func(p *ui.Painter, r ui.Rect) {
						p.Icon(IconCheck, ui.Rect{X: r.X + r.W - 26, Y: r.Y + (r.H-14)/2, W: 14, H: 14}, t.Success)
					})
				}
				if in.Submitted() && ready {
					r.Open, r.Answered = false, true
					r.OnConfirm()
				}
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					r.Open, r.Answered = false, true
					if r.OnCancel != nil {
						r.OnCancel()
					}
				}
				if r.OnSkip != nil && ui.Button(c, "Skip").Clicked() {
					r.Open, r.Answered = false, true
					r.OnSkip()
				}
				if r.OnRunAll != nil && ui.Button(c, "Run All").Tooltip("Run this and the script's other writes without asking; destructive statements, and those that commit the open transaction, still ask").Clicked() {
					r.Open, r.Answered = false, true
					r.OnRunAll()
				}
				var confirm ui.Element
				if r.Harmless {
					confirm = ui.PrimaryButton(c, r.Action).Disabled(!ready)
				} else {
					confirm = DangerButton(c, r.Action, !ready)
				}
				if confirm.Clicked() && ready {
					r.Open, r.Answered = false, true
					r.OnConfirm()
				}
			})
		})
	})
}

// detailRow draws a row of a request's details, a changed one in the
// warning color and said to be changed, which a color alone would not
// tell everyone.
func detailRow(c *ui.Context, d ConfirmDetail) {
	t := c.Theme()
	pal := PaletteOf(c)
	ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
		label := ui.Text(c, d.Label).FontSize(12.5).Width(140).Shrink(0).TextColor(pal.Muted)
		value := ui.Text(c, d.Value).FontSize(12.5).Grow(1).Shrink(1).Selectable()
		if d.Changed {
			label.TextColor(t.Warning).Bold()
			value.TextColor(t.Warning)
			ui.Text(c, "changed").FontSize(11).Bold().TextColor(t.Warning).Shrink(0)
		}
	})
}

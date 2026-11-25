package widgets

import (
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/db"

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

// EnvBand is the band across the top of a dialog that names the
// connection it acts on, in the colors of its environment. It sits flush
// at the top of a panel without padding that clips its corners.
func EnvBand(c *ui.Context, cfg *db.Config) {
	envCol := SafetyColor(cfg)
	fill, ink := EnvironmentLabel(cfg.Env, envCol, c.Theme().Background)
	ui.Row(c).Padding(10, 16).Gap(8).Background(fill).BorderWidth(3, 0, 0, 0).BorderColor(envCol).Children(func() {
		ui.Icon(c, IconShield).TextColor(ink).FontSize(IconMedium)
		ui.Text(c, cfg.Name+" · "+cfg.Env.Label()).Bold().TextColor(ink)
		if cfg.ReadOnly {
			ui.Text(c, "read-only").TextColor(ink)
		}
	})
}

// ConfirmView draws a request as a dialog, in the colors of the
// connection's environment; its buttons answer it and close it.
func ConfirmView(c *ui.Context, r *ConfirmRequest) {
	t := c.Theme()
	pal := PaletteOf(c)
	Dialog(c, &r.Open, DialogMedium, func(panel ui.Element) {
		panel.MaxHeightPercent(90).Role(ui.RoleAlertDialog).Label(r.Title)
		EnvBand(c, &r.Conn.Config)
		DialogBody(c, func() {
			DialogTitle(c, r.Title)
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
				ui.Column(c).Gap(6).Padding(10, 12).Radius(RadiusLarge).Background(t.Warning.Alpha(0.10)).Border(1, t.Warning.Alpha(0.3)).Children(func() {
					for _, reason := range risks {
						ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
							ui.Icon(c, IconAlert).TextColor(t.Warning).FontSize(IconMedium)
							ui.Text(c, reason).Grow(1).Shrink(1)
						})
					}
				})
			}
			for _, note := range notes {
				ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
					ui.Icon(c, IconInfo).TextColor(pal.Muted).FontSize(IconMedium)
					ui.Text(c, note).TextColor(pal.Muted).Grow(1).Shrink(1)
				})
			}
			if len(r.Details) > 0 {
				ui.Scroll(c).MaxHeight(300).Radius(RadiusLarge).Border(1, t.Border).Children(func() {
					ui.Column(c).Padding(10).Gap(6).Children(func() {
						for _, d := range r.Details {
							detailRow(c, d)
						}
					})
				})
			}
			if r.Preview != "" {
				ui.Scroll(c).MaxHeight(220).Radius(RadiusLarge).Background(pal.EditorBg).Border(1, t.Border).Children(func() {
					ui.Text(c, r.Preview).Font(MonoFont).FontSize(TextSmall).Padding(10).Selectable()
				})
			}
			ready := true
			if r.TypeName {
				ready = strings.TrimSpace(r.Typed) == r.Conn.Config.Name
				name := r.Conn.Config.Name
				ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
					ui.Text(c, "Type").TextColor(pal.Muted)
					// Not selectable: copied and pasted, it would not be typed.
					ui.Text(c, name).Font(MonoFont).FontSize(TextBody).Padding(1, 6).Radius(RadiusSmall).Background(pal.Hover)
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
				why := When(!ready, "Type the connection's name first")
				if r.Harmless {
					confirm = Unavailable(ui.PrimaryButton(c, r.Action), why)
				} else {
					confirm = DangerButton(c, r.Action, why)
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
		label := ui.Text(c, d.Label).FontSize(TextBody).Width(140).Shrink(0).TextColor(pal.Muted)
		value := ui.Text(c, d.Value).FontSize(TextBody).Grow(1).Shrink(1).Selectable()
		if d.Changed {
			label.TextColor(t.Warning).Bold()
			value.TextColor(t.Warning)
			ui.Text(c, "changed").FontSize(TextMeta).Bold().TextColor(t.Warning).Shrink(0)
		}
	})
}

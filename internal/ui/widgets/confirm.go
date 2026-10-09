package widgets

import (
	"strings"

	"dgopher/internal/connection"

	"github.com/egoist/mygo/ui"
)

// ConfirmRequest asks before running something the safety policy flags.
type ConfirmRequest struct {
	Open      bool
	Conn      *connection.Conn
	Title     string
	Reasons   []string
	Preview   string // the statements, as they will run
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
	// Answered is set by the buttons: closed otherwise, the request was
	// cancelled.
	Answered bool
}

// ConfirmView draws a request as a dialog, in the colors of the
// connection's environment; its buttons answer it and close it.
func ConfirmView(c *ui.Context, r *ConfirmRequest) {
	t := c.Theme()
	pal := PaletteOf(c)
	envCol := SafetyColor(&r.Conn.Config)
	ui.DialogBase(c, &r.Open, func(backdrop, panel ui.Element) {
		backdrop.Background(Backdrop)
		panel.Width(560).MaxHeightPercent(90).Radius(12).Background(t.Background).Border(1, t.Border).Clip().
			Shadow(0, 12, 40, 0, ui.RGBA(0, 0, 0, 0.3)).Role(ui.RoleAlertDialog).Label(r.Title)
		ui.Row(c).Padding(10, 16).Gap(8).Background(envCol).Children(func() {
			ui.Icon(c, IconShield).TextColor(ui.RGB(255, 255, 255)).FontSize(14)
			ui.Text(c, r.Conn.Config.Name+" · "+r.Conn.Config.Env.Label()).Bold().TextColor(ui.RGB(255, 255, 255))
			if r.Conn.Config.ReadOnly {
				ui.Text(c, "read-only").TextColor(ui.RGB(255, 255, 255))
			}
		})
		ui.Column(c).Padding(16, 20, 20, 20).Gap(12).Children(func() {
			ui.Text(c, r.Title).FontSize(16).Bold()
			for _, reason := range r.Reasons {
				ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
					ui.Icon(c, IconAlert).TextColor(t.Warning).FontSize(14)
					ui.Text(c, reason).Grow(1).Shrink(1)
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
				ui.Text(c, "Type the connection's name, "+r.Conn.Config.Name+", to confirm.").TextColor(pal.Muted)
				in := ui.TextInput(c, &r.Typed).AutoFocus().Label("Connection name").Placeholder(r.Conn.Config.Name)
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
				if r.OnRunAll != nil && ui.Button(c, "Run All").Tooltip("Run this and the script's other writes without asking; destructive statements still ask").Clicked() {
					r.Open, r.Answered = false, true
					r.OnRunAll()
				}
				if DangerButton(c, r.Action, !ready).Clicked() && ready {
					r.Open, r.Answered = false, true
					r.OnConfirm()
				}
			})
		})
	})
}

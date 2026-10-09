package app

import (
	"fmt"

	"dgopher/internal/keymap"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Parts of the window a stop of the tour shows, by the bounds they had in
// the last frame.
const (
	tourSidebar   = "sidebar"
	tourWorkspace = "workspace"
)

// tourStop is a stop of the guided tour: the part of the window it shows,
// "" for none, what it says, and what it offers to do.
type tourStop struct {
	part        string
	title, text string
	action      string
	run         func(a *App)
}

// tourStops are the stops of the tour, the keys named as they are set.
func tourStops() []tourStop {
	return []tourStop{
		{title: "Welcome to DGopher", text: "A minute on how it works. Esc ends the tour, which the start page and the palette offer again."},
		{part: tourSidebar, title: "Projects and connections",
			text: "Every connection belongs to a project: a folder whose dgopher.json the team shares through Git, " +
				"passwords kept out of it. The navigator lists each connection's schemas and tables, and the filter finds one."},
		{part: tourWorkspace, title: "Editors and results",
			text: keymap.Hint("Running", keymap.Run) + " runs the statement at the caret, which blank lines and semicolons end. " +
				"Its rows show below in a grid: edit cells, then review the SQL before it is applied."},
		{title: "Every command, by name",
			text: keymap.Hint("The palette", keymap.Palette) + " lists every command, connection, table and snippet; " +
				keymap.Hint("Open Table", keymap.OpenTable) + " finds a table by its name.",
			action: "Open the Palette", run: func(a *App) { a.openPalette(false) }},
		{title: "Careful with production",
			text: "A connection's environment colors it. A production one asks before every write, can be read-only, " +
				"and every statement goes to the history and the audit log."},
		{title: "Try it",
			text: "The sample database is a small shop in SQLite, made on this computer. " +
				keymap.Hint("The list of keys", keymap.ShortcutsList) + " shows every key; Settings change them, the theme, the fonts, and turn on Vim's.",
			action: "Open the Sample Database", run: func(a *App) { a.openSample() }},
	}
}

// tourPart keeps the bounds a part of the window had, for the tour to
// show it.
func (a *App) tourPart(name string, e ui.Element) {
	if !a.touring {
		return
	}
	if a.tourParts == nil {
		a.tourParts = map[string]ui.Rect{}
	}
	a.tourParts[name] = e.Bounds()
}

// startTour starts the tour at its first stop.
func (a *App) startTour() {
	a.tourStop, a.touring = 0, true
}

// endTour ends the tour, which the start page then stops offering.
func (a *App) endTour() {
	a.touring = false
	if !a.settings.TourDone {
		a.settings.TourDone = true
		a.SaveSettings()
	}
}

// tourView shows the tour's stop: the window dimmed but for the part it
// shows, and a card saying what it is, beside it.
func (a *App) tourView(c *ui.Context) {
	stops := tourStops()
	a.tourStop = min(a.tourStop, len(stops)-1)
	stop := stops[a.tourStop]
	part, shown := a.tourParts[stop.part]
	shown = shown && part.W > 0 && part.H > 0
	if c.Shortcut(0, ui.KeyEscape) {
		a.endTour()
		return
	}
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	w, h := c.Size()
	ui.Overlay(c, func() {
		// The window under the tour takes no pointer, and shows dimmed.
		ui.Box(c).Absolute().Left(0).Top(0).Size(w, h).
			HandleInput(func(ui.InputEvent) bool { return true }).
			Draw(func(p *ui.Painter, r ui.Rect) {
				dim := widgets.Backdrop
				if !shown {
					p.Fill(r, dim, 0)
					return
				}
				p.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: part.Y - r.Y}, dim, 0)
				p.Fill(ui.Rect{X: r.X, Y: part.Y + part.H, W: r.W, H: r.Y + r.H - part.Y - part.H}, dim, 0)
				p.Fill(ui.Rect{X: r.X, Y: part.Y, W: part.X - r.X, H: part.H}, dim, 0)
				p.Fill(ui.Rect{X: part.X + part.W, Y: part.Y, W: r.X + r.W - part.X - part.W, H: part.H}, dim, 0)
				p.Stroke(part, th.Accent, 6, 2)
			})
		const cardW = 360
		x, y := (w-cardW)/2, h/3
		if shown {
			// Beside the part, on the side with room, else over it.
			switch {
			case part.X+part.W+cardW+24 <= w:
				x, y = part.X+part.W+16, part.Y+24
			case part.X-cardW-16 >= 0:
				x, y = part.X-cardW-16, part.Y+24
			default:
				x, y = part.X+(part.W-cardW)/2, part.Y+24
			}
		}
		ui.Column(c).Absolute().Left(x).Top(y).Width(cardW).Padding(18).Gap(10).Radius(12).
			Background(th.Background).Border(1, th.Border).Role(ui.RoleDialog).Label(stop.title).Children(func() {
			ui.Text(c, fmt.Sprintf("%d of %d", a.tourStop+1, len(stops))).FontSize(11.5).TextColor(pal.Muted)
			ui.Text(c, stop.title).FontSize(16).Bold()
			ui.Text(c, stop.text).FontSize(13)
			ui.Row(c).Gap(8).Padding(4, 0, 0, 0).Children(func() {
				if ui.Button(c, "End Tour").Clicked() {
					a.endTour()
				}
				ui.Spacer(c)
				if stop.action != "" && ui.Button(c, stop.action).Clicked() {
					a.endTour()
					stop.run(a)
				}
				if a.tourStop > 0 && ui.Button(c, "Back").Clicked() {
					a.tourStop--
				}
				last := a.tourStop == len(stops)-1
				label := "Next"
				if last {
					label = "Done"
				}
				next := ui.PrimaryButton(c, label)
				next.Focus()
				if next.Clicked() {
					if last {
						a.endTour()
					} else {
						a.tourStop++
					}
				}
			})
		})
	})
}

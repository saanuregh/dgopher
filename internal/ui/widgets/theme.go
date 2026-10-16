// Package widgets holds what every view shares: the palette, icons, small
// controls and text helpers.
package widgets

import (
	"fmt"
	"math"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/sqltext"

	"github.com/egoist/mygo/ui"
)

// Palette is the colors the app adds to the theme's.
type Palette struct {
	Sidebar  ui.Color
	EditorBg ui.Color
	// CurrentStatement is behind the statement a run would send.
	CurrentStatement ui.Color
	LineNumber       ui.Color
	GridHeader       ui.Color
	GridLine         ui.Color
	CellSelected     ui.Color
	Null             ui.Color
	Modified         ui.Color // a cell changed and not yet applied
	Inserted         ui.Color
	Deleted          ui.Color
	Muted            ui.Color
	Hover            ui.Color
	Syntax           map[sqltext.Kind]ui.Color
}

var lightPalette = Palette{
	Sidebar:          ui.Hex("#f5f5f4"),
	EditorBg:         ui.Hex("#ffffff"),
	CurrentStatement: ui.Hex("#f4f7fd"),
	LineNumber:       ui.Hex("#a8a29e"),
	GridHeader:       ui.Hex("#f5f5f4"),
	GridLine:         ui.RGBA(0, 0, 0, 0.07),
	CellSelected:     ui.RGBA(37, 99, 235, 0.16),
	Null:             ui.Hex("#a8a29e"),
	Modified:         ui.RGBA(234, 179, 8, 0.25),
	Inserted:         ui.RGBA(34, 197, 94, 0.18),
	Deleted:          ui.RGBA(239, 68, 68, 0.16),
	Muted:            ui.Hex("#78716c"),
	Hover:            ui.RGBA(0, 0, 0, 0.05),
	Syntax: map[sqltext.Kind]ui.Color{
		sqltext.Keyword:     ui.Hex("#7c3aed"),
		sqltext.String:      ui.Hex("#15803d"),
		sqltext.Number:      ui.Hex("#c2410c"),
		sqltext.Comment:     ui.Hex("#a8a29e"),
		sqltext.QuotedIdent: ui.Hex("#0e7490"),
		sqltext.Param:       ui.Hex("#be185d"),
		sqltext.Operator:    ui.Hex("#57534e"),
	},
}

var darkPalette = Palette{
	// The chrome a step darker than the work, which comes forward.
	Sidebar:          ui.Hex("#141312"),
	EditorBg:         ui.Hex("#1a1918"),
	CurrentStatement: ui.Hex("#1f2229"),
	LineNumber:       ui.Hex("#57534e"),
	GridHeader:       ui.Hex("#1f1d1b"),
	GridLine:         ui.RGBA(255, 255, 255, 0.07),
	CellSelected:     ui.RGBA(96, 165, 250, 0.22),
	Null:             ui.Hex("#78716c"),
	Modified:         ui.RGBA(234, 179, 8, 0.22),
	Inserted:         ui.RGBA(34, 197, 94, 0.20),
	Deleted:          ui.RGBA(239, 68, 68, 0.22),
	Muted:            ui.Hex("#a8a29e"),
	Hover:            ui.RGBA(255, 255, 255, 0.06),
	Syntax: map[sqltext.Kind]ui.Color{
		sqltext.Keyword:     ui.Hex("#c4b5fd"),
		sqltext.String:      ui.Hex("#86efac"),
		sqltext.Number:      ui.Hex("#fdba74"),
		sqltext.Comment:     ui.Hex("#78716c"),
		sqltext.QuotedIdent: ui.Hex("#67e8f9"),
		sqltext.Param:       ui.Hex("#f9a8d4"),
		sqltext.Operator:    ui.Hex("#d6d3d1"),
	},
}

// PaletteOf is the palette of the frame's theme: the one in use, else
// the window's darkness's.
func PaletteOf(c *ui.Context) *Palette {
	if look != nil {
		return &look.pal
	}
	if c.Theme().Dark {
		return &darkPalette
	}
	return &lightPalette
}

// EnvColor is the color of a connection: its own, or its environment's.
func EnvColor(cfg *db.Config) ui.Color {
	// A color from a shared project file may be malformed, which ui.Hex
	// would panic on.
	if db.ValidColor(cfg.Color) {
		return ui.Hex(cfg.Color)
	}
	return EnvironmentColor(cfg.Env)
}

// HexColor writes an opaque color as a connection keeps it: #rrggbb.
func HexColor(c ui.Color) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

// ChosenRows builds lists whose chosen row is tinted with the accent
// rather than filled with it, its text and icons in their own colors: the
// choice shows without outshouting the work, as in Linear's, Zed's and the
// macOS sidebars. Lists' rows are what take the accent from the theme
// here, so build nothing else in build that would.
func ChosenRows(c *ui.Context, build func()) {
	prev := c.Theme()
	t := *prev
	t.Accent, t.AccentText = ChosenRow(c), prev.Text
	c.SetTheme(&t)
	defer c.SetTheme(prev)
	build()
}

// ChosenCells is a table's theme with chosen cells in tint over bg, the
// table's background, as ChosenRows has rows: opaque, so the
// lines the table draws in its accent, as a column's resize edge, still
// show, in the tint's color.
func ChosenCells(t ui.Theme, bg, tint ui.Color) ui.Theme {
	t.Accent = bg.Mix(ui.RGB(tint.R, tint.G, tint.B), float32(tint.A)/255)
	t.AccentText = t.Text
	return t
}

// ChosenRow is the tint behind a chosen row, for rows painted by hand.
func ChosenRow(c *ui.Context) ui.Color { return chosenTint(c.Theme()) }

func chosenTint(t *ui.Theme) ui.Color {
	if t.Dark {
		return t.Accent.Alpha(0.24)
	}
	return t.Accent.Alpha(0.14)
}

// SafetyColor is the color of where a connection's statements run: on
// staging and production, the environment's whatever the connection's
// own, which a shared project file may set; elsewhere, EnvColor.
func SafetyColor(cfg *db.Config) ui.Color {
	if cfg.Env == db.Staging || cfg.Env == db.Production {
		return EnvironmentColor(cfg.Env)
	}
	return EnvColor(cfg)
}

func EnvironmentColor(e db.Environment) ui.Color {
	switch e {
	case db.Production:
		return ui.Hex("#dc2626")
	case db.Staging:
		return ui.Hex("#d97706")
	}
	return ui.Hex("#16a34a")
}

// LabelFill is col darkened until white text on it reads as WCAG AA asks
// (4.5:1), for a label filled with an environment's or a connection's
// color: the hue stays, so production still reads as red.
func LabelFill(col ui.Color) ui.Color {
	for i := 0; i < 20 && contrastWithWhite(col) < 4.5; i++ {
		col = col.Mix(ui.RGB(0, 0, 0), 0.06)
	}
	return col
}

// LabelInk is col for text on bg, lightened on a dark bg or darkened on
// a light one until it reads as WCAG AA asks (4.5:1), as LabelFill is
// for a fill under white text: production's name stays red, and legible.
func LabelInk(col, bg ui.Color) ui.Color {
	toward := ui.RGB(0, 0, 0)
	if luminance(bg) < 0.5 {
		toward = ui.RGB(255, 255, 255)
	}
	for i := 0; i < 20 && contrast(col, bg) < 4.5; i++ {
		col = col.Mix(toward, 0.08)
	}
	return col
}

// EnvironmentLabel is the fill and ink of a label in an environment's
// color col on bg: staging and production solid, so that they stand out
// the more; development a tint of it.
func EnvironmentLabel(e db.Environment, col, bg ui.Color) (fill, ink ui.Color) {
	if e == db.Staging || e == db.Production {
		return LabelFill(col), ui.RGB(255, 255, 255)
	}
	return col.Alpha(0.14), LabelInk(col, bg.Mix(ui.RGB(col.R, col.G, col.B), 0.14))
}

// contrastWithWhite is the WCAG contrast ratio of white text on col.
func contrastWithWhite(col ui.Color) float64 { return contrast(ui.RGB(255, 255, 255), col) }

// contrast is the WCAG contrast ratio of a and b.
func contrast(a, b ui.Color) float64 {
	la, lb := luminance(a), luminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// luminance is a color's WCAG relative luminance, its alpha left out.
func luminance(col ui.Color) float64 {
	lin := func(v uint8) float64 {
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(col.R) + 0.7152*lin(col.G) + 0.0722*lin(col.B)
}

// EngineColor tints an engine's icon.
func EngineColor(e db.Engine) ui.Color {
	switch e {
	case db.Postgres:
		return ui.Hex("#336791")
	case db.MySQL:
		return ui.Hex("#00758f")
	case db.ClickHouse:
		return ui.Hex("#d4a10a")
	case db.Redis:
		return ui.Hex("#dc382d")
	case db.SQLite:
		return ui.Hex("#0f80cc")
	case db.DuckDB:
		return ui.Hex("#c49a00")
	}
	return ui.Hex("#737373")
}

// MonoFont is the family of the editor's and the grids' text: the user's,
// or the system's monospaced one.
var MonoFont = DefaultMonoFont

// DefaultMonoFont is the system's monospaced family.
const DefaultMonoFont = "monospace"

// BackdropOf dims the window behind a dialog, as ui.Modal does, or less
// behind a picker that drops from the top. A light theme takes less: the
// same black over white turns it to mud.
func BackdropOf(c *ui.Context, picker bool) ui.Color {
	switch dark := c.Theme().Dark; {
	case dark && picker:
		return ui.RGBA(0, 0, 0, 0.25)
	case dark:
		return ui.RGBA(0, 0, 0, 0.4)
	case picker:
		return ui.RGBA(0, 0, 0, 0.10)
	}
	return ui.RGBA(0, 0, 0, 0.18)
}

// ConnState shows how a connection stands: its dot, with a warning when
// it failed.
func ConnState(c *ui.Context, cn *connection.Conn) {
	ConnDot(c, cn)
	if cn.Status == connection.StatusFailed {
		ui.Icon(c, IconAlert).FontSize(10).TextColor(c.Theme().Danger)
	}
}

// ConnDot is a connection's dot, in its color: filled while the
// connection is open, a ring while it is not, and a ring filling and
// emptying while it connects, the work going on where its result shows.
func ConnDot(c *ui.Context, cn *connection.Conn) ui.Element {
	col := EnvColor(&cn.Config)
	dot := ui.Box(c).Size(7, 7).Radius(4)
	switch cn.Status {
	case connection.StatusConnected:
		return dot.Background(col)
	case connection.StatusConnecting:
		if c.Preferences().ReduceMotion {
			return dot.Background(col.Alpha(0.5)).Border(1.5, col)
		}
		// Painted from the clock: the frames it takes only paint.
		dot.Draw(func(p *ui.Painter, r ui.Rect) {
			p.AnimationFrame()
			phase := float64(p.Now().UnixMilli()%connectPulse) / connectPulse
			fill := 0.5 - 0.5*math.Cos(2*math.Pi*phase)
			p.Fill(r, col.Alpha(float32(fill)), r.W/2)
		})
	}
	return dot.Border(1.5, col)
}

// connectPulse is how long, in milliseconds, a connecting dot takes to
// fill and empty: slow enough to read as waiting, not as an alarm.
const connectPulse = 1200

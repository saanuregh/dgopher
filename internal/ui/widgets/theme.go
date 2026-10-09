// Package widgets holds what every view shares: the palette, icons, small
// controls and text helpers.
package widgets

import (
	"fmt"

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
	Gutter           ui.Color
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
	Gutter:           ui.Hex("#fafaf9"),
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
	Sidebar:          ui.Hex("#1c1917"),
	EditorBg:         ui.Hex("#171717"),
	CurrentStatement: ui.Hex("#1d2129"),
	Gutter:           ui.Hex("#1a1a1a"),
	LineNumber:       ui.Hex("#57534e"),
	GridHeader:       ui.Hex("#232323"),
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

// PaletteOf is the palette of the frame's theme: the one in use, when
// the window is of its darkness.
func PaletteOf(c *ui.Context) *Palette {
	if look != nil && look.dark == c.Theme().Dark {
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

// Backdrop dims the window behind a dialog, as ui.Modal does;
// PickerBackdrop, behind a picker that drops from the top, less.
var (
	Backdrop       = ui.RGBA(0, 0, 0, 0.4)
	PickerBackdrop = ui.RGBA(0, 0, 0, 0.25)
)

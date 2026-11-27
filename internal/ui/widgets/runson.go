package widgets

import (
	"dgopher/internal/db"

	"github.com/egoist/mygo/ui"
)

// RunsOn names the connection a holder's statements run on, before the
// button that runs them: on staging and production in the environment's
// color, with its name, where a development connection takes a tint of
// its own color. It is no menu: the holder names its connection. holder
// is what the tooltip calls it, such as "editor" or "console".
func RunsOn(c *ui.Context, cfg *db.Config, holder string) {
	guarded := cfg.Env == db.Staging || cfg.Env == db.Production
	fill, ink := EnvironmentLabel(cfg.Env, SafetyColor(cfg), c.Theme().Background)
	label, tip := "Runs on "+cfg.Name, "Statements of this "+holder+" run on "+cfg.Name
	if guarded {
		label += ", " + cfg.Env.Label()
		tip += ", a " + cfg.Env.Label() + " connection"
	}
	ui.Row(c.Key("runs-on")).Gap(6).Padding(3, 8).Radius(RadiusSmall).Background(fill).AlignItems(ui.Center).Shrink(0).
		Label(label).Tooltip(tip).Children(func() {
		if guarded {
			ui.Text(c, cfg.Env.Label()).FontSize(TextMeta).Bold().TextColor(ink)
		}
		ui.Text(c, cfg.Name).FontSize(TextSmall).FontWeight(600).TextColor(ink).SingleLine().MaxWidth(180)
	})
}

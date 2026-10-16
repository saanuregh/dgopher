package editor

import (
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	code "github.com/egoist/mygo/ui/editor"
)

// cardHover builds the editor's hover as the app's cards look: the
// problems at the text hovered, each with a bar of its severity's color,
// then the card the owner gave ShowHover.
func cardHover(c *ui.Context, panel ui.Element, h *code.HoverCard) {
	t := c.Theme()
	ct := h.Theme()
	widgets.CardPanel(c, panel, false)
	panel.Margin(0, 0, t.Space(1), 0)
	panel.Children(func() {
		widgets.CardScroll(c).Children(func() {
			if diags := h.Diagnostics(); len(diags) > 0 {
				ui.Column(c).Gap(6).Padding(10, 12).Children(func() {
					for _, d := range diags {
						ui.Row(c).Gap(8).Children(func() {
							ui.Box(c).Width(3).Radius(1.5).Background(ct.SeverityColor(d.Severity)).AlignSelf(ui.Stretch)
							ui.Text(c, d.Message).Shrink(1).Selectable()
						})
					}
				})
			}
			if text := h.Text(); text != "" {
				ui.Text(c, text).Font(h.Font().Family).FontSize(h.Font().Size).Padding(10, 12).Selectable()
			}
			h.Content()
		})
	})
}

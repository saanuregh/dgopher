package query

import (
	"dgopher/internal/ui/editor"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// syncVim gives the editor Vim's keys, or takes them away, as the
// settings say.
func (q *Tab) syncVim() {
	switch on := q.a.Settings().Vim; {
	case on && q.Editor.Vim == nil:
		v := editor.NewVim()
		v.ReadClipboard, v.WriteClipboard, v.Save = q.a.ReadClipboard, q.a.WriteClipboard, q.save
		q.Editor.Vim = v
	case !on && q.Editor.Vim != nil:
		q.Editor.Vim = nil
	}
}

// vimBar shows Vim's mode, the command being typed, or what the last
// said, under the editor.
func (q *Tab) vimBar(c *ui.Context) {
	v := q.Editor.Vim
	if v == nil {
		return
	}
	pal := widgets.PaletteOf(c)
	status := v.Status()
	if status == "" {
		status = "-- NORMAL --"
	}
	ui.Row(c).Padding(2, 12).BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(func() {
		ui.Text(c, status).Font(widgets.MonoFont).FontSize(11.5).TextColor(pal.Muted).SingleLine()
	})
}

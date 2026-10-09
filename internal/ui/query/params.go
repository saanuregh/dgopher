package query

import (
	"strings"

	"dgopher/internal/params"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// paramForm asks the values of a run's parameters.
type paramForm struct {
	open   bool
	fields []paramField
	run    func(values map[string]params.Input)
}

type paramField struct {
	key, value string
	kind       int // in paramKinds
}

func (f *paramForm) submit() {
	values := map[string]params.Input{}
	for _, fl := range f.fields {
		values[fl.key] = params.Input{Value: fl.value, Kind: params.Kinds[fl.kind]}
	}
	f.open = false
	f.run(values)
}

func paramsView(a Host, c *ui.Context) {
	f := a.QueryDialogs().params
	pal := widgets.PaletteOf(c)
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(520).Gap(12).Children(func() {
			ui.Text(c, "Parameters").FontSize(15).Bold()
			ui.Text(c, ":name values are sent apart from the SQL, as text, numbers or NULL; ${name} values are written into it.").FontSize(12).TextColor(pal.Muted)
			submit := false
			ui.Form(c, func() {
				for i := range f.fields {
					fl := &f.fields[i]
					ui.Field(c, fl.key, func() {
						ui.Row(c).Gap(6).Children(func() {
							in := ui.TextInput(c, &fl.value).Font(widgets.MonoFont).FontSize(12.5).Label(fl.key).Grow(1)
							if i == 0 {
								in.AutoFocus()
							}
							if in.Submitted() {
								submit = true
							}
							if !strings.HasPrefix(fl.key, "${") {
								ui.Segmented(c, &fl.kind, "Auto", "Text", "Number", "NULL").Label("Send as")
							}
						})
					}).Description(paramKind(fl))
				}
			})
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if ui.PrimaryButton(c, "Run").Clicked() || submit {
					f.submit()
				}
			})
		})
	})
	if !f.open && a.QueryDialogs().params == f {
		a.QueryDialogs().params = nil
	}
}

func paramKind(fl *paramField) string {
	if strings.HasPrefix(fl.key, "${") {
		return "written into the SQL as typed"
	}
	if fl.kind == 0 {
		return "Auto: NULL for null; on PostgreSQL text the server converts; elsewhere a number when it reads as one"
	}
	return ""
}

// DialogsView draws the query tab's open dialogs.
func DialogsView(a Host, c *ui.Context) {
	if a.QueryDialogs().params != nil {
		paramsView(a, c)
	}
	if a.QueryDialogs().outline != nil {
		outlineView(a, c)
	}
	if a.QueryDialogs().usages != nil {
		usagesView(a, c)
	}
	if a.QueryDialogs().rename != nil {
		renameView(a, c)
	}
}

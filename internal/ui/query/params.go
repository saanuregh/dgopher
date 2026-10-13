package query

import (
	"strings"
	"time"

	"dgopher/internal/params"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// paramForm asks the values of a run's parameters.
type paramForm struct {
	open    bool
	fields  []paramField
	run     func(values map[string]params.Input)
	dismiss widgets.DismissGuard
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
	dlg := ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(520).Gap(12).Children(func() {
			ui.Text(c, "Parameters").FontSize(15).Bold()
			ui.Text(c, ":name values are sent apart from the SQL, as text, numbers or NULL; ${name} values are written into it. They are kept for this connection's editors: change them in the Parameters bar.").FontSize(12).TextColor(pal.Muted)
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
			if widgets.DialogFooter(c, &f.open, "Run", submit) {
				f.submit()
			}
		})
	})
	f.dismiss.Keep(c, dlg, &f.open, f.fields, false)
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

// textParams are the parameters of the editor's whole text, found again
// as it changes.
func (q *Tab) textParams() []string {
	if q.Editor.Text() != q.paramKeysText {
		var stmts []string
		for _, s := range sqltext.SplitWith(q.Editor.Text(), q.Editor.Dialect, SplitOptions(q.a.Settings())) {
			stmts = append(stmts, s.Text)
		}
		q.paramKeysText, q.paramKeys = q.Editor.Text(), params.Keys(stmts, q.Editor.Dialect)
	}
	return q.paramKeys
}

// paramsBarState is the parameters bar's state: the values kept for
// the connection, as last read, and those typed over them.
type paramsBarState struct {
	conn    string // the connection's ID the values are of
	version int64  // dataview.ParamsVersion as kept was read
	kept    map[string]params.Input
	edits   map[string]*paramEdit
	typedAt time.Time
}

// paramEdit is a value of the bar: as typed, as last seen typed, and as
// kept when typing began over it.
type paramEdit struct{ now, seen, base params.Input }

// paramKeepDelay is how long typing in the bar stops before what is
// typed is kept.
const paramKeepDelay = 500 * time.Millisecond

// paramsBar edits the values kept for the parameters of the editor's
// text, kept once typing stops: a run takes them without asking, and
// asks for one left empty. One kept from another editor shows.
func (q *Tab) paramsBar(c *ui.Context, keys []string) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	b := &q.paramsBarState
	q.readKeptParams()
	if b.edits == nil {
		b.edits = map[string]*paramEdit{}
	}
	pending := false
	ui.Row(c).Padding(6, 12).Gap(10).Wrap().AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
		ui.Text(c, "Parameters").FontSize(12).TextColor(pal.Muted)
		for _, k := range keys {
			base := b.keptOf(k)
			e := b.edits[k]
			if e == nil || e.base != base {
				e = &paramEdit{now: base, seen: base, base: base}
				b.edits[k] = e
			}
			ui.Row(c.Key("param-" + k)).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Text(c, k).Font(widgets.MonoFont).FontSize(12).TextColor(pal.Muted)
				ui.TextInput(c, &e.now.Value).Width(140).Font(widgets.MonoFont).FontSize(12.5).Placeholder("asked when run").Label("Value of " + k)
				if !strings.HasPrefix(k, "${") {
					ui.Select(c, &e.now.Kind, params.Kinds).Width(90).Label("Send " + k + " as")
				}
				if e.now != blankParam && widgets.IconButton(c, widgets.IconX, "Forget the value of "+k).Clicked() {
					e.now = blankParam
				}
			})
			if e.now != e.seen {
				e.seen, b.typedAt = e.now, q.a.Now()
			}
			pending = pending || e.now != e.base
		}
	})
	if pending {
		if wait := paramKeepDelay - q.a.Now().Sub(b.typedAt); wait > 0 {
			c.After(wait)
		} else {
			q.keepParamEdits()
		}
	}
}

// blankParam is a parameter's value before one is given.
var blankParam = params.Input{Kind: params.Kinds[0]}

// readKeptParams reads the values kept for the connection again when
// they changed since, or the connection's ID did.
func (q *Tab) readKeptParams() {
	b := &q.paramsBarState
	if id := q.connID(); b.conn != id || b.version != dataview.ParamsVersion() {
		b.conn, b.version, b.kept = id, dataview.ParamsVersion(), dataview.ParamValues(q.Conn.Project, id)
	}
}

// keptOf is the value kept for a parameter, blank for none.
func (b *paramsBarState) keptOf(key string) params.Input {
	if in, ok := b.kept[key]; ok {
		return in
	}
	return blankParam
}

// keepParamEdits keeps what was typed in the parameters bar, unless the
// value it was typed over changed since, as in another editor while this
// one was not shown: the later value stays, and shows in the bar.
func (q *Tab) keepParamEdits() {
	b := &q.paramsBarState
	if len(b.edits) == 0 {
		return
	}
	q.readKeptParams()
	for k, e := range b.edits {
		if e.now != e.base && b.keptOf(k) == e.base {
			q.keepParam(b.conn, k, e.now)
			e.base = e.now
		}
	}
}

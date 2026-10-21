package chat

import (
	"cmp"
	"slices"
	"strings"

	agent "github.com/saanuregh/agent-cli-connector"

	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// pickerHeight is the most the model picker's list takes, which scrolls
// past it: pi lists hundreds of models.
const pickerHeight = 360

// modelPicker is the model picker's state: whether it shows, what is
// typed in its search, and the model the keys are on.
type modelPicker struct {
	open  bool
	query string
	hi    int
	// moved asks the model the keys are on to come into view.
	moved bool
}

// pickEntry is a model the picker offers: id "" for the agent's default.
type pickEntry struct {
	id, label, detail, group string
}

// pickEntries are the models matching the search, the default first,
// then by group, as the agent lists them.
func (t *Chat) pickEntries(l *modelList, query string) []pickEntry {
	def := pickEntry{label: "Default"}
	if d := l.modelOf(""); d != nil {
		if name := cmp.Or(d.DisplayName, d.ID); !strings.HasPrefix(name, "Default") {
			def.detail = name
		}
	}
	all := []pickEntry{def}
	var groups []string
	for _, md := range l.models {
		if md.Provider != "" && !slices.Contains(groups, md.Provider) {
			groups = append(groups, md.Provider)
		}
	}
	add := func(md agent.Model, group string) {
		if md.IsDefault {
			return // Default's, above
		}
		all = append(all, pickEntry{id: md.ID, label: cmp.Or(md.DisplayName, md.ID), detail: md.ID, group: group})
	}
	if len(groups) < 2 {
		for _, md := range l.models {
			add(md, "")
		}
	} else {
		for _, g := range groups {
			for _, md := range l.models {
				if md.Provider == g {
					add(md, g)
				}
			}
		}
	}
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return all
	}
	// Every word typed, anywhere in its name, ID or group.
	return slices.DeleteFunc(all, func(e pickEntry) bool {
		hay := strings.ToLower(e.label + " " + e.id + " " + e.group + " " + e.detail)
		return slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(hay, w) })
	})
}

// modelPickerView is the model button, and, open, the picker above it:
// a search field and the models, which the arrows walk and ↵ chooses.
func (t *Chat) modelPickerView(c *ui.Context, button ui.Element) {
	p := &t.picker
	if button.Clicked() {
		p.open = !p.open
		p.query, p.hi, p.moved = "", 0, true
	}
	if !p.open {
		return
	}
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	l := t.models()
	entries := t.pickEntries(l, p.query)
	p.hi = max(0, min(p.hi, len(entries)-1))
	ui.Overlay(c, func() {
		panel := ui.Column(c.Key("model-picker")).AttachTo(button, ui.AnchorTopLeft, ui.AnchorBottomLeft).
			Width(340).Margin(0, 0, 6, 0).Padding(6).Gap(4).Radius(th.Radius+2).Background(th.Background).Border(1, th.Border).
			Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.18)).Role(ui.RoleMenu).Label("Models")
		panel.Children(func() {
			search := ui.TextInput(c.Key("model-search"), &p.query).Placeholder("Search models").Label("Search models").AutoFocus().FillWidth()
			if search.Changed() {
				p.hi, p.moved = 0, true
			}
			search.HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind != ui.InputKeyDown {
					return false
				}
				switch ev.Key {
				case ui.KeyDown:
					p.hi, p.moved = min(p.hi+1, len(entries)-1), true
				case ui.KeyUp:
					p.hi, p.moved = max(p.hi-1, 0), true
				case ui.KeyEnter:
					if p.hi < len(entries) {
						t.pick(entries[p.hi])
					}
				default:
					return false
				}
				return true
			})
			list := ui.Scroll(c.Key("model-list")).MaxHeight(pickerHeight).FillWidth()
			list.Children(func() {
				ui.Column(c).Gap(1).Children(func() {
					group := ""
					for i, e := range entries {
						if e.group != group {
							group = e.group
							ui.Text(c, group).FontSize(11).Bold().TextColor(pal.Muted).Padding(8, 8, 2, 8)
						}
						t.pickRow(c, e, i == p.hi)
					}
					switch {
					case l.loading:
						ui.Text(c, "Listing the models…").FontSize(12).TextColor(pal.Muted).Padding(6, 8)
					case l.err != "":
						ui.Text(c, "Not listed: "+widgets.OneLine(l.err, 120)).FontSize(12).TextColor(th.Danger).Padding(6, 8).Tooltip(l.err)
						if ui.Button(c, "Try Again").Clicked() {
							forgetModels(t.provider)
						}
					case len(entries) == 0:
						ui.Text(c, "No model matches.").FontSize(12).TextColor(pal.Muted).Padding(6, 8)
					}
				})
			})
		})
		p.moved = false
		if panel.OverlayShortcut(0, ui.KeyEscape) || panel.PressedOutside() {
			p.open = false
		}
	})
}

// pickRow is a model of the picker: its name, its ID, and a check on the
// one in use.
func (t *Chat) pickRow(c *ui.Context, e pickEntry, hi bool) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	row := ui.ButtonBase(c.Key("model-"+e.id)).Padding(5, 8).Radius(6).Label(e.label)
	if hi || row.Hovered() {
		row.Background(th.Accent.Alpha(0.12))
	}
	if hi && t.picker.moved {
		row.ScrollIntoView()
	}
	row.Children(func() {
		ui.Row(c).Gap(8).Grow(1).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				ui.Text(c, e.label).FontSize(12.5).SingleLine()
				if e.detail != "" && e.detail != e.label {
					ui.Text(c, e.detail).FontSize(11).TextColor(pal.Muted).SingleLine()
				}
			})
			if e.id == t.modelID {
				ui.Icon(c, widgets.IconCheck).FontSize(12).TextColor(th.Accent)
			}
		})
	})
	if row.Clicked() {
		t.pick(e)
	}
}

// pick chooses a model of the picker, and closes it.
func (t *Chat) pick(e pickEntry) {
	t.picker.open = false
	t.chooseModel(e.id, t.effort)
	t.wantFocus = true
}

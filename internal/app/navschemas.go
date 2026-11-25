package app

import (
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The schemas the navigator shows of a database are the ones the user
// ticked, all of them until they tick some: on a server of many schemas,
// the three worked in need not be found among the others each time, as
// DataGrip's "3 of 12" and DBeaver's "Show only". The choice is kept on
// this computer, by connection and database: one person's clutter is not
// another's, so it is not in the project's shared file.

// shownSchemasFile keeps the choices, in the project's local state.
const shownSchemasFile = "shown-schemas.json"

// schemaChoiceKey names a connection's database among the choices.
func schemaChoiceKey(cn *connection.Conn, database string) string {
	return strings.TrimPrefix(cn.Config.ID, cn.Project.Prefix) + "/" + database
}

// schemaChoices are a project's choices, read once and kept.
func (a *App) schemaChoices(p *project.Project) map[string][]string {
	if choices, ok := a.nav.schemas[p]; ok {
		return choices
	}
	choices := map[string][]string{}
	if p.Local != nil {
		p.Local.LoadJSON(shownSchemasFile, &choices)
	}
	if a.nav.schemas == nil {
		a.nav.schemas = map[*project.Project]map[string][]string{}
	}
	a.nav.schemas[p] = choices
	return choices
}

// shownSchemas are the schemas of a database the navigator shows, in the
// server's order: those chosen that are still there, or all of them when
// none is chosen or none of those chosen is left.
func (a *App) shownSchemas(cn *connection.Conn, database string) []string {
	all := cn.Schemas[database]
	chosen := a.schemaChoices(cn.Project)[schemaChoiceKey(cn, database)]
	if len(chosen) == 0 {
		return all
	}
	var shown []string
	for _, s := range all {
		if slices.Contains(chosen, s) {
			shown = append(shown, s)
		}
	}
	if shown == nil {
		return all
	}
	return shown
}

// showSchemas keeps the schemas to show of a connection's database; all
// of them, or none, forgets the choice.
func (a *App) showSchemas(cn *connection.Conn, database string, schemas []string) error {
	choices := a.schemaChoices(cn.Project)
	key := schemaChoiceKey(cn, database)
	if len(schemas) == 0 || len(schemas) == len(cn.Schemas[database]) {
		delete(choices, key)
	} else {
		choices[key] = slices.Clone(schemas)
	}
	if cn.Project.Local == nil {
		return nil // kept for the session
	}
	if err := cn.Project.Local.SaveJSON(shownSchemasFile, choices); err != nil {
		return fmt.Errorf("could not keep the schemas chosen: %w", err)
	}
	return nil
}

// schemaCount says how many of a database's schemas show, as "3 of 12",
// "" when all do or there is one alone.
func (a *App) schemaCount(cn *connection.Conn, database string) string {
	all := len(cn.Schemas[database])
	if shown := len(a.shownSchemas(cn, database)); all > 1 && shown < all {
		return fmt.Sprintf("%d of %d", shown, all)
	}
	return ""
}

// schemaPicker is the dialog choosing the schemas to show.
type schemaPicker struct {
	open     bool
	cn       *connection.Conn
	database string
	filter   string
	ticked   map[string]bool
	err      string
	dismiss  widgets.DismissGuard
}

// openSchemaPicker asks which schemas of a connection's database to show.
func (a *App) openSchemaPicker(cn *connection.Conn, database string) {
	p := &schemaPicker{open: true, cn: cn, database: database, ticked: map[string]bool{}}
	for _, s := range a.shownSchemas(cn, database) {
		p.ticked[s] = true
	}
	a.schemaPicker = p
}

func (a *App) schemaPickerView(c *ui.Context) {
	p := a.schemaPicker
	pal := widgets.PaletteOf(c)
	all := p.cn.Schemas[p.database]
	var ticked []string
	for _, s := range all {
		if p.ticked[s] {
			ticked = append(ticked, s)
		}
	}
	dlg := widgets.Modal(c, &p.open, widgets.DialogSmall, func() {
		ui.Column(c).Gap(10).Children(func() {
			widgets.DialogTitle(c, "Schemas of "+p.cn.Config.Name)
			ui.Text(c, "The navigator and its filter show the schemas ticked. The others are untouched on the server: tick them here to see them again.").
				FontSize(widgets.TextSmall).TextColor(pal.Muted)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				widgets.SearchBox(c, &p.filter, "Find a schema", 0).AutoFocus()
				if ui.Button(c, "All").Clicked() {
					for _, s := range all {
						p.ticked[s] = true
					}
				}
				if ui.Button(c, "None").Clicked() {
					clear(p.ticked)
				}
			})
			needle := strings.ToLower(strings.TrimSpace(p.filter))
			ui.Scroll(c).MaxHeight(320).Children(func() {
				ui.Column(c).Gap(2).Children(func() {
					for _, s := range all {
						if needle != "" && !strings.Contains(strings.ToLower(s), needle) {
							continue
						}
						on := p.ticked[s]
						if ui.Checkbox(c.Key("schema-"+s), &on, s).Changed() {
							p.ticked[s] = on
						}
					}
				})
			})
			ui.Text(c, fmt.Sprintf("%d of %d ticked", len(ticked), len(all))).FontSize(widgets.TextSmall).TextColor(pal.Muted)
			if p.err != "" {
				ui.Text(c, p.err).FontSize(widgets.TextSmall).TextColor(c.Theme().Danger).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					p.open = false
				}
				why := widgets.When(len(ticked) == 0, "Tick at least one schema")
				if widgets.ActivatedUnless(c, ui.PrimaryButton(c, "Show"), why) {
					if err := a.showSchemas(p.cn, p.database, ticked); err != nil {
						p.err = err.Error()
					} else {
						p.open = false
					}
				}
			})
		})
	})
	p.dismiss.Keep(c, dlg, &p.open, strings.Join(ticked, "\x00"), false)
	if !p.open {
		a.schemaPicker = nil
	}
}

// schemaCountButton says on a connection's or a database's row how many
// of its schemas show, once some are hidden, and opens the dialog that
// chooses them: what is hidden is one press from being shown.
func (a *App) schemaCountButton(c *ui.Context, cn *connection.Conn, database string) {
	count := a.schemaCount(cn, database)
	if count == "" {
		return
	}
	b := widgets.Pill(c.Key("schemas-"+database), count, false).Padding(0, 5).Label("Schemas shown: " + count).
		Tooltip("Showing " + count + " schemas: choose which")
	if b.Clicked() {
		a.openSchemaPicker(cn, database)
	}
}

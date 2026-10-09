package app

import (
	"strings"

	"dgopher/internal/project"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// snippetForm names the SQL of an editor to keep it as a snippet of its
// project.
type snippetForm struct {
	open    bool
	project *project.Project
	name    string
	keyword string
	sql     string
	err     string
}

// AskSnippet keeps the selection of an editor, or its statement at the
// caret, as a snippet.
func (a *App) AskSnippet(q *query.Tab) {
	text := q.Editor.Selection()
	if text == "" {
		if st, ok := sqltext.StatementAtWith(q.Editor.Text, q.Editor.SelEnd, q.Editor.Dialect, query.SplitOptions(a.Settings())); ok {
			text = st.Text
		}
	}
	if strings.TrimSpace(text) == "" {
		a.ShowError("Nothing to keep", "Select some SQL, or put the caret in a statement.")
		return
	}
	a.snippetForm = &snippetForm{open: true, project: q.Conn.Project, sql: text, name: widgets.OneLine(text, 40)}
}

func (a *App) snippetFormView(c *ui.Context) {
	f := a.snippetForm
	pal := widgets.PaletteOf(c)
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(520).Gap(10).Children(func() {
			ui.Text(c, "Save as Snippet").FontSize(15).Bold()
			submit := ui.TextInput(c, &f.name).AutoFocus().Label("Name").Placeholder("Name").Submitted()
			ui.TextInput(c, &f.keyword).Label("Keyword").Placeholder("Keyword that completes to it, as recent").Font(widgets.MonoFont)
			ui.Scroll(c).MaxHeight(200).Radius(8).Background(pal.EditorBg).Children(func() {
				ui.Text(c, f.sql).Font(widgets.MonoFont).FontSize(12).Padding(10)
			})
			ui.Text(c, "Saved in "+project.File+" of "+f.project.Name+", to share with the project; inserted from the command palette into any of its editors, or by typing its keyword. Fields to fill in are written ${1:default}, $2, and $0 where the caret ends; Tab goes from one to the next.").FontSize(12).TextColor(pal.Muted)
			if f.err != "" {
				ui.Text(c, f.err).FontSize(12).TextColor(c.Theme().Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if (ui.PrimaryButton(c, "Save").Clicked() || submit) && strings.TrimSpace(f.name) != "" {
					keyword := strings.TrimSpace(f.keyword)
					if strings.ContainsFunc(keyword, func(r rune) bool { return !query.IsIdentRune(r) }) {
						f.err = "A keyword is one word: letters, digits and _."
						return
					}
					if err := f.project.Writable(); err != nil {
						a.ShowError("Could not save the snippet", err.Error())
						return
					}
					name := strings.TrimSpace(f.name)
					replaced := false
					p := f.project
					for i := range p.Snippets {
						if p.Snippets[i].Name == name {
							p.Snippets[i].SQL, p.Snippets[i].Keyword, replaced = f.sql, keyword, true
						}
					}
					if !replaced {
						p.Snippets = append(p.Snippets, project.Snippet{Name: name, SQL: f.sql, Keyword: keyword})
					}
					a.saveProject(p)
					f.open = false
					a.toast = &pendingToast{text: "Saved snippet " + name}
				}
			})
		})
	})
	if !f.open {
		a.snippetForm = nil
	}
}

// insertSnippet puts a snippet's SQL at the caret of the editor in front,
// or opens an editor with it.
func (a *App) insertSnippet(s project.Snippet) {
	if q, ok := a.ActiveTab().(*query.Tab); ok {
		q.InsertSnippet(s.SQL)
		return
	}
	if cn := a.activeConn(); cn != nil && cn.Config.Engine.IsSQL() {
		a.newQueryFile(cn, "", "", func(q *query.Tab) { q.InsertSnippet(s.SQL) })
		return
	}
	a.WriteClipboard(query.SnippetText(s.SQL))
	a.toast = &pendingToast{text: "Copied " + s.Name + ": no editor is open"}
}

// snippetItems lists the snippets in the command palette.
func (a *App) snippetItems() []paletteItem {
	var out []paletteItem
	p := a.currentProject()
	if p == nil {
		return nil
	}
	for _, s := range p.Snippets {
		s := s
		out = append(out, paletteItem{title: s.Name, detail: widgets.OneLine(s.SQL, 60), group: "Snippet", icon: widgets.IconCode, run: func() { a.insertSnippet(s) }})
	}
	return out
}

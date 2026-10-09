package query

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// identifier is a name in the text: what it names, the name before its
// dot, and where it is. keyword is set for a name spelled as a keyword,
// as a column named status: only then are keywords taken for names.
type identifier struct {
	name, qualifier string
	keyword         bool
	start, end      int // runes
}

func tokenName(t sqltext.Token, d sqltext.Dialect) string {
	if t.Kind == sqltext.QuotedIdent {
		return sqltext.DecodeQuoted(t.Text, d)
	}
	return t.Text
}

// identifierAt is the name the caret is in or right after.
func identifierAt(text string, caret int, d sqltext.Dialect) (identifier, bool) {
	toks := sqltext.Tokenize(text, d)
	for i, t := range toks {
		if caret < t.Start || caret > t.End || t.Kind != sqltext.Identifier && t.Kind != sqltext.QuotedIdent && t.Kind != sqltext.Keyword {
			continue
		}
		id := identifier{name: tokenName(t, d), keyword: t.Kind == sqltext.Keyword, start: t.Start, end: t.End}
		if i >= 2 && toks[i-1].Text == "." {
			id.qualifier = tokenName(toks[i-2], d)
		}
		return id, true
	}
	return identifier{}, false
}

// occurrences are where a text names what id does, ignoring case, quoted
// or not; keywords count only for a name spelled as one.
func occurrences(text string, id identifier, d sqltext.Dialect) [][2]int {
	var out [][2]int
	for _, t := range sqltext.Tokenize(text, d) {
		named := t.Kind == sqltext.Identifier || t.Kind == sqltext.QuotedIdent || t.Kind == sqltext.Keyword && id.keyword
		if named && strings.EqualFold(tokenName(t, d), id.name) {
			out = append(out, [2]int{t.Start, t.End})
		}
	}
	return out
}

// GoTo puts the caret at a rune offset, in view.
func (q *Tab) GoTo(at int) {
	q.Editor.PendingSel = &[2]int{at, at}
	q.Editor.WantFocus = true
	q.reveal(at, q.a.Settings().EditorFont)
}

// goToDefinition opens what the name at the caret names: a table's or a
// view's structure, the table of a column or an alias, or a routine's
// definition.
func (q *Tab) goToDefinition() {
	e := &q.Editor
	id, ok := identifierAt(e.Text, e.SelEnd, e.Dialect)
	if !ok {
		q.note("Put the caret in a name to go to what it names.", "", true)
		return
	}
	cn := q.Conn
	if cn.Status != connection.StatusConnected {
		q.note("Connect to go to "+id.name+".", "", true)
		return
	}
	cc := sqltext.CompletionAt(e.Text, id.start, e.Dialect)
	tableOf := func(name string) (sqltext.TableRef, bool) {
		for _, t := range cc.Tables {
			if strings.EqualFold(t.Alias, name) || strings.EqualFold(t.Name, name) {
				return t, true
			}
		}
		return sqltext.TableRef{}, false
	}
	// reading is set when a schema or a table the name may be in is not
	// read yet: it is being read, for the next try.
	reading := false
	open := func(schema, name string) bool {
		if schema == "" {
			schema = q.currentSchema()
		}
		key := connection.SchemaKey{Database: q.Database, Schema: schema}
		objs, ok := cn.Objects[key]
		if !ok && cn.LoadErr[key] == "" {
			connection.LoadObjects(q.a, cn, q.Database, schema)
			reading = true
		}
		i := slices.IndexFunc(objs, func(o db.Object) bool { return strings.EqualFold(o.Name, name) })
		if i < 0 {
			return false
		}
		q.a.OpenTable(cn, q.Database, objs[i], dataview.PageStructure)
		return true
	}
	hasColumn := func(t sqltext.TableRef, column string) bool {
		schema := t.Schema
		if schema == "" {
			schema = q.currentSchema()
		}
		cols, ok := cn.Columns[connection.ObjectKey{Database: q.Database, Schema: schema, Name: t.Name}]
		if !ok {
			// Asked for, they are read whatever the catalog's depth.
			connection.LoadColumns(q.a, cn, q.Database, schema, t.Name, nil)
			reading = true
		}
		return slices.ContainsFunc(cols, func(c db.Column) bool { return strings.EqualFold(c.Name, column) })
	}
	if id.qualifier != "" {
		// t.column, or schema.table.
		if t, ok := tableOf(id.qualifier); ok && open(t.Schema, t.Name) || open(id.qualifier, id.name) || open("", id.qualifier) {
			return
		}
	} else {
		if t, ok := tableOf(id.name); ok && open(t.Schema, t.Name) || open("", id.name) {
			return
		}
		items := cn.Items[connection.SchemaKey{Database: q.Database, Schema: q.currentSchema()}]
		if i := slices.IndexFunc(items, func(it db.Item) bool { return strings.EqualFold(it.Name, id.name) }); i >= 0 {
			dataview.OpenItemDefinition(q.a, cn, q.Database, items[i])
			return
		}
		for _, t := range cc.Tables {
			if hasColumn(t, id.name) && open(t.Schema, t.Name) {
				return // a column of a table of the statement
			}
		}
	}
	if reading {
		q.note("Reading the catalog to find "+id.name+": go to it again in a moment.", "", false)
		return
	}
	q.note("Found nothing named "+id.name+" in "+q.currentSchema()+": the navigator's Refresh reads the schema again.", "", true)
}

// usage is a place a name is used: a file, the line, and its text.
type usage struct {
	path  string // "" for the editor's own text
	file  string // its name as shown
	at    int    // rune offset
	line  int    // 1-based
	text  string
	match [2]int // the name within text, in runes
}

// usagesDialog lists where a name is used, to go to one.
type usagesDialog struct {
	open   bool
	q      *Tab
	name   string
	usages []usage
	sel    int
	list   ui.ListState
}

// usagesIn lists where a text names what id does.
func usagesIn(text string, id identifier, path, file string, d sqltext.Dialect) []usage {
	var out []usage
	runes := []rune(text)
	for _, o := range occurrences(text, id, d) {
		lineStart := o[0]
		for lineStart > 0 && runes[lineStart-1] != '\n' {
			lineStart--
		}
		lineEnd := o[1]
		for lineEnd < len(runes) && runes[lineEnd] != '\n' {
			lineEnd++
		}
		line := string(runes[lineStart:lineEnd])
		trimmed := strings.TrimLeft(line, " \t")
		cut := len([]rune(line)) - len([]rune(trimmed))
		out = append(out, usage{path: path, file: file, at: o[0], line: strings.Count(string(runes[:o[0]]), "\n") + 1,
			text: trimmed, match: [2]int{o[0] - lineStart - cut, o[1] - lineStart - cut}})
	}
	return out
}

// findUsages lists where the name at the caret is used: in the editor,
// then in the other query files of its project.
func (q *Tab) findUsages() {
	e := &q.Editor
	id, ok := identifierAt(e.Text, e.SelEnd, e.Dialect)
	if !ok {
		q.note("Put the caret in a name to find where it is used.", "", true)
		return
	}
	d := &usagesDialog{open: true, q: q, name: id.name}
	d.usages = usagesIn(e.Text, id, "", q.Name, e.Dialect)
	p := q.Conn.Project
	for _, rel := range p.Files {
		path := filepath.Join(p.Queries, filepath.FromSlash(rel))
		if path == q.Path {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue // gone since the folder was read
		}
		d.usages = append(d.usages, usagesIn(string(data), id, path, rel, e.Dialect)...)
	}
	d.list.Selected = &d.sel
	q.a.QueryDialogs().usages = d
}

func usagesView(a Host, c *ui.Context) {
	d := a.QueryDialogs().usages
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	jump := func(i int) {
		if i < 0 || i >= len(d.usages) {
			return
		}
		u := d.usages[i]
		d.open = false
		if u.path == "" {
			d.q.GoTo(u.at)
			return
		}
		a.OpenQueryFile(u.path, u.at)
	}
	ui.DialogBase(c, &d.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.25)).Justify(ui.Start).PaddingY(80)
		panel.Width(720).Radius(12).Background(th.Background).Border(1, th.Border).Clip().Label("Usages")
		ui.Row(c).Padding(12, 16).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, d.name).Font(widgets.MonoFont).Bold()
			files := map[string]bool{}
			for _, u := range d.usages {
				files[u.file] = true
			}
			ui.Text(c, fmt.Sprintf("used %d time%s, in %d file%s", len(d.usages), widgets.Plural(len(d.usages)), len(files), widgets.Plural(len(files)))).
				FontSize(12.5).TextColor(pal.Muted)
		})
		list := ui.List(c, &d.list, len(d.usages), func(i int) {
			u := d.usages[i]
			row := ui.Row(c).Padding(6, 16).Gap(10)
			if i == d.sel {
				row.Background(th.Accent.Alpha(0.15))
			}
			row.Children(func() {
				ui.Text(c, fmt.Sprintf("%s:%d", u.file, u.line)).FontSize(11.5).TextColor(pal.Muted).Width(180).SingleLine()
				runes := []rune(u.text)
				ui.Text(c, string(runes)).Font(widgets.MonoFont).FontSize(12.5).SingleLine().Grow(1).Shrink(1).
					TextRanges(ui.TextRange{Start: u.match[0], End: u.match[1], Color: th.Accent, Weight: 700})
			})
		}).MaxHeight(440).Children(func() {
			if len(d.usages) == 0 {
				ui.Text(c, "Not used in the project's query files.").TextColor(pal.Muted).Padding(14)
			}
		})
		if list.Submitted() {
			jump(d.sel)
		}
	})
	if !d.open {
		a.QueryDialogs().usages = nil
	}
}

// renameForm renames a name everywhere in the editor's text.
type renameForm struct {
	open bool
	q    *Tab
	id   identifier
	name string
	err  string
}

func (q *Tab) askRename() {
	e := &q.Editor
	id, ok := identifierAt(e.Text, e.SelEnd, e.Dialect)
	if !ok {
		q.note("Put the caret in a name to rename it.", "", true)
		return
	}
	q.a.QueryDialogs().rename = &renameForm{open: true, q: q, id: id, name: id.name}
}

// renameInText renames every place a text names what id names, quoting
// the new name where it needs it.
func renameInText(text string, id identifier, name string, d sqltext.Dialect, quote func(string) string) string {
	written := name
	if needsQuote(name) {
		written = quote(name)
	}
	runes := []rune(text)
	occ := occurrences(text, id, d)
	for i := len(occ) - 1; i >= 0; i-- {
		runes = append(runes[:occ[i][0]], append([]rune(written), runes[occ[i][1]:]...)...)
	}
	return string(runes)
}

func renameView(a Host, c *ui.Context) {
	f := a.QueryDialogs().rename
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	q := f.q
	n := len(occurrences(q.Editor.Text, f.id, q.Editor.Dialect))
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(440).Gap(10).Children(func() {
			ui.Text(c, "Rename "+f.id.name).FontSize(15).Bold()
			submit := ui.TextInput(c, &f.name).AutoFocus().Label("New name").Font(widgets.MonoFont).Submitted()
			ui.Text(c, fmt.Sprintf("Renames its %d place%s in %s, not in the database: the navigator's Rename renames there.", n, widgets.Plural(n), q.Name)).
				FontSize(12).TextColor(pal.Muted)
			if f.err != "" {
				ui.Text(c, f.err).FontSize(12).TextColor(th.Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if ui.PrimaryButton(c, "Rename").Clicked() || submit {
					name := strings.TrimSpace(f.name)
					if name == "" {
						f.err = "A name may not be empty."
						return
					}
					q.Editor.Text = renameInText(q.Editor.Text, f.id, name, q.Editor.Dialect, db.DialectOf(q.Conn.Config.Engine).Quote)
					q.ac.lastText = q.Editor.Text
					f.open = false
				}
			})
		})
	})
	if !f.open {
		a.QueryDialogs().rename = nil
	}
}

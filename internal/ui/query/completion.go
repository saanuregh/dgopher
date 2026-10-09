package query

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/project"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// suggestion is an item of the completion popup.
type suggestion struct {
	text   string // what is inserted: a snippet's SQL, with its fields
	label  string
	detail string // its type or kind
	kind   string // "table", "view", "column", "keyword", "schema", "function", "snippet" or "connection"
}

// completion is the state of the SQL editor's completion popup.
type completion struct {
	open     bool
	items    []suggestion
	index    int
	start    int // the rune offset the inserted text replaces from
	lastText string
	// retry asks again once the schema being read arrives.
	retry bool
	// header is set while the popup completes the connection a header
	// line names, whose IDs hold more than an identifier's characters.
	header bool
	list   ui.ListState
	row    int
}

// IsIdentRune reports whether a rune may be part of a word completion
// completes, as a name or a snippet's keyword.
func IsIdentRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// completionView updates the popup after typing and builds it.
func (q *Tab) completionView(c *ui.Context, a Host) {
	ac := &q.ac
	e := &q.Editor
	if e.Text != ac.lastText {
		// Typing adds a few characters on one line where the caret is; a
		// long paste or the app replacing the text does not open the popup.
		grew := utf8.RuneCountInString(e.Text) - utf8.RuneCountInString(ac.lastText)
		typed := e.HasFocus && e.Typing() && grew >= 1 && grew <= 40 &&
			strings.Count(e.Text, "\n") == strings.Count(ac.lastText, "\n")
		ac.lastText = e.Text
		if typed {
			q.suggest(a, false)
		} else if ac.open {
			q.suggest(a, true)
		}
	}
	if q.pressed(c, keymap.Complete) {
		q.suggest(a, true)
	}
	if ac.retry && len(q.Conn.Loading) == 0 {
		ac.retry = false
		if e.HasFocus {
			q.suggest(a, true)
		}
	}
	if ac.open && !q.caretInWord() {
		ac.open = false // the caret left the word being completed
	}
	if !ac.open || len(ac.items) == 0 {
		return
	}
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	x, y := e.CaretXY(a.Settings().EditorFont)
	x -= e.Scroll.X
	y -= e.Scroll.Y
	ui.Box(c).Absolute().Left(max(0, x-8)).Top(y+4).Width(380).MaxHeight(260).
		Radius(8).Background(t.Background).Border(1, t.Border).Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.18)).Clip().
		Children(func() {
			ac.row = ac.index
			ac.list.Selected = &ac.row
			ui.List(c, &ac.list, len(ac.items), func(i int) {
				it := ac.items[i]
				row := ui.Row(c).Padding(3, 10).Gap(8)
				if i == ac.index {
					row.Background(t.Accent)
				}
				row.Children(func() {
					col := pal.Muted
					if i == ac.index {
						col = t.AccentText
					}
					ui.Text(c, kindGlyph(it.kind)).FontSize(11).TextColor(col).Width(14)
					lbl := ui.Text(c, it.label).Font(widgets.MonoFont).FontSize(12.5).SingleLine().Grow(1).Shrink(1)
					if i == ac.index {
						lbl.TextColor(t.AccentText)
					}
					ui.Text(c, it.detail).FontSize(11).TextColor(col).SingleLine().Shrink(1)
				})
				if row.Clicked() {
					ac.index = i
					q.accept()
				}
			}).MaxHeight(260)
		})
}

// completionKey takes the keys of the popup while it shows, before the
// editor does: it runs as the key comes, between frames.
func (q *Tab) completionKey(mods ui.Modifiers, key ui.Key) bool {
	ac := &q.ac
	if !ac.open || len(ac.items) == 0 || mods != 0 {
		return false
	}
	switch key {
	case ui.KeyDown:
		ac.index = (ac.index + 1) % len(ac.items)
		ac.list.ScrollIntoView(ac.index)
	case ui.KeyUp:
		ac.index = (ac.index + len(ac.items) - 1) % len(ac.items)
		ac.list.ScrollIntoView(ac.index)
	case ui.KeyEnter, ui.KeyTab:
		q.accept()
	case ui.KeyEscape:
		ac.open = false
	default:
		return false
	}
	return true
}

func kindGlyph(kind string) string {
	switch kind {
	case "table":
		return "T"
	case "view":
		return "V"
	case "column":
		return "c"
	case "schema":
		return "S"
	case "connection":
		return "C"
	case "function":
		return "ƒ"
	case "snippet":
		return "≡"
	}
	return "k"
}

func (q *Tab) accept() {
	ac := &q.ac
	if ac.index < 0 || ac.index >= len(ac.items) {
		ac.open = false
		return
	}
	if !q.caretInWord() {
		ac.open = false
		return
	}
	it := ac.items[ac.index]
	ac.open = false
	switch it.kind {
	case "snippet":
		q.insertSnippetAt(ac.start, q.Editor.SelEnd, it.text)
		return
	case "function":
		// The caret goes between the parentheses, unless they follow.
		runes := []rune(q.Editor.Text)
		end := min(q.Editor.SelEnd, len(runes))
		if end < len(runes) && runes[end] == '(' {
			q.Editor.Replace(ac.start, end, it.text)
		} else {
			q.Editor.Replace(ac.start, end, it.text+"()")
			at := ac.start + utf8.RuneCountInString(it.text) + 1
			q.Editor.PendingSel = &[2]int{at, at}
		}
	default:
		q.Editor.Replace(ac.start, q.Editor.SelEnd, it.text)
	}
	ac.lastText = q.Editor.Text
}

// suggest finds what may complete the word at the caret. With force, it
// shows even before a letter is typed.
func (q *Tab) suggest(a Host, force bool) {
	ac := &q.ac
	e := &q.Editor
	caret := e.SelEnd
	runes := []rune(e.Text)
	if caret > len(runes) {
		caret = len(runes)
	}
	if start, typed, ok := project.HeaderAt(e.Text, caret); ok {
		ac.start, ac.header, ac.retry = start, true, false
		ac.items, ac.index = filterSuggestions(q.connectionSuggestions(a), typed), 0
		ac.open = len(ac.items) > 0
		return
	}
	ac.header = false
	if !force {
		if caret == 0 || !(IsIdentRune(runes[caret-1]) || runes[caret-1] == '.') {
			ac.open = false
			return
		}
	}
	d := e.Dialect
	cc := sqltext.CompletionAt(e.Text, caret, d)
	ac.start = cc.PrefixStart
	ac.retry = false
	if !force && cc.Prefix == "" && cc.Qualifier == "" {
		ac.open = false
		return
	}
	out := filterSuggestions(q.candidates(a, cc), cc.Prefix)
	ac.items, ac.index, ac.open = out, 0, len(out) > 0
}

// filterSuggestions keeps the items whose label starts with what is
// typed, then those holding it, at most 60.
func filterSuggestions(items []suggestion, typed string) []suggestion {
	prefix := strings.ToLower(typed)
	var starts, contains []suggestion
	for _, it := range items {
		l := strings.ToLower(it.label)
		switch {
		case strings.HasPrefix(l, prefix):
			starts = append(starts, it)
		case prefix != "" && strings.Contains(l, prefix):
			contains = append(contains, it)
		}
	}
	out := append(starts, contains...)
	if len(out) > 60 {
		out = out[:60]
	}
	// Nothing to add to a word typed in full, unless it is a snippet's
	// keyword, which completes to more.
	if len(out) == 1 && strings.EqualFold(out[0].label, typed) && out[0].kind != "snippet" {
		out = nil
	}
	return out
}

// connectionSuggestions are the connections of the editor's project a
// query file may name.
func (q *Tab) connectionSuggestions(a Host) []suggestion {
	p := q.Conn.Project
	var out []suggestion
	for _, cfg := range a.ProjectConfigs(p) {
		if !cfg.Engine.IsSQL() {
			continue
		}
		id := strings.TrimPrefix(cfg.ID, p.Prefix)
		out = append(out, suggestion{text: id, label: id, detail: cfg.Name + " · " + cfg.Engine.Label() + " · " + cfg.Env.Label(), kind: "connection"})
	}
	return out
}

// candidates lists what fits where the caret is.
func (q *Tab) candidates(a Host, cc sqltext.CompletionContext) []suggestion {
	cn := q.Conn
	if cn.DB == nil {
		return nil
	}
	dialect := cn.DB.Dialect
	schema := cn.DefaultSchema
	quote := func(name string) string {
		if needsQuote(name) {
			return dialect.Quote(name)
		}
		return name
	}
	var out []suggestion
	columnsOf := func(tableSchema, table string) {
		if tableSchema == "" {
			tableSchema = schema
		}
		key := connection.ObjectKey{Database: q.Database, Schema: tableSchema, Name: table}
		cols, ok := cn.Columns[key]
		if !ok {
			// One that could not be read is not read again at each key:
			// the navigator's refresh tries again.
			if cn.LoadErr[key] == "" && connection.WantColumns(a, cn, q.Database, tableSchema, table) {
				q.ac.retry = true
			}
			return
		}
		for _, c := range cols {
			out = append(out, suggestion{text: quote(c.Name), label: c.Name, detail: c.Type, kind: "column"})
		}
	}
	objectsOf := func(s string) {
		key := connection.SchemaKey{Database: q.Database, Schema: s}
		objs, ok := cn.Objects[key]
		if !ok {
			if cn.LoadErr[key] == "" {
				connection.LoadObjects(a, cn, q.Database, s)
				q.ac.retry = true
			}
			return
		}
		for _, o := range objs {
			kind := "table"
			if o.Kind == db.KindView || o.Kind == db.KindMaterializedView {
				kind = "view"
			}
			out = append(out, suggestion{text: quote(o.Name), label: o.Name, detail: string(o.Kind), kind: kind})
		}
	}
	if cc.Qualifier != "" {
		for _, t := range cc.Tables {
			if strings.EqualFold(t.Alias, cc.Qualifier) || strings.EqualFold(t.Name, cc.Qualifier) {
				columnsOf(t.Schema, t.Name)
				return out
			}
		}
		if slices.ContainsFunc(cn.Schemas[q.Database], func(s string) bool { return strings.EqualFold(s, cc.Qualifier) }) {
			objectsOf(cc.Qualifier)
			return out
		}
		// A table named without an alias, not in the statement's FROM yet.
		columnsOf("", cc.Qualifier)
		return out
	}
	if cc.WantTable {
		objectsOf(schema)
		for _, s := range cn.Schemas[q.Database] {
			out = append(out, suggestion{text: quote(s) + ".", label: s, detail: "schema", kind: "schema"})
		}
		return out
	}
	for _, t := range cc.Tables {
		columnsOf(t.Schema, t.Name)
	}
	objectsOf(schema)
	out = append(out, q.snippetSuggestions()...)
	if items, ok := cn.Items[connection.SchemaKey{Database: q.Database, Schema: schema}]; ok {
		for _, it := range items {
			if it.Kind == db.ItemFunction || it.Kind == db.ItemProcedure {
				out = append(out, suggestion{text: quote(it.Name), label: it.Name, detail: "(" + it.Detail + ")", kind: "function"})
			}
		}
	} else if cn.ItemsError(q.Database, schema) == "" {
		connection.LoadItems(a, cn, q.Database, schema)
		q.ac.retry = true
	}
	for _, f := range sqltext.Functions(q.Editor.Dialect) {
		out = append(out, suggestion{text: f, label: f, detail: "function", kind: "function"})
	}
	for _, k := range sqltext.Keywords(q.Editor.Dialect) {
		out = append(out, suggestion{text: k, label: k, kind: "keyword"})
	}
	return out
}

// snippetSuggestions are the snippets a keyword completes to: the
// project's, then those every editor has.
func (q *Tab) snippetSuggestions() []suggestion {
	var out []suggestion
	for _, s := range append(slices.Clone(q.Conn.Project.Snippets), builtinSnippets...) {
		if s.Keyword != "" {
			out = append(out, suggestion{text: s.SQL, label: s.Keyword, detail: s.Name, kind: "snippet"})
		}
	}
	return out
}

// needsQuote reports whether an identifier must be quoted.
func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for i, r := range s {
		if r == '_' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return true
	}
	return false
}

// caretInWord reports whether the caret, with nothing selected, is still at
// the end of the word the popup completes: only letters, digits and dots
// between where the word starts and the caret.
func (q *Tab) caretInWord() bool {
	e := &q.Editor
	if e.SelStart != e.SelEnd || e.SelEnd < q.ac.start {
		return false
	}
	runes := []rune(e.Text)
	if e.SelEnd > len(runes) {
		return false
	}
	for _, r := range runes[q.ac.start:e.SelEnd] {
		if q.ac.header && unicode.IsSpace(r) || !q.ac.header && !IsIdentRune(r) && r != '.' {
			return false
		}
	}
	return true
}

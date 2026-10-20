package query

import (
	"cmp"
	"slices"
	"strings"
	"unicode"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/project"
	"dgopher/internal/sqltext"

	"github.com/egoist/mygo/ui"
	code "github.com/egoist/mygo/ui/editor"
)

// suggestion is a completion the editor offers.
type suggestion struct {
	text   string // what is inserted: a snippet's SQL, with its fields
	label  string
	detail string // its type or kind
	kind   string // "table", "view", "column", "keyword", "schema", "function", "snippet" or "connection"
}

// completionKinds are the editor's kinds of the suggestions', which its
// list shows as icons.
var completionKinds = map[string]code.CompletionKind{
	"table": code.CompletionType, "view": code.CompletionType, "column": code.CompletionField,
	"schema": code.CompletionModule, "connection": code.CompletionConstant, "function": code.CompletionFunction,
	"snippet": code.CompletionSnippet, "keyword": code.CompletionKeyword,
}

// completion is what the editor's completions were last asked for: the
// editor filters and ranks them as typing goes on.
type completion struct {
	// items are the suggestions last given the editor, in their order.
	items []suggestion
	// retry asks again once the schema being read arrives, while the
	// completions show or the caret stays at retryAt; explicit as asked.
	retry    bool
	retryAt  int
	explicit bool
	// inQueryFrom is set where a table completed takes an alias, apart
	// from those of tables, the statement's others.
	inQueryFrom bool
	tables      []sqltext.TableRef
}

// IsIdentRune reports whether a rune may be part of a word completion
// completes, as a name or a snippet's keyword.
func IsIdentRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// completionKeys asks for completions by the keymap's key, which the
// editor's own Ctrl+Space does too, and again once the schema they
// wait for is read.
func (q *Tab) completionKeys(c *ui.Context) {
	ac := &q.ac
	e := q.Editor
	if q.pressed(c, keymap.Complete) && e.HasFocus() {
		e.State.Run(code.ShowCompletions)
	}
	if ac.retry && len(q.Conn.Loading) == 0 {
		ac.retry = false
		if e.HasFocus() && (e.State.CompletionsOpen() || e.Caret() == ac.retryAt) {
			q.suggest(e.Caret(), ac.explicit)
		}
	}
}

// complete answers the editor's request for completions.
func (q *Tab) complete(r code.CompletionRequest) {
	q.suggest(r.Offset, r.Explicit)
}

// suggest gives the editor what may complete the word before a byte
// offset, the caret. With explicit, it shows even before a letter is
// typed.
func (q *Tab) suggest(caret int, explicit bool) {
	ac := &q.ac
	e := q.Editor
	text := e.Text()
	caret = max(0, min(caret, len(text)))
	ac.retry, ac.retryAt, ac.explicit = false, caret, explicit
	ac.inQueryFrom, ac.tables, ac.items = false, nil, nil
	if start, _, ok := project.HeaderAt(text, caret); ok {
		q.show(start, q.connectionSuggestions(q.a))
		return
	}
	cc := sqltext.CompletionAt(text, caret, e.Dialect)
	if !explicit && cc.Prefix == "" && cc.Qualifier == "" {
		q.show(-1, nil)
		return
	}
	ac.inQueryFrom, ac.tables = cc.InQueryFrom, cc.Tables
	q.show(cc.PrefixStart, q.candidates(q.a, cc))
}

// show gives the editor suggestions of the word from start to the caret.
func (q *Tab) show(start int, items []suggestion) {
	q.ac.items = items
	out := make([]code.Completion, len(items))
	for i, it := range items {
		out[i] = code.Completion{Label: it.label, Detail: it.detail, Kind: completionKinds[it.kind], Insert: it.text}
	}
	q.Editor.State.ShowCompletions(start, out)
}

// acceptCompletion inserts a completion accepted in the editor, in place
// of the word typed from start to end, the caret, and at the other carets
// with the same word before them: a function with its parentheses, a
// table in FROM or JOIN with an alias, a snippet with its fields. It
// leaves the others to the editor.
func (q *Tab) acceptCompletion(it code.Completion, start, end int) bool {
	e := q.Editor
	text := e.Text()
	ins := cmp.Or(it.Insert, it.Label)
	switch {
	case it.Kind == code.CompletionSnippet:
		expanded, fields := expandSnippet(ins)
		if len(e.State.Selections()) == 1 {
			q.insertSnippetAt(start, end, ins)
			return true
		}
		// At several carets, each copy's first field is selected.
		var first snippetField
		if len(fields) > 0 {
			first = fields[0]
		}
		q.typeAtCarets(start, end, func(int) (string, int, int) { return expanded, first.start, first.end })
	case it.Kind == code.CompletionFunction:
		// The caret goes between the parentheses, unless they follow.
		q.typeAtCarets(start, end, func(after int) (string, int, int) {
			if after < len(text) && text[after] == '(' {
				return ins, len(ins), len(ins)
			}
			return ins + "()", len(ins) + 1, len(ins) + 1
		})
	case it.Kind == code.CompletionType && q.ac.inQueryFrom:
		var taken []string
		for _, t := range q.ac.tables {
			taken = append(taken, t.Name, t.Alias)
		}
		withAlias := ins + " " + sqltext.TableAlias(it.Label, taken, e.Dialect)
		q.typeAtCarets(start, end, func(after int) (string, int, int) {
			if q.aliasFollows(text, after) {
				return ins, len(ins), len(ins)
			}
			return withAlias, len(withAlias), len(withAlias)
		})
	default:
		return false
	}
	return true
}

// typeAtCarets replaces the word typed from start to end, the newest
// caret, with what place gives, and so at every other selection the same
// word is before, as the editor completes; elsewhere it replaces the
// selection. place is given the end of the text replaced, and gives the
// text and the selection to make in it. All is one step of undo.
func (q *Tab) typeAtCarets(start, end int, place func(after int) (text string, selStart, selEnd int)) {
	e := q.Editor
	text := e.Text()
	n, typed := end-start, text[start:end]
	newest := e.State.Selection()
	sels := e.State.Selections()
	slices.SortFunc(sels, func(a, b code.Selection) int { return a.Start() - b.Start() })
	edits := make([]code.Edit, 0, len(sels))
	type made struct {
		anchor, head int // in the text inserted
		newest       bool
	}
	var after []made
	for _, s := range sels {
		from, to := s.Start(), s.End()
		if s.Head-n >= 0 && s.Head <= len(text) && text[s.Head-n:s.Head] == typed {
			from = s.Head - n
		}
		if len(edits) > 0 && from < edits[len(edits)-1].End {
			continue // overlaps the one before
		}
		ins, a, h := place(to)
		edits = append(edits, code.Edit{Start: from, End: to, Text: ins})
		after = append(after, made{a, h, s == newest})
	}
	e.State.Buffer.Edit(edits...)
	out := make([]code.Selection, 0, len(edits))
	var newestAt []code.Selection
	delta := 0
	for i, ed := range edits {
		at := ed.Start + delta
		sel := code.Selection{Anchor: at + after[i].anchor, Head: at + after[i].head}
		delta += len(ed.Text) - (ed.End - ed.Start)
		if after[i].newest {
			newestAt = append(newestAt, sel)
			continue
		}
		out = append(out, sel)
	}
	// The newest last, as it was.
	e.State.SetSelections(append(out, newestAt...)...)
}

// aliasFollows reports whether the table name ending at byte end of text
// has an alias after it already, or goes on: a name, AS, or a dot.
func (q *Tab) aliasFollows(text string, end int) bool {
	for _, t := range sqltext.Tokenize(text[min(end, len(text)):], q.Editor.Dialect) {
		switch {
		case t.Kind == sqltext.Whitespace || t.Kind == sqltext.Comment:
		case sqltext.IsName(t):
			return true
		case t.Kind == sqltext.Keyword:
			return strings.EqualFold(t.Text, "AS")
		default:
			return t.Kind == sqltext.Punct && t.Text == "."
		}
	}
	return false
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
	quote := func(name string) string { return db.QuoteIfNeeded(dialect, name) }
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
		objs, ok := connection.ObjectsOf(a, cn, q.Database, s)
		if !ok {
			if cn.LoadErr[connection.SchemaKey{Database: q.Database, Schema: s}] == "" {
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

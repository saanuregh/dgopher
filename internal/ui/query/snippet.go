package query

import (
	"dgopher/internal/keymap"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"dgopher/internal/project"

	"github.com/egoist/mygo/ui"
)

// builtinSnippets are the snippets every editor completes, by keyword,
// beside its project's.
var builtinSnippets = []project.Snippet{
	{Name: "SELECT rows", Keyword: "sel", SQL: "SELECT ${2:*}\nFROM ${1:table}\nWHERE ${3:condition};$0"},
	{Name: "SELECT count by", Keyword: "selc", SQL: "SELECT ${2:column}, count(*)\nFROM ${1:table}\nGROUP BY ${2:column}\nORDER BY count(*) DESC;$0"},
	{Name: "INSERT a row", Keyword: "ins", SQL: "INSERT INTO ${1:table} (${2:columns})\nVALUES (${3:values});$0"},
	{Name: "UPDATE rows", Keyword: "upd", SQL: "UPDATE ${1:table}\nSET ${2:column} = ${3:value}\nWHERE ${4:condition};$0"},
	{Name: "DELETE rows", Keyword: "del", SQL: "DELETE FROM ${1:table}\nWHERE ${2:condition};$0"},
	{Name: "Join", Keyword: "join", SQL: "JOIN ${1:table} ${2:t} ON ${2:t}.${3:id} = ${4:other}.${5:id}$0"},
}

// snippetField is a field of an inserted snippet, by rune offsets.
type snippetField struct{ start, end int }

// expandSnippet reads a snippet's fields, ${1:default}, $1 and the end
// $0, as VS Code writes them: the text with the defaults in place, and
// the fields in their numbers' order, $0 last. A field written again
// takes the first's default; \$ is a dollar sign.
func expandSnippet(sql string) (string, []snippetField) {
	var b strings.Builder
	type numbered struct {
		n     int
		field snippetField
	}
	var found []numbered
	defaults := map[int]string{}
	at := 0 // runes written
	write := func(s string) {
		b.WriteString(s)
		at += utf8.RuneCountInString(s)
	}
	for i := 0; i < len(sql); i++ {
		if sql[i] == '\\' && i+1 < len(sql) && sql[i+1] == '$' {
			write("$")
			i++
			continue
		}
		if sql[i] != '$' || i+1 >= len(sql) {
			write(sql[i : i+1])
			continue
		}
		n, def, size := -1, "", 0
		switch {
		case sql[i+1] >= '0' && sql[i+1] <= '9':
			j := i + 1
			for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
				j++
			}
			n, _ = strconv.Atoi(sql[i+1 : j])
			size = j - i
		case sql[i+1] == '{':
			end := strings.IndexByte(sql[i:], '}')
			num, text, _ := strings.Cut(sql[i+2:i+max(end, 2)], ":")
			if v, err := strconv.Atoi(num); end > 0 && err == nil {
				n, def, size = v, text, end+1
			}
		}
		if n < 0 {
			write("$")
			continue
		}
		if d, ok := defaults[n]; ok && def == "" {
			def = d
		}
		if _, ok := defaults[n]; !ok {
			defaults[n] = def
		}
		start := at
		write(def)
		found = append(found, numbered{n, snippetField{start, at}})
		i += size - 1
	}
	slices.SortStableFunc(found, func(x, y numbered) int {
		switch {
		case x.n == y.n:
			return 0
		case x.n == 0:
			return 1
		case y.n == 0:
			return -1
		}
		return x.n - y.n
	})
	var fields []snippetField
	seen := map[int]bool{}
	for _, f := range found {
		if !seen[f.n] { // a number written again is filled in at its first place
			seen[f.n] = true
			fields = append(fields, f.field)
		}
	}
	return b.String(), fields
}

// SnippetText is a snippet's SQL with its fields' defaults in place.
func SnippetText(sql string) string {
	text, _ := expandSnippet(sql)
	return text
}

// snippetSession walks the fields of a snippet just inserted: Tab goes
// to the next, Shift+Tab to the one before, and typing in a field moves
// those after it.
type snippetSession struct {
	fields []snippetField
	at     int
	// length is the editor's text in runes when last seen.
	length int
}

// InsertSnippet puts a snippet's SQL in place of the selection, its first
// field chosen.
func (q *Tab) InsertSnippet(sql string) {
	e := &q.Editor
	q.insertSnippetAt(min(e.SelStart, e.SelEnd), max(e.SelStart, e.SelEnd), sql)
	e.WantFocus = true
}

func (q *Tab) insertSnippetAt(start, end int, sql string) {
	text, fields := expandSnippet(sql)
	e := &q.Editor
	e.Replace(start, end, text)
	q.ac.lastText = e.Text
	q.snippet = nil
	if len(fields) == 0 {
		return
	}
	for i := range fields {
		fields[i].start += start
		fields[i].end += start
	}
	q.snippet = &snippetSession{fields: fields, length: utf8.RuneCountInString(e.Text)}
	q.selectField(0)
}

func (q *Tab) selectField(i int) {
	s := q.snippet
	s.at = i
	f := s.fields[i]
	q.Editor.PendingSel = &[2]int{f.start, f.end}
	if i == len(s.fields)-1 && f.start == f.end {
		q.snippet = nil // the end: nothing more to fill in
	}
}

// trackSnippet follows the text typed into the field: the fields after
// it move by what it grew. An edit elsewhere ends the session.
func (q *Tab) trackSnippet() {
	s := q.snippet
	if s == nil {
		return
	}
	e := &q.Editor
	length := utf8.RuneCountInString(e.Text)
	delta := length - s.length
	if delta == 0 {
		return
	}
	s.length = length
	f := &s.fields[s.at]
	oldEnd := f.end
	caret := min(e.SelStart, e.SelEnd)
	if caret < f.start || caret > oldEnd+delta || oldEnd+delta < f.start {
		q.snippet = nil
		return
	}
	f.end += delta
	for i := range s.fields {
		if i != s.at && s.fields[i].start >= oldEnd {
			s.fields[i].start += delta
			s.fields[i].end += delta
		}
	}
}

// snippetKey takes Tab, Shift+Tab and Escape while a snippet's fields are
// being filled in.
func (q *Tab) snippetKey(mods ui.Modifiers, key ui.Key) bool {
	s := q.snippet
	if s == nil {
		return false
	}
	switch {
	case key == ui.KeyTab && mods == 0:
		if s.at+1 < len(s.fields) {
			q.selectField(s.at + 1)
		} else {
			q.snippet = nil
		}
	case key == ui.KeyTab && mods == ui.Shift && s.at > 0:
		q.selectField(s.at - 1)
	case key == ui.KeyEscape && mods == 0:
		q.snippet = nil
	default:
		return false
	}
	return true
}

// editorKey takes the keys of the completion popup, then of a snippet's
// fields, then of the commands on the text, before the editor does.
func (q *Tab) editorKey(mods ui.Modifiers, key ui.Key) bool {
	return q.completionKey(mods, key) || q.snippetKey(mods, key) || q.textKey(mods, key)
}

// textKey takes the keys of the carets and of folding, which the text
// area would take as its own moves before a shortcut could.
func (q *Tab) textKey(mods ui.Modifiers, key ui.Key) bool {
	e := &q.Editor
	switch {
	case e.Vim != nil:
		return false
	case keymap.Is(keymap.Fold, mods, key):
		e.Fold()
	case keymap.Is(keymap.Unfold, mods, key):
		e.Unfold()
	case keymap.Is(keymap.FoldAll, mods, key):
		e.FoldAll()
	case keymap.Is(keymap.UnfoldAll, mods, key):
		e.UnfoldAll()
	case keymap.Is(keymap.CursorAbove, mods, key):
		e.AddCursor(-1)
	case keymap.Is(keymap.CursorBelow, mods, key):
		e.AddCursor(1)
	case keymap.Is(keymap.SelectNext, mods, key):
		e.SelectNext()
	case keymap.Is(keymap.SelectAll, mods, key):
		e.SelectAll()
	default:
		return false
	}
	return true
}

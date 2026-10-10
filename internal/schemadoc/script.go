package schemadoc

import (
	"slices"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/sqltext"
)

// kindOrder is the order a script creates objects in, so that each comes
// after what it needs: types before the tables using them, functions
// before the defaults and views calling them, tables before their views,
// triggers and partitions.
var kindOrder = []string{
	string(db.ItemExtension), string(db.ItemType), string(db.ItemSequence),
	string(db.ItemFunction), string(db.ItemProcedure),
	string(db.KindTable), string(db.KindForeignTable),
	string(db.KindView), string(db.KindMaterializedView), string(db.KindDictionary),
	string(db.ItemTrigger), string(db.ItemEvent), string(db.ItemProjection), string(db.ItemPartition),
}

// statement is an object's part of a script.
type statement struct {
	rank       int
	name, text string
	failed     bool
}

// Script writes the statements creating the objects read, in an order
// that runs: by kind, and within a kind each after the others its
// definition names, as a table after those its foreign keys point at.
// An object that could not be read is a comment saying why.
func Script(s *Schema) string {
	var stmts []statement
	add := func(kind, name, label, def, err string) {
		st := statement{rank: slices.Index(kindOrder, kind), name: name}
		if err != "" {
			st.text, st.failed = comment(label+": "+err), true
		} else {
			st.text = strings.TrimRight(def, " \t\r\n")
			if !strings.HasSuffix(st.text, ";") {
				st.text += ";"
			}
		}
		stmts = append(stmts, st)
	}
	for _, t := range s.Tables {
		add(string(t.Kind), t.Name, t.Name, t.Definition, t.Err)
	}
	functions := false
	for _, it := range s.Items {
		add(string(it.Kind), it.Name, it.Label(), it.Definition, it.Err)
		functions = functions || it.Kind == db.ItemFunction || it.Kind == db.ItemProcedure
	}
	slices.SortStableFunc(stmts, func(a, b statement) int { return a.rank - b.rank })
	stmts = orderByMentions(stmts, safety.Dialect(s.Engine))
	var b strings.Builder
	if functions && s.Engine == db.Postgres {
		// As pg_dump does: a function's body may name a table created
		// after it.
		b.WriteString("SET check_function_bodies = false;\n\n")
	}
	for i, st := range stmts {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(st.text)
	}
	if len(stmts) > 0 {
		b.WriteString("\n")
	}
	return b.String()
}

// orderByMentions moves each statement after the others of its kind
// whose names it holds, keeping the order otherwise, and where they name
// each other in a circle.
func orderByMentions(stmts []statement, d sqltext.Dialect) []statement {
	out := make([]statement, 0, len(stmts))
	for start := 0; start < len(stmts); {
		end := start
		for end < len(stmts) && stmts[end].rank == stmts[start].rank {
			end++
		}
		out = append(out, topological(stmts[start:end], d)...)
		start = end
	}
	return out
}

// topological orders statements of one kind each after those it names.
func topological(group []statement, d sqltext.Dialect) []statement {
	if len(group) < 2 {
		return group
	}
	index := map[string]int{}
	for i, st := range group {
		index[strings.ToLower(st.name)] = i
	}
	needs := make([][]int, len(group))
	for i, st := range group {
		if st.failed {
			continue
		}
		for name := range namesIn(st.text, d) {
			if j, ok := index[name]; ok && j != i && !slices.Contains(needs[i], j) {
				needs[i] = append(needs[i], j)
			}
		}
	}
	out := make([]statement, 0, len(group))
	placed := make([]bool, len(group))
	for len(out) < len(group) {
		progressed := false
		for i := range group {
			if placed[i] || slices.ContainsFunc(needs[i], func(j int) bool { return !placed[j] }) {
				continue
			}
			placed[i], progressed = true, true
			out = append(out, group[i])
		}
		if !progressed {
			// The rest name each other in a circle: the first goes next.
			i := slices.Index(placed, false)
			placed[i] = true
			out = append(out, group[i])
		}
	}
	return out
}

// namesIn is the set of names, in lower case, a statement's identifiers
// spell.
func namesIn(text string, d sqltext.Dialect) map[string]bool {
	names := map[string]bool{}
	for _, tok := range sqltext.Tokenize(text, d) {
		switch tok.Kind {
		case sqltext.Identifier, sqltext.Keyword:
			names[strings.ToLower(tok.Text)] = true
		case sqltext.QuotedIdent:
			names[strings.ToLower(sqltext.DecodeQuoted(tok.Text, d))] = true
		}
	}
	return names
}

// comment turns text into SQL comment lines.
func comment(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, l := range lines {
		lines[i] = sqltext.LineComment(l)
	}
	return strings.Join(lines, "\n")
}

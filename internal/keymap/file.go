package keymap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// FileName is the user's keymap in the config directory.
const FileName = "keymap.json"

// Override is a key the user set in a context: to an action, with its
// argument, or to none (ID "") to take it from the action that has it.
type Override struct {
	Scope Scope
	Keys  Keystroke
	ID    string
	Arg   int
}

// entry is what a key runs in a context.
type entry struct {
	id  string
	arg int
}

// table is the keys as they are: the defaults with the user's overrides
// over them.
type table struct {
	overrides []Override
	bound     map[Scope]map[Keystroke]entry
	byCommand map[string][]Binding
	// sequences are the keystrokes of two chords, in any context; firsts
	// their first chords, each once, which Dispatch claims.
	sequences []Keystroke
	firsts    []Chord
}

var current = build(nil)

// build makes the table of overrides, the later of two of a context's
// key winning.
func build(overrides []Override) *table {
	t := &table{bound: map[Scope]map[Keystroke]entry{}, byCommand: map[string][]Binding{}}
	set := func(s Scope, k Keystroke, e entry) {
		if t.bound[s] == nil {
			t.bound[s] = map[Keystroke]entry{}
		}
		if e.id == "" {
			delete(t.bound[s], k)
		} else {
			t.bound[s][k] = e
		}
	}
	for _, c := range Commands {
		for _, b := range c.Default {
			set(b.Scope, b.Keys, entry{c.ID, b.Arg})
		}
	}
	for _, o := range overrides {
		set(o.Scope, o.Keys, entry{o.ID, o.Arg})
	}
	// An action's keys: its own that are left, in their order, then
	// those the user gave it, in theirs.
	add := func(id string, b Binding) {
		if e, ok := t.bound[b.Scope][b.Keys]; ok && e.id == id && e.arg == b.Arg && !slices.Contains(t.byCommand[id], b) {
			t.byCommand[id] = append(t.byCommand[id], b)
		}
	}
	for _, c := range Commands {
		for _, b := range c.Default {
			add(c.ID, b)
		}
	}
	for _, o := range overrides {
		if o.ID != "" {
			add(o.ID, Binding{Keys: o.Keys, Scope: o.Scope, Arg: o.Arg})
		}
	}
	seen := map[Keystroke]bool{}
	for _, s := range Scopes {
		for k := range t.bound[s] {
			if k.Sequence() && !seen[k] {
				seen[k] = true
				t.sequences = append(t.sequences, k)
			}
		}
	}
	slices.SortFunc(t.sequences, func(a, b Keystroke) int { return strings.Compare(a.String(), b.String()) })
	for _, k := range t.sequences {
		if !slices.Contains(t.firsts, k.First) {
			t.firsts = append(t.firsts, k.First)
		}
	}
	t.overrides = overrides
	return t
}

// Use puts the user's overrides to use.
func Use(overrides []Override) { current = build(slices.Clone(overrides)) }

// Overrides are the user's keys now in use.
func Overrides() []Override { return slices.Clone(current.overrides) }

// SetBindings gives an action exactly the keys of bindings, in its own
// context, as the keys editor sets them; it returns the overrides that
// make it so, to keep, and puts them to use.
func SetBindings(id string, bindings []Binding) []Override {
	cmd, _ := Lookup(id)
	var out []Override
	// What the action had that it no longer has is unbound, and what
	// another action had in the same place is the user's again.
	keep := func(o Override) {
		out = slices.DeleteFunc(out, func(x Override) bool { return x.Scope == o.Scope && x.Keys == o.Keys })
		out = append(out, o)
	}
	for _, o := range current.overrides {
		keep(o)
	}
	for _, b := range Bindings(id) {
		if !slices.ContainsFunc(bindings, func(n Binding) bool { return n.Keys == b.Keys && n.Arg == b.Arg }) {
			keep(Override{Scope: b.Scope, Keys: b.Keys})
		}
	}
	for _, b := range bindings {
		if b.Scope == "" {
			b.Scope = cmd.Scope
		}
		keep(Override{Scope: b.Scope, Keys: b.Keys, ID: id, Arg: b.Arg})
	}
	out = minimize(out)
	Use(out)
	return out
}

// minimize drops the overrides that say what the defaults say.
func minimize(overrides []Override) []Override {
	defaults := build(nil)
	var out []Override
	for i, o := range overrides {
		// Only the last of a place counts.
		if slices.ContainsFunc(overrides[i+1:], func(x Override) bool { return x.Scope == o.Scope && x.Keys == o.Keys }) {
			continue
		}
		e, ok := defaults.bound[o.Scope][o.Keys]
		if ok && e.id == o.ID && e.arg == o.Arg || !ok && o.ID == "" {
			continue
		}
		out = append(out, o)
	}
	return out
}

// Reset gives an action its own keys again, and returns the overrides
// left, to keep.
func Reset(id string) []Override {
	cmd, _ := Lookup(id)
	return SetBindings(id, cmd.Default)
}

// block is a block of keymap.json: the keys of a context.
type block struct {
	Context  string                     `json:"context,omitempty"`
	Bindings map[string]json.RawMessage `json:"bindings"`
}

// Parse reads keymap.json: blocks of a context and its keys, as Zed
// writes them, // comments and trailing commas allowed. A key or action
// that does not read is reported and left out; the rest is used.
func Parse(data []byte) ([]Override, []error) {
	var blocks []block
	if err := json.Unmarshal(relaxJSON(data), &blocks); err != nil {
		return nil, []error{fmt.Errorf("%s does not read: %w", FileName, err)}
	}
	var out []Override
	var errs []error
	for _, b := range blocks {
		scope := Global
		if b.Context != "" {
			i := slices.IndexFunc(Scopes, func(s Scope) bool { return strings.EqualFold(string(s), strings.TrimSpace(b.Context)) })
			if i < 0 {
				errs = append(errs, fmt.Errorf("%q is no context: one of %s", b.Context, strings.Join(scopeNames(), ", ")))
				continue
			}
			scope = Scopes[i]
		}
		for _, written := range sortedKeys(b.Bindings) {
			k, err := ParseKeystroke(written)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", written, err))
				continue
			}
			id, arg, err := parseAction(b.Bindings[written])
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", written, err))
				continue
			}
			if id != "" {
				cmd, ok := Lookup(id)
				switch {
				case !ok:
					errs = append(errs, fmt.Errorf("%s: %q is no action", written, id))
					continue
				case !scope.contains(cmd.Scope) && !cmd.Scope.contains(scope):
					errs = append(errs, fmt.Errorf("%s: %s works in %s, not %s", written, id, cmd.Scope, scope))
					continue
				case cmd.TakesArg && arg == 0:
					errs = append(errs, fmt.Errorf("%s: %s needs an argument, as [%q, 1]", written, id, id))
					continue
				}
			}
			out = append(out, Override{Scope: scope, Keys: k, ID: id, Arg: arg})
		}
	}
	return out, append(errs, prefixErrors(out)...)
}

// prefixErrors reports a key of one chord, set in the file, that begins
// keys of two where both work: the window takes it as the first of the
// two, so it would never run its action.
func prefixErrors(overrides []Override) []error {
	t := build(overrides)
	var errs []error
	for _, o := range overrides {
		if o.ID == "" || o.Keys.Sequence() {
			continue
		}
		if k, ok := t.sequenceFrom(o.Keys.First); ok {
			errs = append(errs, fmt.Errorf("%s: it begins %s, so it would never run %s; the keys of two win", o.Keys, k, o.ID))
		}
	}
	return errs
}

// sequenceFrom is a keystroke of two beginning with first, in any
// context: Dispatch claims the first keys of all of them wherever the
// focus is, so a key of one alone in another context would never run.
func (t *table) sequenceFrom(first Chord) (Keystroke, bool) {
	for _, bound := range t.bound {
		for k := range bound {
			if k.Sequence() && k.First == first {
				return k, true
			}
		}
	}
	return Keystroke{}, false
}

// parseAction reads a binding's action: "name", ["name", n], or null
// (also "none") for no action.
func parseAction(raw json.RawMessage) (string, int, error) {
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		if strings.EqualFold(name, "none") || name == "" {
			return "", 0, nil
		}
		return name, 0, nil
	}
	if string(bytes.TrimSpace(raw)) == "null" {
		return "", 0, nil
	}
	var pair []json.RawMessage
	if err := json.Unmarshal(raw, &pair); err != nil || len(pair) != 2 {
		return "", 0, errors.New(`an action is "name", ["name", argument] or null`)
	}
	if err := json.Unmarshal(pair[0], &name); err != nil {
		return "", 0, errors.New("an action's name is text")
	}
	var arg int
	if err := json.Unmarshal(pair[1], &arg); err != nil || arg < 1 {
		return "", 0, fmt.Errorf("%s: the argument is a number from 1", name)
	}
	return name, arg, nil
}

// FormatFile writes overrides as keymap.json: a block for each context, its
// keys in order.
func FormatFile(overrides []Override) []byte {
	var b strings.Builder
	b.WriteString(fileHeader)
	b.WriteString("[")
	first := true
	for _, s := range Scopes {
		var lines []string
		for _, o := range overrides {
			if o.Scope != s {
				continue
			}
			key, _ := json.Marshal(o.Keys.String())
			var action []byte
			switch {
			case o.ID == "":
				action = []byte("null")
			case o.Arg != 0:
				action = fmt.Appendf(nil, "[%q, %d]", o.ID, o.Arg)
			default:
				action, _ = json.Marshal(o.ID)
			}
			lines = append(lines, fmt.Sprintf("      %s: %s", key, action))
		}
		if len(lines) == 0 {
			continue
		}
		if !first {
			b.WriteString(",")
		}
		first = false
		fmt.Fprintf(&b, "\n  {\n    \"context\": %q,\n    \"bindings\": {\n%s\n    }\n  }", s, strings.Join(lines, ",\n"))
	}
	b.WriteString("\n]\n")
	return []byte(b.String())
}

const fileHeader = `// DGopher's keys, over its own: a block of keys for each context.
// Contexts: Workspace (anywhere), Navigator, Editor, Notebook, Grid, Chat,
// Dialog. Keys as "cmd-shift-f" (cmd is ⌘ on macOS, Ctrl elsewhere), two
// in a row as "cmd-k cmd-s". An action is "namespace::Name",
// ["namespace::Name", n] for one taking an argument, or null to take the
// key from the action that has it. Later keys win. The keys editor in
// Settings rewrites this file, without its comments.
`

// relaxJSON drops // and /* */ comments and the commas before a closing
// bracket, outside strings, so encoding/json reads what Zed writes.
func relaxJSON(data []byte) []byte {
	var out []byte
	inString, escaped := false, false
	for i := 0; i < len(data); i++ {
		ch := data[i]
		switch {
		case inString:
			out = append(out, ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
		case ch == '"':
			inString = true
			out = append(out, ch)
		case ch == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case ch == '/' && i+1 < len(data) && data[i+1] == '*':
			end := bytes.Index(data[i+2:], []byte("*/"))
			if end < 0 {
				i = len(data)
			} else {
				i += end + 3
			}
		case ch == ']' || ch == '}':
			trimmed := bytes.TrimRight(out, " \t\r\n")
			if len(trimmed) > 0 && trimmed[len(trimmed)-1] == ',' {
				out = trimmed[:len(trimmed)-1]
			}
			out = append(out, ch)
		default:
			out = append(out, ch)
		}
	}
	return out
}

// sortedKeys are an object's keys in a stable order: those of one block
// are all different, so their order changes only how a list shows them.
func sortedKeys(m map[string]json.RawMessage) []string { return slices.Sorted(maps.Keys(m)) }

func scopeNames() []string {
	out := make([]string, len(Scopes))
	for i, s := range Scopes {
		out[i] = string(s)
	}
	return out
}

// FromLegacy turns the keys the settings once kept, by the old IDs, as
// {"editor.run": ["Cmd+Enter"]}, into overrides.
func FromLegacy(keys map[string][]string) ([]Override, []error) {
	saved := current
	defer func() { current = saved }()
	current = build(nil)
	var errs []error
	ids := make([]string, 0, len(keys))
	for id := range keys {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var out []Override
	for _, old := range ids {
		id, ok := legacyIDs[old]
		if !ok {
			errs = append(errs, fmt.Errorf("%q is no command with keys", old))
			continue
		}
		var bindings []Binding
		for _, s := range keys[old] {
			ch, err := ParseChord(s)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", old, err))
				continue
			}
			bindings = append(bindings, Binding{Keys: Keystroke{First: ch}})
		}
		if len(bindings) == 0 && len(keys[old]) > 0 {
			continue // none read: the action keeps its own
		}
		out = SetBindings(id, bindings)
	}
	return out, errs
}

// Package keymap is the app's actions that have keys, and the keys the
// user gave them in keymap.json, as Zed keeps them: every place an
// action's key works asks here whether it was pressed, so that a key
// changed in the file or the settings changes it there.
package keymap

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"dgopher/internal/lists"
)

// Scope is a context where keys work, by the name keymap.json gives it.
type Scope string

const (
	Global    Scope = "Workspace"
	Navigator Scope = "Navigator"
	Editor    Scope = "Editor"
	Notebook  Scope = "Notebook"
	Grid      Scope = "Grid"
	Chat      Scope = "Chat"
	Dialog    Scope = "Dialog"
)

// Scopes are the contexts in the order lists show them.
var Scopes = []Scope{Global, Navigator, Editor, Notebook, Grid, Chat, Dialog}

// Label names a context as the app's lists show it.
func (s Scope) Label() string {
	switch s {
	case Global:
		return "Anywhere"
	case Editor:
		return "SQL editor"
	case Notebook:
		return "Notebook cells"
	case Grid:
		return "Results and table data"
	case Chat:
		return "AI chat"
	case Dialog:
		return "Dialogs"
	}
	return string(s)
}

// contains reports whether keys bound in s work where inner's do: its
// own, the window's anywhere, and an editor's in a notebook's cells,
// which are editors.
func (s Scope) contains(inner Scope) bool {
	return s == inner || s == Global || s == Editor && inner == Notebook
}

// overlaps reports whether keys of two contexts can be pressed in one
// place.
func (s Scope) overlaps(o Scope) bool { return s.contains(o) || o.contains(s) }

// Chord is a key with the modifiers held.
type Chord struct {
	Mods ui.Modifiers
	Key  ui.Key
}

// Keystroke is what runs an action: a chord, or two pressed one after
// the other, as cmd-k cmd-s. Then.Key is ui.KeyUnknown for one.
type Keystroke struct {
	First, Then Chord
}

// Of is a keystroke of one chord.
func Of(mods ui.Modifiers, key ui.Key) Keystroke { return Keystroke{First: Chord{mods, key}} }

// Sequence reports whether the keystroke is of two chords.
func (k Keystroke) Sequence() bool { return k.Then.Key != ui.KeyUnknown }

// Binding is a keystroke of an action, in the context it was bound in,
// with the argument it passes, 0 for none.
type Binding struct {
	Keys  Keystroke
	Scope Scope
	Arg   int
}

// Command is something the app does on a key.
type Command struct {
	ID      string
	Scope   Scope
	Title   string
	Default []Binding
	// TakesArg says the action reads an argument: a binding gives it as
	// ["action", n].
	TakesArg bool
	// written are the default keys as the registry writes them, where cmd
	// and ctrl differ, as they do on macOS alone.
	written []string
}

// keyNames name the keys a chord may be of, as keymap.json writes them.
var keyNames = func() map[ui.Key]string {
	names := map[ui.Key]string{
		ui.KeyEnter: "enter", ui.KeyEscape: "escape", ui.KeyBackspace: "backspace", ui.KeyTab: "tab", ui.KeySpace: "space",
		ui.KeyDelete: "delete", ui.KeyInsert: "insert", ui.KeyHome: "home", ui.KeyEnd: "end", ui.KeyPageUp: "pageup",
		ui.KeyPageDown: "pagedown", ui.KeyLeft: "left", ui.KeyRight: "right", ui.KeyUp: "up", ui.KeyDown: "down",
		ui.KeyMinus: "-", ui.KeyEqual: "=", ui.KeyComma: ",", ui.KeyPeriod: ".", ui.KeySlash: "/", ui.KeySemicolon: ";",
		ui.KeyQuote: "'", ui.KeyBracketLeft: "[", ui.KeyBracketRight: "]", ui.KeyBackslash: "\\", ui.KeyBackquote: "`",
	}
	for i := range 12 {
		names[ui.KeyF1+ui.Key(i)] = fmt.Sprint("f", i+1)
	}
	for i := range 10 {
		names[ui.Key0+ui.Key(i)] = fmt.Sprint(i)
	}
	for i := range 26 {
		names[ui.KeyA+ui.Key(i)] = string(rune('a' + i))
	}
	return names
}()

// keyAliases are other names keymap.json may give a key, as Zed's and
// the settings' older ones.
var keyAliases = map[string]ui.Key{"esc": ui.KeyEscape, "return": ui.KeyEnter, "del": ui.KeyDelete, "pgup": ui.KeyPageUp, "pgdn": ui.KeyPageDown}

// Nameable reports whether a shortcut may be of a key: not a modifier
// pressed alone, nor a key keymap.json has no name for.
func Nameable(k ui.Key) bool {
	_, ok := keyNames[k]
	return ok
}

// keyByName is a key by its name, in any case.
func keyByName(name string) (ui.Key, bool) {
	name = strings.ToLower(name)
	if k, ok := keyAliases[name]; ok {
		return k, true
	}
	for k, n := range keyNames {
		if n == name {
			return k, true
		}
	}
	return ui.KeyUnknown, false
}

// modNames are the modifiers as keymap.json writes them, cmd first: the
// platform's own, ⌘ on macOS and Control elsewhere, so that a key set on
// one carries to the other. The others are read too.
var modNames = []modName{{"cmd", ui.Cmd}, {"ctrl", ui.Ctrl}, {"alt", ui.Alt}, {"shift", ui.Shift}, {"super", ui.Super}}

var modAliases = map[string]ui.Modifiers{"secondary": ui.Cmd, "control": ui.Ctrl, "option": ui.Alt, "opt": ui.Alt, "win": ui.Super, "meta": ui.Super}

type modName struct {
	name string
	mod  ui.Modifiers
}

func modByName(name string) (ui.Modifiers, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if m, ok := modAliases[name]; ok {
		return m, true
	}
	i := slices.IndexFunc(modNames, func(m modName) bool { return m.name == name })
	if i < 0 {
		return 0, false
	}
	return modNames[i].mod, true
}

// String writes a chord as keymap.json keeps it, as "cmd-shift-f".
func (ch Chord) String() string {
	var parts []string
	mods := ch.Mods
	for _, m := range modNames {
		if mods&m.mod != 0 {
			parts = append(parts, m.name)
			mods &^= m.mod
		}
	}
	return strings.Join(append(parts, keyNames[ch.Key]), "-")
}

// String writes a keystroke as keymap.json keeps it, as "cmd-k cmd-s".
func (k Keystroke) String() string {
	if k.Sequence() {
		return k.First.String() + " " + k.Then.String()
	}
	return k.First.String()
}

// ParseChord reads a chord as String writes it; "+" may join its parts
// too, as the settings once wrote them.
func ParseChord(s string) (Chord, error) {
	s = strings.TrimSpace(s)
	sep := "-"
	if strings.Contains(strings.TrimSuffix(s, "+"), "+") && !strings.Contains(s, "-") || strings.HasSuffix(s, "++") {
		sep = "+"
	}
	// The key may be the separator itself, as cmd-- or "-".
	keyName, mods := s, ""
	switch i := strings.LastIndex(s, sep); {
	case s == sep:
	case strings.HasSuffix(s, sep+sep):
		keyName, mods = sep, strings.TrimSuffix(s, sep+sep)
	case i >= 0:
		keyName, mods = s[i+1:], s[:i]
	}
	var ch Chord
	if mods != "" {
		for _, p := range strings.Split(mods, sep) {
			m, ok := modByName(p)
			if !ok {
				return Chord{}, fmt.Errorf("%q is no modifier key", p)
			}
			ch.Mods |= m
		}
	}
	k, ok := keyByName(strings.TrimSpace(keyName))
	if !ok {
		return Chord{}, fmt.Errorf("%q is no key a shortcut may be of", keyName)
	}
	ch.Key = k
	return ch, nil
}

// ParseKeystroke reads a keystroke as String writes it: one chord, or two
// apart by a space.
func ParseKeystroke(s string) (Keystroke, error) {
	parts := strings.Fields(s)
	switch len(parts) {
	case 0:
		return Keystroke{}, errors.New("no key")
	case 1, 2:
	default:
		return Keystroke{}, fmt.Errorf("%q: a shortcut is one key or two in a row", s)
	}
	var k Keystroke
	var err error
	if k.First, err = ParseChord(parts[0]); err != nil {
		return Keystroke{}, err
	}
	if len(parts) == 2 {
		if k.Then, err = ParseChord(parts[1]); err != nil {
			return Keystroke{}, err
		}
	}
	return k, nil
}

// Label writes a chord as the platform shows keys: "⌘⇧F" on macOS,
// "Ctrl+Shift+F" elsewhere.
func (ch Chord) Label() string { return ch.labelOn(runtime.GOOS == "darwin") }

// labelOn writes a chord as macOS shows keys, or as the others do.
func (ch Chord) labelOn(mac bool) string {
	key := displayName(ch.Key)
	if !mac {
		var parts []string
		for _, m := range []struct {
			name string
			mod  ui.Modifiers
		}{{"Ctrl", ui.Ctrl}, {"Alt", ui.Alt}, {"Shift", ui.Shift}, {"Win", ui.Super}} {
			if ch.Mods&m.mod != 0 {
				parts = append(parts, m.name)
			}
		}
		return strings.Join(append(parts, key), "+")
	}
	var b strings.Builder
	for _, m := range []struct {
		sym string
		mod ui.Modifiers
	}{{"⌃", ui.Ctrl}, {"⌥", ui.Alt}, {"⇧", ui.Shift}, {"⌘", ui.Super}} {
		if ch.Mods&m.mod != 0 {
			b.WriteString(m.sym)
		}
	}
	switch ch.Key {
	case ui.KeyEnter:
		key = "↵"
	case ui.KeyUp:
		key = "↑"
	case ui.KeyDown:
		key = "↓"
	case ui.KeyLeft:
		key = "←"
	case ui.KeyRight:
		key = "→"
	}
	return b.String() + key
}

// displayName is a key's name as labels show it: "Enter", "F5", "A".
func displayName(k ui.Key) string {
	switch k {
	case ui.KeyEscape:
		return "Esc"
	case ui.KeyPageUp:
		return "PageUp"
	case ui.KeyPageDown:
		return "PageDown"
	}
	n := keyNames[k]
	if n == "" {
		return "?"
	}
	return strings.ToUpper(n[:1]) + n[1:]
}

// Label writes a keystroke as the platform shows keys, its two chords
// apart by a space.
func (k Keystroke) Label() string {
	if k.Sequence() {
		return k.First.Label() + " " + k.Then.Label()
	}
	return k.First.Label()
}

// Accelerator writes a chord as a native menu takes it.
func (ch Chord) Accelerator() string {
	var parts []string
	mods := ch.Mods
	if mods&ui.Cmd != 0 {
		parts, mods = append(parts, "CmdOrCtrl"), mods&^ui.Cmd
	}
	for _, m := range []struct {
		name string
		mod  ui.Modifiers
	}{{"Ctrl", ui.Ctrl}, {"Alt", ui.Alt}, {"Shift", ui.Shift}, {"Super", ui.Super}} {
		if mods&m.mod != 0 {
			parts = append(parts, m.name)
		}
	}
	key := keyNames[ch.Key]
	if len(key) > 1 {
		key = displayName(ch.Key)
	}
	return strings.Join(append(parts, key), "+")
}

// Bindings are an action's keys as they are now: those of its own the
// user kept, then those the user gave it.
func Bindings(id string) []Binding { return current.byCommand[id] }

// Changed reports whether the user changed an action's keys.
func Changed(id string) bool {
	cmd, _ := Lookup(id)
	return !sameBindings(Bindings(id), cmd.Default)
}

// Primary is the key a menu shows for an action: its first of one chord
// without an argument, which a native menu can take.
func Primary(id string) (Chord, bool) {
	for _, b := range Bindings(id) {
		if !b.Keys.Sequence() && b.Arg == 0 {
			return b.Keys.First, true
		}
	}
	return Chord{}, false
}

// Label is an action's keys as the platform shows them, "" for none.
func Label(id string) string {
	return strings.Join(lists.Map(Bindings(id), func(b Binding) string { return b.Keys.Label() }), "  or  ")
}

// First is an action's first key as the platform shows it, "" for none.
func First(id string) string {
	if bs := Bindings(id); len(bs) > 0 {
		return bs[0].Keys.Label()
	}
	return ""
}

// Hint is a tooltip or a label with an action's first key after it, as
// "Format SQL (⌘⇧F)"; the text alone for an action without keys.
func Hint(text, id string) string {
	if k := First(id); k != "" {
		return text + " (" + k + ")"
	}
	return text
}

// sameBindings reports whether two lists hold the same keys, in any
// order.
func sameBindings(a, b []Binding) bool {
	key := func(b Binding) Binding { b.Scope = ""; return b }
	return len(a) == len(b) && !slices.ContainsFunc(a, func(x Binding) bool {
		return !slices.ContainsFunc(b, func(y Binding) bool { return key(x) == key(y) })
	})
}

// reserved are keys that are no action's, in the contexts where they are
// a widget's own: the grid's F2.
var reserved = []reservedKey{{[]Scope{Grid}, Chord{0, ui.KeyF2}}}

type reservedKey struct {
	scopes []Scope
	chord  Chord
}

// typesOrMoves says why a chord cannot start or end a shortcut, nil when
// it can: a key that types needs a modifier but Shift, as it would type
// as well, and one that moves or edits in a list or a form any modifier,
// but as the second of two.
func typesOrMoves(ch Chord, first bool) error {
	if !Nameable(ch.Key) {
		return errors.New("that key cannot be a shortcut")
	}
	name := keyNames[ch.Key]
	switch {
	case ch.Key >= ui.KeyF1 && ch.Key <= ui.KeyF12, ch.Key == ui.KeyDelete, ch.Key == ui.KeyInsert:
	case len(name) == 1 || ch.Key == ui.KeySpace:
		if ch.Mods&^ui.Shift == 0 {
			return fmt.Errorf("%s types: add ⌘, Ctrl or Alt", ch.Label())
		}
	case ch.Mods == 0 && first:
		// The second of two keys may move or edit: the window takes it,
		// as the first was pressed, before the focus does.
		return fmt.Errorf("%s moves or edits in lists and forms: add a modifier", ch.Label())
	}
	return nil
}

// Allowed says why a keystroke cannot be an action's, nil when it can: a
// first key that types or moves alone, a key that is another action's
// where both work, the first of another's two keys, or a widget's own.
func Allowed(id string, k Keystroke) error {
	if err := typesOrMoves(k.First, true); err != nil {
		return err
	}
	if k.Sequence() {
		if err := typesOrMoves(k.Then, false); err != nil {
			return err
		}
	}
	cmd, _ := Lookup(id)
	for _, r := range reserved {
		if !k.Sequence() && r.chord == k.First && slices.Contains(r.scopes, cmd.Scope) {
			return fmt.Errorf("%s is the app's own, there", k.Label())
		}
	}
	if other, ok := Conflict(id, k); ok {
		return fmt.Errorf("%s is the key of “%s” (%s): remove it there first", k.Label(), other.Title, other.Scope.Label())
	}
	if other, ok := prefixConflict(id, k); ok {
		if k.Sequence() {
			return fmt.Errorf("%s alone is the key of “%s” (%s): it cannot begin two keys", k.First.Label(), other.Title, other.Scope.Label())
		}
		return fmt.Errorf("%s begins a key of “%s” (%s)", k.Label(), other.Title, other.Scope.Label())
	}
	return nil
}

// Conflict is the action of a context overlapping id's that already has
// keystroke k, other than id.
func Conflict(id string, k Keystroke) (Command, bool) {
	cmd, _ := Lookup(id)
	for s, bound := range current.bound {
		if !s.overlaps(cmd.Scope) {
			continue
		}
		if e, ok := bound[k]; ok && e.id != id {
			other, _ := Lookup(e.id)
			return other, true
		}
	}
	return Command{}, false
}

// prefixConflict is an action of an overlapping context whose one key is
// the first of k's two, or whose two begin with k alone.
func prefixConflict(id string, k Keystroke) (Command, bool) {
	// In any context: Dispatch claims the first key of a keystroke of two
	// wherever the focus is (sequenceFrom).
	for _, bound := range current.bound {
		for other, e := range bound {
			if e.id == id {
				continue
			}
			if k.Sequence() && !other.Sequence() && other.First == k.First || !k.Sequence() && other.Sequence() && other.First == k.First {
				c, _ := Lookup(e.id)
				return c, true
			}
		}
	}
	return Command{}, false
}

// Item gives a menu item the action's first key.
func Item[T shortcutter[T]](it T, id string) T {
	if ch, ok := Primary(id); ok {
		it.Shortcut(ch.Mods, ch.Key)
	}
	return it
}

// shortcutter is a menu item showing keys: the system's or one the app
// draws.
type shortcutter[T any] interface {
	Shortcut(mods ui.Modifiers, key ui.Key) T
}

// commandIndex maps an action's ID to its index in Commands.
var commandIndex = func() map[string]int {
	m := make(map[string]int, len(Commands))
	for i, c := range Commands {
		if _, dup := m[c.ID]; !dup {
			m[c.ID] = i
		}
	}
	return m
}()

// Lookup is an action by its ID.
func Lookup(id string) (Command, bool) {
	i, ok := commandIndex[id]
	if !ok {
		return Command{}, false
	}
	return Commands[i], true
}

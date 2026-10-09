// Package keymap is the app's commands that have keys, and the keys the
// user gave them: every place a command's key works asks here whether it
// was pressed, so that a key changed in the settings changes it there.
package keymap

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

// Scope is where a command's keys work.
type Scope string

const (
	Global    Scope = "Anywhere"
	Editor    Scope = "SQL editor"
	Grid      Scope = "Results and table data"
	Navigator Scope = "Navigator"
)

// Scopes are the scopes in the order lists show them.
var Scopes = []Scope{Global, Navigator, Editor, Grid}

// overlaps reports whether keys of two scopes can be pressed in one
// place: a scope's own, and the window's anywhere.
func (s Scope) overlaps(o Scope) bool { return s == o || s == Global || o == Global }

// Chord is a key with the modifiers held.
type Chord struct {
	Mods ui.Modifiers
	Key  ui.Key
}

// Command is something the app does on a key.
type Command struct {
	ID      string
	Scope   Scope
	Title   string
	Default []Chord
	written []string // the default keys as the registry writes them
}

// keyNames name the keys a chord may be of, as the settings write them.
var keyNames = func() map[ui.Key]string {
	names := map[ui.Key]string{
		ui.KeyEnter: "Enter", ui.KeyEscape: "Esc", ui.KeyBackspace: "Backspace", ui.KeyTab: "Tab", ui.KeySpace: "Space",
		ui.KeyDelete: "Delete", ui.KeyInsert: "Insert", ui.KeyHome: "Home", ui.KeyEnd: "End", ui.KeyPageUp: "PageUp",
		ui.KeyPageDown: "PageDown", ui.KeyLeft: "Left", ui.KeyRight: "Right", ui.KeyUp: "Up", ui.KeyDown: "Down",
		ui.KeyMinus: "-", ui.KeyEqual: "=", ui.KeyComma: ",", ui.KeyPeriod: ".", ui.KeySlash: "/", ui.KeySemicolon: ";",
		ui.KeyQuote: "'", ui.KeyBracketLeft: "[", ui.KeyBracketRight: "]", ui.KeyBackslash: "\\", ui.KeyBackquote: "`",
	}
	for i := range 12 {
		names[ui.KeyF1+ui.Key(i)] = fmt.Sprint("F", i+1)
	}
	for i := range 10 {
		names[ui.Key0+ui.Key(i)] = fmt.Sprint(i)
	}
	for i := range 26 {
		names[ui.KeyA+ui.Key(i)] = string(rune('A' + i))
	}
	return names
}()

// Nameable reports whether a shortcut may be of a key: not a modifier
// pressed alone, nor a key the settings have no name for.
func Nameable(k ui.Key) bool {
	_, ok := keyNames[k]
	return ok
}

// keyByName is a key by its name, in any case.
func keyByName(name string) (ui.Key, bool) {
	for k, n := range keyNames {
		if strings.EqualFold(n, name) {
			return k, true
		}
	}
	return ui.KeyUnknown, false
}

// modNames are the modifiers as the settings write them, Cmd first: the
// platform's own, ⌘ on macOS and Control elsewhere, so that a key set on
// one carries to the other.
var modNames = []modName{{"Cmd", ui.Cmd}, {"Ctrl", ui.Ctrl}, {"Alt", ui.Alt}, {"Shift", ui.Shift}, {"Super", ui.Super}}

type modName struct {
	name string
	mod  ui.Modifiers
}

// String writes a chord as the settings keep it, as "Cmd+Shift+F".
func (ch Chord) String() string {
	var parts []string
	mods := ch.Mods
	for _, m := range modNames {
		if mods&m.mod != 0 {
			parts = append(parts, m.name)
			mods &^= m.mod
		}
	}
	return strings.Join(append(parts, keyNames[ch.Key]), "+")
}

// ParseChord reads a chord as String writes it.
func ParseChord(s string) (Chord, error) {
	parts := strings.Split(s, "+")
	var ch Chord
	for _, p := range parts[:len(parts)-1] {
		i := slices.IndexFunc(modNames, func(m modName) bool { return strings.EqualFold(m.name, strings.TrimSpace(p)) })
		if i < 0 {
			return Chord{}, fmt.Errorf("%q is no modifier key", p)
		}
		ch.Mods |= modNames[i].mod
	}
	k, ok := keyByName(strings.TrimSpace(parts[len(parts)-1]))
	if !ok {
		return Chord{}, fmt.Errorf("%q is no key a shortcut may be of", parts[len(parts)-1])
	}
	ch.Key = k
	return ch, nil
}

// Label writes a chord as the platform shows keys: "⌘⇧F" on macOS,
// "Ctrl+Shift+F" elsewhere.
func (ch Chord) Label() string {
	key := keyNames[ch.Key]
	if runtime.GOOS != "darwin" {
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
	if len(key) == 1 {
		key = strings.ToLower(key)
	}
	return strings.Join(append(parts, key), "+")
}

// keys are the user's keys of the commands they changed, by command;
// none for a command they took every key from.
var keys = map[string][]Chord{}

// Use sets the user's keys, as the settings keep them: the keys of the
// commands changed, by command. A key that does not read is reported,
// and left out.
func Use(set map[string][]string) []error {
	next := map[string][]Chord{}
	var errs []error
	for id, chords := range set {
		if _, ok := Lookup(id); !ok {
			errs = append(errs, fmt.Errorf("%q is no command with keys", id))
			continue
		}
		read := []Chord{}
		for _, s := range chords {
			ch, err := ParseChord(s)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", id, err))
				continue
			}
			read = append(read, ch)
		}
		// None read of keys given, the command keeps its own.
		if len(read) > 0 || len(chords) == 0 {
			next[id] = read
		}
	}
	keys = next
	return errs
}

// Chords are a command's keys: the user's, else its own.
func Chords(id string) []Chord {
	if chords, ok := keys[id]; ok {
		return chords
	}
	cmd, _ := Lookup(id)
	return cmd.Default
}

// Changed reports whether the user changed a command's keys.
func Changed(id string) bool {
	_, ok := keys[id]
	return ok
}

// Primary is the key a menu shows for a command, its first.
func Primary(id string) (Chord, bool) {
	chords := Chords(id)
	if len(chords) == 0 {
		return Chord{}, false
	}
	return chords[0], true
}

// Label is a command's keys as the platform shows them, "" for none.
func Label(id string) string {
	var out []string
	for _, ch := range Chords(id) {
		out = append(out, ch.Label())
	}
	return strings.Join(out, "  or  ")
}

// Written are a command's keys as the settings keep them: one of its own
// as the registry writes it, as Ctrl+Tab, which on Linux, where Ctrl and
// Cmd are one key, would otherwise read Cmd+Tab and be ⌘Tab on macOS.
func Written(id string, chords []Chord) []string {
	cmd, _ := Lookup(id)
	out := []string{}
	for _, ch := range chords {
		if i := slices.Index(cmd.Default, ch); i >= 0 {
			out = append(out, cmd.written[i])
		} else {
			out = append(out, ch.String())
		}
	}
	return out
}

// SameKeys reports whether two lists hold the same keys, in any order.
func SameKeys(a, b []Chord) bool {
	return len(a) == len(b) && !slices.ContainsFunc(a, func(ch Chord) bool { return !slices.Contains(b, ch) })
}

// reserved are keys that are no command's, in the scopes they are the
// window's or a list's own: the tabs' ⌘1 … ⌘9, which a grid with the
// keys takes for itself, and the grid's F2.
var reserved = func() []reservedKey {
	out := []reservedKey{{[]Scope{Grid}, Chord{0, ui.KeyF2}}}
	for i := range 9 {
		out = append(out, reservedKey{[]Scope{Global, Navigator, Editor}, Chord{ui.Cmd, ui.Key1 + ui.Key(i)}})
	}
	return out
}()

type reservedKey struct {
	scopes []Scope
	chord  Chord
}

// Allowed says why a key cannot be a command's, nil when it can: a key
// that types needs a modifier but Shift, one that moves or edits in a
// list or a form any modifier, and a key that is another command's, or
// the window's own, where both work is refused.
func Allowed(id string, ch Chord) error {
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
	case ch.Mods == 0:
		return fmt.Errorf("%s moves or edits in lists and forms: add a modifier", ch.Label())
	}
	cmd, _ := Lookup(id)
	for _, r := range reserved {
		if r.chord == ch && slices.Contains(r.scopes, cmd.Scope) {
			return fmt.Errorf("%s is the app's own, there", ch.Label())
		}
	}
	if other, ok := Conflict(id, ch); ok {
		return fmt.Errorf("%s is the key of “%s” (%s): remove it there first", ch.Label(), other.Title, other.Scope)
	}
	return nil
}

// LaterPressed reports whether a key of the command but its first was
// pressed: the menu bar takes the first.
func LaterPressed(c *ui.Context, id string) bool {
	chords := Chords(id)
	for _, ch := range chords[min(1, len(chords)):] {
		if c.Shortcut(ch.Mods, ch.Key) {
			return true
		}
	}
	return false
}

// Pressed reports whether a key of the command was pressed, as
// c.Shortcut does for one.
func Pressed(c *ui.Context, id string) bool {
	for _, ch := range Chords(id) {
		if c.Shortcut(ch.Mods, ch.Key) {
			return true
		}
	}
	return false
}

// ListPressed reports whether a key of the command was pressed in a
// list, as its Shortcut does for one.
func ListPressed(c *ui.Context, l *ui.ListState, id string) bool {
	for _, ch := range Chords(id) {
		if l.Shortcut(c, ch.Mods, ch.Key) {
			return true
		}
	}
	return false
}

// Is reports whether a key pressed is one of the command's.
func Is(id string, mods ui.Modifiers, key ui.Key) bool {
	return slices.Contains(Chords(id), Chord{mods, key})
}

// Item gives a menu item the command's first key.
func Item(it *ui.MenuItem, id string) *ui.MenuItem {
	if ch, ok := Primary(id); ok {
		it.Shortcut(ch.Mods, ch.Key)
	}
	return it
}

// Conflict is the command of a scope that one of the scopes overlap a
// chord already belongs to, other than id.
func Conflict(id string, ch Chord) (Command, bool) {
	cmd, _ := Lookup(id)
	for _, other := range Commands {
		if other.ID != id && cmd.Scope.overlaps(other.Scope) && slices.Contains(Chords(other.ID), ch) {
			return other, true
		}
	}
	return Command{}, false
}

// Lookup is a command by its ID.
func Lookup(id string) (Command, bool) {
	i := slices.IndexFunc(Commands, func(c Command) bool { return c.ID == id })
	if i < 0 {
		return Command{}, false
	}
	return Commands[i], true
}

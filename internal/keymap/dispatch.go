package keymap

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// sequenceWait is how long the second of two keys is waited for.
const sequenceWait = 1500 * time.Millisecond

// frameState is what a window's keys did in its frame: the first of two
// keys pressed and waiting for the second, the keystrokes of two keys
// completed, and the contexts that asked for keys.
type frameState struct {
	waiting bool
	pending Chord
	since   time.Time
	fired   map[Keystroke]bool
	// asked are the contexts whose actions asked for their keys in this
	// frame, asked those of the last: an action's keys anywhere give way
	// to the same keys of a context that had them in the last frame.
	asked, askedLast map[Scope]bool
}

// frame is the state of the window drawing, which Dispatch picks; frames
// are each window's, by the window.
var (
	frame  = &frameState{}
	frames = map[any]*frameState{}
)

// Forget lets a window closed go.
func Forget(window any) { delete(frames, window) }

// Dispatch takes the keys of two in a row in a window's frame, once
// before any action asks for its keys: the first key of each, then, once
// one was pressed, the keys that can follow it, until one is, Esc, or a
// while passes. What it keeps is the window's.
func Dispatch(c *ui.Context, window any) {
	if frames[window] == nil {
		frames[window] = &frameState{}
	}
	frame = frames[window]
	frame.askedLast, frame.asked = frame.asked, map[Scope]bool{}
	frame.fired = nil
	now := c.Now()
	if frame.waiting {
		left := sequenceWait - now.Sub(frame.since)
		if left <= 0 {
			frame.waiting = false
		} else {
			for _, k := range current.sequences {
				if k.First == frame.pending && c.Shortcut(k.Then.Mods, k.Then.Key) && frame.waiting {
					frame.fired = map[Keystroke]bool{k: true}
					frame.waiting = false
				}
			}
			if frame.waiting && c.Shortcut(0, ui.KeyEscape) {
				frame.waiting = false
			}
			if frame.waiting {
				c.After(left)
			}
		}
		if !frame.waiting {
			c.Invalidate() // the status bar's pending key goes
		}
		return
	}
	for _, ch := range current.firsts {
		if c.Shortcut(ch.Mods, ch.Key) && !frame.waiting {
			frame.waiting, frame.pending, frame.since = true, ch, now
			c.After(sequenceWait)
			c.Invalidate()
		}
	}
}

// Pending is the first of two keys pressed, waiting for the second, as
// the platform shows it; "" when none waits.
func Pending() string {
	if !frame.waiting {
		return ""
	}
	return frame.pending.Label()
}

// shadowed reports whether a binding does not work now: one anywhere
// gives way to the same key of a context that asked for keys in the last
// frame, which holds the focus, as the narrower context wins in Zed; and
// an action of the window's bound in a narrower context works only while
// that context has the focus.
func shadowed(id string, b Binding) bool {
	if cmd, _ := Lookup(id); b.Scope != cmd.Scope && cmd.Scope == Global {
		return !frame.askedLast[b.Scope]
	}
	if b.Scope != Global {
		return false
	}
	for s := range frame.askedLast {
		if e, ok := current.bound[s][b.Keys]; s != Global && ok && e.id != id {
			return true
		}
	}
	return false
}

// pressedBinding reports whether a binding's keys were pressed: its one
// key, as c.Shortcut does, or its two, as Dispatch took them.
func pressedBinding(c *ui.Context, b Binding) bool {
	if b.Keys.Sequence() {
		if frame.fired[b.Keys] {
			delete(frame.fired, b.Keys)
			return true
		}
		return false
	}
	return c.Shortcut(b.Keys.First.Mods, b.Keys.First.Key)
}

// ask notes that a context asked for an action's keys in this frame.
func ask(id string) {
	if cmd, ok := Lookup(id); ok && frame.asked != nil {
		frame.asked[cmd.Scope] = true
	}
}

// Pressed reports whether a key of the action was pressed, as c.Shortcut
// does for one.
func Pressed(c *ui.Context, id string) bool {
	_, ok := PressedArg(c, id)
	return ok
}

// PressedArg reports whether a key of the action was pressed, and the
// argument its binding gives, 0 for none.
func PressedArg(c *ui.Context, id string) (int, bool) {
	ask(id)
	for _, b := range Bindings(id) {
		if !shadowed(id, b) && pressedBinding(c, b) {
			return b.Arg, true
		}
	}
	return 0, false
}

// LaterPressed reports whether a key of the action but its first was
// pressed: the menu bar takes the first.
func LaterPressed(c *ui.Context, id string) bool {
	ask(id)
	primary, hasPrimary := Primary(id)
	for _, b := range Bindings(id) {
		if hasPrimary && !b.Keys.Sequence() && b.Arg == 0 && b.Keys.First == primary {
			continue
		}
		if !shadowed(id, b) && pressedBinding(c, b) {
			return true
		}
	}
	return false
}

// ListPressed reports whether a key of the action was pressed in a list,
// as its Shortcut does for one; the action's keys of two, as Dispatch
// took them.
func ListPressed(c *ui.Context, l *ui.ListState, id string) bool {
	ask(id)
	for _, b := range Bindings(id) {
		if b.Keys.Sequence() {
			if pressedBinding(c, b) {
				return true
			}
			continue
		}
		if l.Shortcut(c, b.Keys.First.Mods, b.Keys.First.Key) {
			return true
		}
	}
	return false
}

// Fired reports whether the action's keys of two were pressed, as
// Dispatch took them, for a widget that reads one key by itself.
func Fired(id string) bool {
	for _, b := range Bindings(id) {
		if b.Keys.Sequence() && frame.fired[b.Keys] {
			delete(frame.fired, b.Keys)
			return true
		}
	}
	return false
}

// Is reports whether a key pressed is one of the action's of one key.
func Is(id string, mods ui.Modifiers, key ui.Key) bool {
	for _, b := range Bindings(id) {
		if !b.Keys.Sequence() && b.Keys.First == (Chord{mods, key}) {
			return true
		}
	}
	return false
}

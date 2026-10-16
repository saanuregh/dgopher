package editor

import (
	"slices"

	"dgopher/internal/keymap"

	code "github.com/egoist/mygo/ui/editor"
)

type editorAction struct {
	id      string
	command code.Command
}

// editorActions are the keymap's editor commands the editor itself runs,
// by the editor's command.
var editorActions = [...]editorAction{
	{keymap.CursorAbove, code.AddCursorAbove},
	{keymap.CursorBelow, code.AddCursorBelow},
	{keymap.SelectNext, code.SelectNext},
	{keymap.SelectAll, code.SelectAllMatches},
	{keymap.Fold, code.Fold},
	{keymap.Unfold, code.Unfold},
	{keymap.FoldAll, code.FoldAll},
	{keymap.UnfoldAll, code.UnfoldAll},
	{keymap.Comment, code.ToggleComment},
	{keymap.BlockComment, code.ToggleBlockComment},
	{keymap.Find, code.Find},
	{keymap.Replace, code.FindReplace},
	{keymap.ShowHover, code.ShowHover},
}

// syncBindings binds the editor's keys of the keymap, as the user set
// them, before the editor's own, and takes the editor's own keys for
// those commands away, so the keymap alone says what runs them: a key of
// the app's that the window claims with a shortcut wins over both.
func (e *Editor) syncBindings() {
	e.keys = e.keys[:0]
	for _, a := range editorActions {
		for _, ch := range keymap.Chords(a.id) {
			e.keys = append(e.keys, code.Binding{Mods: ch.Mods, Key: ch.Key, Command: a.command})
		}
	}
	for _, b := range defaultBindings {
		mapped := slices.ContainsFunc(editorActions[:], func(a editorAction) bool { return a.command == b.Command })
		if !mapped || slices.ContainsFunc(e.keys, func(k code.Binding) bool { return sameKeys(k, b) }) {
			continue
		}
		b.Command = code.NoCommand
		e.keys = append(e.keys, b)
	}
	if !slices.Equal(e.keys, e.State.Bindings) {
		e.State.Bindings = slices.Clone(e.keys)
	}
}

// defaultBindings are the editor's own keys.
var defaultBindings = code.DefaultBindings()

// sameKeys reports whether two bindings are for the same keys.
func sameKeys(a, b code.Binding) bool {
	return a.Mods == b.Mods && a.Key == b.Key && a.ThenMods == b.ThenMods && a.ThenKey == b.ThenKey
}

// boundCommand is the command a key runs in the editor, NoCommand for
// none: the app's bindings first, then the editor's own.
func (e *Editor) boundCommand(b code.Binding) code.Command {
	for _, list := range [][]code.Binding{e.State.Bindings, defaultBindings} {
		for _, k := range list {
			if k.Mods == b.Mods && k.Key == b.Key && k.ThenKey == 0 {
				return k.Command
			}
		}
	}
	return code.NoCommand
}

// editsText reports whether a command of the editor changes the text.
func editsText(cmd code.Command) bool {
	return cmd >= code.Undo && cmd <= code.TrimTrailingWhitespace && cmd != code.Copy || cmd == code.RevertHunk
}

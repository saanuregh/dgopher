package keymap

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Every default key reads back as written, and none is another's where
// both work, nor begins another's two.
func TestDefaults(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Commands {
		if seen[c.ID] {
			t.Errorf("%s twice", c.ID)
		}
		seen[c.ID] = true
		if !strings.Contains(c.ID, "::") {
			t.Errorf("%s: an action is namespace::Name", c.ID)
		}
		for _, b := range c.Default {
			back, err := ParseKeystroke(b.Keys.String())
			if err != nil || back != b.Keys {
				t.Errorf("%s: %s read back as %v, %v", c.ID, b.Keys, back, err)
			}
			if err := Allowed(c.ID, b.Keys); err != nil {
				t.Errorf("%s: %v", c.ID, err)
			}
		}
	}
}

func TestParseKeystroke(t *testing.T) {
	for in, want := range map[string]Keystroke{
		"cmd-shift-f":      Of(ui.Cmd|ui.Shift, ui.KeyF),
		"Cmd+Shift+Enter":  Of(ui.Cmd|ui.Shift, ui.KeyEnter),
		"cmd--":            Of(ui.Cmd, ui.KeyMinus),
		"alt-shift-=":      Of(ui.Alt|ui.Shift, ui.KeyEqual),
		"cmd-k cmd-s":      {First: Chord{ui.Cmd, ui.KeyK}, Then: Chord{ui.Cmd, ui.KeyS}},
		"secondary-k left": {First: Chord{ui.Cmd, ui.KeyK}, Then: Chord{0, ui.KeyLeft}},
		"f5":               Of(0, ui.KeyF5),
		"option-esc":       Of(ui.Alt, ui.KeyEscape),
	} {
		got, err := ParseKeystroke(in)
		if err != nil || got != want {
			t.Errorf("%q: %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "cmd-nope", "hyper-a", "a b c"} {
		if _, err := ParseKeystroke(bad); err == nil {
			t.Errorf("%q read", bad)
		}
	}
}

const sample = `// mine
[
  {
    "bindings": {
      "cmd-k cmd-f": "editor::Format", // on two keys
      "cmd-t": null,
    },
  },
  {
    "context": "Editor",
    "bindings": {
      "f5": "none",
      "alt-1": ["workspace::ActivatePane", 1],
      "cmd-r": "grid::Refresh",
      "cmd-e": "nothing::There",
    }
  },
  { "context": "Kitchen", "bindings": { "cmd-y": "editor::Run" } },
  { "context": "Workspace", "bindings": { "cmd-u": "pane::ActivateItem" } }
]`

func TestParse(t *testing.T) {
	defer Use(nil)
	overrides, errs := Parse([]byte(sample))
	if len(errs) != 4 {
		t.Fatalf("errors %v", errs)
	}
	Use(overrides)
	format := Bindings(Format)
	if !slices.ContainsFunc(format, func(b Binding) bool { return b.Keys.String() == "cmd-k cmd-f" && b.Scope == Global }) {
		t.Errorf("format %v", format)
	}
	if len(Bindings(NewEditor)) != 0 || !Changed(NewEditor) {
		t.Error("cmd-t kept")
	}
	if Is(Run, 0, ui.KeyF5) || !Is(Run, ui.Cmd, ui.KeyEnter) {
		t.Error("f5 kept, or cmd-enter lost")
	}
	if p := Bindings(ActivatePane); !slices.ContainsFunc(p, func(b Binding) bool { return b.Arg == 1 && b.Keys == Of(ui.Alt, ui.Key1) }) {
		t.Errorf("activate pane %v", p)
	}
	if _, errs := Parse([]byte(`[{"context": "Editor", "bindings": {"cmd-k": "editor::Format"}}]`)); len(errs) != 1 {
		t.Errorf("a key beginning keys of two read without a word: %v", errs)
	}
	if _, err := Parse([]byte("[{")); err == nil {
		t.Error("broken JSON read")
	}
}

// What the file writes reads back as the same keys.
func TestFormatFile(t *testing.T) {
	defer Use(nil)
	overrides, _ := Parse([]byte(sample))
	back, errs := Parse(FormatFile(overrides))
	if len(errs) != 0 {
		t.Fatalf("errors %v\n%s", errs, FormatFile(overrides))
	}
	key := func(o Override) string { return string(o.Scope) + o.Keys.String() + o.ID }
	a, b := slices.Sorted(slices.Values(mapKeys(overrides, key))), slices.Sorted(slices.Values(mapKeys(back, key)))
	if !slices.Equal(a, b) {
		t.Errorf("%v\n%v", a, b)
	}
}

func mapKeys(list []Override, key func(Override) string) []string {
	out := make([]string, len(list))
	for i, o := range list {
		out[i] = key(o)
	}
	return out
}

func TestSetBindings(t *testing.T) {
	defer Use(nil)
	out := SetBindings(Format, []Binding{{Keys: Of(ui.Alt|ui.Shift, ui.KeyF)}})
	if got := Bindings(Format); len(got) != 1 || got[0].Keys != Of(ui.Alt|ui.Shift, ui.KeyF) || !Changed(Format) {
		t.Fatalf("format %v", got)
	}
	if len(out) != 2 { // the old key taken away, the new given
		t.Errorf("overrides %v", out)
	}
	if ch, _ := Primary(Format); ch.Accelerator() != "Alt+Shift+f" {
		t.Errorf("accelerator %q", ch.Accelerator())
	}
	if out := Reset(Format); len(out) != 0 || Changed(Format) {
		t.Errorf("reset left %v", out)
	}
	// Taking a key from one action and giving it to another.
	SetBindings(Explain, nil)
	SetBindings(Format, []Binding{{Keys: Of(ui.Cmd, ui.KeyE)}})
	if !Is(Format, ui.Cmd, ui.KeyE) || len(Bindings(Explain)) != 0 {
		t.Error("cmd-e not moved")
	}
}

func TestLegacy(t *testing.T) {
	defer Use(nil)
	out, errs := FromLegacy(map[string][]string{"editor.format": {"Alt+Shift+F"}, "editor.explain": {}, "nothing": {"Cmd+Q"}, "editor.find": {"Cmd+Nope"}})
	if len(errs) != 2 {
		t.Fatalf("errors %v", errs)
	}
	Use(out)
	if !Is(Format, ui.Alt|ui.Shift, ui.KeyF) || len(Bindings(Explain)) != 0 || len(Bindings(Find)) != 1 {
		t.Errorf("format %v, explain %v, find %v", Bindings(Format), Bindings(Explain), Bindings(Find))
	}
}

// A key that types, or moves in a list, or is another's, is no action's.
func TestAllowed(t *testing.T) {
	for _, k := range []Keystroke{Of(0, ui.KeyA), Of(ui.Shift, ui.KeyA), Of(0, ui.KeyEnter), Of(0, ui.KeyDown), Of(ui.Cmd, ui.Key3), Of(ui.Cmd, ui.KeyK),
		{First: Chord{ui.Cmd, ui.KeyK}, Then: Chord{0, ui.KeyA}}, {First: Chord{ui.Cmd, ui.KeyT}, Then: Chord{ui.Cmd, ui.KeyA}}} {
		if Allowed(Format, k) == nil {
			t.Errorf("%s allowed", k)
		}
	}
	for _, k := range []Keystroke{Of(0, ui.KeyF9), Of(ui.Alt, ui.KeyQ), {First: Chord{ui.Cmd, ui.KeyK}, Then: Chord{ui.Cmd, ui.KeyQ}}, {First: Chord{ui.Cmd, ui.KeyK}, Then: Chord{0, ui.KeyPageDown}}} {
		if err := Allowed(Format, k); err != nil {
			t.Errorf("%s refused: %v", k, err)
		}
	}
	if Allowed(EditValue, Of(ui.Shift, ui.KeyEnter)) != nil {
		t.Error("the grid's shift-enter refused")
	}
	if Allowed(Copy, Of(0, ui.KeyF2)) == nil {
		t.Error("the grid's F2 taken")
	}
}

// A key anywhere gives way to the same key of the context that has the
// focus.
func TestShadowed(t *testing.T) {
	defer Use(nil)
	defer func() { frame.askedLast = nil }()
	overrides, _ := Parse([]byte(`[{"context": "Editor", "bindings": {"cmd-p": "editor::Format"}}]`))
	Use(overrides)
	b := Bindings(OpenTable)[0]
	if shadowed(OpenTable, b) {
		t.Fatal("shadowed with no context asking")
	}
	frame.askedLast = map[Scope]bool{Editor: true}
	if !shadowed(OpenTable, b) {
		t.Error("the editor's cmd-p did not win in the editor")
	}
	frame.askedLast = map[Scope]bool{Grid: true}
	if shadowed(OpenTable, b) {
		t.Error("shadowed in the grid")
	}
}

func TestLabels(t *testing.T) {
	k := Keystroke{First: Chord{ui.Cmd, ui.KeyK}, Then: Chord{0, ui.KeyPageDown}}
	if got := k.Label(); got != "Ctrl+K PageDown" && got != "⌘K PageDown" {
		t.Errorf("label %q", got)
	}
	if got := Of(0, ui.KeyF5).Label(); got != "F5" {
		t.Errorf("label %q", got)
	}
}

// docs/keyboard.md lists every default key, as macOS shows it.
func TestKeyboardDoc(t *testing.T) {
	data, err := os.ReadFile("../../docs/keyboard.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := normalizeKeys(strings.ReplaceAll(string(data), "Ctrl+", "⌃"))
	for _, c := range Commands {
		if c.TakesArg {
			continue
		}
		for _, w := range c.written {
			label := macLabel(t, w)
			if !strings.Contains(doc, normalizeKeys(label)) {
				t.Errorf("%s: %s is not in docs/keyboard.md", c.ID, label)
			}
		}
	}
}

// normalizeKeys writes each run of modifier symbols in one order, as
// docs write them in another.
func normalizeKeys(s string) string {
	const order = "⌃⌥⇧⌘"
	var b strings.Builder
	var run []rune
	flush := func() {
		slices.SortFunc(run, func(x, y rune) int { return strings.IndexRune(order, x) - strings.IndexRune(order, y) })
		b.WriteString(string(run))
		run = run[:0]
	}
	for _, r := range s {
		if strings.ContainsRune(order, r) {
			run = append(run, r)
			continue
		}
		flush()
		b.WriteRune(r)
	}
	flush()
	return b.String()
}

// macLabel writes keys as the registry writes them as macOS shows them,
// whatever the system: cmd is ⌘ and ctrl ⌃.
func macLabel(t *testing.T, written string) string {
	var chords []string
	for _, part := range strings.Fields(written) {
		ch, err := ParseChord(part)
		if err != nil {
			t.Fatal(err)
		}
		mods := ""
		for _, m := range []struct{ name, sym string }{{"ctrl", "⌃"}, {"alt", "⌥"}, {"shift", "⇧"}, {"cmd", "⌘"}} {
			if strings.Contains(part, m.name+"-") {
				mods += m.sym
			}
		}
		key := (Chord{Key: ch.Key}).labelOn(true)
		key = strings.ReplaceAll(key, "Backspace", "⌫")
		chords = append(chords, mods+key)
	}
	return strings.Join(chords, " ")
}

// A key alone in one context that begins a key of two in another is
// reported: Dispatch claims the first key wherever the focus is.
func TestPrefixAcrossContexts(t *testing.T) {
	_, errs := Parse([]byte(`[
		{"context": "Navigator", "bindings": {"ctrl-g": "navigator::Refresh"}},
		{"context": "Editor", "bindings": {"ctrl-g ctrl-g": "editor::Format"}}
	]`))
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "never run") {
		t.Fatalf("errors %v", errs)
	}
}

package dataview

import (
	"strings"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// jsonLine is the height of a line of a JSON tree.
const jsonLine = 19

// jsonTree shows a JSON document as a tree, colored as the editor colors
// JSON, its objects and arrays folding; a click on a key copies the
// expression reading it. Its owner keeps it across frames.
type jsonTree struct {
	text   string
	node   *jsonNode // nil for a text not read, or not JSON
	err    error
	folded map[string]bool
	// copied is the expression copied last, at copiedAt.
	copied   string
	copiedAt time.Time
}

// read reads text when the tree holds another, forgetting its folds:
// objects and arrays from depth foldFrom start folded, none for 0.
func (t *jsonTree) read(text string, foldFrom int) {
	if t.folded != nil && t.text == text {
		return
	}
	*t = jsonTree{text: text, folded: map[string]bool{}}
	t.node, t.err = parseJSON(strings.TrimSpace(text))
	if t.node == nil || foldFrom == 0 {
		return
	}
	var fold func(n *jsonNode, depth int)
	fold = func(n *jsonNode, depth int) {
		if len(n.children) == 0 {
			return
		}
		if depth >= foldFrom {
			t.folded[n.id] = true
		}
		for _, ch := range n.children {
			fold(ch, depth+1)
		}
	}
	fold(t.node, 0)
}

// hint says what a click on a key does, or what it last copied.
func (t *jsonTree) hint(c *ui.Context) string {
	if t.copied != "" && c.Now().Sub(t.copiedAt) < 2*time.Second {
		c.After(2*time.Second - c.Now().Sub(t.copiedAt))
		return "Copied " + t.copied
	}
	return "Click a key to copy the expression reading it"
}

// view shows the tree read, at most maxLines lines of it; the keys copy
// expressions reading column in engine's SQL. It reports whether the
// document went on past the lines shown.
func (t *jsonTree) view(c *ui.Context, engine db.Engine, column string, maxLines int) bool {
	pal := widgets.PaletteOf(c)
	punct := pal.Syntax[sqltext.Operator]
	mono := func(c *ui.Context, s string, color ui.Color) ui.Element {
		return ui.Text(c, s).Font(widgets.MonoFont).FontSize(12).TextColor(color)
	}
	lines := 0
	var walk func(n *jsonNode, depth int, last bool)
	walk = func(n *jsonNode, depth int, last bool) {
		if lines >= maxLines {
			return
		}
		lines++
		id := n.id
		comma := ","
		if last {
			comma = ""
		}
		ui.Row(c.Key(id)).Height(jsonLine).AlignItems(ui.Center).Padding(0, 0, 0, float32(depth*14)).Children(func() {
			if n.kind != 0 && len(n.children) > 0 {
				open := !t.folded[id]
				icon := widgets.IconChevronRight
				if open {
					icon = widgets.IconDropdown
				}
				if ui.ButtonBase(c.Key("fold")).Label("Fold").Size(14, jsonLine).Padding(0).Children(func() {
					ui.Icon(c, icon).FontSize(10).TextColor(pal.Muted)
				}).Clicked() {
					t.folded[id] = open
				}
			} else {
				ui.Box(c).Width(14)
			}
			if n.parent != nil {
				label := n.label
				keyColor := pal.Syntax[sqltext.QuotedIdent]
				if n.index >= 0 {
					keyColor = pal.Muted // an array's places are not keys
				}
				k := ui.ButtonBase(c.Key("key")).Label(label).Height(jsonLine).Padding(0, 2).Radius(3).Cursor(ui.CursorPointer).Children(func() {
					mono(c, label, keyColor)
					mono(c, ":", punct)
				})
				if k.Hovered() {
					k.Background(pal.Hover)
				}
				if k.Clicked() {
					expr := JSONPath(engine, column, n.path(), n.kind == 0)
					c.WriteClipboard(expr)
					t.copied, t.copiedAt = expr, c.Now()
				}
				ui.Box(c).Width(6)
			}
			switch {
			case n.kind == 0:
				ui.Row(c).Shrink(1).Children(func() {
					mono(c, n.scalar, scalarColor(pal, n.scalar)).SingleLine().Shrink(1)
					mono(c, comma, punct)
				})
			case len(n.children) == 0:
				mono(c, string(n.kind)+closer(n.kind)+comma, punct)
			case t.folded[id]:
				mono(c, string(n.kind)+" … "+closer(n.kind)+comma, punct)
				ui.Text(c, "  "+n.size()).FontSize(11).TextColor(pal.Muted)
			default:
				mono(c, string(n.kind), punct)
			}
		})
		if n.kind == 0 || len(n.children) == 0 || t.folded[id] {
			return
		}
		for i, ch := range n.children {
			walk(ch, depth+1, i == len(n.children)-1)
		}
		lines++
		mono(c.Key(id+"\x00end"), closer(n.kind)+comma, punct).Height(jsonLine).Padding(0, 0, 0, float32(depth*14+14))
	}
	walk(t.node, 0, true)
	return lines >= maxLines
}

// size counts an object's keys or an array's items.
func (n *jsonNode) size() string {
	if n.kind == '[' {
		return widgets.Count(len(n.children), "item")
	}
	return widgets.Count(len(n.children), "key")
}

func closer(open byte) string {
	if open == '[' {
		return "]"
	}
	return "}"
}

// scalarColor colors a JSON scalar as the editor colors SQL: strings,
// numbers, and true, false and null as keywords.
func scalarColor(pal *widgets.Palette, s string) ui.Color {
	switch {
	case strings.HasPrefix(s, `"`):
		return pal.Syntax[sqltext.String]
	case s == "true" || s == "false" || s == "null":
		return pal.Syntax[sqltext.Keyword]
	}
	return pal.Syntax[sqltext.Number]
}

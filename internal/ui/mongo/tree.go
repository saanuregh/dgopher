package mongo

import (
	"strconv"

	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// treeLine is the height of a line of a document tree.
const treeLine = 20

// docTree shows documents as trees of their fields, typed as the console
// writes them, objects and arrays folding. Its owner keeps it across
// frames. It is not dataview's JSON tree, which reads JSON text: it would
// show an ObjectId or a date as an object of $-keys, and its keys copy a
// SQL expression.
type docTree struct {
	// flipped are the nodes folded or unfolded from how they start, by
	// their path.
	flipped map[string]bool
}

// treeRoot is a document of a tree: its label, and the document it was
// before an edit, whose changed fields show what they were; nil for none.
type treeRoot struct {
	label string
	doc   bson.Raw
	was   bson.Raw
}

// view shows the roots, at most maxLines lines of them. A lone document
// starts unfolded; of several, the first. It reports whether the lines
// ran out.
func (t *docTree) view(c *ui.Context, roots []treeRoot, maxLines int) bool {
	if t.flipped == nil {
		t.flipped = map[string]bool{}
	}
	pal := widgets.PaletteOf(c)
	mono := func(s string, color ui.Color) ui.Element {
		return ui.Text(c, s).Font(widgets.MonoFont).FontSize(widgets.TextSmall).TextColor(color).SingleLine()
	}
	lines := 0
	var walk func(id, key string, v bson.RawValue, was *bson.RawValue, depth int, startsOpen bool)
	walk = func(id, key string, v bson.RawValue, was *bson.RawValue, depth int, startsOpen bool) {
		if lines >= maxLines {
			return
		}
		lines++
		nested := v.Type == bson.TypeEmbeddedDocument || v.Type == bson.TypeArray
		keys, vals := children(v)
		open := nested && len(vals) > 0 && startsOpen != t.flipped[id]
		ui.Row(c.Key(id)).Height(treeLine).AlignItems(ui.Center).Gap(4).Padding(0, 0, 0, float32(depth*16)).Children(func() {
			if nested && len(vals) > 0 {
				icon, label := widgets.IconChevronRight, "Unfold "+key
				if open {
					icon, label = widgets.IconDropdown, "Fold "+key
				}
				if widgets.SmallIconButton(c.Key("fold"), icon, label).Clicked() {
					t.flipped[id] = !t.flipped[id]
				}
			} else {
				ui.Box(c).Width(18)
			}
			if key != "" {
				mono(key, pal.Syntax[sqltext.QuotedIdent])
				mono(":", pal.Muted)
			}
			if nested {
				size := "{" + strconv.Itoa(len(vals)) + "}"
				if v.Type == bson.TypeArray {
					size = "[" + strconv.Itoa(len(vals)) + "]"
				}
				mono(size, pal.Muted)
			} else {
				mono(scalarText(v), valueColor(pal, v.Type)).Shrink(1)
			}
			switch {
			case was != nil && was.Type == 0:
				ui.Text(c, "new").FontSize(widgets.TextMeta).TextColor(pal.Muted).SingleLine()
			case was != nil:
				ui.Text(c, "was "+widgets.OneLine(shellLine(*was), 40)).FontSize(widgets.TextMeta).TextColor(pal.Muted).Strikethrough().SingleLine().Shrink(1)
			}
		})
		if !open {
			return
		}
		for i, ch := range vals {
			label := strconv.Itoa(i)
			if keys != nil {
				label = keys[i]
			}
			walk(id+"\x00"+label, label, ch, nil, depth+1, true)
		}
	}
	for i, r := range roots {
		id := strconv.Itoa(i)
		v := bson.RawValue{Type: bson.TypeEmbeddedDocument, Value: r.doc}
		open := (i == 0) != t.flipped[id]
		keys, vals := children(v)
		// A root without a label is its fields alone, as the document
		// beside the rows, whose bar names it.
		depth := 0
		if r.label != "" {
			depth = 1
			lines++
			t.rootRow(c, id, r.label, v, open)
		} else {
			open = true
		}
		if !open {
			continue
		}
		for j, ch := range vals {
			var was *bson.RawValue
			if r.was != nil {
				old, err := r.was.LookupErr(keys[j])
				if err != nil {
					old = bson.RawValue{} // the field is new
				}
				if !sameValue(old, ch) {
					was = &old
				}
			}
			walk(id+"\x00"+keys[j], keys[j], ch, was, depth, true)
		}
		if lines >= maxLines {
			return true
		}
	}
	return lines >= maxLines
}

// rootRow is the line of a document of the tree: its label, and its text
// while folded.
func (t *docTree) rootRow(c *ui.Context, id, label string, v bson.RawValue, open bool) {
	pal := widgets.PaletteOf(c)
	ui.Row(c.Key(id)).Height(treeLine).AlignItems(ui.Center).Gap(4).Children(func() {
		icon, name := widgets.IconChevronRight, "Unfold "+label
		if open {
			icon, name = widgets.IconDropdown, "Fold "+label
		}
		if widgets.SmallIconButton(c.Key("fold"), icon, name).Clicked() {
			t.flipped[id] = !t.flipped[id]
		}
		ui.Text(c, label).FontSize(widgets.TextSmall).FontWeight(600).SingleLine()
		if !open {
			ui.Text(c, widgets.OneLine(shellLine(v), 120)).Font(widgets.MonoFont).FontSize(widgets.TextMeta).TextColor(pal.Muted).SingleLine().Shrink(1)
		}
	})
}

// children are an object's keys and values, or an array's values with
// nil keys.
func children(v bson.RawValue) ([]string, []bson.RawValue) {
	switch v.Type {
	case bson.TypeEmbeddedDocument:
		els, _ := v.Document().Elements()
		keys := make([]string, len(els))
		vals := make([]bson.RawValue, len(els))
		for i, e := range els {
			keys[i], vals[i] = e.Key(), e.Value()
		}
		return keys, vals
	case bson.TypeArray:
		vals, _ := v.Array().Values()
		return nil, vals
	}
	return nil, nil
}

// valueColor colors a value as the editor colors code: strings, numbers,
// and literals and helpers as keywords.
func valueColor(pal *widgets.Palette, t bson.Type) ui.Color {
	switch t {
	case bson.TypeString:
		return pal.Syntax[sqltext.String]
	case bson.TypeDouble, bson.TypeInt32, bson.TypeInt64, bson.TypeDecimal128:
		return pal.Syntax[sqltext.Number]
	case bson.TypeNull, bson.TypeUndefined:
		return pal.Null
	}
	return pal.Syntax[sqltext.Keyword]
}

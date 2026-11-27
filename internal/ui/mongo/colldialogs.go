package mongo

import (
	"strconv"
	"strings"

	"dgopher/internal/redact"
	"dgopher/internal/ui/editor"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// The document beside the rows, and the dialogs of the collection tab:
// a field's value, a new document, and the statements Save runs.

// detailView shows the chosen document as a tree, with what its edited
// fields were, or as text to edit.
func (t *CollectionTab) detailView(c *ui.Context, readOnly string) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	shown := t.shownDocs()
	place := t.detail
	doc := shown[place]
	state := ""
	switch {
	case place >= len(t.read):
		state = "new"
	case t.edits.deleted[place]:
		state = "deleted"
	case t.edits.changed[place] != nil:
		state = "edited"
	}
	ui.Column(c).Fill().BorderWidth(0, 0, 0, 1).BorderColor(th.Border).Children(func() {
		widgets.Bar(c, "Document", func() {
			widgets.BarTitle(c, func() {
				ui.Text(c, "Document "+strconv.Itoa(place+1)).FontWeight(600).SingleLine()
				if state != "" {
					ui.Text(c, "· "+state).FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine()
				}
			})
			ui.Spacer(c)
			ui.Row(c.Key("detail-mode")).Gap(1).Padding(1).Radius(widgets.RadiusMedium).Background(th.Surface).Label("Show the document as").Children(func() {
				if widgets.ToggleIconButton(c, widgets.IconSchema, "Tree", !t.detailJSON).Tooltip("Tree: its fields, and what edited ones were").Clicked() {
					t.detailJSON = false
				}
				if widgets.ToggleIconButton(c, widgets.IconCode, "JSON", t.detailJSON).Tooltip("JSON: the document as text, to edit").Clicked() {
					t.detailJSON, t.detailOf = true, -1
				}
			})
			if widgets.IconButton(c, widgets.IconX, "Close").Tooltip("Close the document").Clicked() {
				t.detail = -1
			}
		})
		if !t.detailJSON {
			ui.Scroll(c).Grow(1).FillWidth().Padding(6, 10).Children(func() {
				root := treeRoot{doc: doc}
				if state == "edited" {
					root.was = t.read[place]
				}
				t.view.tree.view(c, []treeRoot{root}, treeLines)
			})
			return
		}
		if t.detailOf != t.edits.gen {
			t.detailText, t.detailOf, t.detailErr = shellDocument(doc), t.edits.gen, ""
		}
		why := readOnly
		if why == "" && state == "deleted" {
			why = "The document is marked for deletion"
		}
		t.detailArea.ReadOnly = why != ""
		ui.Column(c).Grow(1).FillWidth().Padding(6).Children(func() {
			el, _ := t.detailArea.View(c, &t.detailText)
			el.Grow(1).FillWidth()
		})
		if t.detailErr != "" {
			ui.Text(c, t.detailErr).FontSize(widgets.TextSmall).TextColor(th.Danger).Padding(0, 10).Selectable()
		}
		ui.Row(c).Padding(widgets.PadStatusY, widgets.PadStatusX).Gap(8).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
			if why != "" {
				ui.Icon(c, widgets.IconLock).TextColor(pal.Muted).FontSize(widgets.IconSmall)
				ui.Text(c, widgets.FirstLine(why)).FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine().Shrink(1).Tooltip(why)
				return
			}
			ui.Spacer(c)
			if state == "edited" && widgets.StatusButton(c, "Revert").Tooltip("Drop this document's changes").Clicked() {
				delete(t.edits.changed, place)
				t.edits.gen++
			}
			if widgets.StatusPrimaryButton(c, "Keep Changes").Tooltip("Keep the text as the document's pending change").Clicked() {
				t.keepDetail(place)
			}
		})
	})
}

// keepDetail keeps the text edited as the document at place.
func (t *CollectionTab) keepDetail(place int) {
	d, err := parseDocument(t.detailText, "The text")
	if err == nil && d == nil {
		d = bson.D{}
	}
	var raw bson.Raw
	if err == nil {
		raw, err = rawOf(d)
	}
	if err != nil {
		t.detailErr = err.Error()
		return
	}
	t.detailErr = ""
	t.setShown(place, raw)
}

// setShown makes doc the document shown at place: a read one's pending
// change, or the new one's content.
func (t *CollectionTab) setShown(place int, doc bson.Raw) {
	if t.refused(t.readOnly()) {
		return
	}
	if place >= len(t.read) {
		t.edits.inserted[place-len(t.read)] = doc
		t.edits.gen++
		return
	}
	t.edits.edit(t.read, place, doc)
}

// fieldEdit edits one top-level field of a document.
type fieldEdit struct {
	open  bool
	place int
	key   string
	text  string
	err   string
	had   bool // the document has the field
}

func (t *CollectionTab) openFieldEdit(place int, key string) {
	f := &fieldEdit{open: true, place: place, key: key}
	if v, err := t.shownDocs()[place].LookupErr(key); err == nil {
		f.text, f.had = shellLine(v), true
	}
	t.field = f
}

// keepField sets or removes the field edited.
func (t *CollectionTab) keepField(remove bool) {
	f := t.field
	if f.key == "_id" && f.place < len(t.read) {
		f.err = "A document's _id cannot change: duplicate it and delete this one instead."
		return
	}
	var v any
	if !remove {
		var err error
		if v, err = parseValue(f.text); err != nil {
			f.err = err.Error()
			return
		}
		if strings.TrimSpace(f.text) == "" {
			f.err = `Type a value, as "paid", 12 or null, or remove the field.`
			return
		}
	}
	var doc bson.D
	found := false
	for _, e := range elementsOf(t.shownDocs()[f.place]) {
		if e.Key == f.key {
			found = true
			if remove {
				continue
			}
			e.Value = v
		}
		doc = append(doc, e)
	}
	if !found && !remove {
		doc = append(doc, bson.E{Key: f.key, Value: v})
	}
	raw, err := rawOf(doc)
	if err != nil {
		f.err = err.Error()
		return
	}
	t.setShown(f.place, raw)
	t.field = nil
}

// insertDialog writes a new document.
type insertDialog struct {
	open bool
	text string
	area editor.CodeArea
	err  string
}

// openInsert opens the dialog on doc, or an empty document for nil.
func (t *CollectionTab) openInsert(doc bson.D) {
	text := "{\n  \n}"
	if doc != nil {
		if raw, err := rawOf(doc); err == nil {
			text = shellDocument(raw)
		}
	}
	t.insert = &insertDialog{open: true, text: text, area: editor.CodeArea{Label: "New document", Language: editor.LanguageJSON, AutoFocus: true}}
}

// keepInsert adds the document written as a new one, with an _id when it
// has none, as mongosh gives it.
func (t *CollectionTab) keepInsert() {
	if t.refused(t.readOnly()) {
		return
	}
	in := t.insert
	d, err := parseDocument(in.text, "The new document")
	if err != nil {
		in.err = err.Error()
		return
	}
	if _, ok := fieldOf(d, "_id"); !ok {
		d = append(bson.D{{Key: "_id", Value: bson.NewObjectID()}}, d...)
	}
	raw, err := rawOf(d)
	if err != nil {
		in.err = err.Error()
		return
	}
	t.edits.inserted = append(t.edits.inserted, raw)
	t.edits.gen++
	t.insert = nil
	g := t.view.grid
	t.view.set(t.shownDocs(), t.shownKey[0]<<32|t.shownKey[1])
	g.SelRow = len(g.ViewOrder(&t.view.src)) - 1
	g.List.ScrollToEnd()
}

// statementsDialog shows the statements Save runs.
type statementsDialog struct {
	open bool
	text string
	area editor.CodeArea
}

func (t *CollectionTab) openStatements() {
	text := ""
	plan, err := t.edits.plan(t.read, t.Collection)
	if err != nil {
		text = "// " + err.Error()
	} else {
		texts := make([]string, len(plan.ops))
		for i, op := range plan.ops {
			texts[i] = redact.Mongo(op.Text)
		}
		text = strings.Join(texts, "\n")
	}
	t.statements = &statementsDialog{open: true, text: text, area: editor.CodeArea{Label: "Statements", Language: editor.LanguageJSON, ReadOnly: true, SoftWrap: true}}
}

// dialogs draws the dialogs open.
func (t *CollectionTab) dialogs(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	if f := t.field; f != nil {
		widgets.Modal(c, &f.open, widgets.DialogSmall, func() {
			widgets.DialogTitle(c, f.key+" of document "+strconv.Itoa(f.place+1))
			ui.Text(c, `As the console writes values: "paid", 259.0, NumberLong(7), ISODate("2026-01-31T12:00:00Z"), null.`).FontSize(widgets.TextSmall).TextColor(pal.Muted)
			in := ui.TextInput(c, &f.text).Font(widgets.MonoFont).FillWidth().AutoFocus().Label("Value")
			if f.err != "" {
				ui.Text(c, f.err).FontSize(widgets.TextSmall).TextColor(c.Theme().Danger).Selectable()
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if f.had && ui.Button(c, "Remove Field").Tooltip("Remove "+f.key+" from the document").Clicked() {
					t.keepField(true)
				}
				ui.Spacer(c)
				if widgets.DialogFooter(c, &f.open, "Keep", in.Submitted()) && t.field == f {
					t.keepField(false)
				}
			})
		})
		if t.field != nil && !t.field.open {
			t.field = nil
		}
	}
	if in := t.insert; in != nil {
		widgets.Modal(c, &in.open, widgets.DialogMedium, func() {
			widgets.DialogTitle(c, "Insert into "+t.Title())
			ui.Text(c, "Written as the console writes documents. Without an _id, it gets a new ObjectId. Nothing is written until you save.").FontSize(widgets.TextSmall).TextColor(pal.Muted)
			el, _ := in.area.View(c, &in.text)
			el.Height(260).FillWidth()
			if in.err != "" {
				ui.Text(c, in.err).FontSize(widgets.TextSmall).TextColor(c.Theme().Danger).Selectable()
			}
			if widgets.DialogFooter(c, &in.open, "Add", false) {
				t.keepInsert()
			}
		})
		if t.insert != nil && !t.insert.open {
			t.insert = nil
		}
	}
	if s := t.statements; s != nil {
		widgets.Modal(c, &s.open, widgets.DialogLarge, func() {
			widgets.DialogTitle(c, "The statements Save runs")
			el, _ := s.area.View(c, &s.text)
			el.Height(240).FillWidth()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Copy").Clicked() {
					t.a.WriteClipboard(s.text)
				}
				if ui.PrimaryButton(c, "Close").Clicked() {
					s.open = false
				}
			})
		})
		if !s.open {
			t.statements = nil
		}
	}
}

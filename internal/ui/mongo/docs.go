package mongo

import (
	"strconv"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/editor"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// The presentations of documents.
const (
	showGrid = iota
	showJSON
	showTree
)

// treeLines bounds the lines a tree of many documents shows.
const treeLines = 4000

// docView shows documents as a grid of their top-level fields, as text
// in the console's syntax, or as trees. Its owner gives it the documents
// with a generation that changes when they do.
type docView struct {
	grid *dataview.Grid
	src  dataview.Source
	cols []column
	docs []bson.Raw
	gen  int // of the documents the grid and text were made for; -1 for none
	mode int

	text     string
	textGen  int
	textArea editor.CodeArea
	tree     docTree
}

func newDocView() *docView {
	v := &docView{grid: dataview.NewGrid(), gen: -1, textGen: -1}
	v.textArea = editor.CodeArea{Label: "Documents", Language: editor.LanguageJSON, ReadOnly: true, NoBorder: true}
	return v
}

// set gives the view the documents of generation gen.
func (v *docView) set(docs []bson.Raw, gen int) {
	if gen == v.gen {
		return
	}
	v.gen, v.docs = gen, docs
	v.cols = columnsOf(docs)
	v.src.Cols = make([]db.ColumnInfo, len(v.cols))
	for i, col := range v.cols {
		v.src.Cols[i] = db.ColumnInfo{Name: col.name, Type: col.typ}
	}
	v.src.Rows = make([][]any, len(docs))
	for r, d := range docs {
		row := make([]any, len(v.cols))
		for i, col := range v.cols {
			val, err := d.LookupErr(col.name)
			if err != nil {
				val = bson.RawValue{}
			}
			row[i] = cellValue(val)
		}
		v.src.Rows[r] = row
	}
}

// reset starts the view over for documents of another statement.
func (v *docView) reset() {
	v.grid.Reset()
	v.tree = docTree{}
}

// chosen is the document of the grid's chosen row, -1 for none.
func (v *docView) chosen() int {
	order := v.grid.ViewOrder(&v.src)
	if v.grid.SelRow < 0 || v.grid.SelRow >= len(order) {
		return -1
	}
	return order[v.grid.SelRow]
}

// modeButtons switch the presentation, on a track as a segmented
// control's.
func (v *docView) modeButtons(c *ui.Context) {
	ui.Row(c.Key("presentation")).Gap(1).Padding(1).Radius(widgets.RadiusMedium).Background(c.Theme().Surface).AlignItems(ui.Center).Shrink(0).
		Label("Show the documents as").Children(func() {
		for _, p := range []struct {
			mode       int
			icon       *ui.SVG
			label, tip string
		}{
			{showGrid, widgets.IconTable, "Grid", "Grid: the top-level fields as columns"},
			{showJSON, widgets.IconCode, "JSON", "JSON: the documents as the console writes them"},
			{showTree, widgets.IconSchema, "Tree", "Tree: each document's fields, folding"},
		} {
			if widgets.ToggleIconButton(c, p.icon, p.label, v.mode == p.mode).Tooltip(p.tip).Clicked() {
				v.mode = p.mode
			}
		}
	})
}

// body shows the documents as the mode has them, roots giving the
// tree's. It returns the grid's table, and false in the other modes.
func (v *docView) body(c *ui.Context, a Host, roots func() []treeRoot) (ui.Element, bool) {
	switch v.mode {
	case showJSON:
		if v.textGen != v.gen {
			v.text, v.textGen = documentsText(v.docs), v.gen
		}
		text := v.text
		el, _ := v.textArea.View(c, &text)
		el.Grow(1).FillWidth()
		return ui.Element{}, false
	case showTree:
		ui.Scroll(c).Grow(1).FillWidth().Padding(6, 10).Children(func() {
			if v.tree.view(c, roots(), treeLines) {
				ui.Text(c, "The tree stops here: open one document, or show the documents as JSON.").FontSize(widgets.TextSmall).TextColor(widgets.PaletteOf(c).Muted)
			}
		})
		return ui.Element{}, false
	}
	return v.grid.View(c, a, &v.src), true
}

// documentsText is documents as the console writes them, in an array.
func documentsText(docs []bson.Raw) string {
	var b strings.Builder
	b.WriteString("[\n")
	for i, d := range docs {
		for j, line := range strings.Split(shellDocument(d), "\n") {
			if j > 0 {
				b.WriteString("\n")
			}
			b.WriteString("  " + line)
		}
		if i < len(docs)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("]")
	return b.String()
}

// plainRoots are documents as roots of a tree, numbered from 1.
func plainRoots(docs []bson.Raw) []treeRoot {
	out := make([]treeRoot, len(docs))
	for i, d := range docs {
		out[i] = treeRoot{label: "Document " + strconv.Itoa(i+1), doc: d}
	}
	return out
}

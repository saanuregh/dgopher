package mongo

import (
	"fmt"
	"slices"
	"strconv"

	"dgopher/internal/keymap"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// View builds the tab: the find's bar, the documents' tools, the
// documents and the one chosen beside them, and the status of both.
func (t *CollectionTab) View(c *ui.Context) {
	a := t.a
	keys := a.KeysTo(t)
	pressed := func(id string) bool { return keys && keymap.Pressed(c, id) }
	readOnly := t.readOnly()
	shown := t.shownDocs()
	t.view.set(shown, t.shownKey[0]<<32|t.shownKey[1])
	t.view.grid.SetTint(t.tint)
	g := t.view.grid
	listed := func(id string) bool { return keymap.ListPressed(c, &g.List, id) }
	switch {
	case pressed(keymap.Apply):
		t.Save()
	case pressed(keymap.Discard) && t.edits.count() > 0:
		t.Discard()
	case pressed(keymap.Refresh):
		t.Find()
	case listed(keymap.FetchNext):
		t.LoadMore()
	case listed(keymap.FetchAll):
		t.FetchAll()
	case listed(keymap.AddRow) && readOnly == "":
		t.openInsert(nil)
	case listed(keymap.DuplicateRow) && readOnly == "":
		t.duplicate()
	case listed(keymap.DeleteRows) && readOnly == "":
		t.deleteChosen()
	}
	ui.Column(c).Grow(1).Children(func() {
		t.findBar(c)
		if t.inputErr != "" {
			widgets.NoticeBar(c, c.Theme().Danger, func() {
				ui.Text(c, t.inputErr).FontSize(widgets.TextSmall).Shrink(1).Selectable()
			}).Label("The find does not read")
		}
		t.toolsBar(c, readOnly)
		if t.loadErr != "" {
			widgets.ErrorBanner(c, widgets.ErrorReport{Title: "Could not read the documents", Detail: t.loadErr}, a.ShowReport)
		}
		body := func() {
			table, isGrid := t.view.body(c, a, t.treeRoots)
			if isGrid && table.Submitted() {
				t.openChosen()
			}
			// A double click on a cell, or the value panel's key, opens
			// the document beside the rows: the grid's own panel shows
			// one value as text.
			if g.ShowValue {
				g.ShowValue = false
				t.showDetail(t.view.chosen())
			}
			if listed(keymap.EditValue) {
				t.openChosen()
			}
		}
		if t.detail >= 0 && t.detail < len(shown) {
			ui.SplitPercent(c, &t.split, func() {
				ui.Column(c).Fill().Children(body)
			}, func() {
				t.detailView(c, readOnly)
			}).Grow(1)
		} else {
			ui.Column(c).Grow(1).Children(body)
		}
		// The status says the documents are being read already.
		t.statusBar(c, widgets.When(readOnly != beingRead, readOnly))
	})
	t.dialogs(c)
}

// findBar is the filter, sort and projection, and Find.
func (t *CollectionTab) findBar(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	submitted := false
	field := func(key, label, placeholder string, value *string, width float32) {
		box := ui.Row(c.Key(key)).Gap(6).Padding(0, 6).Radius(th.Radius).Background(th.Surface).Border(1, th.Border).AlignItems(ui.Center)
		if width > 0 {
			box.Width(width).Shrink(1)
		} else {
			box.Grow(1)
		}
		box.Children(func() {
			ui.Text(c, label).FontSize(widgets.TextMeta).Bold().TextColor(pal.Muted).Shrink(0)
			in := ui.TextInputBase(c, value).Font(widgets.MonoFont).FontSize(widgets.TextBody).Placeholder(placeholder).
				Grow(1).Padding(4, 2).Label(label)
			if in.Submitted() {
				submitted = true
			}
			if in.Focused() {
				box.Border(1, th.Accent)
			}
			if key == "filter" {
				if want := t.a.FocusWant(); *want == "filter" && t.a.KeysTo(t) && !in.Focused() && in.Focus().Focused() {
					*want = ""
				}
			}
		})
	}
	widgets.Bar(c, "Find", func() {
		field("filter", "FILTER", `{ status: "paid" }`, &t.FilterIn, 0)
		field("sort", "SORT", "{ _id: -1 }", &t.SortIn, 190)
		field("fields", "FIELDS", "{ name: 1 }", &t.FieldsIn, 150)
		why := t.savingWhy()
		if widgets.Clicked(widgets.LeadToolButton(c.Key("find"), widgets.IconPlay, "Find", "Find the documents matching the filter (Enter)", th.Success), why) {
			submitted = true
		}
		if widgets.Clicked(widgets.IconButton(c.Key("refresh"), widgets.IconRefresh, "Refresh").Tooltip(widgets.KeyLabel(keymap.Hint("Read the documents again", keymap.Refresh))), why) {
			submitted = true
		}
	}).Gap(8)
	if submitted {
		t.Find()
	}
}

// toolsBar switches the presentation and adds, duplicates and deletes
// documents. The buttons keep their place while the documents cannot be
// edited, and say why.
func (t *CollectionTab) toolsBar(c *ui.Context, readOnly string) {
	pal := widgets.PaletteOf(c)
	widgets.Bar(c, "Documents", func() {
		t.view.modeButtons(c)
		if t.view.mode == showGrid {
			ui.Text(c, keymap.Hint("Nested values show compact; a double click opens one beside the rows", keymap.EditValue)).FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine().Shrink(1)
		}
		ui.Spacer(c)
		rowWhy := readOnly
		if rowWhy == "" && t.view.chosen() < 0 {
			rowWhy = "Choose a document first"
		}
		tool := func(key string, ic *ui.SVG, label, tip, why string) bool {
			return widgets.Clicked(widgets.ToolButton(c.Key(key), ic, label, tip), why)
		}
		if tool("insert", widgets.IconPlus, "Insert…", keymap.Hint("Insert a document", keymap.AddRow), readOnly) {
			t.openInsert(nil)
		}
		if tool("duplicate", widgets.IconCopy, "Duplicate", keymap.Hint("Insert a copy of the chosen document", keymap.DuplicateRow), rowWhy) {
			t.duplicate()
		}
		if tool("delete", widgets.IconTrash, "Delete", keymap.Hint("Mark the chosen documents for deletion", keymap.DeleteRows), rowWhy) {
			t.deleteChosen()
		}
	})
}

// statusBar is the pending changes and what is done with them, and how
// many documents are read.
func (t *CollectionTab) statusBar(c *ui.Context, readOnly string) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	pending := t.edits.count()
	bar := ui.Row(c.Key("status")).Padding(widgets.PadStatusY, widgets.PadStatusX).Gap(8).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(th.Border)
	if pending > 0 {
		bar.Background(pal.Modified)
	}
	bar.Children(func() {
		switch {
		case pending > 0:
			ui.Text(c, t.edits.summary()).Bold().FontSize(widgets.TextSmall).SingleLine().Shrink(0).
				Tooltip("Nothing is written until you save.")
			saveWhy := t.savingWhy()
			if widgets.Clicked(widgets.StatusPrimaryButton(c.Key("save"), "Save…").Tooltip(keymap.Hint("Review the statements, then save them", keymap.Apply)),
				t.busyWhy()) {
				t.Save()
			}
			if widgets.Clicked(widgets.StatusButton(c.Key("discard"), "Discard").Tooltip(keymap.Hint("Drop every pending change", keymap.Discard)), saveWhy) {
				t.Discard()
			}
			if widgets.StatusButton(c.Key("statements"), "Show Statements").Tooltip("The statements Save runs").Clicked() {
				t.openStatements()
			}
			if t.saving && widgets.Lasted(c, t.saveStart) &&
				widgets.Appear(widgets.StatusButton(c.Key("stop-save"), "Stop")).Tooltip("Stop before the next change").Clicked() {
				t.StopSave()
			}
		case readOnly != "":
			ui.Row(c).Gap(4).Shrink(1).MinWidth(0).Children(func() {
				ui.Icon(c, widgets.IconLock).TextColor(pal.Muted).FontSize(widgets.IconSmall)
				ui.Text(c, widgets.FirstLine(readOnly)).FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine().Shrink(1).Tooltip(readOnly)
			})
		}
		ui.Spacer(c)
		muted := func(s string) ui.Element {
			return ui.Text(c, s).FontSize(widgets.TextSmall).TextColor(pal.Muted).SingleLine()
		}
		n := len(t.read)
		status := widgets.Count(n, "document")
		switch {
		case t.loading && n == 0:
			status = "Reading the documents…"
		case t.total >= 0 && !t.done:
			status = fmt.Sprintf("%s of %s documents", strconv.Itoa(n), widgets.HumanCount(t.total))
		case !t.done:
			status += " loaded"
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			muted(status)
			if n > 0 || !t.loading {
				muted("in " + widgets.FormatDuration(t.elapsed)).Tooltip("The time the documents took to read")
			}
			if t.done || t.cursor == nil {
				return
			}
			busy := t.busyWhy()
			if widgets.Clicked(widgets.Appear(widgets.StatusButton(c.Key("load-more"), "Load More")).Tooltip(keymap.Hint("Read the next documents", keymap.FetchNext)), busy) {
				t.LoadMore()
			}
			if t.fetchingAll {
				if widgets.Appear(widgets.StatusButton(c.Key("fetch-all"), "Stop")).Tooltip("Stop after the page being read").Clicked() {
					t.StopFetchAll()
				}
				return
			}
			if widgets.Clicked(widgets.Appear(widgets.StatusButton(c.Key("fetch-all"), "Fetch All")).Tooltip(keymap.Hint("Read every document", keymap.FetchAll)), busy) {
				t.FetchAll()
			}
		})
	})
}

// tint colours the cells of the pending changes: a new document's, a
// deleted one's, and a changed field's.
func (t *CollectionTab) tint(row, col int, pal *widgets.Palette) (ui.Color, bool) {
	switch {
	case row >= len(t.read):
		return pal.Inserted, true
	case t.edits.deleted[row]:
		return pal.Deleted, true
	}
	d, ok := t.edits.changed[row]
	if !ok || col >= len(t.view.cols) {
		return ui.Color{}, false
	}
	key := t.view.cols[col].name
	was, err1 := t.read[row].LookupErr(key)
	now, err2 := d.LookupErr(key)
	if (err1 != nil) != (err2 != nil) || err1 == nil && !sameValue(was, now) {
		return pal.Modified, true
	}
	return ui.Color{}, false
}

// treeRoots are the documents shown as a tree, each with what it was
// before its edit.
func (t *CollectionTab) treeRoots() []treeRoot {
	shown := t.shownDocs()
	roots := plainRoots(shown)
	for i := range roots {
		if d, ok := t.edits.changed[i]; ok && i < len(t.read) {
			roots[i].doc, roots[i].was = d, t.read[i]
		}
	}
	return roots
}

// openChosen opens the chosen cell: a nested value beside the rows, a
// scalar in its editor, or the document when it cannot be edited.
func (t *CollectionTab) openChosen() {
	place := t.view.chosen()
	if place < 0 {
		return
	}
	col := t.chosenColumn()
	if col < 0 {
		t.showDetail(place)
		return
	}
	key := t.view.cols[col].name
	v, err := t.shownDocs()[place].LookupErr(key)
	nested := err == nil && (v.Type == bson.TypeEmbeddedDocument || v.Type == bson.TypeArray)
	if nested || t.readOnly() != "" || t.edits.deleted[place] {
		t.showDetail(place)
		return
	}
	t.openFieldEdit(place, key)
}

// chosenColumn is the chosen cell's column, -1 for none.
func (t *CollectionTab) chosenColumn() int {
	cols := t.view.grid.List.Cells.Cursor
	if i, err := strconv.Atoi(cols); err == nil && i >= 0 && i < len(t.view.cols) {
		return i
	}
	return -1
}

// showDetail shows the document at place beside the rows.
func (t *CollectionTab) showDetail(place int) {
	if place < 0 {
		return
	}
	t.detail, t.detailErr, t.detailOf = place, "", -1
}

// deleteChosen marks the documents chosen for deletion, or keeps them
// when they all are; a new one chosen goes.
func (t *CollectionTab) deleteChosen() {
	if t.refused(t.readOnly()) {
		return
	}
	rows := t.view.grid.SelectedRows(&t.view.src)
	restore := true
	for _, r := range rows {
		if r < len(t.read) && !t.edits.deleted[r] {
			restore = false
		}
	}
	var gone []int
	for _, r := range rows {
		switch {
		case r >= len(t.read):
			gone = append(gone, r-len(t.read))
		case restore:
			delete(t.edits.deleted, r)
		default:
			t.edits.deleted[r] = true
		}
	}
	slices.Sort(gone)
	for _, j := range slices.Backward(gone) {
		t.edits.inserted = slices.Delete(t.edits.inserted, j, j+1)
	}
	if len(gone) > 0 {
		t.detail = -1
	}
	t.edits.gen++
}

// duplicate opens the insert dialog on a copy of the chosen document,
// without its _id.
func (t *CollectionTab) duplicate() {
	place := t.view.chosen()
	if place < 0 {
		return
	}
	var doc bson.D
	for _, e := range elementsOf(t.shownDocs()[place]) {
		if e.Key != "_id" {
			doc = append(doc, e)
		}
	}
	t.openInsert(doc)
}

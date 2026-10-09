// Package dataview shows rows: the grid of a result or a table, its panels,
// filters, generated SQL, chart and export, the table tab and the ER
// diagram.
package dataview

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/export"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// unset marks a cell of a new row that the user left to its default.
type unsetValue struct{}

var unset = unsetValue{}

// pendingEdits are changes of a grid not yet applied to the database.
type pendingEdits struct {
	updates  map[int]map[int]any // data row → column → new value
	deleted  map[int]bool
	inserted [][]any // rows added, each cell a value or unset
}

func newPendingEdits() *pendingEdits {
	return &pendingEdits{updates: map[int]map[int]any{}, deleted: map[int]bool{}}
}

func (p *pendingEdits) count() int {
	if p == nil {
		return 0
	}
	n := len(p.deleted) + len(p.inserted)
	for _, cols := range p.updates {
		n += len(cols)
	}
	return n
}

func (p *pendingEdits) clear() {
	clear(p.updates)
	clear(p.deleted)
	p.inserted = nil
}

// Source is what a grid shows: the rows read so far.
type Source struct {
	Cols []db.ColumnInfo
	Rows [][]any
}

// Grid shows rows in columns, with a chosen cell, and edits them when
// edits is set.
type Grid struct {
	// numeric caches numericColumn for the columns and rows it was
	// worked out for.
	numeric     []bool
	numericFrom *db.ColumnInfo
	numericRows int
	List        ui.ListState
	SelRow      int // in view order
	selection   ui.Selection[int]
	selCol      int
	sort        ui.SortOrder
	// serverSort leaves the order to the database: the grid reports a new
	// sort instead of sorting the rows itself.
	serverSort  bool
	sortChanged bool
	Filter      string

	Order    []int // view row → data row
	orderKey string
	widths   []float32
	colsKey  string

	editing   *cellEdit
	edits     *pendingEdits
	readOnly  string // why the rows cannot be edited, when edits is set
	ShowValue bool
	// menu adds items of the grid's owner to a cell's context menu.
	menu func(m *ui.Menu, row, col int)
	// SQLOpts writes copied rows as SQL of the grid's connection.
	SQLOpts export.Options
	// noDefaultUpdate is set for engines whose UPDATE cannot set DEFAULT.
	noDefaultUpdate bool
	// conds filter the rows read, as the Filter menu sets them on results.
	conds []rowCond
	// filterSQL, when set, takes the Filter menu's conditions instead, as
	// a table does, to filter on the server.
	filterSQL func(rowCond)
	clearSQL  func(col int)
	// masked are the columns whose values show as maskedText, and
	// valuesMenu offers to hide or show a column's.
	masked     []bool
	valuesMenu func(m *ui.Menu, col int)
	// hidden and pinned are columns the header menu hid or froze, by index
	// in the result.
	hidden, pinned map[int]bool
	// undoStack and redoStack are the pending edits as they were before
	// each change, and as they were before each undo.
	undoStack, redoStack []*pendingEdits
	// panel is the panel shown when showValue is set (panelNames), and
	// panelMax has it take the grid's place.
	panel    int
	panelMax bool
	// The Value panel's viewer, its image, and the cell they are for.
	viewer   string
	bitmap   *ui.Bitmap
	valueKey string
	// The Grouping panel's columns, and its groups when counted on the
	// server; groupOnServer counts them there, for a table.
	groupCols     []int
	groupRows     []groupRow
	groupServer   bool
	groupErr      string
	groupOnServer func(cols []int, then func([]groupRow, error))
	// The Profile panel's profiles of the rows read, for profileKey's rows;
	// of every row when profiled on the server, as profileOnServer does.
	profiles        []columnProfile
	profileKey      string
	profileServer   []columnProfile
	profileErr      string
	profiling       bool
	profileOnServer func(then func([]columnProfile, error))
	// referencesOf reads the rows of other tables that refer to a row,
	// for a table; refsRow is the row they were read for.
	referencesOf func(row int, then func([]refRows, error))
	refsRow      int
	refsShown    []refRows
	refsErr      string
	refsLoading  bool
	// Exported records an export of rows in the audit log, for the
	// grid's owner, who knows the connection.
	Exported func(detail string, rows int)
	// colorRules color rows by their values; colorsChanged keeps them, as
	// a table does in its project.
	colorRules    []colorRule
	colorsChanged func([]colorRule)
	// gen counts the times the grid showed new rows, for work that ends
	// after a reload to tell its rows are gone.
	gen int
	// mode is the presentation: viewGrid, viewRecord or viewText; recList
	// and recRow are the record's list and its chosen field, text the
	// text view's, for textKey.
	mode    int
	recList ui.ListState
	recRow  int
	text    string
	textKey string
	// viewW is the table's width in the last frame, which fitting the
	// columns to the screen shares out.
	viewW float32
	// distinctOf, when set, reads a column's distinct values on the
	// server, as a table does; else they are counted in the rows read.
	distinctOf func(col int, then func([]distinctValue, error))
	// keyCols are the columns of the rows' key, which a duplicated row
	// leaves to their defaults.
	keyCols map[int]bool
	// readOnlyCols say why columns cannot be edited when the rows can, by
	// index: a query's column that is not its table's.
	readOnlyCols map[int]string
}

type cellEdit struct {
	row, col int // data row, column
	value    string
	initial  string // the text the edit started with
	focused  bool
}

func NewGrid() *Grid {
	g := &Grid{SelRow: -1, refsRow: -1}
	g.List.Selected = &g.SelRow
	g.List.Selection = &g.selection
	g.List.Sort = &g.sort
	return g
}

// reset forgets what the grid showed, for a new result.
func (g *Grid) reset() {
	g.SelRow, g.selCol = -1, 0
	g.selection.Clear()
	g.Order, g.orderKey, g.widths, g.colsKey = nil, "", nil, ""
	g.hidden, g.pinned, g.conds = nil, nil, nil
	g.refsRow, g.refsShown, g.groupRows, g.valueKey = -1, nil, nil, ""
	g.gen++
	// Edits kept for undo name rows by their place: other rows now.
	g.undoStack, g.redoStack = nil, nil
	g.editing = nil
	if !g.serverSort {
		g.sort = ui.SortOrder{}
	}
}

func colID(i int) string { return strconv.Itoa(i) }

// ViewOrder returns the data rows in the order shown, filtered.
func (g *Grid) ViewOrder(src *Source) []int {
	inserted := 0
	if g.edits != nil {
		inserted = len(g.edits.inserted)
	}
	key := fmt.Sprintf("%d/%d/%v/%s/%v", len(src.Rows), inserted, sortKeys(g.sort, len(src.Cols)), g.Filter, g.conds)
	if key == g.orderKey && g.Order != nil {
		return g.Order
	}
	g.orderKey = key
	g.Order = g.Order[:0]
	filter := strings.ToLower(strings.TrimSpace(g.Filter))
	for i, row := range src.Rows {
		if filter != "" && !rowMatches(row, filter) || !g.passes(row) {
			continue
		}
		g.Order = append(g.Order, i)
	}
	if keys := sortKeys(g.sort, len(src.Cols)); len(keys) > 0 && !g.serverSort {
		slices.SortStableFunc(g.Order, func(x, y int) int {
			for _, k := range keys {
				col, _ := strconv.Atoi(k.Column)
				c := compareValues(src.Rows[x][col], src.Rows[y][col])
				if k.Descending {
					c = -c
				}
				if c != 0 {
					return c
				}
			}
			return 0
		})
	}
	for i := range inserted {
		g.Order = append(g.Order, len(src.Rows)+i)
	}
	if g.Order == nil {
		g.Order = []int{}
	}
	return g.Order
}

func rowMatches(row []any, filter string) bool {
	for _, v := range row {
		if strings.Contains(strings.ToLower(db.Display(v)), filter) {
			return true
		}
	}
	return false
}

// compareValues orders values for sorting: NULLs first, numbers by value,
// the rest by their text.
func compareValues(a, b any) int {
	if a == nil || b == nil {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return -1
		}
		return 1
	}
	if fa, ok := toFloat(a); ok {
		if fb, ok := toFloat(b); ok {
			return cmp.Compare(fa, fb)
		}
	}
	if ta, ok := a.(time.Time); ok {
		if tb, ok := b.(time.Time); ok {
			return ta.Compare(tb)
		}
	}
	return strings.Compare(db.Display(a), db.Display(b))
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case int:
		return float64(x), true
	case uint64:
		return float64(x), true
	case uint32:
		return float64(x), true
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		return f, true
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// columnWidths sizes the columns to their names and first rows.
func (g *Grid) columnWidths(src *Source) []float32 {
	key := fmt.Sprint(len(src.Cols), src.Cols)
	if key == g.colsKey && g.widths != nil {
		return g.widths
	}
	g.colsKey = key
	g.widths = make([]float32, len(src.Cols))
	for i, col := range src.Cols {
		n := len([]rune(col.Name)) + 2
		for r := 0; r < len(src.Rows) && r < 100; r++ {
			n = max(n, len([]rune(db.Cell(src.Rows[r][i], 60))))
		}
		g.widths[i] = float32(min(max(n*7+28, 72), 380))
	}
	return g.widths
}

// value returns a cell's value as shown: pending changes included.
func (g *Grid) value(src *Source, row, col int) (any, bool) {
	if row >= len(src.Rows) {
		if g.edits == nil {
			return nil, false
		}
		return g.edits.inserted[row-len(src.Rows)][col], true
	}
	if g.edits != nil {
		if v, ok := g.edits.updates[row][col]; ok {
			return v, true
		}
	}
	return src.Rows[row][col], false
}

// View builds the grid; it returns its table.
func (g *Grid) View(c *ui.Context, a Host, src *Source) ui.Element {
	pal := widgets.PaletteOf(c)
	order := g.ViewOrder(src)
	widths := g.columnWidths(src)
	g.List.Key = func(i int) any { return order[i] }
	cols := make([]ui.TableColumn, 0, len(src.Cols)+1)
	digits := len(strconv.Itoa(len(order) + 1))
	cols = append(cols, ui.TableColumn{Title: "#", ID: "#", Width: float32(digits*8 + 18), Fixed: true, Frozen: true, Align: ui.End})
	visible := make([]int, 0, len(src.Cols))
	for i, col := range src.Cols {
		if g.hidden[i] {
			continue
		}
		visible = append(visible, i)
		align := ui.Start
		if db.IsNumericType(col.Type) {
			align = ui.End
		}
		cols = append(cols, ui.TableColumn{Title: col.Name, ID: colID(i), Width: widths[i], MinWidth: 40, Align: align, Sortable: true, Frozen: g.pinned[i]})
	}
	g.List.HeaderMenu = func(m *ui.Menu, col int) {
		if col > 0 {
			g.headerMenu(m, a, src, visible[col-1])
		} else {
			g.showAllMenu(m)
		}
	}
	sortBefore := g.sort
	sortBefore.Then = slices.Clone(g.sort.Then)
	var table ui.Element
	// Rows as dense as a spreadsheet's: the theme's spacing sizes them.
	theme := c.Theme()
	dense := *theme
	dense.Spacing = 2.5
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		g.presentationBar(c)
		switch g.mode {
		case viewRecord:
			g.recordView(c, a, src, order)
			return
		case viewText:
			g.textView(c, a, src, order)
			return
		}
		c.SetTheme(&dense)
		table = ui.Table(c, &g.List, cols, len(order), func(r, col int) {
			if r >= len(order) {
				return
			}
			data := order[r]
			if col == 0 {
				num := ui.Text(c, strconv.Itoa(r+1)).FontSize(11).TextColor(pal.Muted).SingleLine()
				if g.edits != nil {
					switch {
					case data >= len(src.Rows):
						num.Background(pal.Inserted)
					case g.edits.deleted[data]:
						num.Background(pal.Deleted)
					}
				}
				return
			}
			g.cell(c, a, src, r, data, visible[col-1])
		}).Grow(1).Label("Results").Draw(func(_ *ui.Painter, r ui.Rect) { g.viewW = r.W })
		c.SetTheme(theme)
		if g.ShowValue {
			g.panelsView(c, a, src, order)
		}
	})
	if !sameSort(g.sort, sortBefore) {
		g.sortChanged = true
	}
	// Tab shows the chosen row as a record, ⌘` switches presentations.
	if g.mode == viewGrid && g.List.Shortcut(c, 0, ui.KeyTab) {
		g.mode = viewRecord
	}
	if c.Shortcut(ui.Cmd, ui.KeyBackquote) {
		g.mode = (g.mode + 1) % 3
	}
	g.moveKeys(c, a, src, order)
	if g.List.Shortcut(c, 0, ui.KeyF7) || g.recList.Shortcut(c, 0, ui.KeyF7) {
		g.ShowValue = !g.ShowValue
	}
	// Keys: Left and Right move between cells, as in a spreadsheet.
	if g.List.Shortcut(c, 0, ui.KeyLeft) {
		g.moveCol(src, -1)
	}
	if g.List.Shortcut(c, 0, ui.KeyRight) {
		g.moveCol(src, 1)
	}
	if g.List.Shortcut(c, ui.Cmd, ui.KeyC) {
		a.WriteClipboard(g.copySelection(src, export.TSV, false))
	}
	if g.List.Shortcut(c, ui.Cmd|ui.Shift, ui.KeyC) {
		openAdvancedCopy(a, g, src)
	}
	if g.List.Shortcut(c, ui.Cmd, ui.KeyV) {
		pasteInto(a, g, src, c.ReadClipboard(), pasteOptions{NullText: "NULL"})
	}
	if g.List.Shortcut(c, ui.Cmd|ui.Shift, ui.KeyV) {
		openAdvancedPaste(a, g, src)
	}
	if g.edits != nil && g.readOnly == "" {
		g.editKeys(c, src, order)
		if g.List.Shortcut(c, 0, ui.KeyF2) || table.Submitted() {
			if g.SelRow >= 0 && g.SelRow < len(order) {
				g.startEdit(src, order[g.SelRow], g.selCol)
			}
		}
		if g.List.Shortcut(c, 0, ui.KeyDelete) || g.List.Shortcut(c, ui.Cmd, ui.KeyBackspace) {
			g.deleteSelected(src)
		}
	}
	return table
}

func (g *Grid) cell(c *ui.Context, a Host, src *Source, viewRow, data, col int) {
	pal := widgets.PaletteOf(c)
	t := c.Theme()
	if e := g.editing; e != nil && e.row == data && e.col == col {
		in := ui.TextInputBase(c, &e.value).Font(widgets.MonoFont).FontSize(12).FillWidth().Padding(1, 4).
			Background(t.Background).Border(1, t.Accent).Radius(3).Label("Cell value")
		if !e.focused {
			in.AutoFocus().Focus()
		}
		switch {
		case in.Shortcut(0, ui.KeyEscape):
			g.editing = nil
		case in.Submitted():
			g.commitEdit(src)
		case e.focused && !in.Focused():
			g.commitEdit(src)
		}
		if in.Focused() {
			e.focused = true
		}
		return
	}
	v, changed := g.value(src, data, col)
	box := ui.Box(c).FillWidth().Padding(0, 2)
	selected := viewRow == g.SelRow && col == g.selCol
	if selected {
		box.Border(1, t.AccentText).Radius(2)
	}
	if color, ok := g.rowColor(src, data); ok {
		box.Background(ui.Hex(color).Alpha(0.45))
	}
	if g.edits != nil {
		switch {
		case data >= len(src.Rows):
			box.Background(pal.Inserted)
		case g.edits.deleted[data]:
			box.Background(pal.Deleted)
		case changed:
			box.Background(pal.Modified)
		}
	}
	box.Children(func() {
		var txt ui.Element
		switch {
		case v == unset || v == db.Default:
			txt = ui.Text(c, "DEFAULT").TextColor(pal.Null).Italic()
		case v == nil:
			txt = ui.Text(c, a.Settings().ViewFormat.Format(nil)).TextColor(pal.Null).Italic()
		case g.isMasked(col):
			txt = ui.Text(c, maskedText).TextColor(pal.Muted)
		default:
			if typed, ok := v.(db.Typed); ok {
				v = string(typed)
			}
			txt = ui.Text(c, cellText(a.Settings().ViewFormat.Format(v), 200))
		}
		if g.numericColumn(src, col) {
			txt.TextAlign(ui.End).FillWidth()
		}
		txt.SingleLine().FontSize(a.Settings().GridFontSize())
		if g.edits != nil && g.edits.deleted[data] {
			txt.Strikethrough()
		}
	})
	if box.Clicked() {
		g.selCol = col
		g.SelRow = viewRow
		if box.ClickModifiers()&(ui.Cmd|ui.Shift) == 0 {
			g.selection.Clear()
			g.selection.Add(data)
		}
	}
	if box.DoubleClicked() {
		if g.edits != nil && g.columnReadOnly(col) == "" {
			g.startEdit(src, data, col)
		} else {
			g.ShowValue = true
		}
	}
	box.ContextMenu(func(m *ui.Menu) {
		g.selCol = col
		g.cellMenu(m, a, src, data, col)
	})
}

func (g *Grid) startEdit(src *Source, row, col int) {
	if g.edits != nil && g.edits.deleted[row] || g.readOnlyCols[col] != "" {
		return
	}
	v, _ := g.value(src, row, col)
	text := ""
	switch x := v.(type) {
	case nil, unsetValue:
	case db.Typed:
		text = string(x)
	default:
		text = db.Display(x)
	}
	g.editing = &cellEdit{row: row, col: col, value: text, initial: text}
}

func (g *Grid) commitEdit(src *Source) {
	e := g.editing
	g.editing = nil
	if e == nil || g.edits == nil || e.value == e.initial {
		return // untouched: a NULL stays NULL, not ''
	}
	g.checkpoint()
	g.setValue(src, e.row, e.col, db.Typed(e.value))
}

// columnReadOnly says why a column's cells cannot be edited, "" when they
// can.
func (g *Grid) columnReadOnly(col int) string {
	if g.readOnly != "" {
		return g.readOnly
	}
	return g.readOnlyCols[col]
}

// setValue records a new value of a cell, nil for NULL.
func (g *Grid) setValue(src *Source, row, col int, v any) {
	if g.readOnlyCols[col] != "" {
		return
	}
	if row >= len(src.Rows) {
		g.edits.inserted[row-len(src.Rows)][col] = v
		return
	}
	orig := src.Rows[row][col]
	if typed, ok := v.(db.Typed); ok && orig != nil && string(typed) == db.Display(orig) {
		delete(g.edits.updates[row], col)
		return
	}
	if v == nil && orig == nil {
		delete(g.edits.updates[row], col)
		return
	}
	if g.edits.updates[row] == nil {
		g.edits.updates[row] = map[int]any{}
	}
	g.edits.updates[row][col] = v
}

func (g *Grid) selectedRows(src *Source) []int {
	var rows []int
	for _, r := range g.ViewOrder(src) {
		if g.selection.Has(r) {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 && g.SelRow >= 0 && g.SelRow < len(g.Order) {
		rows = []int{g.Order[g.SelRow]}
	}
	return rows
}

func (g *Grid) deleteSelected(src *Source) {
	g.checkpoint()
	rows := g.selectedRows(src)
	// From the last: an added row taken out moves those after it.
	slices.SortFunc(rows, func(x, y int) int { return y - x })
	for _, r := range rows {
		if r >= len(src.Rows) {
			i := r - len(src.Rows)
			if i < len(g.edits.inserted) {
				g.edits.inserted = slices.Delete(g.edits.inserted, i, i+1)
			}
			continue
		}
		if g.edits.deleted[r] {
			delete(g.edits.deleted, r)
		} else {
			g.edits.deleted[r] = true
		}
	}
	g.orderKey = ""
}

// addRow adds a new row, left to its defaults, and starts editing it.
func (g *Grid) addRow(src *Source) {
	g.checkpoint()
	row := make([]any, len(src.Cols))
	for i := range row {
		row[i] = unset
	}
	g.edits.inserted = append(g.edits.inserted, row)
	g.orderKey = ""
	order := g.ViewOrder(src)
	g.SelRow = len(order) - 1
	g.selection.Clear()
	g.List.ScrollToEnd()
	g.selCol = 0
	g.editing = &cellEdit{row: len(src.Rows) + len(g.edits.inserted) - 1, col: 0}
}

// copySelection writes the rows chosen, or the cell chosen when one row
// is, in a format.
func (g *Grid) copySelection(src *Source, f export.Format, header bool) string {
	rows := g.selectedRows(src)
	if len(rows) == 1 && !header && f == export.TSV && g.selCol < len(src.Cols) {
		v, _ := g.value(src, rows[0], g.selCol)
		if v == nil || v == unset {
			return ""
		}
		return db.Display(v)
	}
	names := make([]string, len(src.Cols))
	for i, col := range src.Cols {
		names[i] = col.Name
	}
	data := make([][]any, 0, len(rows))
	for _, r := range rows {
		vals := make([]any, len(src.Cols))
		for col := range src.Cols {
			v, _ := g.value(src, r, col)
			if v == unset || v == db.Default {
				v = nil
			}
			if typed, ok := v.(db.Typed); ok {
				v = string(typed)
			}
			vals[col] = v
		}
		data = append(data, vals)
	}
	opt := g.SQLOpts
	opt.Header, opt.NullText = header || f == export.CSV, "NULL"
	out, err := export.Text(f, names, data, opt)
	if err != nil {
		return ""
	}
	return strings.TrimRight(out, "\n")
}

func (g *Grid) cellMenu(m *ui.Menu, a Host, src *Source, row, col int) {
	if m.Item("Copy").Shortcut(ui.Cmd, ui.KeyC).Chosen() {
		a.WriteClipboard(g.copySelection(src, export.TSV, false))
	}
	if m.Item("Copy with Header").Chosen() {
		a.WriteClipboard(g.copySelection(src, export.TSV, true))
	}
	if m.Item("Advanced Copy…").Shortcut(ui.Cmd|ui.Shift, ui.KeyC).Chosen() {
		openAdvancedCopy(a, g, src)
	}
	editable := g.edits != nil && g.readOnly == ""
	if m.Item("Paste").Shortcut(ui.Cmd, ui.KeyV).Disabled(!editable).Chosen() {
		pasteInto(a, g, src, a.ReadClipboard(), pasteOptions{NullText: "NULL"})
	}
	if m.Item("Advanced Paste…").Shortcut(ui.Cmd|ui.Shift, ui.KeyV).Disabled(!editable).Chosen() {
		openAdvancedPaste(a, g, src)
	}
	m.Submenu("Copy as", func(m *ui.Menu) {
		for _, f := range []export.Format{export.CSV, export.TSV, export.JSON, export.SQL, export.Markdown} {
			if m.Item(f.Label()).Chosen() {
				a.WriteClipboard(g.copyText(a, src, string(f)))
			}
		}
		if m.Item("Plain Text Table").Chosen() {
			a.WriteClipboard(g.copyText(a, src, "text"))
		}
	})
	m.Submenu("Panels", func(m *ui.Menu) {
		for i, name := range panelNames {
			if m.Item(name).Checked(g.ShowValue && g.panel == i).Chosen() {
				g.ShowValue, g.panel = true, i
			}
		}
		m.Separator()
		if m.Item("Show Panels").Checked(g.ShowValue).Shortcut(0, ui.KeyF7).Chosen() {
			g.ShowValue = !g.ShowValue
		}
	})
	formatMenu(a, m)
	g.colorMenu(m, src, row, col)
	if m.Item("Open in Spreadsheet App").Chosen() {
		g.openWith(a, src)
	}
	apply, clearF := g.filterFuncs()
	m.Separator()
	g.filterMenu(m, a, src, row, col, apply, clearF)
	g.orderMenu(m, col)
	if g.menu != nil {
		g.menu(m, row, col)
	}
	if g.edits != nil && g.readOnly == "" {
		m.Separator()
		g.editMenu(m, src, row, col)
	}
}

// PrettyValue indents JSON, and leaves other text as it is.
func PrettyValue(s string) string {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) > 1 && (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid([]byte(trimmed)) {
		var b bytes.Buffer
		if json.Indent(&b, []byte(trimmed), "", "  ") == nil {
			return b.String()
		}
	}
	return s
}

// SQLOptionsFor returns how rows of a connection are written as SQL.
func SQLOptionsFor(cn *connection.Conn, table string) export.Options {
	opt := export.Options{Table: table, Literal: literalOf(cn.Config.Engine)}
	if cn.DB != nil {
		opt.Quote = cn.DB.Dialect.Quote
	}
	return opt
}

// sameSort reports whether two orders sort alike.
func sameSort(x, y ui.SortOrder) bool {
	return x.Column == y.Column && x.Descending == y.Descending && slices.Equal(x.Then, y.Then)
}

// numericColumn reports whether a column holds numbers, which line up on
// the right, NULLs included. Its type says so; without one, as for an
// expression in SQLite, its first value that is not NULL does.
func (g *Grid) numericColumn(src *Source, col int) bool {
	if len(src.Cols) == 0 {
		return false
	}
	if g.numericFrom != &src.Cols[0] || g.numericRows != len(src.Rows) || len(g.numeric) != len(src.Cols) {
		g.numeric = make([]bool, len(src.Cols))
		for i, c := range src.Cols {
			if c.Type != "" {
				g.numeric[i] = db.IsNumericType(c.Type)
				continue
			}
			for _, row := range src.Rows[:min(len(src.Rows), 1000)] {
				if i < len(row) && row[i] != nil {
					g.numeric[i] = db.IsNumeric(row[i])
					break
				}
			}
		}
		g.numericFrom, g.numericRows = &src.Cols[0], len(src.Rows)
	}
	return col < len(g.numeric) && g.numeric[col]
}

// moveCol moves the chosen cell to the next column shown, left or right.
func (g *Grid) moveCol(src *Source, step int) {
	for col := g.selCol + step; col >= 0 && col < len(src.Cols); col += step {
		if !g.hidden[col] {
			g.selCol = col
			return
		}
	}
}

// isMasked reports whether a column's values are hidden on screen.
func (g *Grid) isMasked(col int) bool { return col >= 0 && col < len(g.masked) && g.masked[col] }

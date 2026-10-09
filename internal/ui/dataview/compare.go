package dataview

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// rowSnapshot is the rows of a result kept to compare with another's.
type rowSnapshot struct {
	title string
	cols  []string
	rows  [][]any
	// key names the columns that tell the rows apart, when they are a
	// table's; masked those whose values the grid hid.
	key    []string
	masked []string
}

// snapshot keeps the rows read, which later reads leave as they are.
func (v *Viewer) snapshot() *rowSnapshot {
	s := &rowSnapshot{title: "result", rows: slices.Clone(v.src.Rows)}
	if v.source.Table != nil {
		s.title = v.source.Table.Name
	}
	s.title += " at " + v.a.Now().Format(time.TimeOnly)
	for i, c := range v.src.Cols {
		s.cols = append(s.cols, c.Name)
		if v.grid.isMasked(i) {
			s.masked = append(s.masked, c.Name)
		}
	}
	if key, _, err := v.keyColumns(); err == nil && v.source.Table != nil {
		if !slices.ContainsFunc(key, func(k string) bool { return !slices.Contains(s.cols, k) }) {
			s.key = key
		}
	}
	return s
}

// compareMenu pins the rows to compare, or compares them with those
// pinned.
func (v *Viewer) compareMenu(m *ui.Menu) {
	pinned := v.a.Dialogs().pinned
	if m.Item("Pin These Rows for Comparison").Disabled(len(v.src.Cols) == 0).Chosen() {
		v.a.Dialogs().pinned = v.snapshot()
		v.a.Toast("Pinned the rows: compare another result with them", "", nil)
	}
	if pinned == nil {
		m.Item("Compare with Pinned Rows").Disabled(true)
		return
	}
	if m.Item("Compare with " + pinned.title).Chosen() {
		v.a.AddTab(newCompareTab(pinned, v.snapshot()))
	}
	if m.Item("Unpin " + pinned.title).Chosen() {
		v.a.Dialogs().pinned = nil
	}
}

// Kinds of the rows of a comparison.
const (
	rowSame = iota
	rowChanged
	rowOnlyA
	rowOnlyB
)

// rowDiff is a row of a comparison: of both results, matched, or of one.
// Its values follow the comparison's columns, and diff marks those that
// differ.
type rowDiff struct {
	kind int
	a, b []any
	diff []bool
}

// missing is the value of a column a result does not have.
type missingValue struct{}

var missing = missingValue{}

// compareRows matches the rows of two results by the values of key, or
// by their position without one, and tells how they differ over the
// columns of both, those of one only included.
func compareRows(a, b *rowSnapshot, key []string) (cols []string, diffs []rowDiff) {
	cols = slices.Clone(a.cols)
	for _, c := range b.cols {
		if !slices.Contains(cols, c) {
			cols = append(cols, c)
		}
	}
	align := func(s *rowSnapshot, row []any) []any {
		out := make([]any, len(cols))
		for i, c := range cols {
			out[i] = missing
			if j := slices.Index(s.cols, c); j >= 0 {
				out[i] = row[j]
			}
		}
		return out
	}
	pair := func(x, y []any) rowDiff {
		d := rowDiff{kind: rowSame, a: x, b: y, diff: make([]bool, len(cols))}
		for i := range cols {
			if !sameValue(x[i], y[i]) {
				d.diff[i], d.kind = true, rowChanged
			}
		}
		return d
	}
	keyOf := func(row []any) string {
		parts := make([]string, len(key))
		for i, k := range key {
			parts[i] = fmt.Sprintf("%T:%s", row[slices.Index(cols, k)], db.Display(row[slices.Index(cols, k)]))
		}
		return strings.Join(parts, "\x1f")
	}
	if len(key) == 0 {
		for i := range max(len(a.rows), len(b.rows)) {
			switch {
			case i >= len(b.rows):
				diffs = append(diffs, rowDiff{kind: rowOnlyA, a: align(a, a.rows[i])})
			case i >= len(a.rows):
				diffs = append(diffs, rowDiff{kind: rowOnlyB, b: align(b, b.rows[i])})
			default:
				diffs = append(diffs, pair(align(a, a.rows[i]), align(b, b.rows[i])))
			}
		}
		return cols, diffs
	}
	inB := map[string]int{}
	for i, row := range b.rows {
		inB[keyOf(align(b, row))] = i
	}
	matched := make([]bool, len(b.rows))
	for _, row := range a.rows {
		x := align(a, row)
		if j, ok := inB[keyOf(x)]; ok && !matched[j] {
			matched[j] = true
			diffs = append(diffs, pair(x, align(b, b.rows[j])))
		} else {
			diffs = append(diffs, rowDiff{kind: rowOnlyA, a: x})
		}
	}
	for j, row := range b.rows {
		if !matched[j] {
			diffs = append(diffs, rowDiff{kind: rowOnlyB, b: align(b, row)})
		}
	}
	return cols, diffs
}

// sameValue reports whether two values are equal, as the grid shows them.
func sameValue(x, y any) bool {
	switch {
	case x == missing || y == missing:
		return x == y
	case x == nil || y == nil:
		return x == nil && y == nil
	}
	return compareValues(x, y) == 0
}

// CompareTab shows two results side by side: the rows of both, matched,
// with what differs marked.
type CompareTab struct {
	a, b *rowSnapshot
	// matchBy is "" for the rows' position, else a column or "key"; the
	// select shows its label.
	matchBy, matchLabel string
	// shown is 0 for the differences, 1 for every row.
	shown int
	cols  []string
	diffs []rowDiff
	lines []compareLine
}

// compareLine is a line of the comparison's table: a row of A or of B,
// or of both when they are the same.
type compareLine struct {
	diff *rowDiff
	side int // 0 both, 1 A, 2 B
}

func newCompareTab(a, b *rowSnapshot) *CompareTab {
	t := &CompareTab{a: a, b: b}
	labels, values := t.matchChoices()
	at := 0
	if len(values) > 1 && values[1] == "key" {
		at = 1 // the key of A, which B has
	}
	t.matchBy, t.matchLabel = values[at], labels[at]
	t.compare()
	return t
}

// matchChoices are the ways rows can be matched: by position, by the
// key of A when B has its columns, or by a column of both.
func (t *CompareTab) matchChoices() (labels, values []string) {
	labels, values = []string{"Row position"}, []string{""}
	if len(t.a.key) > 0 && !slices.ContainsFunc(t.a.key, func(k string) bool { return !slices.Contains(t.b.cols, k) }) {
		labels, values = append(labels, "Key ("+strings.Join(t.a.key, ", ")+")"), append(values, "key")
	}
	for _, col := range t.a.cols {
		if slices.Contains(t.b.cols, col) {
			labels, values = append(labels, col), append(values, col)
		}
	}
	return labels, values
}

func (t *CompareTab) compare() {
	var key []string
	switch t.matchBy {
	case "":
	case "key":
		key = t.a.key
	default:
		key = []string{t.matchBy}
	}
	t.cols, t.diffs = compareRows(t.a, t.b, key)
	t.lay()
}

// lay puts the rows on lines: one for rows the same, two for rows that
// differ, A's first.
func (t *CompareTab) lay() {
	t.lines = t.lines[:0]
	for i := range t.diffs {
		d := &t.diffs[i]
		switch d.kind {
		case rowSame:
			if t.shown == 1 {
				t.lines = append(t.lines, compareLine{diff: d})
			}
		case rowChanged:
			t.lines = append(t.lines, compareLine{diff: d, side: 1}, compareLine{diff: d, side: 2})
		case rowOnlyA:
			t.lines = append(t.lines, compareLine{diff: d, side: 1})
		case rowOnlyB:
			t.lines = append(t.lines, compareLine{diff: d, side: 2})
		}
	}
}

func (t *CompareTab) counts() (same, changed, onlyA, onlyB int) {
	for _, d := range t.diffs {
		switch d.kind {
		case rowSame:
			same++
		case rowChanged:
			changed++
		case rowOnlyA:
			onlyA++
		case rowOnlyB:
			onlyB++
		}
	}
	return
}

func (t *CompareTab) Title() string                { return t.a.title + " ↔ " + t.b.title }
func (t *CompareTab) Connection() *connection.Conn { return nil }
func (t *CompareTab) CloseReason() string          { return "" }
func (t *CompareTab) Close()                       {}

func (t *CompareTab) View(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	same, changed, onlyA, onlyB := t.counts()
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(8, 12).Gap(12).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, "A: "+t.a.title+"   B: "+t.b.title).Bold().SingleLine().Shrink(1)
			ui.Text(c, fmt.Sprintf("%d the same · %d changed · %d only in A · %d only in B", same, changed, onlyA, onlyB)).
				FontSize(12.5).TextColor(pal.Muted).SingleLine().Grow(1).Shrink(1)
			labels, values := t.matchChoices()
			ui.Text(c, "Match rows by").FontSize(12.5).TextColor(pal.Muted)
			ui.Select(c, &t.matchLabel, labels).Label("Match rows by")
			if by := values[max(0, slices.Index(labels, t.matchLabel))]; by != t.matchBy {
				t.matchBy = by
				t.compare()
			}
			if ui.Segmented(c, &t.shown, "Differences", "All Rows").Label("Rows shown").Changed() {
				t.lay()
			}
		})
		if len(t.lines) == 0 {
			ui.Text(c, "The rows are the same.").TextColor(pal.Muted).Padding(16)
			return
		}
		cols := []ui.TableColumn{{Title: "", ID: "side", Width: 34, Fixed: true}}
		for _, name := range t.cols {
			cols = append(cols, ui.TableColumn{Title: name, Width: 150})
		}
		ui.Table(c, nil, cols, len(t.lines), func(r, col int) {
			line := t.lines[r]
			d := line.diff
			if col == 0 {
				marks := []string{"=", "A", "B"}
				ui.Text(c, marks[line.side]).FontSize(11.5).Bold().TextColor(pal.Muted)
				return
			}
			i := col - 1
			values := d.a
			if line.side == 2 {
				values = d.b
			}
			box := ui.Box(c).FillWidth().Padding(0, 2)
			switch {
			case d.kind == rowOnlyA:
				box.Background(pal.Deleted)
			case d.kind == rowOnlyB:
				box.Background(pal.Inserted)
			case d.diff != nil && d.diff[i]:
				box.Background(pal.Modified)
			}
			masked := slices.Contains(t.a.masked, t.cols[i]) || slices.Contains(t.b.masked, t.cols[i])
			box.Children(func() {
				switch v := values[i]; {
				case v == missing:
					ui.Text(c, "—").TextColor(pal.Muted)
				case v == nil:
					ui.Text(c, "NULL").TextColor(pal.Null).Italic()
				case masked:
					ui.Text(c, MaskedText).TextColor(pal.Muted)
				default:
					ui.Text(c, cellText(db.Display(v), 120)).SingleLine()
				}
			})
		}).Grow(1).Label("Compared rows")
	})
}

package dataview

import (
	"bytes"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/settings"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The panels beside a grid, as DBeaver's: F7 shows them.
var panelNames = []string{"Value", "Calc", "Profile", "Metadata", "Grouping", "References"}

// groupRow is a group of rows: its values, and how many rows it holds.
type groupRow struct {
	vals  []any
	count int64
}

// refRows are rows of a table that refer to the chosen row.
type refRows struct {
	title string
	where string
	cols  []string
	rows  [][]any
	open  func()
}

// panelsView shows the panel chosen at the grid's right; maximized, it
// takes the grid's place.
func (g *Grid) panelsView(c *ui.Context, a Host, src *Source, order []int) {
	th := c.Theme()
	col := ui.Column(c).BorderWidth(0, 0, 0, 1).BorderColor(th.Border)
	if g.panelMax {
		col.Grow(1)
	} else {
		col.Width(360)
	}
	col.Children(func() {
		ui.Row(c).Padding(4, 6).Gap(4).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			name := panelNames[g.panel]
			if ui.Select(c, &name, panelNames).Label("Panel").Grow(1).Changed() {
				g.panel = slices.Index(panelNames, name)
			}
			label := "Maximize the panel"
			if g.panelMax {
				label = "Restore the panel"
			}
			if widgets.IconButton(c, widgets.IconColumns, label).Clicked() {
				g.panelMax = !g.panelMax
			}
			if widgets.IconButton(c, widgets.IconX, keymap.Hint("Close the panels", keymap.ValuePanel)).Clicked() {
				g.ShowValue = false
			}
		})
		row := -1
		if g.SelRow >= 0 && g.SelRow < len(order) {
			row = order[g.SelRow]
		}
		switch g.panel {
		case 0:
			g.valuePanel(c, a, src, row)
		case 1:
			g.calcPanel(c, src)
		case 2:
			g.profilePanel(c, a, src)
		case 3:
			g.metadataPanel(c, src)
		case 4:
			g.groupingPanel(c, a, src)
		case 5:
			g.referencesPanel(c, a, row)
		}
	})
}

// valuePanel shows the whole value of the chosen cell, in the viewer its
// content asks for.
func (g *Grid) valuePanel(c *ui.Context, a Host, src *Source, row int) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if row < 0 || g.selCol >= len(src.Cols) {
		ui.Text(c, "Choose a cell.").TextColor(pal.Muted).Padding(12)
		return
	}
	if g.isMasked(g.selCol) {
		ui.Text(c, "The values of "+src.Cols[g.selCol].Name+" are hidden: Show Values, in its header's menu, shows them.").
			TextColor(pal.Muted).Padding(12)
		return
	}
	v, _ := g.value(src, row, g.selCol)
	if typed, ok := v.(db.Typed); ok {
		v = string(typed)
	}
	var raw []byte
	switch x := v.(type) {
	case []byte:
		raw = x
	case string:
		raw = []byte(x)
	}
	if g.valueKey != fmt.Sprint(row, "/", g.selCol, "/", len(raw)) {
		g.valueKey = fmt.Sprint(row, "/", g.selCol, "/", len(raw))
		g.bitmap, g.viewer = nil, viewerFor(v, raw)
		if b, err := ui.DecodeBitmap(raw); err == nil && len(raw) > 0 {
			g.bitmap, g.viewer = b, "Image"
		}
	}
	text := "NULL"
	if v != nil && v != unset && v != db.Default {
		switch g.viewer {
		case "JSON":
			text = PrettyValue(db.Display(v))
		case "XML":
			text = prettyXML(db.Display(v))
		case "Hex":
			text = hex.Dump(raw)
		default:
			text = a.Settings().ViewFormat.Format(v)
		}
	}
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(6, 8).Gap(6).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, src.Cols[g.selCol].Name).Bold().SingleLine().Grow(1).Shrink(1)
			viewers := []string{"Text", "JSON", "XML", "Hex"}
			if g.bitmap != nil {
				viewers = append(viewers, "Image")
			}
			ui.Select(c, &g.viewer, viewers).Label("Viewer")
		})
		if g.viewer == "Image" && g.bitmap != nil {
			ui.Scroll(c).Grow(1).Children(func() { ui.Image(c, g.bitmap).Padding(10) })
		} else {
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Text(c, text).Font(widgets.MonoFont).FontSize(12).Padding(10).Selectable()
			})
		}
		ui.Row(c).Padding(6, 8).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
			if widgets.IconButton(c, widgets.IconCopy, "Copy the value").Clicked() {
				a.WriteClipboard(text)
			}
			if widgets.IconButton(c, widgets.IconDownload, "Save the value to a file…").Disabled(v == nil).Clicked() {
				data := raw
				if data == nil {
					data = []byte(db.Display(v))
				}
				name := src.Cols[g.selCol].Name
				go func() {
					path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "Save the Value", DefaultPath: name})
					if err != nil || path == "" {
						return
					}
					err = os.WriteFile(path, data, 0o600)
					a.Post(func() {
						if err != nil {
							a.ShowError("Could not save the value", err.Error())
						}
					})
				}()
			}
			if g.edits != nil && g.columnReadOnly(g.selCol) == "" && ui.Button(c, "Load from File…").Clicked() {
				col, gen := g.selCol, g.gen
				go func() {
					paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Load the Value"})
					if err != nil || len(paths) == 0 {
						return
					}
					data, err := readCapped(paths[0], 64<<20)
					a.Post(func() {
						if err == nil && g.gen != gen {
							err = errors.New("the rows were read again meanwhile: choose the cell again")
						}
						if err != nil {
							a.ShowError("Could not load the value", err.Error())
							return
						}
						g.checkpoint()
						if isTextData(data) {
							g.setValue(src, row, col, db.Typed(string(data)))
						} else {
							g.setValue(src, row, col, data)
						}
					})
				}()
			}
		})
	})
}

// viewerFor is the viewer a value's content asks for.
func viewerFor(v any, raw []byte) string {
	s := strings.TrimSpace(db.Display(v))
	switch {
	case raw != nil && !isTextData(raw):
		return "Hex"
	case len(s) > 1 && (s[0] == '{' || s[0] == '['):
		return "JSON"
	case strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">"):
		return "XML"
	}
	return "Text"
}

func isTextData(b []byte) bool {
	return bytes.IndexByte(b, 0) < 0 && strings.ToValidUTF8(string(b), "�") == string(b)
}

func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("the file is larger than %d MB", limit>>20)
	}
	return data, err
}

// prettyXML indents XML, and leaves what does not parse as it is.
func prettyXML(s string) string {
	d := xml.NewDecoder(strings.NewReader(s))
	var b bytes.Buffer
	e := xml.NewEncoder(&b)
	e.Indent("", "  ")
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return s
		}
		if cd, ok := tok.(xml.CharData); ok && strings.TrimSpace(string(cd)) == "" {
			continue
		}
		if err := e.EncodeToken(xml.CopyToken(tok)); err != nil {
			return s
		}
	}
	if e.Flush() != nil {
		return s
	}
	return b.String()
}

// calcStats are DBeaver's Calc of the chosen column over the chosen rows.
type calcStats struct {
	count, distinct, nulls int
	numeric                bool
	sum, avg, min, max     float64
	median                 float64
	mode                   string
	modeCount              int
}

func (g *Grid) calc(src *Source) calcStats {
	var s calcStats
	if g.selCol >= len(src.Cols) {
		return s
	}
	seen := map[string]int{}
	var nums []float64
	s.numeric = true
	for _, r := range g.selectedRows(src) {
		v, _ := g.value(src, r, g.selCol)
		if v == nil || v == unset || v == db.Default {
			s.nulls++
			continue
		}
		s.count++
		text := db.Display(v)
		seen[text]++
		if seen[text] > s.modeCount {
			s.mode, s.modeCount = text, seen[text]
		}
		var f float64
		if _, err := fmt.Sscan(text, &f); err != nil || !db.IsNumeric(v) {
			s.numeric = false
			continue
		}
		nums = append(nums, f)
	}
	s.distinct = len(seen)
	if !s.numeric || len(nums) == 0 {
		s.numeric = false
		return s
	}
	sort.Float64s(nums)
	s.min, s.max = nums[0], nums[len(nums)-1]
	for _, n := range nums {
		s.sum += n
	}
	s.avg = s.sum / float64(len(nums))
	if m := len(nums) / 2; len(nums)%2 == 1 {
		s.median = nums[m]
	} else {
		s.median = (nums[m-1] + nums[m]) / 2
	}
	return s
}

func (g *Grid) calcPanel(c *ui.Context, src *Source) {
	pal := widgets.PaletteOf(c)
	s := g.calc(src)
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(10, 12).Gap(6).Children(func() {
			if g.selCol < len(src.Cols) {
				ui.Text(c, src.Cols[g.selCol].Name+" over the chosen rows").Bold()
			}
			line := func(k, v string) {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, k).TextColor(pal.Muted).Width(110)
					ui.Text(c, v).Font(widgets.MonoFont).FontSize(12.5).Selectable()
				})
			}
			line("Count", fmt.Sprint(s.count))
			line("Distinct", fmt.Sprint(s.distinct))
			line("NULLs", fmt.Sprint(s.nulls))
			if s.numeric && !g.isMasked(g.selCol) {
				num := func(f float64) string { return settings.GroupDigits(fmt.Sprint(f)) }
				line("Sum", num(s.sum))
				line("Average", num(s.avg))
				line("Minimum", num(s.min))
				line("Maximum", num(s.max))
				line("Median", num(s.median))
			}
			if s.modeCount > 1 && !g.isMasked(g.selCol) {
				line("Most frequent", fmt.Sprintf("%s (%d)", cellText(s.mode, 40), s.modeCount))
			}
			ui.Text(c, "Choose rows with ⇧ or ⌘ and a click.").FontSize(12).TextColor(pal.Muted)
		})
	})
}

func (g *Grid) metadataPanel(c *ui.Context, src *Source) {
	pal := widgets.PaletteOf(c)
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(8, 10).Gap(4).Children(func() {
			for i, col := range src.Cols {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, fmt.Sprint(i+1)).FontSize(11).TextColor(pal.Muted).Width(24)
					ui.Text(c, col.Name).Bold().SingleLine().Shrink(1)
					ui.Text(c, col.Type).FontSize(12).TextColor(pal.Muted).SingleLine()
					if g.keyCols[i] {
						ui.Icon(c, widgets.IconKey).FontSize(11).TextColor(c.Theme().Warning)
					}
				})
			}
		})
	})
}

// groups counts the rows read by the values of columns.
func (g *Grid) groups(src *Source, cols []int) []groupRow {
	at := map[string]int{}
	var out []groupRow
	for _, r := range src.Rows {
		key := make([]string, len(cols))
		vals := make([]any, len(cols))
		for i, c := range cols {
			key[i], vals[i] = fmt.Sprintf("%T:%s", r[c], db.Display(r[c])), r[c]
		}
		k := strings.Join(key, "\x00")
		if i, ok := at[k]; ok {
			out[i].count++
			continue
		}
		at[k] = len(out)
		out = append(out, groupRow{vals: vals, count: 1})
	}
	slices.SortStableFunc(out, func(x, y groupRow) int { return int(y.count - x.count) })
	return out[:min(len(out), 1000)]
}

func (g *Grid) groupingPanel(c *ui.Context, a Host, src *Source) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(6, 8).Gap(6).Wrap().BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, "Group by").FontSize(12).TextColor(pal.Muted)
			for i, col := range src.Cols {
				on := slices.Contains(g.groupCols, i)
				if widgets.Pill(c, col.Name, on).Clicked() {
					if on {
						g.groupCols = slices.DeleteFunc(g.groupCols, func(x int) bool { return x == i })
					} else {
						g.groupCols = append(g.groupCols, i)
					}
					g.groupRows, g.groupErr = nil, ""
				}
			}
		})
		if len(g.groupCols) == 0 {
			ui.Text(c, "Choose the columns to group the rows by.").TextColor(pal.Muted).Padding(12)
			return
		}
		ui.Row(c).Padding(4, 8).Gap(8).Children(func() {
			label := "Counted in the rows read"
			if g.groupServer {
				label = "Counted on the server, over the whole table"
			}
			ui.Text(c, label).FontSize(12).TextColor(pal.Muted).Grow(1)
			if g.groupOnServer != nil && ui.Button(c, "Group on the Server").Clicked() {
				cols := slices.Clone(g.groupCols)
				g.groupOnServer(cols, func(rows []groupRow, err error) {
					g.groupRows, g.groupServer = rows, true
					if err != nil {
						g.groupErr = err.Error()
					}
				})
			}
		})
		rows := g.groupRows
		if rows == nil {
			rows, g.groupServer = g.groups(src, g.groupCols), false
		}
		if g.groupErr != "" {
			ui.Text(c, g.groupErr).TextColor(th.Danger).Padding(8).Selectable()
		}
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(4, 8).Children(func() {
				for _, gr := range rows {
					ui.Row(c).Gap(8).PaddingY(2).Children(func() {
						parts := make([]string, len(gr.vals))
						for i, v := range gr.vals {
							parts[i] = cellText(a.Settings().ViewFormat.Format(v), 40)
							if v != nil && g.isMasked(g.groupCols[i]) {
								parts[i] = MaskedText
							}
						}
						ui.Text(c, strings.Join(parts, " · ")).SingleLine().Grow(1).Shrink(1)
						ui.Text(c, fmt.Sprint(gr.count)).Font(widgets.MonoFont).FontSize(12).TextColor(pal.Muted)
					})
				}
			})
		})
	})
}

func (g *Grid) referencesPanel(c *ui.Context, a Host, row int) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if g.referencesOf == nil {
		ui.Text(c, "Open a table to see the rows of other tables that refer to its rows.").TextColor(pal.Muted).Padding(12)
		return
	}
	if row < 0 {
		ui.Text(c, "Choose a row.").TextColor(pal.Muted).Padding(12)
		return
	}
	if g.refsRow != row {
		g.refsRow, g.refsShown, g.refsErr, g.refsLoading = row, nil, "", true
		g.referencesOf(row, func(refs []refRows, err error) {
			if g.refsRow != row {
				return
			}
			g.refsShown, g.refsLoading = refs, false
			if err != nil {
				g.refsErr = err.Error()
			}
		})
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(8, 10).Gap(12).Children(func() {
			switch {
			case g.refsLoading:
				ui.Spinner(c)
				return
			case g.refsErr != "":
				ui.Text(c, g.refsErr).TextColor(th.Danger).Selectable()
				return
			case len(g.refsShown) == 0:
				ui.Text(c, "No table refers to this row.").TextColor(pal.Muted)
				return
			}
			for _, r := range g.refsShown {
				ui.Column(c).Gap(4).Children(func() {
					ui.Row(c).Gap(8).Children(func() {
						ui.Text(c, fmt.Sprintf("%s (%d rows)", r.title, len(r.rows))).Bold().Grow(1).Shrink(1).SingleLine()
						if ui.Link(c, "Open", "").FontSize(12).Clicked() {
							r.open()
						}
					})
					src := Source{Rows: r.rows}
					for _, name := range r.cols {
						src.Cols = append(src.Cols, db.ColumnInfo{Name: name})
					}
					mini := NewGrid()
					text := mini.plainText(a, &src, mini.ViewOrder(&src), 50)
					ui.ScrollHorizontal(c).Children(func() {
						ui.Text(c, text).Font(widgets.MonoFont).FontSize(11).Selectable().NoWrap()
					})
				})
			}
		})
	})
}

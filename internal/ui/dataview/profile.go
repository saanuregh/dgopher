package dataview

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// profileBins is how many bars a histogram of numbers or times has.
const profileBins = 24

// columnProfile is what a column's values are like.
type columnProfile struct {
	name, typ string
	count     int64 // values, NULL included
	nulls     int64
	distinct  int64 // -1 when not known; approximate from the server
	min, max  string
	avg       string // of numbers, "" otherwise
	median    string
	// bins count the numbers or times in equal widths from min to max;
	// top are the most frequent other values.
	bins []int64
	top  []topValue
}

type topValue struct {
	text  string
	count int64
}

// profileRows profiles the columns of the rows read.
func profileRows(src *Source, format func(any) string) []columnProfile {
	out := make([]columnProfile, len(src.Cols))
	for c, col := range src.Cols {
		p := columnProfile{name: col.Name, typ: col.Type, count: int64(len(src.Rows))}
		counts := map[string]int64{}
		var nums []float64
		var times []time.Time
		other := false // a value neither a number nor a time
		for _, r := range src.Rows {
			v := r[c]
			if v == nil {
				p.nulls++
				continue
			}
			counts[db.Display(v)]++
			switch x := v.(type) {
			case time.Time:
				times = append(times, x)
				continue
			case string, []byte:
				other = true
				continue
			}
			if f, ok := toFloat(v); ok && !math.IsNaN(f) && !math.IsInf(f, 0) {
				nums = append(nums, f)
			} else {
				other = true
			}
		}
		p.distinct = int64(len(counts))
		switch {
		case !other && len(times) == 0 && len(nums) > 0:
			slices.Sort(nums)
			sum := 0.0
			for _, n := range nums {
				sum += n
			}
			p.min, p.max = format(nums[0]), format(nums[len(nums)-1])
			p.avg = format(sum / float64(len(nums)))
			if m := len(nums) / 2; len(nums)%2 == 1 {
				p.median = format(nums[m])
			} else {
				p.median = format((nums[m-1] + nums[m]) / 2)
			}
			p.bins = histogram(nums)
		case !other && len(nums) == 0 && len(times) > 0:
			slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
			p.min, p.max = format(times[0]), format(times[len(times)-1])
			p.median = format(times[len(times)/2])
			secs := make([]float64, len(times))
			for i, t := range times {
				secs[i] = float64(t.UnixNano()) / 1e9
			}
			p.bins = histogram(secs)
		default:
			p.top = topValues(counts, 5)
			if len(counts) > 0 {
				texts := make([]string, 0, len(counts))
				for t := range counts {
					texts = append(texts, t)
				}
				p.min, p.max = slices.Min(texts), slices.Max(texts)
			}
		}
		out[c] = p
	}
	return out
}

// histogram counts sorted values in profileBins widths from the first to
// the last.
func histogram(sorted []float64) []int64 {
	bins := make([]int64, profileBins)
	lo, hi := sorted[0], sorted[len(sorted)-1]
	for _, v := range sorted {
		i := 0
		if hi > lo {
			i = min(profileBins-1, int((v-lo)/(hi-lo)*profileBins))
		}
		bins[i]++
	}
	return bins
}

// topValues are the n most frequent values, the most first.
func topValues(counts map[string]int64, n int) []topValue {
	out := make([]topValue, 0, len(counts))
	for t, c := range counts {
		out = append(out, topValue{t, c})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].count > out[j].count || out[i].count == out[j].count && out[i].text < out[j].text
	})
	return out[:min(n, len(out))]
}

// comparable reports whether the server can compare a column's values,
// for their minimum, maximum and distinct count.
func comparable(e db.Engine, typ string) bool {
	t := strings.ToLower(typ)
	switch e {
	case db.Postgres:
		for _, p := range []string{"json", "xml", "point", "line", "lseg", "box", "path", "polygon", "circle"} {
			if t == p {
				return false
			}
		}
	case db.ClickHouse:
		for _, p := range []string{"map(", "object(", "json", "variant(", "dynamic", "nested("} {
			if strings.HasPrefix(t, p) {
				return false
			}
		}
	}
	return true
}

// profileQuery is the statement profiling every column of the rows of
// from on the server, with how to read its one row: DuckDB summarizes
// them itself; the others count, and find the bounds of what they can
// compare.
func profileQuery(d db.Dialect, cols []db.ColumnInfo, from, where string) string {
	if d.Engine() == db.DuckDB {
		q := "SUMMARIZE SELECT * FROM " + from
		if where != "" {
			q += " WHERE " + where
		}
		return q
	}
	parts := []string{"count(*)"}
	for _, c := range cols {
		col := d.Quote(c.Name)
		if comparable(d.Engine(), c.Type) {
			parts = append(parts, "count("+col+")", "count(DISTINCT "+col+")", "min("+col+")", "max("+col+")")
		} else {
			parts = append(parts, "count("+col+")", "NULL", "NULL", "NULL")
		}
		if db.IsNumericType(c.Type) {
			parts = append(parts, "avg("+col+")")
		} else {
			parts = append(parts, "NULL")
		}
	}
	q := "SELECT " + strings.Join(parts, ", ") + " FROM " + from
	if where != "" {
		q += " WHERE " + where
	}
	return q
}

// serverProfiles reads the rows of profileQuery.
func serverProfiles(e db.Engine, cols []db.ColumnInfo, names []string, rows [][]any, format func(any) string) ([]columnProfile, error) {
	text := func(v any) string {
		if v == nil {
			return ""
		}
		return format(v)
	}
	num := func(v any) int64 {
		n, _ := strconv.ParseInt(db.Display(v), 10, 64)
		return n
	}
	if e == db.DuckDB {
		// One row a column: column_name, column_type, min, max,
		// approx_unique, avg, std, q25, q50, q75, count, null_percentage.
		at := map[string]int{}
		for i, n := range names {
			at[n] = i
		}
		out := make([]columnProfile, 0, len(rows))
		for _, r := range rows {
			p := columnProfile{name: db.Display(r[at["column_name"]]), typ: db.Display(r[at["column_type"]]),
				min: text(r[at["min"]]), max: text(r[at["max"]]), avg: text(r[at["avg"]]), median: text(r[at["q50"]]),
				count: num(r[at["count"]]), distinct: num(r[at["approx_unique"]])}
			pct, _ := strconv.ParseFloat(db.Display(r[at["null_percentage"]]), 64)
			p.nulls = int64(math.Round(pct / 100 * float64(p.count)))
			out = append(out, p)
		}
		return out, nil
	}
	if len(rows) != 1 || len(rows[0]) != 1+5*len(cols) {
		return nil, fmt.Errorf("the profile came back as %d rows", len(rows))
	}
	r := rows[0]
	total := num(r[0])
	out := make([]columnProfile, len(cols))
	for i, c := range cols {
		v := r[1+5*i:]
		p := columnProfile{name: c.Name, typ: c.Type, count: total, nulls: total - num(v[0]), distinct: -1,
			min: text(v[2]), max: text(v[3]), avg: text(v[4])}
		if v[1] != nil {
			p.distinct = num(v[1])
		}
		out[i] = p
	}
	return out, nil
}

// profilePanel shows every column's profile at once: of the rows read, or
// of every row, from the server; a click chooses the column in the grid.
func (g *Grid) profilePanel(c *ui.Context, a Host, src *Source) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	format := func(v any) string { return cellText(a.Settings().ViewFormat.Format(v), 40) }
	key := fmt.Sprint(g.gen, "/", len(src.Rows), "/", len(src.Cols))
	if g.profileKey != key {
		g.profileKey, g.profileServer, g.profileErr = key, nil, ""
		g.profiles = profileRows(src, format)
	}
	profiles := g.profiles
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(4, 8).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			label := fmt.Sprintf("Of the %d rows read", len(src.Rows))
			if g.profileServer != nil {
				profiles = g.profileServer
				label = "Of every row, on the server"
			}
			ui.Text(c, label).FontSize(12).TextColor(pal.Muted).Grow(1).Shrink(1)
			if g.profileOnServer != nil && !g.profiling && ui.Button(c, "Profile Every Row").Clicked() {
				g.profiling = true
				g.profileOnServer(func(ps []columnProfile, err error) {
					g.profiling = false
					g.profileServer, g.profileErr = ps, ""
					if err != nil {
						g.profileErr = err.Error()
					}
				})
			}
			if g.profiling {
				ui.Spinner(c).Size(14, 14)
			}
		})
		if g.profileErr != "" {
			ui.Text(c, g.profileErr).TextColor(th.Danger).Padding(8).Selectable()
		}
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(6, 8).Gap(4).Children(func() {
				for i, p := range profiles {
					g.profileRow(c, src, i, p)
				}
			})
		})
	})
}

// profileRow shows a column's profile: its name and type, its histogram
// or most frequent values, and its figures.
func (g *Grid) profileRow(c *ui.Context, src *Source, i int, p columnProfile) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	row := ui.ButtonBase(c.Key("profile-"+strconv.Itoa(i))).Padding(6, 8).Radius(6).Label("Column " + p.name)
	if col := slices.IndexFunc(src.Cols, func(c db.ColumnInfo) bool { return c.Name == p.name }); col == g.selCol {
		row.Background(th.Accent.Alpha(0.12))
	} else if row.Hovered() {
		row.Background(pal.Hover)
	}
	row.Children(func() {
		ui.Column(c).Gap(3).Grow(1).Children(func() {
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, p.name).Bold().SingleLine().Shrink(1)
				ui.Text(c, p.typ).FontSize(11).TextColor(pal.Muted).SingleLine().Shrink(1)
				ui.Spacer(c)
				if p.count > 0 {
					ui.Text(c, fmt.Sprintf("%.0f%% NULL", float64(p.nulls)/float64(p.count)*100)).FontSize(11).TextColor(pal.Muted)
				}
			})
			masked := g.isMasked(slices.IndexFunc(src.Cols, func(c db.ColumnInfo) bool { return c.Name == p.name }))
			if masked {
				// Its figures and frequent values would show what it hides.
				p.min, p.max, p.avg, p.median, p.top = "", "", "", "", nil
			}
			if len(p.bins) > 0 && !masked {
				drawHistogram(c, p.bins, th.Accent)
			}
			figures := []string{widgets.Count(p.count-p.nulls, "value")}
			if p.distinct >= 0 {
				figures = append(figures, fmt.Sprintf("%d distinct", p.distinct))
			}
			if p.min != "" || p.max != "" {
				figures = append(figures, p.min+" … "+p.max)
			}
			if p.avg != "" {
				figures = append(figures, "avg "+p.avg)
			}
			if p.median != "" {
				figures = append(figures, "median "+p.median)
			}
			ui.Text(c, strings.Join(figures, " · ")).Font(widgets.MonoFont).FontSize(11).TextColor(pal.Muted)
			for _, t := range p.top {
				ui.Row(c).Gap(6).Children(func() {
					ui.Text(c, t.text).FontSize(11.5).SingleLine().Grow(1).Shrink(1)
					ui.Text(c, strconv.FormatInt(t.count, 10)).Font(widgets.MonoFont).FontSize(11).TextColor(pal.Muted)
				})
			}
		})
	})
	if row.Clicked() {
		if col := slices.IndexFunc(src.Cols, func(c db.ColumnInfo) bool { return c.Name == p.name }); col >= 0 {
			g.selCol = col
		}
	}
}

// drawHistogram draws bars of counts, as high as the most.
func drawHistogram(c *ui.Context, bins []int64, col ui.Color) {
	most := slices.Max(bins)
	ui.Box(c).FillWidth().Height(28).Role(ui.RoleImage).Label(fmt.Sprintf("Distribution of the values, in %d bins", len(bins))).Draw(func(p *ui.Painter, r ui.Rect) {
		w := r.W / float32(len(bins))
		for i, n := range bins {
			if n == 0 || most == 0 {
				continue
			}
			h := max(1, r.H*float32(n)/float32(most))
			p.Fill(ui.Rect{X: r.X + float32(i)*w + 0.5, Y: r.Y + r.H - h, W: max(1, w-1), H: h}, col.Alpha(0.75), 1)
		}
	}).Label(fmt.Sprintf("Histogram: %v", bins))
}

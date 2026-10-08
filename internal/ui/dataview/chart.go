package dataview

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The categorical palette of the dataviz reference (validated for color
// vision deficiencies on adjacent pairs, in light and dark), in its fixed
// order: series take slots in order, never cycled.
var (
	seriesLight = []string{"#2a78d6", "#eb6834", "#1baf7a", "#eda100", "#e87ba4", "#008300", "#4a3aa7", "#e34948"}
	seriesDark  = []string{"#3987e5", "#d95926", "#199e70", "#c98500", "#d55181", "#008300", "#9085e9", "#e66767"}
)

func seriesColor(c *ui.Context, i int) ui.Color {
	if c.Theme().Dark {
		return ui.Hex(seriesDark[i])
	}
	return ui.Hex(seriesLight[i])
}

const (
	chartBar = iota
	chartLine
	chartScatter
	chartPie
)

var chartKinds = []string{"Bar", "Line", "Scatter", "Pie"}

// Chart is how a result is charted.
type Chart struct {
	kind   int
	x      string // the column along the x axis
	ys     []bool // the columns drawn as series
	colsOf string // the columns the choice was made for
	hover  int    // the point under the pointer, -1 for none
}

func columnsKey(cols []db.ColumnInfo) string {
	var b strings.Builder
	for _, c := range cols {
		b.WriteString(c.Name + "\x00")
	}
	return b.String()
}

func isTimeType(t string) bool {
	t = strings.ToUpper(t)
	return strings.Contains(t, "DATE") || strings.Contains(t, "TIME")
}

// numericColumn reports whether a column's values are numbers, by its type
// or its first values.
func numericColumn(src *Source, col int) bool {
	if db.IsNumericType(src.Cols[col].Type) {
		return true
	}
	seen := 0
	for _, row := range src.Rows[:min(len(src.Rows), 20)] {
		if row[col] == nil {
			continue
		}
		if _, ok := toFloat(row[col]); !ok {
			return false
		}
		seen++
	}
	return seen > 0
}

// detect picks the axes: a time column, else the first column of text,
// along x; the numbers as series.
func (s *Chart) detect(src *Source) {
	s.colsOf = columnsKey(src.Cols)
	s.ys = make([]bool, len(src.Cols))
	s.x, s.hover = "", -1
	for _, c := range src.Cols {
		if isTimeType(c.Type) {
			s.x = c.Name
			break
		}
	}
	if s.x == "" {
		for i, c := range src.Cols {
			if !numericColumn(src, i) {
				s.x = c.Name
				break
			}
		}
	}
	if s.x == "" && len(src.Cols) > 0 {
		s.x = src.Cols[0].Name
	}
	n := 0
	for i, c := range src.Cols {
		if c.Name != s.x && numericColumn(src, i) && n < 4 {
			s.ys[i] = true
			n++
		}
	}
	s.kind = chartBar
	if x := s.xIndex(src); x >= 0 && isTimeType(src.Cols[x].Type) {
		s.kind = chartLine
	}
}

func (s *Chart) xIndex(src *Source) int {
	for i, c := range src.Cols {
		if c.Name == s.x {
			return i
		}
	}
	return -1
}

// series lists the columns charted, in order, up to the kind's limit.
func (s *Chart) series() []int {
	limit := 8
	switch s.kind {
	case chartScatter:
		limit = 3 // the slots that stay apart whatever pair sits together
	case chartPie:
		limit = 1
	}
	var out []int
	for i, on := range s.ys {
		if on && len(out) < limit {
			out = append(out, i)
		}
	}
	return out
}

// chartPoint is a row as the chart reads it.
type chartPoint struct {
	label string
	x     float64 // time or number, for continuous axes
	ys    []float64
	ok    []bool
}

const maxChartPoints = 5000

func (s *Chart) points(src *Source, series []int) (pts []chartPoint, continuous, timeAxis bool) {
	xi := s.xIndex(src)
	if xi < 0 {
		return nil, false, false
	}
	timeAxis = isTimeType(src.Cols[xi].Type)
	continuous = s.kind != chartBar && s.kind != chartPie && (timeAxis || numericColumn(src, xi))
	for _, row := range src.Rows {
		if len(pts) == maxChartPoints {
			break
		}
		p := chartPoint{label: db.Cell(row[xi], 40), ys: make([]float64, len(series)), ok: make([]bool, len(series))}
		if continuous {
			switch v := row[xi].(type) {
			case time.Time:
				p.x = float64(v.UnixMilli())
			default:
				f, ok := toFloat(v)
				if !ok && timeAxis {
					if tm, err := time.Parse("2006-01-02 15:04:05", db.Display(v)); err == nil {
						f, ok = float64(tm.UnixMilli()), true
					} else if tm, err := time.Parse("2006-01-02", db.Display(v)); err == nil {
						f, ok = float64(tm.UnixMilli()), true
					}
				}
				if !ok {
					continue
				}
				p.x = f
			}
		}
		for j, col := range series {
			p.ys[j], p.ok[j] = toFloat(row[col])
		}
		pts = append(pts, p)
	}
	if continuous {
		sort.SliceStable(pts, func(i, j int) bool { return pts[i].x < pts[j].x })
	}
	return pts, continuous, timeAxis
}

// niceTicks returns about n round values covering [lo, hi].
func niceTicks(lo, hi float64, n int) []float64 {
	if hi == lo {
		hi = lo + 1
	}
	raw := (hi - lo) / float64(n)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	step := mag
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if raw <= m*mag {
			step = m * mag
			break
		}
	}
	var out []float64
	for v := math.Floor(lo/step) * step; v <= hi+step*0.001; v += step {
		out = append(out, v)
	}
	return out
}

func fmtNumber(v float64) string {
	short := func(x float64) string {
		return strconv.FormatFloat(math.Round(x*10)/10, 'f', -1, 64)
	}
	a := math.Abs(v)
	switch {
	case a >= 1e9:
		return short(v/1e9) + "B"
	case a >= 1e6:
		return short(v/1e6) + "M"
	case a >= 1e4:
		return short(v/1e3) + "k"
	}
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

func fmtAxisTime(ms float64, span float64) string {
	t := time.UnixMilli(int64(ms))
	switch {
	case span > 2*365*24*3600e3:
		return t.Format("2006")
	case span > 60*24*3600e3:
		return t.Format("Jan 2006")
	case span > 2*24*3600e3:
		return t.Format("Jan 2")
	}
	return t.Format("15:04")
}

// ChartView builds a chart of a result, with its controls.
func ChartView(c *ui.Context, s *Chart, src *Source) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if s.colsOf != columnsKey(src.Cols) {
		s.detect(src)
	}
	names := make([]string, len(src.Cols))
	for i, col := range src.Cols {
		names[i] = col.Name
	}
	series := s.series()
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(6, 10).Gap(10).Wrap().BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			if ui.Segmented(c, &s.kind, chartKinds...).Label("Chart type").Changed() {
				s.hover = -1
			}
			ui.Text(c, "X").FontSize(12).TextColor(pal.Muted)
			ui.Select(c, &s.x, names).Label("X axis")
			ui.Text(c, "Series").FontSize(12).TextColor(pal.Muted)
			for i, col := range src.Cols {
				if col.Name == s.x || !numericColumn(src, i) {
					continue
				}
				ui.Checkbox(c, &s.ys[i], col.Name)
			}
		})
		if len(series) == 0 {
			ui.Text(c, "Choose a numeric column as a series.").TextColor(pal.Muted).Padding(16)
			return
		}
		pts, continuous, timeAxis := s.points(src, series)
		if len(pts) == 0 {
			ui.Text(c, "No rows to chart.").TextColor(pal.Muted).Padding(16)
			return
		}
		if len(series) > 1 && s.kind != chartPie {
			ui.Row(c).Padding(6, 16, 0, 16).Gap(14).Children(func() {
				for j, col := range series {
					ui.Row(c).Gap(6).Children(func() {
						ui.Box(c).Size(10, 10).Radius(2).Background(seriesColor(c, j))
						ui.Text(c, src.Cols[col].Name).FontSize(12)
					})
				}
			})
		}
		if len(src.Rows) > maxChartPoints {
			ui.Text(c, fmt.Sprintf("Charting the first %d rows.", maxChartPoints)).FontSize(12).TextColor(pal.Muted).Padding(4, 16)
		}
		if s.kind == chartPie {
			pieView(c, s, src, series[0], pts)
			return
		}
		plot := ui.Box(c).Grow(1).Margin(8, 12, 12, 8)
		px, py, over := plot.PointerPosition()
		plot.Draw(func(p *ui.Painter, r ui.Rect) {
			drawXY(c, p, r, s, src, series, pts, continuous, timeAxis, px, py, over)
		})
	})
}

func drawXY(c *ui.Context, p *ui.Painter, r ui.Rect, s *Chart, src *Source, series []int, pts []chartPoint, continuous, timeAxis bool, px, py float32, over bool) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ink, muted := th.Text, pal.Muted
	grid := pal.GridLine
	// The y range, from zero for bars, whose length is their value.
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, pt := range pts {
		for j := range series {
			if pt.ok[j] {
				lo, hi = min(lo, pt.ys[j]), max(hi, pt.ys[j])
			}
		}
	}
	if math.IsInf(lo, 1) {
		return
	}
	if s.kind == chartBar {
		lo, hi = min(lo, 0), max(hi, 0)
	}
	ticks := niceTicks(lo, hi, 5)
	lo, hi = min(lo, ticks[0]), max(hi, ticks[len(ticks)-1])
	left, bottom := float32(56), float32(28)
	plot := ui.Rect{X: r.X + left, Y: r.Y + 8, W: r.W - left - 8, H: r.H - bottom - 8}
	if plot.W < 40 || plot.H < 40 {
		return
	}
	yOf := func(v float64) float32 { return plot.Y + plot.H - float32((v-lo)/(hi-lo))*plot.H }
	for _, tv := range ticks {
		y := yOf(tv)
		p.Line(plot.X, y, plot.X+plot.W, y, 1, grid)
		p.Text(r.X+4, y-7, fmtNumber(tv), 11, muted)
	}
	// The x positions: a band per row for bars and categories, else a
	// continuous scale.
	xlo, xhi := 0.0, float64(len(pts)-1)
	if continuous {
		xlo, xhi = pts[0].x, pts[len(pts)-1].x
		if xhi == xlo {
			xhi = xlo + 1
		}
	}
	band := plot.W / float32(len(pts))
	xOf := func(i int) float32 {
		if continuous {
			return plot.X + float32((pts[i].x-xlo)/(xhi-xlo))*plot.W
		}
		return plot.X + band*(float32(i)+0.5)
	}
	// X labels, as many as fit.
	labelEvery := max(1, int(math.Ceil(float64(len(pts))*80/float64(plot.W))))
	if continuous && !timeAxis {
		for _, tv := range niceTicks(xlo, xhi, 6) {
			if tv < xlo || tv > xhi {
				continue
			}
			x := plot.X + float32((tv-xlo)/(xhi-xlo))*plot.W
			p.Text(x-12, plot.Y+plot.H+8, fmtNumber(tv), 11, muted)
		}
	} else {
		for i := 0; i < len(pts); i += labelEvery {
			label := pts[i].label
			if timeAxis && continuous {
				label = fmtAxisTime(pts[i].x, xhi-xlo)
			}
			if len([]rune(label)) > 12 {
				label = string([]rune(label)[:11]) + "…"
			}
			p.Text(xOf(i)-float32(len([]rune(label)))*3, plot.Y+plot.H+8, label, 11, muted)
		}
	}
	p.Line(plot.X, yOf(0), plot.X+plot.W, yOf(0), 1, muted.Alpha(0.6))
	// The point nearest the pointer, for the tooltip.
	hover := -1
	if over && px >= left && px <= r.W-8 {
		best := float32(math.MaxFloat32)
		for i := range pts {
			if d := float32(math.Abs(float64(xOf(i) - (r.X + px)))); d < best {
				best, hover = d, i
			}
		}
	}
	switch s.kind {
	case chartBar:
		n := float32(len(series))
		w := max(1, min(band*0.8/n, 48))
		gap := float32(2)
		if w < 6 {
			gap = 0
		}
		for i := range pts {
			for j := range series {
				if !pts[i].ok[j] {
					continue
				}
				x := xOf(i) - w*n/2 + float32(j)*w
				y0, y1 := yOf(0), yOf(pts[i].ys[j])
				top, h := min(y0, y1), float32(math.Abs(float64(y1-y0)))
				col := seriesColor(c, j)
				if hover >= 0 && hover != i {
					col = col.Alpha(0.55)
				}
				p.Fill(ui.Rect{X: x + gap/2, Y: top, W: max(1, w-gap), H: max(1, h)}, col, min(4, (w-gap)/2))
			}
		}
	case chartLine, chartScatter:
		for j := range series {
			col := seriesColor(c, j)
			if s.kind == chartLine {
				// One path per run of values; a NULL breaks the line.
				var path *ui.Path
				for i := range pts {
					if !pts[i].ok[j] {
						if path != nil {
							p.StrokePath(path, 2, col)
						}
						path = nil
						continue
					}
					x, y := xOf(i), yOf(pts[i].ys[j])
					if path == nil {
						path = new(ui.Path).MoveTo(x, y)
					} else {
						path.LineTo(x, y)
					}
				}
				if path != nil {
					p.StrokePath(path, 2, col)
				}
			}
			for i := range pts {
				if !pts[i].ok[j] {
					continue
				}
				x, y := xOf(i), yOf(pts[i].ys[j])
				if s.kind == chartScatter || len(pts) <= 60 {
					d := float32(8)
					if s.kind == chartLine {
						d = 6
					}
					p.Fill(ui.Rect{X: x - d/2 - 1, Y: y - d/2 - 1, W: d + 2, H: d + 2}, th.Background, (d+2)/2)
					p.Fill(ui.Rect{X: x - d/2, Y: y - d/2, W: d, H: d}, col, d/2)
				}
			}
		}
	}
	if hover < 0 {
		return
	}
	// The crosshair and the tooltip.
	hx := xOf(hover)
	p.Line(hx, plot.Y, hx, plot.Y+plot.H, 1, muted.Alpha(0.7))
	lines := []string{pts[hover].label}
	for j, col := range series {
		v := "NULL"
		if pts[hover].ok[j] {
			v = strconv.FormatFloat(pts[hover].ys[j], 'f', -1, 64)
		}
		lines = append(lines, src.Cols[col].Name+": "+v)
	}
	tw := float32(0)
	for _, l := range lines {
		tw = max(tw, float32(len([]rune(l)))*6.6)
	}
	tw += 28
	th2 := float32(len(lines))*17 + 12
	tx := hx + 12
	if tx+tw > r.X+r.W {
		tx = hx - 12 - tw
	}
	ty := r.Y + py - th2/2
	ty = max(r.Y, min(ty, r.Y+r.H-th2))
	p.Shadow(ui.Rect{X: tx, Y: ty, W: tw, H: th2}, 6, 0, 2, 8, 0, ui.RGBA(0, 0, 0, 0.18))
	p.Fill(ui.Rect{X: tx, Y: ty, W: tw, H: th2}, th.Background, 6)
	p.Stroke(ui.Rect{X: tx, Y: ty, W: tw, H: th2}, th.Border, 6, 1)
	for i, l := range lines {
		y := ty + 6 + float32(i)*17
		x := tx + 10
		if i > 0 {
			p.Fill(ui.Rect{X: x, Y: y + 4, W: 8, H: 8}, seriesColor(c, i-1), 2)
			x += 14
		}
		p.Text(x, y, l, 12, ink)
	}
}

// pieView draws the shares of one series, the largest seven slices and
// the rest as "Other".
func pieView(c *ui.Context, s *Chart, src *Source, col int, pts []chartPoint) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	type slice struct {
		label string
		v     float64
	}
	var slices []slice
	total := 0.0
	for _, pt := range pts {
		if pt.ok[0] && pt.ys[0] > 0 {
			slices = append(slices, slice{pt.label, pt.ys[0]})
			total += pt.ys[0]
		}
	}
	if total == 0 {
		ui.Text(c, "A pie needs positive values.").TextColor(pal.Muted).Padding(16)
		return
	}
	sort.SliceStable(slices, func(i, j int) bool { return slices[i].v > slices[j].v })
	if len(slices) > 8 {
		other := 0.0
		for _, sl := range slices[7:] {
			other += sl.v
		}
		slices = append(slices[:7], slice{"Other", other})
	}
	ui.Row(c).Grow(1).Padding(16).Gap(24).Children(func() {
		ui.Box(c).Grow(1).AspectRatio(1).MaxWidth(420).Draw(func(p *ui.Painter, r ui.Rect) {
			size := min(r.W, r.H)
			cx, cy := r.X+r.W/2, r.Y+r.H/2
			radius := size / 2
			start := -math.Pi / 2
			for i, sl := range slices {
				sweep := sl.v / total * 2 * math.Pi
				steps := max(2, int(sweep*24))
				wedge := new(ui.Path).MoveTo(cx, cy)
				for k := 0; k <= steps; k++ {
					a := start + sweep*float64(k)/float64(steps)
					wedge.LineTo(cx+radius*float32(math.Cos(a)), cy+radius*float32(math.Sin(a)))
				}
				p.FillPath(wedge.Close(), seriesColor(c, i))
				start += sweep
			}
			// A gap of the surface between the slices.
			start = -math.Pi / 2
			for _, sl := range slices {
				edge := new(ui.Path).MoveTo(cx, cy).LineTo(cx+radius*float32(math.Cos(start)), cy+radius*float32(math.Sin(start)))
				p.StrokePath(edge, 2, th.Background)
				start += sl.v / total * 2 * math.Pi
			}
			p.Fill(ui.Rect{X: cx - radius*0.55, Y: cy - radius*0.55, W: radius * 1.1, H: radius * 1.1}, th.Background, radius*0.55)
			p.Text(cx-30, cy-8, fmtNumber(total), 14, th.Text)
		})
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, src.Cols[col].Name).Bold()
			for i, sl := range slices {
				ui.Row(c).Gap(8).Children(func() {
					ui.Box(c).Size(10, 10).Radius(2).Background(seriesColor(c, i))
					ui.Text(c, sl.label).FontSize(12.5).SingleLine().MaxWidth(200)
					ui.Text(c, fmt.Sprintf("%s · %.1f%%", fmtNumber(sl.v), sl.v/total*100)).FontSize(12).TextColor(pal.Muted)
				})
			}
		})
	})
}

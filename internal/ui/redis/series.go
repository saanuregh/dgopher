package redis

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// seriesSpans are how far back from its last sample a series' chart
// goes; the last is the whole series.
var seriesSpans = []struct {
	label string
	span  time.Duration
}{{"Hour", time.Hour}, {"Day", 24 * time.Hour}, {"Week", 7 * 24 * time.Hour}, {"Month", 30 * 24 * time.Hour}, {"All", 0}}

// seriesPoints is about how many points a chart of a series shows, each
// the average of a bucket of samples.
const seriesPoints = 500

// seriesState is a time series' chart, with what it was read for.
type seriesState struct {
	span      int // an index of seriesSpans
	readFor   string
	loading   bool
	err       string
	first     time.Time
	last      time.Time
	props     []db.Property
	bucket    time.Duration
	source    *dataview.Source
	chart     dataview.Chart
	newAt     string // the new sample's time, as typed
	newValue  string
	reads     int // counts the reads: a stale one's result is dropped
	propsList ui.ListState
}

// loadSeries reads a series' description, then its samples over the span
// chosen, by buckets.
func (r *Tab) loadSeries() {
	s := &r.series
	s.loading, s.err = true, ""
	s.reads++
	kv, key, span, read := r.conn.KV, r.selected, seriesSpans[s.span].span, s.reads
	s.readFor = key + "\x00" + seriesSpans[s.span].label
	r.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		first, last, props, err := kv.SeriesInfo(ctx, key)
		var samples []db.Sample
		var bucket time.Duration
		if err == nil && !last.IsZero() {
			from := first
			if span > 0 && last.Add(-span).After(first) {
				from = last.Add(-span)
			}
			bucket = max(last.Sub(from)/seriesPoints, time.Millisecond).Truncate(time.Millisecond)
			samples, err = kv.SeriesRange(ctx, key, from, last, bucket)
		}
		return func() {
			if read != s.reads {
				return
			}
			s.loading = false
			if err != nil {
				s.err = err.Error()
				return
			}
			s.first, s.last, s.props, s.bucket = first, last, props, bucket
			rows := make([][]any, len(samples))
			for i, p := range samples {
				rows[i] = []any{p.At, p.Value}
			}
			s.source = &dataview.Source{Cols: []db.ColumnInfo{{Name: "time", Type: "TIMESTAMP"}, {Name: "average", Type: "DOUBLE"}}, Rows: rows}
		}
	})
}

// seriesView charts a time series, with its description, and adds
// samples to it.
func (r *Tab) seriesView(c *ui.Context, ro bool) {
	s := &r.series
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if s.readFor != r.selected+"\x00"+seriesSpans[s.span].label && !s.loading {
		r.loadSeries()
	}
	labels := make([]string, len(seriesSpans))
	for i, sp := range seriesSpans {
		labels[i] = sp.label
	}
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(6, 14).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Segmented(c, &s.span, labels...).Label("Span charted")
			switch {
			case s.loading:
				ui.Spinner(c).Size(12, 12)
			case s.err != "":
				ui.Text(c, s.err).FontSize(12).TextColor(th.Danger).SingleLine().Shrink(1)
			case s.last.IsZero():
				ui.Text(c, "No samples yet.").FontSize(12).TextColor(pal.Muted)
			default:
				ui.Text(c, fmt.Sprintf("From %s to %s, averaged by %s", s.first.Format(time.DateTime), s.last.Format(time.DateTime), s.bucket)).
					FontSize(12).TextColor(pal.Muted).SingleLine().Shrink(1)
			}
		})
		ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				if s.source != nil && len(s.source.Rows) > 0 {
					dataview.ChartView(c, &s.chart, s.source)
				}
			})
			ui.Column(c).Width(240).BorderWidth(0, 0, 0, 1).BorderColor(th.Border).Children(func() {
				ui.List(c, &s.propsList, len(s.props), func(i int) {
					p := s.props[i]
					ui.Column(c).Padding(3, 10).Children(func() {
						ui.Text(c, p.Name).FontSize(11).TextColor(pal.Muted)
						ui.Text(c, p.Value).Font(widgets.MonoFont).FontSize(12).Selectable()
					})
				}).Grow(1).Label("Series properties")
			})
		})
		if ro {
			return
		}
		ui.Row(c).Padding(8, 14).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
			ui.TextInput(c, &s.newAt).Placeholder("Time in ms, * for now").Width(170).Font(widgets.MonoFont).Label("Sample time")
			ui.TextInput(c, &s.newValue).Placeholder("Value").Width(140).Font(widgets.MonoFont).Label("Sample value")
			if ui.Button(c, "Add Sample").Clicked() {
				r.addSample()
			}
		})
	})
}

// addSample adds a sample to the series shown.
func (r *Tab) addSample() {
	s := &r.series
	at := strings.TrimSpace(s.newAt)
	if at == "" {
		at = "*"
	}
	value := strings.TrimSpace(s.newValue)
	if _, err := strconv.ParseFloat(value, 64); err != nil {
		r.a.ShowError("Not added", fmt.Sprintf("%q is not a number.", value))
		return
	}
	if _, err := strconv.ParseInt(at, 10, 64); err != nil && at != "*" {
		r.a.ShowError("Not added", "A sample's time is milliseconds since 1970, or * for now.")
		return
	}
	r.write([]string{"TS.ADD", r.selected, at, value}, func() {
		s.newAt, s.newValue = "", ""
		r.loadSeries()
	})
}

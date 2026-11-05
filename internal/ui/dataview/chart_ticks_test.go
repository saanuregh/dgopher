package dataview

import (
	"math"
	"testing"
	"time"

	"dgopher/internal/db"
	"github.com/egoist/mygo/ui"
)

// Ticks come back, a bounded number of them, for any finite range: equal
// bounds, ranges too narrow for float64 to step through, and huge ones.
func TestNiceTicksBoundedForAnyFiniteRange(t *testing.T) {
	for _, c := range []struct {
		lo, hi float64
	}{
		{0, 0},
		{1e17, 1e17},
		{1e17, 1e17 + 16},
		{-1e300, 1e300},
		{5e-324, 1e-323},
		{-math.MaxFloat64, math.MaxFloat64},
		{0, 100},
	} {
		done := make(chan []float64, 1)
		go func() { done <- niceTicks(c.lo, c.hi, 5) }()
		select {
		case ticks := <-done:
			if len(ticks) < 2 || len(ticks) > maxTicks {
				t.Errorf("[%g, %g]: %d ticks %v", c.lo, c.hi, len(ticks), ticks)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("[%g, %g]: niceTicks did not return", c.lo, c.hi)
		}
	}
}

// A chart skips NaN and ±Inf values, which have no place on an axis, and
// draws the rest.
func TestChartIgnoresNonFiniteValues(t *testing.T) {
	src := &Source{Cols: []db.ColumnInfo{{Name: "x", Type: "FLOAT8"}, {Name: "y", Type: "FLOAT8"}}}
	for i, v := range []float64{math.NaN(), math.Inf(1), 3, math.Inf(-1), 5} {
		src.Rows = append(src.Rows, []any{float64(i), v})
	}
	src.Rows = append(src.Rows, []any{math.NaN(), 7.0})
	for kind := range chartKinds {
		s := Chart{}
		tt := ui.NewTester(func(c *ui.Context) {
			if s.colsOf == "" {
				s.detect(src)
				s.kind = kind
			}
			ChartView(c, &s, src)
		}, 600, 300)
		tt.Frame()
	}
	s := Chart{}
	s.detect(src)
	pts, _, _ := s.points(src, s.series())
	for _, p := range pts {
		if math.IsNaN(p.x) || math.IsInf(p.x, 0) {
			t.Errorf("a point at x %v", p.x)
		}
		for j, y := range p.ys {
			if p.ok[j] && (math.IsNaN(y) || math.IsInf(y, 0)) {
				t.Errorf("a value %v kept", y)
			}
		}
	}
}

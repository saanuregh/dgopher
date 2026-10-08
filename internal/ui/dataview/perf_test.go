package dataview

import (
	"fmt"
	"testing"
	"time"

	"dgopher/internal/db"

	"github.com/egoist/mygo/ui"
)

// A result at the limit of rows stays quick to draw and to scroll: the
// grid builds only the rows in view.
func TestLargeResultFrameTime(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	a := NewFakeHost(t)
	const n = 200000
	src := &Source{Cols: []db.ColumnInfo{{Name: "i", Type: "INTEGER"}, {Name: "label", Type: "TEXT"}, {Name: "amount", Type: "REAL"}, {Name: "day", Type: "TEXT"}}}
	day0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		src.Rows = append(src.Rows, []any{int64(i), fmt.Sprintf("row %d", i), float64(i) * 1.5, day0.AddDate(0, 0, i%365).Format("2006-01-02")})
	}
	g := NewGrid()
	tt := ui.NewTester(func(c *ui.Context) { g.View(c, a, src) }, 1280, 800)
	start := time.Now()
	const frames = 20
	for i := range frames {
		g.List.ScrollTo(i*9000, ui.Start)
		tt.Frame()
	}
	per := time.Since(start) / frames
	t.Logf("%v per frame while scrolling 200k rows (software renderer)", per)
	if per > 250*time.Millisecond {
		t.Errorf("frames take %v", per)
	}
	// Sorting the rows on the client.
	start = time.Now()
	g.sort = ui.SortOrder{Column: "2", Descending: true}
	tt.Frame()
	t.Logf("sorting 200k rows: %v", time.Since(start))
}

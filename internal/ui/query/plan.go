package query

import (
	"fmt"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// How an explain's result shows.
const (
	planTree = iota
	planFlame
	planRows
)

// planMetric is how much of the query a step takes: what it took to run,
// else its estimated cost, else one for each step it holds.
func planMetric(p *db.Plan) (func(*db.PlanNode) float64, string) {
	switch {
	case p.Analyzed && p.Root.Time > 0:
		return func(n *db.PlanNode) float64 { return max(n.Time, 0) }, "time"
	case p.Root.Cost > 0:
		return func(n *db.PlanNode) float64 { return max(n.Cost, 0) }, "cost"
	}
	var leaves func(n *db.PlanNode) float64
	leaves = func(n *db.PlanNode) float64 {
		if len(n.Children) == 0 {
			return 1
		}
		sum := 0.0
		for _, c := range n.Children {
			sum += leaves(c)
		}
		return sum
	}
	return leaves, "steps"
}

// planSummary says what the plan took, or is thought to take.
func planSummary(p *db.Plan) string {
	steps := 0
	p.Root.Walk(func(*db.PlanNode, int) { steps++ })
	switch {
	case p.Analyzed && p.Root.Time >= 0:
		return fmt.Sprintf("%d steps · ran in %s", steps, formatMs(p.Root.Time))
	case p.Root.Cost >= 0:
		return fmt.Sprintf("%d steps · estimated cost %s", steps, formatNumber(p.Root.Cost))
	}
	return fmt.Sprintf("%d steps", steps)
}

func formatMs(ms float64) string {
	if ms < 1 {
		return fmt.Sprintf("%.3f ms", ms)
	}
	return fmt.Sprintf("%.1f ms", ms)
}

func formatNumber(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprint(int64(f))
	}
	return fmt.Sprintf("%.2f", f)
}

// planView shows an explain's plan: its steps as a tree with what they
// take and the advice about them, as a flame graph, or the rows as the
// server gave them.
func (q *Tab) planView(c *ui.Context, r *result) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(4, 10).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Segmented(c, &r.planMode, "Plan", "Flame Graph", "Rows").Label("Plan view")
			ui.Text(c, planSummary(r.plan)).FontSize(12).TextColor(pal.Muted).SingleLine().Shrink(1)
		})
		switch r.planMode {
		case planRows:
			r.view.View(c)
		case planFlame:
			q.flameView(c, r)
		default:
			ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
				ui.Scroll(c).Grow(1).Children(func() {
					ui.Column(c).Padding(6, 0).Children(func() {
						q.adviceView(c, r)
						q.treeView(c, r)
					})
				})
				q.stepView(c, r)
			})
		}
	})
}

func (q *Tab) adviceView(c *ui.Context, r *result) {
	if len(r.advice) == 0 {
		return
	}
	th := c.Theme()
	ui.Column(c).Padding(4, 12, 8, 12).Gap(4).Children(func() {
		for i, a := range r.advice {
			row := ui.ButtonBase(c.Key(fmt.Sprint("advice-", i))).Padding(6, 10).Radius(6).Background(th.Warning.Alpha(0.12)).Label(a.Text)
			row.Children(func() {
				ui.Row(c).Gap(8).FillWidth().Children(func() {
					ui.Icon(c, widgets.IconAlert).TextColor(th.Warning).FontSize(13)
					ui.Text(c, a.Text).FontSize(12.5).Grow(1).Shrink(1)
				})
			})
			if row.Clicked() {
				r.planSel = a.Node
			}
		}
	})
}

// treeView lists the steps, each under the one taking its rows, with its
// rows and its share of the query.
func (q *Tab) treeView(c *ui.Context, r *result) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	metric, _ := planMetric(r.plan)
	total := max(metric(r.plan.Root), 1e-9)
	ui.Row(c).Padding(4, 12).Gap(10).Children(func() {
		ui.Text(c, "Step").FontSize(11.5).Bold().TextColor(pal.Muted).Grow(1)
		ui.Text(c, "Rows").FontSize(11.5).Bold().TextColor(pal.Muted).Width(140).TextAlign(ui.End)
		ui.Text(c, "Share").FontSize(11.5).Bold().TextColor(pal.Muted).Width(160)
	})
	r.plan.Root.Walk(func(n *db.PlanNode, depth int) {
		row := ui.ButtonBase(c.Key(fmt.Sprintf("step-%p", n))).Padding(4, 12).Label(n.Op)
		if n == r.planSel {
			row.Background(th.Accent.Alpha(0.15))
		} else if row.Hovered() {
			row.Background(pal.Hover)
		}
		row.Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).FillWidth().Children(func() {
				ui.Row(c).Gap(6).Grow(1).Shrink(1).PaddingX(float32(depth) * 16).Children(func() {
					ui.Text(c, n.Op).Font(widgets.MonoFont).FontSize(12.5).Bold().SingleLine().Shrink(0)
					if n.Target != "" {
						ui.Text(c, n.Target).FontSize(12.5).SingleLine().Shrink(1)
					}
					if n.Detail != "" {
						ui.Text(c, n.Detail).FontSize(11.5).TextColor(pal.Muted).SingleLine().Shrink(1)
					}
				})
				ui.Text(c, rowsLabel(n)).FontSize(12).TextColor(pal.Muted).Width(140).TextAlign(ui.End).SingleLine()
				share := metric(n) / total
				ui.Row(c).Width(160).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Width(100).Height(6).Radius(3).Background(pal.Hover).Children(func() {
						ui.Box(c).Width(100 * float32(min(share, 1))).Height(6).Radius(3).Background(shareColor(th, share))
					})
					ui.Text(c, fmt.Sprintf("%.0f%%", share*100)).FontSize(11.5).TextColor(pal.Muted)
				})
			})
		})
		if row.Clicked() {
			r.planSel = n
		}
	})
}

// rowsLabel is a step's rows: estimated, and found when it ran.
func rowsLabel(n *db.PlanNode) string {
	switch {
	case n.ActualRows >= 0 && n.Rows >= 0:
		return formatNumber(n.Rows) + " → " + formatNumber(n.ActualRows)
	case n.ActualRows >= 0:
		return formatNumber(n.ActualRows)
	case n.Rows >= 0:
		return "~" + formatNumber(n.Rows)
	}
	return ""
}

// shareColor is the color of a step taking that share of the query.
func shareColor(th *ui.Theme, share float64) ui.Color {
	switch {
	case share >= 0.5:
		return th.Danger
	case share >= 0.2:
		return th.Warning
	}
	return th.Accent
}

// stepView shows everything the engine tells of the step chosen.
func (q *Tab) stepView(c *ui.Context, r *result) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Scroll(c).Width(340).BorderWidth(0, 0, 0, 1).BorderColor(th.Border).Children(func() {
		ui.Column(c).Padding(10, 12).Gap(6).Children(func() {
			n := r.planSel
			if n == nil {
				ui.Text(c, "Choose a step to see what the server tells of it.").FontSize(12.5).TextColor(pal.Muted)
				return
			}
			ui.Text(c, n.Op).Font(widgets.MonoFont).Bold()
			for _, p := range n.Props {
				ui.Column(c).Gap(1).Children(func() {
					ui.Text(c, p[0]).FontSize(11).TextColor(pal.Muted)
					ui.Text(c, p[1]).Font(widgets.MonoFont).FontSize(12).Selectable()
				})
			}
		})
	})
}

// flameView draws the plan as an icicle: each step as wide as its share
// of the query, under the step taking its rows.
func (q *Tab) flameView(c *ui.Context, r *result) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	metric, unit := planMetric(r.plan)
	const rowH = 26
	depth := 0
	r.plan.Root.Walk(func(_ *db.PlanNode, d int) { depth = max(depth, d+1) })
	ui.Column(c).Grow(1).Children(func() {
		ui.Text(c, "Each step is as wide as its "+unit+", its own and its steps'; a click chooses it.").
			FontSize(12).TextColor(pal.Muted).Padding(6, 12)
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Box(c).FillWidth().Height(float32(depth*rowH+8)).Padding(0, 12).Children(func() {
				var lay func(n *db.PlanNode, d int, from, to float64)
				lay = func(n *db.PlanNode, d int, from, to float64) {
					if to-from < 0.15 {
						return // too narrow to show, or to click
					}
					total := max(metric(n), 1e-9)
					self := total
					for _, ch := range n.Children {
						self -= metric(ch)
					}
					box := ui.ButtonBase(c.Key(fmt.Sprintf("flame-%p", n))).Absolute().
						LeftPercent(float32(from)).RightPercent(float32(100-to)).Top(float32(d*rowH)).Height(rowH-2).
						Radius(3).Padding(0, 6).Label(n.Op).Tooltip(n.Op + " " + n.Target).ClipX()
					col := shareColor(th, max(self, 0)/max(metric(r.plan.Root), 1e-9))
					if n == r.planSel {
						box.Background(col).Border(1, th.Text)
					} else {
						box.Background(col.Alpha(0.55))
					}
					box.Children(func() {
						ui.Text(c, strings.TrimSpace(n.Op+" "+n.Target)).FontSize(11.5).SingleLine()
					})
					if box.Clicked() {
						r.planSel = n
					}
					at := from
					for _, ch := range n.Children {
						w := (to - from) * metric(ch) / total
						lay(ch, d+1, at, at+w)
						at += w
					}
				}
				lay(r.plan.Root, 0, 0, 100)
			})
		})
		if n := r.planSel; n != nil {
			ui.Row(c).Padding(6, 12).Gap(10).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
				ui.Text(c, n.Op).Font(widgets.MonoFont).Bold()
				ui.Text(c, n.Target).SingleLine().Shrink(1)
				ui.Text(c, rowsLabel(n)+" rows").FontSize(12).TextColor(pal.Muted)
				if n.Time >= 0 {
					ui.Text(c, formatMs(n.Time)).FontSize(12).TextColor(pal.Muted)
				}
			})
		}
	})
}

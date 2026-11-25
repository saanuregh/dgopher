package app

import (
	"cmp"
	"fmt"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/procmem"
	"dgopher/internal/settings"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// memoryEvery is how often the memory used is read again: often enough to
// follow a large result coming in, seldom enough to cost nothing.
const memoryEvery = 5 * time.Second

// memoryUse is the memory the app uses, as last read.
type memoryUse struct {
	// used is what the system counts for the process, and most the most
	// it has: the system's own figure where it keeps one, else the most
	// read here. inGo is what Go holds of it, its heap with the rest.
	used, most, inGo uint64
	ok               bool
	at               time.Time
	// shown is whether the details show, under the figure clicked.
	shown bool
}

// read reads the memory again.
func (m *memoryUse) read(now time.Time) {
	m.at = now
	m.used, m.ok = procmem.Used()
	most, _ := procmem.Peak()
	m.most = max(m.most, m.used, most)
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	// What Go has of the system and has not given back.
	m.inGo = stats.Sys - stats.HeapReleased
}

// sidebarFoot is the sidebar's foot: the memory the app uses, as the
// system counts it, with Go's part of it on hover and the rest of what is
// known a click away. It is the app's, as the sidebar is; the status bar
// under the work is the tab's in front. Nothing shows on a system that
// cannot be asked.
func (a *App) sidebarFoot(c *ui.Context) {
	m := &a.nav.memory
	if now := c.Now(); m.at.IsZero() || now.Sub(m.at) >= memoryEvery {
		m.read(now)
	}
	if !m.ok {
		return
	}
	c.After(memoryEvery)
	t := c.Theme()
	muted := widgets.PaletteOf(c).Muted
	// A status line's padding, as the one under the work has.
	ui.Row(c).Shrink(0).Padding(widgets.PadStatusY, widgets.PadStatusX).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		// Its figure is a button of its own: a status line's chip without
		// a border, an icon before its text, which no shared one is.
		figure := ui.ButtonBase(c.Key("memory")).Padding(1, 4).Radius(widgets.RadiusSmall).Label("Memory used").
			Tooltip("Memory DGopher uses: " + memoryText(m.used) + "\nOf that, Go's: " + memoryText(m.inGo) + "\nClick for more")
		if figure.Hovered() || m.shown {
			figure.Background(widgets.PaletteOf(c).Hover)
		}
		figure.Children(func() {
			ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, widgets.IconChip).FontSize(widgets.IconSmall).TextColor(muted)
				ui.Text(c, memoryText(m.used)).FontSize(widgets.TextSmall).FontFeatures("tnum").TextColor(muted).SingleLine()
			})
		})
		if figure.Clicked() {
			m.shown = !m.shown
			m.read(c.Now()) // as it is now, not five seconds ago
		}
		ui.Popover(c, figure, &m.shown, func() { a.memoryDetails(c) })
	})
}

// memoryDetails is what is known of the memory used: the most it has
// been, Go's part and the rest, the rows the tabs hold, which closing a
// tab lets go, and the connections open.
func (a *App) memoryDetails(c *ui.Context) {
	m := &a.nav.memory
	muted := widgets.PaletteOf(c).Muted
	line := func(label, value string, indent float32) {
		ui.Row(c).Gap(12).Padding(0, 0, 0, indent).Children(func() {
			ui.Text(c, label).TextColor(muted).SingleLine().Grow(1).Shrink(1)
			ui.Text(c, value).FontFeatures("tnum").SingleLine()
		})
	}
	ui.Column(c).Width(290).Padding(12, 14).Gap(5).FontSize(widgets.TextSmall).Label("Memory").Children(func() {
		ui.Row(c).Gap(12).Children(func() {
			ui.Text(c, "Memory").Bold().Grow(1)
			ui.Text(c, memoryText(m.used)).Bold().FontFeatures("tnum")
		})
		line("Highest this run", memoryText(m.most), 0)
		ui.Divider(c).Margin(3, 0)
		line("Go's", memoryText(m.inGo), 0)
		// The system's count is of what is in memory, and Go's of what it
		// was given: the rest is never less than nothing.
		line("Outside Go: databases, drawing", memoryText(m.used-min(m.used, m.inGo)), 0)
		ui.Divider(c).Margin(3, 0)
		held := a.rowsHeld()
		total := 0
		for _, h := range held {
			total += h.rows
		}
		line("Rows held in results", fmt.Sprintf("%s in %s", groupedInt(total), widgets.Count(len(held), "tab")), 0)
		for _, h := range held[:min(len(held), memoryTabsShown)] {
			line(h.title, groupedInt(h.rows), 12)
		}
		open := 0
		for _, cn := range a.conns {
			if cn.Status == connection.StatusConnected {
				open++
			}
		}
		line("Open connections", strconv.Itoa(open), 0)
		if ui.Button(c, "Free Memory").AlignSelf(ui.Start).Margin(5, 0, 0, 0).
			Tooltip("Drop what Go no longer needs and hand it back to the system. Rows a tab still shows stay.").Clicked() {
			debug.FreeOSMemory()
			m.read(c.Now())
		}
	})
}

// memoryTabsShown is how many tabs the details name, those holding the
// most rows.
const memoryTabsShown = 5

// tabRows is a tab and the rows it holds.
type tabRows struct {
	title string
	rows  int
}

// rowsHeld are the tabs that hold rows, of every window, the one holding
// the most first.
func (a *App) rowsHeld() []tabRows {
	var held []tabRows
	for _, t := range a.everyTab() {
		if h, ok := t.(widgets.RowHolder); ok {
			if rows := h.RowsHeld(); rows > 0 {
				held = append(held, tabRows{t.Title(), rows})
			}
		}
	}
	slices.SortStableFunc(held, func(x, y tabRows) int { return cmp.Compare(y.rows, x.rows) })
	return held
}

// groupedInt is a count with its thousands apart.
func groupedInt(n int) string { return settings.GroupDigits(strconv.Itoa(n)) }

// memoryText is an amount of memory as the foot shows it: whole megabytes,
// which do not flicker as the amount moves, and a gigabyte's tenths.
func memoryText(bytes uint64) string {
	if bytes >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1<<30))
	}
	return fmt.Sprintf("%d MB", (bytes+1<<19)>>20)
}

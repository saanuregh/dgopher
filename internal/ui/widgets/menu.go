package widgets

import (
	"slices"

	"dgopher/internal/keymap"

	"github.com/egoist/mygo/ui"
)

// Menu is a menu being built, as ui.Menu is, for MenuOn, which draws it
// in the app's theme rather than handing it to the system: a button's
// menu then looks as the app's selects do, whatever the desktop's theme.
type Menu struct {
	entries []*MenuItem
	// want is the place of the item chosen, a submenu's after its own,
	// in the run after a click, which Chosen reports.
	want []int
}

// MenuItem is an item of a Menu: set up by its methods, which return it.
type MenuItem struct {
	m                   *Menu
	i                   int
	label, keys, tip    string
	disabled, checkable bool
	checked, separator  bool
	sub                 func(m *Menu)
	built               *Menu // what sub built, once it showed
	id                  int   // the item of a menu the window handed over
}

// submenu builds the item's submenu, once while the menu shows, as a
// system's menu is built as it opens.
func (it *MenuItem) submenu() *Menu {
	if it.built == nil {
		it.built = &Menu{}
		it.sub(it.built)
	}
	return it.built
}

func (m *Menu) add(it *MenuItem) *MenuItem {
	it.m, it.i = m, len(m.entries)
	m.entries = append(m.entries, it)
	return it
}

// Item adds an item.
func (m *Menu) Item(label string) *MenuItem { return m.add(&MenuItem{label: label}) }

// Separator adds a line between groups of items.
func (m *Menu) Separator() { m.add(&MenuItem{separator: true}) }

// Submenu adds an item opening a menu that build fills.
func (m *Menu) Submenu(label string, build func(m *Menu)) *MenuItem {
	it := m.add(&MenuItem{label: label, sub: build})
	if len(m.want) > 1 && m.want[0] == it.i {
		build(&Menu{want: m.want[1:]})
	}
	return it
}

// Disabled shows the item, not to be chosen.
func (it *MenuItem) Disabled(d bool) *MenuItem { it.disabled = d; return it }

// Checked shows a check before the item while on.
func (it *MenuItem) Checked(on bool) *MenuItem { it.checkable, it.checked = true, on; return it }

// Shortcut shows the keys that do what the item does; the menu does not
// take them.
func (it *MenuItem) Shortcut(mods ui.Modifiers, key ui.Key) *MenuItem {
	it.keys = keymap.Chord{Mods: mods, Key: key}.Label()
	return it
}

// Chosen reports whether the item was chosen, in the run after the click.
func (it *MenuItem) Chosen() bool {
	return !it.disabled && len(it.m.want) == 1 && it.m.want[0] == it.i
}

// menuState is what a menu button keeps: whether its menu shows, and the
// item highlighted and the submenu open at each level.
type menuState struct {
	open bool
	root *Menu // built as the menu opened
	hi   []int // by level, -1 for none
	sub  []int // by level, the submenu open, -1 for none
}

// MenuOn makes b a button opening the menu build fills, below it, drawn
// as a select's popup is: a click or Enter chooses an item, which runs
// build again where its Chosen reports it; the arrows move among the
// items, Right and Left into and out of submenus, and Escape or a press
// outside closes it.
func MenuOn(c *ui.Context, b ui.Element, build func(m *Menu)) ui.Element {
	st := ui.Local(b, "menu", func() menuState { return menuState{} })
	b.Role(ui.RoleMenuButton)
	if b.Clicked() {
		*st = menuState{open: !st.open, hi: []int{-1}, sub: []int{-1}}
	}
	if !st.open {
		return b
	}
	if st.root == nil {
		st.root = &Menu{}
		build(st.root)
	}
	root := st.root
	var chosen []int
	menuPopup(c, b, ui.AnchorBottomStart, &st.open, func(panel ui.Element) {
		// The keys go to the menu, as a system's menu takes them while
		// open; it gives the focus back to the button as it closes.
		panel.Focusable().AutoFocus()
		chosen = menuLevel(c, panel, st, 0, root)
	})
	if chosen != nil {
		st.open = false
		build(&Menu{want: chosen})
	}
	if !st.open {
		st.root = nil
	}
	return b
}

// ShownMenus draws the menu the window hands over (ui.DrawnMenu) as MenuOn
// draws a button's, at the point it opens at: every other menu of the
// window, a right-click's, a table's, a text input's or an editor's, so
// that all look the app's rather than the desktop's. The window's view
// calls it once a frame, after what the menus open over.
func ShownMenus(c *ui.Context) {
	shown, ok := ui.DrawnMenu(c)
	if !ok {
		return
	}
	ui.Overlay(c, func() {
		// What the menu hangs from, a button's box or the point it opens
		// at; one per menu, which starts its state afresh.
		a := shown.Anchor
		anchor := ui.Box(c.Key(shown)).Absolute().Left(a.X).Top(a.Y).Size(max(a.W, 1), max(a.H, 1))
		st := ui.Local(anchor, "menu", func() menuState {
			return menuState{open: true, root: handedOver(shown.Items), hi: []int{-1}, sub: []int{-1}}
		})
		var chosen []int
		// Below a button; at a point from its top, not below it, so that
		// one taller than the room below moves up into the window, as a
		// system's menu does.
		at := ui.AnchorTopStart
		if a.H > 0 {
			at = ui.AnchorBottomStart
		}
		menuPopup(c, anchor, at, &st.open, func(panel ui.Element) {
			panel.Focusable().AutoFocus()
			chosen = menuLevel(c, panel, st, 0, st.root)
		})
		switch {
		case chosen != nil:
			shown.Choose(itemAt(st.root, chosen).id)
		case !st.open:
			shown.Close()
		}
	})
}

// handedOver is a menu the window handed over as MenuOn's menus are.
func handedOver(items []ui.MenuEntry) *Menu {
	m := &Menu{}
	for _, e := range items {
		it := m.add(&MenuItem{label: e.Label, keys: e.Keys, tip: e.ToolTip, disabled: e.Disabled, id: e.ID,
			separator: e.Kind == ui.MenuEntrySeparator, checked: e.Checked,
			checkable: e.Kind == ui.MenuEntryCheckbox || e.Kind == ui.MenuEntryRadio})
		if e.Kind == ui.MenuEntrySubmenu {
			it.built = handedOver(e.Submenu)
			it.sub = func(*Menu) {}
		}
	}
	return m
}

// itemAt is the item at a place, a submenu's after its own.
func itemAt(m *Menu, at []int) *MenuItem {
	it := m.entries[at[0]]
	if len(at) > 1 {
		return itemAt(it.submenu(), at[1:])
	}
	return it
}

// menuLevel draws the items of a menu at a level, the submenu open from
// it after them, and handles the keys, which keys has, while it is the
// deepest open. It returns the place of the item chosen, nil for none.
func menuLevel(c *ui.Context, keys ui.Element, st *menuState, level int, m *Menu) (chosen []int) {
	t := c.Theme()
	pal := PaletteOf(c)
	for len(st.hi) <= level {
		st.hi = append(st.hi, -1)
		st.sub = append(st.sub, -1)
	}
	checks := slices.ContainsFunc(m.entries, func(it *MenuItem) bool { return it.checkable })
	// The keys first, in the deepest menu open, and the pointer before
	// any item is drawn: every item then shows the highlight where it
	// is, in the frame it moved in.
	if s := st.sub[level]; s < 0 || s >= len(m.entries) || m.entries[s].sub == nil {
		if got := menuKeys(keys, st, level, m); got != nil {
			return got
		}
	}
	rows := make([]ui.Element, len(m.entries))
	for _, it := range m.entries {
		if it.separator {
			ui.Box(c.Key(it.i)).Height(1).FillWidth().Margin(4, 0).Background(t.Border)
			continue
		}
		row := ui.Row(c.Key(it.i)).Padding(t.Space(1.5), t.Space(2.5)).Gap(8).Radius(t.Radius).AlignItems(ui.Center).
			Label(it.label).Role(ui.RoleMenuItem)
		rows[it.i] = row
		if it.tip != "" {
			row.Tooltip(it.tip)
		}
		if it.disabled {
			row.Disabled(true)
		} else if row.Hovered() && st.hi[level] != it.i {
			st.hi[level] = it.i
			// Onto another item: its submenu opens, another closes.
			st.sub[level] = -1
			if it.sub != nil {
				st.sub[level] = it.i
			}
			st.trim(level)
		}
	}
	for _, it := range m.entries {
		row := rows[it.i]
		if it.separator {
			continue
		}
		on := st.hi[level] == it.i && !it.disabled
		ink := t.Text
		switch {
		case it.disabled:
			ink = pal.Muted
		case on:
			row.Background(t.Accent)
			ink = t.AccentText
		}
		row.TextColor(ink).Children(func() {
			if checks {
				check := ui.Box(c).Size(14, 14).Shrink(0)
				if it.checked {
					check.Children(func() { ui.Icon(c, IconCheck).FontSize(14).TextColor(ink) })
				}
			}
			ui.Text(c, it.label).SingleLine().Grow(1)
			if it.keys != "" {
				keys := ui.Text(c, it.keys).FontSize(12).SingleLine().Shrink(0).MarginStart(16)
				if !on {
					keys.TextColor(pal.Muted)
				}
			}
			if it.sub != nil {
				ui.Icon(c, IconChevronRight).FontSize(12).TextColor(ink).MarginStart(16)
			}
		})
		if row.Clicked() && !it.disabled {
			if it.sub == nil {
				return []int{it.i}
			}
			st.hi[level], st.sub[level] = it.i, it.i
			st.trim(level)
		}
	}
	if s := st.sub[level]; s >= 0 && s < len(m.entries) && m.entries[s].sub != nil && rows[s] != (ui.Element{}) {
		sub := m.entries[s].submenu()
		open := true
		menuPopup(c, rows[s], ui.AnchorTopEnd, &open, func(p ui.Element) {
			p.Margin(-t.Space(1)-1, 0, 0, t.Space(1))
			if got := menuLevel(c, keys, st, level+1, sub); got != nil {
				chosen = append([]int{s}, got...)
			}
		})
		if !open {
			st.sub[level] = -1
			st.trim(level)
		}
		// Closed while it was drawn, as by Left in it: gone next frame,
		// asked for now.
		if st.sub[level] != s {
			c.Invalidate()
		}
	}
	return chosen
}

// menuKeys moves the highlight of the deepest menu open with the arrows,
// and opens, closes or chooses with Right, Left, Enter and Space.
func menuKeys(keys ui.Element, st *menuState, level int, m *Menu) []int {
	usable := func(i int) bool { it := m.entries[i]; return !it.separator && !it.disabled }
	step := func(from, by int) int {
		n := len(m.entries)
		for k := 1; k <= n; k++ {
			if i := ((from+by*k)%n + n) % n; usable(i) {
				return i
			}
		}
		return from
	}
	hi := st.hi[level]
	switch {
	case len(m.entries) == 0:
	case keys.Shortcut(0, ui.KeyDown):
		st.hi[level] = step(max(hi, -1), 1)
	case keys.Shortcut(0, ui.KeyUp):
		st.hi[level] = step(max(hi, 0), -1)
	case keys.Shortcut(0, ui.KeyHome):
		st.hi[level] = step(-1, 1)
	case keys.Shortcut(0, ui.KeyEnd):
		st.hi[level] = step(0, -1)
	case level > 0 && keys.Shortcut(0, ui.KeyLeft):
		st.sub[level-1] = -1
		st.trim(level - 1)
	case hi >= 0 && m.entries[hi].sub != nil && (keys.Shortcut(0, ui.KeyRight) || keys.Shortcut(0, ui.KeyEnter)):
		st.sub[level] = hi
		st.trim(level)
		st.hi = append(st.hi[:level+1], -1)
		st.sub = append(st.sub[:level+1], -1)
		st.hi[level+1] = first(m.entries[hi].submenu())
	case hi >= 0 && usable(hi) && (keys.Shortcut(0, ui.KeyEnter) || keys.Shortcut(0, ui.KeySpace)):
		return []int{hi}
	}
	return nil
}

// first returns the first item of m that can be chosen, -1 for none.
func first(m *Menu) int {
	for i, it := range m.entries {
		if !it.separator && !it.disabled {
			return i
		}
	}
	return -1
}

// trim forgets the levels past one, whose submenu changed.
func (st *menuState) trim(level int) {
	st.hi, st.sub = st.hi[:level+1], st.sub[:level+1]
}

// menuPopup shows a menu's panel attached to anchor at its point at, by
// the panel's top start, while *open: below a button, beside a submenu's
// item. The panel scrolls, where it is taller than the window has room
// for. A press outside it, its anchor and its submenus, or Escape, closes
// it, the submenu on top first; the focus goes back where it was.
func menuPopup(c *ui.Context, anchor ui.Element, at ui.Anchor, open *bool, fn func(panel ui.Element)) {
	if !*open {
		return
	}
	ui.Overlay(c, func() {
		panel := ui.Scroll(c).AttachTo(anchor, at, ui.AnchorTopStart)
		menuPanel(c, panel)
		panel.Children(func() { fn(panel) })
		if panel.OverlayShortcut(0, ui.KeyEscape) || panel.PressedOutside() {
			*open = false
		}
	})
}

// menuPanel gives a menu's panel the look of a select's popup.
func menuPanel(c *ui.Context, panel ui.Element) {
	t := c.Theme()
	panel.Margin(t.Space(1), 0, 0, 0).Padding(t.Space(1)).Radius(t.Radius+2).Background(t.Background).Border(1, t.Border).
		MinWidth(180).Role(ui.RoleMenu).Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.18))
}

package app

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

// The order of a section's files: by name, as DBeaver lists scripts, or
// the last changed first, which DBeaver does not offer. Each section keeps
// its own, on this computer, for every project. Folders come first, by
// name, either way.

// sortedByModified reports whether a section's files are sorted by when
// they last changed.
func (a *App) sortedByModified(s fileSection) bool {
	return slices.Contains(strings.Split(a.layout.NavByModified, "\n"), s.plural)
}

// sortByModified sorts a section's files by when they last changed, or,
// not set, by name.
func (a *App) sortByModified(s fileSection, set bool) {
	var keys []string
	for _, k := range strings.Split(a.layout.NavByModified, "\n") {
		if k != "" && k != s.plural {
			keys = append(keys, k)
		}
	}
	if set {
		keys = append(keys, s.plural)
	}
	slices.Sort(keys)
	a.layout.NavByModified = strings.Join(keys, "\n")
}

// sortMenu offers a section's orders, the one it has checked.
func (a *App) sortMenu(m *ui.Menu, s fileSection) {
	m.Submenu("Sort By", func(m *ui.Menu) {
		byModified := a.sortedByModified(s)
		if m.Item("Name").Checked(!byModified).Chosen() {
			a.sortByModified(s, false)
		}
		if m.Item("Last Modified").Checked(byModified).Chosen() {
			a.sortByModified(s, true)
		}
	})
}

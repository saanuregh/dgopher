package app

import (
	"encoding/json"
	"errors"
	"log"

	"dgopher/internal/state"
	"dgopher/internal/store"

	"github.com/egoist/mygo"
)

// useUIState reads the windows' layout from the UI state file, which keeps
// it from then on; each value missing keeps its default. A sidebar width
// the settings kept before the file did comes over once.
func (a *App) useUIState(ui *state.DB) {
	a.ui = ui
	values, err := layoutValues(a.layout)
	if err != nil {
		log.Println("ui state:", err)
		return
	}
	for name := range values {
		var v json.RawMessage
		switch err := ui.LoadJSON(name, &v); {
		case err == nil:
			values[name] = v
		case !errors.Is(err, store.ErrNotFound):
			log.Println("ui state:", name+":", err)
		}
	}
	data, _ := json.Marshal(values)
	if err := json.Unmarshal(data, &a.layout); err != nil {
		log.Println("ui state:", err)
	}
	a.layoutSaved = a.layout
	var legacy struct {
		SidebarWidth float32 `json:"sidebarWidth"`
	}
	var kept json.RawMessage
	if errors.Is(ui.LoadJSON("sidebarWidth", &kept), store.ErrNotFound) &&
		a.st.LoadJSON("settings.json", &legacy) == nil && legacy.SidebarWidth > 0 {
		a.layout.SidebarWidth = legacy.SidebarWidth // written with the next save
	}
}

// saveLayout writes the values of the layout that changed since it was
// last written; without a UI state file, the layout lasts the run. A value
// that cannot be written is said once, not tried each frame: the next
// change writes it again.
func (a *App) saveLayout() {
	now, err := layoutValues(a.layout)
	was, _ := layoutValues(a.layoutSaved)
	a.layoutSaved = a.layout
	if a.ui == nil || err != nil {
		return
	}
	for name, v := range now {
		if string(v) == string(was[name]) {
			continue
		}
		if err := a.ui.SaveJSON(name, v); err != nil {
			log.Println("ui state:", name+":", err)
		}
	}
}

// layoutValues are a layout's values by their names, as the file keeps
// them, one a row.
func layoutValues(l any) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	var values map[string]json.RawMessage
	return values, json.Unmarshal(data, &values)
}

// windowStates keeps MyGo's window states, the places of the windows
// opened with a StateKey, in the UI state file with the layout, rather than
// in a file of MyGo's own (mygo.App.SetWindowStateStore).
type windowStates struct{ ui *state.DB }

func (s windowStates) LoadWindowStates() (map[string]mygo.WindowState, error) {
	windows, err := s.ui.Windows()
	if err != nil {
		return nil, err
	}
	states := make(map[string]mygo.WindowState, len(windows))
	for key, w := range windows {
		states[key] = mygo.WindowState(w)
	}
	return states, nil
}

func (s windowStates) SaveWindowStates(states map[string]mygo.WindowState) error {
	windows := make(map[string]state.Window, len(states))
	for key, w := range states {
		windows[key] = state.Window(w)
	}
	return s.ui.SaveWindows(windows)
}

// noWindowStates keeps no window states, for a run without the UI state
// file.
type noWindowStates struct{}

func (noWindowStates) LoadWindowStates() (map[string]mygo.WindowState, error) { return nil, nil }
func (noWindowStates) SaveWindowStates(map[string]mygo.WindowState) error     { return nil }

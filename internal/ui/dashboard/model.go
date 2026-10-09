// Package dashboard is the dashboard tab: panels of the results of
// queries, as charts, tables or single values, with parameters they share
// and an automatic refresh, kept in a project's file for its team.
package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"dgopher/internal/project"
	"dgopher/internal/ui/dataview"
)

// Folder is where a project keeps its dashboards.
const Folder = "dashboards"

// How a panel shows its result.
const (
	ViewChart = "chart"
	ViewTable = "table"
	ViewValue = "value"
)

// Views are the ways a panel shows its result, in the order a choice
// lists them.
var Views = []string{ViewChart, ViewTable, ViewValue}

// Columns is how many columns the grid of panels has.
const Columns = 4

// Refreshes are the intervals, in seconds, a dashboard refreshes at; 0 is
// never.
var Refreshes = []int{0, 60, 300, 900, 3600}

// Dashboard is a dashboard as its file keeps it, for a team: the values
// of its parameters are each user's own, kept apart.
type Dashboard struct {
	Name    string  `json:"name"`
	Refresh int     `json:"refreshSeconds,omitempty"`
	Panels  []Panel `json:"panels"`
}

// Panel is a query's result as a dashboard shows it.
type Panel struct {
	Title string `json:"title"`
	// Connection is the connection's ID in the project's dgopher.json.
	Connection string                  `json:"connection"`
	Database   string                  `json:"database,omitempty"`
	SQL        string                  `json:"sql"`
	View       string                  `json:"view"`
	Chart      *dataview.ChartSettings `json:"chart,omitempty"`
	Width      int                     `json:"width"` // in grid columns, 1 to Columns
}

// File is a dashboard's file in a project, by its name.
func File(p *project.Project, name string) string {
	return filepath.Join(p.Dir, Folder, project.Slug(name)+".json")
}

// List names a project's dashboards, by their files, in order.
func List(p *project.Project) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(p.Dir, Folder))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, filepath.Join(p.Dir, Folder, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// Load reads a dashboard's file.
func Load(path string) (*Dashboard, error) {
	d, _, err := LoadFile(path)
	return d, err
}

// LoadFile reads a dashboard's file, with its bytes, which a save checks
// the file still holds.
func LoadFile(path string) (*Dashboard, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var d Dashboard
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if err := d.check(); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return &d, data, nil
}

// check refuses a dashboard its file holds wrong, as edited by hand.
func (d *Dashboard) check() error {
	if strings.TrimSpace(d.Name) == "" {
		return errors.New("the dashboard has no name")
	}
	if !slices.Contains(Refreshes, d.Refresh) {
		return fmt.Errorf("a refresh of %d seconds is not one the app offers", d.Refresh)
	}
	for i := range d.Panels {
		p := &d.Panels[i]
		switch {
		case p.Connection == "":
			return fmt.Errorf("the panel %q names no connection", p.Title)
		case strings.TrimSpace(p.SQL) == "":
			return fmt.Errorf("the panel %q has no query", p.Title)
		case !slices.Contains(Views, p.View):
			return fmt.Errorf("the panel %q shows its result as %q, which is not a view", p.Title, p.View)
		}
		p.Width = min(max(p.Width, 1), Columns)
	}
	return nil
}

// Save writes a dashboard's file, whole or not at all, when it still holds
// was, as read; a new one, when there is none. It returns what it wrote.
func (d *Dashboard) Save(path string, was []byte) ([]byte, error) {
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := project.SaveShared(path, was, data); err != nil {
		return nil, err
	}
	return data, nil
}

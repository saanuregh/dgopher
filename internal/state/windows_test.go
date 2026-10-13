package state

import (
	"database/sql"
	"errors"
	"maps"
	"testing"

	"dgopher/internal/store"
)

func openUI(t *testing.T, dir string, migrations []func(*sql.Tx) error) *DB {
	t.Helper()
	d, err := openFile(dir, UIFile, migrations)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// The windows' places are rows: nothing saved loads as none, a save
// replaces them all, and the next run loads them back.
func TestWindowsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if got, err := openUI(t, dir, uiMigrations).Windows(); err != nil || len(got) != 0 {
		t.Fatalf("nothing saved loads %v, %v", got, err)
	}
	saved := map[string]Window{
		"main":        {X: 40, Y: 50, Width: 800, Height: 600, Maximized: true},
		"preferences": {X: -1200, Y: 10, Width: 500, Height: 400, FullScreen: true},
	}
	if err := openUI(t, dir, uiMigrations).SaveWindows(saved); err != nil {
		t.Fatal(err)
	}
	if got, err := openUI(t, dir, uiMigrations).Windows(); err != nil || !maps.Equal(got, saved) {
		t.Fatalf("the next run loads %v, %v; want %v", got, err, saved)
	}
	delete(saved, "preferences")
	d := openUI(t, dir, uiMigrations)
	if err := d.SaveWindows(saved); err != nil {
		t.Fatal(err)
	}
	if got, err := d.Windows(); err != nil || !maps.Equal(got, saved) {
		t.Fatalf("after a smaller save %v, %v; want %v", got, err, saved)
	}
}

// The places an older version kept as JSON in kv move into the table,
// and the kv row goes; a damaged row goes without them.
func TestWindowsComeOverFromJSON(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        map[string]Window
	}{
		{"kept", `{"main":{"x":40,"y":50,"width":800,"height":600,"maximized":true}}`,
			map[string]Window{"main": {X: 40, Y: 50, Width: 800, Height: 600, Maximized: true}}},
		{"damaged", `{"main":`, map[string]Window{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			old := openUI(t, dir, uiMigrations[:1])
			if _, err := old.SQL().Exec("INSERT INTO kv (name, value) VALUES ('windowState', ?)", tc.value); err != nil {
				t.Fatal(err)
			}
			old.Close()
			d := openUI(t, dir, uiMigrations)
			if got, err := d.Windows(); err != nil || !maps.Equal(got, tc.want) {
				t.Fatalf("loads %v, %v; want %v", got, err, tc.want)
			}
			var v any
			if err := d.LoadJSON("windowState", &v); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("the kv row stays: %v", err)
			}
		})
	}
}

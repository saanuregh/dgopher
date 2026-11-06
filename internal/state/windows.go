package state

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Window is where a window opened with a state key was, in the UI state
// file: its bounds in the normal state, which it gets back when it leaves
// the maximized or full screen state, and that state.
type Window struct {
	X          int  `json:"x"`
	Y          int  `json:"y"`
	Width      int  `json:"width"`
	Height     int  `json:"height"`
	Maximized  bool `json:"maximized,omitempty"`
	FullScreen bool `json:"fullScreen,omitempty"`
	// PixelX, PixelY, PixelWidth and PixelHeight are the bounds in the
	// screen's pixels, which MyGo keeps on Windows, 0 elsewhere and in
	// rows older versions saved.
	PixelX      int `json:"pixelX,omitzero"`
	PixelY      int `json:"pixelY,omitzero"`
	PixelWidth  int `json:"pixelWidth,omitzero"`
	PixelHeight int `json:"pixelHeight,omitzero"`
}

// createWindows makes the windows table and moves into it the places
// older versions kept as JSON in kv; a damaged value is dropped, as
// loading it dropped it.
func createWindows(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE windows (
	key TEXT PRIMARY KEY,
	x INTEGER NOT NULL,
	y INTEGER NOT NULL,
	width INTEGER NOT NULL,
	height INTEGER NOT NULL,
	maximized INTEGER NOT NULL,
	full_screen INTEGER NOT NULL
)`); err != nil {
		return err
	}
	var data string
	switch err := tx.QueryRow("SELECT value FROM kv WHERE name = 'windowState'").Scan(&data); {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	var windows map[string]Window
	if json.Unmarshal([]byte(data), &windows) == nil {
		for key, w := range windows {
			if _, err := tx.Exec("INSERT INTO windows (key, x, y, width, height, maximized, full_screen) VALUES (?, ?, ?, ?, ?, ?, ?)",
				key, w.X, w.Y, w.Width, w.Height, w.Maximized, w.FullScreen); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec("DELETE FROM kv WHERE name = 'windowState'")
	return err
}

// addWindowPixels adds the columns of Window's bounds in pixels; the rows
// saved before have none.
func addWindowPixels(tx *sql.Tx) error {
	for _, col := range []string{"pixel_x", "pixel_y", "pixel_width", "pixel_height"} {
		if _, err := tx.Exec("ALTER TABLE windows ADD COLUMN " + col + " INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	return nil
}

// Windows returns the windows' places by their keys.
func (d *DB) Windows() (map[string]Window, error) {
	rows, err := d.sql.Query("SELECT key, x, y, width, height, maximized, full_screen, pixel_x, pixel_y, pixel_width, pixel_height FROM windows")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	windows := map[string]Window{}
	for rows.Next() {
		var key string
		var w Window
		if err := rows.Scan(&key, &w.X, &w.Y, &w.Width, &w.Height, &w.Maximized, &w.FullScreen,
			&w.PixelX, &w.PixelY, &w.PixelWidth, &w.PixelHeight); err != nil {
			return nil, err
		}
		windows[key] = w
	}
	return windows, rows.Err()
}

// SaveWindows replaces the windows' places with windows.
func (d *DB) SaveWindows(windows map[string]Window) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM windows"); err != nil {
		return err
	}
	if err := insertWindows(tx, windows); err != nil {
		return err
	}
	return tx.Commit()
}

func insertWindows(tx *sql.Tx, windows map[string]Window) error {
	for key, w := range windows {
		if _, err := tx.Exec(`INSERT INTO windows (key, x, y, width, height, maximized, full_screen, pixel_x, pixel_y, pixel_width, pixel_height)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			key, w.X, w.Y, w.Width, w.Height, w.Maximized, w.FullScreen, w.PixelX, w.PixelY, w.PixelWidth, w.PixelHeight); err != nil {
			return err
		}
	}
	return nil
}

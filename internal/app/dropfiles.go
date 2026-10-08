package app

import (
	"os"
	"path/filepath"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// openDropped opens files dropped on the window, by their kind: database
// files as connections, SQL as editors, data files queried by DuckDB.
func (a *App) openDropped(paths []string) {
	for _, p := range paths {
		switch ext := strings.ToLower(filepath.Ext(p)); ext {
		case ".sqlite", ".sqlite3", ".db", ".db3":
			a.openFileDatabase(db.SQLite, p)
		case ".duckdb", ".ddb":
			a.openFileDatabase(db.DuckDB, p)
		case ".sql":
			data, err := os.ReadFile(p)
			if err != nil {
				a.ShowError("Could not open "+filepath.Base(p), err.Error())
				continue
			}
			cn := a.activeConn()
			if cn == nil || !cn.Config.Engine.IsSQL() {
				a.ShowError("Which connection?", "Choose a connection in the navigator, then drop "+filepath.Base(p)+" again.")
				continue
			}
			text := string(data)
			path := p
			a.Connect(cn, func() {
				q := query.New(a, cn, "", filepath.Base(path), text)
				q.Path, q.Saved = path, text
				a.AddTab(q)
			})
		case ".csv", ".tsv", ".parquet", ".json", ".jsonl", ".ndjson":
			a.queryDataFile(p)
		default:
			a.ShowError("Not a file DGopher opens", filepath.Base(p)+": drop a SQLite or DuckDB database, a .sql script, or a CSV, Parquet or JSON file.")
		}
	}
}

// openFileDatabase connects to a database file, adding it once.
func (a *App) openFileDatabase(e db.Engine, path string) {
	for _, cn := range a.conns {
		if cn.Config.Engine == e && cn.Config.Database == path {
			a.activate(navNode{kind: nodeConn, conn: cn.Config.ID})
			return
		}
	}
	a.withProject(func(p *project.Project) {
		cn := a.addConn(p, db.Config{Name: filepath.Base(path), Engine: e, Database: path, Env: db.Development})
		if cn == nil {
			return
		}
		a.activate(navNode{kind: nodeConn, conn: cn.Config.ID})
	})
}

// queryDataFile opens an editor querying a data file with an in-memory
// DuckDB, which reads CSV, Parquet and JSON itself.
func (a *App) queryDataFile(path string) {
	a.withProject(func(p *project.Project) { a.queryDataFileIn(p, path) })
}

func (a *App) queryDataFileIn(p *project.Project, path string) {
	var cn *connection.Conn
	for _, c := range a.projectConns(p) {
		if c.Config.Engine == db.DuckDB && c.Config.Database == ":memory:" {
			cn = c
		}
	}
	if cn == nil {
		cn = a.addConn(p, db.Config{Name: "Files (DuckDB)", Engine: db.DuckDB, Database: ":memory:", Env: db.Development})
		if cn == nil {
			return
		}
	}
	lit := db.Literal(db.DuckDB, path)
	// The name goes in a comment: a newline in it would end the comment
	// and run what follows, as this editor runs at once.
	name := strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, filepath.Base(path))
	text := "-- " + name + ", read in place by DuckDB\nSELECT *\nFROM " + lit + "\nLIMIT 1000;\n"
	a.newQueryFile(cn, "", text, func(q *query.Tab) { q.Run(query.RunScript) })
}

// dropZone takes files dropped anywhere on the window, and shows where
// they would go while they are dragged over it.
func (a *App) dropZone(c *ui.Context, root ui.Element) {
	if files := root.DroppedFiles(); files != nil {
		a.openDropped(files)
	}
	if root.FileDragOver() {
		t := c.Theme()
		ui.Overlay(c, func() {
			ui.Column(c).Absolute().Left(12).Top(12).Right(12).Bottom(12).Center().Gap(6).
				Radius(16).Border(2, t.Accent).BorderStyle(ui.BorderDashed).Background(t.Accent.Alpha(0.08)).PassThrough().
				Children(func() {
					ui.Icon(c, widgets.IconDownload).FontSize(28).TextColor(t.Accent)
					ui.Text(c, "Drop to open").FontSize(16).Bold()
					ui.Text(c, "SQLite or DuckDB databases, .sql scripts, CSV, Parquet or JSON files").TextColor(widgets.PaletteOf(c).Muted)
				})
		})
	}
}

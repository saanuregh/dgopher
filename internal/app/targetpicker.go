package app

import (
	"dgopher/internal/connection"

	"github.com/egoist/mygo/ui"
)

// targetPicker chooses a schema of a database of a SQL connection: where
// tables are copied, or the table compared with is.
type targetPicker struct {
	label            string // the connection, as its select shows it
	database, schema string
	// shown is the connection the database and schema were chosen for.
	shown *connection.Conn
}

func targetLabel(cn *connection.Conn) string {
	return cn.Config.Name + " · " + cn.Config.Engine.Label() + " · " + cn.Project.Name
}

// conn is the connection chosen, nil before one is.
func (p *targetPicker) conn(a *App) *connection.Conn {
	for _, cn := range a.conns {
		if targetLabel(cn) == p.label {
			return cn
		}
	}
	return nil
}

// fields draws the connection's, the database's and the schema's fields,
// connecting the connection chosen; another connection starts at its own
// database and its default schema. It returns the connection chosen.
func (p *targetPicker) fields(c *ui.Context, a *App, label string, disabled bool) *connection.Conn {
	var labels []string
	for _, cn := range a.conns {
		if cn.Config.Engine.IsSQL() {
			labels = append(labels, targetLabel(cn))
		}
	}
	cn := p.conn(a)
	if cn != p.shown {
		p.shown, p.database, p.schema = cn, "", ""
		if cn != nil {
			p.database = cn.Config.Database
		}
	}
	if cn != nil && cn.Status == connection.StatusIdle {
		a.Connect(cn, nil)
	}
	ui.Field(c, label, func() {
		ui.Select(c, &p.label, labels).Label(label).Disabled(disabled)
	})
	if cn == nil {
		return nil
	}
	if cn.SwitchesDatabase() {
		ui.Field(c, "Database", func() {
			ui.Select(c, &p.database, cn.Databases).Label("Database").Disabled(disabled)
		})
	}
	schemas, ok := cn.Schemas[p.database]
	if !ok && cn.Status == connection.StatusConnected && cn.SchemasError(p.database) == "" {
		connection.LoadSchemas(a, cn, p.database, nil)
	}
	if p.schema == "" && ok {
		p.schema = cn.DefaultSchema
	}
	ui.Field(c, "Schema", func() {
		ui.Select(c, &p.schema, schemas).Label("Schema").Disabled(disabled)
	})
	return cn
}

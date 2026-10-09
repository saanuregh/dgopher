package dataview

import (
	"context"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
)

// OpenItemDefinition opens an item's definition, as a routine's, in an
// editor where it can be changed and run.
func OpenItemDefinition(a Host, cn *connection.Conn, database string, it db.Item) {
	poolOf := cn.PoolFor(database) // read on the main thread
	a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var def string
		if err == nil {
			def, err = d.Dialect.ItemDDL(ctx, d.SQL, it)
		}
		return func() {
			if err != nil {
				a.ShowError("Could not read the definition of "+it.Name, err.Error())
				return
			}
			a.NewQueryTab(cn, database, strings.TrimRight(def, "\n")+"\n")
		}
	})
}

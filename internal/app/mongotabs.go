package app

import (
	"dgopher/internal/connection"
	"dgopher/internal/ui/mongo"
	"dgopher/internal/ui/widgets"
)

// The tabs of a document connection: its collections' and its console.
// Each opens once the connection is, or comes forward when it is open.

// openMongoCollection opens a collection's documents.
func (a *App) openMongoCollection(cn *connection.Conn, database, coll string) {
	a.openMongoCollectionWith(cn, database, coll, "")
}

// openMongoCollectionWith opens a collection's documents found with a
// filter, as a saved tab keeps it; an open tab of it comes forward as it
// is.
func (a *App) openMongoCollectionWith(cn *connection.Conn, database, coll, filter string) {
	database = mongo.DatabaseFor(cn, database)
	if a.ActivateTab(func(t widgets.Tab) bool {
		ct, ok := t.(*mongo.CollectionTab)
		return ok && ct.Connection() == cn && ct.Database == database && ct.Collection == coll
	}) {
		return
	}
	a.Connect(cn, func() {
		a.AddTab(mongo.NewCollectionFiltered(a, cn, database, coll, filter))
	})
}

// openMongoConsole opens a console on a database of cn, "" for the
// connection's own, or brings forward the console open on it.
func (a *App) openMongoConsole(cn *connection.Conn, database string) {
	database = mongo.DatabaseFor(cn, database)
	if a.ActivateTab(func(t widgets.Tab) bool {
		ct, ok := t.(*mongo.ConsoleTab)
		return ok && ct.Connection() == cn && ct.Database == database
	}) {
		return
	}
	a.Connect(cn, func() {
		t := mongo.NewConsole(a, cn, database)
		t.OpenCollection = func(database, coll string) { a.openMongoCollection(cn, database, coll) }
		a.AddTab(t)
	})
}

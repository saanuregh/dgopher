package db

// tidbDialect is MySQL's Dialect but for the engine it names, so that
// the rules found through it are TiDB's.
type tidbDialect struct{ mysqlDialect }

func (tidbDialect) Engine() Engine { return TiDB }

// tidbEngine is TiDB: MySQL's rules but for its name and how it is
// reached by default, until it overrides what differs.
type tidbEngine struct{ mysqlEngine }

var _ SQLRules = tidbEngine{}

func (tidbEngine) Engine() Engine       { return TiDB }
func (tidbEngine) dialect() Dialect     { return tidbDialect{} }
func (tidbEngine) Label() string        { return "TiDB" }
func (tidbEngine) Base() Engine         { return MySQL }
func (tidbEngine) ColorHex() string     { return "#e6007a" }
func (tidbEngine) DefaultPort() int     { return 4000 }
func (tidbEngine) DefaultUser() string  { return "root" }
func (tidbEngine) URLSchemes() []string { return []string{"tidb"} }

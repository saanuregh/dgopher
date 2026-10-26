package db

// mariadbDialect is MySQL's Dialect but for the engine it names, so that
// the rules found through it are MariaDB's.
type mariadbDialect struct{ mysqlDialect }

func (mariadbDialect) Engine() Engine { return MariaDB }

// mariadbEngine is MariaDB: MySQL's rules but for its name and how it is
// reached by default, until it overrides what differs.
type mariadbEngine struct{ mysqlEngine }

var _ SQLRules = mariadbEngine{}

func (mariadbEngine) Engine() Engine       { return MariaDB }
func (mariadbEngine) dialect() Dialect     { return mariadbDialect{} }
func (mariadbEngine) Label() string        { return "MariaDB" }
func (mariadbEngine) Base() Engine         { return MySQL }
func (mariadbEngine) ColorHex() string     { return "#c0765a" }
func (mariadbEngine) DefaultPort() int     { return 3306 }
func (mariadbEngine) DefaultUser() string  { return "root" }
func (mariadbEngine) URLSchemes() []string { return []string{"mariadb"} }

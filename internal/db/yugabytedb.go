package db

// yugabytedbDialect is PostgreSQL's Dialect but for the engine it names, so that
// the rules found through it are YugabyteDB's.
type yugabytedbDialect struct{ postgresDialect }

func (yugabytedbDialect) Engine() Engine { return YugabyteDB }

// yugabytedbEngine is YugabyteDB: PostgreSQL's rules but for its name and how it is
// reached by default, until it overrides what differs.
type yugabytedbEngine struct{ postgresEngine }

var _ SQLRules = yugabytedbEngine{}

func (yugabytedbEngine) Engine() Engine       { return YugabyteDB }
func (yugabytedbEngine) dialect() Dialect     { return yugabytedbDialect{} }
func (yugabytedbEngine) Label() string        { return "YugabyteDB" }
func (yugabytedbEngine) Base() Engine         { return Postgres }
func (yugabytedbEngine) ColorHex() string     { return "#ff6e42" }
func (yugabytedbEngine) DefaultPort() int     { return 5433 }
func (yugabytedbEngine) DefaultUser() string  { return "yugabyte" }
func (yugabytedbEngine) URLSchemes() []string { return []string{"yugabytedb"} }

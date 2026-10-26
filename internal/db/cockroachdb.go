package db

// cockroachdbDialect is PostgreSQL's Dialect but for the engine it names, so that
// the rules found through it are CockroachDB's.
type cockroachdbDialect struct{ postgresDialect }

func (cockroachdbDialect) Engine() Engine { return CockroachDB }

// cockroachdbEngine is CockroachDB: PostgreSQL's rules but for its name and how it is
// reached by default, until it overrides what differs.
type cockroachdbEngine struct{ postgresEngine }

var _ SQLRules = cockroachdbEngine{}

func (cockroachdbEngine) Engine() Engine       { return CockroachDB }
func (cockroachdbEngine) dialect() Dialect     { return cockroachdbDialect{} }
func (cockroachdbEngine) Label() string        { return "CockroachDB" }
func (cockroachdbEngine) Base() Engine         { return Postgres }
func (cockroachdbEngine) ColorHex() string     { return "#6933ff" }
func (cockroachdbEngine) DefaultPort() int     { return 26257 }
func (cockroachdbEngine) DefaultUser() string  { return "root" }
func (cockroachdbEngine) URLSchemes() []string { return []string{"cockroachdb", "cockroach"} }

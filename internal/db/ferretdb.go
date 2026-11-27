package db

var _ DocumentRules = ferretdbEngine{}

// ferretdbEngine is FerretDB, which answers MongoDB's protocol over
// PostgreSQL with the DocumentDB extension: MongoDB's rules but for its
// name and what it lacks.
type ferretdbEngine struct{ mongodbEngine }

func (ferretdbEngine) Label() string { return "FerretDB" }
func (ferretdbEngine) Base() Engine  { return MongoDB }

// ColorHex is a blue apart from MongoDB's green: the project publishes no
// colour.
func (ferretdbEngine) ColorHex() string     { return "#3d7bd9" }
func (ferretdbEngine) Mark() string         { return "FD" }
func (ferretdbEngine) URLSchemes() []string { return nil }

// Lacks follows the commands FerretDB 2 registers
// (https://github.com/FerretDB/FerretDB/blob/main/internal/handler/commands.go):
// any other is answered "no such command".
func (ferretdbEngine) Lacks(f DocFeature) string {
	switch f {
	case DocTransactions:
		return "FerretDB has no multi-document transactions."
	case DocProfiler:
		return "FerretDB has no profiler."
	case DocChangeStreams:
		return "FerretDB has no change streams."
	case DocUsers:
		return "FerretDB manages users but cannot grant them roles: it has no grantRolesToUser."
	case DocCurrentOp:
		return "FerretDB cannot end an operation: it has no killOp."
	case DocFieldsByName:
		// Seen on FerretDB 2: the document comes back with its _id twice.
		return "FerretDB cannot change a field whose name holds a dot or starts with $: it corrupts a document an update pipeline replaces."
	}
	return ""
}

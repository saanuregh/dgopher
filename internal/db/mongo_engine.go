package db

// The document family's rules, beside engine.go's registry and kinds.

// DocumentRules is what a document engine answers besides; packages
// outside db reach it through Document.
type DocumentRules interface {
	engineRules
	// Lacks says why the engine has no such feature, "" when it has it.
	Lacks(f DocFeature) string
}

// DocFeature is a feature some document engines lack.
type DocFeature int

const (
	// DocTransactions are multi-document transactions, which also need
	// a replica set or a sharded cluster (Doc.Transactions).
	DocTransactions DocFeature = iota + 1
	DocValidation
	DocProfiler
	DocChangeStreams
	DocGridFS
	DocUsers
	DocCurrentOp
	// DocFieldsByName is setting and removing a field by its name in an
	// update pipeline, with $setField and $unsetField in $replaceWith
	// (DocEdit.Update).
	DocFieldsByName
)

// Document returns the engine's document rules; false for an engine that
// does not store documents or that no build knows.
func Document(e Engine) (DocumentRules, bool) {
	r, ok := rulesOf(e).(DocumentRules)
	return r, ok
}

// IsDocument reports whether the engine stores documents, as MongoDB.
func (e Engine) IsDocument() bool { return e.Kind() == KindDocument }

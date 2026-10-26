package datamodel

import (
	"dgopher/internal/db"
)

// convertType is a column's type of one engine as another's that holds the
// same values, its length, precision and scale kept: varchar(40) stays
// varchar(40), not text. A type no kind has, as an array, takes the type a
// copy of its values would.
func convertType(from, to db.Engine, typ string) string {
	kind, size, scale := db.TypeKindOf(from, typ)
	if kind == "" {
		return db.ColumnType(to, db.CanonicalType(from, typ))
	}
	return db.TypeOfKind(to, kind, size, scale)
}

// keepsKind reports whether an engine has a type of a kind: the type it
// writes for the kind reads back as the kind, where MySQL's DATETIME for a
// time with a zone, or SQLite's INTEGER for a boolean, reads as another.
func keepsKind(e db.Engine, kind db.TypeKind) bool {
	k, _, _ := db.TypeKindOf(e, db.TypeOfKind(e, kind, "", ""))
	return k == kind
}

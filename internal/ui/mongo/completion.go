package mongo

import (
	"slices"
	"strings"

	"dgopher/internal/mongosh"

	code "github.com/egoist/mygo/ui/editor"
)

// complete offers what may complete the word before the caret.
func (t *ConsoleTab) complete(r code.CompletionRequest) {
	start, items := completionsAt(t.Editor.Text(), r.Offset, t.collections, r.Explicit)
	t.Editor.State.ShowCompletions(start, items)
}

// collectionMethods are the methods of a collection the console runs,
// with their arguments.
var collectionMethods = []struct{ name, args string }{
	{"find", "filter, projection"}, {"findOne", "filter, projection"}, {"aggregate", "pipeline"},
	{"countDocuments", "filter"}, {"estimatedDocumentCount", ""}, {"distinct", "field, filter"},
	{"insertOne", "doc"}, {"insertMany", "docs"}, {"updateOne", "filter, update"}, {"updateMany", "filter, update"},
	{"replaceOne", "filter, doc"}, {"deleteOne", "filter"}, {"deleteMany", "filter"},
	{"findOneAndUpdate", "filter, update"}, {"findOneAndReplace", "filter, doc"}, {"findOneAndDelete", "filter"},
	{"bulkWrite", "operations"}, {"getIndexes", ""}, {"createIndex", "keys, options"}, {"dropIndex", "name"},
	{"dropIndexes", ""}, {"stats", ""}, {"renameCollection", "name"}, {"drop", ""},
}

// databaseMethods are the methods of the database the console runs.
var databaseMethods = []struct{ name, args string }{
	{"getCollection", "name"}, {"getSiblingDB", "name"}, {"getCollectionNames", ""}, {"runCommand", "command"},
	{"adminCommand", "command"}, {"aggregate", "pipeline"}, {"stats", ""}, {"serverStatus", ""}, {"currentOp", ""},
	{"createCollection", "name, options"}, {"dropDatabase", ""},
}

// cursorMethods are what a find's cursor takes.
var cursorMethods = []string{"sort", "limit", "skip", "projection", "hint", "collation", "maxTimeMS", "comment", "count", "explain", "toArray"}

// operators are the query, update and pipeline operators, with what they
// do.
var operators = []struct{ name, what string }{
	{"$eq", "query"}, {"$ne", "query"}, {"$gt", "query"}, {"$gte", "query"}, {"$lt", "query"}, {"$lte", "query"},
	{"$in", "query"}, {"$nin", "query"}, {"$and", "query"}, {"$or", "query"}, {"$nor", "query"}, {"$not", "query"},
	{"$exists", "query"}, {"$type", "query"}, {"$regex", "query"}, {"$elemMatch", "query"}, {"$size", "query"},
	{"$all", "query"}, {"$expr", "query"}, {"$text", "query"},
	{"$set", "update"}, {"$unset", "update"}, {"$inc", "update"}, {"$mul", "update"}, {"$rename", "update"},
	{"$push", "update"}, {"$pull", "update"}, {"$addToSet", "update"}, {"$pop", "update"}, {"$currentDate", "update"},
	{"$setOnInsert", "update"},
	{"$match", "stage"}, {"$group", "stage"}, {"$project", "stage"}, {"$sort", "stage"}, {"$limit", "stage"},
	{"$skip", "stage"}, {"$lookup", "stage"}, {"$unwind", "stage"}, {"$count", "stage"}, {"$addFields", "stage"},
	{"$facet", "stage"}, {"$sample", "stage"}, {"$out", "stage · write"}, {"$merge", "stage · write"},
	{"$sum", "accumulator"}, {"$avg", "accumulator"}, {"$min", "accumulator"}, {"$max", "accumulator"},
	{"$first", "accumulator"}, {"$last", "accumulator"},
}

// topWords start a statement or a value.
var topWords = []struct{ name, what string }{
	{"db", "the database"}, {"use", "switch the database"}, {"show", "dbs, collections, users, roles"},
	{"ObjectId", "helper"}, {"ISODate", "helper"}, {"NumberLong", "helper"}, {"NumberInt", "helper"},
	{"NumberDecimal", "helper"}, {"UUID", "helper"}, {"Timestamp", "helper"}, {"BinData", "helper"},
}

// methodTag says what a method does as the safety policy classes it:
// read, write, or write · risky.
func methodTag(collection, method string) string {
	cl := mongosh.Classify(mongosh.Op{Collection: collection, Method: method})
	switch {
	case cl.Risk != "":
		return "write · risky"
	case cl.Writes:
		return "write"
	}
	return "read"
}

func isWordByte(b byte) bool {
	return b == '_' || b == '$' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// completionsAt are what may complete the word before caret in text, and
// where the word starts: collections and methods after db., a
// collection's methods after db.name., a cursor's after a call, and the
// operators for a word starting with $. Without explicit, an empty word
// outside a member offers nothing.
func completionsAt(text string, caret int, collections []string, explicit bool) (int, []code.Completion) {
	caret = max(0, min(caret, len(text)))
	start := caret
	for start > 0 && isWordByte(text[start-1]) {
		start--
	}
	word := text[start:caret]
	var out []code.Completion
	add := func(label, detail string, kind code.CompletionKind, insert string) {
		if strings.HasPrefix(strings.ToLower(label), strings.ToLower(word)) {
			out = append(out, code.Completion{Label: label, Detail: detail, Kind: kind, Insert: insert})
		}
	}
	if start > 0 && text[start-1] == '.' {
		dot := start - 1
		q := dot
		for q > 0 && isWordByte(text[q-1]) {
			q--
		}
		qualifier := text[q:dot]
		before := strings.TrimRight(text[:q], " \t")
		switch {
		case qualifier == "db" && !strings.HasSuffix(before, "."):
			for _, c := range collections {
				if identifier.MatchString(c) {
					add(c, "collection", code.CompletionField, "")
				}
			}
			for _, m := range databaseMethods {
				add(m.name, "("+m.args+") "+methodTag("", m.name), code.CompletionMethod, m.name+"(")
			}
		case qualifier != "" && strings.HasSuffix(before, "db."):
			for _, m := range collectionMethods {
				add(m.name, "("+m.args+") "+methodTag(qualifier, m.name), code.CompletionMethod, m.name+"(")
			}
		case qualifier == "" && dot > 0 && text[dot-1] == ')':
			for _, m := range cursorMethods {
				add(m, "cursor", code.CompletionMethod, m+"(")
			}
		}
		return start, out
	}
	switch {
	case strings.HasPrefix(word, "$"):
		for _, o := range operators {
			add(o.name, o.what, code.CompletionKeyword, "")
		}
	case word != "" || explicit:
		for _, w := range topWords {
			kind := code.CompletionKeyword
			if w.what == "helper" {
				kind = code.CompletionFunction
			}
			add(w.name, w.what, kind, "")
		}
	}
	slices.SortStableFunc(out, func(a, b code.Completion) int {
		return strings.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label))
	})
	return start, out
}

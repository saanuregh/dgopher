package sqltext

import (
	"math/big"
	"slices"
	"strings"
)

// whereEnds are the clauses that end an UPDATE's or a DELETE's WHERE.
var whereEnds = []string{"RETURNING", "ORDER", "LIMIT", "OFFSET", "OUTPUT"}

// alwaysTrue reports whether a WHERE condition holds for every row, as
// TRUE, 1 = 1 or a column compared with itself does: the slip of a
// condition left to be filled in. toks are the condition's tokens.
func alwaysTrue(toks []Token) bool {
	for len(toks) > 0 && isPunct(toks[len(toks)-1], ";") {
		toks = toks[:len(toks)-1]
	}
	dep := depths(toks)
	// One term of an OR at the top always true makes the whole so.
	for _, term := range splitTop(toks, dep, "OR") {
		if allTrue(term) {
			return true
		}
	}
	return false
}

// allTrue reports whether every part of a term's top-level AND always
// holds.
func allTrue(toks []Token) bool {
	for _, part := range splitTop(toks, depths(toks), "AND") {
		if !trueTerm(part) {
			return false
		}
	}
	return true
}

// splitTop splits tokens at a word outside parentheses; at AND, at
// MySQL's && as well, which no other engine reads as a condition's, but
// not at the AND of a BETWEEN.
func splitTop(toks []Token, dep []int, sep string) [][]Token {
	var out [][]Token
	start := 0
	between := false
	for i := 0; i <= len(toks); i++ {
		if i < len(toks) && dep[i] == 0 && word(toks[i]) == "BETWEEN" {
			between = true
		}
		if i < len(toks) && between && dep[i] == 0 && sep == "AND" && word(toks[i]) == "AND" {
			between = false
			continue
		}
		if i == len(toks) || dep[i] == 0 && (word(toks[i]) == sep || sep == "AND" && isOperator(toks[i], "&&")) {
			out = append(out, toks[start:i])
			start = i + 1
		}
	}
	return out
}

// trueTerm reports whether a condition without a top-level OR or AND
// always holds: a constant true, a comparison of a thing with itself, a
// constant IN, BETWEEN or IS NULL test that holds, or a group in
// parentheses always true. Casts are read through: TRUE::bool and
// CAST(1 AS int) are constants.
func trueTerm(toks []Token) bool {
	if len(toks) >= 2 && isPunct(toks[0], "(") && isPunct(toks[len(toks)-1], ")") && closes(toks) {
		return alwaysTrue(toks[1 : len(toks)-1]) // a group may hold ORs and ANDs
	}
	dep := depths(toks)
	for i := 1; i < len(toks)-1; i++ {
		if toks[i].Kind != Operator || toks[i].Text == "::" || dep[i] != 0 {
			continue
		}
		left, _ := stripCasts(toks[:i])
		right, _ := stripCasts(toks[i+1:])
		if !sameTokens(left, right) && !sameColumn(left, right) {
			return constantsCompare(left, right, toks[i].Text)
		}
		switch toks[i].Text {
		case "=", "==", "<=", ">=", "<=>":
			return true
		}
		return false
	}
	if i := topLevelIndex(toks, "BETWEEN"); i > 0 {
		return trueBetween(toks[:i], toks[i+1:])
	}
	if i := topLevelIndex(toks, "IN"); i > 0 {
		return trueIn(toks[:i], toks[i+1:])
	}
	if i := topLevelIndex(toks, "IS"); i > 0 {
		return trueIsNull(toks[:i], toks[i+1:])
	}
	if len(toks) > 1 && word(toks[0]) == "NOT" {
		v, toBool := stripCasts(toks[1:])
		return len(v) == 1 && falsy(v[0], toBool)
	}
	v, toBool := stripCasts(toks)
	return len(v) == 1 && truthy(v[0], toBool)
}

// stripCasts returns the value toks cast, without its casts: "::type", any
// number of times, CAST(value AS type), and parentheses around the value.
// toBool reports a cast to boolean among them.
func stripCasts(toks []Token) (value []Token, toBool bool) {
	for {
		dep := depths(toks)
		cast := -1
		for i, t := range toks {
			if dep[i] == 0 && isOperator(t, "::") {
				cast = i
				break
			}
		}
		switch {
		case cast > 0:
			// Each part after a top-level "::" is a type.
			start := cast + 1
			for j := start; j <= len(toks); j++ {
				if j == len(toks) || dep[j] == 0 && isOperator(toks[j], "::") {
					toBool = toBool || boolType(toks[start:j])
					start = j + 1
				}
			}
			toks = toks[:cast]
		case len(toks) > 2 && isPunct(toks[0], "(") && isPunct(toks[len(toks)-1], ")") && closes(toks):
			toks = toks[1 : len(toks)-1]
		case len(toks) > 3 && word(toks[0]) == "CAST" && isPunct(toks[1], "(") && isPunct(toks[len(toks)-1], ")") && closes(toks[1:]):
			as := -1
			for j := 2; j < len(toks)-1; j++ {
				if dep[j] == 1 && word(toks[j]) == "AS" {
					as = j
					break
				}
			}
			if as < 0 {
				return toks, toBool
			}
			toBool = toBool || boolType(toks[as+1:len(toks)-1])
			toks = toks[2:as]
		default:
			return toks, toBool
		}
	}
}

// boolType reports whether a cast's type is boolean: bool or boolean,
// qualified by a schema or not.
func boolType(toks []Token) bool {
	if len(toks) == 3 && isPunct(toks[1], ".") {
		toks = toks[2:]
	}
	if len(toks) != 1 {
		return false
	}
	name := word(toks[0])
	if toks[0].Kind == QuotedIdent {
		name = strings.ToUpper(DecodeQuoted(toks[0].Text, Postgres))
	}
	return name == "BOOL" || name == "BOOLEAN"
}

// boolValue reads text as PostgreSQL reads a boolean: true, yes, on or 1,
// false, no, off or 0, or a prefix only one of them starts with, in any
// case and with spaces around.
func boolValue(s string) (value, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, b := range []struct {
		word  string
		least int
		value bool
	}{
		{"true", 1, true}, {"yes", 1, true}, {"on", 2, true}, {"1", 1, true},
		{"false", 1, false}, {"no", 1, false}, {"off", 2, false}, {"0", 1, false},
	} {
		if len(s) >= b.least && strings.HasPrefix(b.word, s) {
			return b.value, true
		}
	}
	return false, false
}

func isOperator(t Token, s string) bool { return t.Kind == Operator && t.Text == s }

// closes reports whether the first token's parenthesis closes at the
// last token.
func closes(toks []Token) bool {
	dep := depths(toks)
	for i := 1; i < len(toks)-1; i++ {
		if dep[i] == 0 {
			return false
		}
	}
	return true
}

// truthy reports whether t is a true constant: TRUE, a number but 0, or,
// cast to boolean, a string that reads as true.
func truthy(t Token, toBool bool) bool {
	if word(t) == "TRUE" {
		return true
	}
	if toBool && t.Kind == String {
		v, ok := boolValue(DecodeQuoted(t.Text, Postgres))
		return ok && v
	}
	n, ok := number(t)
	return ok && n.Sign() != 0
}

// falsy reports whether t is a false constant, as truthy reads it.
func falsy(t Token, toBool bool) bool {
	if word(t) == "FALSE" {
		return true
	}
	if toBool && t.Kind == String {
		v, ok := boolValue(DecodeQuoted(t.Text, Postgres))
		return ok && !v
	}
	n, ok := number(t)
	return ok && n.Sign() == 0
}

func number(t Token) (*big.Rat, bool) {
	if t.Kind != Number {
		return nil, false
	}
	return new(big.Rat).SetString(t.Text)
}

// trueBetween reports whether "left [NOT] BETWEEN rest" holds of
// constants: 1 BETWEEN 0 AND 2.
func trueBetween(left, rest []Token) bool {
	not := len(left) > 1 && word(left[len(left)-1]) == "NOT"
	if not {
		left = left[:len(left)-1]
	}
	symmetric := len(rest) > 0 && word(rest[0]) == "SYMMETRIC"
	if len(rest) > 0 && (symmetric || word(rest[0]) == "ASYMMETRIC") {
		rest = rest[1:]
	}
	and := topLevelIndex(rest, "AND")
	if and < 0 {
		return false
	}
	x, xok := constantNumber(left)
	lo, lok := constantNumber(rest[:and])
	hi, hok := constantNumber(rest[and+1:])
	if !xok || !lok || !hok {
		return false
	}
	if symmetric && lo.Cmp(hi) > 0 {
		lo, hi = hi, lo
	}
	return (lo.Cmp(x) <= 0 && x.Cmp(hi) <= 0) != not
}

// trueIn reports whether "left [NOT] IN (list)" holds: the list holds
// left, as 1 IN (2, 1) or id IN (id), or, for NOT IN, only constants
// that differ from a constant left.
func trueIn(left, list []Token) bool {
	not := len(left) > 1 && word(left[len(left)-1]) == "NOT"
	if not {
		left = left[:len(left)-1]
	}
	if len(list) < 3 || !isPunct(list[0], "(") || !isPunct(list[len(list)-1], ")") || !closes(list) {
		return false
	}
	left, _ = stripCasts(left)
	if len(left) == 1 && word(left[0]) == "NULL" {
		return false // NULL IN (...) is NULL, never true
	}
	inner := list[1 : len(list)-1]
	dep := depths(inner)
	start := 0
	for i := 0; i <= len(inner); i++ {
		if i < len(inner) && (dep[i] != 0 || !isPunct(inner[i], ",")) {
			continue
		}
		item, _ := stripCasts(inner[start:i])
		start = i + 1
		same := sameTokens(left, item) || sameColumn(left, item) || constantsCompare(left, item, "=")
		switch {
		case !not && same:
			return true
		case not && (same || !constantsCompare(left, item, "<>")):
			return false
		}
	}
	return not
}

// trueIsNull reports whether "left IS [NOT] NULL" holds of a constant:
// NULL IS NULL, 1 IS NOT NULL.
func trueIsNull(left, rest []Token) bool {
	left, _ = stripCasts(left)
	not := len(rest) > 0 && word(rest[0]) == "NOT"
	if not {
		rest = rest[1:]
	}
	if len(left) != 1 || len(rest) != 1 || word(rest[0]) != "NULL" {
		return false
	}
	if not {
		_, isNumber := number(left[0])
		return isNumber || word(left[0]) == "TRUE" || word(left[0]) == "FALSE"
	}
	return word(left[0]) == "NULL"
}

// constantNumber is the number toks hold, read through casts.
func constantNumber(toks []Token) (*big.Rat, bool) {
	v, _ := stripCasts(toks)
	if len(v) != 1 {
		return nil, false
	}
	return number(v[0])
}

// sameColumn reports whether one side names a column by its qualified
// name and the other by its name alone, as t.id and id: in an UPDATE's or
// a DELETE's WHERE the bare name is the same column, or the server
// rejects it as ambiguous.
func sameColumn(a, b []Token) bool {
	if len(a) < len(b) {
		a, b = b, a
	}
	if len(b) != 1 || len(a) < 3 || len(a)%2 == 0 || !sameTokens(a[len(a)-1:], b) {
		return false
	}
	for i, t := range a {
		name := t.Kind == Identifier || t.Kind == QuotedIdent || t.Kind == Keyword
		if i%2 == 0 && !name || i%2 == 1 && !isPunct(t, ".") {
			return false
		}
	}
	return true
}

// sameTokens reports whether two runs of tokens read alike: the same
// column, or the same value.
func sameTokens(a, b []Token) bool {
	return slices.EqualFunc(a, b, func(x, y Token) bool { return x.Kind == y.Kind && x.Text == y.Text })
}

// constantsCompare reports whether two different constants compare true
// by op: 1 <> 2, 'a' != 'b'.
func constantsCompare(a, b []Token, op string) bool {
	if len(a) != 1 || len(b) != 1 {
		return false
	}
	x, xok := number(a[0])
	y, yok := number(b[0])
	switch {
	case xok && yok:
		c := x.Cmp(y)
		switch op {
		case "<>", "!=":
			return c != 0
		case "=", "==":
			return c == 0
		case "<":
			return c < 0
		case ">":
			return c > 0
		case "<=":
			return c <= 0
		case ">=":
			return c >= 0
		}
	case a[0].Kind == String && b[0].Kind == String:
		return op == "<>" || op == "!="
	}
	return false
}

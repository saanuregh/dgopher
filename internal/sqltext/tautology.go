package sqltext

import (
	"math/big"
	"slices"
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
// holds. A BETWEEN's AND splits it into parts that do not.
func allTrue(toks []Token) bool {
	for _, part := range splitTop(toks, depths(toks), "AND") {
		if !trueTerm(part) {
			return false
		}
	}
	return true
}

// splitTop splits tokens at a word outside parentheses; at AND, at
// MySQL's && as well, which no other engine reads as a condition's.
func splitTop(toks []Token, dep []int, sep string) [][]Token {
	var out [][]Token
	start := 0
	for i := 0; i <= len(toks); i++ {
		if i == len(toks) || dep[i] == 0 && (word(toks[i]) == sep || sep == "AND" && isOperator(toks[i], "&&")) {
			out = append(out, toks[start:i])
			start = i + 1
		}
	}
	return out
}

// trueTerm reports whether a condition without a top-level OR or AND
// always holds: a constant true, a comparison of a thing with itself, or
// a group in parentheses always true.
func trueTerm(toks []Token) bool {
	if len(toks) >= 2 && isPunct(toks[0], "(") && isPunct(toks[len(toks)-1], ")") && closes(toks) {
		return alwaysTrue(toks[1 : len(toks)-1]) // a group may hold ORs and ANDs
	}
	switch {
	case len(toks) == 1:
		return truthy(toks[0])
	case len(toks) == 2 && word(toks[0]) == "NOT":
		return falsy(toks[1])
	case len(toks) >= 3:
		for i := 1; i < len(toks)-1; i++ {
			if toks[i].Kind != Operator {
				continue
			}
			left, right := toks[:i], toks[i+1:]
			if !sameTokens(left, right) {
				return constantsCompare(left, right, toks[i].Text)
			}
			switch toks[i].Text {
			case "=", "==", "<=", ">=", "<=>":
				return true
			}
			return false
		}
	}
	return false
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

func truthy(t Token) bool {
	if word(t) == "TRUE" {
		return true
	}
	n, ok := number(t)
	return ok && n.Sign() != 0
}

func falsy(t Token) bool {
	if word(t) == "FALSE" {
		return true
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

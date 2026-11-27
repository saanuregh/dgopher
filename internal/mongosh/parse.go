package mongosh

import (
	"fmt"
	"strings"
)

// Value is a value of a statement: bson.D (keys in order), bson.A, or a
// primitive of go.mongodb.org/mongo-driver/v2/bson.
type Value = any

// Call is a method called on a cursor or on what a method returns.
type Call struct {
	Name string
	Args []Value
}

// Op is a parsed statement.
type Op struct {
	Text string // the statement as written
	// Database is the database db.getSiblingDB("x") or `use x` names; ""
	// for the tab's.
	Database   string
	Collection string // "" for database methods
	// Method is the collection or database method (find, aggregate,
	// insertOne, runCommand, …), "show" or "use".
	Method string
	Args   []Value
	// Chain is what is called on the method's result: sort, limit, skip,
	// projection, hint, explain, toArray, ….
	Chain []Call
}

// notJavaScript starts the refusal of what only JavaScript could run.
const notJavaScript = "the console runs MongoDB shell statements, not JavaScript: "

// maxDepth bounds how deeply values nest, so a hostile text cannot
// exhaust the stack.
const maxDepth = 200

var showTopics = map[string]string{
	"dbs": "dbs", "databases": "dbs", "collections": "collections", "tables": "collections",
	"users": "users", "roles": "roles", "profile": "profile",
}

// javaScriptWords are the keywords that start what the console does not
// run, with what it is called.
var javaScriptWords = map[string]string{
	"var": "variables", "let": "variables", "const": "variables",
	"function": "functions", "async": "functions", "class": "classes",
	"for": "loops", "while": "loops", "do": "loops",
	"if": "conditions", "switch": "conditions", "try": "exceptions", "throw": "exceptions",
	"return": "functions", "await": "functions", "import": "modules", "export": "modules",
}

type parser struct {
	src   string
	toks  []token
	i     int
	depth int
}

// Parse parses one statement. Its error names the 1-based line and
// column, and what was expected there.
func Parse(stmt string) (Op, error) {
	p := &parser{src: stmt, toks: lex(stmt)}
	op, err := p.statement()
	if err != nil {
		return Op{}, err
	}
	op.Text = stmt
	return op, nil
}

func (p *parser) peek() token {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return token{kind: tokenEnd, start: len(p.src), end: len(p.src)}
}

func (p *parser) peekAt(n int) token {
	if p.i+n < len(p.toks) {
		return p.toks[p.i+n]
	}
	return token{kind: tokenEnd, start: len(p.src), end: len(p.src)}
}

func (p *parser) next() token {
	t := p.peek()
	if p.i < len(p.toks) {
		p.i++
	}
	return t
}

func (p *parser) errAt(t token, format string, args ...any) error {
	line, col := position(p.src, t.start)
	return fmt.Errorf("%d:%d: %s", line, col, fmt.Sprintf(format, args...))
}

// unexpected is the error of a token that is not what was expected, or
// what is wrong with it if it is a string or comment that does not end.
func (p *parser) unexpected(t token, expected string) error {
	switch {
	case t.kind == tokenBad && strings.HasPrefix(t.text, "/*"):
		return p.errAt(t, "the comment does not end")
	case t.kind == tokenBad:
		return p.errAt(t, "the string does not end")
	case t.is("="):
		return p.errAt(t, notJavaScript+"assignments are not supported")
	case t.is("=>"):
		return p.errAt(t, notJavaScript+"functions are not supported")
	case t.kind == tokenPunct && strings.Contains("+-*/%", t.text):
		return p.errAt(t, notJavaScript+"arithmetic is not supported")
	}
	return p.errAt(t, "expected %s, found %s", expected, describe(t))
}

func describe(t token) string {
	if t.kind == tokenEnd {
		return "the end of the statement"
	}
	return fmt.Sprintf("%q", t.text)
}

func (p *parser) expect(punct, expected string) (token, error) {
	t := p.next()
	if !t.is(punct) {
		return t, p.unexpected(t, expected)
	}
	return t, nil
}

// end accepts one ; and then the end of the statement.
func (p *parser) end(expected string) error {
	t := p.next()
	if t.kind == tokenEnd {
		return nil
	}
	if !t.is(";") {
		if t.is("=") {
			return p.errAt(t, notJavaScript+"assignments are not supported")
		}
		return p.unexpected(t, expected)
	}
	if t := p.next(); t.kind != tokenEnd {
		return p.errAt(t, "expected the end of the statement, found %s", describe(t))
	}
	return nil
}

func (p *parser) statement() (Op, error) {
	t := p.peek()
	if t.kind == tokenEnd {
		return Op{}, p.errAt(t, "expected a statement")
	}
	if t.kind != tokenIdent {
		if t.kind == tokenBad {
			return Op{}, p.unexpected(t, "")
		}
		return Op{}, p.errAt(t, notJavaScript+"expected db, use or show, found %s", describe(t))
	}
	switch t.text {
	case "db":
		p.next()
		return p.dbStatement()
	case "use":
		return p.use()
	case "show":
		return p.show()
	case "it":
		return Op{}, p.errAt(t, "it is not supported: the results load more rows as you scroll")
	case "exit", "quit":
		return Op{}, p.errAt(t, "%s is not supported: close the tab instead", t.text)
	}
	if what, ok := javaScriptWords[t.text]; ok {
		return Op{}, p.errAt(t, notJavaScript+"%s are not supported", what)
	}
	switch after := p.peekAt(1); {
	case after.is("="):
		return Op{}, p.errAt(after, notJavaScript+"assignments are not supported")
	case after.is("("):
		return Op{}, p.errAt(t, notJavaScript+"%s() is not a shell statement here; expected db, use or show", t.text)
	}
	return Op{}, p.errAt(t, notJavaScript+"%s is not a shell statement here; expected db, use or show", t.text)
}

// use reads `use name`. A name may hold characters that are tokens of
// their own, as shop-2024, so the name is the text of adjacent tokens.
func (p *parser) use() (Op, error) {
	keyword := p.next()
	first := p.peek()
	if first.kind == tokenEnd || first.is(";") {
		return Op{}, p.errAt(first, "expected a database name after use")
	}
	last := p.next()
	for t := p.peek(); t.kind != tokenEnd && !t.is(";") && t.start == last.end; t = p.peek() {
		last = p.next()
	}
	if t := p.peek(); t.kind != tokenEnd && !t.is(";") {
		return Op{}, p.errAt(t, "expected one database name after %s", keyword.text)
	}
	name := p.src[first.start:last.end]
	if err := p.end("the end of the statement"); err != nil {
		return Op{}, err
	}
	return Op{Database: name, Method: "use", Args: []Value{name}}, nil
}

func (p *parser) show() (Op, error) {
	p.next()
	t := p.next()
	topic, ok := showTopics[t.text]
	if t.kind != tokenIdent || !ok {
		return Op{}, p.errAt(t, "expected dbs, databases, collections, tables, users, roles or profile after show")
	}
	if err := p.end("the end of the statement"); err != nil {
		return Op{}, err
	}
	return Op{Method: "show", Args: []Value{topic}}, nil
}

// dbStatement reads what follows db: getSiblingDB and getCollection, a
// collection's name and its method, or a database method; then the chain.
func (p *parser) dbStatement() (Op, error) {
	var op Op
	after := "db"
	for {
		switch t := p.peek(); {
		case t.is("["):
			name, err := p.bracketName()
			if err != nil {
				return Op{}, err
			}
			op.Collection = name
			return p.collection(op)
		case t.is("."):
			p.next()
			name, err := p.ident("a collection or method")
			if err != nil {
				return Op{}, err
			}
			if !p.peek().is("(") {
				op.Collection = name.text
				return p.collection(op)
			}
			args, err := p.args()
			if err != nil {
				return Op{}, err
			}
			switch name.text {
			case "getSiblingDB":
				s, err := p.nameArg(name, args, "getSiblingDB expects a database name")
				if err != nil {
					return Op{}, err
				}
				op.Database, after = s, "db.getSiblingDB()"
				continue
			case "getCollection":
				s, err := p.nameArg(name, args, "getCollection expects a collection name")
				if err != nil {
					return Op{}, err
				}
				op.Collection = s
				return p.collection(op)
			case "use", "show":
				return Op{}, p.errAt(name, "%s is a statement of its own, as %s, not a database method", name.text, map[string]string{"use": "use shop", "show": "show collections"}[name.text])
			}
			op.Method, op.Args = name.text, args.values
			return p.chain(op)
		default:
			return Op{}, p.unexpected(t, `"." and a collection or method after `+after)
		}
	}
}

// collection reads the rest of a collection's name, as profile in
// db.system.profile, then its method.
func (p *parser) collection(op Op) (Op, error) {
	for {
		switch t := p.peek(); {
		case t.is("["):
			name, err := p.bracketName()
			if err != nil {
				return Op{}, err
			}
			op.Collection += "." + name
		case t.is("."):
			p.next()
			name, err := p.ident("a method")
			if err != nil {
				return Op{}, err
			}
			if !p.peek().is("(") {
				op.Collection += "." + name.text
				continue
			}
			args, err := p.args()
			if err != nil {
				return Op{}, err
			}
			op.Method, op.Args = name.text, args.values
			return p.chain(op)
		case t.is("="):
			return Op{}, p.errAt(t, notJavaScript+"assignments are not supported")
		default:
			return Op{}, p.unexpected(t, "a method call such as .find() after the collection")
		}
	}
}

func (p *parser) chain(op Op) (Op, error) {
	for p.peek().is(".") {
		p.next()
		name, err := p.ident("a method")
		if err != nil {
			return Op{}, err
		}
		if !p.peek().is("(") {
			return Op{}, p.unexpected(p.peek(), `"(" after `+name.text)
		}
		args, err := p.args()
		if err != nil {
			return Op{}, err
		}
		op.Chain = append(op.Chain, Call{Name: name.text, Args: args.values})
	}
	if err := p.end(`"." or the end of the statement`); err != nil {
		return Op{}, err
	}
	return op, nil
}

func (p *parser) ident(expected string) (token, error) {
	t := p.next()
	if t.kind != tokenIdent {
		return t, p.unexpected(t, expected)
	}
	return t, nil
}

func (p *parser) bracketName() (string, error) {
	p.next()
	t := p.next()
	if t.kind != tokenString {
		return "", p.unexpected(t, "a collection name in quotes")
	}
	s, err := p.decodeString(t)
	if err != nil {
		return "", err
	}
	if _, err := p.expect("]", `"]"`); err != nil {
		return "", err
	}
	return s, nil
}

// arguments are the values of a call, with where each starts.
type arguments struct {
	values []Value
	tokens []token
	close  token
}

func (p *parser) args() (arguments, error) {
	var a arguments
	p.next() // (
	for {
		t := p.peek()
		if t.is(")") {
			a.close = p.next()
			return a, nil
		}
		v, err := p.value(`a value or ")"`)
		if err != nil {
			return a, err
		}
		a.values, a.tokens = append(a.values, v), append(a.tokens, t)
		switch t := p.next(); {
		case t.is(","):
		case t.is(")"):
			a.close = t
			return a, nil
		default:
			return a, p.afterValue(t, `"," or ")"`)
		}
	}
}

// afterValue is the error of what follows a value where a separator was
// expected.
func (p *parser) afterValue(t token, expected string) error {
	switch {
	case t.is("."):
		return p.errAt(t, notJavaScript+"property access on a value is not supported")
	case t.is("?"), t.is("&"), t.is("|"), t.is("<"), t.is(">"), t.is("!"):
		return p.errAt(t, notJavaScript+"operators are not supported")
	}
	return p.unexpected(t, expected)
}

func (p *parser) nameArg(call token, a arguments, msg string) (string, error) {
	if len(a.values) == 1 {
		if s, ok := a.values[0].(string); ok && s != "" {
			return s, nil
		}
		return "", p.errAt(a.tokens[0], "%s", msg)
	}
	return "", p.errAt(call, "%s", msg)
}

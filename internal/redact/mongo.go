package redact

import (
	"strings"

	"dgopher/internal/mongosh"
)

// mongoToken is a token of MongoDB shell or JSON text, as far as finding
// its secrets needs: a string, a word, a regular expression or one
// character.
type mongoToken struct {
	start, end int
	kind       byte // '"' a string, 'w' a word, 'r' a regular expression, else the character itself
}

// Mongo replaces the secrets of a MongoDB shell statement or JSON command
// with •••: the values of pwd and password fields (as createUser,
// updateUser and auth take them), the password auth and
// changeUserPassword take as their second argument, and a connection
// string's password. A string that only mentions a password stays.
func Mongo(text string) string {
	cuts := mongoSecrets(text)
	var b strings.Builder
	last := 0
	for _, c := range cuts {
		if c.start < last {
			continue
		}
		b.WriteString(text[last:c.start])
		if c.kind == '"' {
			b.WriteByte(text[c.start])
			b.WriteString("•••")
			if closed(text, c) {
				b.WriteByte(text[c.start])
			}
		} else {
			b.WriteString("•••")
		}
		last = c.end
	}
	b.WriteString(text[last:])
	out := b.String()
	if strings.Contains(out, "://") {
		out = urlPassword.ReplaceAllString(out, "${1}•••@")
	}
	return out
}

// mongoSecrets are the tokens of text that are secrets, in order.
func mongoSecrets(text string) []mongoToken {
	toks := mongoTokens(text)
	var cuts []mongoToken
	for i, t := range toks {
		switch {
		case i+2 < len(toks) && toks[i+1].kind == ':' && secretKey(text, t):
			if v := toks[i+2]; v.kind == '"' || v.kind == 'w' && (i+3 >= len(toks) || toks[i+3].kind != '(') {
				cuts = append(cuts, v)
			}
		case t.kind == 'w' && i > 0 && toks[i-1].kind == '.' && i+1 < len(toks) && toks[i+1].kind == '(':
			if name := text[t.start:t.end]; name == "auth" || name == "changeUserPassword" {
				if v, ok := secondArgument(toks[i+1:]); ok {
					cuts = append(cuts, v)
				}
			}
		}
	}
	return cuts
}

// MongoError hides in an error the secrets of the MongoDB statement or
// command it came from, as Mongo finds them, since a server may quote
// them (a duplicate key error names the key's value), and what Error
// hides. text is the statement as written, before Mongo.
func MongoError(msg, text string) string {
	if msg == "" {
		return ""
	}
	for _, c := range mongoSecrets(text) {
		v := text[c.start:c.end]
		if c.kind == '"' {
			v = v[1:]
			if closed(text, c) {
				v = v[:len(v)-1]
			}
		}
		for _, form := range []string{v, unescapeBackslashes(v)} {
			if len([]rune(form)) >= 3 {
				msg = hideValue(msg, form)
			}
		}
	}
	return Error(msg, text)
}

// closed reports whether the string token t ends with the quote it
// starts with.
func closed(text string, t mongoToken) bool {
	return t.end-t.start > 1 && text[t.end-1] == text[t.start]
}

// secretKey reports whether a key names a password, as pwd, password or
// tlsCertificateKeyFilePassword do, but not passwordDigestor or
// digestPassword, which say how a password is hashed.
func secretKey(text string, t mongoToken) bool {
	if t.kind != '"' && t.kind != 'w' {
		return false
	}
	k := strings.ToLower(strings.Trim(text[t.start:t.end], "\"'`"))
	switch k {
	case "pwd", "passwd":
		return true
	case "passworddigestor", "digestpassword":
		return false
	}
	return strings.Contains(k, "password")
}

// secondArgument is the second argument of the call whose ( starts toks,
// when it is a string or a word.
func secondArgument(toks []mongoToken) (mongoToken, bool) {
	depth, arg := 0, 0
	for i, t := range toks {
		switch t.kind {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth--; depth == 0 {
				return mongoToken{}, false
			}
		case ',':
			if depth == 1 {
				if arg++; arg == 1 && i+1 < len(toks) && (toks[i+1].kind == '"' || toks[i+1].kind == 'w') {
					return toks[i+1], true
				}
			}
		}
	}
	return mongoToken{}, false
}

// mongoTokens reads the tokens of a text as the console's parser does,
// so that a regular expression, a string and a comment are told apart as
// they are when the statement runs.
func mongoTokens(s string) []mongoToken {
	toks := mongosh.Tokens(s)
	out := make([]mongoToken, len(toks))
	for i, t := range toks {
		kind := s[t.Start]
		switch t.Kind {
		case mongosh.Word:
			kind = 'w'
		case mongosh.String:
			kind = '"'
		case mongosh.Regex:
			kind = 'r'
		}
		out[i] = mongoToken{t.Start, t.End, kind}
	}
	return out
}

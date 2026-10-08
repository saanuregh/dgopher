// Package redact removes secrets from SQL and Redis commands before they
// are kept in history or the audit log.
package redact

import (
	"regexp"
	"strconv"
	"strings"

	"dgopher/internal/sqltext"
)

// redisSecrets find the secrets of Redis commands that a regular
// expression reads well: AUTH [user] password, and CONFIG SET of the
// settings that hold passwords.
var redisSecrets = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(\s*auth\s+)(.*)$`),
	regexp.MustCompile(`(?i)(\bconfig\s+set\s+(?:requirepass|masterauth|masteruser)\s+)(\S+)`),
}

var aclPassword = regexp.MustCompile(`(\s)>\S+`)

// credentialFunctions take credentials among their arguments: their
// strings are redacted whole.
var credentialFunctions = map[string]bool{
	"dblink": true, "dblink_connect": true, "dblink_connect_u": true, "dblink_exec": true, "dblink_open": true,
	"mysql": true, "postgresql": true, "mongodb": true, "redis": true, "remote": true, "remotesecure": true,
	"s3": true, "s3cluster": true, "gcs": true, "azureblobstorage": true, "azureblobstoragecluster": true,
	"url": true, "deltalake": true, "iceberg": true, "hudi": true,
}

// Secrets replaces the secrets of a statement or command with •••,
// for the query history and the audit log.
func Secrets(s string) string {
	if isRedisCredentialCommand(s) {
		s = redactRedisArgs(s)
	} else {
		// Each lexer reads strings its own way: PostgreSQL's dollar
		// quotes, MySQL's double quotes.
		s = redactSQLSecrets(s, sqltext.Postgres)
		s = redactSQLSecrets(s, sqltext.MySQL)
	}
	for _, re := range redisSecrets {
		s = re.ReplaceAllString(s, "${1}•••")
	}
	return aclPassword.ReplaceAllString(s, "${1}>•••")
}

func isRedisCredentialCommand(s string) bool {
	f := strings.Fields(s)
	if len(f) == 0 {
		return false
	}
	switch strings.ToUpper(f[0]) {
	case "MIGRATE", "HELLO":
		return true
	}
	return false
}

// redactRedisArgs hides what follows AUTH (one argument) and AUTH2 (two),
// as MIGRATE and HELLO take them.
func redactRedisArgs(s string) string {
	f := strings.Fields(s)
	for i := 0; i < len(f); i++ {
		n := 0
		switch strings.ToUpper(f[i]) {
		case "AUTH":
			n = 1
			if strings.EqualFold(f[0], "HELLO") {
				n = 2 // HELLO … AUTH user password
			}
		case "AUTH2":
			n = 2
		}
		for k := 1; k <= n && i+k < len(f); k++ {
			f[i+k] = "•••"
		}
		i += n
	}
	return strings.Join(f, " ")
}

// redactSQLSecrets replaces the strings that carry passwords: after
// PASSWORD (PASSWORD FOR 'u' = 'x' included) or IDENTIFIED … BY, and the
// strings among the arguments of credentialFunctions. It reads the tokens,
// so a string that only mentions a password stays.
func redactSQLSecrets(s string, d sqltext.Dialect) string {
	toks := sqltext.Tokenize(s, d)
	runes := []rune(s)
	var b strings.Builder
	last := 0
	cut := func(t sqltext.Token) {
		b.WriteString(string(runes[last:t.Start]))
		b.WriteString("'•••'")
		last = t.End
	}
	const (
		idle = iota
		wantSecret
		wantEquals // after PASSWORD FOR: the user, then =
	)
	state, identified := idle, false
	depth, inFunc := 0, false
	prevIdent := ""
	for _, t := range toks {
		switch t.Kind {
		case sqltext.Whitespace, sqltext.Comment:
			continue
		case sqltext.String, sqltext.QuotedIdent:
			if inFunc && t.Kind == sqltext.String || state == wantSecret || t.Kind == sqltext.String && carriesPassword(t.Text) {
				cut(t)
				if state == wantSecret {
					state = idle
				}
			}
			prevIdent = ""
			continue
		}
		word := strings.ToUpper(t.Text)
		switch {
		case word == "(" && credentialFunctions[strings.ToLower(prevIdent)] && !inFunc:
			inFunc, depth = true, 1
		case word == "(" && inFunc:
			depth++
		case word == ")" && inFunc:
			if depth--; depth == 0 {
				inFunc = false
			}
		}
		switch {
		case secretWord(word):
			state = wantSecret
		case word == "FOR" && state == wantSecret:
			state = wantEquals
		case word == "=" && (state == wantEquals || state == wantSecret):
			state = wantSecret
		case state == wantEquals:
			// the user and host before =
		case state == wantSecret && (word == "(" || word == "U" || word == "E" || word == "N" || word == "&"):
			// PASSWORD('x'), U&'x', E'x': the secret is the string after
		case word == "IDENTIFIED":
			identified = true
		case (word == "BY" || word == "REPLACE") && identified:
			state = wantSecret
		default:
			state = idle
		}
		prevIdent = ""
		if t.Kind == sqltext.Identifier || t.Kind == sqltext.Keyword {
			prevIdent = t.Text
		}
	}
	b.WriteString(string(runes[last:]))
	return b.String()
}

// redisSecretSettings are the CONFIG SET parameters that hold secrets.
var redisSecretSettings = map[string]bool{
	"requirepass": true, "masterauth": true, "masteruser": true,
	"tls-key-file-pass": true, "tls-client-key-file-pass": true,
}

// Redis hides the secrets among a Redis command's arguments, read
// as the server reads them, and writes the command for the logs.
func Redis(args []string) string {
	out := append([]string(nil), args...)
	hide := func(i int) {
		if i < len(out) {
			out[i] = "•••"
		}
	}
	if len(out) == 0 {
		return ""
	}
	switch strings.ToUpper(out[0]) {
	case "AUTH":
		for i := 1; i < len(out); i++ {
			hide(i)
		}
	case "CONFIG":
		for i := 2; i+1 < len(out); i += 2 {
			if redisSecretSettings[strings.ToLower(out[i])] {
				hide(i + 1)
			}
		}
	case "ACL":
		for i := 2; i < len(out); i++ {
			if strings.HasPrefix(out[i], ">") || strings.HasPrefix(out[i], "#") {
				out[i] = out[i][:1] + "•••"
			}
		}
	}
	for i := 1; i < len(out); i++ {
		switch strings.ToUpper(out[i]) {
		case "AUTH":
			hide(i + 1)
			if strings.EqualFold(out[0], "HELLO") {
				hide(i + 2)
			}
		case "AUTH2":
			hide(i + 1)
			hide(i + 2)
		}
	}
	return quoteArgs(out)
}

// secretWord reports whether a keyword introduces a secret, as
// PASSWORD, SECRET, or MySQL's SOURCE_PASSWORD.
func secretWord(w string) bool {
	switch w {
	case "PASSWORD", "SECRET", "TOKEN", "SESSION_TOKEN":
		return true
	}
	for _, suffix := range []string{"_PASSWORD", "_SECRET", "_TOKEN", "ACCESS_KEY"} {
		if strings.HasSuffix(w, suffix) {
			return true
		}
	}
	return false
}

// carriesPassword reports whether a string holds a connection string's
// password, as 'host=h password=x' does.
func carriesPassword(text string) bool {
	l := strings.ToLower(text)
	return strings.Contains(l, "password=") || strings.Contains(l, "password =") || strings.Contains(l, "pwd=")
}

func quoteArgs(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n\"'") {
			out[i] = strconv.Quote(a)
		} else {
			out[i] = a
		}
	}
	return strings.Join(out, " ")
}

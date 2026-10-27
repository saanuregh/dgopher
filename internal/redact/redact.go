// Package redact removes secrets from SQL and Redis commands before they
// are kept in history or the audit log.
package redact

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/sqltext"
)

// redisSecrets find the secrets of Redis commands that a regular
// expression reads well: AUTH [user] password, and CONFIG SET of the
// settings that hold passwords.
// Each word is one its expression cannot match without, so a text that
// lacks it skips the expression.
var redisSecrets = []struct {
	word string
	re   *regexp.Regexp
}{
	{"auth", regexp.MustCompile(`(?i)^(\s*auth\s+)(.*)$`)},
	{"config", regexp.MustCompile(`(?i)(\bconfig\s+set\s+(?:requirepass|masterauth|masteruser)\s+)(\S+)`)},
	{"sentinel", regexp.MustCompile(`(?i)(\bsentinel\s+(?:config\s+)?set\b.*?\s(?:auth-pass|sentinel-pass)\s+)(\S+)`)},
}

// aclPassword finds ACL SETUSER's >password and <password (which
// removes one) rules.
var aclPassword = regexp.MustCompile(`([\s'"])([<>])[^\s'"]+`)

var aclSetUser = regexp.MustCompile(`(?i)\bacl\s+setuser\b`)

// connPassword finds the password of a connection string.
var connPassword = regexp.MustCompile(`(?i)\b(?:password|pwd|passwd|accountkey|motherduck_token)\s*=\s*'?([^\s'";]+)`)

// passwordKey finds the keys of a connection string, URL query or HTTP
// header that hold a secret.
var passwordKey = regexp.MustCompile(`(?i)(?:password|pwd|passwd|accountkey|motherduck_token)\s*=|bearer\s`)

var bearerToken = regexp.MustCompile(`(?i)\bbearer\s+([^\s'",}]+)`)

// urlPassword finds the password of a URL's user information, as in
// postgres://user:password@host.
var urlPassword = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.\-]*://[^\s:/@'"]*:)([^\s@/]+)@`)

// credentialFunctions take credentials among their arguments: their
// strings are redacted whole.
var credentialFunctions = map[string]bool{
	"dblink": true, "dblink_connect": true, "dblink_connect_u": true, "dblink_exec": true, "dblink_open": true,
	"mysql": true, "postgresql": true, "mongodb": true, "redis": true, "remote": true, "remotesecure": true,
	"s3": true, "s3cluster": true, "gcs": true, "azureblobstorage": true, "azureblobstoragecluster": true,
	"url": true, "deltalake": true, "iceberg": true, "hudi": true,
	"materializedpostgresql": true, "materializedmysql": true, "s3queue": true, "azurequeue": true,
	"icebergs3": true, "icebergazure": true, "iceberghdfs": true, "icebergs3cluster": true,
	"deltalakecluster": true, "hudicluster": true, "externaldistributed": true, "odbc": true, "jdbc": true,
}

// credentialProcedures are SQL Server's procedures that take a password
// among their arguments: the strings after one are cut, to the end of the
// statement.
var credentialProcedures = map[string]bool{
	"sp_addlinkedsrvlogin": true, "sp_password": true, "sp_addlogin": true, "sp_setapprole": true,
	"sp_addapprole": true, "sp_change_users_login": true,
}

// lexDialects are the lexers whose secrets are all redacted: callers do
// not know the engine, and each reads strings its own way (PostgreSQL's
// dollar quotes, MySQL's and ClickHouse's backslash escapes, Oracle's
// q'[…]' quotes).
var lexDialects = []sqltext.Dialect{sqltext.Postgres, sqltext.MySQL, sqltext.ClickHouse, sqltext.Oracle}

// Secrets replaces the secrets of a statement or command with •••,
// for the query history and the audit log.
func Secrets(s string) string {
	if isRedisCredentialCommand(s) {
		s = redactRedisArgs(s)
	} else {
		s = redactSQL(s)
	}
	for _, r := range redisSecrets {
		if mayContainFold(s, r.word) {
			s = r.re.ReplaceAllString(s, "${1}•••")
		}
	}
	// In SQL, < and > are operators.
	if f := strings.Fields(s); len(f) > 0 && strings.EqualFold(f[0], "ACL") {
		s = aclPassword.ReplaceAllString(s, "${1}${2}•••")
	} else if mayContainFold(s, "setuser") {
		if loc := aclSetUser.FindStringIndex(s); loc != nil {
			s = s[:loc[1]] + aclPassword.ReplaceAllString(s[loc[1]:], "${1}${2}•••")
		}
	}
	if !strings.Contains(s, "://") {
		return s
	}
	return urlPassword.ReplaceAllString(s, "${1}•••@")
}

// redactSQL cuts the union of the secrets every lexer finds in the
// original text, so a string one lexer ends early is still cut whole.
func redactSQL(s string) string {
	var ranges []secretRange
	for _, d := range lexDialects {
		r, _ := sqlSecrets(s, d)
		ranges = append(ranges, r...)
	}
	if len(ranges) == 0 {
		return s
	}
	slices.SortFunc(ranges, func(a, b secretRange) int { return a.start - b.start })
	var b strings.Builder
	last := 0
	for i := 0; i < len(ranges); {
		start, end := ranges[i].start, ranges[i].end
		for i++; i < len(ranges) && ranges[i].start < end; i++ {
			end = max(end, ranges[i].end)
		}
		b.WriteString(s[last:start])
		b.WriteString("'•••'")
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// Error hides in an error the secrets of the statement it is about, which
// a server may quote, as MySQL's syntax errors do, cut off where a lexer
// would not find them; then what Secrets finds.
func Error(msg, stmt string) string {
	if msg == "" {
		return ""
	}
	for _, d := range lexDialects {
		_, found := sqlSecrets(stmt, d)
		for _, text := range found {
			for _, v := range secretValues(text) {
				msg = hideValue(msg, v)
			}
		}
	}
	for _, m := range urlPassword.FindAllStringSubmatch(stmt, -1) {
		if len([]rune(m[2])) >= 3 {
			msg = hideValue(msg, m[2])
		}
	}
	return Secrets(msg)
}

// minSecret is the fewest runes of a secret, or of its start, that an
// error is searched for: fewer would hide ordinary words.
const minSecret = 6

// hideValue hides a secret in a message, or the longest start of it the
// message holds, as an error that quotes so many characters of the text
// cuts it off.
func hideValue(msg, v string) string {
	r := []rune(v)
	if len(r) < minSecret {
		return strings.ReplaceAll(msg, v, "•••")
	}
	for n := len(r); n >= minSecret; n-- {
		if part := string(r[:n]); strings.Contains(msg, part) {
			return strings.ReplaceAll(msg, part, "•••")
		}
	}
	return msg
}

// secretValues are how a server may write a string token's value: as it
// is written, with doubled quotes undone, and with backslash escapes
// undone. A value too short to tell from other text is left out.
func secretValues(text string) []string {
	if i := strings.IndexAny(text, `'"$`); i >= 0 {
		text = text[i:]
	}
	inner := strings.Trim(text, `'"`)
	if strings.HasPrefix(text, "$") {
		if j := strings.Index(text[1:], "$"); j >= 0 {
			tag := text[:j+2]
			inner = strings.TrimSuffix(strings.TrimPrefix(text, tag), tag)
		}
	}
	values := []string{inner, strings.ReplaceAll(inner, "''", "'"), strings.ReplaceAll(inner, `""`, `"`), unescapeBackslashes(inner)}
	for _, v := range values[1:] {
		// an error may name a connection string's password or a token alone
		for _, m := range connPassword.FindAllStringSubmatch(v, -1) {
			values = append(values, m[1])
		}
		for _, m := range bearerToken.FindAllStringSubmatch(v, -1) {
			values = append(values, m[1])
		}
	}
	var out []string
	for _, v := range values {
		if len([]rune(v)) >= 3 && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// unescapeBackslashes undoes MySQL's and ClickHouse's backslash escapes
// by keeping the character each backslash escapes.
func unescapeBackslashes(s string) string {
	var b strings.Builder
	escaped := false
	for _, r := range s {
		if r == '\\' && !escaped {
			escaped = true
			continue
		}
		escaped = false
		b.WriteRune(r)
	}
	return b.String()
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

// secretRange is a secret's bytes [start, end) in a text.
type secretRange struct{ start, end int }

// sqlSecrets finds, as one lexer reads them, the strings that carry
// passwords: after PASSWORD (PASSWORD FOR 'u' = 'x' included) or
// IDENTIFIED … BY, the strings among the arguments of credentialFunctions,
// Oracle's bare-word password right after IDENTIFIED BY, and after the
// REPLACE that follows it,
// and the MAP {…} after a word such as EXTRA_HTTP_HEADERS. It reads the
// tokens, so a string that only mentions a password stays. found are the
// tokens cut that are secrets for certain, as written.
func sqlSecrets(s string, d sqltext.Dialect) (ranges []secretRange, found []string) {
	toks := sqltext.Tokenize(s, d)
	// known is a secret for certain: a password, not any argument of a
	// function that takes credentials among others.
	cut := func(t sqltext.Token, known bool) {
		ranges = append(ranges, secretRange{t.Start, t.End})
		if known {
			found = append(found, s[t.Start:t.End])
		}
	}
	const (
		idle = iota
		wantSecret
		wantEquals // after PASSWORD FOR: the user, then =
	)
	state, identified := idle, false
	// bare: the token may be a bare-word password, as right after
	// IDENTIFIED BY, or after the REPLACE that follows that password;
	// afterSecret: the last token was that password.
	bare, afterSecret, lastWord := false, false, ""
	depth, inFunc := 0, false
	inCall := false // the arguments of a credentialProcedures call
	prevIdent := ""
	// A secret MAP {…} is cut whole; its values, not its keys, are found.
	mapDepth, mapStart := 0, 0
	var mapValue sqltext.Token
	hasMapValue := false
	for i, t := range toks {
		switch t.Kind {
		case sqltext.Whitespace, sqltext.Comment:
			continue
		}
		wasBare, wasSecret, prevWord := bare, afterSecret, lastWord
		bare, afterSecret, lastWord = false, false, ""
		if mapDepth > 0 {
			switch {
			case t.Kind == sqltext.String:
				if hasMapValue {
					found = append(found, s[mapValue.Start:mapValue.End])
				}
				mapValue, hasMapValue = t, true
			case t.Text == ":":
				hasMapValue = false // the string before : is a key
			case t.Text == "{":
				mapDepth++
			case t.Text == "}":
				if mapDepth--; mapDepth == 0 {
					if hasMapValue {
						found = append(found, s[mapValue.Start:mapValue.End])
					}
					ranges = append(ranges, secretRange{mapStart, t.End})
					state = idle
				}
			}
			continue
		}
		switch t.Kind {
		case sqltext.String, sqltext.QuotedIdent:
			if (inFunc || inCall) && t.Kind == sqltext.String || state == wantSecret || t.Kind == sqltext.String && carriesPassword(t.Text) {
				cut(t, state == wantSecret || inCall || carriesPassword(t.Text))
				if state == wantSecret {
					state, afterSecret = idle, wasBare
				}
			}
			prevIdent = ""
			continue
		}
		word := strings.ToUpper(t.Text)
		if wasBare && state == wantSecret && (t.Kind == sqltext.Identifier || t.Kind == sqltext.Keyword) && !keywordAfterBy(toks, i) {
			if word == "VALUES" || prefixesString(toks, i) {
				bare = true // the secret is the string that follows
			} else {
				cut(t, true)
				state, afterSecret = idle, true
			}
			continue
		}
		switch {
		case word == ";":
			inCall = false
		case t.Kind == sqltext.Identifier || t.Kind == sqltext.Keyword:
			inCall = inCall || credentialProcedures[strings.ToLower(t.Text)]
		}
		if word == "{" && state == wantSecret {
			mapDepth, mapStart, hasMapValue = 1, t.Start, false
			continue
		}
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
		case state == wantSecret && (word == "(" || word == "U" || word == "E" || word == "N" || word == "&" || word == "MAP"):
			// PASSWORD('x'), U&'x', E'x', MAP {…}: the secret is what follows
		case word == "IDENTIFIED":
			identified = true
		case (word == "BY" || word == "REPLACE") && identified:
			state = wantSecret
			bare = word == "BY" && prevWord == "IDENTIFIED" || word == "REPLACE" && wasSecret
		default:
			state = idle
		}
		prevIdent = ""
		if t.Kind == sqltext.Identifier || t.Kind == sqltext.Keyword {
			prevIdent = t.Text
		}
		lastWord = word
	}
	return ranges, found
}

// keywordAfterBy reports a word at toks[i], after IDENTIFIED BY, that is
// no password: MySQL's RANDOM PASSWORD and PASSWORD 'hash', whose hash
// PASSWORD makes a secret, and Oracle's EXTERNAL STORE.
func keywordAfterBy(toks []sqltext.Token, i int) bool {
	var next sqltext.Token
	for _, t := range toks[i+1:] {
		if t.Kind != sqltext.Whitespace && t.Kind != sqltext.Comment {
			next = t
			break
		}
	}
	switch strings.ToUpper(toks[i].Text) {
	case "RANDOM":
		return strings.EqualFold(next.Text, "PASSWORD")
	case "PASSWORD":
		return next.Kind == sqltext.String
	case "EXTERNAL":
		return strings.EqualFold(next.Text, "STORE")
	}
	return false
}

// prefixesString reports a word written against the string or & after
// it, as the E of E'x', the q of q'[x]' a lexer other than Oracle's
// reads, and the U of U&'x'.
func prefixesString(toks []sqltext.Token, i int) bool {
	if i+1 >= len(toks) || toks[i+1].Start != toks[i].End {
		return false
	}
	return toks[i+1].Kind == sqltext.String || toks[i+1].Text == "&"
}

// redisSecretSettings are the CONFIG SET parameters that hold secrets.
var redisSecretSettings = map[string]bool{
	"requirepass": true, "masterauth": true, "masteruser": true,
	"tls-key-file-pass": true, "tls-client-key-file-pass": true,
}

// sentinelSecretSettings are the SENTINEL SET and SENTINEL CONFIG SET
// options that hold passwords.
var sentinelSecretSettings = map[string]bool{"auth-pass": true, "sentinel-pass": true}

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
			if strings.HasPrefix(out[i], ">") || strings.HasPrefix(out[i], "<") || strings.HasPrefix(out[i], "#") {
				out[i] = out[i][:1] + "•••"
			}
		}
	case "SENTINEL":
		// SENTINEL SET master option value … and SENTINEL CONFIG SET
		// parameter value …: the pairs start at the fourth argument.
		for i := 3; i+1 < len(out); i += 2 {
			if sentinelSecretSettings[strings.ToLower(out[i])] {
				hide(i + 1)
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
// PASSWORD, SECRET, MySQL's SOURCE_PASSWORD, or DuckDB's CONNECTION_STRING
// and EXTRA_HTTP_HEADERS.
func secretWord(w string) bool {
	switch w {
	case "PASSWORD", "SECRET", "TOKEN", "SESSION_TOKEN", "CONNECTION_STRING", "EXTRA_HTTP_HEADERS":
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
// password or a token, as 'host=h password=x', 'md:db?motherduck_token=x'
// and 'Bearer x' do.
func carriesPassword(text string) bool {
	return mayContainFold(text, "password", "pwd", "passwd", "accountkey", "motherduck_token", "bearer") &&
		passwordKey.MatchString(text)
}

// mayContainFold reports whether s may hold one of the lowercase ASCII
// words, matched as (?i) matches them, so a regular expression that needs
// the word can be skipped when it is false. A text with ſ or K, which
// (?i) folds to s and k, is reported as a match.
func mayContainFold(s string, words ...string) bool {
	if strings.Contains(s, "\u017f") || strings.Contains(s, "\u212a") {
		return true
	}
	for _, w := range words {
		for i := 0; i+len(w) <= len(s); i++ {
			if asciiLower(s[i]) == w[0] && asciiEqualFold(s[i:i+len(w)], w) {
				return true
			}
		}
	}
	return false
}

func asciiEqualFold(s, lower string) bool {
	for i := 0; i < len(lower); i++ {
		if asciiLower(s[i]) != lower[i] {
			return false
		}
	}
	return true
}

func asciiLower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
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

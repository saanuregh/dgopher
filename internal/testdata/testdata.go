// Package testdata makes up the values of a table's columns, to fill it
// with rows that look real: names, e-mails, numbers and dates in ranges,
// existing values of the tables a column refers to.
package testdata

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Kind is what a generator makes.
type Kind int

const (
	Default Kind = iota // the column is left to its default
	Null
	Sequence
	Integer
	Decimal
	Boolean
	Date
	Timestamp
	Time
	UUID
	Words
	Sentence
	Paragraph
	FirstName
	LastName
	FullName
	Username
	Email
	Phone
	City
	Country
	Company
	Street
	URL
	OneOf
	Reference // an existing value of the column a foreign key refers to
	JSON
)

// Kinds are the kinds in the order a list of them shows.
var Kinds = []Kind{Default, Null, Sequence, Integer, Decimal, Boolean, Date, Timestamp, Time, UUID,
	Words, Sentence, Paragraph, FirstName, LastName, FullName, Username, Email, Phone, City, Country,
	Company, Street, URL, OneOf, Reference, JSON}

var labels = map[Kind]string{
	Default: "Default", Null: "NULL", Sequence: "Sequence", Integer: "Integer", Decimal: "Decimal",
	Boolean: "Boolean", Date: "Date", Timestamp: "Date and time", Time: "Time of day", UUID: "UUID",
	Words: "Words", Sentence: "Sentence", Paragraph: "Paragraph", FirstName: "First name",
	LastName: "Last name", FullName: "Full name", Username: "Username", Email: "E-mail", Phone: "Phone",
	City: "City", Country: "Country", Company: "Company", Street: "Street address", URL: "URL",
	OneOf: "One of", Reference: "Existing value", JSON: "JSON",
}

// Label names a kind as a list of them shows it.
func (k Kind) Label() string { return labels[k] }

// Ranged reports whether a kind's values lie between From and To.
func (k Kind) Ranged() bool {
	switch k {
	case Integer, Decimal, Date, Timestamp:
		return true
	}
	return false
}

// Textual reports whether a kind makes text, which Unique and MaxLength
// apply to.
func (k Kind) Textual() bool {
	switch k {
	case Words, Sentence, Paragraph, FirstName, LastName, FullName, Username, Email, Phone, City, Country, Company, Street, URL:
		return true
	}
	return false
}

// Generator says how a column's values are made.
type Generator struct {
	Kind Kind
	// From and To bound the values of a ranged kind, as typed: numbers,
	// or dates as 2006-01-02, a time after one; From starts a Sequence.
	From, To string
	Scale    int      // the digits after a decimal's point
	Values   []string // the choices of OneOf and Reference
	// NullPercent is the share of the rows, in percent, left NULL.
	NullPercent int
	// Unique makes each text differ from the others of the run, and of
	// the runs before.
	Unique bool
	// MaxLength cuts text to as many characters; 0 for no limit.
	MaxLength int
}

// Limit is the most rows a run makes, which unique text has room for.
const Limit = 1_000_000

// Values makes the value of the i-th row (from 0): an int64, a float's
// text, a bool, a time.Time, a string, or nil for NULL.
type Values func(r *rand.Rand, i int) any

// Prepare checks a generator and readies it. run is a short text unique
// to this run, which unique text carries.
func (g Generator) Prepare(run string) (Values, error) {
	if g.NullPercent < 0 || g.NullPercent > 100 {
		return nil, errors.New("the share of NULLs is a percentage, from 0 to 100")
	}
	values, err := g.maker(run)
	if err != nil {
		return nil, err
	}
	if g.NullPercent == 0 {
		return values, nil
	}
	return func(r *rand.Rand, i int) any {
		if r.IntN(100) < g.NullPercent {
			return nil
		}
		return values(r, i)
	}, nil
}

func (g Generator) maker(run string) (Values, error) {
	switch g.Kind {
	case Default:
		return nil, errors.New("a column left to its default has no values")
	case Null:
		return func(*rand.Rand, int) any { return nil }, nil
	case Sequence:
		from, err := parseInt(g.From, 1, "the first number")
		if err != nil {
			return nil, err
		}
		return func(_ *rand.Rand, i int) any { return from + int64(i) }, nil
	case Integer:
		from, to, err := intRange(g.From, g.To)
		if err != nil {
			return nil, err
		}
		return func(r *rand.Rand, _ int) any { return from + r.Int64N(to-from+1) }, nil
	case Decimal:
		from, to, err := floatRange(g.From, g.To)
		if err != nil {
			return nil, err
		}
		if g.Scale < 0 || g.Scale > 18 {
			return nil, errors.New("a decimal has from 0 to 18 digits after its point")
		}
		return func(r *rand.Rand, _ int) any {
			return strconv.FormatFloat(from+r.Float64()*(to-from), 'f', g.Scale, 64)
		}, nil
	case Boolean:
		return func(r *rand.Rand, _ int) any { return r.IntN(2) == 1 }, nil
	case Date, Timestamp:
		from, to, err := timeRange(g.From, g.To)
		if err != nil {
			return nil, err
		}
		// In whole seconds: a span of nanoseconds overflows past 292 years.
		start, span := from.Unix(), to.Unix()-from.Unix()
		return func(r *rand.Rand, _ int) any {
			t := time.Unix(start+r.Int64N(span+1), 0).UTC()
			if g.Kind == Date {
				return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
			}
			return t
		}, nil
	case Time:
		return func(r *rand.Rand, _ int) any {
			return time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(r.Int64N(86400)) * time.Second).Format(time.TimeOnly)
		}, nil
	case UUID:
		return func(r *rand.Rand, _ int) any { return uuid(r) }, nil
	case OneOf, Reference:
		if len(g.Values) == 0 {
			if g.Kind == Reference {
				return nil, errors.New("the table referred to has no rows to refer to")
			}
			return nil, errors.New("list the values to choose from")
		}
		return func(r *rand.Rand, _ int) any { return g.Values[r.IntN(len(g.Values))] }, nil
	case JSON:
		return func(r *rand.Rand, i int) any {
			return fmt.Sprintf(`{"id": %d, "label": %q, "active": %t, "score": %d}`, i+1, pick(r, words), r.IntN(2) == 1, r.IntN(100))
		}, nil
	}
	if !g.Kind.Textual() {
		return nil, fmt.Errorf("no generator of kind %d", g.Kind)
	}
	if g.Unique && g.MaxLength > 0 {
		// The mark, a dash and the run's and a million rows' numbers,
		// with a character of the text and an e-mail's domain.
		need := 1 + 1 + len(run) + len(strconv.Itoa(Limit))
		if g.Kind == Email {
			need += len("@example.com")
		}
		if g.MaxLength < need {
			return nil, fmt.Errorf("%d characters are too few for generated text to be unique: choose a sequence", g.MaxLength)
		}
	}
	text := textMaker(g.Kind)
	return func(r *rand.Rand, i int) any {
		s := text(r)
		mark := ""
		if g.Unique {
			mark = run + strconv.Itoa(i+1)
		}
		return fit(g.Kind, s, mark, g.MaxLength)
	}, nil
}

func textMaker(k Kind) func(r *rand.Rand) string {
	switch k {
	case Sentence:
		return sentence
	case Paragraph:
		return func(r *rand.Rand) string {
			parts := make([]string, 3+r.IntN(3))
			for i := range parts {
				parts[i] = sentence(r)
			}
			return strings.Join(parts, " ")
		}
	case FirstName:
		return func(r *rand.Rand) string { return pick(r, firstNames) }
	case LastName:
		return func(r *rand.Rand) string { return pick(r, lastNames) }
	case FullName:
		return func(r *rand.Rand) string { return pick(r, firstNames) + " " + pick(r, lastNames) }
	case Username:
		return func(r *rand.Rand) string {
			return strings.ToLower(pick(r, firstNames)) + "_" + pick(r, words) + strconv.Itoa(r.IntN(100))
		}
	case Email:
		return func(r *rand.Rand) string {
			return strings.ToLower(pick(r, firstNames)+"."+pick(r, lastNames)) + "@example.com"
		}
	case Phone:
		return func(r *rand.Rand) string { return fmt.Sprintf("+1 555-%03d-%04d", r.IntN(1000), r.IntN(10000)) }
	case City:
		return func(r *rand.Rand) string { return pick(r, cities) }
	case Country:
		return func(r *rand.Rand) string { return pick(r, countries) }
	case Company:
		return func(r *rand.Rand) string { return pick(r, companyWords) + " " + pick(r, companySuffixes) }
	case Street:
		return func(r *rand.Rand) string {
			return strconv.Itoa(1+r.IntN(400)) + " " + pick(r, lastNames) + " " + pick(r, streetSuffixes)
		}
	case URL:
		return func(r *rand.Rand) string {
			return "https://" + strings.ToLower(pick(r, companyWords)) + ".example.com/" + pick(r, words)
		}
	}
	return func(r *rand.Rand) string {
		parts := make([]string, 1+r.IntN(3))
		for i := range parts {
			parts[i] = pick(r, words)
		}
		return strings.Join(parts, " ")
	}
}

// fit cuts text to limit characters, keeping mark, which makes it
// unique: an e-mail carries it before its @, other text after a dash.
func fit(k Kind, s, mark string, limit int) string {
	tail := ""
	if k == Email {
		at := strings.LastIndexByte(s, '@')
		s, tail = s[:at], s[at:]
	}
	if mark != "" {
		mark = "-" + mark
		if k == Email {
			mark = "." + mark[1:]
		}
	}
	if limit > 0 {
		room := limit - len([]rune(mark)) - len([]rune(tail))
		if rs := []rune(s); len(rs) > room {
			s = strings.TrimSpace(string(rs[:room]))
		}
	}
	return s + mark + tail
}

func sentence(r *rand.Rand) string {
	n := 6 + r.IntN(7)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = pick(r, words)
	}
	s := strings.Join(parts, " ")
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

func pick(r *rand.Rand, list []string) string { return list[r.IntN(len(list))] }

func uuid(r *rand.Rand) string {
	var b [16]byte
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 9562 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func parseInt(s string, otherwise int64, what string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return otherwise, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is a whole number: %q is not", what, s)
	}
	return n, nil
}

func intRange(from, to string) (int64, int64, error) {
	a, err := parseInt(from, 1, "the lowest number")
	if err != nil {
		return 0, 0, err
	}
	b, err := parseInt(to, 1000, "the highest number")
	if err != nil {
		return 0, 0, err
	}
	if span := b - a; a > b || span < 0 || span == math.MaxInt64 {
		return 0, 0, errors.New("the lowest number is above the highest, or the range is too wide")
	}
	return a, b, nil
}

func floatRange(from, to string) (float64, float64, error) {
	parse := func(s string, otherwise float64, what string) (float64, error) {
		if s = strings.TrimSpace(s); s == "" {
			return otherwise, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return 0, fmt.Errorf("%s is a number: %q is not", what, s)
		}
		return f, nil
	}
	a, err := parse(from, 0, "the lowest number")
	if err != nil {
		return 0, 0, err
	}
	b, err := parse(to, 1000, "the highest number")
	if err != nil {
		return 0, 0, err
	}
	if a > b {
		return 0, 0, errors.New("the lowest number is above the highest")
	}
	return a, b, nil
}

var timeForms = []string{time.DateOnly, "2006-01-02 15:04", time.DateTime}

func timeRange(from, to string) (time.Time, time.Time, error) {
	parse := func(s, what string) (time.Time, error) {
		s = strings.TrimSpace(s)
		for _, form := range timeForms {
			if t, err := time.Parse(form, s); err == nil {
				return t, nil
			}
		}
		return time.Time{}, fmt.Errorf("%s is a date, as 2024-01-31, or a time, as 2024-01-31 09:30: %q is not", what, s)
	}
	a, err := parse(from, "the earliest")
	if err != nil {
		return a, a, err
	}
	b, err := parse(to, "the latest")
	if err != nil {
		return a, b, err
	}
	if a.After(b) {
		return a, b, errors.New("the earliest is after the latest")
	}
	return a, b, nil
}

// Column is what a suggestion of a column's generator goes by.
type Column struct {
	Name string
	// Type is the column's type; Canonical, its type as DuckDB names the
	// type that holds its values (db.CanonicalType).
	Type, Canonical string
	Nullable        bool
	AutoIncrement   bool
	// PartOfForeignKey is set for a column of a foreign key of several,
	// whose values together must be another table's.
	PartOfForeignKey bool
	// Unique is set for the primary key's one column, and a unique
	// index's.
	Unique bool
	Enum   []string // an enum's values
	// References is set for the one column of a foreign key.
	References bool
}

var (
	sizedText    = regexp.MustCompile(`(?i)char[a-z ]*\(\s*(\d+)\s*\)`)
	decimalSizes = regexp.MustCompile(`^DECIMAL\((\d+),(\d+)\)$`)
	// smallInteger matches the types of numbers up to 127: MySQL's and
	// DuckDB's TINYINT, ClickHouse's Int8 (int8 is PostgreSQL's bigint).
	smallInteger = regexp.MustCompile(`^(?:(?:Nullable|LowCardinality)\()*(?:(?i:u?tinyint)|U?Int8)\b`)
	// clickhouseDate is ClickHouse's Date, which holds no day before 1970.
	clickhouseDate = regexp.MustCompile(`^(?:Nullable\()?Date\)?$`)
	nameWords      = regexp.MustCompile(`[a-z0-9]+`)
)

// Suggest is the generator a column most likely wants, by its type and
// its name.
func Suggest(c Column, now time.Time) Generator {
	g := Generator{Kind: Words, Unique: c.Unique}
	if m := sizedText.FindStringSubmatch(c.Type); m != nil {
		g.MaxLength, _ = strconv.Atoi(m[1])
	}
	// The words of the name, split at underscores, dashes and capitals.
	var spaced strings.Builder
	for i, ch := range c.Name {
		if i > 0 && ch >= 'A' && ch <= 'Z' {
			spaced.WriteByte(' ')
		}
		spaced.WriteRune(ch)
	}
	parts := nameWords.FindAllString(strings.ToLower(spaced.String()), -1)
	has := func(ws ...string) bool {
		for _, p := range parts {
			for _, w := range ws {
				if p == w {
					return true
				}
			}
		}
		return false
	}
	inner := c.Type
	for m := wrapperType.FindStringSubmatch(inner); m != nil; m = wrapperType.FindStringSubmatch(inner) {
		inner = m[1]
	}
	typeName := strings.ToLower(strings.TrimSpace(typeArgs.ReplaceAllString(inner, "")))
	switch {
	case c.AutoIncrement:
		g.Kind = Default
		return g
	case c.PartOfForeignKey:
		// Values made one column at a time match no row it refers to.
		g.Kind = Default
		if c.Nullable {
			g.Kind = Null
		}
		return g
	case c.References:
		g.Kind = Reference
		return g
	case len(c.Enum) > 0:
		g.Kind, g.Values = OneOf, c.Enum
		return g
	}
	switch c.Canonical {
	case "BOOLEAN":
		g.Kind = Boolean
	case "BIGINT", "UBIGINT":
		g.Kind, g.From, g.To = Integer, "1", "1000"
		switch {
		case c.Unique:
			g.Kind, g.To = Sequence, ""
		case has("age"):
			g.From, g.To = "18", "90"
		case has("year"):
			g.From, g.To = strconv.Itoa(now.Year()-30), strconv.Itoa(now.Year())
		case has("qty", "quantity", "count", "stock", "rank", "position"):
			g.From, g.To = "0", "100"
		}
		switch {
		case g.Kind != Integer:
		case typeName == "year":
			// MySQL's YEAR holds 1901 to 2155.
			g.From, g.To = strconv.Itoa(now.Year()-30), strconv.Itoa(now.Year())
		case smallInteger.MatchString(c.Type):
			g.From, g.To = "0", "100"
		}
	case "FLOAT", "DOUBLE":
		g.Kind, g.From, g.To, g.Scale = Decimal, "0", "1000", 2
		switch {
		case has("lat", "latitude"):
			g.From, g.To, g.Scale = "-90", "90", 6
		case has("lng", "lon", "long", "longitude"):
			g.From, g.To, g.Scale = "-180", "180", 6
		}
	case "DATE":
		g.Kind, g.From, g.To = Date, now.AddDate(-3, 0, 0).Format(time.DateOnly), now.Format(time.DateOnly)
		if has("birth", "birthday", "dob", "born") {
			g.From, g.To = now.AddDate(-80, 0, 0).Format(time.DateOnly), now.AddDate(-18, 0, 0).Format(time.DateOnly)
			if clickhouseDate.MatchString(c.Type) && g.From < "1970-01-01" {
				g.From = "1970-01-01"
			}
		}
	case "TIMESTAMP", "TIMESTAMPTZ":
		g.Kind, g.From, g.To = Timestamp, now.AddDate(-1, 0, 0).Format("2006-01-02 15:04"), now.Format("2006-01-02 15:04")
	case "TIME":
		g.Kind = Time
	case "UUID":
		g.Kind = UUID
	case "JSON":
		g.Kind = JSON
	}
	if m := decimalSizes.FindStringSubmatch(c.Canonical); m != nil {
		precision, _ := strconv.Atoi(m[1])
		scale, _ := strconv.Atoi(m[2])
		top := 1000.0
		if digits := precision - scale; digits < 4 {
			top = math.Pow10(digits) - 1
		}
		g.Kind, g.From, g.To, g.Scale = Decimal, "0", strconv.FormatFloat(top, 'f', -1, 64), scale
		if c.Unique {
			g.Kind, g.From, g.To = Sequence, "1", ""
		}
	}
	if c.Canonical == "VARCHAR" && !textType(typeName) {
		// A type no generator writes, as an interval, an address or an
		// array, but a number without its sizes.
		switch typeName {
		case "numeric", "decimal", "number", "money":
			g.Kind, g.From, g.To, g.Scale = Decimal, "0", "1000", 2
			if c.Unique {
				g.Kind, g.From, g.To, g.Scale = Sequence, "1", "", 0
			}
		default:
			g.Kind = Default
			if c.Nullable {
				g.Kind = Null
			}
		}
		return g
	}
	if g.Kind != Words {
		return g
	}
	if c.Unique {
		// A key of text: a UUID where it fits, else words marked unique.
		if g.MaxLength == 0 || g.MaxLength >= 36 {
			g.Kind = UUID
		}
		return g
	}
	switch {
	case has("email", "mail"):
		g.Kind = Email
	case has("firstname", "forename", "givenname") || has("first", "given") && has("name"):
		g.Kind = FirstName
	case has("lastname", "surname", "familyname") || has("last", "family") && has("name"):
		g.Kind = LastName
	case has("username", "login", "handle", "nick", "nickname") || has("user") && has("name"):
		g.Kind = Username
	case has("phone", "mobile", "tel", "telephone", "fax"):
		g.Kind = Phone
	case has("city", "town"):
		g.Kind = City
	case has("country"):
		g.Kind = Country
	case has("company", "organization", "organisation", "employer", "vendor", "supplier"):
		g.Kind = Company
	case has("address", "street"):
		g.Kind = Street
	case has("url", "website", "homepage", "link", "site"):
		g.Kind = URL
	case has("fullname") || has("name") && has("full", "display", "contact", "customer", "author", "owner", "person", "employee"):
		g.Kind = FullName
	case has("description", "notes", "note", "comment", "comments", "body", "bio", "summary", "about", "content", "message", "text"):
		g.Kind = Paragraph
		if g.MaxLength > 0 && g.MaxLength < 200 {
			g.Kind = Sentence
		}
	}
	return g
}

var (
	typeArgs = regexp.MustCompile(`\([^)]*\)`)
	// wrapperType matches ClickHouse's wrappers of a column's type.
	wrapperType = regexp.MustCompile(`^(?:Nullable|LowCardinality)\((.*)\)$`)
)

// textType reports whether a type, without its sizes, holds text: any
// type of SQLite's text affinity, or none, as other engines' text types.
func textType(name string) bool {
	switch {
	case name == "" || name == "name":
		return true
	case strings.HasSuffix(name, "]"):
		return false // an array
	}
	for _, w := range []string{"char", "text", "clob", "string"} {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

package mongo

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"dgopher/internal/mongosh"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Values are shown and written as the console reads them (mongosh's
// syntax, which mongosh.Parse takes back), so that a document shown can
// be edited and sent again with its types: a long stays NumberLong, a
// whole double keeps its ".0".

// shellLine is v on one line.
func shellLine(v bson.RawValue) string {
	var b strings.Builder
	writeValue(&b, v, "", false)
	return b.String()
}

// shellDocument is a document on several lines, short objects and arrays
// inside it on one.
func shellDocument(d bson.Raw) string {
	var b strings.Builder
	writeValue(&b, bson.RawValue{Type: bson.TypeEmbeddedDocument, Value: d}, "", true)
	return b.String()
}

// shellOf is a Go value, as a statement's argument, on one line.
func shellOf(v any) string {
	t, data, err := bson.MarshalValue(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return shellLine(bson.RawValue{Type: t, Value: data})
}

// inlineWidth is how long an object or array inside a document may be to
// stay on one line.
const inlineWidth = 72

func writeValue(b *strings.Builder, v bson.RawValue, indent string, pretty bool) {
	switch v.Type {
	case bson.TypeEmbeddedDocument, bson.TypeArray:
		writeNested(b, v, indent, pretty)
	default:
		b.WriteString(scalarText(v))
	}
}

func writeNested(b *strings.Builder, v bson.RawValue, indent string, pretty bool) {
	open, close := "{", "}"
	var keys []string
	var vals []bson.RawValue
	if v.Type == bson.TypeArray {
		open, close = "[", "]"
		vals, _ = v.Array().Values()
	} else {
		els, _ := v.Document().Elements()
		for _, e := range els {
			keys = append(keys, e.Key())
			vals = append(vals, e.Value())
		}
	}
	if len(vals) == 0 {
		b.WriteString(open + close)
		return
	}
	entry := func(b *strings.Builder, i int, indent string, pretty bool) {
		if keys != nil {
			b.WriteString(shellKey(keys[i]) + ": ")
		}
		writeValue(b, vals[i], indent, pretty)
	}
	// One line first: kept when it is short, or when not pretty.
	var line strings.Builder
	line.WriteString(open + " ")
	for i := range vals {
		if i > 0 {
			line.WriteString(", ")
		}
		entry(&line, i, "", false)
	}
	line.WriteString(" " + close)
	if !pretty || indent != "" && len(line.String()) <= inlineWidth && !strings.Contains(line.String(), "\n") {
		b.WriteString(line.String())
		return
	}
	inner := indent + "  "
	b.WriteString(open + "\n")
	for i := range vals {
		b.WriteString(inner)
		entry(b, i, inner, true)
		if i < len(vals)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(indent + close)
}

var identifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// shellKey is a key unquoted when it is a name, as mongosh writes it.
func shellKey(k string) string {
	if identifier.MatchString(k) {
		return k
	}
	return quote(k)
}

// quote is a string as JSON writes it, without escaping HTML.
func quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

// scalarText is a value that is not an object or an array.
func scalarText(v bson.RawValue) string {
	switch v.Type {
	case bson.TypeString:
		return quote(v.StringValue())
	case bson.TypeDouble:
		return doubleText(v.Double())
	case bson.TypeInt32:
		return strconv.FormatInt(int64(v.Int32()), 10)
	case bson.TypeInt64:
		return "NumberLong(" + strconv.FormatInt(v.Int64(), 10) + ")"
	case bson.TypeDecimal128:
		return `NumberDecimal("` + v.Decimal128().String() + `")`
	case bson.TypeBoolean:
		return strconv.FormatBool(v.Boolean())
	case bson.TypeNull:
		return "null"
	case bson.TypeUndefined:
		return "undefined"
	case bson.TypeObjectID:
		return `ObjectId("` + v.ObjectID().Hex() + `")`
	case bson.TypeDateTime:
		return `ISODate("` + v.Time().UTC().Format("2006-01-02T15:04:05.000Z") + `")`
	case bson.TypeTimestamp:
		t, i := v.Timestamp()
		return fmt.Sprintf("Timestamp({ t: %d, i: %d })", t, i)
	case bson.TypeBinary:
		sub, data := v.Binary()
		if sub == 4 && len(data) == 16 {
			return `UUID("` + uuidText(data) + `")`
		}
		return fmt.Sprintf(`BinData(%d, "%s")`, sub, base64.StdEncoding.EncodeToString(data))
	case bson.TypeRegex:
		pattern, options := v.Regex()
		if !strings.ContainsAny(pattern, "/\n") {
			return "/" + pattern + "/" + options
		}
	case bson.TypeMinKey:
		return "MinKey()"
	case bson.TypeMaxKey:
		return "MaxKey()"
	}
	// The rest, as JavaScript code, in Extended JSON.
	b, err := bson.MarshalExtJSON(bson.D{{Key: "v", Value: v}}, true, false)
	if err != nil {
		return v.String()
	}
	return string(b[len(`{"v":`) : len(b)-1])
}

func doubleText(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eEn") {
		s += ".0" // read back as a double, not an integer
	}
	return s
}

func uuidText(b []byte) string {
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Cells and types, for the grid.

// missing is a field a document does not have, as its cell shows it.
const missing = ""

// cellValue is a field's value as the grid shows it: scalars as values
// the grid formats, an object as its keys, an array as its length.
func cellValue(v bson.RawValue) any {
	switch v.Type {
	case 0:
		return missing
	case bson.TypeEmbeddedDocument:
		els, _ := v.Document().Elements()
		keys := make([]string, 0, 4)
		for i, e := range els {
			if i == 3 {
				keys = append(keys, "…")
				break
			}
			keys = append(keys, e.Key())
		}
		if len(keys) == 0 {
			return "{ }"
		}
		return "{ " + strings.Join(keys, ", ") + " }"
	case bson.TypeArray:
		vals, _ := v.Array().Values()
		return fmt.Sprintf("[ %d ]", len(vals))
	case bson.TypeString:
		return v.StringValue()
	case bson.TypeDouble:
		return v.Double()
	case bson.TypeInt32:
		return int64(v.Int32())
	case bson.TypeInt64:
		return v.Int64()
	case bson.TypeDecimal128:
		return v.Decimal128().String()
	case bson.TypeBoolean:
		return v.Boolean()
	case bson.TypeNull, bson.TypeUndefined:
		return nil
	case bson.TypeObjectID:
		return v.ObjectID().Hex()
	case bson.TypeDateTime:
		return v.Time().UTC()
	}
	return scalarText(v)
}

// typeName is a value's type as the column headers and the tree name it.
func typeName(t bson.Type) string {
	switch t {
	case bson.TypeDouble:
		return "double"
	case bson.TypeString:
		return "string"
	case bson.TypeEmbeddedDocument:
		return "object"
	case bson.TypeArray:
		return "array"
	case bson.TypeBinary:
		return "binary"
	case bson.TypeObjectID:
		return "ObjectId"
	case bson.TypeBoolean:
		return "bool"
	case bson.TypeDateTime:
		return "date"
	case bson.TypeNull, bson.TypeUndefined:
		return "null"
	case bson.TypeRegex:
		return "regex"
	case bson.TypeInt32:
		return "int"
	case bson.TypeInt64:
		return "long"
	case bson.TypeDecimal128:
		return "decimal"
	case bson.TypeTimestamp:
		return "timestamp"
	}
	return t.String()
}

// column is a top-level field of the documents shown.
type column struct {
	name string
	// typ is the type of its values: "number" when they are numbers of
	// several types, as MongoDB's $type names them, "mixed" when they
	// differ otherwise; null values and documents without the field do
	// not count.
	typ string
}

// columnsOf are the top-level fields of docs, _id first and the others
// in the order they first appear.
func columnsOf(docs []bson.Raw) []column {
	var cols []column
	at := map[string]int{}
	add := func(name string, t bson.Type) {
		i, ok := at[name]
		if !ok {
			i = len(cols)
			at[name] = i
			cols = append(cols, column{name: name})
		}
		if t == 0 || t == bson.TypeNull || t == bson.TypeUndefined {
			return
		}
		switch tn := typeName(t); {
		case cols[i].typ == "" || cols[i].typ == tn:
			cols[i].typ = tn
		case numericTypes[tn] && (numericTypes[cols[i].typ] || cols[i].typ == "number"):
			cols[i].typ = "number"
		default:
			cols[i].typ = "mixed"
		}
	}
	add("_id", 0)
	for _, d := range docs {
		els, _ := d.Elements()
		for _, e := range els {
			add(e.Key(), e.Value().Type)
		}
	}
	if !hasField(docs, "_id") {
		cols = cols[1:]
	}
	for i := range cols {
		if cols[i].typ == "" {
			cols[i].typ = "null"
		}
	}
	return cols
}

var numericTypes = map[string]bool{"int": true, "long": true, "double": true, "decimal": true}

func hasField(docs []bson.Raw, name string) bool {
	for _, d := range docs {
		if _, err := d.LookupErr(name); err == nil {
			return true
		}
	}
	return false
}

// Values typed.

// parseValue reads one value written as the console writes it; "" is
// nil, no value.
func parseValue(text string) (mongosh.Value, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	// The value is read as the argument of a statement, on its own lines,
	// so that an error's column is the value's; its line is one less.
	op, err := mongosh.Parse("db.c.find(\n" + text + "\n)")
	if err != nil {
		return nil, valueError(err)
	}
	if op.Collection != "c" || op.Method != "find" || op.Database != "" || len(op.Args) != 1 || len(op.Chain) > 0 {
		return nil, errors.New("expected one value")
	}
	return op.Args[0], nil
}

var position = regexp.MustCompile(`^(\d+):(\d+): `)

// valueError is the parser's error about a value parseValue wrapped,
// its line as the value's own.
func valueError(err error) error {
	m := position.FindStringSubmatch(err.Error())
	if m == nil {
		return err
	}
	line, _ := strconv.Atoi(m[1])
	rest := err.Error()[len(m[0]):]
	if line <= 1 {
		return errors.New(rest)
	}
	return fmt.Errorf("%d:%s: %s", line-1, m[2], rest)
}

// parseDocument reads a document, what naming it in the error.
func parseDocument(text, what string) (bson.D, error) {
	v, err := parseValue(text)
	if err != nil || v == nil {
		return nil, err
	}
	d, ok := v.(bson.D)
	if !ok {
		return nil, fmt.Errorf("%s is not a document: write it in braces, as { name: 1 }", what)
	}
	return d, nil
}

// rawOf is a document as BSON.
func rawOf(d bson.D) (bson.Raw, error) {
	b, err := bson.Marshal(d)
	return bson.Raw(b), err
}

// elementsOf are a document's fields, keys and values, in order.
func elementsOf(d bson.Raw) bson.D {
	els, _ := d.Elements()
	out := make(bson.D, len(els))
	for i, e := range els {
		out[i] = bson.E{Key: e.Key(), Value: e.Value()}
	}
	return out
}

// fieldOf is a document's field.
func fieldOf(d bson.D, key string) (any, bool) {
	for _, e := range d {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

// sameValue reports whether two values are the same BSON.
func sameValue(a, b bson.RawValue) bool {
	return a.Type == b.Type && bytes.Equal(a.Value, b.Value)
}

// shortID is an _id as the grid and the titles show it.
func shortID(v bson.RawValue) string {
	if v.Type == bson.TypeObjectID {
		h := v.ObjectID().Hex()
		return h[:6] + "…" + h[len(h)-4:]
	}
	return shellLine(v)
}

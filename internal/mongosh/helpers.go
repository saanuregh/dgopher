package mongosh

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// helper reads a shell helper's call, as ObjectId("…") or new Date().
func (p *parser) helper(name token, isNew bool) (Value, error) {
	a, err := p.args()
	if err != nil {
		return nil, err
	}
	// at is where an error about the arguments points: the first one, or
	// the helper when it has none.
	at := name
	if len(a.tokens) > 0 {
		at = a.tokens[0]
	}
	fail := func(format string, args ...any) (Value, error) {
		return nil, p.errAt(at, format, args...)
	}
	switch name.text {
	case "ObjectId", "ObjectID":
		if len(a.values) == 0 {
			return bson.NewObjectID(), nil
		}
		if id, ok := objectID(a.values); ok {
			return id, nil
		}
		return fail("%s expects 24 hexadecimal digits", name.text)
	case "ISODate", "Date":
		if name.text == "Date" && !isNew {
			// As in JavaScript, Date() without new is the time as a string.
			return time.Now().Format("Mon Jan 02 2006 15:04:05 GMT-0700 (MST)"), nil
		}
		if len(a.values) == 0 {
			return bson.NewDateTimeFromTime(time.Now()), nil
		}
		if len(a.values) == 1 {
			if d, ok := dateOf(a.values[0]); ok {
				return d, nil
			}
		}
		return fail(`%s expects a date such as "2024-01-31T12:00:00Z"`, name.text)
	case "NumberInt", "Int32":
		if n, ok := intArg(a.values, math.MinInt32, math.MaxInt32); ok {
			return int32(n), nil
		}
		return fail("%s expects an integer of 32 bits", name.text)
	case "NumberLong", "Long":
		if n, ok := intArg(a.values, math.MinInt64, math.MaxInt64); ok {
			return n, nil
		}
		return fail("%s expects an integer of 64 bits", name.text)
	case "Double":
		if len(a.values) == 1 {
			if f, ok := floatOf(a.values[0]); ok {
				return f, nil
			}
		}
		return fail("Double expects a number")
	case "NumberDecimal", "Decimal128":
		if len(a.values) == 1 {
			if d, ok := decimalOf(a.values[0]); ok {
				return d, nil
			}
		}
		return fail("%s expects a decimal number", name.text)
	case "UUID":
		if len(a.values) == 0 {
			b := make([]byte, 16)
			_, _ = rand.Read(b)
			b[6] = b[6]&0x0f | 0x40
			b[8] = b[8]&0x3f | 0x80
			return bson.Binary{Subtype: 4, Data: b}, nil
		}
		if len(a.values) == 1 {
			if u, ok := uuidOf(a.values[0]); ok {
				return u, nil
			}
		}
		return fail("UUID expects 32 hexadecimal digits")
	case "BinData":
		if len(a.values) == 2 {
			sub, ok1 := intArg(a.values[:1], 0, 255)
			s, ok2 := a.values[1].(string)
			if data, err := base64.StdEncoding.DecodeString(s); ok1 && ok2 && err == nil {
				return bson.Binary{Subtype: byte(sub), Data: data}, nil
			}
		}
		return fail("BinData expects a subtype and a base64 string")
	case "Timestamp":
		switch len(a.values) {
		case 0:
			return bson.Timestamp{}, nil
		case 1:
			if d, ok := a.values[0].(bson.D); ok {
				if ts, ok := timestampOf(d); ok {
					return ts, nil
				}
			}
		case 2:
			t, ok1 := intArg(a.values[:1], 0, math.MaxUint32)
			i, ok2 := intArg(a.values[1:], 0, math.MaxUint32)
			if ok1 && ok2 {
				return bson.Timestamp{T: uint32(t), I: uint32(i)}, nil
			}
		}
		return fail("Timestamp expects seconds and an increment")
	}
	return nil, p.errAt(name, notJavaScript+"%s() is not a known shell helper", name.text)
}

func objectID(args []Value) (bson.ObjectID, bool) {
	if len(args) != 1 {
		return bson.ObjectID{}, false
	}
	s, ok := args[0].(string)
	if !ok {
		return bson.ObjectID{}, false
	}
	id, err := bson.ObjectIDFromHex(s)
	return id, err == nil
}

// dateLayouts are the forms ISODate reads; one without a zone is UTC.
var dateLayouts = []string{
	time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04Z07:00", "2006-01-02T15:04",
	"2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02",
	"2006-01-02T15:04:05.999999999Z0700",
}

func dateOf(v Value) (bson.DateTime, bool) {
	switch v := v.(type) {
	case string:
		for _, layout := range dateLayouts {
			if t, err := time.Parse(layout, v); err == nil {
				return bson.NewDateTimeFromTime(t), true
			}
		}
	case int32:
		return bson.DateTime(v), true
	case int64:
		return bson.DateTime(v), true
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) && v == math.Trunc(v) && math.Abs(v) < 1<<62 {
			return bson.DateTime(int64(v)), true
		}
	}
	return 0, false
}

// intArg is the one integer, or string of one, in args, within [lo, hi].
func intArg(args []Value, lo, hi int64) (int64, bool) {
	if len(args) != 1 {
		return 0, false
	}
	var n int64
	switch v := args[0].(type) {
	case int32:
		n = int64(v)
	case int64:
		n = v
	case float64:
		if v != math.Trunc(v) || v < math.MinInt64 || v >= math.MaxInt64 {
			return 0, false
		}
		n = int64(v)
	case string:
		var err error
		if n, err = strconv.ParseInt(strings.TrimSpace(v), 10, 64); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return n, n >= lo && n <= hi
}

func floatOf(v Value) (float64, bool) {
	switch v := v.(type) {
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil || isRangeError(err)
	}
	return 0, false
}

func decimalOf(v Value) (bson.Decimal128, bool) {
	var s string
	switch v := v.(type) {
	case string:
		s = v
	case int32:
		s = strconv.FormatInt(int64(v), 10)
	case int64:
		s = strconv.FormatInt(v, 10)
	case float64:
		s = strconv.FormatFloat(v, 'g', -1, 64)
	default:
		return bson.Decimal128{}, false
	}
	d, err := bson.ParseDecimal128(s)
	return d, err == nil
}

func uuidOf(v Value) (bson.Binary, bool) {
	s, ok := v.(string)
	if !ok {
		return bson.Binary{}, false
	}
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		return bson.Binary{}, false
	}
	return bson.Binary{Subtype: 4, Data: b}, true
}

func timestampOf(d bson.D) (bson.Timestamp, bool) {
	t, okT := field(d, "t")
	i, okI := field(d, "i")
	if len(d) != 2 || !okT || !okI {
		return bson.Timestamp{}, false
	}
	tn, ok1 := intArg([]Value{t}, 0, math.MaxUint32)
	in, ok2 := intArg([]Value{i}, 0, math.MaxUint32)
	return bson.Timestamp{T: uint32(tn), I: uint32(in)}, ok1 && ok2
}

func field(d bson.D, key string) (Value, bool) {
	for _, e := range d {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

// extendedJSON is the value of a document that is an Extended JSON
// wrapper, as {"$oid": "…"}, or the document itself. A query operator
// such as {$regex: …} is no wrapper.
func (p *parser) extendedJSON(open token, d bson.D) (Value, error) {
	if len(d) != 1 {
		return d, nil
	}
	key, v := d[0].Key, d[0].Value
	fail := func(expects string) (Value, error) {
		return nil, p.errAt(open, "%s expects %s", key, expects)
	}
	switch key {
	case "$oid":
		if id, ok := objectID([]Value{v}); ok {
			return id, nil
		}
		return fail("24 hexadecimal digits")
	case "$date":
		if inner, ok := v.(bson.D); ok && len(inner) == 1 && inner[0].Key == "$numberLong" {
			v = inner[0].Value
		}
		if s, ok := v.(string); ok {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return bson.DateTime(n), nil
			}
		}
		if dt, ok := dateOf(v); ok {
			return dt, nil
		}
		return fail("a date")
	case "$numberInt":
		if n, ok := intArg([]Value{v}, math.MinInt32, math.MaxInt32); ok {
			return int32(n), nil
		}
		return fail("an integer of 32 bits as a string")
	case "$numberLong":
		if n, ok := intArg([]Value{v}, math.MinInt64, math.MaxInt64); ok {
			return n, nil
		}
		return fail("an integer of 64 bits as a string")
	case "$numberDouble":
		if s, ok := v.(string); ok {
			switch s {
			case "Infinity":
				return math.Inf(1), nil
			case "-Infinity":
				return math.Inf(-1), nil
			case "NaN":
				return math.NaN(), nil
			}
		}
		if f, ok := floatOf(v); ok {
			return f, nil
		}
		return fail("a number as a string")
	case "$numberDecimal":
		if dec, ok := decimalOf(v); ok {
			return dec, nil
		}
		return fail("a decimal number as a string")
	case "$binary":
		if inner, ok := v.(bson.D); ok {
			b64, ok1 := field(inner, "base64")
			sub, ok2 := field(inner, "subType")
			s, ok3 := b64.(string)
			subHex, ok4 := sub.(string)
			data, err := base64.StdEncoding.DecodeString(s)
			n, err2 := strconv.ParseUint(subHex, 16, 8)
			if len(inner) == 2 && ok1 && ok2 && ok3 && ok4 && err == nil && err2 == nil {
				return bson.Binary{Subtype: byte(n), Data: data}, nil
			}
		}
		return fail(`{"base64": "…", "subType": "…"}`)
	case "$uuid":
		if u, ok := uuidOf(v); ok {
			return u, nil
		}
		return fail("32 hexadecimal digits")
	case "$timestamp":
		if inner, ok := v.(bson.D); ok {
			if ts, ok := timestampOf(inner); ok {
				return ts, nil
			}
		}
		return fail(`{"t": seconds, "i": increment}`)
	case "$regularExpression":
		if inner, ok := v.(bson.D); ok {
			pat, ok1 := field(inner, "pattern")
			opts, ok2 := field(inner, "options")
			ps, ok3 := pat.(string)
			os, ok4 := opts.(string)
			if len(inner) == 2 && ok1 && ok2 && ok3 && ok4 {
				return bson.Regex{Pattern: ps, Options: sortedFlags(os)}, nil
			}
		}
		return fail(`{"pattern": "…", "options": "…"}`)
	case "$minKey":
		return bson.MinKey{}, nil
	case "$maxKey":
		return bson.MaxKey{}, nil
	}
	return d, nil
}

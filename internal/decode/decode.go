// Package decode turns values stored compressed or serialized, as caches
// keep them, into text a person reads: gzip, zlib, zstd, LZ4, Snappy and
// Brotli; MessagePack, Python's pickle, PHP's serialize, Java's
// serialization and Protocol Buffers without their schema.
package decode

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Format is a way a value is encoded.
type Format string

const (
	Gzip        Format = "gzip"
	Zlib        Format = "zlib"
	Zstd        Format = "zstd"
	LZ4         Format = "LZ4"
	Snappy      Format = "Snappy"
	Brotli      Format = "Brotli"
	MessagePack Format = "MessagePack"
	Pickle      Format = "Pickle"
	PHP         Format = "PHP serialize"
	Java        Format = "Java serialization"
	Protobuf    Format = "Protobuf"
)

// Formats are the formats, in the order a choice of them lists them.
var Formats = []Format{Gzip, Zlib, Zstd, LZ4, Snappy, Brotli, MessagePack, Pickle, PHP, Java, Protobuf}

// maxSteps is how many encodings Auto undoes, one inside the other, as
// MessagePack compressed with gzip.
const maxSteps = 3

// MaxOutput is how large a value decompresses to at most: a small value
// may expand to gigabytes.
const MaxOutput = 16 << 20

// MaxText is how much text a decoded value shows at most, the rest cut:
// a small value nested deep shows as gigabytes of indented JSON.
const MaxText = 4 << 20

// preallocate is how many items a decoder makes room for before reading
// them, whatever count a value claims.
const preallocate = 1024

// compressed reports whether a format compresses bytes rather than
// serializing values.
func (f Format) compressed() bool {
	switch f {
	case Gzip, Zlib, Zstd, LZ4, Snappy, Brotli:
		return true
	}
	return false
}

// Detect finds the format a value's first bytes say it is in, "" for
// none: the compressions and serializations that start with a mark of
// their own. MessagePack, Protobuf and raw Brotli have none, and are
// chosen by hand.
func Detect(b []byte) Format {
	switch {
	case bytes.HasPrefix(b, []byte{0x1f, 0x8b}):
		return Gzip
	case len(b) >= 2 && b[0] == 0x78 && (uint16(b[0])<<8|uint16(b[1]))%31 == 0 && b[1]&0x20 == 0 && opensAsZlib(b):
		// Text may start so too, as "x = 5": only a stream that opens is.
		return Zlib
	case bytes.HasPrefix(b, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return Zstd
	case bytes.HasPrefix(b, []byte{0x04, 0x22, 0x4d, 0x18}):
		return LZ4
	case bytes.HasPrefix(b, []byte("\xff\x06\x00\x00sNaPpY")):
		return Snappy
	case bytes.HasPrefix(b, []byte{0xac, 0xed, 0x00, 0x05}):
		return Java
	case len(b) >= 2 && b[0] == 0x80 && b[1] >= 2 && b[1] <= 5 && b[len(b)-1] == '.':
		return Pickle
	case phpStart.Match(b):
		return PHP
	}
	return ""
}

// Decoded is a value as decoding showed it.
type Decoded struct {
	// Steps are the formats undone, the outermost first.
	Steps []Format
	// Text shows the value: JSON for a serialized value, the text
	// decompressed, or a hex dump of bytes that are not text.
	Text string
}

// opensAsZlib reports whether bytes start a zlib stream whose first
// bytes decompress.
func opensAsZlib(b []byte) bool {
	z, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return false
	}
	defer z.Close()
	_, err = z.Read(make([]byte, 1))
	return err == nil || err == io.EOF
}

// recovered turns a decoder's panic into an error: a value no test
// foresaw ends its decoding, not the app.
func recovered(err *error) {
	if p := recover(); p != nil {
		*err = fmt.Errorf("the value could not be decoded: %v", p)
	}
}

// Auto undoes the encodings a value's marks tell, one inside the other;
// none leaves it as it was.
func Auto(b []byte) (d Decoded, err error) {
	defer recovered(&err)
	for range maxSteps {
		f := Detect(b)
		if f == "" {
			break
		}
		out, err := decodeOne(f, b)
		if err != nil {
			return d, fmt.Errorf("%s: %w", f, err)
		}
		d.Steps = append(d.Steps, f)
		if !f.compressed() {
			d.Text = out.(string)
			return d, nil
		}
		b = out.([]byte)
	}
	d.Text = Text(b)
	return d, nil
}

// As decodes a value in the format chosen: a compression, then what its
// marks tell inside it; a serialization, which has no mark, inside the
// compressions its marks tell.
func As(f Format, b []byte) (d Decoded, err error) {
	defer recovered(&err)
	if !f.compressed() {
		for range maxSteps {
			c := Detect(b)
			if !c.compressed() {
				break
			}
			out, err := decompress(c, b)
			if err != nil {
				return d, fmt.Errorf("%s: %w", c, err)
			}
			d.Steps, b = append(d.Steps, c), out
		}
	}
	out, err := decodeOne(f, b)
	if err != nil {
		return d, err
	}
	d.Steps = append(d.Steps, f)
	if !f.compressed() {
		d.Text = out.(string)
		return d, nil
	}
	inner, err := Auto(out.([]byte))
	inner.Steps = append(d.Steps, inner.Steps...)
	return inner, err
}

// decodeOne undoes one encoding: bytes for a compression, text for a
// serialization.
func decodeOne(f Format, b []byte) (any, error) {
	switch f {
	case Gzip, Zlib, Zstd, LZ4, Snappy, Brotli:
		return decompress(f, b)
	case MessagePack:
		v, err := decodeMsgpack(b)
		if err != nil {
			return nil, err
		}
		return toJSON(v)
	case Pickle:
		v, err := decodePickle(b)
		if err != nil {
			return nil, err
		}
		return toJSON(v)
	case PHP:
		v, err := decodePHP(b)
		if err != nil {
			return nil, err
		}
		return toJSON(v)
	case Java:
		v, err := decodeJava(b)
		if err != nil {
			return nil, err
		}
		return toJSON(v)
	case Protobuf:
		return decodeProtobuf(b)
	}
	return nil, fmt.Errorf("no decoder for %q", f)
}

// errTruncated is the error of a value that ends before its encoding does.
var errTruncated = errors.New("the value ends early")

// maxValues is how many values a decoded value shows at most, a value
// shared by others counted each time it shows: a small pickle or stream
// sharing its values can show more than any memory holds.
const maxValues = 1_000_000

var errTooLarge = fmt.Errorf("it shows more than %d values", maxValues)

// walk counts the values shown, and those around the one being shown,
// which a value inside itself would show again forever.
type walk struct {
	left   int
	onPath map[any]bool
	// names makes the keys of the maps shown, when they are made.
	names *names
}

func newWalk() *walk { return &walk{left: maxValues, onPath: map[any]bool{}} }

// enter counts a value shown, and reports whether it is around itself.
func (w *walk) enter(v any) (recursive bool, err error) {
	if w.left--; w.left < 0 {
		return false, errTooLarge
	}
	return w.onPath[v], nil
}

// bounded checks a decoded value shows within maxValues, a value inside
// itself shown once, then marked.
func bounded(v any, w *walk) (any, error) {
	switch x := v.(type) {
	case *orderedMap:
		if recursive, err := w.enter(x); err != nil || recursive {
			return "<recursion>", err
		}
		w.onPath[x] = true
		defer delete(w.onPath, x)
		out := &orderedMap{keys: x.keys, values: make([]any, len(x.values))}
		for i, item := range x.values {
			var err error
			if out.values[i], err = bounded(item, w); err != nil {
				return nil, err
			}
		}
		return out, nil
	case []any:
		if _, err := w.enter(nil); err != nil {
			return nil, err
		}
		out := make([]any, len(x))
		for i, item := range x {
			var err error
			if out[i], err = bounded(item, w); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	if _, err := w.enter(nil); err != nil {
		return nil, err
	}
	return v, nil
}

// toJSON writes a decoded value as indented JSON, its keys in the order
// decoding found them, cut past MaxText.
func toJSON(v any) (string, error) {
	v, err := bounded(v, newWalk())
	if err != nil {
		return "", err
	}
	w := &jsonWriter{indent: []byte("\n")}
	w.encoder = json.NewEncoder(&w.scalarText)
	w.encoder.SetEscapeHTML(false)
	w.value(v, 0)
	w.write([]byte("\n"))
	if w.err != nil && w.err != errTextFull {
		return "", w.err
	}
	return w.out.text(), nil
}

// jsonWriter writes a value as json.Encoder indented by two spaces does,
// a piece at a time, and stops at MaxText: a value shared many times
// holds as much memory as the text shown, not as all of its JSON.
type jsonWriter struct {
	out        textWriter
	encoder    *json.Encoder
	scalarText bytes.Buffer
	// indent is a newline and the spaces of the deepest line so far.
	indent []byte
	// err stops the writing: errTextFull, or a value JSON cannot hold.
	err error
}

// value writes v nested depth deep.
func (w *jsonWriter) value(v any, depth int) {
	switch x := v.(type) {
	case *orderedMap:
		if len(x.keys) == 0 {
			w.write([]byte("{}"))
			return
		}
		w.write([]byte("{"))
		for i, k := range x.keys {
			if i > 0 {
				w.write([]byte(","))
			}
			w.newline(depth + 1)
			w.scalar(k)
			w.write([]byte(": "))
			w.value(x.values[i], depth+1)
			if w.err != nil {
				return
			}
		}
		w.newline(depth)
		w.write([]byte("}"))
	case []any:
		if len(x) == 0 {
			w.write([]byte("[]"))
			return
		}
		w.write([]byte("["))
		for i, item := range x {
			if i > 0 {
				w.write([]byte(","))
			}
			w.newline(depth + 1)
			w.value(item, depth+1)
			if w.err != nil {
				return
			}
		}
		w.newline(depth)
		w.write([]byte("]"))
	default:
		w.scalar(v)
	}
}

// scalar writes a value holding no others as json.Encoder does, with <, >
// and & as they are.
func (w *jsonWriter) scalar(v any) {
	if w.err != nil {
		return
	}
	w.scalarText.Reset()
	if w.err = w.encoder.Encode(v); w.err == nil {
		w.write(bytes.TrimRight(w.scalarText.Bytes(), "\n"))
	}
}

// newline starts a line depth deep.
func (w *jsonWriter) newline(depth int) {
	for len(w.indent) < 1+2*depth {
		w.indent = append(w.indent, ' ')
	}
	w.write(w.indent[:1+2*depth])
}

func (w *jsonWriter) write(p []byte) {
	if w.err == nil {
		_, w.err = w.out.Write(p)
	}
}

// errTextFull stops writing a text past MaxText.
var errTextFull = errors.New("the text is longer than it shows")

// textWriter keeps what is written up to MaxText.
type textWriter struct {
	b    strings.Builder
	full bool
}

func (w *textWriter) Write(p []byte) (int, error) {
	if room := MaxText - w.b.Len(); len(p) > room {
		w.b.Write(p[:max(room, 0)])
		w.full = true
		return 0, errTextFull
	}
	return w.b.Write(p)
}

func (w *textWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

// text is what was written, with a note where it was cut.
func (w *textWriter) text() string {
	s := strings.TrimRight(w.b.String(), "\n")
	if w.full {
		s = strings.ToValidUTF8(s, "") + fmt.Sprintf("\n… cut at %d MB", MaxText>>20)
	}
	return s
}

// Text shows bytes as text when they are, else as a hex dump.
func Text(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return hex.Dump(b)
}

// orderedMap is a map that keeps its keys' order as JSON, as decoders
// read them.
type orderedMap struct {
	keys   []string
	values []any
}

func (m *orderedMap) set(k string, v any) {
	m.keys = append(m.keys, k)
	m.values = append(m.values, v)
}

// bytesValue is bytes a decoder read, as JSON a string: their text, or 0x and
// their hex digits when they are not text.
type bytesValue []byte

func (b bytesValue) String() string {
	if utf8.Valid(b) {
		return string(b)
	}
	return "0x" + hex.EncodeToString(b)
}

func (b bytesValue) MarshalJSON() ([]byte, error) { return marshal(b.String()) }

// marshal writes a value as JSON as it is shown: <, > and & as they are,
// where json.Marshal would escape them for HTML.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// maxName is how long a key or a name made of a decoded value is at most,
// the rest cut: a tuple holding one twice, 40 deep, has 2^40 items.
const maxName = 4 << 10

// maxNames is how much text the keys and names made while decoding one
// value take together at most, each past it "…": a value shared by many
// is made into a name each time it is used.
const maxNames = 16 << 20

// names makes keys and names of decoded values as fmt.Sprint writes them,
// within maxName each and maxNames together.
type names struct {
	used int
	// buffer is written each name, then copied.
	buffer []byte
}

// keyString is a decoded map key as a JSON object's key.
func (n *names) keyString(k any) string {
	if s, ok := k.(string); ok {
		return s
	}
	return n.name(k)
}

// name writes parts one after the other, each as fmt.Sprint writes it
// alone, cut past maxName with "…".
func (n *names) name(parts ...any) string {
	room := min(maxName, maxNames-n.used)
	if room <= 0 {
		return "…"
	}
	p := namePrinter{b: n.buffer[:0], room: room}
	for _, part := range parts {
		p.arg(part)
	}
	s := p.text()
	n.buffer = p.b
	n.used += len(s)
	return s
}

// namePrinter writes values as fmt does with %v, up to a byte past room:
// fmt makes all of a value's text before writing any.
type namePrinter struct {
	b    []byte
	room int
}

func (p *namePrinter) full() bool { return len(p.b) > p.room }

func (p *namePrinter) write(s string) {
	if n := p.room + 1 - len(p.b); n > 0 {
		p.b = append(p.b, s[:min(len(s), n)]...)
	}
}

// text is what was written, cut past room between characters, with "…".
func (p *namePrinter) text() string {
	if p.full() {
		i := p.room
		for k := 1; k < utf8.UTFMax && i > 0 && !utf8.RuneStart(p.b[i]); k++ {
			i--
		}
		p.b = append(p.b[:i], "…"...)
	}
	return string(p.b)
}

// arg writes a value as fmt.Sprint writes it alone.
func (p *namePrinter) arg(v any) {
	if p.full() {
		return
	}
	if v == nil {
		p.write("<nil>")
	} else if !p.method(v) {
		p.value(reflect.ValueOf(v), 0)
	}
}

// method writes a value with its String or Error method, or its Format, as
// fmt does, and reports whether it has one.
func (p *namePrinter) method(v any) bool {
	switch x := v.(type) {
	case bytesValue:
		// As String does, making no more of a long value than shows.
		n := max(p.room+1-len(p.b), 0)
		if utf8.Valid(x) {
			p.write(string(x[:min(len(x), n)]))
		} else {
			p.write("0x")
			p.write(hex.EncodeToString(x[:min(len(x), n/2+1)]))
		}
	case fmt.Formatter, error, fmt.Stringer:
		p.write(fmt.Sprint(v))
	default:
		return false
	}
	return true
}

// value writes v nested depth deep as fmt's printValue does with %v: a
// pointer below the top as its address, and a value reached through an
// unexported field without its methods.
func (p *namePrinter) value(v reflect.Value, depth int) {
	if p.full() {
		return
	}
	if depth > 0 && v.IsValid() && v.CanInterface() && p.method(v.Interface()) {
		return
	}
	switch v.Kind() {
	case reflect.Invalid:
		p.write("<nil>")
	case reflect.String:
		p.write(v.String())
	case reflect.Bool:
		p.write(strconv.FormatBool(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		p.write(strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		p.write(strconv.FormatUint(v.Uint(), 10))
	case reflect.Struct:
		p.write("{")
		for i := range v.NumField() {
			if i > 0 {
				p.write(" ")
			}
			f := v.Field(i)
			if f.Kind() == reflect.Interface && !f.IsNil() {
				f = f.Elem()
			}
			p.value(f, depth+1)
		}
		p.write("}")
	case reflect.Interface:
		if e := v.Elem(); e.IsValid() {
			p.value(e, depth+1)
		} else {
			p.write("<nil>")
		}
	case reflect.Array, reflect.Slice:
		p.write("[")
		for i := 0; i < v.Len() && !p.full(); i++ {
			if i > 0 {
				p.write(" ")
			}
			p.value(v.Index(i), depth+1)
		}
		p.write("]")
	case reflect.Pointer:
		if depth == 0 && !v.IsNil() {
			switch v.Elem().Kind() {
			case reflect.Array, reflect.Slice, reflect.Struct, reflect.Map:
				p.write("&")
				p.value(v.Elem(), depth+1)
				return
			}
		}
		if v.IsNil() {
			p.write("<nil>")
		} else {
			p.write("0x" + strconv.FormatUint(uint64(v.Pointer()), 16))
		}
	default:
		// A number, or a kind no decoder makes: fmt writes it so at any
		// depth.
		p.write(fmt.Sprint(v))
	}
}

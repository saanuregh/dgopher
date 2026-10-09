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
	w := &textWriter{}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil && err != errTextFull {
		return "", err
	}
	return w.text(), nil
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

func (m *orderedMap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		val, err := marshal(m.values[i])
		if err != nil {
			return nil, err
		}
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
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

// keyString is a decoded map key as a JSON object's key.
func keyString(k any) string {
	if s, ok := k.(string); ok {
		return s
	}
	return fmt.Sprint(k)
}

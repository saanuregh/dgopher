package decode

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func compact(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return b.String()
}

// The same dict pickled by Python with protocols 0, 2, 4 and 5:
// {"a": 1, "b": [True, None, 2.5], "c": (1, "x"), "d": b"\x00\xff", "big": 2**70, "s": {3}}
var pickles = map[string]string{
	"0": "286470300a56610a70310a49310a7356620a70320a286c70330a4930310a614e6146322e350a617356630a70340a2849310a56780a70350a7470360a7356640a70370a635f636f646563730a656e636f64650a70380a28565c7530303030ff0a70390a566c6174696e310a7031300a747031310a527031320a73566269670a7031330a4c313138303539313632303731373431313330333432344c0a7356730a7031340a635f5f6275696c74696e5f5f0a7365740a7031350a28286c7031360a49330a61747031370a527031380a732e",
	"2": "80027d71002858010000006171014b0158010000006271025d710328884e4740040000000000006558010000006371044b0158010000007871058671065801000000647107635f636f646563730a656e636f64650a7108580300000000c3bf710958060000006c6174696e31710a86710b52710c5803000000626967710d8a09000000000000000040580100000073710e635f5f6275696c74696e5f5f0a7365740a710f5d71104b0361857111527112752e",
	"4": "8004954e000000000000007d94288c0161944b018c0162945d9428884e474004000000000000658c0163944b018c01789486948c016494430200ff948c03626967948a090000000000000000408c0173948f94284b0390752e",
	"5": "8005954e000000000000007d94288c0161944b018c0162945d9428884e474004000000000000658c0163944b018c01789486948c016494430200ff948c03626967948a090000000000000000408c0173948f94284b0390752e",
}

func TestPickle(t *testing.T) {
	want := `{"a":1,"b":[true,null,2.5],"c":[1,"x"],"d":"0x00ff","big":"1180591620717411303424","s":[3]}`
	for proto, h := range pickles {
		b := unhex(t, h)
		if proto != "0" && Detect(b) != Pickle {
			t.Errorf("protocol %s not detected", proto)
		}
		d, err := As(Pickle, b)
		if err != nil {
			t.Fatalf("protocol %s: %v", proto, err)
		}
		if got := compact(t, d.Text); got != want {
			t.Errorf("protocol %s:\n%s\nwant\n%s", proto, got, want)
		}
	}
	// l = [1]; l.append(l)
	d, err := As(Pickle, unhex(t, "80049509000000000000005d94284b016800652e"))
	if err != nil || compact(t, d.Text) != `[1,"<recursion>"]` {
		t.Fatalf("a list inside itself: %v %s", err, d.Text)
	}
	// OrderedDict([("k", 1)]), protocol 2
	d, err = As(Pickle, unhex(t, "800263636f6c6c656374696f6e730a4f726465726564446963740a71002952710158010000006b71024b01732e"))
	if err != nil || compact(t, d.Text) != `{"k":1}` {
		t.Fatalf("an OrderedDict: %v %s", err, d.Text)
	}
}

func TestMessagePack(t *testing.T) {
	// {"a": 1, "b": [true, nil], "c": -3, "f": 1.5, "bin": 0x00ff, "t": timestamp 1}
	b := unhex(t, "86 a161 01 a162 92 c3 c0 a163 fd a166 cb3ff8000000000000 a3 62696e c4 02 00ff a174 d6 ff 00000001")
	d, err := As(MessagePack, b)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := compact(t, d.Text), `{"a":1,"b":[true,null],"c":-3,"f":1.5,"bin":"0x00ff","t":"1970-01-01T00:00:01Z"}`; got != want {
		t.Fatalf("%s\nwant\n%s", got, want)
	}
	for _, bad := range []string{"92 01", "dc ffff", "c1", "01 02"} {
		if _, err := As(MessagePack, unhex(t, bad)); err == nil {
			t.Errorf("%s read", bad)
		}
	}
}

func TestPHP(t *testing.T) {
	for in, want := range map[string]string{
		`a:2:{i:0;s:1:"x";i:1;d:1.5;}`: `["x",1.5]`,
		`a:2:{s:1:"k";b:1;i:7;N;}`:     `{"k":true,"7":null}`,
		`O:8:"stdClass":2:{s:4:"name";s:3:"Ada";s:6:"` + "\x00*\x00" + `age";i:36;}`: `{"__class":"stdClass","name":"Ada","age":36}`,
		`s:6:"héllo";`: `"héllo"`,
	} {
		if Detect([]byte(in)) != PHP {
			t.Errorf("%q not detected", in)
		}
		d, err := As(PHP, []byte(in))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got := compact(t, d.Text); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
	if _, err := As(PHP, []byte(`s:9:"short";`)); err == nil {
		t.Error("a string shorter than its length read")
	}
}

func TestJava(t *testing.T) {
	// A Point with int x = 1 and int y = 2, then the string "hi" and a
	// reference to the Point again.
	b := unhex(t, "aced0005"+
		"73 72 0005 506f696e74 0000000000000001 02 0002 49 0001 78 49 0001 79 78 70 00000001 00000002"+
		"74 0002 6869"+
		"71 007e0001")
	if Detect(b) != Java {
		t.Fatal("not detected")
	}
	d, err := As(Java, b)
	if err != nil {
		t.Fatal(err)
	}
	point := `{"__class":"Point","x":1,"y":2}`
	if got := compact(t, d.Text); got != "["+point+`,"hi",`+point+"]" {
		t.Fatalf("%s", got)
	}
}

func TestProtobuf(t *testing.T) {
	// 1: 150, 2: "hi", 3 { 1: 1 }, 4: fixed32 7
	d, err := As(Protobuf, unhex(t, "089601 12026869 1a020801 2507000000"))
	if err != nil {
		t.Fatal(err)
	}
	want := "1: 150\n2: \"hi\"\n3 {\n  1: 1\n}\n4: 0x00000007"
	if d.Text != want {
		t.Fatalf("%q\nwant\n%q", d.Text, want)
	}
	if _, err := As(Protobuf, []byte("plain text")); err == nil {
		t.Fatal("text read as a message")
	}
}

func TestCompressions(t *testing.T) {
	text := []byte(strings.Repeat("compressed text ", 50))
	var gz, zl, lz, br bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(text)
	w.Close()
	zw := zlib.NewWriter(&zl)
	zw.Write(text)
	zw.Close()
	lw := lz4.NewWriter(&lz)
	lw.Write(text)
	lw.Close()
	bw := brotli.NewWriter(&br)
	bw.Write(text)
	bw.Close()
	enc, _ := zstd.NewWriter(nil)
	zs := enc.EncodeAll(text, nil)
	var framed bytes.Buffer
	sw := s2.NewWriter(&framed, s2.WriterSnappyCompat())
	sw.Write(text)
	sw.Close()
	for f, b := range map[Format][]byte{Gzip: gz.Bytes(), Zlib: zl.Bytes(), LZ4: lz.Bytes(), Zstd: zs, Snappy: framed.Bytes()} {
		d, err := Auto(b)
		if err != nil || len(d.Steps) != 1 || d.Steps[0] != f || d.Text != string(text) {
			t.Errorf("%s: %v %v %q", f, err, d.Steps, d.Text[:min(len(d.Text), 20)])
		}
	}
	for f, b := range map[Format][]byte{Brotli: br.Bytes(), Snappy: s2.EncodeSnappy(nil, text)} {
		if d, err := As(f, b); err != nil || d.Text != string(text) {
			t.Errorf("%s chosen: %v", f, err)
		}
	}
	// MessagePack inside gzip: the gzip found, the MessagePack chosen.
	var packed bytes.Buffer
	w = gzip.NewWriter(&packed)
	w.Write(unhex(t, "81 a161 01"))
	w.Close()
	if d, err := As(MessagePack, packed.Bytes()); err != nil || compact(t, d.Text) != `{"a":1}` || len(d.Steps) != 2 {
		t.Fatalf("MessagePack in gzip: %v %v %s", err, d.Steps, d.Text)
	}
	// A bomb stops at the limit.
	var bomb bytes.Buffer
	w = gzip.NewWriter(&bomb)
	w.Write(make([]byte, MaxOutput+10))
	w.Close()
	if _, err := Auto(bomb.Bytes()); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("a bomb: %v", err)
	}
}

// Values shared stop at maxValues, and a value inside itself is shown
// once, however small the pickle or the stream.
func TestSharedValues(t *testing.T) {
	// Each list holds the one before it twice, 40 deep: 2^40 values.
	p := []byte("\x80\x02]q\x00")
	for i := 1; i <= 40; i++ {
		p = append(p, "](h"...)
		p = append(p, byte(i-1), 'h', byte(i-1), 'e', 'q', byte(i))
	}
	p = append(p, '.')
	if _, err := As(Pickle, p); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("a pickle of 2^40 values: %v", err)
	}
	// A Node whose field self is the Node.
	b := unhex(t, "aced0005 73 72 0004 4e6f6465 0000000000000001 02 0001 4c 0004 73656c66 74 0006 4c4e6f64653b 78 70 71 007e0002")
	d, err := As(Java, b)
	if err != nil || compact(t, d.Text) != `{"__class":"Node","self":"<recursion>"}` {
		t.Fatalf("an object holding itself: %v %s", err, d.Text)
	}
}

// wholeMap is an orderedMap marshaled as toJSON did before it streamed.
type wholeMap orderedMap

func (m *wholeMap) MarshalJSON() ([]byte, error) {
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

// whole gives a bounded value's maps the MarshalJSON they had.
func whole(v any) any {
	switch x := v.(type) {
	case *orderedMap:
		m := &wholeMap{keys: x.keys, values: make([]any, len(x.values))}
		for i, item := range x.values {
			m.values[i] = whole(item)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = whole(item)
		}
		return out
	}
	return v
}

// encodedWhole is toJSON as it was: the value encoded whole by
// json.Encoder, then cut.
func encodedWhole(v any) (string, error) {
	v, err := bounded(v, newWalk())
	if err != nil {
		return "", err
	}
	w := &textWriter{}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(whole(v)); err != nil && err != errTextFull {
		return "", err
	}
	return w.text(), nil
}

// The JSON written a piece at a time is the JSON json.Encoder wrote of
// the value whole, byte for byte, cut the same past MaxText.
func TestStreamedJSONMatches(t *testing.T) {
	var values []any
	decoded := func(decode func([]byte) (any, error), b []byte) {
		t.Helper()
		v, err := decode(b)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, v)
	}
	for _, h := range pickles {
		decoded(decodePickle, unhex(t, h))
	}
	decoded(decodePickle, unhex(t, "80049509000000000000005d94284b016800652e"))
	decoded(decodePickle, unhex(t, "800263636f6c6c656374696f6e730a4f726465726564446963740a71002952710158010000006b71024b01732e"))
	decoded(decodePickle, []byte("\x80\x02((\x86."))
	decoded(decodeJava, unhex(t, "aced0005 73 72 0005 506f696e74 0000000000000001 02 0002 49 0001 78 49 0001 79 78 70 00000001 00000002 74 0002 6869 71 007e0001"))
	decoded(decodeJava, unhex(t, "aced0005 73 72 0004 4e6f6465 0000000000000001 02 0001 4c 0004 73656c66 74 0006 4c4e6f64653b 78 70 71 007e0002"))
	decoded(decodeJava, unhex(t, "aced0005"))
	for _, s := range []string{
		`a:2:{i:0;s:1:"x";i:1;d:1.5;}`, `a:2:{s:1:"k";b:1;i:7;N;}`, `s:6:"héllo";`,
		`O:8:"stdClass":2:{s:4:"name";s:3:"Ada";s:6:"` + "\x00*\x00" + `age";i:36;}`,
		`a:3:{i:0;a:0:{}i:1;a:1:{s:1:"k";a:0:{}}i:2;a:1:{i:0;a:1:{i:0;a:2:{i:0;N;i:1;s:2:"<>";}}}}`,
		"a:2:{s:1:\"\xff\";s:2:\"\xfe\x80\";s:3:\"\xe2\x80\xa8\";s:1:\"\x7f\";}",
	} {
		decoded(decodePHP, []byte(s))
	}
	decoded(decodeMsgpack, unhex(t, "86 a161 01 a162 92 c3 c0 a163 fd a166 cb3ff8000000000000 a3 62696e c4 02 00ff a174 d6 ff 00000001"))
	decoded(decodeMsgpack, unhex(t, "83 a1ff a2fe80 a3e280a9 90 a0 80"))
	decoded(decodeMsgpack, unhex(t, "82 a161 01 a161 92 cb7ff8000000000000 01"))
	decoded(decodeMsgpack, unhex(t, "82 a161 01 a161 02"))

	scalars := []any{
		nil, true, false, int64(-1 << 63), uint64(1<<64 - 1), 1e21, 1e20, 1e-7, 1e-6, 0.1, math.Copysign(0, -1),
		5e-324, math.MaxFloat64, float64(float32(0.1)), "", "<a & b>", "  ", "\x00\x01\x1f\x7f\t\n",
		"\xff\xfe x \xc3", `"\/`, "é€😀", bytesValue("\xff\x00"), bytesValue("text"), pyMark{}, "<recursion>",
	}
	keys := []string{"", "k", "<a & b>", " ", "\x00\x1f", "\xff", `"\`, "é€😀"}
	values = append(values, scalars...)
	values = append(values, []any(nil), []any{}, &orderedMap{}, []any{[]any{}, &orderedMap{}, []any{[]any{[]any{}}}}, []any{math.Inf(-1)})
	self := &orderedMap{}
	self.set("self", self)
	self.set("list", []any{self, []any{self}})
	shared := &orderedMap{keys: []string{"a"}, values: []any{int64(1)}}
	values = append(values, []any{shared, shared, &orderedMap{keys: []string{"x", "y"}, values: []any{shared, self}}})

	rng := rand.New(rand.NewPCG(1, 2))
	var random func(depth int) any
	random = func(depth int) any {
		switch n := rng.IntN(4); {
		case depth > 3 || n == 0:
			return scalars[rng.IntN(len(scalars))]
		case n == 1:
			list := make([]any, rng.IntN(4))
			for i := range list {
				list[i] = random(depth + 1)
			}
			return list
		default:
			m := &orderedMap{}
			for range rng.IntN(4) {
				m.set(keys[rng.IntN(len(keys))], random(depth+1))
			}
			return m
		}
	}
	for range 500 {
		values = append(values, random(0))
	}

	// Past MaxText: cut within characters of 2, 3 and 4 bytes, within
	// indentation, at the closing quote and at the last newline.
	for shift := range 4 {
		list := make([]any, 40_000)
		for i := range list {
			list[i] = strings.Repeat("x", shift) + strings.Repeat("é€😀", 12)
		}
		values = append(values, list)
	}
	deep := any(make([]any, 10_000))
	for range 300 {
		deep = []any{deep}
	}
	values = append(values, deep)
	for n := MaxText - 3; n <= MaxText; n++ {
		values = append(values, strings.Repeat("x", n))
	}

	for i, v := range values {
		want, wantErr := encodedWhole(v)
		got, err := toJSON(v)
		if (err == nil) != (wantErr == nil) {
			t.Errorf("value %d: error %v, want %v", i, err, wantErr)
		}
		if got != want {
			at := 0
			for at < min(len(got), len(want)) && got[at] == want[at] {
				at++
			}
			t.Errorf("value %d: differs at byte %d of %d (want %d):\n%q\nwant\n%q", i, at, len(got), len(want),
				got[max(at-40, 0):min(at+40, len(got))], want[max(at-40, 0):min(at+40, len(want))])
		}
	}
}

// Keys and names made of decoded values read as fmt.Sprint writes them,
// up to maxName, and past it are cut, their start kept.
func TestNamesMatchSprint(t *testing.T) {
	m := &orderedMap{}
	m.set("k", bytesValue("\xff"))
	m.set("<", []any{int64(1), &orderedMap{}, bytesValue("hi")})
	object := &pyObject{class: "a.B", args: []any{bytesValue("hi"), pyGlobal{"m", "n"}}, state: &pyDict{keys: []any{"k"}, values: []any{nil}}}
	values := []any{
		nil, true, int64(-5), uint64(1<<64 - 1), 1.5, 1e21, 1e-7, float64(float32(0.1)), math.Inf(1), math.Inf(-1), math.NaN(),
		math.Copysign(0, -1), "", "x", "a b", "\xff\xfe", "é€😀", bytesValue(""), bytesValue("hi"), bytesValue("\xff\x00"),
		[]any(nil), []any{}, []any{nil, "a b", []any{int64(1), bytesValue("\xff")}}, m, []any{m}, (*orderedMap)(nil),
		[]any{(*orderedMap)(nil)}, pyMark{}, pyGlobal{"mod", "name"}, []any{pyGlobal{"m", "n"}, pyMark{}},
		&pyList{items: []any{int64(1), bytesValue("x")}}, &pySet{}, &pyDict{keys: []any{[]any{int64(1)}}, values: []any{"v"}},
		object, []any{object}, "<class A>",
	}
	for _, h := range pickles {
		v, err := decodePickle(unhex(t, h))
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, v)
	}
	for _, b := range [][]byte{
		unhex(t, "aced0005 73 72 0005 506f696e74 0000000000000001 02 0002 49 0001 78 49 0001 79 78 70 00000001 00000002 74 0002 6869 71 007e0001"),
		unhex(t, "aced0005 73 72 0004 4e6f6465 0000000000000001 02 0001 4c 0004 73656c66 74 0006 4c4e6f64653b 78 70 71 007e0002"),
	} {
		v, err := decodeJava(b)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, v)
	}
	for _, b := range [][]byte{
		unhex(t, "86 a161 01 a162 92 c3 c0 a163 fd a166 cb3ff8000000000000 a3 62696e c4 02 00ff a174 d6 ff 00000001"),
		unhex(t, "83 a1ff a2fe80 a3e280a9 90 a0 80"),
	} {
		v, err := decodeMsgpack(b)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, v)
	}
	v, err := decodePHP([]byte(`O:8:"stdClass":2:{s:4:"name";s:3:"Ada";s:3:"age";a:2:{i:0;d:1.5;i:1;b:1;}}`))
	if err != nil {
		t.Fatal(err)
	}
	values = append(values, v)

	scalars := len(values)
	rng := rand.New(rand.NewPCG(3, 4))
	var random func(depth int) any
	random = func(depth int) any {
		items := func() []any {
			list := make([]any, rng.IntN(4))
			for i := range list {
				list[i] = random(depth + 1)
			}
			return list
		}
		switch n := rng.IntN(7); {
		case depth > 3 || n < 2:
			return values[rng.IntN(scalars)]
		case n == 2:
			return items()
		case n == 3:
			m := &orderedMap{}
			for _, item := range items() {
				m.set("k", item)
			}
			return m
		case n == 4:
			return &pyObject{class: "c", args: random(depth + 1), state: random(depth + 1)}
		case n == 5:
			return &pyDict{keys: items(), values: items()}
		default:
			return &pyList{items: items()}
		}
	}
	for range 500 {
		values = append(values, random(0))
	}

	for i, v := range values {
		var n names
		if got, want := n.name(v), fmt.Sprint(v); got != want {
			t.Errorf("value %d: %q, want %q", i, got, want)
		}
		if got, want := n.name("E", ".", v), fmt.Sprintf("%s.%v", "E", v); got != want {
			t.Errorf("value %d after a name: %q, want %q", i, got, want)
		}
	}

	// Past maxName, the start kept, cut between characters.
	doubled := any("é")
	for range 40 {
		doubled = []any{doubled, doubled}
	}
	long := []any{strings.Repeat("é", maxName), bytesValue(strings.Repeat("x", 2*maxName)), bytesValue(strings.Repeat("\xff", maxName)), doubled}
	for i, v := range long {
		var n names
		got := n.name(v)
		start, cut := strings.CutSuffix(got, "…")
		if !cut || len(start) > maxName || len(start) < maxName-utf8.UTFMax || !utf8.ValidString(start) {
			t.Errorf("%d bytes not cut at maxName: %q", len(got), got[max(len(got)-20, 0):])
		}
		// fmt.Sprint would write 2^40 items of the doubled one.
		if i < len(long)-1 && !strings.HasPrefix(fmt.Sprint(v), start) {
			t.Errorf("%T cut to text it does not start with", v)
		}
	}
	// Past maxNames together, each is "…".
	var n names
	for range maxNames/maxName + 1 {
		n.name(doubled)
	}
	if got := n.name(int64(1)); got != "…" {
		t.Errorf("past maxNames: %q", got)
	}
	if got := n.keyString("a key"); got != "a key" {
		t.Errorf("a string key past maxNames: %q", got)
	}
}

package decode

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func varint(n int) []byte {
	var b []byte
	for n >= 0x80 {
		b = append(b, byte(n)|0x80)
		n >>= 7
	}
	return append(b, byte(n))
}

// TestHostileValues decodes values made to crash, stall or exhaust the
// app: each must end, soon, with an error or a value.
func TestHostileValues(t *testing.T) {
	hostile := map[string]func() (Format, []byte){
		"a Java enum without a class": func() (Format, []byte) { return "", unhex(t, "aced0005 7e 70 74 0001 41") },
		"a Java class its own superclass": func() (Format, []byte) {
			return "", unhex(t, "aced0005 73 72 0001 41 0000000000000001 02 0000 78 71 007e0000")
		},
		"Java's resets by the million": func() (Format, []byte) {
			return "", append(append(unhex(t, "aced0005"), bytes.Repeat([]byte{0x79}, 2_000_000)...), 0x70)
		},
		"a chain of Java superclasses": func() (Format, []byte) {
			b := unhex(t, "aced0005 73")
			for range 200_000 {
				b = append(b, unhex(t, "72 0000 0000000000000001 02 0000 78")...)
			}
			return "", append(b, 0x70)
		},
		"Java arrays claiming the rest": func() (Format, []byte) {
			const size = 1 << 20
			b := unhex(t, "aced0005")
			for k := range 1000 {
				if k == 0 {
					b = append(b, unhex(t, "75 72 0002 5b4c 0000000000000001 02 0000 78 70")...)
				} else {
					b = append(b, unhex(t, "75 71 007e0000")...)
				}
				c := size - len(b) - 4
				b = append(b, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
			}
			return "", append(b, bytes.Repeat([]byte{0x70}, size-len(b))...)
		},
		"MessagePack arrays claiming the rest": func() (Format, []byte) {
			const size = 1 << 20
			b := make([]byte, size)
			for k := range 1000 {
				c := size - 5*(k+1)
				copy(b[5*k:], []byte{0xdd, byte(c >> 24), byte(c >> 16), byte(c >> 8), byte(c)})
			}
			return MessagePack, b
		},
		"a timestamp past year 9999": func() (Format, []byte) { return MessagePack, unhex(t, "c7 0c ff 00000000 7fffffffffffffff") },
		"Protobuf groups opened by the million": func() (Format, []byte) {
			return Protobuf, bytes.Repeat([]byte{0x0b}, 2_000_000)
		},
		"Protobuf text of 200 KB": func() (Format, []byte) {
			s := bytes.Repeat([]byte("é"), 100_000)
			return Protobuf, append(append([]byte{0x0a}, varint(len(s))...), s...)
		},
		"Protobuf nested 999 deep around 4 MB": func() (Format, []byte) {
			inner := bytes.Repeat([]byte{0x08, 0x00}, 2_000_000)
			for range 999 {
				inner = append(append([]byte{0x0a}, varint(len(inner))...), inner...)
			}
			return Protobuf, inner
		},
		"a PHP array of 200,000": func() (Format, []byte) {
			var b bytes.Buffer
			fmt.Fprintf(&b, "a:200000:{")
			b.WriteString(strings.Repeat("i:0;N;", 200_000))
			b.WriteString("}")
			return "", b.Bytes()
		},
		"PHP nested 999 deep around 300,000": func() (Format, []byte) {
			var b bytes.Buffer
			b.WriteString(strings.Repeat("a:1:{i:0;", 999))
			fmt.Fprintf(&b, "a:300000:{")
			for i := range 300_000 {
				fmt.Fprintf(&b, "i:%d;N;", i)
			}
			b.WriteString(strings.Repeat("}", 1000))
			return "", b.Bytes()
		},
		"a pickled number of 2,000,000 digits": func() (Format, []byte) {
			return "", append(append([]byte("\x80\x02L"), bytes.Repeat([]byte("9"), 2_000_000)...), "L\n."...)
		},
	}
	for name, make := range hostile {
		f, b := make()
		start := time.Now()
		var d Decoded
		var err error
		if f == "" {
			d, err = Auto(b)
		} else {
			d, err = As(f, b)
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("%s: took %v", name, took)
		}
		if len(d.Text) > MaxText+100 {
			t.Errorf("%s: %d bytes of text", name, len(d.Text))
		}
		t.Logf("%s: %d bytes of text, %v", name, len(d.Text), err)
	}
	for _, s := range []string{"x = 5", "x?y", "x}"} {
		if f := Detect([]byte(s)); f != "" {
			t.Errorf("%q detected as %s", s, f)
		}
	}
}

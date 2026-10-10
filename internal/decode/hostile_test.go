package decode

import (
	"bytes"
	"fmt"
	"runtime"
	"slices"
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

// pickledMebibyte starts a pickle with a string of 1 MiB, kept in the
// memo as 0.
func pickledMebibyte() []byte {
	b := []byte("\x80\x02X\x00\x00\x10\x00")
	b = append(b, bytes.Repeat([]byte("x"), 1<<20)...)
	return append(b, 'q', 0)
}

// pickledTuples pickles a string of 1 MiB, then levels of tuples each
// holding the one before twice, the last kept in the memo as levels.
func pickledTuples(levels int) []byte {
	b := pickledMebibyte()
	for i := 1; i <= levels; i++ {
		b = append(b, 'h', byte(i-1), 'h', byte(i-1), 0x86, 'q', byte(i))
	}
	return b
}

// javaArrays writes an array of a string of 1 MiB, then arrays each
// holding the one before twice, levels in all, each at the top: the
// arrays' class is handle 0, the first array 1, its string 2, and the
// last array baseHandle+levels+1.
func javaArrays(t *testing.T, levels int) []byte {
	b := unhex(t, "aced0005 75 72 0002 5b4c 0000000000000001 02 0000 78 70 00000002 7c 0000000000100000")
	b = append(b, bytes.Repeat([]byte("x"), 1<<20)...)
	b = append(b, unhex(t, "71 007e0002")...)
	for i := 2; i <= levels; i++ {
		before := baseHandle + i
		if i == 2 {
			before = baseHandle + 1
		}
		reference := []byte{0x71, byte(before >> 24), byte(before >> 16), byte(before >> 8), byte(before)}
		b = append(append(append(b, unhex(t, "75 71 007e0000 00000002")...), reference...), reference...)
	}
	return b
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
		// 2^18 copies of a string of 1 MiB: 256 GiB of text.
		"pickled tuples each holding the one before twice": func() (Format, []byte) {
			return "", append(pickledTuples(18), '.')
		},
		"pickled dicts each holding the one before twice": func() (Format, []byte) {
			b := pickledMebibyte()
			for i := 1; i <= 17; i++ {
				b = append(b, '}', '(', 0x8c, 1, 'a', 'h', byte(i-1), 0x8c, 1, 'b', 'h', byte(i-1), 'u', 'q', byte(i))
			}
			return "", append(b, '.')
		},
		"Java arrays each holding the one before twice": func() (Format, []byte) {
			return "", javaArrays(t, 17)
		},
		// Keys and names made of such values.
		"a pickled dict keyed by tuples each holding the one before twice": func() (Format, []byte) {
			return "", append(pickledTuples(18), '}', 'h', 18, 'K', 1, 's', '.')
		},
		"a pickled global named by tuples each holding the one before twice": func() (Format, []byte) {
			return "", append(pickledTuples(18), 'h', 18, 'h', 18, 0x93, '.')
		},
		"a pickled object of a class named by tuples each holding the one before twice": func() (Format, []byte) {
			return "", append(pickledTuples(18), 'h', 18, ')', 'R', '.')
		},
		"a pickled dict keyed by a tuple of 1 MiB, shown 2^17 times": func() (Format, []byte) {
			b := append(pickledMebibyte(), 0x85, 'q', 1, '}', 'h', 1, 'N', 's', 'q', 2)
			for i := 3; i <= 19; i++ {
				b = append(b, ']', '(', 'h', byte(i-1), 'h', byte(i-1), 'e', 'q', byte(i))
			}
			return "", append(b, '.')
		},
		"a Java enum named by arrays each holding the one before twice": func() (Format, []byte) {
			return "", append(javaArrays(t, 17), unhex(t, "7e 72 0001 45 0000000000000001 02 0000 78 70 71 007e0012")...)
		},
		"a Java field typed by arrays each holding the one before twice": func() (Format, []byte) {
			return "", append(javaArrays(t, 17), unhex(t, "73 72 0001 41 0000000000000001 02 0001 4c 0001 66 71 007e0012 78 70 70")...)
		},
		"a pickled class named by 1 MiB, reduced 1,000 times": func() (Format, []byte) {
			b := append([]byte("\x80\x02(cm\n"), bytes.Repeat([]byte("x"), 1<<20)...)
			b = append(b, "\nq\x00"...)
			for range 1000 {
				b = append(b, "h\x00)R"...)
			}
			return "", append(b, 'l', '.')
		},
		"a pickled global named by 1 MiB, shown 1,000 times": func() (Format, []byte) {
			b := append([]byte("\x80\x02(cm\n"), bytes.Repeat([]byte("x"), 1<<20)...)
			b = append(b, "\nq\x00"...)
			for range 999 {
				b = append(b, 'h', 0)
			}
			return "", append(b, 'l', '.')
		},
		"pickled bytes of 1 MiB, encoded 1,000 times": func() (Format, []byte) {
			b := append([]byte("\x80\x02(c_codecs\nencode\nq\x00X\x00\x00\x10\x00"), bytes.Repeat([]byte("x"), 1<<20)...)
			b = append(b, 'q', 1)
			for range 1000 {
				b = append(b, "h\x00h\x01X\x06\x00\x00\x00latin1\x86R"...)
			}
			return "", append(b, 'l', '.')
		},
		"a Java class named by 64 KiB, referred to 16,000 times": func() (Format, []byte) {
			b := append(unhex(t, "aced0005 72 ffff"), bytes.Repeat([]byte("x"), 0xffff)...)
			b = append(b, unhex(t, "0000000000000001 02 0000 78 70")...)
			for range 16_000 {
				b = append(b, unhex(t, "71 007e0000")...)
			}
			return "", b
		},
	}
	// Values made far larger than they are take less than 128 MiB, whether
	// they are shown or refused.
	bounded := []string{
		"a pickled class named by 1 MiB, reduced 1,000 times",
		"a pickled global named by 1 MiB, shown 1,000 times",
		"pickled bytes of 1 MiB, encoded 1,000 times",
		"a Java class named by 64 KiB, referred to 16,000 times",
	}
	// Values shown far larger than they are take less than 128 MiB, and
	// those marked are shown past MaxText, cut there.
	large := map[string]bool{
		"PHP nested 999 deep around 300,000":                                            true,
		"pickled tuples each holding the one before twice":                              true,
		"pickled dicts each holding the one before twice":                               true,
		"Java arrays each holding the one before twice":                                 true,
		"a pickled dict keyed by tuples each holding the one before twice":              false,
		"a pickled global named by tuples each holding the one before twice":            false,
		"a pickled object of a class named by tuples each holding the one before twice": false,
		"a pickled dict keyed by a tuple of 1 MiB, shown 2^17 times":                    true,
		"a Java enum named by arrays each holding the one before twice":                 true,
		"a Java field typed by arrays each holding the one before twice":                true,
	}
	for name, make := range hostile {
		t.Run(name, func(t *testing.T) {
			f, b := make()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			var d Decoded
			var err error
			if f == "" {
				d, err = Auto(b)
			} else {
				d, err = As(f, b)
			}
			took := time.Since(start)
			runtime.ReadMemStats(&after)
			allocated := after.TotalAlloc - before.TotalAlloc
			if took > 3*time.Second {
				t.Errorf("%s: took %v", name, took)
			}
			if len(d.Text) > MaxText+100 {
				t.Errorf("%s: %d bytes of text", name, len(d.Text))
			}
			if cut, ok := large[name]; ok {
				if err != nil || cut && !strings.HasSuffix(d.Text, fmt.Sprintf("… cut at %d MB", MaxText>>20)) {
					t.Errorf("%s: not cut at MaxText: %v", name, err)
				}
				if allocated > 128<<20 {
					t.Errorf("%s: %d MiB allocated", name, allocated>>20)
				}
			}
			if slices.Contains(bounded, name) && allocated > 128<<20 {
				t.Errorf("%s: %d MiB allocated", name, allocated>>20)
			}
			t.Logf("%s: %d bytes of text, %d MiB allocated, %v", name, len(d.Text), allocated>>20, err)
		})
	}
	for _, s := range []string{"x = 5", "x?y", "x}"} {
		if f := Detect([]byte(s)); f != "" {
			t.Errorf("%q detected as %s", s, f)
		}
	}
}

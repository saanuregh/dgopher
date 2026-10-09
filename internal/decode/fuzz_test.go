package decode

import (
	"encoding/hex"
	"testing"
	"time"
)

// FuzzDecode decodes any bytes as every format: values come from servers
// and their clients, so none may crash the app.
func FuzzDecode(f *testing.F) {
	for _, h := range pickles {
		b, _ := hex.DecodeString(h)
		f.Add(b)
	}
	for _, s := range []string{
		"\xac\xed\x00\x05t\x00\x02hi", `a:1:{i:0;s:1:"x";}`, "\x86\xa1a\x01", "\x08\x96\x01\x12\x02hi",
		"\x1f\x8b", "\x28\xb5\x2f\xfd", "\x04\x22\x4d\x18", "\xff\x06\x00\x00sNaPpY",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, format := range append([]Format{""}, Formats...) {
			start := time.Now()
			if format == "" {
				Auto(b)
			} else {
				As(format, b)
			}
			// A value the app shows must not hold it up.
			if took := time.Since(start); took > time.Second {
				t.Fatalf("%q took %v as %q", b[:min(len(b), 64)], took, format)
			}
		}
	})
}

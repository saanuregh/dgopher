package redis

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// stringKey answers the commands that read a string key holding value.
func stringKey(key, value string, ttlMS int64) func(args []string) any {
	return func(args []string) any {
		switch strings.ToUpper(args[0]) {
		case "SCAN":
			return []any{"0", []string{key}}
		case "TYPE":
			return fakeStatus("string")
		case "PTTL":
			return ttlMS
		case "STRLEN":
			return int64(len(value))
		case "GETRANGE", "GET":
			return value
		case "SET", "EXPIRE", "PERSIST", "EVAL":
			return fakeStatus("OK")
		}
		return unhandled{}
	}
}

// A string that is not UTF-8 shows as hex, read only: typing in it
// cannot save the value with its bytes replaced.
func TestBinaryStringIsNotEditedRaw(t *testing.T) {
	value := "\xff\xfe\x00bin\x80"
	f := newFakeRedis(t, stringKey("blob", value, -1))
	_, tt, _, r := openFake(t, f, db.Development)
	r.open("blob")
	testutil.WaitFor(t, tt, "the value", func() bool { return !r.loadingKey && r.info.Type == "string" })
	if err := tt.Click("Value"); err == nil {
		tt.Type("x")
		tt.Frame()
	}
	if tt.HasText("Save") {
		tt.Key(ui.Ctrl, ui.KeyS)
		testutil.WaitFor(t, tt, "the write", func() bool { return len(f.SentNamed("SET")) > 0 })
	}
	for _, set := range f.SentNamed("SET") {
		if !utf8.ValidString(set[2]) || strings.ContainsRune(set[2], utf8.RuneError) {
			t.Fatalf("SET wrote %q over the binary %q", set[2], value)
		}
	}
	if !tt.HasText("Not UTF-8: shown as hex, read only.") {
		t.Fatalf("no note that the value is binary: %v", slices.Compact(tt.Texts()))
	}
}

// Saving a string keeps its time to live: by KEEPTTL from Redis 6, by a
// script that sets it again before.
func TestSaveStringKeepsTTL(t *testing.T) {
	for _, c := range []struct {
		version string
		sent    string
	}{
		{"7.4.0", "SET"},
		{"5.0.14", "EVAL"},
	} {
		f := newFakeRedis(t, stringKey("greeting", "hello", 90_000))
		f.version = c.version
		a, tt, _, r := openFake(t, f, db.Development)
		r.open("greeting")
		testutil.WaitFor(t, tt, "the value", func() bool { return !r.loadingKey && r.info.Type == "string" })
		r.saveString("greeting", "hi")
		testutil.WaitFor(t, tt, "the write", func() bool { return len(f.SentNamed(c.sent)) > 0 || len(a.Errors) > 0 })
		if len(a.Errors) > 0 {
			t.Fatalf("Redis %s: %v", c.version, a.Errors)
		}
		args := f.SentNamed(c.sent)[0]
		switch c.sent {
		case "SET":
			if !slices.Equal(args, []string{"SET", "greeting", "hi", "KEEPTTL"}) {
				t.Fatalf("Redis %s: sent %q", c.version, args)
			}
		case "EVAL":
			if !strings.Contains(args[1], "PX") || !slices.Equal(args[2:], []string{"1", "greeting", "hi"}) {
				t.Fatalf("Redis %s: sent %q", c.version, args)
			}
		}
	}
}

// Load All asks first for a string larger than 10 MiB, and reads a
// smaller one at once.
func TestLoadAllAsksAboveTenMiB(t *testing.T) {
	big := strings.Repeat("x", stringStart+1)
	f := newFakeRedis(t, func(args []string) any {
		if strings.EqualFold(args[0], "STRLEN") {
			return int64(11 << 20)
		}
		return stringKey("big", big, -1)(args)
	})
	a, tt, _, r := openFake(t, f, db.Development)
	r.open("big")
	testutil.WaitFor(t, tt, "the value", func() bool { return !r.loadingKey && r.info.Type == "string" })
	if err := tt.Click("Load All"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Confirm == nil || len(f.SentNamed("GET")) > 0 {
		t.Fatalf("an 11 MiB string loads without asking: confirm %+v", a.Confirm)
	}
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the whole string", func() bool { return r.whole })
}

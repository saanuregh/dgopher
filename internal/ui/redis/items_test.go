package redis

import (
	"errors"
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// collections answers reads of a list "queue" and a hash "user", and
// takes writes; hpttl answers HPTTL.
func collections(hpttl any) func(args []string) any {
	return func(args []string) any {
		switch strings.ToUpper(args[0]) {
		case "SCAN":
			return []any{"0", []string{"queue", "user"}}
		case "TYPE":
			if args[1] == "queue" {
				return fakeStatus("list")
			}
			return fakeStatus("hash")
		case "PTTL":
			return int64(-1)
		case "LLEN", "HLEN":
			return int64(2)
		case "LRANGE":
			return []string{"a", "b"}
		case "HSCAN":
			return []any{"0", []string{"f1", "v1", "f2", "v2"}}
		case "HPTTL":
			return hpttl
		case "EVAL", "HSET", "HDEL", "LSET", "EXPIRE", "PERSIST":
			return int64(1)
		}
		return unhandled{}
	}
}

func openKey(t *testing.T, tt *ui.Tester, r *Tab, key string) {
	t.Helper()
	r.open(key)
	testutil.WaitFor(t, tt, key, func() bool { return !r.loadingKey && r.info.Type != "" && (len(r.fields) > 0 || r.keyErr != "") })
}

// Replace Chosen changes the list's item only while it still holds the
// value shown, as Remove Chosen does.
func TestReplaceChosenChecksTheValue(t *testing.T) {
	f := newFakeRedis(t, collections(nil))
	a, tt, _, r := openFake(t, f, db.Development)
	openKey(t, tt, r, "queue")
	r.fieldRow, r.newValue = 0, "z"
	tt.Frame()
	if err := tt.Click("Replace Chosen"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the write", func() bool { return len(f.SentNamed("EVAL"))+len(f.SentNamed("LSET")) > 0 || len(a.Errors) > 0 })
	if len(f.SentNamed("LSET")) > 0 {
		t.Fatalf("LSET by index alone: %q", f.SentNamed("LSET"))
	}
	eval := f.SentNamed("EVAL")[0]
	if !strings.Contains(eval[1], "LINDEX") || strings.Join(eval[2:], " ") != "1 queue 0 a z" {
		t.Fatalf("sent %q", eval)
	}
}

// A hash whose fields' times to live cannot be read still shows its
// fields, without them.
func TestHashShowsFieldsWhenTTLsFail(t *testing.T) {
	f := newFakeRedis(t, collections(errors.New("NOPERM this user has no permissions to run the 'hpttl' command")))
	f.readOnly = []string{"HPTTL"}
	_, tt, _, r := openFake(t, f, db.Development)
	openKey(t, tt, r, "user")
	if len(r.fields) != 2 || r.keyErr != "" {
		t.Fatalf("fields %+v, error %q", r.fields, r.keyErr)
	}
}

// Deleting a key unlinks it, which frees its memory aside; a server
// without UNLINK deletes it with DEL.
func TestDeleteKeyUnlinks(t *testing.T) {
	for _, unlink := range []bool{true, false} {
		f := newFakeRedis(t, func(args []string) any {
			switch strings.ToUpper(args[0]) {
			case "UNLINK":
				if !unlink {
					return errors.New("ERR unknown command 'UNLINK'")
				}
				return int64(1)
			case "DEL":
				return int64(1)
			}
			return stringKey("k", "v", -1)(args)
		})
		a, tt, _, r := openFake(t, f, db.Development)
		testutil.WaitFor(t, tt, "the keys", func() bool { return len(r.keys) == 1 })
		r.selected = "k"
		r.askDelete("k")
		if a.Discard == nil {
			t.Fatal("delete not asked")
		}
		a.Discard()
		testutil.WaitFor(t, tt, "the deletion", func() bool { return len(r.keys) == 0 || len(a.Errors) > 0 })
		if len(a.Errors) > 0 || len(f.SentNamed("UNLINK")) != 1 || len(f.SentNamed("DEL")) != map[bool]int{true: 0, false: 1}[unlink] {
			t.Fatalf("UNLINK known %v: errors %v, sent %q", unlink, a.Errors, f.Sent())
		}
	}
}

// A TTL that is not a whole number of seconds says so; a TTL under a
// second shows as 1, not 0.
func TestTTLInput(t *testing.T) {
	f := newFakeRedis(t, stringKey("k", "v", 400))
	_, tt, _, r := openFake(t, f, db.Development)
	r.open("k")
	testutil.WaitFor(t, tt, "the value", func() bool { return !r.loadingKey && r.info.Type == "string" })
	if r.ttlIn != "1" {
		t.Fatalf("a 400 ms TTL shows as %q", r.ttlIn)
	}
	r.ttlIn = "soon"
	tt.Frame()
	if err := tt.Click("Set TTL"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("TTL is whole seconds: empty removes it.") {
		t.Fatalf("no error for the TTL %q", r.ttlIn)
	}
	if len(f.SentNamed("EXPIRE"))+len(f.SentNamed("PERSIST")) > 0 {
		t.Fatalf("sent %q", f.Sent())
	}
}

// Removing an item, and setting a hash field that exists, ask first on
// every environment; setting a new field does not.
func TestItemChangesAsk(t *testing.T) {
	for _, env := range []db.Environment{db.Development, db.Production} {
		f := newFakeRedis(t, collections(nil))
		a, tt, _, r := openFake(t, f, env)
		openKey(t, tt, r, "user")
		r.fieldRow = 0
		tt.Frame()
		if err := tt.Click("Remove Chosen"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if a.Confirm == nil || len(f.SentNamed("HDEL")) > 0 {
			t.Fatalf("%s: Remove Chosen does not ask: %+v", env, a.Confirm)
		}
		a.Confirm.OnConfirm()
		a.Confirm = nil
		testutil.WaitFor(t, tt, "HDEL", func() bool { return len(f.SentNamed("HDEL")) == 1 })

		for _, c := range []struct {
			field string
			asks  bool
		}{{"f2", true}, {"new", env == db.Production}} {
			sets := len(f.SentNamed("HSET"))
			r.newName, r.newValue = c.field, "x"
			tt.Frame()
			if err := tt.Click("Set"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if (a.Confirm != nil) != c.asks {
				t.Fatalf("%s: setting the field %s asks %v", env, c.field, a.Confirm != nil)
			}
			if a.Confirm != nil {
				a.Confirm.OnConfirm()
				a.Confirm = nil
			}
			testutil.WaitFor(t, tt, "HSET "+c.field, func() bool { return len(f.SentNamed("HSET")) > sets && r.newName == "" })
		}
		testutil.WaitFor(t, tt, "HSET", func() bool { return len(f.SentNamed("HSET")) == 2 || len(a.Errors) > 0 })
		if len(a.Errors) > 0 || len(f.SentNamed("HSET")) != 2 {
			t.Fatalf("%s: errors %v, sent %q", env, a.Errors, f.Sent())
		}
	}
}

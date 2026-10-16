package sqltext

import "testing"

func checkSignatures(t *testing.T, d Dialect, min int, want map[string]bool) {
	t.Helper()
	sigs := BuiltinSignatures(d)
	if len(sigs) < min {
		t.Errorf("%d signatures, want at least %d", len(sigs), min)
	}
	got := map[string]Signature{}
	for _, s := range sigs {
		if s.Name == "" || s.Description == "" {
			t.Errorf("incomplete: %+v", s)
		}
		got[s.Name] = s
	}
	for name, aggregate := range want {
		s, ok := got[name]
		if !ok {
			t.Errorf("%s missing", name)
		} else if s.Aggregate != aggregate {
			t.Errorf("%s aggregate %v", name, s.Aggregate)
		}
	}
	t.Logf("%d signatures", len(sigs))
}

func TestBuiltinSignaturesSQLite(t *testing.T) {
	checkSignatures(t, SQLite, 60, map[string]bool{"substr": false, "count": true, "strftime": false, "json_extract": false, "group_concat": true})
}

func TestBuiltinSignaturesMySQL(t *testing.T) {
	checkSignatures(t, MySQL, 90, map[string]bool{"concat": false, "date_format": false, "json_extract": false, "count": true, "if": false})
	if BuiltinSignatures(Postgres) != nil || BuiltinSignatures(Generic) != nil {
		t.Error("a table for an engine with a catalog")
	}
}

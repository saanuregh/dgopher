package connection

import (
	"testing"

	"dgopher/internal/db"
)

// A connection whose dialect reads no signatures gets its dialect's table.
func TestLoadSignaturesFallback(t *testing.T) {
	cn := &Conn{Config: db.Config{Name: "lite", Engine: db.SQLite}}
	cn.Reset()
	var got map[string][]db.Signature
	LoadSignatures(runNow{}, cn, func(m map[string][]db.Signature) { got = m })
	if len(got["substr"]) == 0 || len(got["count"]) == 0 || got["count"][0].Name == "" {
		t.Fatalf("got %d names", len(got))
	}
	if cn.Signatures == nil || len(cn.Loading) != 0 || len(cn.LoadErr) != 0 {
		t.Fatalf("cache %d, loading %v, errors %v", len(cn.Signatures), cn.Loading, cn.LoadErr)
	}
	calls := 0
	LoadSignatures(runNow{}, cn, func(map[string][]db.Signature) { calls++ })
	if calls != 1 {
		t.Fatal("cached signatures not given")
	}
	cn.Reset()
	if cn.Signatures != nil {
		t.Fatal("reset keeps signatures")
	}
}

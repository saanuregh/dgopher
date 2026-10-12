package dataview

import (
	"maps"
	"testing"

	"dgopher/internal/params"
)

// Each connection keeps its own values; an empty one is forgotten
// unless a kind was chosen for it.
func TestParamValuesPerConnection(t *testing.T) {
	p := NewFakeHost(t).Project
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(SaveParamValue(p, "prod", ":id", params.Input{Value: "42", Kind: "number"}))
	must(SaveParamValue(p, "prod", ":gone", params.Input{Kind: "null"}))
	must(SaveParamValue(p, "staging", ":id", params.Input{Value: "7", Kind: "auto"}))
	expect := func(conn string, want map[string]params.Input) {
		t.Helper()
		if got := ParamValues(p, conn); !maps.Equal(got, want) {
			t.Fatalf("%s: %v, want %v", conn, got, want)
		}
	}
	expect("prod", map[string]params.Input{":id": {Value: "42", Kind: "number"}, ":gone": {Kind: "null"}})
	expect("staging", map[string]params.Input{":id": {Value: "7", Kind: "auto"}})
	must(SaveParamValue(p, "prod", ":id", params.Input{Kind: "auto"}))
	must(SaveParamValue(p, "prod", ":n", params.Input{Kind: "number"}))
	expect("prod", map[string]params.Input{":gone": {Kind: "null"}, ":n": {Kind: "number"}})
}

// A connection's values follow its new ID.
func TestRenameConnectionMovesParams(t *testing.T) {
	p := NewFakeHost(t).Project
	if err := SaveParamValue(p, "duck", "${table}", params.Input{Value: "orders", Kind: "auto"}); err != nil {
		t.Fatal(err)
	}
	if err := RenameConnection(p, "duck", "duck2"); err != nil {
		t.Fatal(err)
	}
	if len(ParamValues(p, "duck")) != 0 || ParamValues(p, "duck2")["${table}"].Value != "orders" {
		t.Fatalf("after the new ID: %v, %v", ParamValues(p, "duck"), ParamValues(p, "duck2"))
	}
}
